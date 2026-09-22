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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const hubConfigAdapterFinalizer = "hub.openshift.io/adapter-cleanup"

type HubConfigReconciler struct {
	client            client.Client
	operatorNamespace string
	adapterImage      string
}

func NewHubConfigReconciler(c client.Client, operatorNamespace, adapterImage string) *HubConfigReconciler {
	return &HubConfigReconciler{
		client:            c,
		operatorNamespace: operatorNamespace,
		adapterImage:      adapterImage,
	}
}

// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;create;update;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts;configmaps,verbs=get;list;create;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;create;update;delete
// RBAC escalation: the operator must hold every permission it grants to the adapter
// +kubebuilder:rbac:groups=agentic.openshift.io,resources=agenticruns,verbs=create;list;get
// +kubebuilder:rbac:groups=agentic.openshift.io,resources=agenticolsconfigs,verbs=get
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=alertmanagers/api,resourceNames=main,verbs=get;list

func (r *HubConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var hc hubv1alpha1.HubConfig
	if err := r.client.Get(ctx, req.NamespacedName, &hc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !hc.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&hc, hubConfigAdapterFinalizer) {
			logger.Info("HubConfig deleting, tearing down adapter stack")
			teardownSandboxRBAC(ctx, r.client, r.operatorNamespace)
			if err := teardownAdapterStack(ctx, r.client, r.operatorNamespace); err != nil {
				return ctrl.Result{}, fmt.Errorf("tearing down adapter stack: %w", err)
			}
			controllerutil.RemoveFinalizer(&hc, hubConfigAdapterFinalizer)
			if err := r.client.Update(ctx, &hc); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&hc, hubConfigAdapterFinalizer) {
		controllerutil.AddFinalizer(&hc, hubConfigAdapterFinalizer)
		if err := r.client.Update(ctx, &hc); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	if err := ensureSandboxRBAC(ctx, r.client, r.operatorNamespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring sandbox RBAC: %w", err)
	}

	if err := ensureAdapterStack(ctx, r.client, r.operatorNamespace, r.adapterImage); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring adapter stack: %w", err)
	}

	logger.Info("Adapter stack ensured")
	return ctrl.Result{}, nil
}

func (r *HubConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hubv1alpha1.HubConfig{}).
		Named("hubconfig").
		Complete(r)
}
