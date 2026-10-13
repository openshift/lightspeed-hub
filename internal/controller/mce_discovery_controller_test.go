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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/controller"
	"github.com/openshift/lightspeed-hub/internal/credential"
)

func discoveryScheme() *runtime.Scheme {
	s := newTestScheme()
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cluster.open-cluster-management.io", Version: "v1", Kind: "ManagedClusterList"},
		&unstructured.UnstructuredList{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cluster.open-cluster-management.io", Version: "v1", Kind: "ManagedCluster"},
		&unstructured.Unstructured{},
	)
	// ManagedServiceAccount CRD for MSA creation/deletion tests
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "authentication.open-cluster-management.io", Version: "v1beta1", Kind: "ManagedServiceAccount"},
		&unstructured.Unstructured{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "authentication.open-cluster-management.io", Version: "v1beta1", Kind: "ManagedServiceAccountList"},
		&unstructured.UnstructuredList{},
	)
	// ManifestWork CRD for RBAC push tests
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "work.open-cluster-management.io", Version: "v1", Kind: "ManifestWork"},
		&unstructured.Unstructured{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "work.open-cluster-management.io", Version: "v1", Kind: "ManifestWorkList"},
		&unstructured.UnstructuredList{},
	)
	return s
}

func managedCluster(name, apiURL string, labels map[string]string) *unstructured.Unstructured {
	mc := &unstructured.Unstructured{}
	mc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.open-cluster-management.io",
		Version: "v1",
		Kind:    "ManagedCluster",
	})
	mc.SetName(name)
	if labels != nil {
		mc.SetLabels(labels)
	}
	_ = unstructured.SetNestedSlice(mc.Object, []interface{}{
		map[string]interface{}{
			"url": apiURL,
		},
	}, "spec", "managedClusterClientConfigs")
	return mc
}

func mceHubConfigWithSelector(matchLabels map[string]string) *hubv1alpha1.HubConfig {
	hc := &hubv1alpha1.HubConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: hubv1alpha1.HubConfigSpec{
			ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeMCE,
		},
	}
	if matchLabels != nil {
		hc.Spec.MCE = &hubv1alpha1.MCEConfig{
			Selector: &hubv1alpha1.MCESelector{
				MatchLabels: matchLabels,
			},
		}
	}
	return hc
}

func discoverySpoke(name, apiServer string) *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				credential.MCEManagedByLabel: credential.MCEManagedByValue,
			},
		},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: apiServer,
			CredentialSource: hubv1alpha1.CredentialSource{
				MCE: &hubv1alpha1.MCECredentialSource{
					ManagedClusterName: name,
				},
			},
		},
	}
}

var msaGVK = schema.GroupVersionKind{
	Group:   "authentication.open-cluster-management.io",
	Version: "v1beta1",
	Kind:    "ManagedServiceAccount",
}

// getMSA retrieves a ManagedServiceAccount by name/namespace using unstructured.
func getMSA(c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	msa := &unstructured.Unstructured{}
	msa.SetGroupVersionKind(msaGVK)
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, msa)
	return msa, err
}

var mwGVK = schema.GroupVersionKind{
	Group:   "work.open-cluster-management.io",
	Version: "v1",
	Kind:    "ManifestWork",
}

// existingMSA creates a ManagedServiceAccount fixture for tests that start
// with an existing MSA (e.g. deletion tests).
func existingMSA(namespace string) *unstructured.Unstructured {
	msa := &unstructured.Unstructured{}
	msa.SetGroupVersionKind(msaGVK)
	msa.SetName(credential.MSAName)
	msa.SetNamespace(namespace)
	msa.SetLabels(map[string]string{
		credential.MCEManagedByLabel: credential.MCEManagedByValue,
	})
	return msa
}

func newDiscoveryReconciler(hubClient client.Client, apiReader client.Reader) *controller.MCEDiscoveryReconciler {
	return controller.NewMCEDiscoveryReconciler(hubClient, apiReader)
}

var _ = Describe("MCEDiscoveryReconciler", func() {
	It("should create SpokeCluster CRs for matching ManagedClusters", func() {
		mc1 := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443",
			map[string]string{"lightspeed-enabled": "true"})
		mc2 := managedCluster("spoke-2", "https://api.spoke-2.example.com:6443",
			map[string]string{"lightspeed-enabled": "true"})
		hc := mceHubConfigWithSelector(map[string]string{"lightspeed-enabled": "true"})

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc1, mc2).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		result, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		// Verify SpokeClusters were created
		var sc1 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc1)).To(Succeed())
		Expect(sc1.Spec.APIServer).To(Equal("https://api.spoke-1.example.com:6443"))
		Expect(sc1.Spec.CredentialSource.MCE.ManagedClusterName).To(Equal("spoke-1"))
		Expect(sc1.Labels[credential.MCEManagedByLabel]).To(Equal(credential.MCEManagedByValue))

		var sc2 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-2"}, &sc2)).To(Succeed())
		Expect(sc2.Spec.APIServer).To(Equal("https://api.spoke-2.example.com:6443"))

		// Verify ManagedServiceAccounts were created in each spoke's namespace
		msa1, err := getMSA(hubClient, "spoke-1", credential.MSAName)
		Expect(err).NotTo(HaveOccurred(), "MSA should be created for spoke-1")
		Expect(msa1.GetLabels()[credential.MCEManagedByLabel]).To(Equal(credential.MCEManagedByValue))

		_, err = getMSA(hubClient, "spoke-2", credential.MSAName)
		Expect(err).NotTo(HaveOccurred(), "MSA should be created for spoke-2")

		// Verify ManifestWorks were created for spoke RBAC
		var mw1 unstructured.Unstructured
		mw1.SetGroupVersionKind(mwGVK)
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "lightspeed-hub-msa-rbac", Namespace: "spoke-1"}, &mw1)).To(Succeed(),
			"RBAC ManifestWork should be created for spoke-1")
	})

	It("should exclude hub self-import (local-cluster=true)", func() {
		localCluster := managedCluster("local-cluster", "https://api.hub.example.com:6443",
			map[string]string{"local-cluster": "true", "lightspeed-enabled": "true"})
		spoke := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443",
			map[string]string{"lightspeed-enabled": "true"})
		hc := mceHubConfigWithSelector(map[string]string{"lightspeed-enabled": "true"})

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(localCluster, spoke).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// spoke-1 should be created
		var sc1 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc1)).To(Succeed())

		// local-cluster should NOT be created
		var scLocal hubv1alpha1.SpokeCluster
		err = hubClient.Get(ctx, types.NamespacedName{Name: "local-cluster"}, &scLocal)
		Expect(err).To(HaveOccurred())
		Expect(client.IgnoreNotFound(err)).To(Succeed())
	})

	It("should include all non-hub clusters when selector is nil", func() {
		spoke1 := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443", nil)
		spoke2 := managedCluster("spoke-2", "https://api.spoke-2.example.com:6443",
			map[string]string{"env": "prod"})
		localCluster := managedCluster("local-cluster", "https://api.hub.example.com:6443",
			map[string]string{"local-cluster": "true"})
		// HubConfig with no selector
		hc := mceHubConfigWithSelector(nil)

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(spoke1, spoke2, localCluster).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// Both spokes created, hub excluded
		var sc1, sc2 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc1)).To(Succeed())
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-2"}, &sc2)).To(Succeed())

		var scLocal hubv1alpha1.SpokeCluster
		err = hubClient.Get(ctx, types.NamespacedName{Name: "local-cluster"}, &scLocal)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
	})

	It("should filter by selector matchLabels", func() {
		matching := managedCluster("spoke-match", "https://api.spoke-match.example.com:6443",
			map[string]string{"lightspeed-enabled": "true", "env": "prod"})
		nonMatching := managedCluster("spoke-nomatch", "https://api.spoke-nomatch.example.com:6443",
			map[string]string{"env": "staging"})
		hc := mceHubConfigWithSelector(map[string]string{"lightspeed-enabled": "true"})

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(matching, nonMatching).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// Matching spoke created
		var sc hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-match"}, &sc)).To(Succeed())

		// Non-matching spoke NOT created
		err = hubClient.Get(ctx, types.NamespacedName{Name: "spoke-nomatch"}, &sc)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
	})

	It("should delete SpokeCluster and MSA when ManagedCluster is removed", func() {
		// Start with a discovery-owned SpokeCluster, its MSA, and RBAC ManifestWork
		existingSpoke := discoverySpoke("spoke-1", "https://api.spoke-1.example.com:6443")
		msa := existingMSA("spoke-1")
		rbacMW := &unstructured.Unstructured{}
		rbacMW.SetGroupVersionKind(mwGVK)
		rbacMW.SetName("lightspeed-hub-msa-rbac")
		rbacMW.SetNamespace("spoke-1")
		rbacMW.SetLabels(map[string]string{
			credential.MCEManagedByLabel: credential.MCEManagedByValue,
		})
		hc := mceHubConfigWithSelector(nil)

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc, existingSpoke, msa, rbacMW).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		// No ManagedClusters — spoke-1 should be deleted
		apiReader := fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// SpokeCluster should be deleted
		var sc hubv1alpha1.SpokeCluster
		err = hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())

		// MSA should be deleted
		_, err = getMSA(hubClient, "spoke-1", credential.MSAName)
		Expect(err).To(HaveOccurred(), "MSA should be deleted when SpokeCluster is removed")

		// RBAC ManifestWork should be deleted
		var mw unstructured.Unstructured
		mw.SetGroupVersionKind(mwGVK)
		err = hubClient.Get(ctx, types.NamespacedName{Name: "lightspeed-hub-msa-rbac", Namespace: "spoke-1"}, &mw)
		Expect(err).To(HaveOccurred(), "RBAC ManifestWork should be deleted")
	})

	It("should delete SpokeCluster when labels no longer match", func() {
		existingSpoke := discoverySpoke("spoke-1", "https://api.spoke-1.example.com:6443")
		// ManagedCluster no longer has the matching label
		mc := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443",
			map[string]string{"env": "staging"})
		hc := mceHubConfigWithSelector(map[string]string{"lightspeed-enabled": "true"})

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc, existingSpoke).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		var sc hubv1alpha1.SpokeCluster
		err = hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
	})

	It("should update SpokeCluster API server when ManagedCluster URL changes", func() {
		existingSpoke := discoverySpoke("spoke-1", "https://old-api.spoke-1.example.com:6443")
		mc := managedCluster("spoke-1", "https://new-api.spoke-1.example.com:6443", nil)
		hc := mceHubConfigWithSelector(nil)

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc, existingSpoke).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		var sc hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc)).To(Succeed())
		Expect(sc.Spec.APIServer).To(Equal("https://new-api.spoke-1.example.com:6443"))
	})

	It("should not create SpokeCluster when name collides with manual CR", func() {
		// Manual SpokeCluster without discovery label
		manualSpoke := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "spoke-1",
				UID:  types.UID("manual-uid"),
			},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer: "https://manual-api.example.com:6443",
				CredentialSource: hubv1alpha1.CredentialSource{
					Secret: &hubv1alpha1.SecretCredentialSource{
						Name: "manual-secret", Namespace: "default",
					},
				},
			},
		}
		mc := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443", nil)
		hc := mceHubConfigWithSelector(nil)

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc, manualSpoke).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// Manual SpokeCluster should be untouched
		var sc hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc)).To(Succeed())
		Expect(sc.Spec.APIServer).To(Equal("https://manual-api.example.com:6443"))
		// Should NOT have the discovery label
		Expect(sc.Labels).NotTo(HaveKey(credential.MCEManagedByLabel))
	})

	It("should do nothing when HubConfig is in secret mode", func() {
		mc := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443", nil)
		hc := defaultHubConfig() // secret mode

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// No SpokeCluster should be created
		var scList hubv1alpha1.SpokeClusterList
		Expect(hubClient.List(ctx, &scList)).To(Succeed())
		Expect(scList.Items).To(BeEmpty())
	})

	It("should clean up discovery spokes and MSAs when HubConfig is deleted", func() {
		existingSpoke := discoverySpoke("spoke-1", "https://api.spoke-1.example.com:6443")
		msa := existingMSA("spoke-1")
		rbacMW := &unstructured.Unstructured{}
		rbacMW.SetGroupVersionKind(mwGVK)
		rbacMW.SetName("lightspeed-hub-msa-rbac")
		rbacMW.SetNamespace("spoke-1")
		rbacMW.SetLabels(map[string]string{
			credential.MCEManagedByLabel: credential.MCEManagedByValue,
		})
		// No HubConfig

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(existingSpoke, msa, rbacMW).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// Discovery SpokeCluster should be deleted
		var sc hubv1alpha1.SpokeCluster
		err = hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())

		// MSA should be deleted
		_, err = getMSA(hubClient, "spoke-1", credential.MSAName)
		Expect(err).To(HaveOccurred(), "MSA should be cleaned up with discovery spokes")
	})

	It("should skip ManagedClusters with no HTTPS URL", func() {
		mc := managedCluster("spoke-nourl", "", nil)
		// Override to have no client configs
		_ = unstructured.SetNestedSlice(mc.Object, []interface{}{}, "spec", "managedClusterClientConfigs")
		hc := mceHubConfigWithSelector(nil)

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// No SpokeCluster for the broken ManagedCluster
		var scList hubv1alpha1.SpokeClusterList
		Expect(hubClient.List(ctx, &scList)).To(Succeed())
		Expect(scList.Items).To(BeEmpty())
	})

	It("should be idempotent — second reconcile does not duplicate CRs", func() {
		mc := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443", nil)
		hc := mceHubConfigWithSelector(nil)

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		// First reconcile
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// Second reconcile — should not error or create duplicates
		_, err = reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		var scList hubv1alpha1.SpokeClusterList
		Expect(hubClient.List(ctx, &scList)).To(Succeed())
		Expect(scList.Items).To(HaveLen(1))
		Expect(scList.Items[0].Name).To(Equal("spoke-1"))
	})

	It("should handle selector change by removing non-matching spokes", func() {
		mc1 := managedCluster("spoke-1", "https://api.spoke-1.example.com:6443",
			map[string]string{"lightspeed-enabled": "true"})
		mc2 := managedCluster("spoke-2", "https://api.spoke-2.example.com:6443",
			map[string]string{"env": "staging"})
		// Start with both discovered (nil selector)
		existingSpoke1 := discoverySpoke("spoke-1", "https://api.spoke-1.example.com:6443")
		existingSpoke2 := discoverySpoke("spoke-2", "https://api.spoke-2.example.com:6443")
		// Now selector requires lightspeed-enabled=true
		hc := mceHubConfigWithSelector(map[string]string{"lightspeed-enabled": "true"})

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc, existingSpoke1, existingSpoke2).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mc1, mc2).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// spoke-1 should remain, spoke-2 should be deleted
		var sc1 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &sc1)).To(Succeed())

		var sc2 hubv1alpha1.SpokeCluster
		err = hubClient.Get(ctx, types.NamespacedName{Name: "spoke-2"}, &sc2)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
	})

	It("should not delete manual SpokeClusters when not in desired set", func() {
		// A manually created SpokeCluster (no discovery label) should not be touched
		manualSpoke := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "manual-spoke",
				UID:  types.UID("manual-uid"),
			},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer: "https://manual.example.com:6443",
				CredentialSource: hubv1alpha1.CredentialSource{
					Secret: &hubv1alpha1.SecretCredentialSource{
						Name: "s", Namespace: "default",
					},
				},
			},
		}
		hc := mceHubConfigWithSelector(nil)

		scheme := discoveryScheme()
		hubClient := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(hc, manualSpoke).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()
		apiReader := fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := newDiscoveryReconciler(hubClient, apiReader)

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		// Manual SpokeCluster should still exist
		var sc hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "manual-spoke"}, &sc)).To(Succeed())
	})
})
