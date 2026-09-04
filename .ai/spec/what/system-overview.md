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

6. **SpokeCluster Controller** — reconciles SpokeCluster CRs. Validates spoke connectivity, deploys standalone adapter pods on hub, manages credential lifecycle, updates status conditions.
7. **Credential Broker** — pluggable interface returning a `rest.Config` for a given spoke. Implementations: `SecretCredentialSource` (stored kubeconfig), `MCECredentialSource` (MCE cluster-proxy). [PLANNED] `BackplaneCredentialSource`.
8. **Adapter Orchestrator** — manages standalone adapter Deployments on the hub. One adapter pod per spoke per adapter type, configured with spoke kubeconfig for remote event source access.

### Spoke Onboarding

9. When a SpokeCluster CR is created, the hub MUST validate spoke connectivity via the credential broker before marking the spoke as ready.
10. Spoke onboarding MUST be idempotent — re-applying a SpokeCluster CR must converge without side effects.
11. The hub MUST deploy standalone adapter pods for the spoke on the hub.

### Spoke Health Monitoring

12. The hub MUST continuously monitor spoke health (API server reachability via the credential broker).
13. Spoke health status MUST be reflected in SpokeCluster status conditions (`Connected`, `AdaptersReady`).
14. A spoke transitioning to unhealthy MUST NOT block operations on other spokes.

### Dependencies

15. The agentic-operator MUST be installed on the hub cluster — it reconciles AgenticRuns with `spec.targetCluster` set.
16. The lightspeed-operator MUST be installed on the hub cluster — it provides OLSConfig, lightspeed-service, and LLM provider configuration.
17. For MCE credential source: MCE MUST be installed on the hub cluster.

### Deployment

18. The hub operator is deployed via Helm chart.
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
| `mce.selector.matchLabels` | When mode=mce | Label selector filtering which ManagedCluster CRs become spokes. Only ManagedClusters matching these labels are auto-discovered. When omitted, all ManagedClusters are included. |

### Validation

20. CRD validation MUST require `metadata.name` equals `cluster` (singleton pattern, same as ApprovalPolicy and AgenticOLSConfig).
21. `clusterRegistryMode` MUST be one of `secret` or `mce`.

### HubConfig Lifecycle

22. When no HubConfig exists, the hub operator MUST ignore all SpokeCluster CRs. No provisioning, no connectivity checks, no standing kubeconfigs. SpokeCluster CRs MAY exist but are not managed.
23. When HubConfig is present, a SpokeCluster is managed only if its `credentialSource` type matches `clusterRegistryMode`. Mismatched SpokeCluster CRs MUST be ignored with a status condition indicating the mismatch (e.g. `Ready=False`, reason `CredentialSourceMismatch`).
24. When HubConfig is deleted, the hub operator MUST stop managing all spokes and clean up resources it created for them (standing kubeconfig Secrets, spoke-side namespace/SA/ClusterRoleBindings). The operator MUST NOT delete SpokeCluster CRs — removing CRs is the user's (or GitOps's) responsibility.
25. When HubConfig is created or `clusterRegistryMode` changes, the hub operator MUST re-evaluate all SpokeCluster CRs. Spokes matching the new mode are adopted; non-matching spokes are unmanaged with their associated resources cleaned up and a status condition set.
26. A HubConfig create, update, or delete MUST trigger reconciliation of all SpokeCluster CRs.

### MCE Auto-Discovery

27. When `clusterRegistryMode: mce`, the hub operator MUST watch MCE `ManagedCluster` CRs and automatically create/delete `SpokeCluster` CRs for matching clusters.
28. The `mce.selector.matchLabels` field filters which ManagedClusters are included. When omitted, all ManagedClusters are included.
29. Auto-created SpokeCluster CRs MUST have `spec.apiServer` and `spec.credentialSource.mce.managedClusterName` populated from the ManagedCluster CR.
30. When a ManagedCluster is deleted or its labels no longer match the selector, the corresponding SpokeCluster CR MUST be deleted (triggering standard decommission cleanup).
31. When `clusterRegistryMode: mce`, manually created SpokeCluster CRs MUST be rejected by a validating webhook — MCE is the single source of truth.
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
| `AdaptersReady` | All standalone adapter pods are running for this spoke |

### Planned Status Conditions

| Condition | Meaning |
|---|---|
| `CRDInstalled` | AgenticRun CRD installed on spoke for embedded adapters |
| `WatcherActive` | Dedicated watcher running for spoke AgenticRun CRs |
| `AgenticStackReady` | Spoke-local mode: full agentic stack deployed to spoke |

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
