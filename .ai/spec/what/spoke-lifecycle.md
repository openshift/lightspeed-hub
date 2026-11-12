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
   - Resolve the deduplicated union of HubConfig `spec.agentRBAC.defaultClusterRoles` and SpokeCluster `spec.agentRBAC.additionalClusterRoles`. Omitted fleet defaults select `cluster-reader` and `cluster-monitoring-view`; explicit lists replace defaults, including an empty list. Spokes cannot subtract defaults.
   - Read all referenced spoke ClusterRoles and validate current effective rules: only get/list/watch, no core Secret/RBAC access, and no wildcard bypasses. Parent ADR 0048 exempts exactly `cluster-reader` and `cluster-monitoring-view` from permission-content validation, regardless of configuration origin or administrator changes; every other role, including custom fleet defaults, remains validated. Exempt roles still require existence/accessibility checks. Missing roles, unsafe non-exempt roles, and API failures set `Provisioned=False`; partial updates are not success. Never manage role definitions or widen standing credentials.
   - Reconcile deterministic baseline ClusterRoleBindings to the seed SA. Preserve default names `lightspeed-hub:cluster-reader` and `lightspeed-hub:cluster-monitoring-view`; custom names contain a role-name hash. Ownership labels identify the baseline component and registration UID. Repair owned drift and remove obsolete owned bindings; unowned or differently owned collisions are errors, never silently adopted. No legacy migration is required.
6. The seed SA establishes remote reader discovery, not sandbox identity. The parent `multicluster-agent-rbac.md` contract requires the compatible agentic-operator implementation to discover exact-seed bindings and create isolated per-step bindings, never mutate source subjects. [PLANNED: OLS-4685] Remote setup checks existence/accessibility and applies strict reader eligibility except for the exact ClusterRole names `cluster-reader` and `cluster-monitoring-view`; unsafe non-exempt sources are skipped/logged, while missing-role/API errors fail setup. Local validation is unchanged. Administrator modifications to the two exempt roles can grant writes or sensitive access to every remote phase independently of execution approval. That agentic change is separately delivered. Hub configuration updates reconcile through existing watches, and MCE discovery preserves administrator additions.
7. The hub operator MUST provision per-spoke credential Secrets for each registered standalone adapter type. Naming pattern: `spoke-{adapter-type}-credential-{spoke-name}` (e.g., `spoke-alert-credential-{spoke-name}` for the alerts-adapter). Each Secret contains the credentials and endpoint information the adapter needs to access the spoke's event source, scoped to the minimum required access. The hub operator sets a label `hub.openshift.io/{adapter-type}-credential-secret` on the SpokeCluster CR with the Secret name as the value. For the alerts-adapter, the Secret contains the spoke's AlertManager Route URL (`alertmanager-url`), a bearer token for a spoke-side SA with `monitoring-alertmanager-view` access (`token`), and optionally the spoke's ingress CA (`ca-bundle`).
7a. The hub operator MUST provision a spoke-side SA for each adapter type with minimum RBAC. For the alerts-adapter: create SA `lightspeed-alert-adapter` in `openshift-lightspeed-managed`, create a `monitoring-alertmanager-view` RoleBinding in `openshift-monitoring` binding the SA. Create a long-lived token Secret for the SA on the spoke, read the token, and store it in the hub-side adapter credential Secret.
7b. The hub operator MUST discover the spoke's AlertManager Route URL from the `alertmanager-main` Route in the `openshift-monitoring` namespace on the spoke, and store it in the adapter credential Secret.
8. Standalone adapters list SpokeCluster CRs at startup and discover spokes via adapter-type-specific labels. The hub operator restarts the adapter when the spoke target set changes — no per-spoke adapter Deployments. Each adapter type reads its own label to locate its credential Secret. See adapter-specific specs (e.g., `alerts-adapter-multicluster.md` in parent spec) for details.
9. Provisioning status MUST be tracked in SpokeCluster status conditions: `Connected` (API server reachable), `Provisioned` (spoke-side resources created), `AdaptersReady` (adapter credential Secrets provisioned and adapter spoke-side SA + RBAC created). `Connected` and `Provisioned` are independent — a spoke can be connected but not yet provisioned.
10. Provisioning MUST be idempotent — re-reconciling a SpokeCluster must converge without side effects. Baseline cleanup during decommission or HubConfig cleanup discovers all bindings owned by the registration independently of current role configuration, preserving administrator bindings and ClusterRole definitions. Configuration updates are not atomic revocation of existing per-step grants; role checks are preflight, not continuous revocation; role edits can change existing grants. Administrators own permission review for the two name-exempt roles; non-exempt roles remain validated.

### Standing Kubeconfig Secret

11. During registration, the hub operator MUST create a normalized standing kubeconfig Secret on the hub for each spoke. Naming convention: `spoke-kubeconfig-{SpokeCluster.metadata.name}`.
12. The standing kubeconfig Secret MUST contain a standard kubeconfig file with the spoke API server URL and appropriate credentials. The format is identical regardless of credential source — consumers cannot distinguish between modes.
13. **Secret mode**: the hub operator reads the admin-provided kubeconfig from the referenced Secret and normalizes it into the standing kubeconfig Secret.
14. **MCE mode**: the hub operator reads a ManagedServiceAccount token from the spoke's hub namespace, discovers the MCE cluster-proxy endpoint and CA, and creates the standing kubeconfig with `server` set to the reverse-proxy endpoint (path-based, not `proxy-url`). Each spoke has its own ManagedServiceAccount and token, parallel to secret mode's per-spoke Secrets.
15. The standing kubeconfig Secret MUST have an owner reference to the SpokeCluster CR (auto-GC on deletion).
16. The standing kubeconfig Secret is used by the agentic-operator (to create per-step SAs and get ephemeral tokens on the spoke). Standalone adapters use their own per-spoke credential Secrets (e.g., `spoke-alert-credential-{spoke-name}` for the alerts-adapter).
17. For MCE mode, the hub operator MUST refresh the standing kubeconfig token on each reconciliation. MCE handles token rotation; spoke-side RBAC for the ManagedServiceAccount SA is a deployment concern.

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

**MCE mode (reverse-proxy endpoint as server, no proxy-url):**
```yaml
data:
  kubeconfig: |
    clusters:
    - cluster:
        server: https://cluster-proxy-addon-user.<mce-namespace>.svc:<port>/<managed-cluster-name>
        certificate-authority-data: <MCE service CA>
      name: spoke
    users:
    - user:
        token: <ManagedServiceAccount token>
      name: spoke-user
    contexts:
    - context:
        cluster: spoke
        user: spoke-user
      name: spoke
    current-context: spoke
```

The server URL includes the managed-cluster-name path prefix. Consumers use `clientcmd.RESTConfigFromKubeConfig()` and get a `rest.Config` that routes through the proxy automatically.

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

24. [PLANNED: OLS-4787] HubConfig deletion is fleet decommissioning, not unmanagement: gate discovery/enrollment/new work, drain, then delete all registrations (including staged ones) and generated credentials. Preserve source Secrets/static permissions/history; acknowledge before OLS removes runtime/HubConfig. Re-enable requires fresh intent and registration/discovery. GitOps must stop reapplying deleted resources. For MCE auto-discovered SpokeCluster CRs (labeled `hub.openshift.io/managed-by: mce-auto-discovery`), the discovery controller deletes them along with their ManagedServiceAccount and ManifestWork companions.
25. [PLANNED: OLS-4781] Registry-mode-change unmanagement instead retains SpokeCluster/source Secret. Gate/drain affected registrations using old-source access, then remove generated resources and report mismatch. Matching spokes continue and a later matching supported mode can reprovision retained registrations. Repeated changes/readoption/discovery must not apply stale cleanup; do not substitute ignore-without-cleanup or require deleting all registrations first. In the current MCE implementation, mode-change cleanup also revokes ManagedServiceAccount credentials and deletes ManifestWork companions.
26. [PLANNED: OLS-4781] Mode-change cleanup reuses the shared drain contract and post-drain remote best-effort/hub-error-retry ordering. Selector narrowing, ManagedCluster removal, credential migration, prerequisite configuration deletion (OLS-4782), general uninstall and durable exclusion remain separate scope.

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — spoke lifecycle MVP |
| OLS-3948 | Decommission warning Events and spec alignment |
| OLS-4152 | Lightweight spoke health check with separate time handler — rules 18, 18a, 20, 20a |
| — | Embedded adapter support: install AgenticRun CRD on spoke, start dedicated watcher |
| — | Spoke-local mode: deploy full agentic stack to spoke during registration |
| OLS-3954 | MCE credential source: ManagedServiceAccount token via cluster-proxy, auto-discovery from ManagedCluster CRs, reverse-proxy standing kubeconfig — rules 7, 14, 17 |
| — | Standing kubeconfig token rotation for MCE mode |
