# End-to-End Testing

This file covers both the **automated** multicluster e2e suite (T1 kind / T2 OCP) and the legacy manual test.

---

## Automated Multicluster E2E Suite

Six Ginkgo test cases covering the full spoke lifecycle (OLS-3952):
registration, provisioning, unreachable detection, and three decommission variants.

Build tags: `mc_e2e` (T1) and `mc_product_e2e` (T2).

### T1 — kind (self-contained, no cloud credentials)

Prerequisites: `kind`, `podman` or `docker`, `kubectl`, `python3`.

```bash
make mc-e2e
```

`hack/mc-kind-up.sh` creates three kind clusters (hub + 2 spokes), builds and loads the
operator image, deploys it with `--health-check-interval=15s`, runs the suite, then tears
everything down. To use a custom image:

```bash
MC_E2E_IMG=quay.io/youruser/lightspeed-hub-operator:tag make mc-e2e
```

### T2 — real OCP clusters

The operator must already be deployed on the hub cluster. Set the three env vars and run:

```bash
export MC_HUB_KUBECONFIG=/path/to/hub.kubeconfig
export MC_SPOKE_KUBECONFIGS=/path/to/spoke1.kubeconfig,/path/to/spoke2.kubeconfig
export MC_SPOKE_INTERNAL_KUBECONFIGS=/path/to/spoke1.kubeconfig,/path/to/spoke2.kubeconfig

make mc-product-e2e
```

`MC_SPOKE_INTERNAL_KUBECONFIGS` is the set of kubeconfigs the **operator pod** uses to reach
spoke API servers. On OCP this is the same as the external kubeconfig. For kind T1 the
up-script generates container-IP-based kubeconfigs automatically.

For a single-cluster self-referencing test, point all three env vars at the same kubeconfig.

### Test cases

| # | Test | T1 | T2 |
|---|---|---|---|
| 1 | Registration → `Connected=True`, standing kubeconfig, spoke-side namespace/SA/CRBs | ✓ | ✓ |
| 2 | `Provisioned=True` + `AdaptersReady` (T1: `False/AdaptersFailed`; T2: `True` + adapter Deployment available) | ✓ | ✓ |
| 3 | Simulate unreachable → `Connected=False`, other spoke unaffected | ✓ | ✓ |
| 4 | Delete reachable spoke → spoke-side cleanup, credential Secret GC'd, no `SpokeCleanupFailed` event | ✓ | ✓ |
| 5 | Delete reachable spoke (OLS-3948 AC1) → spoke-side namespace gone | ✓ | ✓ |
| 6 | Corrupted kubeconfig → `SpokeCleanupFailed` event in `default` ns, finalizer released (OLS-3948 AC2) | ✓ | ✓ |

> **Note:** `SpokeCleanupFailed` events land in the `default` namespace because `SpokeCluster`
> is cluster-scoped. Check with `kubectl get events -n default --field-selector reason=SpokeCleanupFailed`.

---

## Alert Pipeline E2E Suite (OLS-4478)

Validates the full hub-managed alert pipeline on real OpenShift clusters: a spoke
PrometheusRule fires, the hub multicluster alerts-adapter polls the spoke AlertManager and
creates an AgenticRun on the hub, and the agentic stages (Analysis, Execution, Verification)
fix the fault on the spoke. Design: `.ai/spec/what/multicluster-testing.md`, section
"Alert pipeline E2E".

Build tag: `alert_pipeline_e2e`. Runs only on dedicated, ephemeral, connected OCP test
clusters (it raises a `severity: critical` alert on the spoke).

### Cluster setup script

`hack/alert-pipeline-setup.sh` (`make alert-pipeline-setup`) prepares a fresh OpenShift 5 cluster
as hub and spoke, with ACM as the inventory, and satisfies every prerequisite below. It is
idempotent; re-running converges.

```bash
export KUBECONFIG=/path/to/cluster-admin.kubeconfig   # static token or client certificate
export HUB_IMAGE=<lightspeed-hub image>               # e.g. the Konflux image of the PR
export OPENAI_API_KEY=<key>                           # or SKIP_LLM_SETUP=true
make alert-pipeline-setup
```

Steps: install ACM (hub self-management off, `availabilityConfig: Basic`, and the ACM components the
test does not use turned off: `multicluster-observability`, `search`, `grc`, `insights`,
`submariner-addon`, `volsync`, `console`, `app-lifecycle`; override with `ACM_AVAILABILITY` /
`ACM_DISABLED_COMPONENTS`), import
the hub as ManagedCluster `hub-self`, deploy lightspeed-agentic-operator (manifests from
`AGENTIC_OPERATOR_REF`, default `main`) and the hub, create `HubConfig` in `mce` mode and wait
for `SpokeCluster/hub-self`, configure `LLMProvider`/`Agent/default` (`LLM_MODEL`, default
`gpt-6.1-sol`) and an all-`Automatic` `ApprovalPolicy`, silence any firing critical alerts, then
enable and restart the alerts adapter.

Pre-existing critical alerts are silenced for `SILENCE_DURATION_HOURS` (default 24) through an
`oc port-forward` to AlertManager and listed in the script output; the test's own alert is never
silenced. Required tools: `oc`, `curl`, `git`, `make`, `go`. Image overrides:
`AGENTIC_OPERATOR_IMAGE`, `SANDBOX_IMAGE`, `ALERTS_ADAPTER_IMAGE` (default: Konflux `:main`).

### Konflux CI

`.tekton/integration-tests/pipelines/alert-pipeline-e2e-pipeline.yaml` runs the setup script and the
test for a `lightspeed-hub` snapshot: it provisions an AWS HyperShift OpenShift 5 cluster
(`openshift-version-prefix`, default `5.0`), checks out the snapshot commit, runs
`make alert-pipeline-setup` with the snapshot image and the tenant `openai` Secret, then
`make alert-pipeline-e2e`, and pushes logs and cluster resources to
`quay.io/openshift-lightspeed/ols-operator-artifacts`. The IntegrationTestScenario is maintained
outside this repository.

### Prerequisites

The suite verifies these in `BeforeSuite` and fails with a hint when one is missing:

1. HubConfig exists and `SpokeCluster/$MC_SPOKE_NAME` has `Connected`, `Provisioned` and
   `AdaptersReady` all `True`.
2. The adapter allows the `Critical` receiver and has no pre-run delay. The hub creates
   `hub-alerts-adapter-config` with an empty allowlist and never overwrites it:

   ```bash
   oc -n openshift-lightspeed edit configmap hub-alerts-adapter-config
   # config.yaml:
   #   filtering:
   #     allowedReceivers: [Critical]
   ```

3. The adapter pod started after the spoke became `AdaptersReady` and after the last
   ConfigMap change (config is read only at startup):

   ```bash
   oc -n openshift-lightspeed rollout restart deploy/lightspeed-hub-alerts-adapter
   oc -n openshift-lightspeed rollout status deploy/lightspeed-hub-alerts-adapter
   ```

4. The spoke uses default OpenShift monitoring (`severity=critical` routes to `Critical`).
5. lightspeed-agentic-operator is installed on the hub; `AgenticOLSConfig/cluster`, if
   present, is not suspended.
6. `ApprovalPolicy/cluster` sets every stage to `Automatic`. The suite creates it that way
   when absent and fails if an existing policy has any other mode.
7. Unless `SKIP_LLM_STAGES=true`: `Agent/default` exists with a model and an existing
   `LLMProvider`.

### Run

```bash
export MC_HUB_KUBECONFIG=/path/to/hub.kubeconfig
export MC_SPOKE_KUBECONFIG=/path/to/spoke.kubeconfig
export MC_SPOKE_NAME=<SpokeCluster name for that spoke>

SKIP_LLM_STAGES=true make alert-pipeline-e2e   # Phase 1 only, no LLM needed (~7m)
make alert-pipeline-e2e                        # full pipeline with a real provider (~11m)
```

For a single cluster acting as hub and spoke, point both kubeconfig variables at the same
file:

- **Secret mode:** register a SpokeCluster for it yourself (e.g. `MC_SPOKE_NAME=self`).
- **ACM / MCE mode:** discovery always skips the hub's self-import (`local-cluster`). Disable
  self-management and import the hub again under another name (validated on OCP 5.0.2 with
  ACM 5.0; test clusters only):

  ```bash
  oc -n open-cluster-management patch multiclusterhub multiclusterhub --type merge \
    -p '{"spec":{"disableHubSelfManagement":true}}'      # ACM removes local-cluster
  oc create ns hub-self
  oc apply -f - <<'YAML'
  apiVersion: cluster.open-cluster-management.io/v1
  kind: ManagedCluster
  metadata: {name: hub-self, labels: {cloud: auto-detect, vendor: auto-detect}}
  spec: {hubAcceptsClient: true}
  YAML
  oc -n hub-self create secret generic auto-import-secret \
    --from-literal=autoImportRetry=5 --from-file=kubeconfig=$KUBECONFIG
  # HubConfig: spec.clusterRegistryMode: mce  -> SpokeCluster/hub-self is discovered
  export MC_SPOKE_NAME=hub-self
  ```

> **Caution:** with `ApprovalPolicy/cluster` fully `Automatic`, *any* real critical alert on
> the spoke gets an auto-approved remediation run. Silence unrelated critical alerts on test
> clusters (`amtool silence add alertname=<name>`).

### What it does

| Step | Detail |
|---|---|
| Fixture | Namespace `ls-alert-e2e-<id>` (labelled `openshift.io/cluster-monitoring=true`), Deployment `e2e-app` mounting missing ConfigMap `e2e-app-config`, PrometheusRule `LightspeedE2EDeploymentUnavailable` (critical, `for: 1m`) |
| Phase 1 (5m) | Exactly one AgenticRun appears with `spec.targetCluster`, spoke label `spoke-<name>`, alert labels, `proof=<id>` in `spec.request`, agent `default` |
| Phase 2 (10m per stage) | `Analyzed`, `Executed`, `Verified` become `True`; ConfigMap has `proof=<id>`; Deployment Available with unchanged generation |
| Cleanup (up to ~9m) | Fix the fault if still present, wait until the alert clears (4m after the Deployment became Available), delete the fixture AgenticRuns (finalizers never stripped), then the spoke namespace |

On failure, the AgenticRun status, its Analysis/Execution/Verification results and the spoke
namespace events are printed.

---

## Legacy Manual E2E Test

Manual e2e test for the hub operator on a live OCP cluster. Uses a single cluster acting as both hub and spoke.

## Prerequisites

- Access to an OCP cluster (`oc login` or `KUBECONFIG` set)
- `kubectl` / `oc` CLI
- Operator image pushed to a public registry (e.g. `quay.io/hasun/lightspeed-hub-operator:latest`)

## Build and Push

```bash
IMG=quay.io/hasun/lightspeed-hub-operator:latest
podman build -t $IMG .
podman push $IMG
```

## Deploy

```bash
# Install CRDs
make install

# Deploy the operator (uses kustomize)
IMG=quay.io/hasun/lightspeed-hub-operator:latest make deploy

# Verify operator is running
kubectl get pods -n openshift-lightspeed -l control-plane=hub-controller-manager
kubectl logs -n openshift-lightspeed -l control-plane=hub-controller-manager --tail=10
```

## Test Registration

```bash
# 1. Create HubConfig (secret mode)
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: secret
EOF

# 2. Create admin kubeconfig Secret (self-referencing — points at same cluster)
kubectl create secret generic self-spoke-creds \
  -n openshift-lightspeed \
  --from-file=kubeconfig=$KUBECONFIG

# 3. Create SpokeCluster
API_SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
kubectl apply -f - <<EOF
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: self-spoke
spec:
  apiServer: "$API_SERVER"
  credentialSource:
    secret:
      name: self-spoke-creds
      namespace: openshift-lightspeed
EOF
```

## Verify Registration

```bash
# SpokeCluster status — expect Connected: True (ConnectionSucceeded)
kubectl get spokecluster self-spoke -o jsonpath='{range .status.conditions[*]}{.type}: {.status} ({.reason}) - {.message}{"\n"}{end}'

# Standing kubeconfig Secret created on hub
kubectl get secret spoke-kubeconfig-self-spoke -n openshift-lightspeed

# Spoke-side resources created
kubectl get ns openshift-lightspeed-managed
kubectl get sa lightspeed-agent -n openshift-lightspeed-managed
kubectl get clusterrolebinding lightspeed-hub:cluster-reader
kubectl get clusterrolebinding lightspeed-hub:cluster-monitoring-view
```

## Verify Standing Kubeconfig

Extract the standing kubeconfig and verify it can access the spoke cluster:

```bash
# Extract the standing kubeconfig from the Secret
kubectl get secret spoke-kubeconfig-self-spoke -n openshift-lightspeed \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > /tmp/spoke-kubeconfig

# Test basic access via the standing kubeconfig
KUBECONFIG=/tmp/spoke-kubeconfig kubectl get nodes
KUBECONFIG=/tmp/spoke-kubeconfig kubectl get namespaces
KUBECONFIG=/tmp/spoke-kubeconfig kubectl get pods -A --no-headers | head -10

# Cleanup
rm /tmp/spoke-kubeconfig
```

Note: In a self-referencing test (single cluster), the standing kubeconfig contains
admin credentials. The reader RBAC is for the `lightspeed-agent` SA, not for the
standing kubeconfig itself.

## Verify Spoke Reader RBAC

Verify the `lightspeed-agent` SA has the expected read-only permissions:

```bash
# Should be allowed (cluster-reader)
kubectl auth can-i get nodes \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: yes

kubectl auth can-i list pods --all-namespaces \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: yes

# Should be allowed (cluster-monitoring-view)
kubectl auth can-i get prometheusrules --all-namespaces \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: yes

# Should be denied (no write access)
kubectl auth can-i create deployments \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: no

kubectl auth can-i delete pods \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: no
```

## Verify Health Check

Wait 5+ minutes and check that the controller re-reconciles:

```bash
kubectl logs -n openshift-lightspeed -l control-plane=hub-controller-manager --tail=10
# Expect periodic "Successfully reconciled spoke" log entries
```

## Test Decommission

```bash
# Delete the SpokeCluster
kubectl delete spokecluster self-spoke

# Verify spoke-side cleanup
kubectl get ns openshift-lightspeed-managed                    # Terminating or NotFound
kubectl get sa lightspeed-agent -n openshift-lightspeed-managed  # NotFound
kubectl get clusterrolebinding lightspeed-hub:cluster-reader     # NotFound
kubectl get clusterrolebinding lightspeed-hub:cluster-monitoring-view  # NotFound

# Verify standing kubeconfig GC'd via owner reference
kubectl get secret spoke-kubeconfig-self-spoke -n openshift-lightspeed  # NotFound

# Check operator logs for decommission
kubectl logs -n openshift-lightspeed -l control-plane=hub-controller-manager --tail=5
# Expect: "Deprovisioning spoke" and "Removed finalizer, spoke cleanup complete"
```

## Test CEL Validation

These work without the operator running — enforced by the API server:

```bash
# Reject HubConfig with wrong name
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: not-cluster
spec:
  clusterRegistryMode: secret
EOF
# Expected error: HubConfig name must be 'cluster'

# Reject invalid clusterRegistryMode
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: invalid
EOF
# Expected error: supported values: "secret", "mce"

# Reject SpokeCluster with both credential sources
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: bad-spoke
spec:
  apiServer: "https://api.example.com:6443"
  credentialSource:
    secret:
      name: creds
      namespace: openshift-lightspeed
    mce:
      managedClusterName: bad-spoke
EOF
# Expected error: exactly one of secret or mce must be specified
```

## Cleanup

```bash
kubectl delete hubconfig cluster
make undeploy   # removes operator deployment, RBAC, CRDs
```
