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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs,verbs=get;list;watch

// HubConfigReconciler exists only to register a watch on HubConfig. All spoke
// lifecycle logic lives in the SpokeCluster controller, which watches HubConfig
// events via mapHubConfigToSpokeClusters. No finalizer is needed — the
// SpokeCluster controller handles both "deleting HubConfig" and "missing
// HubConfig" identically via unmanageSpoke.
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

func (r *HubConfigReconciler) Reconcile(_ context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}
