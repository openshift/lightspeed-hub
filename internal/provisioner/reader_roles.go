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

package provisioner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	hub "github.com/openshift/lightspeed-hub/api/v1alpha1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ReaderComponentLabel separates baseline bindings from other hub resources.
	ReaderComponentLabel = "hub.openshift.io/component"
	// ReaderOwnerLabel ties bindings to a registration lifetime, not its name.
	ReaderOwnerLabel = "hub.openshift.io/spoke-uid"
	readerComponent  = "baseline-reader"
)

// ResolveReaderRoles preserves omission versus empty fleet defaults and returns
// an order-independent union. Role definitions always remain administrator-owned.
func ResolveReaderRoles(hc *hub.HubConfig, sc *hub.SpokeCluster) ([]string, error) {
	roles := []hub.ClusterRoleName{"cluster-reader", "cluster-monitoring-view"}
	if hc.Spec.AgentRBAC != nil && hc.Spec.AgentRBAC.DefaultClusterRoles != nil {
		roles = *hc.Spec.AgentRBAC.DefaultClusterRoles
	}
	roles = append([]hub.ClusterRoleName(nil), roles...)
	if sc.Spec.AgentRBAC != nil {
		roles = append(roles, sc.Spec.AgentRBAC.AdditionalClusterRoles...)
	}
	unique := map[string]struct{}{}
	for _, role := range roles {
		name := string(role)
		if name == "" || len(content.IsPathSegmentName(name)) != 0 {
			return nil, fmt.Errorf("invalid ClusterRole name %q", name)
		}
		unique[name] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for name := range unique {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func validateReaderRole(role *rbac.ClusterRole) error {
	// Trust these exact default-role names, including administrator-modified
	// contents. The caller must still read the role before validation; existence,
	// API authorization, and binding ownership checks are not exempt.
	switch role.Name {
	case "cluster-reader", "cluster-monitoring-view":
		return nil
	}
	for _, rule := range role.Rules {
		for _, verb := range rule.Verbs {
			if verb != "get" && verb != "list" && verb != "watch" {
				return fmt.Errorf("ClusterRole %q grants prohibited verb %q", role.Name, verb)
			}
		}
		for _, group := range rule.APIGroups {
			group = strings.TrimSpace(group)
			if (group == rbac.GroupName || group == "*") && len(rule.Resources) > 0 {
				return fmt.Errorf("ClusterRole %q grants RBAC resource access via group %q", role.Name, group)
			}
			if group == "" || group == "core" || group == "v1" || group == "core/v1" {
				for _, resource := range rule.Resources {
					if resource == "*" || resource == "secrets" || strings.HasPrefix(resource, "secrets/") || strings.HasPrefix(resource, "*/") {
						return fmt.Errorf("ClusterRole %q grants core Secret access via resource %q", role.Name, resource)
					}
				}
			}
		}
	}
	return nil
}

func readerBindingName(role string) string {
	switch role {
	case "cluster-reader":
		return ClusterRoleBindingClusterReader
	case "cluster-monitoring-view":
		return ClusterRoleBindingMonitoringView
	}
	return fmt.Sprintf("lightspeed-hub:reader:%x", sha256.Sum256([]byte(role)))
}

func readerLabels(uid types.UID) map[string]string {
	return map[string]string{ReaderComponentLabel: readerComponent, ReaderOwnerLabel: string(uid)}
}
func ownsReader(binding *rbac.ClusterRoleBinding, uid types.UID) bool {
	return uid != "" && binding.Labels[ReaderComponentLabel] == readerComponent && binding.Labels[ReaderOwnerLabel] == string(uid)
}

func reconcileReaderBindings(ctx context.Context, c client.Client, uid types.UID, roles []string) error {
	if uid == "" {
		return fmt.Errorf("baseline reader ownership requires a SpokeCluster UID")
	}
	// Validate the complete role set before changing any grants.
	for _, name := range roles {
		var role rbac.ClusterRole
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &role); err != nil {
			return fmt.Errorf("reading baseline ClusterRole %q: %w", name, err)
		}
		if err := validateReaderRole(&role); err != nil {
			return err
		}
	}
	desired := map[string]bool{}
	for _, name := range roles {
		binding := &rbac.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:   readerBindingName(name),
				Labels: readerLabels(uid),
			},
			RoleRef: rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "ClusterRole", Name: name},
			Subjects: []rbac.Subject{{
				Kind: "ServiceAccount", Name: ServiceAccountName, Namespace: ManagedNamespace,
			}},
		}
		desired[binding.Name] = true
		if err := ensureReaderBinding(ctx, c, uid, binding); err != nil {
			return fmt.Errorf("reconciling baseline ClusterRole %q: %w", name, err)
		}
	}
	return removeReaderBindings(ctx, c, uid, desired)
}

func ensureReaderBinding(ctx context.Context, c client.Client, uid types.UID, want *rbac.ClusterRoleBinding) error {
	if err := c.Create(ctx, want); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("%w %s: %w", ErrCreatingClusterRoleBinding, want.RoleRef.Name, err)
	}
	var existing rbac.ClusterRoleBinding
	if err := c.Get(ctx, client.ObjectKeyFromObject(want), &existing); err != nil {
		return err
	}
	if !ownsReader(&existing, uid) {
		return fmt.Errorf("ClusterRoleBinding %q ownership collision; not owned by SpokeCluster UID %q", want.Name, uid)
	}
	if !existing.DeletionTimestamp.IsZero() {
		return fmt.Errorf("ClusterRoleBinding %q is terminating", want.Name)
	}
	if existing.RoleRef != want.RoleRef {
		if err := deleteReaderBinding(ctx, c, &existing); err != nil {
			return err
		}
		// Retry through Create + ownership validation after immutable roleRef replacement.
		return fmt.Errorf("replacing immutable roleRef of ClusterRoleBinding %q; retry required", want.Name)
	}
	if reflect.DeepEqual(existing.Subjects, want.Subjects) {
		return nil
	}
	existing.Subjects = want.Subjects
	return c.Update(ctx, &existing)
}

func deleteReaderBinding(ctx context.Context, c client.Client, binding *rbac.ClusterRoleBinding) error {
	err := c.Delete(ctx, binding, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: &binding.UID, ResourceVersion: &binding.ResourceVersion,
		},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// removeReaderBindings discovers ownership independently of configuration.
func removeReaderBindings(ctx context.Context, c client.Client, uid types.UID, keep map[string]bool) error {
	if uid == "" {
		return fmt.Errorf("baseline reader cleanup requires a SpokeCluster UID")
	}
	var bindings rbac.ClusterRoleBindingList
	if err := c.List(ctx, &bindings, client.MatchingLabels(readerLabels(uid))); err != nil {
		return fmt.Errorf("listing owned baseline ClusterRoleBindings: %w", err)
	}
	var errs []error
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		if ownsReader(binding, uid) && !keep[binding.Name] {
			if err := deleteReaderBinding(ctx, c, binding); err != nil {
				errs = append(errs, fmt.Errorf("deleting baseline ClusterRoleBinding %q: %w", binding.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}
