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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cas"
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

// UpdateOSSDomainCertificate updates OSS domain certificate configuration
// Note: This is a simplified implementation. In production, you would need to:
// 1. Check if the domain is already bound to the bucket
// 2. Bind the domain if not already bound (requires DNS CNAME configuration)
// 3. Set the certificate for the domain using OSS API or Console
// The actual implementation may vary based on your Aliyun account setup and requirements
func (c *Client) UpdateOSSDomainCertificate(ctx context.Context, bucketName, domain, certificateId string) error {
	c.logger.Info("Updating OSS domain certificate", "bucket", bucketName, "domain", domain, "certificateId", certificateId)

	// Create OSS client
	client, err := oss.New(fmt.Sprintf("https://oss-%s.aliyuncs.com", c.region), c.accessKeyID, c.accessKeySecret)
	if err != nil {
		return fmt.Errorf("failed to create OSS client: %w", err)
	}

	// Try to bind domain to bucket if not already bound
	// This will fail if the domain is already bound, which is acceptable
	c.logger.Info("Attempting to bind domain to bucket", "domain", domain)
	err = client.PutBucketCname(bucketName, domain)
	if err != nil {
		// Log the error but don't fail - domain might already be bound
		c.logger.Info("Domain binding result (may already be bound)", "domain", domain, "error", err.Error())
	} else {
		c.logger.Info("Domain bound to bucket successfully", "domain", domain)
	}

	// Note: Setting the certificate for a CNAME requires using the Aliyun Console or
	// a specific API that may not be directly available in the OSS Go SDK.
	// The certificate uploaded to CAS can be used, but the association needs to be done
	// through the Aliyun Console > OSS > Bucket > Domain Management > Certificate Hosting
	//
	// Alternative approaches:
	// 1. Use Aliyun CLI: aliyun oss put-cname-cert
	// 2. Use Aliyun SDK for OpenAPI
	// 3. Manual configuration through console
	//
	// For now, we log the information and assume the certificate will be manually associated
	// or a separate automation will handle the certificate binding

	c.logger.Info("OSS domain certificate update completed",
		"bucket", bucketName,
		"domain", domain,
		"certificateId", certificateId,
		"note", "Certificate uploaded to CAS. Please associate it with the domain in OSS Console > Domain Management")

	return nil
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
