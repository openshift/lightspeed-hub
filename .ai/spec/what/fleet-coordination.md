# Fleet Coordination

How the hub coordinates agentic operations across multiple spoke clusters.

## Behavioral Rules

### AgenticRun Creation (Hub-Managed Mode)

1. Standalone adapters (e.g., alerts-adapter) run on the hub and create AgenticRun CRs on the hub with `spec.targetCluster` set to the SpokeCluster name.
2. The agentic-operator on the hub reconciles these AgenticRuns. It resolves the SpokeCluster, obtains spoke kubeconfig via the credential broker, creates ephemeral SA on spoke, and starts sandbox pods on the hub.
3. [PLANNED] Embedded adapters (CVO, ACS, CMO) create AgenticRun CRs locally on the spoke. The hub's dedicated spoke watcher detects them and the agentic-operator reconciles them from the hub.

### Fleet Visibility

4. The hub console displays AgenticRuns from all spokes in a unified fleet view.
5. Each AgenticRun is traceable to its originating spoke via `spec.targetCluster` (standalone adapters) or the spoke watcher source (embedded adapters).
6. Fleet visibility MUST tolerate individual spoke failures — an unreachable spoke must not prevent viewing AgenticRuns from other spokes.

### Approval

7. All AgenticRun approvals happen in the hub console. This is the single approval surface regardless of which spoke the run targets.
8. The existing ApprovalPolicy on the hub applies to all AgenticRuns, including those targeting spokes.

### Alert Aggregation

9. Each spoke has its own alerts-adapter pod running on the hub, polling the spoke's AlertManager.
10. Alerts from different spokes create separate AgenticRun CRs on the hub — no cross-spoke deduplication in MVP.
11. [PLANNED] Fleet-wide alert deduplication for identical alerts firing across multiple spokes.

### [PLANNED] Spoke-Local Mode Fleet Coordination

12. [PLANNED] In spoke-local mode, AgenticRun CRs live on the spoke. The hub operator syncs MirrorAgenticRun CRs to the hub for console visibility.
13. [PLANNED] Approval on MirrorAgenticRun CRs is propagated back to the spoke's AgenticRun by the hub operator via remote kube-api.
14. [PLANNED] Spoke sync controller pushes AgenticRun status updates to the hub.

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — fleet coordination MVP |
| — | Embedded adapter support: hub watches spoke AgenticRun CRs |
| — | Spoke-local mode: MirrorAgenticRun CRs, approval routing |
| — | Fleet-wide alert deduplication |
