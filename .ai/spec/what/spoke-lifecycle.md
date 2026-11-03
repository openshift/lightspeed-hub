# Spoke Lifecycle

Full lifecycle of a spoke cluster from registration through decommission.

## Behavioral Rules

### Registration

1. A spoke is registered by a `SpokeCluster` CR appearing on the hub cluster. The CR source depends on `HubConfig.spec.clusterRegistryMode`:
   - `secret`: admin creates the SpokeCluster CR manually with `spec.apiServer` and `spec.credentialSource.secret`.
   - [PLANNED: OLS-3954] `mce`: hub continuously discovers all MCE ManagedClusters by default, including future additions; optional `HubConfig.spec.mce.selector.matchLabels` restricts scope. Documentation must warn about default-all enrollment.
2. The CR MUST include `spec.apiServer` and `spec.credentialSource` matching the HubConfig mode. If the credential source type does not match `clusterRegistryMode`, the SpokeCluster is not managed and a status condition indicates the mismatch.
3. The hub MUST validate that the spoke is reachable and the credentials are valid before marking the spoke as `Connected=True`. Connectivity MUST be validated using the standing kubeconfig (the saved copy), not the original admin kubeconfig.
4. MVP uses direct kube-api connectivity. The admin is responsible for ensuring network path between hub and spoke.
4a. The admin kubeconfig MUST contain a static token or client certificate. Exec-based auth (e.g. `oc login`) is not supported because adapter pods cannot run the exec plugin. Kubeconfigs without static credentials MUST be rejected.
4b. When no HubConfig exists, SpokeCluster CRs are ignored — no provisioning, no connectivity checks, no standing kubeconfigs.

### Spoke Provisioning

5. After successful connectivity validation, the hub operator MUST provision the spoke with the following resources via remote kube-api:
   - Create `openshift-lightspeed-managed` namespace on the spoke (if it does not exist). This namespace is separate from `openshift-lightspeed` because the spoke may have its own standalone OLS installation in `openshift-lightspeed` — using a separate namespace avoids SA and RBAC collisions.
   - Create `lightspeed-agent` ServiceAccount in `openshift-lightspeed-managed` on the spoke.
   - Create `cluster-reader` ClusterRoleBinding binding `openshift-lightspeed-managed/lightspeed-agent` to the `cluster-reader` ClusterRole.
   - Create `cluster-monitoring-view` ClusterRoleBinding binding `openshift-lightspeed-managed/lightspeed-agent` to the `cluster-monitoring-view` ClusterRole.
6. These spoke-side resources establish the reader RBAC pattern that the agentic-operator's `addReaderSubject` uses — per-step SAs are added to these same ClusterRoleBindings to inherit cluster-wide read access. This is identical to how `lightspeed-agent` is used in single-cluster mode, but in the `openshift-lightspeed-managed` namespace to avoid conflicts with a spoke-local OLS installation.
7. The hub operator MUST provision per-spoke credential Secrets for each registered standalone adapter type. Naming pattern: `spoke-{adapter-type}-credential-{spoke-name}` (e.g., `spoke-alert-credential-{spoke-name}` for the alerts-adapter). Each Secret contains the credentials and endpoint information the adapter needs to access the spoke's event source, scoped to the minimum required access. The hub operator sets a label `hub.openshift.io/{adapter-type}-credential-secret` on the SpokeCluster CR with the Secret name as the value. For the alerts-adapter, the Secret contains the spoke's AlertManager Route URL (`alertmanager-url`), a bearer token for a spoke-side SA with `monitoring-alertmanager-view` access (`token`), and optionally the spoke's ingress CA (`ca-bundle`).
7a. The hub operator MUST provision a spoke-side SA for each adapter type with minimum RBAC. For the alerts-adapter: create SA `lightspeed-alert-adapter` in `openshift-lightspeed-managed`, create a `monitoring-alertmanager-view` RoleBinding in `openshift-monitoring` binding the SA. Create a long-lived token Secret for the SA on the spoke, read the token, and store it in the hub-side adapter credential Secret.
7b. The hub operator MUST discover the spoke's AlertManager Route URL from the `alertmanager-main` Route in the `openshift-monitoring` namespace on the spoke, and store it in the adapter credential Secret.
8. Standalone adapters list SpokeCluster CRs at startup and discover spokes via adapter-type-specific labels. The hub operator restarts the adapter when the spoke target set changes — no per-spoke adapter Deployments. Each adapter type reads its own label to locate its credential Secret. See adapter-specific specs (e.g., `alerts-adapter-multicluster.md` in parent spec) for details.
9. Provisioning status MUST be tracked in SpokeCluster status conditions: `Connected` (API server reachable), `Provisioned` (spoke-side resources created), `AdaptersReady` (adapter credential Secrets provisioned and adapter spoke-side SA + RBAC created). `Connected` and `Provisioned` are independent — a spoke can be connected but not yet provisioned.
10. Provisioning MUST be idempotent — re-reconciling a SpokeCluster must converge without side effects.

### Standing Kubeconfig Secret

11. During registration, the hub operator MUST create a normalized standing kubeconfig Secret on the hub for each spoke. Naming convention: `spoke-kubeconfig-{SpokeCluster.metadata.name}`.
12. The standing kubeconfig Secret MUST contain a standard kubeconfig file with the spoke API server URL and appropriate credentials. The format is identical regardless of credential source — consumers cannot distinguish between modes.
13. **Secret mode**: the hub operator reads the admin-provided kubeconfig from the referenced Secret and normalizes it into the standing kubeconfig Secret.
14. **MCE mode**: the hub operator reads the spoke API server from the ManagedCluster CR, discovers the MCE cluster-proxy service endpoint and CA, obtains a hub-side SA token authorized to use the proxy, and creates the standing kubeconfig with `proxy-url` set to the MCE cluster-proxy endpoint.
15. The standing kubeconfig Secret MUST have an owner reference to the SpokeCluster CR (auto-GC on deletion).
16. The standing kubeconfig Secret is used by the agentic-operator (to create per-step SAs and get ephemeral tokens on the spoke). Standalone adapters use their own per-spoke credential Secrets (e.g., `spoke-alert-credential-{spoke-name}` for the alerts-adapter).
17. For MCE mode, the hub operator MUST periodically refresh the standing kubeconfig Secret if the hub SA token has a bounded lifetime.

### Standing Kubeconfig Format

**Secret mode:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: spoke-kubeconfig-prod-rosa-east
  namespace: openshift-lightspeed
  ownerReferences:
    - apiVersion: hub.openshift.io/v1alpha1
      kind: SpokeCluster
      name: prod-rosa-east
data:
  kubeconfig: |
    clusters:
    - cluster:
        server: https://api.prod-rosa-east.example.com:6443
        certificate-authority-data: <CA>
      name: spoke
    users:
    - user:
        token: <from admin kubeconfig>
      name: spoke-user
    contexts:
    - context:
        cluster: spoke
        user: spoke-user
      name: spoke
    current-context: spoke
```

**MCE mode (same format, adds proxy-url):**
```yaml
data:
  kubeconfig: |
    clusters:
    - cluster:
        server: https://api.prod-rosa-east.example.com:6443
        proxy-url: https://cluster-proxy.multicluster-engine.svc:443
        certificate-authority-data: <MCE proxy CA>
      name: spoke
    users:
    - user:
        token: <hub SA token authorized for MCE proxy>
      name: spoke-user
    contexts:
    - context:
        cluster: spoke
        user: spoke-user
      name: spoke
    current-context: spoke
```

The `proxy-url` field is handled transparently by Go's HTTP transport. Consumers (agentic-operator, adapters) use `clientcmd.RESTConfigFromKubeConfig()` and get a `rest.Config` that routes through the proxy automatically.

### Steady State

18. [PLANNED: OLS-4152] Spoke connectivity MUST be monitored by a dedicated health handler running independently of the reconciler. The health handler runs on a configurable interval (CLI flag `--health-check-interval`, default 5 minutes) as a `manager.RunnableFunc` registered with the controller-runtime manager. For each SpokeCluster CR, it reads the standing kubeconfig Secret (cached via informer), calls `discovery.ServerVersion()` on the spoke (1 API call), and patches the `Connected` status condition if the connectivity state changed. The status patch uses `client.MergeFrom` to minimize conflict surface with the reconciler. Spokes without a standing kubeconfig Secret are skipped (not yet onboarded or unmanaged). The health handler MUST NOT perform credential loading, standing kubeconfig updates, or spoke-side provisioning.
18a. [PLANNED: OLS-4152] The reconciler MUST be purely event-driven with no `RequeueAfter` on the happy path. It is triggered only by watch events: SpokeCluster spec changes, HubConfig changes, and `Connected` status condition changes from the health handler. The reconciler retains its full logic (credential loading, standing kubeconfig update, connectivity check, provisioning, adapters) regardless of trigger source — the connectivity check is a prerequisite for provisioning.
19. If a spoke becomes unreachable, the hub MUST set `Connected=False` on the SpokeCluster status. Operations on other spokes are not affected.
20. When connectivity is restored, the hub MUST re-validate and set `Connected=True`. [PLANNED: OLS-4152] The health handler sets `Connected=True`, which triggers the reconciler to re-run full provisioning and repair any drift.
20a. [PLANNED: OLS-4152] Connectivity failures in the reconciler MUST set `Connected=False` and return without error — the health handler handles periodic rechecking, so workqueue backoff retries are unnecessary. Credential errors, provisioning failures, and adapter failures MUST return an error to the workqueue for exponential backoff (these are transient and worth retrying).

### Decommission

21. [PLANNED: OLS-4786] SpokeCluster deletion gates new work/polling for only that target, then drains previously admitted workflows and their credential-consuming cleanup. Retain standing/adapter credentials, required access and controllers through all remaining phases. Approval waits are named blockers until completed or explicitly cancelled; no automatic cancellation/deadline. Manual/in-flight submission races require execution-side enforcement, not merely adapter restart.
22. [PLANNED: OLS-4786] After shared drain completion, attempt removal of owned spoke resources (seed/adapter SAs, bindings, token Secrets and managed namespace), then delete generated hub standing/adapter Secrets and finalize registration deletion. Preserve source Secrets and workflow history. Unreachable-spoke remote cleanup is best-effort only after drain, with logs/Warning Events before CR removal; retained remote resources must be reported, not assumed harmless. Hub-side API/permission errors remain retryable and must not be falsely acknowledged.
23. [PLANNED: OLS-4660] Protect registration lifetime and required credentials with finalizers and the reviewed shared gate/completion contract. Terminal phase labels alone do not prove credential-consuming cleanup completed. Identity/freshness-aware evidence must survive restarts and reject stale acknowledgements for recreated/readopted registrations. Other spokes and single-cluster workflows remain unaffected. Durable individual MCE exclusion is separate; eligible clusters can be rediscovered.

### Unmanaging

24. [PLANNED: OLS-4787] HubConfig deletion is fleet decommissioning, not unmanagement: gate discovery/enrollment/new work, drain, then delete all registrations (including staged ones) and generated credentials. Preserve source Secrets/static permissions/history; acknowledge before OLS removes runtime/HubConfig. Re-enable requires fresh intent and registration/discovery. GitOps must stop reapplying deleted resources.
25. [PLANNED: OLS-4781] Registry-mode-change unmanagement instead retains SpokeCluster/source Secret. Gate/drain affected registrations using old-source access, then remove generated resources and report mismatch. Matching spokes continue and a later matching supported mode can reprovision retained registrations. Repeated changes/readoption/discovery must not apply stale cleanup; do not substitute ignore-without-cleanup or require deleting all registrations first.
26. [PLANNED: OLS-4781] Mode-change cleanup reuses the shared drain contract and post-drain remote best-effort/hub-error-retry ordering. Selector narrowing, ManagedCluster removal, credential migration, prerequisite configuration deletion (OLS-4782), general uninstall and durable exclusion remain separate scope.

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — spoke lifecycle MVP |
| OLS-3948 | Decommission warning Events and spec alignment |
| OLS-4152 | Lightweight spoke health check with separate time handler — rules 18, 18a, 20, 20a |
| — | Embedded adapter support: install AgenticRun CRD on spoke, start dedicated watcher |
| — | Spoke-local mode: deploy full agentic stack to spoke during registration |
| — | Standing kubeconfig token rotation for MCE mode |
