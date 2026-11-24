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
	"slices"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/provisioner"
)

// generatedMSAPermissions holds the spoke RBAC that MCE discovery pushes for
// the ManagedServiceAccount: a cluster-wide ClusterRole plus a Role scoped to
// the managed namespace.
type generatedMSAPermissions struct {
	clusterRules   []rbacv1.PolicyRule
	namespace      string
	namespaceRules []rbacv1.PolicyRule
}

// generatedMSAPermissionsFor runs MCE discovery and extracts the ClusterRole
// and Role from the ManifestWork it generates for the given spoke.
func generatedMSAPermissionsFor(spokeName string) generatedMSAPermissions {
	scheme := discoveryScheme()
	hubClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(mceHubConfigWithSelector(nil)).
		WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
		Build()
	apiReader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(managedCluster(spokeName, "https://api."+spokeName+".example.com:6443", nil)).
		Build()
	_, err := newDiscoveryReconciler(hubClient, apiReader).Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cluster"},
	})
	Expect(err).NotTo(HaveOccurred())

	var mw unstructured.Unstructured
	mw.SetGroupVersionKind(mwGVK)
	Expect(hubClient.Get(ctx, types.NamespacedName{Name: "lightspeed-hub-msa-rbac", Namespace: spokeName}, &mw)).To(Succeed())
	manifests, found, err := unstructured.NestedSlice(mw.Object, "spec", "workload", "manifests")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	var perms generatedMSAPermissions
	for _, m := range manifests {
		obj, ok := m.(map[string]interface{})
		Expect(ok).To(BeTrue())
		switch obj["kind"] {
		case "ClusterRole":
			var role rbacv1.ClusterRole
			Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &role)).To(Succeed())
			perms.clusterRules = role.Rules
		case "Role":
			var role rbacv1.Role
			Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &role)).To(Succeed())
			perms.namespace = role.Namespace
			perms.namespaceRules = role.Rules
		}
	}
	Expect(perms.clusterRules).NotTo(BeEmpty(), "generated ClusterRole not found")
	Expect(perms.namespaceRules).NotTo(BeEmpty(), "generated Role not found")
	return perms
}

func ruleAllows(rule rbacv1.PolicyRule, verb, group, resource, name string) bool {
	match := func(values []string, v string) bool {
		return slices.Contains(values, v) || slices.Contains(values, "*")
	}
	if !match(rule.Verbs, verb) || !match(rule.APIGroups, group) || !match(rule.Resources, resource) {
		return false
	}
	// Kubernetes cannot match resourceNames for create or list requests.
	return len(rule.ResourceNames) == 0 || (name != "" && slices.Contains(rule.ResourceNames, name))
}

func (p generatedMSAPermissions) allows(verb string, gvk schema.GroupVersionKind, namespace, name string) bool {
	resource, _ := meta.UnsafeGuessKindToResource(gvk)
	for _, rule := range p.clusterRules {
		if ruleAllows(rule, verb, gvk.Group, resource.Resource, name) {
			return true
		}
	}
	if namespace == p.namespace {
		for _, rule := range p.namespaceRules {
			if ruleAllows(rule, verb, gvk.Group, resource.Resource, name) {
				return true
			}
		}
	}
	return false
}

// spokeClientWithGeneratedMSAPermissions returns a fake spoke client that
// rejects any request the generated MSA RBAC does not allow.
func spokeClientWithGeneratedMSAPermissions(perms generatedMSAPermissions, objects ...client.Object) (client.Client, client.Client) {
	scheme := newTestScheme()
	authorize := func(verb string, obj runtime.Object, namespace, name string) error {
		gvk, err := apiutil.GVKForObject(obj, scheme)
		if err != nil {
			return err
		}
		gvk.Kind = trimListSuffix(gvk.Kind)
		if perms.allows(verb, gvk, namespace, name) {
			return nil
		}
		resource, _ := meta.UnsafeGuessKindToResource(gvk)
		return apierrors.NewForbidden(resource.GroupResource(), name,
			fmt.Errorf("generated MSA RBAC does not allow %s", verb))
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	authorized := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := authorize("get", obj, key.Namespace, key.Name); err != nil {
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			listOpts := &client.ListOptions{}
			listOpts.ApplyOptions(opts)
			if err := authorize("list", list, listOpts.Namespace, ""); err != nil {
				return err
			}
			return c.List(ctx, list, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := authorize("create", obj, obj.GetNamespace(), ""); err != nil {
				return err
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if err := authorize("update", obj, obj.GetNamespace(), obj.GetName()); err != nil {
				return err
			}
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if err := authorize("patch", obj, obj.GetNamespace(), obj.GetName()); err != nil {
				return err
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if err := authorize("delete", obj, obj.GetNamespace(), obj.GetName()); err != nil {
				return err
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	return authorized, base
}

func trimListSuffix(kind string) string {
	if len(kind) > 4 && kind[len(kind)-4:] == "List" {
		return kind[:len(kind)-4]
	}
	return kind
}

var _ = Describe("Baseline reader RBAC with generated MCE permissions", func() {
	It("provisions, repairs drift, and cleans up using only the generated MSA RBAC", func() {
		perms := generatedMSAPermissionsFor("spoke-1")
		readerRules := []rbacv1.PolicyRule{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}}
		spokeClient, unrestricted := spokeClientWithGeneratedMSAPermissions(perms,
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cluster-reader"}, Rules: readerRules},
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cluster-monitoring-view"}, Rules: readerRules},
		)
		uid := types.UID("mce-spoke-uid")
		roles, err := provisioner.ResolveReaderRoles(&hubv1alpha1.HubConfig{}, &hubv1alpha1.SpokeCluster{})
		Expect(err).NotTo(HaveOccurred())

		By("provisioning default baseline bindings on a fresh spoke")
		Expect(provisioner.Provision(ctx, spokeClient, uid, roles)).To(Succeed())

		By("repairing subject drift on an owned binding")
		var binding rbacv1.ClusterRoleBinding
		key := client.ObjectKey{Name: provisioner.ClusterRoleBindingClusterReader}
		Expect(unrestricted.Get(ctx, key, &binding)).To(Succeed())
		binding.Subjects = nil
		Expect(unrestricted.Update(ctx, &binding)).To(Succeed())
		Expect(provisioner.Provision(ctx, spokeClient, uid, roles)).To(Succeed())
		Expect(unrestricted.Get(ctx, key, &binding)).To(Succeed())
		Expect(binding.Subjects).To(HaveLen(1))

		By("removing obsolete bindings when fleet defaults are explicitly empty")
		Expect(provisioner.Provision(ctx, spokeClient, uid, nil)).To(Succeed())
		var bindings rbacv1.ClusterRoleBindingList
		Expect(unrestricted.List(ctx, &bindings, client.MatchingLabels{provisioner.ReaderOwnerLabel: string(uid)})).To(Succeed())
		Expect(bindings.Items).To(BeEmpty())

		By("cleaning up owned bindings and seed resources during decommission")
		Expect(provisioner.Provision(ctx, spokeClient, uid, roles)).To(Succeed())
		Expect(provisioner.Deprovision(ctx, spokeClient, logr.Discard(), uid)).To(Succeed())
		Expect(unrestricted.List(ctx, &bindings, client.MatchingLabels{provisioner.ReaderOwnerLabel: string(uid)})).To(Succeed())
		Expect(bindings.Items).To(BeEmpty())
	})
})
