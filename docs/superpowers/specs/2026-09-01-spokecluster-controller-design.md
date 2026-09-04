# SpokeCluster Controller Design

**Ticket:** OLS-3947
**Spec:** `.ai/spec/what/spoke-lifecycle.md` (rules 1-23)
**Scope:** SpokeCluster controller, credential broker (secret mode only), spoke provisioning, standing kubeconfig, health monitoring, and decommission.

## Out of Scope

- MCE credential source (secret mode only in this ticket)
- Adapter orchestrator and `AdaptersReady` condition
- MCE auto-discovery

## Components

### 1. Credential Broker

**Package:** `internal/credential`

**Interface:**

```go
type CredentialSource interface {
    GetRESTConfig(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (*rest.Config, error)
}
```

**SecretCredentialSource** (`secret.go`): Reads the admin-provided kubeconfig Secret referenced by `sc.Spec.CredentialSource.Secret` (name + namespace) from the hub cluster. Parses the kubeconfig bytes into a `rest.Config` using `clientcmd.RESTConfigFromKubeConfig()`.

### 2. Standing Kubeconfig

**Package:** `internal/credential`

**File:** `kubeconfig.go`

Builds a normalized standing kubeconfig Secret from a `rest.Config` + the SpokeCluster's `spec.apiServer`. The Secret:

- Name: `spoke-kubeconfig-{SpokeCluster.metadata.name}`
- Namespace: operator namespace (passed via flag/env, typically `openshift-lightspeed`)
- Data key: `kubeconfig` — standard kubeconfig YAML with one cluster/user/context
- Owner reference: SpokeCluster CR (enables auto-GC on deletion)

The normalized format extracts server URL, CA data, and auth credentials (token or client cert) from the `rest.Config` and writes a minimal kubeconfig. Consumers use `clientcmd.RESTConfigFromKubeConfig()` to get back a `rest.Config`.

### 3. Spoke Provisioner

**Package:** `internal/provisioner`

**File:** `spoke.go`

Given a Kubernetes client for the spoke cluster, creates these resources:

1. `openshift-lightspeed-managed` Namespace
2. `lightspeed-agent` ServiceAccount in `openshift-lightspeed-managed`
3. `lightspeed-hub:cluster-reader` ClusterRoleBinding — binds `openshift-lightspeed-managed/lightspeed-agent` to `cluster-reader` ClusterRole
4. `lightspeed-hub:cluster-monitoring-view` ClusterRoleBinding — binds `openshift-lightspeed-managed/lightspeed-agent` to `cluster-monitoring-view` ClusterRole

Idempotent: uses Create + handle `AlreadyExists` (not Get-then-Create), per project convention.

Also provides `Deprovision()` for decommission: deletes the four resources in reverse order, best-effort (errors logged, not returned).

### 4. SpokeCluster Controller

**Package:** `internal/controller`

**File:** `spokecluster_controller.go`

Reconciles `SpokeCluster` CRs. Uses controller-runtime's standard reconciler pattern.

**Dependencies (injected):**

- Hub client (`client.Client`) — reads/writes on the hub cluster
- `CredentialSource` — resolves spoke credentials
- Operator namespace (`string`) — where standing kubeconfig Secrets live
- Spoke client factory (`func(*rest.Config) (client.Client, error)`) — creates clients for remote spoke clusters (mockable in tests)

**Reconciliation flow:**

```
Reconcile(SpokeCluster)
  1. Add finalizer if missing
  2. If being deleted:
     a. Build spoke client from standing kubeconfig (if it exists)
     b. Deprovision spoke-side resources (best-effort)
     c. Remove finalizer
     d. Return (standing kubeconfig auto-GC'd via owner reference)
  3. Get credentials via broker → rest.Config
     - On error: set Connected=False, requeue 30s
  4. Create/update standing kubeconfig Secret (with owner reference)
  5. Validate connectivity: discovery client ServerVersion()
     - On error: set Connected=False, requeue 30s
  6. Provision spoke-side resources (idempotent)
  7. Set Connected=True
  8. Requeue after 5 minutes (health check)
```

**Status conditions:**

| Condition | True | False |
|-----------|------|-------|
| `Connected` | Spoke API server reachable via standing kubeconfig | Connectivity check failed |

Conditions use `meta.SetStatusCondition()` with appropriate reason strings:
- `Connected=True`: reason `ConnectionSucceeded`
- `Connected=False`: reason `ConnectionFailed`, `CredentialError`

**Health check:** The controller requeues every 5 minutes. On each requeue, it re-runs the full reconcile (which re-validates connectivity). If the spoke is unreachable, it sets `Connected=False` and requeues after 30 seconds for faster recovery detection. This pattern means health checks are just regular reconciliation — no separate timer needed.

**Spoke isolation:** Each SpokeCluster CR is reconciled independently by the controller-runtime work queue. One failing spoke's reconcile does not block others. Timeouts on spoke API calls (10 second dial timeout on the rest.Config) prevent a hung spoke from blocking the reconcile goroutine.

**Decommission:**

Finalizer: `hub.openshift.io/spoke-cleanup`

On delete:
1. Read standing kubeconfig Secret from hub (may not exist if registration never completed)
2. If it exists, build a spoke client and call `Deprovision()` — best-effort, errors logged
3. Remove finalizer → CR deletes → owner reference auto-GC's standing kubeconfig Secret

If the spoke is unreachable during decommission, hub-side cleanup still proceeds (finalizer removed, Secret GC'd). Spoke-side resources remain but are harmless (read-only SA, no secrets).

## RBAC

The controller needs these permissions (kubebuilder markers):

On the hub:
- `spokeclusters` — get, list, watch, update, patch (status + finalizer)
- `hubconfigs` — get, list, watch (read-only, for webhook — already exists)
- `secrets` — get, list, watch, create, update, delete (standing kubeconfig + admin kubeconfig)
- `events` — create, patch (for recording events)

On the spoke (via remote client, not RBAC-managed):
- namespaces, serviceaccounts, clusterrolebindings — create, delete

## Wiring in main.go

```go
credSource := credential.NewSecretCredentialSource(mgr.GetClient())
if err := controller.NewSpokeClusterReconciler(
    mgr.GetClient(),
    credSource,
    namespace,
).SetupWithManager(mgr); err != nil {
    log.Error(err, "unable to create controller", "controller", "SpokeCluster")
    os.Exit(1)
}
```

## Testing Strategy

All tests use fake/mock clients — no real clusters needed.

**Credential broker tests:**
- SecretCredentialSource: hub client has a kubeconfig Secret → verify rest.Config fields
- SecretCredentialSource: Secret not found → verify error

**Standing kubeconfig tests:**
- Build from rest.Config with token auth → verify kubeconfig YAML structure
- Build from rest.Config with client cert auth → verify kubeconfig YAML structure
- Owner reference set correctly

**Provisioner tests:**
- Provision: verify 4 resources created on spoke
- Provision idempotent: resources already exist → no error
- Deprovision: verify 4 resources deleted
- Deprovision best-effort: spoke unreachable → no error returned

**Controller tests (envtest or fake client):**
- Happy path: create SpokeCluster → Connected=True, standing kubeconfig exists, spoke resources provisioned
- Credential error: admin Secret missing → Connected=False
- Connectivity failure: spoke unreachable → Connected=False
- Delete with finalizer: spoke resources cleaned up, finalizer removed
- Delete when spoke unreachable: finalizer still removed (best-effort)
- Idempotent: re-reconcile converges without side effects

## File Layout

| File | Lines (est.) | Purpose |
|------|-------------|---------|
| `internal/credential/broker.go` | ~15 | `CredentialSource` interface |
| `internal/credential/secret.go` | ~50 | `SecretCredentialSource` |
| `internal/credential/secret_test.go` | ~80 | Credential broker tests |
| `internal/credential/kubeconfig.go` | ~80 | Standing kubeconfig builder |
| `internal/credential/kubeconfig_test.go` | ~100 | Kubeconfig builder tests |
| `internal/provisioner/spoke.go` | ~100 | Spoke provisioning + cleanup |
| `internal/provisioner/spoke_test.go` | ~120 | Provisioner tests |
| `internal/controller/spokecluster_controller.go` | ~200 | Controller reconciler |
| `internal/controller/spokecluster_controller_test.go` | ~250 | Controller tests |
| `cmd/main.go` (modify) | +15 | Wire controller |
