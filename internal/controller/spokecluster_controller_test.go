/*
Copyright 2026 Red Hat, Inc..

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

package controller_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/controller"
	"github.com/openshift/lightspeed-hub/internal/credential"
)

const (
	spokeClusterFinalizer     = "hub.openshift.io/spoke-cleanup"
	healthyRequeueAfter       = 5 * time.Minute
	unhealthyRequeueAfter     = 30 * time.Second
	conditionTypeConnected    = "Connected"
	reasonConnectionSucceeded = "ConnectionSucceeded"
	reasonConnectionFailed    = "ConnectionFailed"
	reasonCredentialError     = "CredentialError"
)

// fakeCredentialSource is a mock CredentialSource for testing
type fakeCredentialSource struct {
	cfg *rest.Config
	err error
}

func (f *fakeCredentialSource) GetRESTConfig(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (*rest.Config, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cfg, nil
}

// Test helpers
func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = hubv1alpha1.AddToScheme(scheme)
	return scheme
}

func newSpokeCluster(name string) *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID("test-uid-" + name),
		},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api.spoke.example.com:6443",
			CredentialSource: hubv1alpha1.CredentialSource{
				Secret: &hubv1alpha1.SecretCredentialSource{
					Name:      "spoke-admin-kubeconfig",
					Namespace: "default",
				},
			},
		},
	}
}

func newSpokeClusterWithFinalizer(name string) *hubv1alpha1.SpokeCluster {
	sc := newSpokeCluster(name)
	sc.Finalizers = []string{spokeClusterFinalizer}
	return sc
}

func newSpokeClusterDeleting(name string) *hubv1alpha1.SpokeCluster {
	sc := newSpokeClusterWithFinalizer(name)
	now := metav1.Now()
	sc.DeletionTimestamp = &now
	return sc
}

var _ = Describe("SpokeClusterReconciler", func() {
	const (
		testNamespace = "test-operator-ns"
	)

	var (
		ctx context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("Reconcile", func() {
		It("should add finalizer on first reconcile", func() {
			sc := newSpokeCluster("test-spoke")

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{
				cfg: &rest.Config{Host: "https://api.spoke.example.com:6443"},
			}

			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			Expect(err).NotTo(HaveOccurred())
			// Should requeue to continue reconciliation
			Expect(result.RequeueAfter > 0 || result.Requeue).To(BeTrue()) //nolint:staticcheck // Checking legacy field

			// Check finalizer was added
			var updated hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Finalizers).To(ContainElement(spokeClusterFinalizer))
		})

		It("should reconcile happy path to Connected=True", func() {
			sc := newSpokeClusterWithFinalizer("test-spoke")

			spokeScheme := newTestScheme()
			spokeClient := fake.NewClientBuilder().
				WithScheme(spokeScheme).
				Build()

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{
				cfg: &rest.Config{
					Host: "https://api.spoke.example.com:6443",
					TLSClientConfig: rest.TLSClientConfig{
						CAData: []byte("fake-ca"),
					},
					BearerToken: "fake-token",
				},
			}

			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)
			reconciler.NewSpokeClient = func(cfg *rest.Config) (client.Client, error) {
				return spokeClient, nil
			}
			reconciler.CheckConnectivity = func(cfg *rest.Config) error {
				return nil
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(healthyRequeueAfter))

			// Check status
			var updated hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)
			Expect(err).NotTo(HaveOccurred())

			condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeConnected)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal(reasonConnectionSucceeded))

			// Check standing kubeconfig was created
			secretKey := types.NamespacedName{
				Name:      credential.StandingKubeconfigName(sc.Name),
				Namespace: testNamespace,
			}
			var secret corev1.Secret
			err = hubClient.Get(ctx, secretKey, &secret)
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Data).To(HaveKey(credential.KubeconfigKey))
			Expect(secret.OwnerReferences).To(HaveLen(1))
			Expect(secret.OwnerReferences[0].Name).To(Equal(sc.Name))
		})

		It("should set Connected=False on credential error", func() {
			sc := newSpokeClusterWithFinalizer("test-spoke")

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{
				err: fmt.Errorf("secret not found"),
			}

			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(unhealthyRequeueAfter))

			// Check status
			var updated hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)
			Expect(err).NotTo(HaveOccurred())

			condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeConnected)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reasonCredentialError))
		})

		It("should set Connected=False on connectivity failure", func() {
			sc := newSpokeClusterWithFinalizer("test-spoke")

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{
				cfg: &rest.Config{
					Host: "https://api.spoke.example.com:6443",
					TLSClientConfig: rest.TLSClientConfig{
						CAData: []byte("fake-ca"),
					},
					BearerToken: "fake-token",
				},
			}

			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)
			reconciler.CheckConnectivity = func(cfg *rest.Config) error {
				return fmt.Errorf("connection refused")
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(unhealthyRequeueAfter))

			// Check status
			var updated hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)
			Expect(err).NotTo(HaveOccurred())

			condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeConnected)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reasonConnectionFailed))
		})

		It("should delete with finalizer and deprovision spoke", func() {
			sc := newSpokeClusterDeleting("test-spoke")

			// Create standing kubeconfig Secret
			standingSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      credential.StandingKubeconfigName(sc.Name),
					Namespace: testNamespace,
				},
				Data: map[string][]byte{
					credential.KubeconfigKey: []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://api.spoke.example.com:6443
    certificate-authority-data: ZmFrZS1jYQ==
  name: spoke
users:
- user:
    token: fake-token
  name: spoke-user
contexts:
- context:
    cluster: spoke
    user: spoke-user
  name: spoke
current-context: spoke
`),
				},
			}

			spokeScheme := newTestScheme()
			spokeClient := fake.NewClientBuilder().
				WithScheme(spokeScheme).
				Build()

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc, standingSecret).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{}
			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)
			reconciler.NewSpokeClient = func(cfg *rest.Config) (client.Client, error) {
				return spokeClient, nil
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Check finalizer was removed or CR was deleted
			var updated hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)
			// Either the CR is gone (deleted) or the finalizer is removed
			if err == nil {
				Expect(updated.Finalizers).NotTo(ContainElement(spokeClusterFinalizer))
			}
		})

		It("should delete when spoke is unreachable", func() {
			sc := newSpokeClusterDeleting("test-spoke")

			// Create standing kubeconfig Secret
			standingSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      credential.StandingKubeconfigName(sc.Name),
					Namespace: testNamespace,
				},
				Data: map[string][]byte{
					credential.KubeconfigKey: []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://api.spoke.example.com:6443
    certificate-authority-data: ZmFrZS1jYQ==
  name: spoke
users:
- user:
    token: fake-token
  name: spoke-user
contexts:
- context:
    cluster: spoke
    user: spoke-user
  name: spoke
current-context: spoke
`),
				},
			}

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc, standingSecret).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{}
			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)
			reconciler.NewSpokeClient = func(cfg *rest.Config) (client.Client, error) {
				return nil, fmt.Errorf("spoke unreachable")
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			// Should still succeed and remove finalizer
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Check finalizer was removed or CR was deleted
			var updated hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)
			// Either the CR is gone (deleted) or the finalizer is removed
			if err == nil {
				Expect(updated.Finalizers).NotTo(ContainElement(spokeClusterFinalizer))
			}
		})

		It("should delete without standing kubeconfig", func() {
			sc := newSpokeClusterDeleting("test-spoke")

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{}
			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Check finalizer was removed or CR was deleted
			var updated hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)
			// Either the CR is gone (deleted) or the finalizer is removed
			if err == nil {
				Expect(updated.Finalizers).NotTo(ContainElement(spokeClusterFinalizer))
			}
		})

		It("should handle SpokeCluster not found", func() {
			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				Build()

			credSource := &fakeCredentialSource{}
			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent"},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
		})

		It("should be idempotent - reconcile twice converges", func() {
			sc := newSpokeClusterWithFinalizer("test-spoke")

			spokeScheme := newTestScheme()
			spokeClient := fake.NewClientBuilder().
				WithScheme(spokeScheme).
				Build()

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{
				cfg: &rest.Config{
					Host: "https://api.spoke.example.com:6443",
					TLSClientConfig: rest.TLSClientConfig{
						CAData: []byte("fake-ca"),
					},
					BearerToken: "fake-token",
				},
			}

			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)
			reconciler.NewSpokeClient = func(cfg *rest.Config) (client.Client, error) {
				return spokeClient, nil
			}
			reconciler.CheckConnectivity = func(cfg *rest.Config) error {
				return nil
			}

			// First reconcile
			result1, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result1.RequeueAfter).To(Equal(healthyRequeueAfter))

			var firstUpdate hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &firstUpdate)
			Expect(err).NotTo(HaveOccurred())

			condition1 := meta.FindStatusCondition(firstUpdate.Status.Conditions, conditionTypeConnected)
			Expect(condition1).NotTo(BeNil())
			Expect(condition1.Status).To(Equal(metav1.ConditionTrue))

			// Second reconcile
			result2, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result2.RequeueAfter).To(Equal(healthyRequeueAfter))

			var secondUpdate hubv1alpha1.SpokeCluster
			err = hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &secondUpdate)
			Expect(err).NotTo(HaveOccurred())

			condition2 := meta.FindStatusCondition(secondUpdate.Status.Conditions, conditionTypeConnected)
			Expect(condition2).NotTo(BeNil())
			Expect(condition2.Status).To(Equal(metav1.ConditionTrue))

			// Check standing kubeconfig still exists
			secretKey := types.NamespacedName{
				Name:      credential.StandingKubeconfigName(sc.Name),
				Namespace: testNamespace,
			}
			var secret corev1.Secret
			err = hubClient.Get(ctx, secretKey, &secret)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle spoke client creation failure", func() {
			sc := newSpokeClusterWithFinalizer("test-spoke")

			hubClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithObjects(sc).
				WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
				Build()

			credSource := &fakeCredentialSource{
				cfg: &rest.Config{
					Host: "https://api.spoke.example.com:6443",
					TLSClientConfig: rest.TLSClientConfig{
						CAData: []byte("fake-ca"),
					},
					BearerToken: "fake-token",
				},
			}

			reconciler := controller.NewSpokeClusterReconciler(hubClient, credSource, testNamespace)
			reconciler.CheckConnectivity = func(cfg *rest.Config) error {
				return nil
			}
			reconciler.NewSpokeClient = func(cfg *rest.Config) (client.Client, error) {
				return nil, fmt.Errorf("failed to create client")
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: sc.Name},
			})

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("creating spoke client"))
			Expect(result.RequeueAfter).To(BeZero())
		})
	})
})
