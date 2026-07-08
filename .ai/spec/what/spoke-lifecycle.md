# Spoke Lifecycle

Covers the full lifecycle of a spoke cluster from registration through decommission.

## Behavioral Rules

### Registration

1. A spoke is registered by creating a `SpokeCluster` CR on the hub cluster.
2. The CR MUST include the spoke's API server endpoint and authentication credentials (kubeconfig reference or service account token).
3. The hub MUST validate that the spoke is reachable and the credentials are valid before proceeding with onboarding.

### Provisioning

4. After successful registration, the hub MUST deploy the lightspeed-agentic-alerts-adapter to the spoke.
5. The hub MUST configure the spoke's adapter with the hub endpoint so alerts flow back to the hub.
6. The hub MUST set up the necessary RBAC on the spoke for the adapter to function.
7. Provisioning status MUST be tracked in the `SpokeCluster` status (e.g., `Provisioning`, `Ready`, `Degraded`, `Unreachable`).

### Steady State

8. The hub polls or watches each spoke for health and proposal status.
9. The hub aggregates alerts and proposals from all ready spokes.
10. Configuration updates pushed from the hub (e.g., updated approval policies) MUST propagate to all ready spokes.

### Decommission

11. Deleting a `SpokeCluster` CR MUST trigger cleanup of hub-managed resources on the spoke.
12. Cleanup MUST be best-effort — if the spoke is unreachable, the CR deletion MUST still succeed (with a warning condition) rather than blocking indefinitely.
13. Finalizers MUST be used to ensure cleanup runs before CR removal.

## Planned Changes

| Ticket | Summary |
|---|---|
| — | All rules are planned — initial design |
