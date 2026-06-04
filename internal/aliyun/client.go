/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aliyun

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cas"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cdn"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/go-logr/logr"
)

// Client implements the AliCloudClient interface for Aliyun operations
type Client struct {
	accessKeyID     string
	accessKeySecret string
	region          string
	logger          logr.Logger
}

// NewClient creates a new Aliyun client
func NewClient(accessKeyID, accessKeySecret, region string, logger logr.Logger) *Client {
	return &Client{
		accessKeyID:     accessKeyID,
		accessKeySecret: accessKeySecret,
		region:          region,
		logger:          logger,
	}
}

// UploadCertificate uploads certificate to Aliyun SSL certificate service (CAS)
func (c *Client) UploadCertificate(ctx context.Context, certPEM, keyPEM, domain string) (string, error) {
	c.logger.Info("Uploading certificate to Aliyun SSL certificate service", "domain", domain)

	// Create CAS client
	config := sdk.NewConfig()
	config.Timeout = 30 * time.Second
	cred := credentials.NewAccessKeyCredential(c.accessKeyID, c.accessKeySecret)

	client, err := cas.NewClientWithOptions(c.region, config, cred)
	if err != nil {
		return "", fmt.Errorf("failed to create CAS client: %w", err)
	}

	// Prepare request
	// Note: The actual API call may vary depending on Aliyun CAS service
	// This is a simplified example
	request := cas.CreateUploadUserCertificateRequest()
	request.Scheme = "https"
	request.Cert = certPEM
	request.Key = keyPEM
	request.Name = fmt.Sprintf("cert-%s-%d", strings.ReplaceAll(domain, ".", "-"), time.Now().Unix())

	// Send request
	response, err := client.UploadUserCertificate(request)
	if err != nil {
		return "", fmt.Errorf("failed to upload certificate to CAS: %w", err)
	}

	certIdStr := fmt.Sprintf("%d", response.CertId)
	c.logger.Info("Certificate uploaded successfully", "certificateId", certIdStr)
	return certIdStr, nil
}

// UpdateOSSDomainCertificate updates OSS domain certificate configuration.
// It binds the domain to the bucket (if not already bound) and sets the SSL certificate
// for it using the certificate stored in Aliyun CAS.
func (c *Client) UpdateOSSDomainCertificate(ctx context.Context, bucketName, domain, certificateId string) error {
	c.logger.Info("Updating OSS domain certificate", "bucket", bucketName, "domain", domain, "certificateId", certificateId)

	// Create OSS client
	ossClient, err := oss.New(fmt.Sprintf("https://oss-%s.aliyuncs.com", c.region), c.accessKeyID, c.accessKeySecret)
	if err != nil {
		return fmt.Errorf("failed to create OSS client: %w", err)
	}

	// 1. Bind domain to bucket if not already bound
	c.logger.Info("Binding domain to bucket", "domain", domain)
	err = ossClient.PutBucketCname(bucketName, domain)
	if err != nil {
		if strings.Contains(err.Error(), "DomainAlreadyExists") || strings.Contains(err.Error(), "already exists") {
			c.logger.Info("Domain already bound to bucket", "domain", domain)
		} else {
			c.logger.Info("Domain binding result", "domain", domain, "error", err.Error())
		}
	} else {
		c.logger.Info("Domain bound to bucket successfully", "domain", domain)
	}

	// 2. Set SSL certificate for the CNAME domain via OSS REST API
	if err := c.setOSSCnameCertificate(ctx, bucketName, domain, certificateId); err != nil {
		return fmt.Errorf("failed to set OSS CNAME certificate: %w", err)
	}

	c.logger.Info("OSS domain certificate configured successfully",
		"bucket", bucketName, "domain", domain, "certificateId", certificateId)

	return nil
}

// setOSSCnameCertificate sets the SSL certificate for an OSS bucket CNAME domain
// using the OSS REST API (POST /?cname&comp=add).
func (c *Client) setOSSCnameCertificate(ctx context.Context, bucketName, domain, certificateId string) error {
	endpoint := fmt.Sprintf("%s.oss-%s.aliyuncs.com", bucketName, c.region)
	url := fmt.Sprintf("https://%s/?cname&comp=add", endpoint)

	body := fmt.Sprintf(`<BucketCnameConfiguration>
  <Cname>
    <Domain>%s</Domain>
    <CertificateConfiguration>
      <CertId>%s</CertId>
      <Force>true</Force>
    </CertificateConfiguration>
  </Cname>
</BucketCnameConfiguration>`, domain, certificateId)

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Date", date)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	// OSS Signature V1
	signature := c.ossSignature("POST", body, date, req.Header, "/"+bucketName+"/?cname&comp=add")
	req.Header.Set("Authorization", "OSS "+c.accessKeyID+":"+signature)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OSS CNAME cert request failed: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ossSignature computes OSS Signature V1.
func (c *Client) ossSignature(method, bodyStr, date string, headers http.Header, canonicalizedResource string) string {
	contentMD5 := ""
	contentType := headers.Get("Content-Type")

	// Canonicalized OSS Headers (sorted, lowercase)
	var ossHeaders []string
	for k, vs := range headers {
		kl := strings.ToLower(k)
		if strings.HasPrefix(kl, "x-oss-") {
			ossHeaders = append(ossHeaders, kl+":"+strings.TrimSpace(vs[0])+"\n")
		}
	}
	canonicalizedHeaders := strings.Join(ossHeaders, "")

	stringToSign := method + "\n" +
		contentMD5 + "\n" +
		contentType + "\n" +
		date + "\n" +
		canonicalizedHeaders +
		canonicalizedResource

	mac := hmac.New(sha1.New, []byte(c.accessKeySecret))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// FindCertificateByFingerprint searches for an existing certificate in CAS by SHA256 fingerprint
// Returns the certificate ID if found, empty string if not found
func (c *Client) FindCertificateByFingerprint(ctx context.Context, certPEM string) (string, error) {
	c.logger.Info("Searching for existing certificate by fingerprint")

	config := sdk.NewConfig()
	config.Timeout = 30 * time.Second
	cred := credentials.NewAccessKeyCredential(c.accessKeyID, c.accessKeySecret)

	client, err := cas.NewClientWithOptions(c.region, config, cred)
	if err != nil {
		return "", fmt.Errorf("failed to create CAS client: %w", err)
	}

	// Calculate the fingerprint of the certificate to upload
	targetFingerprint := calculateCertFingerprint(certPEM)
	c.logger.Info("Target certificate fingerprint", "fingerprint", targetFingerprint)

	// List all user uploaded certificates using ListUserCertificateOrder API
	// OrderType=UPLOAD means list user uploaded certificates
	request := cas.CreateListUserCertificateOrderRequest()
	request.Scheme = "https"
	request.ShowSize = requests.NewInteger(100)
	request.CurrentPage = requests.NewInteger(1)
	request.OrderType = "UPLOAD" // Only list user uploaded certificates

	response, err := client.ListUserCertificateOrder(request)
	if err != nil {
		return "", fmt.Errorf("failed to list certificates: %w", err)
	}

	c.logger.Info("Listed certificates from Aliyun", "count", len(response.CertificateOrderList))

	// Search through certificates to find matching fingerprint
	for _, cert := range response.CertificateOrderList {
		c.logger.Info("Checking certificate", "certificateId", cert.CertificateId, "commonName", cert.CommonName, "fingerprint", cert.Fingerprint)

		// Compare fingerprints
		// Note: Aliyun returns fingerprint in different format, we also compare by cert content
		if cert.CertificateId > 0 {
			// Get certificate details to compare content
			detailReq := cas.CreateGetUserCertificateDetailRequest()
			detailReq.Scheme = "https"
			detailReq.CertId = requests.Integer(fmt.Sprintf("%d", cert.CertificateId))

			detailResp, err := client.GetUserCertificateDetail(detailReq)
			if err != nil {
				c.logger.Info("Failed to get certificate detail, skipping", "certId", cert.CertificateId, "error", err.Error())
				continue
			}

			// Compare fingerprints using certificate content
			if detailResp.Cert != "" {
				existingFingerprint := calculateCertFingerprint(detailResp.Cert)
				c.logger.Info("Comparing fingerprints", "target", targetFingerprint, "existing", existingFingerprint)
				if existingFingerprint == targetFingerprint {
					c.logger.Info("Found existing certificate with matching fingerprint", "certificateId", cert.CertificateId)
					return fmt.Sprintf("%d", cert.CertificateId), nil
				}
			}
		}
	}

	c.logger.Info("No existing certificate found with matching fingerprint")
	return "", nil
}

// calculateCertFingerprint calculates SHA256 fingerprint of a certificate
func calculateCertFingerprint(certPEM string) string {
	// Hash the raw PEM content for comparison
	hash := sha256.Sum256([]byte(certPEM))
	return hex.EncodeToString(hash[:])
}

// Helper function to check if certificate exists in CAS
func (c *Client) CertificateExists(ctx context.Context, certificateId string) (bool, error) {
	config := sdk.NewConfig()
	config.Timeout = 10 * time.Second
	cred := credentials.NewAccessKeyCredential(c.accessKeyID, c.accessKeySecret)

	client, err := cas.NewClientWithOptions(c.region, config, cred)
	if err != nil {
		return false, fmt.Errorf("failed to create CAS client: %w", err)
	}

	request := cas.CreateGetUserCertificateDetailRequest()
	request.Scheme = "https"
	// Convert certificateId string to int64
	certId, err := strconv.ParseInt(certificateId, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid certificate ID format: %w", err)
	}
	request.CertId = requests.Integer(fmt.Sprintf("%d", certId))

	_, err = client.GetUserCertificateDetail(request)
	if err != nil {
		if strings.Contains(err.Error(), "CertificateNotExist") {
			return false, nil
		}
		return false, fmt.Errorf("failed to check certificate: %w", err)
	}

	return true, nil
}

// SetCDNDomainCertificate configures the SSL certificate for a CDN domain
// using a certificate ID from Aliyun CAS service.
func (c *Client) SetCDNDomainCertificate(ctx context.Context, domain, certificateId string) error {
	c.logger.Info("Setting CDN domain SSL certificate", "domain", domain, "certificateId", certificateId)

	// Create CDN client
	config := sdk.NewConfig()
	config.Timeout = 30 * time.Second
	cred := credentials.NewAccessKeyCredential(c.accessKeyID, c.accessKeySecret)

	client, err := cdn.NewClientWithOptions(c.region, config, cred)
	if err != nil {
		return fmt.Errorf("failed to create CDN client: %w", err)
	}

	// Prepare request
	request := cdn.CreateSetCdnDomainSSLCertificateRequest()
	request.Scheme = "https"
	request.DomainName = domain
	request.CertType = "cas" // Use certificate from Aliyun CAS
	request.CertId = requests.Integer(certificateId)
	request.SSLProtocol = "on"
	request.CertName = fmt.Sprintf("cert-%s", strings.ReplaceAll(domain, ".", "-"))

	// Send request
	response, err := client.SetCdnDomainSSLCertificate(request)
	if err != nil {
		return fmt.Errorf("failed to set CDN domain SSL certificate: %w", err)
	}

	c.logger.Info("CDN domain SSL certificate configured successfully",
		"domain", domain,
		"certificateId", certificateId,
		"requestId", response.RequestId)

	return nil
}
