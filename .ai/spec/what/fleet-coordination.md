# Fleet Coordination

Covers how the hub coordinates agentic operations across multiple spoke clusters.

## Behavioral Rules

### Proposal Aggregation

1. The hub MUST collect proposals from all ready spoke clusters and present them in a unified fleet view.
2. Each aggregated proposal MUST be traceable to its originating spoke cluster.
3. Proposal aggregation MUST tolerate individual spoke failures — an unreachable spoke must not prevent aggregation from other spokes.

### Fleet-Wide Policies

4. Approval policies defined on the hub MUST apply across all registered spokes.
5. Spoke-local policies (if any) are additive — a proposal must satisfy both hub-level and spoke-level policies.
6. Policy updates on the hub MUST propagate to spokes within a bounded time window.

### Hub-Initiated Proposals

7. The hub MUST support creating proposals that target a specific spoke or a set of spokes.
8. Hub-initiated proposals MUST go through the same approval flow as spoke-originated proposals.
9. The hub MUST track execution status of hub-initiated proposals across their target spokes.

### Alert Aggregation

10. Alerts from spoke adapters MUST flow to the hub for fleet-wide visibility.
11. The hub MAY deduplicate alerts that fire identically across multiple spokes (e.g., identical CVE alerts).
12. Alert routing rules on the hub determine whether an alert triggers a spoke-local proposal, a hub-level review, or both.

## Planned Changes

| Ticket | Summary |
|---|---|
| — | All rules are planned — initial design |
