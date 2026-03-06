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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	aliyunv1alpha1 "wyundong.com/certupload/api/v1alpha1"
)

func TestCertUploadController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CertUpload Controller Suite")
}

var _ = Describe("CertUpload Controller", func() {
	var (
		ctx        = context.Background()
		fakeClient client.Client
		reconciler *CertUploadReconciler
	)

	BeforeEach(func() {
		// Setup fake client with scheme
		scheme := newScheme()
		fakeClient = fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&aliyunv1alpha1.CertUpload{}).
			Build()

		reconciler = &CertUploadReconciler{
			Client:       fakeClient,
			Scheme:       scheme,
			Recorder:     record.NewFakeRecorder(10),
			AliyunClient: &MockAliCloudClient{},
		}
	})

	Context("When creating a CertUpload", func() {
		It("Should add finalizer", func() {
			// Create the referenced cert-manager Certificate first
			cert := &certmanagerv1.Certificate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cert",
					Namespace: "default",
				},
				Spec: certmanagerv1.CertificateSpec{
					SecretName: "test-secret",
				},
				Status: certmanagerv1.CertificateStatus{
					Conditions: []certmanagerv1.CertificateCondition{
						{
							Type:   certmanagerv1.CertificateConditionReady,
							Status: cmmeta.ConditionTrue,
						},
					},
				},
			}
			err := fakeClient.Create(ctx, cert)
			Expect(err).NotTo(HaveOccurred())

			// Create the referenced secret for access key
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"accessKeyId":     []byte("test-access-key-id"),
					"accessKeySecret": []byte("test-access-key-secret"),
				},
			}
			err = fakeClient.Create(ctx, secret)
			Expect(err).NotTo(HaveOccurred())

			// Create the certificate secret referenced by the Certificate
			certSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"tls.crt": []byte("-----BEGIN CERTIFICATE-----\ntest-cert\n-----END CERTIFICATE-----\n"),
					"tls.key": []byte("-----BEGIN PRIVATE KEY-----\ntest-key\n-----END PRIVATE KEY-----\n"),
				},
			}
			err = fakeClient.Create(ctx, certSecret)
			Expect(err).NotTo(HaveOccurred())

			certUpload := &aliyunv1alpha1.CertUpload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: aliyunv1alpha1.CertUploadSpec{
					AccessKeyIDRef: aliyunv1alpha1.SecretKeySelector{
						Name: "secret",
						Key:  "accessKeyId",
					},
					AccessKeySecretRef: aliyunv1alpha1.SecretKeySelector{
						Name: "secret",
						Key:  "accessKeySecret",
					},
					Region: "cn-hangzhou",
					Bucket: "test-bucket",
					Domain: "example.com",
					CertManagerCertRef: aliyunv1alpha1.CertManagerCertRef{
						Name: "test-cert",
					},
				},
			}

			err = fakeClient.Create(ctx, certUpload)
			Expect(err).NotTo(HaveOccurred())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test",
					Namespace: "default",
				},
			}

			_, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// Fetch updated CertUpload
			updated := &aliyunv1alpha1.CertUpload{}
			err = fakeClient.Get(ctx, types.NamespacedName{Name: "test", Namespace: "default"}, updated)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Finalizers).To(ContainElement(finalizerName))
		})
	})

	Context("When certificate is not ready", func() {
		It("Should set condition to not ready", func() {
			// Create cert-manager Certificate that is not ready
			cert := &certmanagerv1.Certificate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cert",
					Namespace: "default",
				},
				Status: certmanagerv1.CertificateStatus{
					Conditions: []certmanagerv1.CertificateCondition{
						{
							Type:   certmanagerv1.CertificateConditionReady,
							Status: cmmeta.ConditionFalse,
						},
					},
				},
			}
			err := fakeClient.Create(ctx, cert)
			Expect(err).NotTo(HaveOccurred())

			certUpload := &aliyunv1alpha1.CertUpload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: aliyunv1alpha1.CertUploadSpec{
					AccessKeyIDRef: aliyunv1alpha1.SecretKeySelector{
						Name: "secret",
						Key:  "accessKeyId",
					},
					AccessKeySecretRef: aliyunv1alpha1.SecretKeySelector{
						Name: "secret",
						Key:  "accessKeySecret",
					},
					Region: "cn-hangzhou",
					Bucket: "test-bucket",
					Domain: "example.com",
					CertManagerCertRef: aliyunv1alpha1.CertManagerCertRef{
						Name: "test-cert",
					},
				},
			}
			err = fakeClient.Create(ctx, certUpload)
			Expect(err).NotTo(HaveOccurred())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test",
					Namespace: "default",
				},
			}

			result, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically("~", 5*time.Minute, float64(time.Second)))
		})
	})
})

// Helper function to create scheme with all necessary types
func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = aliyunv1alpha1.AddToScheme(scheme)
	_ = certmanagerv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	return scheme
}
