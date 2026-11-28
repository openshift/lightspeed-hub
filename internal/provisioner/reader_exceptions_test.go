package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"

	hub "github.com/openshift/lightspeed-hub/api/v1alpha1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReaderPolicyExactNameExceptions(t *testing.T) {
	rules := []rbac.PolicyRule{
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"update"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}},
		{APIGroups: []string{rbac.GroupName}, Resources: []string{"roles"}, Verbs: []string{"list"}},
		{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}},
	}
	for _, name := range []string{"cluster-reader", "cluster-monitoring-view", "custom-reader", "Cluster-reader", "cluster-reader-copy", "prefix-cluster-monitoring-view", ClusterRoleBindingClusterReader, ClusterRoleBindingMonitoringView} {
		t.Run(name, func(t *testing.T) {
			exempt := name == "cluster-reader" || name == "cluster-monitoring-view"
			for _, rule := range rules {
				role := &rbac.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: []rbac.PolicyRule{rule}, AggregationRule: &rbac.AggregationRule{ClusterRoleSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"aggregate": "true"}}}}}
				if err := validateReaderRole(role); (err == nil) != exempt {
					t.Fatalf("role %q: error=%v, exempt=%v", name, err, exempt)
				}
			}
		})
	}
}

func TestExemptRolesStillRequireReads(t *testing.T) {
	for _, name := range []string{"cluster-reader", "cluster-monitoring-view"} {
		for _, failure := range []string{"missing", "forbidden"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				c := fake.NewClientBuilder().WithScheme(readerScheme()).WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if failure == "forbidden" {
							return apierrors.NewForbidden(schema.GroupResource{Group: rbac.GroupName, Resource: "clusterroles"}, name, errors.New("denied"))
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).Build()
				err := reconcileReaderBindings(context.Background(), c, "uid", []string{name})
				if err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("expected role read failure, got %v", err)
				}
				if failure == "missing" && !apierrors.IsNotFound(err) {
					t.Fatal(err)
				}
				if failure == "forbidden" && !apierrors.IsForbidden(err) {
					t.Fatal(err)
				}
				var bindings rbac.ClusterRoleBindingList
				if err := c.List(context.Background(), &bindings); err != nil {
					t.Fatal(err)
				}
				if len(bindings.Items) != 0 {
					t.Fatal("created binding without reading exempt role")
				}
			})
		}
	}
}

func TestReaderExceptionsIndependentOfConfigurationOrigin(t *testing.T) {
	for _, origin := range []string{"omitted", "explicit", "additional", "custom-default"} {
		t.Run(origin, func(t *testing.T) {
			hc := &hub.HubConfig{}
			sc := &hub.SpokeCluster{}
			switch origin {
			case "explicit":
				hc.Spec.AgentRBAC = &hub.FleetAgentRBAC{DefaultClusterRoles: &[]hub.ClusterRoleName{"cluster-reader", "cluster-monitoring-view"}}
			case "additional":
				hc.Spec.AgentRBAC = &hub.FleetAgentRBAC{DefaultClusterRoles: &[]hub.ClusterRoleName{}}
				sc.Spec.AgentRBAC = &hub.SpokeAgentRBAC{AdditionalClusterRoles: []hub.ClusterRoleName{"cluster-reader", "cluster-monitoring-view"}}
			case "custom-default":
				hc.Spec.AgentRBAC = &hub.FleetAgentRBAC{DefaultClusterRoles: &[]hub.ClusterRoleName{"custom-reader"}}
			}
			roles, err := ResolveReaderRoles(hc, sc)
			if err != nil {
				t.Fatal(err)
			}
			var objects []client.Object
			for _, name := range roles {
				objects = append(objects, &rbac.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: []rbac.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}})
			}
			c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(objects...).Build()
			err = reconcileReaderBindings(context.Background(), c, "uid", roles)
			if (err != nil) != (origin == "custom-default") {
				t.Fatalf("origin %s: %v", origin, err)
			}
		})
	}
}

func TestExemptRoleBindingOwnershipAndAuthorization(t *testing.T) {
	for _, name := range []string{"cluster-reader", "cluster-monitoring-view"} {
		for _, failure := range []string{"collision", "forbidden"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				role := safeReader(name)
				role.Rules[0].Verbs = []string{"update"}
				objects := []client.Object{role}
				foreign := &rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: readerBindingName(name)}, RoleRef: rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "ClusterRole", Name: name}}
				if failure == "collision" {
					objects = append(objects, foreign)
				}
				c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if failure == "forbidden" {
							return apierrors.NewForbidden(schema.GroupResource{Group: rbac.GroupName, Resource: "clusterrolebindings"}, obj.GetName(), errors.New("bind denied"))
						}
						return c.Create(ctx, obj, opts...)
					},
				}).Build()
				err := reconcileReaderBindings(context.Background(), c, "uid", []string{name})
				if err == nil {
					t.Fatal("exemption bypassed binding checks")
				}
				if failure == "forbidden" && !apierrors.IsForbidden(err) {
					t.Fatal(err)
				}
				if failure == "collision" {
					if !strings.Contains(err.Error(), "ownership collision") {
						t.Fatal(err)
					}
					if err := removeReaderBindings(context.Background(), c, "uid", nil); err != nil {
						t.Fatal(err)
					}
					if err := c.Get(context.Background(), client.ObjectKeyFromObject(foreign), foreign); err != nil {
						t.Fatal("foreign binding removed")
					}
					if len(foreign.Labels) != 0 {
						t.Fatal("foreign binding adopted")
					}
				}
			})
		}
	}
}
