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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	hubConfigFinalizerName = "hub.openshift.io/hubconfig-cleanup"
)

// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs/finalizers,verbs=update

type HubConfigReconciler struct {
	client client.Client
}

func NewHubConfigReconciler(c client.Client) *HubConfigReconciler {
	return &HubConfigReconciler{client: c}
}

func (r *HubConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hubv1alpha1.HubConfig{}).
		Named("hubconfig").
		Complete(r)
}

func (r *HubConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var hc hubv1alpha1.HubConfig
	if err := r.client.Get(ctx, req.NamespacedName, &hc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !controllerutil.ContainsFinalizer(&hc, hubConfigFinalizerName) {
		controllerutil.AddFinalizer(&hc, hubConfigFinalizerName)
		if err := r.client.Update(ctx, &hc); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer to HubConfig: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if hc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// HubConfig is being deleted. The SpokeCluster controller watches HubConfig
	// events and will reconcile all spokes, cleaning up their resources. We just
	// need to remove the finalizer. SpokeCluster CRs are NOT deleted — that is
	// the user's or GitOps's responsibility.
	controllerutil.RemoveFinalizer(&hc, hubConfigFinalizerName)
	if err := r.client.Update(ctx, &hc); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer from HubConfig: %w", err)
	}

	logger.Info("HubConfig deleted, SpokeCluster controller will unmanage all spokes")
	return ctrl.Result{}, nil
}
