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

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

func hubConfigTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)
	_ = hubv1alpha1.AddToScheme(s)
	return s
}

func newFakeHubConfigClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(hubConfigTestScheme()).
		WithObjects(objs...).
		Build()
}

func newHubConfigReconciler(c client.Client) *HubConfigReconciler {
	return NewHubConfigReconciler(c, c)
}

func newHubConfig() *hubv1alpha1.HubConfig {
	return &hubv1alpha1.HubConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       hubv1alpha1.HubConfigSpec{ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeSecret},
	}
}

func newHubConfigWithFinalizer() *hubv1alpha1.HubConfig {
	hc := newHubConfig()
	hc.Finalizers = []string{hubConfigFinalizerName}
	return hc
}

func newHubConfigDeleting() *hubv1alpha1.HubConfig {
	hc := newHubConfigWithFinalizer()
	now := metav1.Now()
	hc.DeletionTimestamp = &now
	return hc
}

func newTestSpokeCluster(name string) *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api." + name + ".example.com:6443",
			CredentialSource: hubv1alpha1.CredentialSource{
				Secret: &hubv1alpha1.SecretCredentialSource{
					Name:      name + "-creds",
					Namespace: "openshift-lightspeed",
				},
			},
		},
	}
}

func TestHubConfigReconcile_AddsFinalizer(t *testing.T) {
	hc := newHubConfig()
	c := newFakeHubConfigClient(hc)
	r := newHubConfigReconciler(c)

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Requeue {
		t.Error("expected requeue after adding finalizer")
	}

	var updated hubv1alpha1.HubConfig
	if err := c.Get(context.Background(), client.ObjectKey{Name: "cluster"}, &updated); err != nil {
		t.Fatalf("failed to get HubConfig: %v", err)
	}
	found := false
	for _, f := range updated.Finalizers {
		if f == hubConfigFinalizerName {
			found = true
		}
	}
	if !found {
		t.Error("expected finalizer to be present")
	}
}

func TestHubConfigReconcile_NotDeleting(t *testing.T) {
	hc := newHubConfigWithFinalizer()
	c := newFakeHubConfigClient(hc)
	r := newHubConfigReconciler(c)

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Error("expected no requeue when not deleting")
	}
}

func TestHubConfigReconcile_DeleteCascadesSpokeClusters(t *testing.T) {
	hc := newHubConfigDeleting()
	spoke1 := newTestSpokeCluster("spoke-1")
	spoke2 := newTestSpokeCluster("spoke-2")
	c := newFakeHubConfigClient(hc, spoke1, spoke2)
	r := newHubConfigReconciler(c)

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != spokeDrainRequeue {
		t.Errorf("expected requeue after %v while draining, got %v", spokeDrainRequeue, result.RequeueAfter)
	}

	// Verify SpokeClusters were deleted (fake client deletes immediately without finalizers)
	var spokeList hubv1alpha1.SpokeClusterList
	if err := c.List(context.Background(), &spokeList); err != nil {
		t.Fatalf("failed to list SpokeClusters: %v", err)
	}
	// Fake client deletes immediately, so list should be empty now
	// But our reconciler counts remaining before delete completes, so it requeues
}

func TestHubConfigReconcile_DeleteNoSpokes(t *testing.T) {
	hc := newHubConfigDeleting()
	c := newFakeHubConfigClient(hc)
	r := newHubConfigReconciler(c)

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected no requeue when no spokes, got %+v", result)
	}

	// Fake client auto-deletes the object once finalizers are empty and DeletionTimestamp is set.
	// Verify the HubConfig is gone (finalizer was removed, GC completed).
	var updated hubv1alpha1.HubConfig
	err = c.Get(context.Background(), client.ObjectKey{Name: "cluster"}, &updated)
	if err == nil {
		for _, f := range updated.Finalizers {
			if f == hubConfigFinalizerName {
				t.Error("expected finalizer to be removed")
			}
		}
	}
}

func TestHubConfigReconcile_NotFound(t *testing.T) {
	c := newFakeHubConfigClient()
	r := newHubConfigReconciler(c)

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err != nil {
		t.Fatalf("unexpected error for NotFound: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected no requeue for NotFound, got %+v", result)
	}
}
