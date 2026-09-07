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
	"bytes"
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
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

type SpokeClusterReconciler struct {
	client            client.Client
	scheme            *runtime.Scheme
	credentialSource  credential.CredentialSource
	operatorNamespace string
	recorder          record.EventRecorder

	NewSpokeClient    func(cfg *rest.Config) (client.Client, error)
	CheckConnectivity func(cfg *rest.Config) error
}

func NewSpokeClusterReconciler(hubClient client.Client, scheme *runtime.Scheme, credSource credential.CredentialSource, operatorNamespace string) *SpokeClusterReconciler {
	return &SpokeClusterReconciler{
		client:            hubClient,
		scheme:            scheme,
		credentialSource:  credSource,
		operatorNamespace: operatorNamespace,
		NewSpokeClient:    defaultNewSpokeClient,
		CheckConnectivity: defaultCheckConnectivity,
	}
}

func (r *SpokeClusterReconciler) SetEventRecorder(recorder record.EventRecorder) {
	r.recorder = recorder
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

	if !sc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &sc)
	}

	if !controllerutil.ContainsFinalizer(&sc, spokeClusterFinalizer) {
		controllerutil.AddFinalizer(&sc, spokeClusterFinalizer)
		if err := r.client.Update(ctx, &sc); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		log.Info("Added finalizer", "spoke", sc.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	// Check HubConfig exists and is not being deleted
	var hubConfig hubv1alpha1.HubConfig
	if err := r.client.Get(ctx, client.ObjectKey{Name: "cluster"}, &hubConfig); err != nil {
		if apierrors.IsNotFound(err) {
			return r.unmanageSpoke(ctx, &sc, reasonHubConfigMissing, "HubConfig 'cluster' not found")
		}
		return ctrl.Result{}, fmt.Errorf("getting HubConfig: %w", err)
	}
	if !hubConfig.DeletionTimestamp.IsZero() {
		return r.unmanageSpoke(ctx, &sc, reasonHubConfigMissing, "HubConfig is being deleted")
	}

	if hubConfig.Spec.ClusterRegistryMode != hubv1alpha1.ClusterRegistryModeSecret {
		return r.unmanageSpoke(ctx, &sc, reasonUnsupportedMode,
			fmt.Sprintf("clusterRegistryMode %q is not yet supported", hubConfig.Spec.ClusterRegistryMode))
	}

	if !r.credentialSourceMatchesMode(&sc, &hubConfig) {
		return r.unmanageSpoke(ctx, &sc, reasonCredentialSourceMismatch,
			fmt.Sprintf("credential source does not match clusterRegistryMode %q", hubConfig.Spec.ClusterRegistryMode))
	}

	// Get credentials
	cfg, err := r.credentialSource.GetRESTConfig(ctx, &sc)
	if err != nil {
		log.Error(err, "Failed to get credentials", "spoke", sc.Name)
		return r.failWithCondition(ctx, &sc, conditionTypeConnected, reasonCredentialError, err)
	}
	cfg.Timeout = spokeDialTimeout

	// Create or update standing kubeconfig (skip update if data and owner unchanged)
	if err := r.ensureStandingKubeconfig(ctx, cfg, &sc); err != nil {
		return r.failWithCondition(ctx, &sc, conditionTypeConnected, reasonCredentialError, err)
	}

	// Parse the standing kubeconfig to use for connectivity and provisioning.
	// This validates with the stored credentials and apiServer URL, not the
	// admin kubeconfig's host which may differ.
	standingCfg, err := r.loadStandingKubeconfig(ctx, &sc)
	if err != nil {
		return r.failWithCondition(ctx, &sc, conditionTypeConnected, reasonCredentialError, err)
	}

	// Check connectivity using the standing kubeconfig
	if err := r.CheckConnectivity(standingCfg); err != nil {
		log.Error(err, "Connectivity check failed", "spoke", sc.Name)
		return r.failWithCondition(ctx, &sc, conditionTypeConnected, reasonConnectionFailed, err)
	}
	r.setCondition(&sc, conditionTypeConnected, metav1.ConditionTrue, reasonConnectionSucceeded, "spoke API server is reachable")

	// Provision spoke-side resources using standing kubeconfig
	spokeClient, err := r.NewSpokeClient(standingCfg)
	if err != nil {
		return r.failWithCondition(ctx, &sc, conditionTypeProvisioned, reasonProvisioningFailed, fmt.Errorf("creating spoke client: %w", err))
	}
	if err := provisioner.Provision(ctx, spokeClient); err != nil {
		log.Error(err, "Failed to provision spoke", "spoke", sc.Name)
		return r.failWithCondition(ctx, &sc, conditionTypeProvisioned, reasonProvisioningFailed, err)
	}
	r.setCondition(&sc, conditionTypeProvisioned, metav1.ConditionTrue, reasonProvisioningSucceeded, "spoke-side resources provisioned")

	// Set Ready=True only after all checks pass
	r.setCondition(&sc, conditionTypeReady, metav1.ConditionTrue, reasonManaged, "spoke is managed by hub")
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

// ensureStandingKubeconfig creates or updates the standing kubeconfig Secret,
// skipping the update if the content is unchanged.
func (r *SpokeClusterReconciler) ensureStandingKubeconfig(ctx context.Context, cfg *rest.Config, sc *hubv1alpha1.SpokeCluster) error {
	log := log.FromContext(ctx)

	standingSecret, err := credential.BuildStandingKubeconfig(cfg, sc, r.operatorNamespace, r.scheme)
	if err != nil {
		return fmt.Errorf("building standing kubeconfig: %w", err)
	}

	err = r.client.Create(ctx, standingSecret)
	if err == nil {
		log.Info("Created standing kubeconfig", "spoke", sc.Name, "secret", standingSecret.Name)
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating standing kubeconfig: %w", err)
	}

	var existingSecret corev1.Secret
	secretKey := client.ObjectKey{Name: standingSecret.Name, Namespace: standingSecret.Namespace}
	if err := r.client.Get(ctx, secretKey, &existingSecret); err != nil {
		return fmt.Errorf("getting existing standing kubeconfig: %w", err)
	}
	dataChanged := !bytes.Equal(existingSecret.Data[credential.KubeconfigKey], standingSecret.Data[credential.KubeconfigKey])
	ownerChanged := !ownerRefsEqual(existingSecret.OwnerReferences, standingSecret.OwnerReferences)
	if dataChanged || ownerChanged {
		existingSecret.Data = standingSecret.Data
		existingSecret.OwnerReferences = standingSecret.OwnerReferences
		if err := r.client.Update(ctx, &existingSecret); err != nil {
			return fmt.Errorf("updating standing kubeconfig: %w", err)
		}
		log.Info("Updated standing kubeconfig", "spoke", sc.Name, "secret", standingSecret.Name)
	}
	return nil
}

// cleanupSpokeResources deprovisions spoke-side resources and deletes the
// standing kubeconfig Secret. Best-effort — errors are logged but do not block.
// Does not early-return on missing Secret so spoke-side cleanup is always attempted.
func (r *SpokeClusterReconciler) cleanupSpokeResources(ctx context.Context, sc *hubv1alpha1.SpokeCluster) {
	logger := log.FromContext(ctx).WithValues("spoke", sc.Name)
	secretKey := client.ObjectKey{
		Name:      credential.StandingKubeconfigName(sc.Name),
		Namespace: r.operatorNamespace,
	}

	var spokeCleanupFailed bool

	// Try to deprovision spoke-side resources using standing kubeconfig
	var standingSecret corev1.Secret
	if err := r.client.Get(ctx, secretKey, &standingSecret); err == nil {
		kubeconfigBytes, ok := standingSecret.Data[credential.KubeconfigKey]
		if ok {
			cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
			if err != nil {
				logger.Error(err, "Failed to parse standing kubeconfig during cleanup")
				spokeCleanupFailed = true
			} else {
				cfg.Timeout = spokeDialTimeout
				spokeClient, err := r.NewSpokeClient(cfg)
				if err != nil {
					logger.Error(err, "Failed to create spoke client during cleanup")
					spokeCleanupFailed = true
				} else {
					logger.Info("Deprovisioning spoke resources")
					provisioner.Deprovision(ctx, spokeClient, logger)
				}
			}
		}
	} else if !apierrors.IsNotFound(err) {
		logger.Error(err, "Failed to read standing kubeconfig during cleanup")
		spokeCleanupFailed = true
	}

	if spokeCleanupFailed && r.recorder != nil {
		r.recorder.Eventf(sc, corev1.EventTypeWarning, "SpokeCleanupFailed",
			"Spoke-side resources were not cleaned up (spoke unreachable); hub-side cleanup proceeded")
	}

	// Always attempt to delete the standing kubeconfig Secret
	secret := &corev1.Secret{}
	secret.Name = secretKey.Name
	secret.Namespace = secretKey.Namespace
	if err := r.client.Delete(ctx, secret); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete standing kubeconfig during cleanup")
		}
	} else {
		logger.Info("Deleted standing kubeconfig", "secret", secretKey.Name)
	}
}

func (r *SpokeClusterReconciler) unmanageSpoke(ctx context.Context, sc *hubv1alpha1.SpokeCluster, reason, message string) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Unmanaging spoke", "spoke", sc.Name, "reason", reason)

	r.cleanupSpokeResources(ctx, sc)

	r.setCondition(sc, conditionTypeReady, metav1.ConditionFalse, reason, message)
	meta.RemoveStatusCondition(&sc.Status.Conditions, conditionTypeConnected)
	meta.RemoveStatusCondition(&sc.Status.Conditions, conditionTypeProvisioned)

	if err := r.client.Status().Update(ctx, sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status after unmanaging spoke: %w", err)
	}
	return ctrl.Result{}, nil
}

// failWithCondition sets a failure condition, clears Ready (since the spoke is
// no longer fully managed), updates status, and returns the error for workqueue backoff.
func (r *SpokeClusterReconciler) failWithCondition(ctx context.Context, sc *hubv1alpha1.SpokeCluster, condType, reason string, err error) (ctrl.Result, error) {
	r.setCondition(sc, condType, metav1.ConditionFalse, reason, err.Error())
	r.setCondition(sc, conditionTypeReady, metav1.ConditionFalse, reason, err.Error())
	if updateErr := r.client.Status().Update(ctx, sc); updateErr != nil {
		log.FromContext(ctx).Error(updateErr, "Failed to update status")
	}
	return ctrl.Result{}, err
}

// loadStandingKubeconfig reads the standing kubeconfig Secret and parses it
// into a rest.Config for connectivity checks and provisioning.
func (r *SpokeClusterReconciler) loadStandingKubeconfig(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (*rest.Config, error) {
	secretKey := client.ObjectKey{
		Name:      credential.StandingKubeconfigName(sc.Name),
		Namespace: r.operatorNamespace,
	}
	var secret corev1.Secret
	if err := r.client.Get(ctx, secretKey, &secret); err != nil {
		return nil, fmt.Errorf("reading standing kubeconfig: %w", err)
	}
	kubeconfigBytes, ok := secret.Data[credential.KubeconfigKey]
	if !ok {
		return nil, fmt.Errorf("standing kubeconfig secret %s missing %q key", secretKey.Name, credential.KubeconfigKey)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing standing kubeconfig: %w", err)
	}
	cfg.Timeout = spokeDialTimeout
	return cfg, nil
}

func ownerRefsEqual(a, b []metav1.OwnerReference) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].UID != b[i].UID || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
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

func (r *SpokeClusterReconciler) mapHubConfigToSpokeClusters(ctx context.Context, _ client.Object) []reconcile.Request {
	logger := log.FromContext(ctx)
	var spokeList hubv1alpha1.SpokeClusterList
	if err := r.client.List(ctx, &spokeList); err != nil {
		logger.Error(err, "Failed to list SpokeCluster CRs for HubConfig event mapping")
		return nil
	}
	requests := make([]reconcile.Request, len(spokeList.Items))
	for i, sc := range spokeList.Items {
		requests[i] = reconcile.Request{NamespacedName: types.NamespacedName{Name: sc.Name}}
	}
	return requests
}

func (r *SpokeClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("spokecluster-controller") //nolint:staticcheck // Using old events API for consistency
	return ctrl.NewControllerManagedBy(mgr).
		For(&hubv1alpha1.SpokeCluster{}).
		Watches(&hubv1alpha1.HubConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapHubConfigToSpokeClusters)).
		Complete(r)
}
