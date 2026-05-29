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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	certuploadv1 "wyundong.com/certupload/api/v1"
	"wyundong.com/certupload/internal/aliyun"
)

const (
	// Finalizer name for cleanup
	finalizerName = "certupload.aliyun.weyundong.com/finalizer"

	// Condition types
	conditionTypeReady = "Ready"

	// Event reasons
	eventReasonSyncStarted   = "SyncStarted"
	eventReasonSyncCompleted = "SyncCompleted"
	eventReasonSyncFailed    = "SyncFailed"
)

// CertUploadReconciler reconciles a CertUpload object
type CertUploadReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     record.EventRecorder
	AliyunClient AliCloudClient
}

// updateStatus safely updates the CertUpload status. Conflicts are handled
// gracefully (logged, not returned as error) to avoid requeue backoff loops.
func (r *CertUploadReconciler) updateStatus(ctx context.Context, certUpload *certuploadv1.CertUpload) error {
	latest := &certuploadv1.CertUpload{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(certUpload), latest); err != nil {
		log.FromContext(ctx).Error(err, "Failed to re-fetch before status update")
		return err
	}
	latest.Status = certUpload.Status
	if err := r.Status().Update(ctx, latest); err != nil {
		log.FromContext(ctx).Info("Status update conflict, will be retried on next reconcile", "error", err.Error())
	}
	return nil
}

// AliCloudClient interface for Aliyun operations
type AliCloudClient interface {
	// UploadCertificate uploads certificate to Aliyun SSL certificate service
	UploadCertificate(ctx context.Context, certPEM, keyPEM, domain string) (string, error)
	// UpdateOSSDomainCertificate updates OSS domain certificate configuration
	UpdateOSSDomainCertificate(ctx context.Context, bucket, domain, certificateId string) error
	// FindCertificateByFingerprint searches for an existing certificate by SHA256 fingerprint
	FindCertificateByFingerprint(ctx context.Context, certPEM string) (string, error)
	// SetCDNDomainCertificate configures CDN domain SSL certificate using a CAS certificate ID
	SetCDNDomainCertificate(ctx context.Context, domain, certificateId string) error
}

// MockAliCloudClient is a mock implementation for testing
type MockAliCloudClient struct{}

func (m *MockAliCloudClient) UploadCertificate(ctx context.Context, certPEM, keyPEM, domain string) (string, error) {
	// Mock implementation - returns a mock certificate ID
	return "mock-cert-id-" + domain, nil
}

func (m *MockAliCloudClient) UpdateOSSDomainCertificate(ctx context.Context, bucket, domain, certificateId string) error {
	// Mock implementation
	return nil
}

func (m *MockAliCloudClient) FindCertificateByFingerprint(ctx context.Context, certPEM string) (string, error) {
	// Mock implementation - returns empty string (no existing certificate found)
	return "", nil
}

func (m *MockAliCloudClient) SetCDNDomainCertificate(ctx context.Context, domain, certificateId string) error {
	// Mock implementation
	return nil
}

//+kubebuilder:rbac:groups=aliyun.weyundong.com,resources=certuploads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=aliyun.weyundong.com,resources=certuploads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=aliyun.weyundong.com,resources=certuploads/finalizers,verbs=update
//+kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconciliation loop
func (r *CertUploadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("certupload", req.NamespacedName)
	logger.Info("Starting reconciliation")

	// Fetch the CertUpload instance
	certUpload := &certuploadv1.CertUpload{}
	if err := r.Get(ctx, req.NamespacedName, certUpload); err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found, likely deleted
			logger.Info("CertUpload resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get CertUpload")
		return ctrl.Result{}, err
	}

	// Add finalizer if not present (conflict-safe: re-fetch before adding)
	if !controllerutil.ContainsFinalizer(certUpload, finalizerName) {
		// Re-fetch to avoid concurrent modification conflicts
		latest := &certuploadv1.CertUpload{}
		if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
			logger.Error(err, "Failed to re-fetch for finalizer")
			return ctrl.Result{}, err
		}
		if !controllerutil.ContainsFinalizer(latest, finalizerName) {
			controllerutil.AddFinalizer(latest, finalizerName)
			if err := r.Update(ctx, latest); err != nil {
				if apierrors.IsConflict(err) {
					logger.Info("Conflict adding finalizer, will retry")
					return ctrl.Result{Requeue: true}, nil
				}
				logger.Error(err, "Failed to add finalizer")
				return ctrl.Result{}, err
			}
		}
		// Update our local copy
		certUpload.ResourceVersion = latest.ResourceVersion
	}

	// Check if object is being deleted
	if !certUpload.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, certUpload, logger)
	}

	// Reconcile the object
	result, err := r.reconcileNormal(ctx, certUpload, logger)
	if err != nil {
		logger.Error(err, "Reconciliation failed")
		r.Recorder.Eventf(certUpload, corev1.EventTypeWarning, eventReasonSyncFailed, "Failed to sync certificate: %v", err)
	}
	return result, err
}

// reconcileNormal handles reconciliation when object is not being deleted
func (r *CertUploadReconciler) reconcileNormal(ctx context.Context, certUpload *certuploadv1.CertUpload, logger logr.Logger) (ctrl.Result, error) {
	// Update status with observed generation
	if certUpload.Status.ObservedGeneration != certUpload.Generation {
		certUpload.Status.ObservedGeneration = certUpload.Generation
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update observed generation: %w", err)
		}
	}

	// Fetch the referenced cert-manager Certificate
	certificate, err := r.getCertManagerCertificate(ctx, certUpload)
	if err != nil {
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  "CertificateNotFound",
			Message: fmt.Sprintf("Failed to get cert-manager Certificate: %v", err),
		})
		certUpload.Status.ErrorMessage = err.Error()
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{}, err
	}

	// Check if certificate is ready
	if !isCertificateReady(certificate) {
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  "CertificateNotReady",
			Message: "Referenced cert-manager Certificate is not ready",
		})
		certUpload.Status.ErrorMessage = "Certificate not ready"
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Get the certificate Secret
	certSecret, keySecret, err := r.getCertificateSecrets(ctx, certificate)
	if err != nil {
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  "SecretNotFound",
			Message: fmt.Sprintf("Failed to get certificate secrets: %v", err),
		})
		certUpload.Status.ErrorMessage = err.Error()
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{}, err
	}

	// Calculate certificate fingerprint
	fingerprint := calculateFingerprint(certSecret)

	// Check if certificate has changed since last sync
	if certUpload.Status.CertificateFingerprint == fingerprint && certUpload.Status.LastSyncTime != nil {
		logger.Info("Certificate unchanged, skipping sync", "fingerprint", fingerprint)
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  "UpToDate",
			Message: "Certificate is up to date",
		})
		certUpload.Status.ErrorMessage = ""
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{RequeueAfter: 1 * time.Hour}, nil
	}

	// Check if an explicit sync target is configured
	if !r.hasSyncTarget(certUpload) {
		logger.Info("No sync target configured (OSS, CDN, or uploadOnly)")
		certUpload.Status.OSSStatus = "Skipped"
		certUpload.Status.CDNStatus = "Skipped"
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  "NoTarget",
			Message: "No OSS, CDN or uploadOnly configured",
		})
		certUpload.Status.ErrorMessage = ""
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{RequeueAfter: 1 * time.Hour}, nil
	}

	// Determine the primary domain for CAS certificate naming
	casDomain := r.getCASDomain(certUpload, certificate)

	// Get Aliyun access key ID and secret, then create client
	accessKeyID, err := r.getAccessKeyID(ctx, certUpload)
	if err != nil {
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  "AccessKeyIDError",
			Message: fmt.Sprintf("Failed to get access key ID: %v", err),
		})
		certUpload.Status.ErrorMessage = err.Error()
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{}, err
	}

	accessKeySecret, err := r.getAccessKeySecret(ctx, certUpload)
	if err != nil {
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  "AccessKeyError",
			Message: fmt.Sprintf("Failed to get access key secret: %v", err),
		})
		certUpload.Status.ErrorMessage = err.Error()
		if err := r.updateStatus(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{}, err
	}

	// Create Aliyun client with credentials
	var aliClient AliCloudClient
	if r.AliyunClient != nil {
		// Use injected client (e.g., mock for testing)
		aliClient = r.AliyunClient
	} else {
		// Create real Aliyun client
		aliClient = aliyun.NewClient(accessKeyID, accessKeySecret, certUpload.Spec.Region, logger)
	}

	logger.Info("Starting certificate sync", "fingerprint", fingerprint)
	r.Recorder.Eventf(certUpload, corev1.EventTypeNormal, eventReasonSyncStarted, "Starting certificate sync for domain %s", casDomain)

	// Check if certificate already exists in Aliyun CAS
	existingCertID, err := aliClient.FindCertificateByFingerprint(ctx, string(certSecret))
	if err != nil {
		logger.Info("Failed to check existing certificates, proceeding with upload", "error", err.Error())
	}

	var certificateID string
	if existingCertID != "" {
		// Certificate already exists in Aliyun, skip upload
		logger.Info("Certificate already exists in Aliyun, skipping upload", "certificateId", existingCertID)
		certificateID = existingCertID
		r.Recorder.Eventf(certUpload, corev1.EventTypeNormal, "CertificateExists", "Certificate already exists in Aliyun (ID: %s), skipping upload", certificateID)
	} else {
		// Upload certificate to Aliyun SSL certificate service
		certificateID, err = aliClient.UploadCertificate(ctx, string(certSecret), string(keySecret), casDomain)
		if err != nil {
			meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
				Type:    conditionTypeReady,
				Status:  metav1.ConditionFalse,
				Reason:  "UploadFailed",
				Message: fmt.Sprintf("Failed to upload certificate to Aliyun: %v", err),
			})
			certUpload.Status.ErrorMessage = err.Error()
			if err := r.updateStatus(ctx, certUpload); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
			}
			return ctrl.Result{}, fmt.Errorf("failed to upload certificate: %w", err)
		}
	}

	// Track sync results for OSS and CDN
	ossErr := r.syncOSS(ctx, certUpload, aliClient, certificateID, logger)
	cdnErr := r.syncCDN(ctx, certUpload, aliClient, certificateID, logger)

	// Update status
	now := metav1.Now()
	certUpload.Status.LastSyncTime = &now
	certUpload.Status.CertificateFingerprint = fingerprint
	certUpload.Status.CASCertificateID = certificateID

	// Determine overall status
	ossFailed := certUpload.Status.OSSStatus == "Failed"
	cdnFailed := certUpload.Status.CDNStatus == "Failed"

	if ossFailed || cdnFailed {
		var errMessages []string
		if ossFailed {
			errMessages = append(errMessages, "OSS: "+certUpload.Status.OSSErrorMessage)
		}
		if cdnFailed {
			errMessages = append(errMessages, "CDN: "+certUpload.Status.CDNErrorMessage)
		}
		combinedErr := strings.Join(errMessages, "; ")

		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  "SyncFailed",
			Message: combinedErr,
		})
		certUpload.Status.ErrorMessage = combinedErr
	} else {
		var syncReason, syncMessage string
		if existingCertID != "" {
			syncReason = "CertificateExists"
			syncMessage = fmt.Sprintf("Certificate already exists in Aliyun (ID: %s), sync completed", certificateID)
		} else {
			syncReason = "Synced"
			syncMessage = fmt.Sprintf("Certificate successfully uploaded to Aliyun (Certificate ID: %s)", certificateID)
		}
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  syncReason,
			Message: syncMessage,
		})
		certUpload.Status.ErrorMessage = ""
	}

	if err := r.updateStatus(ctx, certUpload); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	logger.Info("Certificate sync completed", "certificateID", certificateID, "fingerprint", fingerprint)
	r.Recorder.Eventf(certUpload, corev1.EventTypeNormal, eventReasonSyncCompleted, "Certificate synced to Aliyun (ID: %s)", certificateID)

	// Requeue after 1 hour to check for updates
	if ossErr != nil || cdnErr != nil {
		// If any sync failed, requeue sooner to retry
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	return ctrl.Result{RequeueAfter: 1 * time.Hour}, nil
}

// hasSyncTarget checks if at least one sync target is configured:
// OSS, CDN, or uploadOnly mode.
func (r *CertUploadReconciler) hasSyncTarget(certUpload *certuploadv1.CertUpload) bool {
	return certUpload.Spec.OSS != nil || certUpload.Spec.CDN != nil || certUpload.Spec.UploadOnly
}

// getCASDomain returns the domain to use as certificate name in Aliyun CAS.
// Priority: OSS domain > CDN domain > cert-manager DNS name > cert-manager cert name.
func (r *CertUploadReconciler) getCASDomain(certUpload *certuploadv1.CertUpload, certificate *certmanagerv1.Certificate) string {
	if certUpload.Spec.OSS != nil {
		return certUpload.Spec.OSS.Domain
	}
	if certUpload.Spec.CDN != nil {
		return certUpload.Spec.CDN.Domain
	}
	// Fallback: use cert-manager Certificate's first DNS name or common name
	if len(certificate.Spec.DNSNames) > 0 {
		return certificate.Spec.DNSNames[0]
	}
	if certificate.Spec.CommonName != "" {
		return certificate.Spec.CommonName
	}
	// Last resort: use the Certificate resource name
	return certificate.Name
}

// syncOSS updates the OSS domain certificate if OSS config is present
func (r *CertUploadReconciler) syncOSS(ctx context.Context, certUpload *certuploadv1.CertUpload, aliClient AliCloudClient, certificateID string, logger logr.Logger) error {
	if certUpload.Spec.OSS == nil {
		certUpload.Status.OSSStatus = "Skipped"
		certUpload.Status.OSSErrorMessage = ""
		return nil
	}

	now := metav1.Now()
	logger.Info("Starting OSS domain certificate sync", "bucket", certUpload.Spec.OSS.Bucket, "domain", certUpload.Spec.OSS.Domain)

	if err := aliClient.UpdateOSSDomainCertificate(ctx, certUpload.Spec.OSS.Bucket, certUpload.Spec.OSS.Domain, certificateID); err != nil {
		logger.Error(err, "Failed to update OSS domain certificate")
		certUpload.Status.OSSStatus = "Failed"
		certUpload.Status.OSSErrorMessage = err.Error()
		return err
	}

	certUpload.Status.OSSStatus = "Succeeded"
	certUpload.Status.OSSErrorMessage = ""
	certUpload.Status.OSSLastSyncTime = &now
	logger.Info("OSS domain certificate sync completed")
	return nil
}

// syncCDN updates the CDN domain SSL certificate if CDN config is present
func (r *CertUploadReconciler) syncCDN(ctx context.Context, certUpload *certuploadv1.CertUpload, aliClient AliCloudClient, certificateID string, logger logr.Logger) error {
	if certUpload.Spec.CDN == nil {
		certUpload.Status.CDNStatus = "Skipped"
		certUpload.Status.CDNErrorMessage = ""
		return nil
	}

	now := metav1.Now()
	logger.Info("Starting CDN domain SSL certificate sync", "domain", certUpload.Spec.CDN.Domain)

	if err := aliClient.SetCDNDomainCertificate(ctx, certUpload.Spec.CDN.Domain, certificateID); err != nil {
		logger.Error(err, "Failed to set CDN domain SSL certificate")
		certUpload.Status.CDNStatus = "Failed"
		certUpload.Status.CDNErrorMessage = err.Error()
		return err
	}

	certUpload.Status.CDNStatus = "Succeeded"
	certUpload.Status.CDNErrorMessage = ""
	certUpload.Status.CDNLastSyncTime = &now
	logger.Info("CDN domain SSL certificate sync completed")
	return nil
}

// reconcileDelete handles cleanup when object is being deleted
func (r *CertUploadReconciler) reconcileDelete(ctx context.Context, certUpload *certuploadv1.CertUpload, logger logr.Logger) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(certUpload, finalizerName) {
		// Perform cleanup if needed
		// Note: In a real implementation, you might want to remove the certificate from Aliyun
		// For now, we just remove the finalizer
		controllerutil.RemoveFinalizer(certUpload, finalizerName)
		if err := r.Update(ctx, certUpload); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer, object can be deleted")
	}
	return ctrl.Result{}, nil
}

// getCertManagerCertificate fetches the referenced cert-manager Certificate
func (r *CertUploadReconciler) getCertManagerCertificate(ctx context.Context, certUpload *certuploadv1.CertUpload) (*certmanagerv1.Certificate, error) {
	ref := certUpload.Spec.CertManagerCertRef
	namespace := ref.Namespace
	if namespace == "" {
		namespace = certUpload.Namespace
	}

	certificate := &certmanagerv1.Certificate{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}, certificate)
	return certificate, err
}

// getCertificateSecrets retrieves the certificate and private key from the Secret referenced by the Certificate
func (r *CertUploadReconciler) getCertificateSecrets(ctx context.Context, certificate *certmanagerv1.Certificate) ([]byte, []byte, error) {
	if certificate.Spec.SecretName == "" {
		return nil, nil, fmt.Errorf("certificate does not have a secret name in spec")
	}

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      certificate.Spec.SecretName,
		Namespace: certificate.Namespace,
	}, secret)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get secret %s: %w", certificate.Spec.SecretName, err)
	}

	certData := secret.Data["tls.crt"]
	keyData := secret.Data["tls.key"]

	if len(certData) == 0 {
		return nil, nil, fmt.Errorf("secret %s does not contain tls.crt", certificate.Spec.SecretName)
	}
	if len(keyData) == 0 {
		return nil, nil, fmt.Errorf("secret %s does not contain tls.key", certificate.Spec.SecretName)
	}

	return certData, keyData, nil
}

// getAccessKeyID retrieves the Aliyun access key ID
func (r *CertUploadReconciler) getAccessKeyID(ctx context.Context, certUpload *certuploadv1.CertUpload) (string, error) {
	ref := certUpload.Spec.AccessKeyIDRef
	namespace := ref.Namespace
	if namespace == "" {
		namespace = certUpload.Namespace
	}

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}, secret)
	if err != nil {
		return "", fmt.Errorf("failed to get secret %s: %w", ref.Name, err)
	}

	accessKeyID, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("secret %s does not contain key %s", ref.Name, ref.Key)
	}

	return string(accessKeyID), nil
}

// getAccessKeySecret retrieves the Aliyun access key secret
func (r *CertUploadReconciler) getAccessKeySecret(ctx context.Context, certUpload *certuploadv1.CertUpload) (string, error) {
	ref := certUpload.Spec.AccessKeySecretRef
	namespace := ref.Namespace
	if namespace == "" {
		namespace = certUpload.Namespace
	}

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}, secret)
	if err != nil {
		return "", fmt.Errorf("failed to get secret %s: %w", ref.Name, err)
	}

	accessKeySecret, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("secret %s does not contain key %s", ref.Name, ref.Key)
	}

	return string(accessKeySecret), nil
}

// isCertificateReady checks if a cert-manager Certificate is ready
func isCertificateReady(certificate *certmanagerv1.Certificate) bool {
	for _, condition := range certificate.Status.Conditions {
		if condition.Type == certmanagerv1.CertificateConditionReady {
			return condition.Status == cmmeta.ConditionTrue
		}
	}
	return false
}

// calculateFingerprint calculates a SHA256 fingerprint of the certificate
func calculateFingerprint(certData []byte) string {
	hash := sha256.Sum256(certData)
	return hex.EncodeToString(hash[:])
}

// SetupWithManager sets up the controller with the Manager.
func (r *CertUploadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a controller that watches CertUpload resources
	// and also watches cert-manager Certificate resources that are referenced by CertUpload
	return ctrl.NewControllerManagedBy(mgr).
		For(&certuploadv1.CertUpload{}).
		Watches(
			&certmanagerv1.Certificate{},
			handler.EnqueueRequestsFromMapFunc(r.mapCertToCertUploads),
		).
		Complete(r)
}

// mapCertToCertUploads maps a Certificate to all CertUpload resources that reference it
func (r *CertUploadReconciler) mapCertToCertUploads(ctx context.Context, obj client.Object) []reconcile.Request {
	certificate, ok := obj.(*certmanagerv1.Certificate)
	if !ok {
		return []reconcile.Request{}
	}

	// List all CertUpload resources
	certUploadList := &certuploadv1.CertUploadList{}
	if err := r.List(ctx, certUploadList); err != nil {
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, certUpload := range certUploadList.Items {
		ref := certUpload.Spec.CertManagerCertRef
		namespace := ref.Namespace
		if namespace == "" {
			namespace = certUpload.Namespace
		}

		if ref.Name == certificate.Name && namespace == certificate.Namespace {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      certUpload.Name,
					Namespace: certUpload.Namespace,
				},
			})
		}
	}
	return requests
}
