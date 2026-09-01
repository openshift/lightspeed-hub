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
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ManagedNamespace is the namespace created on spoke clusters for hub-managed resources
	ManagedNamespace = "openshift-lightspeed-managed"

	// ServiceAccountName is the ServiceAccount name for the lightspeed agent
	ServiceAccountName = "lightspeed-agent"

	// ClusterRoleBindingClusterReader is the ClusterRoleBinding name for cluster-reader
	ClusterRoleBindingClusterReader = "lightspeed-hub:cluster-reader"

	// ClusterRoleBindingMonitoringView is the ClusterRoleBinding name for cluster-monitoring-view
	ClusterRoleBindingMonitoringView = "lightspeed-hub:cluster-monitoring-view"
)

// Provision creates the required resources on the spoke cluster.
// It is idempotent and handles AlreadyExists errors.
func Provision(ctx context.Context, spokeClient client.Client) error {
	// 1. Create namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ManagedNamespace,
		},
	}
	if err := spokeClient.Create(ctx, ns); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace: %w", err)
		}
	}

	// 2. Create ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName,
			Namespace: ManagedNamespace,
		},
	}
	if err := spokeClient.Create(ctx, sa); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ServiceAccount: %w", err)
		}
	}

	// 3. Create ClusterRoleBinding for cluster-reader
	crbReader := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: ClusterRoleBindingClusterReader,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "cluster-reader",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      ServiceAccountName,
				Namespace: ManagedNamespace,
			},
		},
	}
	if err := spokeClient.Create(ctx, crbReader); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ClusterRoleBinding cluster-reader: %w", err)
		}
	}

	// 4. Create ClusterRoleBinding for cluster-monitoring-view
	crbMonitoring := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: ClusterRoleBindingMonitoringView,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "cluster-monitoring-view",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      ServiceAccountName,
				Namespace: ManagedNamespace,
			},
		},
	}
	if err := spokeClient.Create(ctx, crbMonitoring); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ClusterRoleBinding cluster-monitoring-view: %w", err)
		}
	}

	return nil
}

// Deprovision deletes the resources from the spoke cluster in reverse order.
// It is best-effort: errors are logged but not returned, and NotFound errors are ignored.
func Deprovision(ctx context.Context, spokeClient client.Client, log logr.Logger) {
	// Delete in reverse order: ClusterRoleBindings, ServiceAccount, Namespace

	// 1. Delete ClusterRoleBinding cluster-monitoring-view
	crbMonitoring := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: ClusterRoleBindingMonitoringView,
		},
	}
	if err := spokeClient.Delete(ctx, crbMonitoring); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete ClusterRoleBinding", "name", ClusterRoleBindingMonitoringView)
		}
	}

	// 2. Delete ClusterRoleBinding cluster-reader
	crbReader := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: ClusterRoleBindingClusterReader,
		},
	}
	if err := spokeClient.Delete(ctx, crbReader); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete ClusterRoleBinding", "name", ClusterRoleBindingClusterReader)
		}
	}

	// 3. Delete ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName,
			Namespace: ManagedNamespace,
		},
	}
	if err := spokeClient.Delete(ctx, sa); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete ServiceAccount", "name", ServiceAccountName, "namespace", ManagedNamespace)
		}
	}

	// 4. Delete Namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ManagedNamespace,
		},
	}
	if err := spokeClient.Delete(ctx, ns); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete Namespace", "name", ManagedNamespace)
		}
	}
}
