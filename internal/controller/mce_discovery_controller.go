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
	"strings"
	"sync"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/credential"
)

const (
	// localClusterLabel is the label that identifies the hub self-import
	// ManagedCluster. It is always excluded from auto-discovery.
	localClusterLabel = "local-cluster"

	// singletonKey is the reconcile key for inventory reconciliation.
	// All events map to this key so the controller processes a full snapshot.
	singletonKey = "cluster"

	// msaGroup and msaVersion identify the ManagedServiceAccount API.
	msaGroup   = "authentication.open-cluster-management.io"
	msaVersion = "v1beta1"
	msaKind    = "ManagedServiceAccount"

	// ManifestWork API for pushing spoke-side RBAC.
	manifestWorkGroup   = "work.open-cluster-management.io"
	manifestWorkVersion = "v1"
	manifestWorkKind    = "ManifestWork"

	// msaRBACWorkName is the ManifestWork that pushes the ClusterRoleBinding
	// for the MSA SA on the spoke.
	msaRBACWorkName = "lightspeed-hub-msa-rbac"

	// msaSpokeNamespace is the namespace where MCE creates the MSA SA on the spoke.
	msaSpokeNamespace = "open-cluster-management-agent-addon"

	// spokeProvisionedNamespace is the namespace provisioned on each spoke
	// for hub-managed resources (SA, secrets, adapter).
	spokeProvisionedNamespace = "openshift-lightspeed-managed"
)

var managedClusterGVK = schema.GroupVersionKind{
	Group:   "cluster.open-cluster-management.io",
	Version: "v1",
	Kind:    "ManagedCluster",
}

// MCEDiscoveryReconciler watches ManagedCluster CRs and creates/deletes
// SpokeCluster CRs for clusters matching the HubConfig MCE selector.
// It uses a singleton reconcile key for full inventory snapshot reconciliation.
type MCEDiscoveryReconciler struct {
	client    client.Client
	apiReader client.Reader // uncached reader for ManagedCluster operations

	// Lazy ManagedCluster watch registration. The watch is registered
	// only after confirming the ManagedCluster API exists, avoiding
	// startup failures when MCE is not installed.
	mu              sync.Mutex
	watchRegistered bool
	ctrlRef         ctrlcontroller.Controller
	cacheRef        cache.Cache
}

func NewMCEDiscoveryReconciler(
	hubClient client.Client,
	apiReader client.Reader,
) *MCEDiscoveryReconciler {
	return &MCEDiscoveryReconciler{
		client:    hubClient,
		apiReader: apiReader,
	}
}

func (r *MCEDiscoveryReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Read HubConfig to determine mode and selector
	var hubConfig hubv1alpha1.HubConfig
	if err := r.client.Get(ctx, client.ObjectKey{Name: singletonKey}, &hubConfig); err != nil {
		if apierrors.IsNotFound(err) {
			// No HubConfig — nothing to discover, but clean up any orphaned
			// discovery SpokeCluster CRs and their companions.
			if err := r.cleanupAllDiscoverySpokes(ctx); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.cleanupOrphanedCompanions(ctx)
		}
		return ctrl.Result{}, err
	}

	if hubConfig.Spec.ClusterRegistryMode != hubv1alpha1.ClusterRegistryModeMCE {
		// Not in MCE mode — no discovery. Don't delete existing discovery
		// spokes on mode change; the SpokeCluster controller handles
		// unmanaging via credential source mismatch.
		return ctrl.Result{}, nil
	}

	// List ManagedClusters via uncached reader
	mcList := &unstructured.UnstructuredList{}
	mcList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   managedClusterGVK.Group,
		Version: managedClusterGVK.Version,
		Kind:    managedClusterGVK.Kind + "List",
	})
	if err := r.apiReader.List(ctx, mcList); err != nil {
		// If the ManagedCluster API is not installed, this is expected
		// on non-MCE clusters. Retry through workqueue backoff.
		return ctrl.Result{}, fmt.Errorf("listing ManagedClusters: %w", err)
	}

	// Register ManagedCluster watch lazily on first successful List
	r.tryRegisterWatch(ctx)

	// Get the selector from HubConfig
	selector := hubConfig.Spec.MCE
	var matchLabels map[string]string
	if selector != nil && selector.Selector != nil {
		matchLabels = selector.Selector.MatchLabels
	}

	// Build the desired set of discovered spokes
	desired := make(map[string]discoveredSpoke)
	for i := range mcList.Items {
		mc := &mcList.Items[i]
		name := mc.GetName()

		// Exclude the hub self-import (local-cluster=true)
		mcLabels := mc.GetLabels()
		if mcLabels[localClusterLabel] == "true" {
			logger.V(2).Info("Skipping hub self-import", "managedCluster", name)
			continue
		}

		// Apply selector filter
		if !matchesSelector(mcLabels, matchLabels) {
			continue
		}

		// Extract API server URL
		apiServer, err := extractAPIServer(mc)
		if err != nil {
			logger.Error(err, "Skipping ManagedCluster with invalid API config", "managedCluster", name)
			continue
		}

		desired[name] = discoveredSpoke{
			apiServer:          apiServer,
			managedClusterName: name,
		}
	}

	// List existing discovery-owned SpokeCluster CRs
	var spokeList hubv1alpha1.SpokeClusterList
	if err := r.client.List(ctx, &spokeList, client.MatchingLabels{
		credential.MCEManagedByLabel: credential.MCEManagedByValue,
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing discovery SpokeClusters: %w", err)
	}

	existing := make(map[string]*hubv1alpha1.SpokeCluster, len(spokeList.Items))
	for i := range spokeList.Items {
		existing[spokeList.Items[i].Name] = &spokeList.Items[i]
	}

	// Create or update desired SpokeClusters
	for name, ds := range desired {
		if sc, ok := existing[name]; ok {
			// Restore discovery-owned fields if they drifted
			needsUpdate := false
			if sc.Spec.APIServer != ds.apiServer {
				sc.Spec.APIServer = ds.apiServer
				needsUpdate = true
			}
			if sc.Spec.CredentialSource.MCE == nil || sc.Spec.CredentialSource.MCE.ManagedClusterName != ds.managedClusterName {
				sc.Spec.CredentialSource = hubv1alpha1.CredentialSource{
					MCE: &hubv1alpha1.MCECredentialSource{
						ManagedClusterName: ds.managedClusterName,
					},
				}
				needsUpdate = true
			}
			if needsUpdate {
				if err := r.client.Update(ctx, sc); err != nil {
					return ctrl.Result{}, fmt.Errorf("updating SpokeCluster %q: %w", name, err)
				}
				logger.Info("Restored discovery SpokeCluster fields", "spoke", name)
			}
		} else {
			// Check for collision with manually created SpokeCluster
			var existingManual hubv1alpha1.SpokeCluster
			if err := r.client.Get(ctx, client.ObjectKey{Name: name}, &existingManual); err == nil {
				// Same-name CR exists without the discovery label — collision
				logger.Error(nil, "Cannot create discovery SpokeCluster: name collision with existing CR",
					"managedCluster", name, "existingLabels", existingManual.Labels)
				continue
			} else if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("checking for collision on %q: %w", name, err)
			}

			// Create new SpokeCluster
			sc := &hubv1alpha1.SpokeCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
					Labels: map[string]string{
						credential.MCEManagedByLabel: credential.MCEManagedByValue,
					},
				},
				Spec: hubv1alpha1.SpokeClusterSpec{
					APIServer: ds.apiServer,
					CredentialSource: hubv1alpha1.CredentialSource{
						MCE: &hubv1alpha1.MCECredentialSource{
							ManagedClusterName: ds.managedClusterName,
						},
					},
				},
			}
			if err := r.client.Create(ctx, sc); err != nil {
				if apierrors.IsAlreadyExists(err) {
					// Race — already created by another reconcile
					continue
				}
				return ctrl.Result{}, fmt.Errorf("creating SpokeCluster %q: %w", name, err)
			}
			logger.Info("Created discovery SpokeCluster", "spoke", name, "apiServer", ds.apiServer)
		}

		// Ensure MSA and spoke RBAC exist for every desired spoke.
		// Both are idempotent; retrying on every reconcile recovers from
		// transient failures and crashes between operations.
		if err := r.ensureMSA(ctx, ds.managedClusterName); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensuring MSA for %q: %w", name, err)
		}
		if err := r.ensureMSARBAC(ctx, ds.managedClusterName); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensuring spoke RBAC for %q: %w", name, err)
		}
	}

	// Delete discovery SpokeClusters no longer in the desired set.
	// The SpokeCluster must be fully gone (finalizer complete) before we
	// revoke MSA/RBAC, because the finalizer uses the standing kubeconfig
	// (backed by the MSA token) for spoke-side cleanup.
	for name, sc := range existing {
		if _, ok := desired[name]; !ok {
			if sc.DeletionTimestamp.IsZero() {
				// Not yet deleting — initiate deletion. The SpokeCluster watch
				// will requeue when the finalizer completes.
				if err := r.client.Delete(ctx, sc); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("deleting SpokeCluster %q: %w", name, err)
				}
				logger.Info("Initiated SpokeCluster deletion, waiting for finalizer", "spoke", name)
			}
			// else: deletion in progress, wait for finalizer to complete.
			// MSA/RBAC stay alive until the SpokeCluster is fully gone.
		}
	}

	if err := r.cleanupOrphanedCompanions(ctx); err != nil {
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Discovery reconciliation complete", "desired", len(desired), "existing", len(existing))
	return ctrl.Result{}, nil
}

// ensureMSA creates a ManagedServiceAccount in the spoke's hub namespace.
// MCE's managed-serviceaccount addon creates the SA on the spoke, fetches
// a token, and stores it in a Secret with the same name. Idempotent.
func (r *MCEDiscoveryReconciler) ensureMSA(ctx context.Context, managedClusterName string) error {
	msa := &unstructured.Unstructured{}
	msa.SetGroupVersionKind(schema.GroupVersionKind{Group: msaGroup, Version: msaVersion, Kind: msaKind})
	msa.SetName(credential.MSAName)
	msa.SetNamespace(managedClusterName)
	msa.SetLabels(map[string]string{
		credential.MCEManagedByLabel: credential.MCEManagedByValue,
	})
	_ = unstructured.SetNestedField(msa.Object, true, "spec", "rotation", "enabled")
	_ = unstructured.SetNestedField(msa.Object, "8760h0m0s", "spec", "rotation", "validity")

	if err := r.client.Create(ctx, msa); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var existing unstructured.Unstructured
			existing.SetGroupVersionKind(schema.GroupVersionKind{Group: msaGroup, Version: msaVersion, Kind: msaKind})
			if getErr := r.client.Get(ctx, client.ObjectKey{Name: credential.MSAName, Namespace: managedClusterName}, &existing); getErr != nil {
				return fmt.Errorf("verifying MSA ownership %s/%s: %w", managedClusterName, credential.MSAName, getErr)
			}
			if existing.GetLabels()[credential.MCEManagedByLabel] != credential.MCEManagedByValue {
				return fmt.Errorf("ManagedServiceAccount %s/%s exists but is not owned by discovery (labels: %v)",
					managedClusterName, credential.MSAName, existing.GetLabels())
			}
			return nil
		}
		return fmt.Errorf("creating ManagedServiceAccount %s/%s: %w", managedClusterName, credential.MSAName, err)
	}
	return nil
}

// ensureMSARBAC creates a ManifestWork that pushes a ClusterRoleBinding to the
// spoke, granting the MSA SA permissions to provision resources. The work-manager
// addon on the spoke applies it automatically. Idempotent.
func (r *MCEDiscoveryReconciler) ensureMSARBAC(ctx context.Context, managedClusterName string) error {
	mw := &unstructured.Unstructured{}
	mw.SetGroupVersionKind(schema.GroupVersionKind{Group: manifestWorkGroup, Version: manifestWorkVersion, Kind: manifestWorkKind})
	mw.SetName(msaRBACWorkName)
	mw.SetNamespace(managedClusterName)
	mw.SetLabels(map[string]string{
		credential.MCEManagedByLabel: credential.MCEManagedByValue,
	})

	// Cluster-scoped permissions: namespace lifecycle, RBAC (agentic creates
	// Roles/ClusterRoles in arbitrary namespaces for sandbox execution),
	// Route/ConfigMap for spoke discovery.
	clusterRole := map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata": map[string]interface{}{
			"name": "lightspeed-hub-spoke-provisioner",
		},
		"rules": []interface{}{
			map[string]interface{}{
				"apiGroups": []interface{}{""},
				"resources": []interface{}{"namespaces"},
				"verbs":     []interface{}{"create", "get"},
			},
			map[string]interface{}{
				"apiGroups":     []interface{}{""},
				"resources":     []interface{}{"namespaces"},
				"verbs":         []interface{}{"delete"},
				"resourceNames": []interface{}{spokeProvisionedNamespace},
			},
			map[string]interface{}{
				"apiGroups": []interface{}{""},
				"resources": []interface{}{"configmaps"},
				"verbs":     []interface{}{"get"},
			},
			map[string]interface{}{
				"apiGroups": []interface{}{"rbac.authorization.k8s.io"},
				"resources": []interface{}{"clusterroles", "roles"},
				"verbs":     []interface{}{"create", "get", "update", "delete", "bind", "escalate"},
			},
			map[string]interface{}{
				"apiGroups": []interface{}{"rbac.authorization.k8s.io"},
				"resources": []interface{}{"clusterrolebindings", "rolebindings"},
				"verbs":     []interface{}{"create", "get", "delete"},
			},
			map[string]interface{}{
				"apiGroups": []interface{}{"route.openshift.io"},
				"resources": []interface{}{"routes"},
				"verbs":     []interface{}{"get"},
			},
		},
	}

	crb := map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata": map[string]interface{}{
			"name": "lightspeed-hub-msa-access",
		},
		"roleRef": map[string]interface{}{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     "lightspeed-hub-spoke-provisioner",
		},
		"subjects": []interface{}{
			map[string]interface{}{
				"kind":      "ServiceAccount",
				"name":      credential.MSAName,
				"namespace": msaSpokeNamespace,
			},
		},
	}

	// Namespace-scoped permissions for the provisioned namespace:
	// SA/Secret management and token requests.
	nsRole := map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "Role",
		"metadata": map[string]interface{}{
			"name":      "lightspeed-hub-spoke-provisioner",
			"namespace": spokeProvisionedNamespace,
		},
		"rules": []interface{}{
			map[string]interface{}{
				"apiGroups": []interface{}{""},
				"resources": []interface{}{"serviceaccounts", "secrets"},
				"verbs":     []interface{}{"create", "get", "delete"},
			},
			map[string]interface{}{
				"apiGroups": []interface{}{""},
				"resources": []interface{}{"serviceaccounts/token"},
				"verbs":     []interface{}{"create"},
			},
		},
	}

	nsRB := map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "RoleBinding",
		"metadata": map[string]interface{}{
			"name":      "lightspeed-hub-msa-access",
			"namespace": spokeProvisionedNamespace,
		},
		"roleRef": map[string]interface{}{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "Role",
			"name":     "lightspeed-hub-spoke-provisioner",
		},
		"subjects": []interface{}{
			map[string]interface{}{
				"kind":      "ServiceAccount",
				"name":      credential.MSAName,
				"namespace": msaSpokeNamespace,
			},
		},
	}

	_ = unstructured.SetNestedSlice(mw.Object, []interface{}{clusterRole, crb, nsRole, nsRB}, "spec", "workload", "manifests")

	if err := r.client.Create(ctx, mw); err != nil {
		if apierrors.IsAlreadyExists(err) {
			var existing unstructured.Unstructured
			existing.SetGroupVersionKind(schema.GroupVersionKind{Group: manifestWorkGroup, Version: manifestWorkVersion, Kind: manifestWorkKind})
			if getErr := r.client.Get(ctx, client.ObjectKey{Name: msaRBACWorkName, Namespace: managedClusterName}, &existing); getErr != nil {
				return fmt.Errorf("verifying ManifestWork ownership %s/%s: %w", managedClusterName, msaRBACWorkName, getErr)
			}
			if existing.GetLabels()[credential.MCEManagedByLabel] != credential.MCEManagedByValue {
				return fmt.Errorf("ManifestWork %s/%s exists but is not owned by discovery (labels: %v)",
					managedClusterName, msaRBACWorkName, existing.GetLabels())
			}
			// Reconcile the spec so RBAC rule changes reach existing spokes.
			desiredManifests, _, _ := unstructured.NestedSlice(mw.Object, "spec", "workload", "manifests")
			_ = unstructured.SetNestedSlice(existing.Object, desiredManifests, "spec", "workload", "manifests")
			if updateErr := r.client.Update(ctx, &existing); updateErr != nil {
				return fmt.Errorf("updating ManifestWork spec %s/%s: %w", managedClusterName, msaRBACWorkName, updateErr)
			}
			return nil
		}
		return fmt.Errorf("creating RBAC ManifestWork %s/%s: %w", managedClusterName, msaRBACWorkName, err)
	}
	return nil
}

// deleteMSARBAC removes the spoke RBAC ManifestWork. The work-manager addon
// deletes the ClusterRoleBinding from the spoke. Returns an error so the
// caller can prevent MSA deletion when the ManifestWork is still present.
func (r *MCEDiscoveryReconciler) deleteMSARBAC(ctx context.Context, managedClusterName string, logger logr.Logger) error {
	var mw unstructured.Unstructured
	mw.SetGroupVersionKind(schema.GroupVersionKind{Group: manifestWorkGroup, Version: manifestWorkVersion, Kind: manifestWorkKind})
	if err := r.client.Get(ctx, client.ObjectKey{Name: msaRBACWorkName, Namespace: managedClusterName}, &mw); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting RBAC ManifestWork %s/%s: %w", managedClusterName, msaRBACWorkName, err)
	}
	if mw.GetLabels()[credential.MCEManagedByLabel] != credential.MCEManagedByValue {
		logger.Info("Skipping foreign RBAC ManifestWork", "namespace", managedClusterName, "labels", mw.GetLabels())
		return nil
	}
	if err := r.client.Delete(ctx, &mw); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting RBAC ManifestWork %s/%s: %w", managedClusterName, msaRBACWorkName, err)
	}
	return nil
}

// deleteMSA removes the ManagedServiceAccount from the spoke's hub namespace.
// MCE cleans up the SA on the spoke. Only deletes if owned by discovery.
func (r *MCEDiscoveryReconciler) deleteMSA(ctx context.Context, managedClusterName string, logger logr.Logger) error {
	var msa unstructured.Unstructured
	msa.SetGroupVersionKind(schema.GroupVersionKind{Group: msaGroup, Version: msaVersion, Kind: msaKind})
	if err := r.client.Get(ctx, client.ObjectKey{Name: credential.MSAName, Namespace: managedClusterName}, &msa); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting ManagedServiceAccount %s/%s: %w", managedClusterName, credential.MSAName, err)
	}
	if msa.GetLabels()[credential.MCEManagedByLabel] != credential.MCEManagedByValue {
		logger.Info("Skipping foreign ManagedServiceAccount", "namespace", managedClusterName, "labels", msa.GetLabels())
		return nil
	}
	if err := r.client.Delete(ctx, &msa); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting ManagedServiceAccount %s/%s: %w", managedClusterName, credential.MSAName, err)
	}
	return nil
}

// cleanupOrphanedCompanions finds discovery-owned MSAs whose SpokeCluster
// no longer exists and deletes the ManifestWork first, then the MSA.
func (r *MCEDiscoveryReconciler) cleanupOrphanedCompanions(ctx context.Context) error {
	logger := log.FromContext(ctx)
	msaList := &unstructured.UnstructuredList{}
	msaList.SetGroupVersionKind(schema.GroupVersionKind{Group: msaGroup, Version: msaVersion, Kind: msaKind + "List"})
	if err := r.client.List(ctx, msaList, client.MatchingLabels{
		credential.MCEManagedByLabel: credential.MCEManagedByValue,
	}); err != nil {
		return fmt.Errorf("listing discovery MSAs for orphan cleanup: %w", err)
	}
	for i := range msaList.Items {
		msaNS := msaList.Items[i].GetNamespace()
		var sc hubv1alpha1.SpokeCluster
		err := r.client.Get(ctx, client.ObjectKey{Name: msaNS}, &sc)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("checking SpokeCluster %q for orphan cleanup: %w", msaNS, err)
		}
		// Clean up if the SpokeCluster is gone, or if a same-named manual
		// CR replaced it (the discovery-owned MSA no longer belongs to it).
		if apierrors.IsNotFound(err) || sc.Labels[credential.MCEManagedByLabel] != credential.MCEManagedByValue {
			// Delete ManifestWork first; only delete MSA if RBAC cleanup
			// succeeds, so the MSA stays as a retry record on failure.
			if rbacErr := r.deleteMSARBAC(ctx, msaNS, logger); rbacErr != nil {
				return fmt.Errorf("cleaning up RBAC for orphaned spoke %q: %w", msaNS, rbacErr)
			}
			if msaErr := r.deleteMSA(ctx, msaNS, logger); msaErr != nil {
				return fmt.Errorf("cleaning up MSA for orphaned spoke %q: %w", msaNS, msaErr)
			}
			logger.Info("Cleaned up orphaned MSA/RBAC", "namespace", msaNS)
		}
	}
	return nil
}

// cleanupAllDiscoverySpokes initiates deletion of all discovery-owned
// SpokeClusters. Companion cleanup runs via cleanupOrphanedCompanions
// after the finalizers complete.
func (r *MCEDiscoveryReconciler) cleanupAllDiscoverySpokes(ctx context.Context) error {
	var spokeList hubv1alpha1.SpokeClusterList
	if err := r.client.List(ctx, &spokeList, client.MatchingLabels{
		credential.MCEManagedByLabel: credential.MCEManagedByValue,
	}); err != nil {
		return fmt.Errorf("listing discovery SpokeClusters for cleanup: %w", err)
	}
	for i := range spokeList.Items {
		sc := &spokeList.Items[i]
		if sc.DeletionTimestamp.IsZero() {
			if err := r.client.Delete(ctx, sc); err != nil {
				if !apierrors.IsNotFound(err) {
					return fmt.Errorf("deleting SpokeCluster %q during cleanup: %w", sc.Name, err)
				}
			}
		}
	}
	return nil
}

type discoveredSpoke struct {
	apiServer          string
	managedClusterName string
}

// matchesSelector checks if a ManagedCluster's labels match the selector.
// A nil or empty selector matches everything.
func matchesSelector(mcLabels, matchLabels map[string]string) bool {
	if len(matchLabels) == 0 {
		return true
	}
	for k, v := range matchLabels {
		actual, exists := mcLabels[k]
		if !exists || actual != v {
			return false
		}
	}
	return true
}

// extractAPIServer reads the first HTTPS URL from the ManagedCluster's
// spec.managedClusterClientConfigs[].url field.
func extractAPIServer(mc *unstructured.Unstructured) (string, error) {
	configs, found, err := unstructured.NestedSlice(mc.Object, "spec", "managedClusterClientConfigs")
	if err != nil || !found || len(configs) == 0 {
		return "", fmt.Errorf("ManagedCluster %q has no managedClusterClientConfigs", mc.GetName())
	}

	for _, cfg := range configs {
		cfgMap, ok := cfg.(map[string]interface{})
		if !ok {
			continue
		}
		url, ok := cfgMap["url"].(string)
		if !ok || url == "" {
			continue
		}
		if strings.HasPrefix(url, "https://") {
			return url, nil
		}
	}

	return "", fmt.Errorf("ManagedCluster %q has no HTTPS client config URL", mc.GetName())
}

// tryRegisterWatch lazily registers a ManagedCluster watch source with the
// controller. Called after confirming the ManagedCluster API exists.
func (r *MCEDiscoveryReconciler) tryRegisterWatch(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.watchRegistered || r.ctrlRef == nil || r.cacheRef == nil {
		return
	}

	mc := &unstructured.Unstructured{}
	mc.SetGroupVersionKind(managedClusterGVK)

	if err := r.ctrlRef.Watch(source.Kind(r.cacheRef, mc,
		handler.TypedEnqueueRequestsFromMapFunc(
			func(_ context.Context, _ *unstructured.Unstructured) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: singletonKey}}}
			},
		),
	)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to register ManagedCluster watch (will retry)")
		return
	}

	r.watchRegistered = true
	log.FromContext(ctx).Info("Registered ManagedCluster watch")
}

func (r *MCEDiscoveryReconciler) mapHubConfigToSingleton(_ context.Context, _ client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: singletonKey}}}
}

func (r *MCEDiscoveryReconciler) mapSpokeClusterToSingleton(_ context.Context, obj client.Object) []reconcile.Request {
	// Only trigger for discovery-owned SpokeClusters
	if obj.GetLabels()[credential.MCEManagedByLabel] == credential.MCEManagedByValue {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: singletonKey}}}
	}
	return nil
}

// +kubebuilder:rbac:groups=hub.openshift.io,resources=spokeclusters,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=multicluster.openshift.io,resources=multiclusterengines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=authentication.open-cluster-management.io,resources=managedserviceaccounts,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=work.open-cluster-management.io,resources=manifestworks,verbs=get;list;watch;create;update;delete

func (r *MCEDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrlInstance, err := ctrl.NewControllerManagedBy(mgr).
		Named("mce-discovery").
		// HubConfig is the primary trigger
		Watches(&hubv1alpha1.HubConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapHubConfigToSingleton)).
		// Watch discovery-owned SpokeCluster changes (deletion by other controllers)
		Watches(&hubv1alpha1.SpokeCluster{}, handler.EnqueueRequestsFromMapFunc(r.mapSpokeClusterToSingleton)).
		Build(r)
	if err != nil {
		return err
	}

	// Store references for lazy ManagedCluster watch registration
	r.ctrlRef = ctrlInstance
	r.cacheRef = mgr.GetCache()

	return nil
}
