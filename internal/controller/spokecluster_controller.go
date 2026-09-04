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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/credential"
	"github.com/openshift/lightspeed-hub/internal/provisioner"
)

const (
	spokeClusterFinalizer = "hub.openshift.io/spoke-cleanup"
	healthyRequeueAfter   = 5 * time.Minute
	spokeDialTimeout      = 10 * time.Second

	conditionTypeReady       = "Ready"
	conditionTypeConnected   = "Connected"
	conditionTypeProvisioned = "Provisioned"

	reasonManaged                  = "Managed"
	reasonHubConfigMissing         = "HubConfigMissing"
	reasonUnsupportedMode          = "UnsupportedMode"
	reasonCredentialSourceMismatch = "CredentialSourceMismatch"
	reasonConnectionSucceeded      = "ConnectionSucceeded"
	reasonConnectionFailed         = "ConnectionFailed"
	reasonCredentialError          = "CredentialError"
	reasonProvisioningSucceeded    = "ProvisioningSucceeded"
	reasonProvisioningFailed       = "ProvisioningFailed"
)

// SpokeClusterReconciler reconciles a SpokeCluster object
type SpokeClusterReconciler struct {
	client            client.Client
	credentialSource  credential.CredentialSource
	operatorNamespace string

	NewSpokeClient    func(cfg *rest.Config) (client.Client, error)
	CheckConnectivity func(cfg *rest.Config) error
}

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

	// Check HubConfig exists
	var hubConfig hubv1alpha1.HubConfig
	if err := r.client.Get(ctx, client.ObjectKey{Name: "cluster"}, &hubConfig); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("No HubConfig found, unmanaging spoke", "spoke", sc.Name)
			r.cleanupSpokeResources(ctx, &sc)
			r.setCondition(&sc, conditionTypeReady, metav1.ConditionFalse, reasonHubConfigMissing, "HubConfig 'cluster' not found")
			if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
				log.Error(updateErr, "Failed to update status")
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting HubConfig: %w", err)
	}

	// Reject unsupported modes
	if hubConfig.Spec.ClusterRegistryMode != hubv1alpha1.ClusterRegistryModeSecret {
		log.Info("Unsupported clusterRegistryMode, unmanaging spoke", "spoke", sc.Name,
			"mode", hubConfig.Spec.ClusterRegistryMode)
		r.cleanupSpokeResources(ctx, &sc)
		r.setCondition(&sc, conditionTypeReady, metav1.ConditionFalse, reasonUnsupportedMode,
			fmt.Sprintf("clusterRegistryMode %q is not yet supported", hubConfig.Spec.ClusterRegistryMode))
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status")
		}
		return ctrl.Result{}, nil
	}

	// Check credential source matches HubConfig mode
	if !r.credentialSourceMatchesMode(&sc, &hubConfig) {
		log.Info("Credential source does not match HubConfig mode, unmanaging spoke", "spoke", sc.Name,
			"mode", hubConfig.Spec.ClusterRegistryMode)
		r.cleanupSpokeResources(ctx, &sc)
		r.setCondition(&sc, conditionTypeReady, metav1.ConditionFalse, reasonCredentialSourceMismatch,
			fmt.Sprintf("credential source does not match clusterRegistryMode %q", hubConfig.Spec.ClusterRegistryMode))
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status")
		}
		return ctrl.Result{}, nil
	}

	r.setCondition(&sc, conditionTypeReady, metav1.ConditionTrue, reasonManaged, "spoke is managed by hub")

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

	cfg.Timeout = spokeDialTimeout

	// Create or update standing kubeconfig Secret
	standingSecret, err := credential.BuildStandingKubeconfig(cfg, &sc, r.operatorNamespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building standing kubeconfig: %w", err)
	}

	err = r.client.Create(ctx, standingSecret)
	if err == nil {
		log.Info("Created standing kubeconfig", "spoke", sc.Name, "secret", standingSecret.Name)
	} else if apierrors.IsAlreadyExists(err) {
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

	// Validate connectivity with the saved standing kubeconfig
	standingCfg, err := clientcmd.RESTConfigFromKubeConfig(standingSecret.Data[credential.KubeconfigKey])
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing standing kubeconfig: %w", err)
	}
	standingCfg.Timeout = spokeDialTimeout

	if err := r.CheckConnectivity(standingCfg); err != nil {
		log.Error(err, "Connectivity check failed", "spoke", sc.Name)
		r.setCondition(&sc, conditionTypeConnected, metav1.ConditionFalse, reasonConnectionFailed, err.Error())
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status after connectivity check")
		}
		return ctrl.Result{}, err
	}

	r.setCondition(&sc, conditionTypeConnected, metav1.ConditionTrue, reasonConnectionSucceeded, "spoke API server is reachable")

	// Provision spoke-side resources
	spokeClient, err := r.NewSpokeClient(standingCfg)
	if err != nil {
		r.setCondition(&sc, conditionTypeProvisioned, metav1.ConditionFalse, reasonProvisioningFailed, fmt.Sprintf("failed to create spoke client: %v", err))
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status")
		}
		return ctrl.Result{}, fmt.Errorf("creating spoke client: %w", err)
	}

	if err := provisioner.Provision(ctx, spokeClient); err != nil {
		log.Error(err, "Failed to provision spoke", "spoke", sc.Name)
		r.setCondition(&sc, conditionTypeProvisioned, metav1.ConditionFalse, reasonProvisioningFailed, err.Error())
		if updateErr := r.client.Status().Update(ctx, &sc); updateErr != nil {
			log.Error(updateErr, "Failed to update status after provisioning error")
		}
		return ctrl.Result{}, fmt.Errorf("provisioning spoke: %w", err)
	}

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

	r.cleanupSpokeResources(ctx, sc)

	controllerutil.RemoveFinalizer(sc, spokeClusterFinalizer)
	if err := r.client.Update(ctx, sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	log.Info("Removed finalizer, spoke cleanup complete", "spoke", sc.Name)
	return ctrl.Result{}, nil
}

// cleanupSpokeResources removes resources created for this spoke: deprovisions
// spoke-side resources and deletes the standing kubeconfig Secret. Best-effort —
// errors are logged but do not block.
func (r *SpokeClusterReconciler) cleanupSpokeResources(ctx context.Context, sc *hubv1alpha1.SpokeCluster) {
	logger := log.FromContext(ctx).WithValues("spoke", sc.Name)
	secretKey := client.ObjectKey{
		Name:      credential.StandingKubeconfigName(sc.Name),
		Namespace: r.operatorNamespace,
	}

	var standingSecret corev1.Secret
	if err := r.client.Get(ctx, secretKey, &standingSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to read standing kubeconfig during cleanup")
		}
		return
	}

	// Deprovision spoke-side resources using standing kubeconfig
	kubeconfigBytes, ok := standingSecret.Data[credential.KubeconfigKey]
	if ok {
		cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
		if err != nil {
			logger.Error(err, "Failed to parse standing kubeconfig during cleanup")
		} else {
			cfg.Timeout = spokeDialTimeout
			spokeClient, err := r.NewSpokeClient(cfg)
			if err != nil {
				logger.Error(err, "Failed to create spoke client during cleanup")
			} else {
				logger.Info("Deprovisioning spoke resources")
				provisioner.Deprovision(ctx, spokeClient, logger)
			}
		}
	}

	// Delete standing kubeconfig Secret
	if err := r.client.Delete(ctx, &standingSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete standing kubeconfig during cleanup")
		}
	} else {
		logger.Info("Deleted standing kubeconfig", "secret", standingSecret.Name)
	}
}

func (r *SpokeClusterReconciler) credentialSourceMatchesMode(sc *hubv1alpha1.SpokeCluster, hc *hubv1alpha1.HubConfig) bool {
	switch hc.Spec.ClusterRegistryMode {
	case hubv1alpha1.ClusterRegistryModeSecret:
		return sc.Spec.CredentialSource.Secret != nil
	case hubv1alpha1.ClusterRegistryModeMCE:
		return sc.Spec.CredentialSource.MCE != nil
	default:
		return false
	}
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

// mapHubConfigToSpokeClusters maps any HubConfig event to reconcile requests
// for all SpokeCluster CRs, so mode changes and HubConfig deletion trigger
// re-evaluation of every spoke.
func (r *SpokeClusterReconciler) mapHubConfigToSpokeClusters(ctx context.Context, _ client.Object) []reconcile.Request {
	var spokeList hubv1alpha1.SpokeClusterList
	if err := r.client.List(ctx, &spokeList); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, len(spokeList.Items))
	for i, sc := range spokeList.Items {
		requests[i] = reconcile.Request{NamespacedName: types.NamespacedName{Name: sc.Name}}
	}
	return requests
}

func (r *SpokeClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hubv1alpha1.SpokeCluster{}).
		Watches(&hubv1alpha1.HubConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapHubConfigToSpokeClusters)).
		Complete(r)
}
