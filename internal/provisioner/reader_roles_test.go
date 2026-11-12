package provisioner

import (
	"context"
	"strings"
	"testing"

	hub "github.com/openshift/lightspeed-hub/api/v1alpha1"
	rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResolveReaderRoles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		defaults  *[]hub.ClusterRoleName
		additions []hub.ClusterRoleName
		want      string
		invalid   bool
	}{
		{name: "omitted", want: "cluster-monitoring-view,cluster-reader"},
		{name: "empty", defaults: &[]hub.ClusterRoleName{}, want: ""},
		{name: "replacement additive duplicates", defaults: &[]hub.ClusterRoleName{"custom:reader", "custom:reader"}, additions: []hub.ClusterRoleName{"other", "custom:reader"}, want: "custom:reader,other"},
		{name: "invalid empty", defaults: &[]hub.ClusterRoleName{""}, invalid: true},
		{name: "invalid slash", defaults: &[]hub.ClusterRoleName{"a/b"}, invalid: true},
		{name: "invalid dot", defaults: &[]hub.ClusterRoleName{".."}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roles, err := ResolveReaderRoles(&hub.HubConfig{Spec: hub.HubConfigSpec{AgentRBAC: &hub.FleetAgentRBAC{DefaultClusterRoles: tc.defaults}}}, &hub.SpokeCluster{Spec: hub.SpokeClusterSpec{AgentRBAC: &hub.SpokeAgentRBAC{AdditionalClusterRoles: tc.additions}}})
			if (err != nil) != tc.invalid || strings.Join(roles, ",") != tc.want {
				t.Fatalf("roles=%v err=%v", roles, err)
			}
		})
	}
}

func TestReaderPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, group, resource, verb string
		unsafe                      bool
	}{
		{"read", "apps", "deployments", "get", false},
		{"secret", "", "secrets", "get", true},
		{"normalized core", "core", "secrets", "get", true},
		{"core version", "core/v1", "secrets", "get", true},
		{"secret subresource", "", "secrets/status", "get", true},
		{"wildcard subresource", "", "*/status", "get", true},
		{"rbac", rbac.GroupName, "roles", "list", true},
		{"wildcard group", "*", "roles", "get", true},
		{"wildcard core resource", "", "*", "watch", true},
		{"safe wildcard resource", "apps", "*", "get", false},
		{"write", "apps", "deployments", "update", true},
		{"wildcard verb", "apps", "deployments", "*", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role := &rbac.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Rules: []rbac.PolicyRule{{APIGroups: []string{tc.group}, Resources: []string{tc.resource}, Verbs: []string{tc.verb}}}}
			if err := validateReaderRole(role); (err != nil) != tc.unsafe {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestOwnedReaderBindings(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = rbac.AddToScheme(scheme)
	role := &rbac.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "custom"}, Rules: []rbac.PolicyRule{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(role).Build()
	if err := reconcileReaderBindings(ctx, c, "registration", []string{"custom"}); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Name: readerBindingName("custom")}
	var binding rbac.ClusterRoleBinding
	if err := c.Get(ctx, key, &binding); err != nil {
		t.Fatal(err)
	}
	binding.Subjects = nil
	binding.Annotations = map[string]string{"administrator-note": "preserve"}
	if err := c.Update(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	if err := reconcileReaderBindings(ctx, c, "registration", []string{"custom"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &binding); err != nil {
		t.Fatal(err)
	}
	if len(binding.Subjects) != 1 {
		t.Fatal("subject drift not repaired")
	}
	if binding.Annotations["administrator-note"] != "preserve" {
		t.Fatal("unrelated metadata lost")
	}
	if err := reconcileReaderBindings(ctx, c, "other-registration", []string{"custom"}); err == nil {
		t.Fatal("foreign ownership accepted")
	}
	if err := reconcileReaderBindings(ctx, c, "registration", nil); err != nil {
		t.Fatal(err)
	}
	var bindings rbac.ClusterRoleBindingList
	if err := c.List(ctx, &bindings); err != nil {
		t.Fatal(err)
	}
	if len(bindings.Items) != 0 {
		t.Fatal("obsolete binding retained")
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "custom"}, role); err != nil {
		t.Fatal("role definition removed")
	}
}
