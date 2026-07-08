# System Overview

The Lightspeed Hub is a Kubernetes operator that runs on a central hub cluster and manages a fleet of spoke clusters for OpenShift Lightspeed. It automates spoke onboarding, coordinates fleet-wide agentic operations, aggregates proposals and alerts across the fleet, and monitors spoke health. The design goal is "install hub, add spokes, done" — minimizing per-cluster setup friction that has historically blocked OLS adoption at fleet scale.

## Behavioral Rules

### System Role

1. The hub operator runs on a single designated hub cluster.
2. Spoke clusters are represented by `SpokeCluster` custom resources on the hub.
3. The hub coordinates fleet-wide operations but does not replace spoke-local agentic operators — each spoke runs its own lightspeed-agentic-operator for local proposal execution.

### Spoke Onboarding

4. When a `SpokeCluster` CR is created, the hub MUST automatically provision the spoke: deploy the alerts adapter, configure credentials, and begin health monitoring.
5. Spoke onboarding MUST be idempotent — re-applying a SpokeCluster CR must converge to the desired state without side effects.
6. The hub MUST validate spoke connectivity before marking a spoke as ready.

### Spoke Health Monitoring

7. The hub MUST continuously monitor spoke health (API server reachability, agent operator status, adapter status).
8. Spoke health status MUST be reflected in the `SpokeCluster` CR status conditions.
9. A spoke transitioning to unhealthy MUST NOT block operations on other spokes.

### Fleet-Wide Proposal Coordination

10. The hub MUST aggregate proposals from all spoke clusters into a unified fleet view.
11. Fleet-wide approval policies defined on the hub apply across all spokes.
12. The hub MUST support hub-initiated proposals that target specific spokes or groups of spokes.

## Configuration Surface

| Field/Flag | Type | Default | Description |
|---|---|---|---|
| Defined by the SpokeCluster CRD and hub operator config — to be specified as the CRD is designed. ||||

## Planned Changes

| Ticket | Summary |
|---|---|
| — | Initial implementation — all rules above are planned |
