# System Overview

The Lightspeed Hub is a Kubernetes operator that runs on a central hub cluster and manages a fleet of spoke clusters for OpenShift Lightspeed multicluster operations. It coordinates fleet-wide agentic workflows through a single control plane, brokering credentials to spoke clusters and orchestrating standalone adapters on the hub.

## Behavioral Rules

### System Role

1. The hub operator is a single Go binary (controller-runtime) running on a designated hub cluster.
2. Spoke clusters are represented by `SpokeCluster` CRs (cluster-scoped, `hub.openshift.io/v1alpha1`).
3. The hub is both control plane and compute plane (hub-managed mode). Sandboxes run on the hub, targeting spoke clusters via remote kube-api.
4. The hub does NOT reimplement the agentic engine. AgenticRun reconciliation, sandbox lifecycle, and approval enforcement stay in the agentic-operator. The hub adds the "which cluster" dimension.
5. [PLANNED] Spoke-local mode: hub is control plane only, full agentic stack deployed to spoke. See `multicluster-ops.md` in parent spec.

### Controllers

6. **SpokeCluster Controller** — reconciles SpokeCluster CRs. Validates spoke connectivity, deploys standalone adapter pods on hub, manages credential lifecycle, updates status conditions. [PLANNED: OLS-4152] The reconciler becomes purely event-driven (no `RequeueAfter`), triggered by spec changes, HubConfig changes, and `Connected` status changes from the health handler.
6a. [PLANNED: OLS-4152] **Spoke Health Handler** — a `manager.RunnableFunc` that runs independently of the reconciler on a configurable interval (CLI flag `--health-check-interval`, default 5 min). Probes each managed spoke's kube-api via `discovery.ServerVersion()` using the standing kubeconfig and patches the `Connected` status condition on change. Status changes trigger the SpokeCluster reconciler via the watch.
7. **Credential Broker** — pluggable interface returning a `rest.Config` for a given spoke. Implementations: `SecretCredentialSource` (user-provided kubeconfig), `MCECredentialSource` (ManagedServiceAccount token + MCE cluster-proxy). A `DispatchingCredentialSource` routes based on `SpokeCluster.spec.credentialSource`. [PLANNED] `BackplaneCredentialSource`.
8. **Adapter Orchestrator** — provisions per-spoke adapter credential Secrets on the hub and labels SpokeCluster CRs for adapter discovery. For each adapter type, creates a spoke-side SA with minimum RBAC, obtains a token, discovers the spoke's event-source endpoint, and stores everything in a credential Secret (`spoke-{adapter-type}-credential-{spoke-name}`). Adapters watch SpokeCluster CRs to discover spokes dynamically — no per-spoke adapter Deployments. The alerts-adapter is the first implementation — see `alerts-adapter-multicluster.md` in the parent spec.
8a. **Adapter Deployment Manager** — the HubConfig controller creates and manages a dedicated multicluster alerts-adapter Deployment (`lightspeed-hub-alerts-adapter`) on the hub, separate from the local single-cluster alerts-adapter deployed by lightspeed-operator. Resources: SA, RBAC, ConfigMap (`hub-alerts-adapter-config`), Deployment. [PLANNED: OLS-4787] The adapter is active only for an accepted, non-terminating HubConfig; deletion suppresses new polling/work before drain and removes runtime only in the coordinated shutdown order. The adapter image is configured via the `--alerts-adapter-image` CLI flag on the hub operator.
8b. **Hub-managed adapter egress exception**: The multicluster adapter uses the hub Kubernetes API to discover spokes and connects to their AlertManager Routes using credential Secrets provisioned as spokes register; its `ALERTMANAGER_URL` is empty so it does not poll the hub's local AlertManager. [PLANNED: OLS-4171] The hub MUST NOT introduce a hub-managed egress NetworkPolicy for this adapter under the current design. Spoke Route hostnames and potentially ports vary as spokes register; standard NetworkPolicy cannot allow-list destinations by hostname, and a fixed pod-level allow-list would disrupt supported spokes. This is deliberate non-coverage, not proof of HPSTRAT-104 compliance or a guarantee of unrestricted connectivity if cluster-admin policies apply. The local, operator-managed single-cluster adapter has a separate, restricted-egress policy plan; do not apply that plan to this deployment. Tightening hub-adapter egress requires a separately approved variable-destination strategy and tests against supported spokes. See the [parent operand-network-policies contract](https://github.com/openshift/ols/pull/104).

### Spoke Onboarding

9. When a SpokeCluster CR is created, the hub MUST validate spoke connectivity via the credential broker before marking the spoke as ready.
10. Spoke onboarding MUST be idempotent — re-applying a SpokeCluster CR must converge without side effects.
11. The hub MUST provision per-spoke credential Secrets for standalone adapters and label the SpokeCluster CR for adapter discovery.

### Spoke Health Monitoring

12. The hub MUST continuously monitor spoke health (API server reachability via the credential broker). [PLANNED: OLS-4152] Monitoring is performed by a dedicated health handler, not the reconciler's periodic requeue.
13. Spoke health status MUST be reflected in SpokeCluster status conditions (`Connected`, `AdaptersReady`).
14. A spoke transitioning to unhealthy MUST NOT block operations on other spokes.

### Dependencies

15. The agentic-operator MUST be installed on the hub cluster — it reconciles AgenticRuns with `spec.targetCluster` set.
16. The lightspeed-operator MUST be installed on the hub cluster — it provides OLSConfig, lightspeed-service, and LLM provider configuration.
17. For MCE credential source: MCE MUST be installed on the hub cluster.

### Deployment

18. [PLANNED: OLS-4661, OLS-4784] The unified OLS bundle installs hub APIs/static ServiceAccount/RBAC but no hub Deployment. Creating HubConfig `cluster` activates an OLS-operator-managed hub operand; there is no OLSConfig activation field or third CSV Deployment. Static permissions have sole OLM ownership and survive fleet disablement. OCP 5.0+ is a runtime boundary, not a separate bundle; HubConfig does not bypass platform gating or independent unsuspended AgenticOLSConfig workflow opt-in. Unknown platform state is not unsupported or cleanup intent.
19. All hub operator workloads run in the `openshift-lightspeed` namespace.

## CRD: HubConfig

API group: `hub.openshift.io/v1alpha1`. Cluster-scoped singleton.

**When `clusterRegistryMode: secret`:**
```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: secret
```

**When `clusterRegistryMode: mce`:**
```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: mce
  mce:
    selector:
      matchLabels:
        lightspeed-enabled: "true"
```

### Spec Fields

| Field | Required | Description |
|---|---|---|
| `clusterRegistryMode` | Yes | How spoke clusters are registered and credentials managed. `secret`: manual SpokeCluster CRs with stored kubeconfigs. `mce`: auto-discover from MCE ManagedCluster CRs. |
| `mce.selector.matchLabels` | No; applies in mce mode | Label selector filtering which ManagedCluster CRs become spokes. Only ManagedClusters matching these labels are auto-discovered. When omitted, all ManagedClusters are included. |

### Validation

20. CRD validation MUST require `metadata.name` equals `cluster` (singleton pattern, same as ApprovalPolicy and AgenticOLSConfig).
21. `clusterRegistryMode` MUST be one of `secret` or `mce`.

### HubConfig Lifecycle

22. When no HubConfig exists, the hub operator MUST ignore all SpokeCluster CRs. No provisioning, no connectivity checks, no standing kubeconfigs. SpokeCluster CRs MAY exist but are not managed.
23. When HubConfig is present, normal provisioning applies only to matching credential sources. Initially ineligible staged registrations stay inactive and report mismatch; [PLANNED: OLS-4781] previously managed registrations made ineligible by a mode change first drain, then unmanage/clean generated resources under rule 25. Mismatch is not permission to abandon existing consumers or skip cleanup.
24. [PLANNED: OLS-4660, OLS-4787] HubConfig deletion gates new fleet activity, drains admitted workflows and credential-consuming cleanup, then decommissions all registrations including staged ones. Delete registrations/generated credentials, not source Secrets, installation-owned APIs/permissions or workflow history. After fresh completion acknowledgement the OLS operator removes runtime and releases lifecycle protection. No automatic cancellation or deadline may abandon active/approval-waiting work. MCE auto-discovered SpokeCluster CRs (labeled `hub.openshift.io/managed-by: mce-auto-discovery`) are deleted by the discovery controller along with their ManagedServiceAccount and ManifestWork companions.
25. [PLANNED: OLS-4781] Creation/mode changes re-evaluate registrations. On a mode change, gate/drain newly ineligible spokes while retaining old-source access, then unmanage/clean generated resources and report mismatch. Retain SpokeCluster CRs/source Secrets; matching supported mode may reprovision them. Coordinate discovery and reject stale cleanup during repeated changes/readoption; do not require deleting all registrations first.
26. A HubConfig create, update, or delete MUST trigger reconciliation of all SpokeCluster CRs.

### MCE Auto-Discovery [PLANNED: OLS-3954]

MCE examples describe target behavior, not proof of implemented discovery. Selecting MCE without a selector authorizes the entire managed fleet, including future additions; installation alone never authorizes discovery.

27. When `clusterRegistryMode: mce`, the hub operator MUST continuously watch MCE ManagedClusters and create registrations for eligible clusters, including future matches. The hub self-import (`local-cluster=true`) is never auto-discovered regardless of selector. Removal/selector-change policy is separately scoped under rule 30; discovery must not resume during HubConfig termination or defeat mode-change draining.
28. The `mce.selector.matchLabels` field filters which ManagedClusters are included. When omitted, all ManagedClusters except the hub self-import are included.
29. Auto-created SpokeCluster CRs MUST have `spec.apiServer` and `spec.credentialSource.mce.managedClusterName` populated from the ManagedCluster CR.
30. When a ManagedCluster is deleted or its labels no longer match the selector, the corresponding SpokeCluster CR MUST be deleted (triggering standard decommission cleanup). ManagedCluster removal and selector-narrowing lifecycle policy require separate follow-up for durable exclusion; deleting an individual auto-discovered registration is not permanent exclusion while it remains discovery-eligible.
31. When `clusterRegistryMode: mce`, manually created SpokeCluster CRs with a mismatched credential source are rejected by the reconciler (`CredentialSourceMismatch` status condition) — MCE auto-discovery is the source of truth for MCE-mode spokes.
32. Auto-created SpokeCluster CRs MUST have an owner label (`hub.openshift.io/managed-by: mce-auto-discovery`) to distinguish them from manually created ones.

## CRD: SpokeCluster

API group: `hub.openshift.io/v1alpha1`. Cluster-scoped, one per spoke.

**When `clusterRegistryMode: secret` (user creates manually):**
```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: prod-rosa-east
spec:
  apiServer: "https://api.prod-rosa-east.example.com:6443"
  credentialSource:
    secret:
      name: prod-rosa-east-credentials
      namespace: openshift-lightspeed
status:
  conditions:
    - type: Connected
      status: "True"
    - type: AdaptersReady
      status: "True"
```

**When `clusterRegistryMode: mce` (auto-created by hub operator):**
```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: prod-rosa-east
  labels:
    hub.openshift.io/managed-by: mce-auto-discovery
spec:
  apiServer: "https://api.prod-rosa-east.example.com:6443"
  credentialSource:
    mce:
      managedClusterName: "prod-rosa-east"
status:
  conditions:
    - type: Connected
      status: "True"
    - type: AdaptersReady
      status: "True"
```

### Spec Fields

| Field | Required | Description |
|---|---|---|
| `apiServer` | Yes | Spoke cluster's kube-api server URL |
| `credentialSource` | Yes | Exactly one of: `secret`, `mce`. Must match HubConfig `clusterRegistryMode`. |
| `credentialSource.secret.name` | When mode=secret | Name of the K8s Secret containing the spoke kubeconfig |
| `credentialSource.secret.namespace` | When mode=secret | Namespace of the credential Secret |
| `credentialSource.mce.managedClusterName` | When mode=mce | MCE ManagedCluster name for cluster-proxy access |

### Status Conditions

| Condition | Meaning |
|---|---|
| `Connected` | Hub can reach spoke kube-api via the credential source |
| `Provisioned` | Spoke-side resources (namespace, SA, ClusterRoleBindings) are created |
| `AdaptersReady` | Per-spoke adapter credential Secrets are provisioned and adapters can reach the spoke's event sources |

### Planned Status Conditions

| Condition | Meaning |
|---|---|
| `CRDInstalled` | AgenticRun CRD installed on spoke for embedded adapters |
| `WatcherActive` | Dedicated watcher running for spoke AgenticRun CRs |
| `AgenticStackReady` | Spoke-local mode: full agentic stack deployed to spoke |

### Planned lifecycle status ownership [PLANNED: OLS-4659, OLS-4660]

The OLS operator owns controller availability; hub owns configuration/fleet/drain/decommission progress; Agentic operator owns workflow/cleanup evidence. Writers must patch non-overlapping conditions conflict-safely. Exact condition identifiers and gate/acknowledgement wire APIs require S0 review. Ready means controller operational and configuration accepted, not all spokes healthy; zero spokes is valid. Report approval/run blockers and incomplete remote cleanup before CR removal. Logs/Warning Events have retention limits, not a durable report API. Re-enable requires a fresh HubConfig and new registration/discovery; GitOps must stop reapplying deleted objects. Prerequisite deletion (OLS-4782) is deferred, outside this phase.

## What the Hub Operator Does NOT Do

- LLM provider configuration (stays in OLSConfig)
- AgenticRun reconciliation (stays in agentic-operator)
- Sandbox management (stays in agentic-operator)
- MCP server definitions (stays in OLSConfig)
- Approval policy enforcement (stays in agentic-operator)

## Planned Changes

| Ticket | Summary |
|---|---|
| OLS-2984 | Initial implementation — hub operator MVP |
| OLS-4152 | Lightweight spoke health check with separate time handler — rules 6, 6a, 12 |
| OLS-4171 | Retain the documented hub-managed multicluster adapter egress exception; no hub-managed adapter NetworkPolicy is introduced — rule 8b |
