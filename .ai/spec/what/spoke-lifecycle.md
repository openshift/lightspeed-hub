# Spoke Lifecycle

Full lifecycle of a spoke cluster from registration through decommission.

## Behavioral Rules

### Registration

1. A spoke is registered by a `SpokeCluster` CR appearing on the hub cluster. The CR source depends on `HubConfig.spec.clusterRegistryMode`:
   - `secret`: admin creates the SpokeCluster CR manually with `spec.apiServer` and `spec.credentialSource.secret`.
   - `mce`: hub operator auto-creates SpokeCluster CRs by watching MCE `ManagedCluster` CRs matching `HubConfig.spec.mce.selector.matchLabels`. The admin labels ManagedClusters to opt in.
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
7. The hub operator MUST provision a per-spoke AlertManager kubeconfig Secret (`spoke-alert-kubeconfig-{spoke-name}`) on the hub and set the `hub.openshift.io/alert-kubeconfig-secret` label on the SpokeCluster CR with the Secret name. This Secret is scoped to AlertManager read-only access on the spoke, separate from the standing kubeconfig used by the agentic-operator.
8. A single alerts-adapter instance on the hub watches SpokeCluster CRs and discovers spokes dynamically — no per-spoke adapter Deployments. See `alerts-adapter-multicluster.md` in the parent spec for details.
9. Provisioning status MUST be tracked in SpokeCluster status conditions: `Connected` (API server reachable), `Provisioned` (spoke-side resources created), `AdaptersReady` (adapter credential Secrets provisioned). `Connected` and `Provisioned` are independent — a spoke can be connected but not yet provisioned.
10. Provisioning MUST be idempotent — re-reconciling a SpokeCluster must converge without side effects.

### Standing Kubeconfig Secret

11. During registration, the hub operator MUST create a normalized standing kubeconfig Secret on the hub for each spoke. Naming convention: `spoke-kubeconfig-{SpokeCluster.metadata.name}`.
12. The standing kubeconfig Secret MUST contain a standard kubeconfig file with the spoke API server URL and appropriate credentials. The format is identical regardless of credential source — consumers cannot distinguish between modes.
13. **Secret mode**: the hub operator reads the admin-provided kubeconfig from the referenced Secret and normalizes it into the standing kubeconfig Secret.
14. **MCE mode**: the hub operator reads the spoke API server from the ManagedCluster CR, discovers the MCE cluster-proxy service endpoint and CA, obtains a hub-side SA token authorized to use the proxy, and creates the standing kubeconfig with `proxy-url` set to the MCE cluster-proxy endpoint.
15. The standing kubeconfig Secret MUST have an owner reference to the SpokeCluster CR (auto-GC on deletion).
16. The standing kubeconfig Secret is used by the agentic-operator (to create per-step SAs and get ephemeral tokens on the spoke). Standalone adapters use their own per-spoke credential Secrets (e.g., `spoke-alert-kubeconfig-{spoke-name}` for the alerts-adapter).
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

18. The hub periodically validates spoke connectivity by checking kube-api reachability via the standing kubeconfig. Connectivity is re-checked on each reconcile (periodic requeue for healthy spokes, workqueue backoff for failures).
19. If a spoke becomes unreachable, the hub MUST set `Connected=False` on the SpokeCluster status. Operations on other spokes are not affected.
20. When connectivity is restored, the hub MUST re-validate and set `Connected=True`.
20a. Failed reconciles (credential errors, connectivity failures, provisioning failures) MUST return an error to the workqueue for exponential backoff. Periodic requeue is only for healthy spokes.

### Decommission

21. Deleting a SpokeCluster CR MUST trigger cleanup:
    - Delete the AlertManager kubeconfig Secret (`spoke-alert-kubeconfig-{spoke-name}`) on the hub (auto-GC via owner reference).
    - Delete the standing kubeconfig Secret on the hub (auto-GC via owner reference).
    - Delete spoke-side resources (`lightspeed-agent` SA, ClusterRoleBindings, `openshift-lightspeed-managed` namespace) via remote kube-api using the standing kubeconfig.
    - The alerts-adapter detects the SpokeCluster deletion via its watch and stops polling that spoke automatically — no explicit adapter cleanup needed.
    - [PLANNED] Delete AgenticRun CRD and related resources on the spoke (for embedded adapter support).
22. Cleanup MUST be best-effort — if the spoke is unreachable, hub-side cleanup MUST still proceed and the CR deletion MUST succeed (with a warning Event on the SpokeCluster CR) rather than blocking indefinitely. Spoke-side resources will remain but are harmless (read-only SA, no secrets).
23. Finalizers MUST be used to ensure cleanup runs before CR removal.

### Unmanaging

24. When HubConfig is deleted, the hub operator MUST clean up resources it created for each spoke (standing kubeconfig Secret, spoke-side namespace/SA/ClusterRoleBindings) but MUST NOT delete the SpokeCluster CRs themselves. Removing CRs is the user's or GitOps's responsibility.
25. When `clusterRegistryMode` changes, spokes whose credential source no longer matches the mode MUST be unmanaged: associated resources cleaned up, status condition set to indicate the mismatch.
26. Unmanaging uses the same best-effort cleanup as decommission — spoke-side cleanup errors do not block the operation.

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — spoke lifecycle MVP |
| OLS-3948 | Decommission warning Events and spec alignment |
| — | Embedded adapter support: install AgenticRun CRD on spoke, start dedicated watcher |
| — | Spoke-local mode: deploy full agentic stack to spoke during registration |
| — | Standing kubeconfig token rotation for MCE mode |
