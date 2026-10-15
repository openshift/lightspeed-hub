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
	MonitoringNamespace = "openshift-monitoring"

	AlertAdapterServiceAccount = "lightspeed-alert-adapter"
	AlertAdapterRoleBinding    = "lightspeed-hub:alert-adapter-monitoring-view"

	MonitoringAlertmanagerViewRole = "monitoring-alertmanager-view"
)

// ProvisionAdapter creates the spoke-side resources needed by the alerts-adapter:
// a ServiceAccount and a monitoring-alertmanager-view RoleBinding in openshift-monitoring.
// The hub operator requests a time-bound token via the TokenRequest API separately.
// Idempotent — AlreadyExists errors are ignored.
func ProvisionAdapter(ctx context.Context, spokeClient client.Client) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AlertAdapterServiceAccount,
			Namespace: ManagedNamespace,
		},
	}
	if err := spokeClient.Create(ctx, sa); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%w: %w", ErrCreatingAdapterServiceAccount, err)
		}
	}

	// Assumes standard OpenShift monitoring: the monitoring-alertmanager-view Role
	// must exist in openshift-monitoring. On non-standard spokes (HyperShift, monitoring
	// disabled), this binding grants nothing and AlertManager access will 401.
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AlertAdapterRoleBinding,
			Namespace: MonitoringNamespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     MonitoringAlertmanagerViewRole,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      AlertAdapterServiceAccount,
				Namespace: ManagedNamespace,
			},
		},
	}
	if err := spokeClient.Create(ctx, rb); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%w: %w", ErrCreatingAdapterRoleBinding, err)
		}
	}

	return nil
}

// DeprovisionAdapter removes spoke-side adapter resources in reverse creation order.
// Best-effort — errors are logged but do not block deletion.
func DeprovisionAdapter(ctx context.Context, spokeClient client.Client, log logr.Logger) {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AlertAdapterRoleBinding,
			Namespace: MonitoringNamespace,
		},
	}
	if err := spokeClient.Delete(ctx, rb); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete alert adapter RoleBinding", "name", AlertAdapterRoleBinding, "namespace", MonitoringNamespace)
		}
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AlertAdapterServiceAccount,
			Namespace: ManagedNamespace,
		},
	}
	if err := spokeClient.Delete(ctx, sa); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete alert adapter ServiceAccount", "name", AlertAdapterServiceAccount, "namespace", ManagedNamespace)
		}
	}
}
