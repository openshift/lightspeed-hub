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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/credential"
	"github.com/openshift/lightspeed-hub/internal/provisioner"
)

const (
	spokeClusterFinalizer = "hub.openshift.io/spoke-cleanup"
	healthyRequeueAfter   = 5 * time.Minute
	spokeDialTimeout      = 10 * time.Second

	conditionTypeConnected   = "Connected"
	conditionTypeProvisioned = "Provisioned"

	reasonConnectionSucceeded   = "ConnectionSucceeded"
	reasonConnectionFailed      = "ConnectionFailed"
	reasonCredentialError       = "CredentialError"
	reasonProvisioningSucceeded = "ProvisioningSucceeded"
	reasonProvisioningFailed    = "ProvisioningFailed"
)

// SpokeClusterReconciler reconciles a SpokeCluster object
type SpokeClusterReconciler struct {
	client            client.Client
	credentialSource  credential.CredentialSource
	operatorNamespace string

	// Mockable dependencies for testing
	NewSpokeClient    func(cfg *rest.Config) (client.Client, error)
	CheckConnectivity func(cfg *rest.Config) error
}

// NewSpokeClusterReconciler creates a new SpokeClusterReconciler with default dependencies
func NewSpokeClusterReconciler(hubClient client.Client, credSource credential.CredentialSource, operatorNamespace string) *SpokeClusterReconciler {
	return &SpokeClusterReconciler{
		client:            hubClient,
		credentialSource:  credSource,
		operatorNamespace: operatorNamespace,
		NewSpokeClient:    defaultNewSpokeClient,
		CheckConnectivity: defaultCheckConnectivity,
	}
}

func defaultNewSpokeClient(cfg *rest.Config) (client.Client, error) {
	return client.New(cfg, client.Options{})
}

func defaultCheckConnectivity(cfg *rest.Config) error {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating discovery client: %w", err)
	}
	_, err = discoveryClient.ServerVersion()
	if err != nil {
		return fmt.Errorf("checking server version: %w", err)
	}
	return nil
}

// +kubebuilder:rbac:groups=hub.openshift.io,resources=hubconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=hub.openshift.io,resources=spokeclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=hub.openshift.io,resources=spokeclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=hub.openshift.io,resources=spokeclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile reconciles a SpokeCluster CR
func (r *SpokeClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var sc hubv1alpha1.SpokeCluster
	if err := r.client.Get(ctx, req.NamespacedName, &sc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !sc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &sc)
	}

	// Add finalizer if missing
	if !controllerutil.ContainsFinalizer(&sc, spokeClusterFinalizer) {
		controllerutil.AddFinalizer(&sc, spokeClusterFinalizer)
		if err := r.client.Update(ctx, &sc); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		log.Info("Added finalizer", "spoke", sc.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	// Get credentials via broker
	cfg, err := r.credentialSource.GetRESTConfig(ctx, &sc)
	if err != nil {
		log.Error(err, "Failed to get credentials", "spoke", sc.Name)
		r.setCondition(&sc, conditionTypeConnected, metav1.ConditionFalse, reasonCredentialError, err.Error())
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status after credential error")
		}
		return ctrl.Result{}, err
	}

	// Set dial timeout on REST config
	cfg.Timeout = spokeDialTimeout

	// Create or update standing kubeconfig Secret
	standingSecret, err := credential.BuildStandingKubeconfig(cfg, &sc, r.operatorNamespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building standing kubeconfig: %w", err)
	}

	// Try Create first (idempotent pattern: Create + handle AlreadyExists)
	err = r.client.Create(ctx, standingSecret)
	if err == nil {
		log.Info("Created standing kubeconfig", "spoke", sc.Name, "secret", standingSecret.Name)
	} else if apierrors.IsAlreadyExists(err) {
		// Already exists — Get then Update (credentials may have rotated)
		var existingSecret corev1.Secret
		secretKey := client.ObjectKey{Name: standingSecret.Name, Namespace: standingSecret.Namespace}
		if err := r.client.Get(ctx, secretKey, &existingSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("getting existing standing kubeconfig: %w", err)
		}
		existingSecret.Data = standingSecret.Data
		existingSecret.OwnerReferences = standingSecret.OwnerReferences
		if err := r.client.Update(ctx, &existingSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating standing kubeconfig: %w", err)
		}
		log.Info("Updated standing kubeconfig", "spoke", sc.Name, "secret", standingSecret.Name)
	} else {
		return ctrl.Result{}, fmt.Errorf("creating standing kubeconfig: %w", err)
	}

	// Parse the saved standing kubeconfig to validate connectivity with the
	// stored credentials, not the original. This catches exec-based kubeconfigs
	// that work locally but produce an empty standing kubeconfig.
	standingCfg, err := clientcmd.RESTConfigFromKubeConfig(standingSecret.Data[credential.KubeconfigKey])
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing standing kubeconfig: %w", err)
	}
	standingCfg.Timeout = spokeDialTimeout

	// Check connectivity using the standing kubeconfig
	if err := r.CheckConnectivity(standingCfg); err != nil {
		log.Error(err, "Connectivity check failed", "spoke", sc.Name)
		r.setCondition(&sc, conditionTypeConnected, metav1.ConditionFalse, reasonConnectionFailed, err.Error())
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status after connectivity check")
		}
		return ctrl.Result{}, err
	}

	// Connectivity succeeded — set Connected=True
	r.setCondition(&sc, conditionTypeConnected, metav1.ConditionTrue, reasonConnectionSucceeded, "spoke API server is reachable")

	// Create spoke client using standing kubeconfig for provisioning
	spokeClient, err := r.NewSpokeClient(standingCfg)
	if err != nil {
		r.setCondition(&sc, conditionTypeProvisioned, metav1.ConditionFalse, reasonProvisioningFailed, fmt.Sprintf("failed to create spoke client: %v", err))
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status")
		}
		return ctrl.Result{}, fmt.Errorf("creating spoke client: %w", err)
	}

	// Provision spoke-side resources (idempotent)
	if err := provisioner.Provision(ctx, spokeClient); err != nil {
		log.Error(err, "Failed to provision spoke", "spoke", sc.Name)
		r.setCondition(&sc, conditionTypeProvisioned, metav1.ConditionFalse, reasonProvisioningFailed, err.Error())
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status after provisioning error")
		}
		return ctrl.Result{}, fmt.Errorf("provisioning spoke: %w", err)
	}

	// Provisioning succeeded
	r.setCondition(&sc, conditionTypeProvisioned, metav1.ConditionTrue, reasonProvisioningSucceeded, "spoke-side resources provisioned")
	if err := r.client.Status().Update(ctx, &sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	log.Info("Successfully reconciled spoke", "spoke", sc.Name)
	return ctrl.Result{RequeueAfter: healthyRequeueAfter}, nil
}

func (r *SpokeClusterReconciler) reconcileDelete(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(sc, spokeClusterFinalizer) {
		return ctrl.Result{}, nil
	}

	// Best-effort deprovision: try to read standing kubeconfig and clean up spoke
	secretKey := client.ObjectKey{
		Name:      credential.StandingKubeconfigName(sc.Name),
		Namespace: r.operatorNamespace,
	}
	var standingSecret corev1.Secret
	if err := r.client.Get(ctx, secretKey, &standingSecret); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Standing kubeconfig not found during deletion, skipping spoke cleanup", "spoke", sc.Name)
		} else {
			log.Error(err, "Failed to read standing kubeconfig during deletion", "spoke", sc.Name)
		}
	} else {
		// Standing kubeconfig exists, try to deprovision spoke
		kubeconfigBytes, ok := standingSecret.Data[credential.KubeconfigKey]
		if ok {
			cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
			if err != nil {
				log.Error(err, "Failed to parse standing kubeconfig during deletion", "spoke", sc.Name)
			} else {
				cfg.Timeout = spokeDialTimeout
				spokeClient, err := r.NewSpokeClient(cfg)
				if err != nil {
					log.Error(err, "Failed to create spoke client during deletion", "spoke", sc.Name)
				} else {
					log.Info("Deprovisioning spoke", "spoke", sc.Name)
					provisioner.Deprovision(ctx, spokeClient, log)
				}
			}
		}
	}

	// Remove finalizer (standing kubeconfig will be auto-GC'd via owner reference)
	controllerutil.RemoveFinalizer(sc, spokeClusterFinalizer)
	if err := r.client.Update(ctx, sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	log.Info("Removed finalizer, spoke cleanup complete", "spoke", sc.Name)
	return ctrl.Result{}, nil
}

func (r *SpokeClusterReconciler) setCondition(sc *hubv1alpha1.SpokeCluster, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&sc.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: sc.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *SpokeClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hubv1alpha1.SpokeCluster{}).
		Complete(r)
}
