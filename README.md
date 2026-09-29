# lightspeed-hub

Multicluster hub operator for OpenShift Lightspeed. Runs on a central hub cluster and manages a fleet of spoke clusters, coordinating agentic operations across the fleet.

The hub operator validates spoke connectivity, provisions credentials for standalone adapters, and monitors spoke health — all driven by two CRDs: `HubConfig` (cluster-wide configuration) and `SpokeCluster` (one per registered spoke).

## Prerequisites

The following must be installed on the hub cluster before deploying lightspeed-hub:

- **lightspeed-operator** — provides `OLSConfig`, the lightspeed-service, and LLM provider configuration
- **lightspeed-agentic-operator** — reconciles `AgenticRun` CRs targeting spoke clusters

## Installation

Deploy the operator via Helm into the `openshift-lightspeed` namespace, then create a `HubConfig` CR to configure how spoke clusters are registered.

```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: secret
```

> **Note:** MCE-based auto-discovery (`clusterRegistryMode: mce`) will be supported in a future release.

## Registering Spoke Clusters

`SpokeCluster` is a cluster-scoped CR. Each instance represents one spoke cluster.

Create a Secret containing the spoke's kubeconfig, then reference it:

```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: spoke-east
spec:
  apiServer: https://api.spoke-east.example.com:6443
  credentialSource:
    secret:
      name: spoke-east-kubeconfig
      namespace: openshift-lightspeed
```

Re-applying a `SpokeCluster` CR is safe — the reconciler is idempotent and converges without side effects.

## Status and Health

The hub continuously monitors each spoke's API server reachability and reflects the result in status conditions. Check a spoke's health with:

```bash
kubectl get spokecluster spoke-east -o jsonpath='{.status.conditions}'
```

Key conditions:

| Condition | Meaning |
|---|---|
| `Connected` | Spoke API server is reachable via the credential broker |
| `AdaptersReady` | Per-spoke adapter credentials have been provisioned |

An unhealthy spoke does not block operations on other spokes.

## Development

```bash
make build      # Build the operator binary
make test       # Run unit tests
make lint       # Run golangci-lint
make generate   # Regenerate DeepCopy implementations
make manifests  # Regenerate CRD YAML and RBAC ClusterRole
make fmt        # Run go fmt
make vet        # Run go vet
```

## License

Apache License 2.0 — see [LICENSE](LICENSE).
