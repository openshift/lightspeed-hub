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

9. A dedicated multicluster alerts-adapter instance (`lightspeed-hub-alerts-adapter`) on the hub lists `SpokeCluster` CRs at startup and polls all spokes' AlertManagers. This is separate from the local single-cluster alerts-adapter deployed by lightspeed-operator — both may coexist on the hub. The hub operator deploys the multicluster adapter and triggers a rollout restart when the spoke target set changes (SpokeCluster label `hub.openshift.io/alert-credential-secret` added, removed, or changed, or a SpokeCluster is deleted). See `alerts-adapter-multicluster.md` in the parent spec for details.
10. Alerts from different spokes create separate AgenticRun CRs on the hub — no cross-spoke deduplication in MVP.
11. [PLANNED] Fleet-wide alert deduplication for identical alerts firing across multiple spokes.

### [PLANNED] Spoke-Local Mode Fleet Coordination

12. [PLANNED] In spoke-local mode, AgenticRun CRs live on the spoke. The hub operator syncs MirrorAgenticRun CRs to the hub for console visibility.
13. [PLANNED] Approval on MirrorAgenticRun CRs is propagated back to the spoke's AgenticRun by the hub operator via remote kube-api.
14. [PLANNED] Spoke sync controller pushes AgenticRun status updates to the hub.

## Protected fleet lifecycle [PLANNED: OLS-4660, OLS-4785–OLS-4787]

15. HubConfig deletion closes discovery/enrollment and new fleet execution; individual deletion closes only the target. Coordinate adapter suppression and manual/controller race enforcement. Existing admitted workflows retain remaining phases and credential-consuming cleanup. Approval waits remain visible blockers without automatic cancellation or abandonment timeout; never implement drain by globally suspending AgenticOLSConfig.
16. S0 with OLS-4659/4661 must finalize the admitted-run boundary, condition writers, identity/freshness and cleanup-completion acknowledgement. Hub owns fleet/drain/decommission progress, OLS owns controller availability/runtime removal, Agentic operator owns run progression/cleanup. Use non-overlapping conflict-safe writes and restart-safe evidence; terminal phase alone is insufficient. Preserve/recreate shutdown runtime without resuming enrollment while terminating.
17. After drain, decommission every registration including staged resources and remove generated credentials. Remote cleanup is best-effort with warnings; hub-side API/permission failures remain retryable. Preserve administrator source Secrets, static installation permissions and workflow history. Emit cleanup details before CR removal; expiring Events/deleted status are not durable reports.
18. Registry-mode changes reuse the contract under OLS-4781 but retain registration/source Secret after generated-resource cleanup. Preserve old-source access through drain and reject stale cleanup after readoption. Missing HubConfig never authorizes new fleet work. Local workflows and unaffected spokes retain normal behavior.
19. OLS-4639 verifies hub-only installed-bundle lifecycle; OLS-4788 verifies real-spoke drain/credential ordering, races, approval blockers, cancellation, restarts and isolation. OLS-4782 prerequisite deletion, durable MCE exclusion, selector/removal policy and general uninstall/recovery are outside this phase.

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — fleet coordination MVP |
| — | Embedded adapter support: hub watches spoke AgenticRun CRs |
| — | Spoke-local mode: MirrorAgenticRun CRs, approval routing |
| — | Fleet-wide alert deduplication |
