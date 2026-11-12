package controller_test

import (
	"context"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	hub "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/provisioner"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Agent RBAC schema", func() {
	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &hub.HubConfig{})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &hub.SpokeCluster{})).To(Succeed())
	})
	DescribeTable("validates role names on both APIs", func(name string, valid bool) {
		roles := []hub.ClusterRoleName{hub.ClusterRoleName(name)}
		hc := defaultHubConfig()
		hc.Spec.AgentRBAC = &hub.FleetAgentRBAC{DefaultClusterRoles: &roles}
		sc := newSpokeCluster("rbac-schema")
		sc.Spec.AgentRBAC = &hub.SpokeAgentRBAC{AdditionalClusterRoles: roles}
		for _, obj := range []client.Object{hc, sc} {
			err := k8sClient.Create(ctx, obj)
			if valid {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
			}
		}
	}, Entry("colon name", "custom:reader", true), Entry("empty", "", false), Entry("slash", "a/b", false), Entry("percent", "a%b", false), Entry("dot", ".", false), Entry("dotdot", "..", false))
	It("persists explicit empty and omitted fleet lists distinctly", func() {
		hc := defaultHubConfig()
		hc.Spec.AgentRBAC = &hub.FleetAgentRBAC{DefaultClusterRoles: &[]hub.ClusterRoleName{}}
		Expect(k8sClient.Create(ctx, hc)).To(Succeed())
		var fetched hub.HubConfig
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, &fetched)).To(Succeed())
		Expect(fetched.Spec.AgentRBAC.DefaultClusterRoles).NotTo(BeNil())
		Expect(*fetched.Spec.AgentRBAC.DefaultClusterRoles).To(BeEmpty())
		fetched.Spec.AgentRBAC.DefaultClusterRoles = nil
		Expect(k8sClient.Update(ctx, &fetched)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, &fetched)).To(Succeed())
		Expect(fetched.Spec.AgentRBAC.DefaultClusterRoles).To(BeNil())
	})
	It("accepts duplicate roles", func() {
		hc := defaultHubConfig()
		hc.Spec.AgentRBAC = &hub.FleetAgentRBAC{DefaultClusterRoles: &[]hub.ClusterRoleName{"reader", "reader"}}
		Expect(k8sClient.Create(ctx, hc)).To(Succeed())
	})
})

func TestReaderConfigurationReconciles(t *testing.T) {
	ctx := context.Background()
	sc := newSpokeClusterWithFinalizer("reader-test")
	hc := defaultHubConfig()
	hubClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(sc, hc).WithStatusSubresource(&hub.SpokeCluster{}).Build()
	spokeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	reconciler := newReconcilerWithAdapterSupport(hubClient, &fakeCredentialSource{cfg: &rest.Config{Host: sc.Spec.APIServer, BearerToken: "test"}}, spokeClient, "default")
	reconcile := func(wantError bool) {
		t.Helper()
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(sc)})
		if (err != nil) != wantError {
			t.Fatalf("reconcile: %v", err)
		}
	}
	// Default-role contents may include permissions rejected for custom roles.
	for _, name := range []string{"cluster-reader", "cluster-monitoring-view"} {
		var role rbac.ClusterRole
		if err := spokeClient.Get(ctx, client.ObjectKey{Name: name}, &role); err != nil {
			t.Fatal(err)
		}
		role.Rules = []rbac.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}
		if err := spokeClient.Update(ctx, &role); err != nil {
			t.Fatal(err)
		}
	}
	reconcile(false)
	if err := hubClient.Get(ctx, client.ObjectKeyFromObject(sc), sc); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(sc.Status.Conditions, "Provisioned") {
		t.Fatal("exempt defaults did not provision")
	}
	for _, name := range []string{provisioner.ClusterRoleBindingClusterReader, provisioner.ClusterRoleBindingMonitoringView} {
		var binding rbac.ClusterRoleBinding
		if err := spokeClient.Get(ctx, client.ObjectKey{Name: name}, &binding); err != nil {
			t.Fatal(err)
		}
	}
	hc.Spec.AgentRBAC = &hub.FleetAgentRBAC{DefaultClusterRoles: &[]hub.ClusterRoleName{}}
	if err := hubClient.Update(ctx, hc); err != nil {
		t.Fatal(err)
	}
	reconcile(false)
	var bindings rbac.ClusterRoleBindingList
	if err := spokeClient.List(ctx, &bindings, client.MatchingLabels{provisioner.ReaderOwnerLabel: string(sc.UID)}); err != nil {
		t.Fatal(err)
	}
	if len(bindings.Items) != 0 {
		t.Fatal("empty defaults did not remove bindings")
	}
	if err := hubClient.Get(ctx, client.ObjectKeyFromObject(sc), sc); err != nil {
		t.Fatal(err)
	}
	sc.Spec.AgentRBAC = &hub.SpokeAgentRBAC{AdditionalClusterRoles: []hub.ClusterRoleName{"missing-role"}}
	if err := hubClient.Update(ctx, sc); err != nil {
		t.Fatal(err)
	}
	reconcile(true)
	if err := hubClient.Get(ctx, client.ObjectKeyFromObject(sc), sc); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(sc.Status.Conditions, "Provisioned")
	if condition == nil || condition.Status != metav1.ConditionFalse || !strings.Contains(condition.Message, "missing-role") {
		t.Fatalf("expected provisioning failure: %v", condition)
	}
	role := &rbac.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "missing-role"}, Rules: []rbac.PolicyRule{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}}}
	if err := spokeClient.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	reconcile(false)
	role.Rules[0].Verbs = []string{"update"}
	if err := spokeClient.Update(ctx, role); err != nil {
		t.Fatal(err)
	}
	reconcile(true)
	// HubConfig removal cleans custom bindings without consulting the now-unsafe role.
	if err := hubClient.Delete(ctx, hc); err != nil {
		t.Fatal(err)
	}
	reconcile(false)
	if err := spokeClient.List(ctx, &bindings, client.MatchingLabels{provisioner.ReaderOwnerLabel: string(sc.UID)}); err != nil {
		t.Fatal(err)
	}
	if len(bindings.Items) != 0 {
		t.Fatal("HubConfig cleanup retained custom bindings")
	}
	if err := spokeClient.Get(ctx, client.ObjectKeyFromObject(role), role); err != nil {
		t.Fatal("cleanup deleted administrator role")
	}
}
