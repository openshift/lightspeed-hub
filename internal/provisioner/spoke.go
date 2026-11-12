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
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ManagedNamespace isolates hub-managed resources from a spoke-local installation.
	ManagedNamespace = "openshift-lightspeed-managed"
	// ServiceAccountName is the reader discovery seed, not a sandbox identity.
	ServiceAccountName = "lightspeed-agent"
	// ClusterRoleBindingClusterReader preserves the built-in reader binding name.
	ClusterRoleBindingClusterReader = "lightspeed-hub:cluster-reader"
	// ClusterRoleBindingMonitoringView preserves the built-in monitoring binding name.
	ClusterRoleBindingMonitoringView = "lightspeed-hub:cluster-monitoring-view"
)

// Provision creates the seed namespace/SA and converges owned baseline bindings.
func Provision(ctx context.Context, spokeClient client.Client, uid types.UID, roles []string) error {
	if uid == "" {
		return fmt.Errorf("baseline reader ownership requires a SpokeCluster UID")
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace}}
	if err := spokeClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("%w: %w", ErrCreatingNamespace, err)
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: ServiceAccountName, Namespace: ManagedNamespace}}
	if err := spokeClient.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("%w: %w", ErrCreatingServiceAccount, err)
	}
	return reconcileReaderBindings(ctx, spokeClient, uid, roles)
}

// Deprovision removes only this registration's baseline bindings, then the seed
// resources. Remote cleanup remains best-effort and reports aggregated errors.
func Deprovision(ctx context.Context, spokeClient client.Client, log logr.Logger, uid types.UID) error {
	var errs []error
	if err := removeReaderBindings(ctx, spokeClient, uid, nil); err != nil {
		log.Error(err, "failed to delete baseline bindings")
		errs = append(errs, err)
	}
	for _, obj := range []client.Object{
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: ServiceAccountName, Namespace: ManagedNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace}},
	} {
		if err := spokeClient.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete seed resource", "name", obj.GetName())
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
