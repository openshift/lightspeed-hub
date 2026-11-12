package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func readerScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = rbac.AddToScheme(s)
	_ = core.AddToScheme(s)
	return s
}
func safeReader(name string) *rbac.ClusterRole {
	return &rbac.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: []rbac.PolicyRule{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}}}
}

func TestReaderValidationBeforeMutation(t *testing.T) {
	for _, kind := range []string{"missing", "unsafe", "forbidden"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			role := safeReader("bad")
			role.Rules[0].Verbs = []string{"create"}
			objects := []client.Object{safeReader("good")}
			if kind == "unsafe" {
				objects = append(objects, role)
			}
			c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if kind == "forbidden" && key.Name == "bad" {
					return apierrors.NewForbidden(schema.GroupResource{Group: rbac.GroupName, Resource: "clusterroles"}, "bad", errors.New("denied"))
				}
				return c.Get(ctx, key, obj, opts...)
			}}).Build()
			err := reconcileReaderBindings(ctx, c, "uid", []string{"good", "bad"})
			if err == nil || !strings.Contains(err.Error(), "bad") {
				t.Fatalf("expected contextual failure: %v", err)
			}
			var bindings rbac.ClusterRoleBindingList
			if err := c.List(ctx, &bindings); err != nil {
				t.Fatal(err)
			}
			if len(bindings.Items) > 0 {
				t.Fatal("mutated bindings before full validation")
			}
		})
	}
}

func TestForeignBindingsAndCleanup(t *testing.T) {
	ctx := context.Background()
	role := safeReader("custom")
	foreign := &rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: readerBindingName("custom")}, RoleRef: rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "ClusterRole", Name: "custom"}, Subjects: []rbac.Subject{{Kind: "ServiceAccount", Name: ServiceAccountName, Namespace: ManagedNamespace}}}
	c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(role, foreign).Build()
	if err := reconcileReaderBindings(ctx, c, "uid", []string{"custom"}); err == nil {
		t.Fatal("unowned exact-match collision adopted")
	}
	if err := reconcileReaderBindings(ctx, c, "uid", nil); err != nil {
		t.Fatal(err)
	}
	if err := Deprovision(ctx, c, logr.Discard(), "uid"); err != nil {
		t.Fatal(err)
	}
	var actual rbac.ClusterRoleBinding
	if err := c.Get(ctx, client.ObjectKeyFromObject(foreign), &actual); err != nil {
		t.Fatal("foreign binding removed")
	}
	if len(actual.Labels) != 0 {
		t.Fatal("foreign binding labeled")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(role), role); err != nil {
		t.Fatal("role removed")
	}
}

func TestOwnedRoleRefReplacement(t *testing.T) {
	ctx := context.Background()
	binding := &rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: readerBindingName("custom"), Labels: readerLabels("uid")}, RoleRef: rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "ClusterRole", Name: "wrong"}}
	c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(safeReader("custom"), binding).Build()
	if err := reconcileReaderBindings(ctx, c, "uid", []string{"custom"}); err == nil {
		t.Fatal("replacement must retry")
	}
	if err := reconcileReaderBindings(ctx, c, "uid", []string{"custom"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(binding), binding); err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Name != "custom" {
		t.Fatal("roleRef not repaired")
	}
}

func TestReaderAPIFailures(t *testing.T) {
	for _, operation := range []string{"create", "list", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			injected := errors.New("authorization denied")
			binding := &rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: readerBindingName("custom"), Labels: readerLabels("uid")}, RoleRef: rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "ClusterRole", Name: "custom"}}
			objects := []client.Object{safeReader("custom")}
			if operation == "update" || operation == "delete" {
				objects = append(objects, binding)
			}
			c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if operation == "create" {
						return injected
					}
					return c.Create(ctx, obj, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if operation == "list" {
						return injected
					}
					return c.List(ctx, list, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if operation == "update" {
						return injected
					}
					return c.Update(ctx, obj, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if operation == "delete" {
						return injected
					}
					return c.Delete(ctx, obj, opts...)
				},
			}).Build()
			roles := []string{"custom"}
			if operation == "delete" {
				roles = nil
			}
			if err := reconcileReaderBindings(ctx, c, "uid", roles); !errors.Is(err, injected) {
				t.Fatalf("expected wrapped API error: %v", err)
			}
		})
	}
}

func TestReaderDeletionPreconditions(t *testing.T) {
	ctx := context.Background()
	binding := &rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "obsolete", UID: "binding-uid", Labels: readerLabels("uid")}}
	c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(binding).WithInterceptorFuncs(interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		options := &client.DeleteOptions{}
		for _, opt := range opts {
			opt.ApplyToDelete(options)
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != obj.GetUID() || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != obj.GetResourceVersion() {
			t.Fatal("unguarded deletion")
		}
		return c.Delete(ctx, obj, opts...)
	}}).Build()
	if err := removeReaderBindings(ctx, c, "uid", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAggregatedRoleRevalidated(t *testing.T) {
	ctx := context.Background()
	role := safeReader("aggregated")
	role.AggregationRule = &rbac.AggregationRule{ClusterRoleSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"reader": "true"}}}}
	c := fake.NewClientBuilder().WithScheme(readerScheme()).WithObjects(role).Build()
	if err := reconcileReaderBindings(ctx, c, "uid", []string{role.Name}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(role), role); err != nil {
		t.Fatal(err)
	}
	role.Rules = append(role.Rules, rbac.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}, ResourceNames: []string{"one"}})
	if err := c.Update(ctx, role); err != nil {
		t.Fatal(err)
	}
	if err := reconcileReaderBindings(ctx, c, "uid", []string{role.Name}); err == nil {
		t.Fatal("cached eligibility accepted unsafe effective rules")
	}
}
