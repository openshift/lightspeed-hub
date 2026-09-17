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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAdapterServiceAccount(t *testing.T) {
	sa := adapterServiceAccount("openshift-lightspeed")
	if sa.Name != adapterName {
		t.Errorf("name = %q, want %q", sa.Name, adapterName)
	}
	if sa.Namespace != "openshift-lightspeed" {
		t.Errorf("namespace = %q, want openshift-lightspeed", sa.Namespace)
	}
}

func TestAdapterRole(t *testing.T) {
	role := adapterRole("openshift-lightspeed")
	if role.Name != adapterName {
		t.Errorf("name = %q, want %q", role.Name, adapterName)
	}
	// Must have AgenticRun create/list/get and Secret get
	wantRules := map[string][]string{
		"agentic.openshift.io": {"create", "list", "get"},
		"":                     {"get"},
	}
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			if expected, ok := wantRules[group]; ok {
				for _, verb := range expected {
					found := false
					for _, v := range rule.Verbs {
						if v == verb {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("missing verb %q for group %q", verb, group)
					}
				}
			}
		}
	}
}

func TestAdapterClusterRole(t *testing.T) {
	cr := adapterClusterRole()
	if cr.Name != adapterClusterRoleName {
		t.Errorf("name = %q, want %q", cr.Name, adapterClusterRoleName)
	}
	// Must have agenticolsconfigs get and spokeclusters list
	var hasOLSConfig, hasSpokeClusters bool
	for _, rule := range cr.Rules {
		for _, res := range rule.Resources {
			if res == "agenticolsconfigs" {
				hasOLSConfig = true
			}
			if res == "spokeclusters" {
				hasSpokeClusters = true
			}
		}
	}
	if !hasOLSConfig {
		t.Error("missing agenticolsconfigs rule")
	}
	if !hasSpokeClusters {
		t.Error("missing spokeclusters rule")
	}
	// Verify watch verb on spokeclusters
	for _, rule := range cr.Rules {
		for _, res := range rule.Resources {
			if res == "spokeclusters" {
				hasWatch := false
				for _, v := range rule.Verbs {
					if v == "watch" {
						hasWatch = true
					}
				}
				if !hasWatch {
					t.Error("missing watch verb on spokeclusters")
				}
			}
		}
	}
}

func TestAdapterDeployment(t *testing.T) {
	dep := adapterDeployment("openshift-lightspeed", "quay.io/test/adapter:v1")
	if dep.Name != adapterName {
		t.Errorf("name = %q, want %q", dep.Name, adapterName)
	}
	if dep.Namespace != "openshift-lightspeed" {
		t.Errorf("namespace = %q, want openshift-lightspeed", dep.Namespace)
	}
	if *dep.Spec.Replicas != 1 {
		t.Errorf("replicas = %d, want 1", *dep.Spec.Replicas)
	}
	container := dep.Spec.Template.Spec.Containers[0]
	if container.Image != "quay.io/test/adapter:v1" {
		t.Errorf("image = %q, want quay.io/test/adapter:v1", container.Image)
	}
	if dep.Spec.Template.Spec.ServiceAccountName != adapterName {
		t.Errorf("serviceAccountName = %q, want %q", dep.Spec.Template.Spec.ServiceAccountName, adapterName)
	}
	// Check config volume mount
	var hasConfigVolume bool
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == "config" && v.ConfigMap != nil && v.ConfigMap.Name == adapterConfigMapName {
			hasConfigVolume = true
		}
	}
	if !hasConfigVolume {
		t.Error("missing config volume from ConfigMap")
	}
	// Check --multicluster arg
	if len(container.Args) == 0 || container.Args[0] != "--multicluster" {
		t.Errorf("args = %v, want [--multicluster]", container.Args)
	}
	// Check security context
	if *dep.Spec.Template.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation should be false")
	}
}

func TestAdapterConfigMap(t *testing.T) {
	cm := adapterConfigMap("openshift-lightspeed")
	if cm.Name != adapterConfigMapName {
		t.Errorf("name = %q, want %q", cm.Name, adapterConfigMapName)
	}
	if _, ok := cm.Data["config.yaml"]; !ok {
		t.Error("missing config.yaml key")
	}
}

func TestAdapterMonitoringRoleBinding(t *testing.T) {
	rb := adapterMonitoringRoleBinding("openshift-lightspeed")
	if rb.Namespace != "openshift-monitoring" {
		t.Errorf("namespace = %q, want openshift-monitoring", rb.Namespace)
	}
	if rb.RoleRef.Name != "monitoring-alertmanager-view" {
		t.Errorf("roleRef.name = %q, want monitoring-alertmanager-view", rb.RoleRef.Name)
	}
	if rb.Subjects[0].Namespace != "openshift-lightspeed" {
		t.Errorf("subject namespace = %q, want openshift-lightspeed", rb.Subjects[0].Namespace)
	}
}

func adapterTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	return scheme
}

func TestEnsureAdapterStack_CreatesAllResources(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()

	err := ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1")
	if err != nil {
		t.Fatalf("ensureAdapterStack: %v", err)
	}

	// Verify SA
	var sa corev1.ServiceAccount
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterName, Namespace: "openshift-lightspeed"}, &sa); err != nil {
		t.Errorf("SA not found: %v", err)
	}
	// Verify Deployment
	var dep appsv1.Deployment
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterName, Namespace: "openshift-lightspeed"}, &dep); err != nil {
		t.Errorf("Deployment not found: %v", err)
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "quay.io/test/adapter:v1" {
		t.Errorf("image = %q, want quay.io/test/adapter:v1", dep.Spec.Template.Spec.Containers[0].Image)
	}
	// Verify ConfigMap
	var cm corev1.ConfigMap
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterConfigMapName, Namespace: "openshift-lightspeed"}, &cm); err != nil {
		t.Errorf("ConfigMap not found: %v", err)
	}
	// Verify ClusterRole
	var cr rbacv1.ClusterRole
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterClusterRoleName}, &cr); err != nil {
		t.Errorf("ClusterRole not found: %v", err)
	}
	// Verify ClusterRoleBinding
	var crb rbacv1.ClusterRoleBinding
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterClusterRoleName}, &crb); err != nil {
		t.Errorf("ClusterRoleBinding not found: %v", err)
	}
	// Verify monitoring RoleBinding
	var monRB rbacv1.RoleBinding
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterMonitoringRBName, Namespace: "openshift-monitoring"}, &monRB); err != nil {
		t.Errorf("monitoring RoleBinding not found: %v", err)
	}
}

func TestEnsureAdapterStack_Idempotent(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()

	if err := ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1"); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

func TestEnsureAdapterStack_UpdatesImage(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()

	if err := ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v2"); err != nil {
		t.Fatalf("update call: %v", err)
	}

	var dep appsv1.Deployment
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterName, Namespace: "openshift-lightspeed"}, &dep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "quay.io/test/adapter:v2" {
		t.Errorf("image not updated: got %q", dep.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestTeardownAdapterStack_DeletesAllResources(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()

	_ = ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1")
	if err := teardownAdapterStack(ctx, fc, "openshift-lightspeed"); err != nil {
		t.Fatalf("teardownAdapterStack: %v", err)
	}

	var dep appsv1.Deployment
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterName, Namespace: "openshift-lightspeed"}, &dep); !apierrors.IsNotFound(err) {
		t.Errorf("Deployment should be deleted, got err: %v", err)
	}
	var cr rbacv1.ClusterRole
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterClusterRoleName}, &cr); !apierrors.IsNotFound(err) {
		t.Errorf("ClusterRole should be deleted, got err: %v", err)
	}
}

func TestTeardownAdapterStack_NoOpWhenMissing(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()
	// Should not panic or error
	if err := teardownAdapterStack(ctx, fc, "openshift-lightspeed"); err != nil {
		t.Fatalf("teardownAdapterStack on empty cluster should not error: %v", err)
	}
}

func TestEnsureAdapterStack_UpdatesRBACRules(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()

	if err := ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Manually mutate the Role to simulate a stale version
	var role rbacv1.Role
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterName, Namespace: "openshift-lightspeed"}, &role); err != nil {
		t.Fatalf("get role: %v", err)
	}
	role.Rules = []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}
	if err := fc.Update(ctx, &role); err != nil {
		t.Fatalf("mutate role: %v", err)
	}

	// Re-ensure should converge the Role back
	if err := ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1"); err != nil {
		t.Fatalf("second call: %v", err)
	}

	if err := fc.Get(ctx, types.NamespacedName{Name: adapterName, Namespace: "openshift-lightspeed"}, &role); err != nil {
		t.Fatalf("get role after ensure: %v", err)
	}
	// Should have the original rules back (agenticruns + secrets), not the mutated one
	if len(role.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(role.Rules))
	}
}

func TestRestartAdapterDeployment_BumpsAnnotation(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()

	// Create the deployment first
	_ = ensureAdapterStack(ctx, fc, "openshift-lightspeed", "quay.io/test/adapter:v1")

	err := restartAdapterDeployment(ctx, fc, "openshift-lightspeed")
	if err != nil {
		t.Fatalf("restartAdapterDeployment: %v", err)
	}

	var dep appsv1.Deployment
	if err := fc.Get(ctx, types.NamespacedName{Name: adapterName, Namespace: "openshift-lightspeed"}, &dep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	ts1 := dep.Spec.Template.Annotations[adapterRestartAnnotation]
	if ts1 == "" {
		t.Fatal("restart annotation not set")
	}
}

func TestRestartAdapterDeployment_NoOpWhenMissing(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewClientBuilder().WithScheme(adapterTestScheme()).Build()

	// No deployment exists — should not error
	err := restartAdapterDeployment(ctx, fc, "openshift-lightspeed")
	if err != nil {
		t.Fatalf("should not error when deployment missing: %v", err)
	}
}
