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
	aliyunv1alpha1 "wyundong.com/certupload/api/v1alpha1"
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

// AliCloudClient interface for Aliyun operations
type AliCloudClient interface {
	// UploadCertificate uploads certificate to Aliyun SSL certificate service
	UploadCertificate(ctx context.Context, certPEM, keyPEM, domain string) (string, error)
	// UpdateOSSDomainCertificate updates OSS domain certificate configuration
	UpdateOSSDomainCertificate(ctx context.Context, bucket, domain, certificateId string) error
	// FindCertificateByFingerprint searches for an existing certificate by SHA256 fingerprint
	FindCertificateByFingerprint(ctx context.Context, certPEM string) (string, error)
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
	certUpload := &aliyunv1alpha1.CertUpload{}
	if err := r.Get(ctx, req.NamespacedName, certUpload); err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found, likely deleted
			logger.Info("CertUpload resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get CertUpload")
		return ctrl.Result{}, err
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(certUpload, finalizerName) {
		controllerutil.AddFinalizer(certUpload, finalizerName)
		if err := r.Update(ctx, certUpload); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Check if object is being deleted
	if !certUpload.ObjectMeta.DeletionTimestamp.IsZero() {
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
func (r *CertUploadReconciler) reconcileNormal(ctx context.Context, certUpload *aliyunv1alpha1.CertUpload, logger logr.Logger) (ctrl.Result, error) {
	// Update status with observed generation
	if certUpload.Status.ObservedGeneration != certUpload.Generation {
		certUpload.Status.ObservedGeneration = certUpload.Generation
		if err := r.Status().Update(ctx, certUpload); err != nil {
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
		if err := r.Status().Update(ctx, certUpload); err != nil {
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
		if err := r.Status().Update(ctx, certUpload); err != nil {
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
		if err := r.Status().Update(ctx, certUpload); err != nil {
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
		if err := r.Status().Update(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{RequeueAfter: 1 * time.Hour}, nil
	}

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
		if err := r.Status().Update(ctx, certUpload); err != nil {
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
		if err := r.Status().Update(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{}, err
	}

	// Create Aliyun client with credentials
	var client AliCloudClient
	if r.AliyunClient != nil {
		// Use injected client (e.g., mock for testing)
		client = r.AliyunClient
	} else {
		// Create real Aliyun client
		client = aliyun.NewClient(accessKeyID, accessKeySecret, certUpload.Spec.Region, logger)
	}

	logger.Info("Starting certificate sync", "fingerprint", fingerprint)
	r.Recorder.Eventf(certUpload, corev1.EventTypeNormal, eventReasonSyncStarted, "Starting certificate sync for domain %s", certUpload.Spec.Domain)

	// Check if certificate already exists in Aliyun CAS
	existingCertID, err := client.FindCertificateByFingerprint(ctx, string(certSecret))
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
		certificateID, err = client.UploadCertificate(ctx, string(certSecret), string(keySecret), certUpload.Spec.Domain)
		if err != nil {
			meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
				Type:    conditionTypeReady,
				Status:  metav1.ConditionFalse,
				Reason:  "UploadFailed",
				Message: fmt.Sprintf("Failed to upload certificate to Aliyun: %v", err),
			})
			certUpload.Status.ErrorMessage = err.Error()
			if err := r.Status().Update(ctx, certUpload); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
			}
			return ctrl.Result{}, fmt.Errorf("failed to upload certificate: %w", err)
		}
	}

	// Update OSS domain certificate
	if err := client.UpdateOSSDomainCertificate(ctx, certUpload.Spec.Bucket, certUpload.Spec.Domain, certificateID); err != nil {
		meta.SetStatusCondition(&certUpload.Status.Conditions, metav1.Condition{
			Type:    conditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  "OSSUploadFailed",
			Message: fmt.Sprintf("Failed to update OSS domain certificate: %v", err),
		})
		certUpload.Status.ErrorMessage = err.Error()
		if err := r.Status().Update(ctx, certUpload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return ctrl.Result{}, fmt.Errorf("failed to update OSS domain certificate: %w", err)
	}

	// Update status
	now := metav1.Now()
	certUpload.Status.LastSyncTime = &now
	certUpload.Status.CertificateFingerprint = fingerprint
	certUpload.Status.ErrorMessage = ""

	var syncReason, syncMessage string
	if existingCertID != "" {
		syncReason = "CertificateExists"
		syncMessage = fmt.Sprintf("Certificate already exists in Aliyun (ID: %s), no upload needed", certificateID)
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

	if err := r.Status().Update(ctx, certUpload); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	logger.Info("Certificate sync completed", "certificateID", certificateID, "fingerprint", fingerprint)
	r.Recorder.Eventf(certUpload, corev1.EventTypeNormal, eventReasonSyncCompleted, "Certificate successfully synced to Aliyun for domain %s", certUpload.Spec.Domain)

	// Requeue after 1 hour to check for updates
	return ctrl.Result{RequeueAfter: 1 * time.Hour}, nil
}

// reconcileDelete handles cleanup when object is being deleted
func (r *CertUploadReconciler) reconcileDelete(ctx context.Context, certUpload *aliyunv1alpha1.CertUpload, logger logr.Logger) (ctrl.Result, error) {
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
func (r *CertUploadReconciler) getCertManagerCertificate(ctx context.Context, certUpload *aliyunv1alpha1.CertUpload) (*certmanagerv1.Certificate, error) {
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
func (r *CertUploadReconciler) getAccessKeyID(ctx context.Context, certUpload *aliyunv1alpha1.CertUpload) (string, error) {
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
func (r *CertUploadReconciler) getAccessKeySecret(ctx context.Context, certUpload *aliyunv1alpha1.CertUpload) (string, error) {
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
		For(&aliyunv1alpha1.CertUpload{}).
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
	certUploadList := &aliyunv1alpha1.CertUploadList{}
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
