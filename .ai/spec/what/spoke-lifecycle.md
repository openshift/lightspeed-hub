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

### Provisioning

5. After successful connectivity validation, the hub MUST deploy standalone adapter pods on the hub for the spoke (e.g., alerts-adapter configured to poll spoke's AlertManager via remote kube-api).
6. Adapter pods run on the hub, not on the spoke. They use the spoke's credential (via credential broker) for remote API access.
7. Provisioning status MUST be tracked in SpokeCluster status conditions: `Connected`, `AdaptersReady`.
8. Provisioning MUST be idempotent — re-reconciling a SpokeCluster must converge without side effects.

### Credential Broker

9. The credential broker reads `SpokeCluster.spec.credentialSource` and returns a `rest.Config` for the spoke.
10. `SecretCredentialSource`: reads kubeconfig from the referenced K8s Secret.
11. `MCECredentialSource`: uses MCE cluster-proxy for proxied access to the spoke.
12. [PLANNED] `BackplaneCredentialSource`: calls Red Hat backplane API for short-lived tokens.
13. The credential broker is used by: the SpokeCluster controller, the agentic-operator (for ephemeral SA creation), and standalone adapter pods.

### Steady State

14. The hub periodically validates spoke connectivity by checking kube-api reachability via the credential broker.
15. If a spoke becomes unreachable, the hub MUST set `Connected=False` on the SpokeCluster status. Operations on other spokes are not affected.
16. When connectivity is restored, the hub MUST re-validate and set `Connected=True`.

### Decommission

17. Deleting a SpokeCluster CR MUST trigger cleanup:
    - Delete standalone adapter pods on the hub for this spoke.
    - Delete credential Secrets owned by the SpokeCluster (if any were auto-generated).
    - [PLANNED] Delete AgenticRun CRD and related resources on the spoke (for embedded adapter support).
18. Cleanup MUST be best-effort — if the spoke is unreachable, the hub-side cleanup MUST still proceed and the CR deletion MUST succeed (with a warning condition) rather than blocking indefinitely.
19. Finalizers MUST be used to ensure cleanup runs before CR removal.

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — spoke lifecycle MVP |
| — | Embedded adapter support: install AgenticRun CRD on spoke, start dedicated watcher |
| — | Spoke-local mode: deploy full agentic stack to spoke during registration |
