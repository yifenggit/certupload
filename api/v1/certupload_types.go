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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OSSConfig defines the OSS domain certificate configuration.
// If set, the controller will update the OSS domain certificate after uploading to CAS.
type OSSConfig struct {
	// Bucket is the name of the OSS bucket where the domain certificate should be updated.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Domain is the domain name for the OSS bucket certificate binding.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Domain string `json:"domain"`
}

// CDNConfig defines the CDN domain SSL certificate configuration.
// If set, the controller will update the CDN domain SSL certificate after uploading to CAS.
type CDNConfig struct {
	// Domain is the CDN accelerated domain name for which the SSL certificate should be configured.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Domain string `json:"domain"`
}

// CertUploadSpec defines the desired state of CertUpload
type CertUploadSpec struct {
	// AccessKeyIDRef is a reference to a Secret containing the Aliyun Access Key ID.
	// The Secret must contain a key named "accessKeyId".
	// +kubebuilder:validation:Required
	AccessKeyIDRef SecretKeySelector `json:"accessKeyIdRef"`

	// AccessKeySecretRef is a reference to a Secret containing the Aliyun Access Key Secret.
	// The Secret must contain a key named "accessKeySecret".
	// +kubebuilder:validation:Required
	AccessKeySecretRef SecretKeySelector `json:"accessKeySecretRef"`

	// Region is the Aliyun region where the SSL certificate service (CAS) is located.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`

	// CertManagerCertRef is a reference to the cert-manager Certificate resource.
	// +kubebuilder:validation:Required
	CertManagerCertRef CertManagerCertRef `json:"certManagerCertRef"`

	// OSS is the optional OSS domain certificate configuration.
	// If set, the controller will update the OSS bucket domain certificate.
	// +optional
	OSS *OSSConfig `json:"oss,omitempty"`

	// CDN is the optional CDN domain SSL certificate configuration.
	// If set, the controller will update the CDN domain SSL certificate.
	// +optional
	CDN *CDNConfig `json:"cdn,omitempty"`

	// UploadOnly when set to true, uploads the certificate to Aliyun CAS only,
	// without binding to OSS or CDN. Use this when you want to manually manage
	// certificate bindings later.
	// +optional
	UploadOnly bool `json:"uploadOnly,omitempty"`
}

// SecretKeySelector selects a key of a Secret.
type SecretKeySelector struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Secret. If empty, defaults to the namespace of the CertUpload resource.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key in the Secret's data to use.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// CertManagerCertRef references a cert-manager Certificate resource.
type CertManagerCertRef struct {
	// Name of the Certificate resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Certificate resource. If empty, defaults to the namespace of the CertUpload resource.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// CertUploadStatus defines the observed state of CertUpload
type CertUploadStatus struct {
	// Conditions represent the latest available observations of the CertUpload's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastSyncTime is the timestamp when the certificate was last successfully synchronized to Aliyun.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// CertificateFingerprint is the fingerprint (e.g., SHA256) of the last uploaded certificate.
	// +optional
	CertificateFingerprint string `json:"certificateFingerprint,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ErrorMessage contains a human-readable error message if the last sync failed.
	// +optional
	ErrorMessage string `json:"errorMessage,omitempty"`

	// CASCertificateID is the ID of the certificate stored in Aliyun CAS.
	// +optional
	CASCertificateID string `json:"casCertificateId,omitempty"`

	// OSSLastSyncTime is the timestamp when the OSS domain certificate was last updated.
	// +optional
	OSSLastSyncTime *metav1.Time `json:"ossLastSyncTime,omitempty"`

	// OSSStatus indicates the last OSS sync result: Succeeded, Failed, or Skipped.
	// +optional
	OSSStatus string `json:"ossStatus,omitempty"`

	// OSSErrorMessage contains the error message if the last OSS sync failed.
	// +optional
	OSSErrorMessage string `json:"ossErrorMessage,omitempty"`

	// CDNLastSyncTime is the timestamp when the CDN domain SSL certificate was last updated.
	// +optional
	CDNLastSyncTime *metav1.Time `json:"cdnLastSyncTime,omitempty"`

	// CDNStatus indicates the last CDN sync result: Succeeded, Failed, or Skipped.
	// +optional
	CDNStatus string `json:"cdnStatus,omitempty"`

	// CDNErrorMessage contains the error message if the last CDN sync failed.
	// +optional
	CDNErrorMessage string `json:"cdnErrorMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cu;cupload
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="OSS Status",type="string",JSONPath=".status.ossStatus"
// +kubebuilder:printcolumn:name="CDN Status",type="string",JSONPath=".status.cdnStatus"
// +kubebuilder:printcolumn:name="Last Sync",type="date",JSONPath=".status.lastSyncTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:storageversion

// CertUpload is the Schema for the certuploads API
type CertUpload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CertUploadSpec   `json:"spec,omitempty"`
	Status CertUploadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CertUploadList contains a list of CertUpload
type CertUploadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CertUpload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CertUpload{}, &CertUploadList{})
}
