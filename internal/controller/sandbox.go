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
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	sandboxSAName                   = "lightspeed-agent"
	sandboxCRBClusterReader         = "lightspeed-hub:sandbox-cluster-reader"
	sandboxCRBClusterMonitoringView = "lightspeed-hub:sandbox-cluster-monitoring-view"
	sandboxCRBMonitoringRulesView   = "lightspeed-hub:sandbox-monitoring-rules-view"
)

func sandboxBindings(namespace string) []*rbacv1.ClusterRoleBinding {
	return []*rbacv1.ClusterRoleBinding{
		{
			ObjectMeta: metav1.ObjectMeta{Name: sandboxCRBClusterReader},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "cluster-reader",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      sandboxSAName,
				Namespace: namespace,
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: sandboxCRBClusterMonitoringView},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "cluster-monitoring-view",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      sandboxSAName,
				Namespace: namespace,
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: sandboxCRBMonitoringRulesView},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "monitoring-rules-view",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      sandboxSAName,
				Namespace: namespace,
			}},
		},
	}
}

func ensureSandboxRBAC(ctx context.Context, c client.Client, namespace string) error {
	for _, crb := range sandboxBindings(namespace) {
		if err := c.Create(ctx, crb); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating ClusterRoleBinding %s: %w", crb.Name, err)
			}
		}
	}
	return nil
}

func teardownSandboxRBAC(ctx context.Context, c client.Client, namespace string) {
	log := logf.FromContext(ctx)
	for _, crb := range sandboxBindings(namespace) {
		if err := c.Delete(ctx, crb); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "failed to delete sandbox ClusterRoleBinding", "name", crb.Name)
			}
		}
	}
}
