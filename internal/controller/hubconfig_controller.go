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
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	hubConfigFinalizerName = "hub.openshift.io/hubconfig-cleanup"
	spokeDrainRequeue      = 5 * time.Second
)

// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=hub.openshift.io,resources=spokeclusters,verbs=list;delete

type HubConfigReconciler struct {
	client    client.Client
	apiReader client.Reader
}

func NewHubConfigReconciler(c client.Client, apiReader client.Reader) *HubConfigReconciler {
	return &HubConfigReconciler{client: c, apiReader: apiReader}
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

	// Use apiReader (uncached) to avoid race where the cache hasn't synced
	// SpokeCluster objects yet, which would cause premature finalizer removal.
	var spokeList hubv1alpha1.SpokeClusterList
	if err := r.apiReader.List(ctx, &spokeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing SpokeCluster CRs: %w", err)
	}

	for i := range spokeList.Items {
		sc := &spokeList.Items[i]
		if sc.DeletionTimestamp.IsZero() {
			logger.Info("deleting SpokeCluster as part of HubConfig teardown", "spoke", sc.Name)
			if err := r.client.Delete(ctx, sc); err != nil {
				if client.IgnoreNotFound(err) != nil {
					logger.Error(err, "failed to delete SpokeCluster", "spoke", sc.Name)
				}
			}
		}
	}

	remaining := len(spokeList.Items)

	if remaining > 0 {
		logger.Info("waiting for SpokeCluster CRs to drain", "remaining", remaining)
		return ctrl.Result{RequeueAfter: spokeDrainRequeue}, nil
	}

	controllerutil.RemoveFinalizer(&hc, hubConfigFinalizerName)
	if err := r.client.Update(ctx, &hc); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer from HubConfig: %w", err)
	}

	logger.Info("HubConfig teardown complete, all SpokeCluster CRs removed")
	return ctrl.Result{}, nil
}
