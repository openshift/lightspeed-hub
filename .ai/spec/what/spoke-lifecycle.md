# Spoke Lifecycle

Full lifecycle of a spoke cluster from registration through decommission.

## Behavioral Rules

### Registration

1. A spoke is registered by a `SpokeCluster` CR appearing on the hub cluster. The CR source depends on `HubConfig.spec.clusterRegistryMode`:
   - `secret`: admin creates the SpokeCluster CR manually with `spec.apiServer` and `spec.credentialSource.secret`.
   - `mce`: hub operator auto-creates SpokeCluster CRs by watching MCE `ManagedCluster` CRs matching `HubConfig.spec.mce.selector.matchLabels`. The admin labels ManagedClusters to opt in.
2. The CR MUST include `spec.apiServer` and `spec.credentialSource` matching the HubConfig mode.
3. The hub MUST validate that the spoke is reachable and the credentials are valid before marking the spoke as `Connected=True`.
4. MVP uses direct kube-api connectivity. The admin is responsible for ensuring network path between hub and spoke.

### Spoke Provisioning

5. After successful connectivity validation, the hub operator MUST provision the spoke with the following resources via remote kube-api:
   - Create `openshift-lightspeed` namespace on the spoke (if it does not exist).
   - Create `lightspeed-agent` ServiceAccount in `openshift-lightspeed` on the spoke.
   - Create `cluster-reader` ClusterRoleBinding binding `lightspeed-agent` to the `cluster-reader` ClusterRole.
   - Create `cluster-monitoring-view` ClusterRoleBinding binding `lightspeed-agent` to the `cluster-monitoring-view` ClusterRole.
6. These spoke-side resources establish the reader RBAC pattern that the agentic-operator's `addReaderSubject` uses — per-step SAs are added to these same ClusterRoleBindings to inherit cluster-wide read access. This is identical to how `lightspeed-agent` is used in single-cluster mode.
7. The hub operator MUST deploy standalone adapter pods on the hub for the spoke (e.g., alerts-adapter configured to poll spoke's AlertManager via remote kube-api).
8. Adapter pods run on the hub, not on the spoke. They use the standing kubeconfig for remote API access.
9. Provisioning status MUST be tracked in SpokeCluster status conditions: `Connected`, `AdaptersReady`.
10. Provisioning MUST be idempotent — re-reconciling a SpokeCluster must converge without side effects.

### Standing Kubeconfig Secret

11. During registration, the hub operator MUST create a normalized standing kubeconfig Secret on the hub for each spoke. Naming convention: `spoke-kubeconfig-{SpokeCluster.metadata.name}`.
12. The standing kubeconfig Secret MUST contain a standard kubeconfig file with the spoke API server URL and appropriate credentials. The format is identical regardless of credential source — consumers cannot distinguish between modes.
13. **Secret mode**: the hub operator reads the admin-provided kubeconfig from the referenced Secret and normalizes it into the standing kubeconfig Secret.
14. **MCE mode**: the hub operator reads the spoke API server from the ManagedCluster CR, discovers the MCE cluster-proxy service endpoint and CA, obtains a hub-side SA token authorized to use the proxy, and creates the standing kubeconfig with `proxy-url` set to the MCE cluster-proxy endpoint.
15. The standing kubeconfig Secret MUST have an owner reference to the SpokeCluster CR (auto-GC on deletion).
16. The standing kubeconfig Secret is used by: the agentic-operator (to create per-step SAs and get ephemeral tokens on the spoke) and standalone adapter pods (to poll spoke event sources).
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

18. The hub periodically validates spoke connectivity by checking kube-api reachability via the standing kubeconfig.
19. If a spoke becomes unreachable, the hub MUST set `Connected=False` on the SpokeCluster status. Operations on other spokes are not affected.
20. When connectivity is restored, the hub MUST re-validate and set `Connected=True`.

### Decommission

21. Deleting a SpokeCluster CR MUST trigger cleanup:
    - Delete standalone adapter pods on the hub for this spoke.
    - Delete the standing kubeconfig Secret on the hub (auto-GC via owner reference).
    - Delete spoke-side resources (`lightspeed-agent` SA, ClusterRoleBindings, `openshift-lightspeed` namespace) via remote kube-api using the standing kubeconfig.
    - [PLANNED] Delete AgenticRun CRD and related resources on the spoke (for embedded adapter support).
22. Cleanup MUST be best-effort — if the spoke is unreachable, hub-side cleanup MUST still proceed and the CR deletion MUST succeed (with a warning condition) rather than blocking indefinitely. Spoke-side resources will remain but are harmless (read-only SA, no secrets).
23. Finalizers MUST be used to ensure cleanup runs before CR removal.

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — spoke lifecycle MVP |
| — | Embedded adapter support: install AgenticRun CRD on spoke, start dedicated watcher |
| — | Spoke-local mode: deploy full agentic stack to spoke during registration |
| — | Standing kubeconfig token rotation for MCE mode |
