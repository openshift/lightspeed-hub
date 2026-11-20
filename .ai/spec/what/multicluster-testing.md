# Multicluster Testing

Behavioral specification for the **multicluster test suite** as owned by
`lightspeed-hub` — the tests that validate hub-managed fleet operations across
real clusters. The hub is the primary owner of this suite; the cross-repo tier
definitions, ownership split, and shared kubeconfig contract live in the parent
spec at `ols/.ai/spec/what/multicluster-testing.md`. This document specifies the
hub's mechanics and its share of the coverage.

Behavioral rules under test live in
[spoke-lifecycle.md](spoke-lifecycle.md) and
[fleet-coordination.md](fleet-coordination.md).

> **Status:** Coverage below is planned, not an implementation inventory. Hub controllers/CRDs and independent tests already exist. Verify landed work rather than treating the repository as greenfield.

## On-demand lifecycle coverage [PLANNED: OLS-4639, OLS-4788]

OLS-4639 owns hub-only installed-bundle validation: unified static assets/images on supported OCP versions, no CSV/idle hub Deployment, sole installation permission ownership, permission-only SA packaging, pinned sync, overrides/mirroring, staged inactivity, activation/configuration recovery, non-overlapping status writers, rollouts/self-healing, deletion during startup/restarts, and protected final runtime teardown. Use an OpenShift/OLM-capable environment for platform/package assertions; kind alone cannot prove them.

OLS-4788 owns real-spoke draining/cleanup evidence: manual/adapter race guards, remaining admitted phases, named approval blockers, explicit cancellation/confirmed teardown, credential retention through cleanup, individual/fleet/mode-change isolation, remote best-effort only after drain, retryable hub-side failures, stale acknowledgements/restarts, fresh re-enable and GitOps reapplication. Mode-change cleanup retains registrations/source Secrets and handles readoption safely; MCE/default-all tests coordinate actual OLS-3954 support. OLS-4660 shared integration fixtures validate identity/freshness, admitted-run boundaries, condition writers and acknowledgement ordering. Match examples to reviewed OLS-4659/4660/4661 APIs and S0–S4 evidence. Hub-only/fake tests do not prove remote safety; OLS-4782/deferred policies are not current-phase blockers.

## Design

Both tiers run against **real clusters** — cross-cluster RBAC, remote kube-api,
ephemeral SA lifecycle, cleanup, and spoke-failure tolerance all require a real
kubelet and a real second apiserver, which an in-process fake (envtest) cannot
provide. kind is cheap enough (~2–4 min setup/teardown for a hub + a couple of
spokes) to gate PRs, so there is no separate envtest tier — the single kind tier
covers the reconcile logic an envtest tier would have covered *and* the real
cross-cluster behavior on top.

The one axis that separates the tiers is the **LLM**: mock agent server (T1) vs.
real provider (T2). Most multicluster risk is LLM-independent — cross-cluster
RBAC, remote kube-api, ephemeral SA creation/cleanup, and spoke-failure
tolerance are all exercised with the mock agent, at no LLM cost. Real LLM only
buys end-to-end behavioral fidelity, so it appears in T2 alone.

See the parent spec for the full tier table (T1 `multicluster-e2e` /
T2 `multicluster-product-e2e`).

## Coverage (hub-owned)

### T1 — multicluster-e2e

Real cross-cluster coverage without LLM cost. One kind cluster is the hub;
N kind clusters are spokes, each with its own apiserver. The hub reaches spokes
over real remote kube-api.

**Reconcile / lifecycle:**

- **SpokeCluster reconcile**: registration drives `Connected` / `AdaptersReady`
  conditions; re-reconcile is idempotent (spoke-lifecycle rules 7, 8, 21).
- **Credential broker**: `SecretCredentialSource` returns a usable `rest.Config`
  from a referenced Secret; a `credentialSource` that mismatches the HubConfig
  mode is rejected (spoke-lifecycle rules 9–13).
- **Registration validation**: preserve credential-source/schema checks (OLS-4637) without requiring HubConfig-first creation. Staged valid registrations are inactive; registry-mode changes follow drain-first unmanagement under OLS-4781.
- **Decommission best-effort after drain**: unreachable-spoke remote cleanup can finish with logs/Warning Events before CR deletion only after workflows and credential-consuming cleanup finish. Hub-side API/permission failures retry; deleted status/expiring Events are not durable cleanup reports.

**Real cross-cluster behavior:**

- **RBAC scoping**: the ephemeral token succeeds inside `targetNamespaces` and
  is denied outside them (the broker-provisioned credential is hub-owned).
- **Fleet visibility tolerates failure**: with one spoke's apiserver stopped,
  AgenticRuns targeting the other spokes still list and reconcile
  (fleet-coordination rule 6; spoke-lifecycle rule 15).
- **alerts-adapter wiring**: a standalone adapter on the hub polls a spoke's mock
  AlertManager over remote kube-api and creates an AgenticRun with the correct
  `spec.targetCluster` (fleet-coordination rules 1, 9). Adapter internals are
  owned by `lightspeed-agentic-alerts-adapter`; the hub owns the wiring test.
  The full real-AlertManager pipeline is covered separately by the alert
  pipeline suite below.

### T2 — multicluster-product-e2e

Full-fidelity fleet on real clusters with a real provider. Everything T1
asserts, plus real hosted-spoke connectivity and the real ROSA-HCP identity path
end-to-end (hub on the management cluster, spokes as hosted clusters).

### Alert pipeline E2E [PLANNED: OLS-4478]

Validates the full hub-managed alert pipeline on real OpenShift clusters:
spoke Prometheus rule evaluation → spoke AlertManager → hub multicluster
alerts-adapter poll → AgenticRun on the hub → Analysis / Execution /
Verification against the spoke → the fault is fixed and the alert resolves
(fleet-coordination rules 1, 2, 9; spoke-lifecycle rules 7–7b).

This is a separate suite: it needs real OpenShift monitoring (not available on
kind) and exactly one spoke, so it cannot share the two-spoke `mc_e2e` /
`mc_product_e2e` suite setup.

#### Environment

Runs only on dedicated, ephemeral, connected OpenShift test clusters. The suite
raises a `severity: critical` alert on the spoke; paging and disconnected
registries are out of scope.

| Variable | Required | Purpose |
|---|---|---|
| `MC_HUB_KUBECONFIG` | yes | Admin kubeconfig for the hub |
| `MC_SPOKE_KUBECONFIG` | yes | Admin kubeconfig for the single spoke under test |
| `MC_SPOKE_NAME` | yes | Name of the `SpokeCluster` CR registered for that spoke |
| `SKIP_LLM_STAGES` | no | `true` runs Phase 1 only (no LLM provider needed) |

`MC_SPOKE_KUBECONFIG` / `MC_SPOKE_NAME` match the single-spoke variables used by
`lightspeed-agentic-operator/test/mc-product-e2e`, so one CI environment can
drive both suites.

#### Prerequisites (verified in BeforeSuite, never modified unless stated)

1. HubConfig exists, and `SpokeCluster/$MC_SPOKE_NAME` has `Connected=True`,
   `Provisioned=True` and `AdaptersReady=True`.
2. The `lightspeed-hub-alerts-adapter` Deployment in `openshift-lightspeed` is
   available, and its pod started after the SpokeCluster reached
   `AdaptersReady=True` (the adapter discovers spokes only at startup).
3. `hub-alerts-adapter-config` contains `filtering.allowedReceivers` including
   `Critical` (case-insensitive) and `preRunDelay` unset or `0`. The hub creates
   this ConfigMap once with an empty allowlist (adapter inert by default) and
   never overwrites it; the admin/CI owns its content. The adapter reads config
   only at startup, so the adapter pod MUST have started after the ConfigMap's
   last change (compare pod start time with the ConfigMap's latest managedFields
   time). The suite does not edit the ConfigMap or restart the adapter, to avoid
   interfering with other suites.
4. The spoke uses default OpenShift monitoring: the `alertmanager-main` Route
   exists and `severity=critical` alerts route to the `Critical` receiver.
5. The AgenticRun CRD is installed on the hub (lightspeed-agentic-operator
   deployed). If `AgenticOLSConfig/cluster` exists, it is not suspended (the
   adapter creates no AgenticRuns while suspended).
6. `ApprovalPolicy/cluster` sets **all** stages (Analysis, Execution,
   Verification, Escalation) to `Automatic`. If absent, the suite creates it
   with all stages `Automatic` and never deletes it. If present with any stage
   not `Automatic`, the suite fails in BeforeSuite with a message naming the
   stage.
7. Unless `SKIP_LLM_STAGES=true`: `Agent/default` (the agent the adapter assigns
   to every step) exists with a model and an `LLMProvider` reference, and the
   referenced `LLMProvider` exists. The suite does not create or modify LLM
   configuration.

#### Fault scenario

An always-firing synthetic alert is not used: the agent would find nothing to
fix and end the run with `NoActionRequired` instead of executing and verifying.
The suite instead creates a real, deterministic fault with exactly one correct,
create-only fix. Per run, with a random short `<id>`, the suite creates on the
spoke:

1. **Namespace** `ls-alert-e2e-<id>` labelled
   `openshift.io/cluster-monitoring=true` (so platform Prometheus loads rules from
   it) and an ownership label carrying `<id>`.
2. **Deployment** `e2e-app` (1 replica, `ubi9/ubi-minimal` running
   `sleep infinity`, restricted-SCC-compliant security context) whose pod mounts
   ConfigMap `e2e-app-config` as a non-optional volume. The ConfigMap does not
   exist, so the pod is stuck in `ContainerCreating` with a
   `configmap "e2e-app-config" not found` event.
3. **PrometheusRule** in the same namespace:

   ```yaml
   alert: LightspeedE2EDeploymentUnavailable
   expr: kube_deployment_status_replicas_available{namespace="ls-alert-e2e-<id>",deployment="e2e-app"} < 1
   for: 1m
   labels:
     severity: critical
   annotations:
     description: >-
       Deployment e2e-app in namespace ls-alert-e2e-<id> has no available
       replicas. It requires ConfigMap e2e-app-config with key proof=<id>.
   ```

   The adapter renders `description` into `spec.request`, giving the agent the
   exact value to create.

Expected agent behavior: Analysis finds the missing ConfigMap and requests only
namespace-scoped ConfigMap create permission; Execution creates
`e2e-app-config` with `proof=<id>`; Verification observes the Deployment become
Available. The alert expression then becomes false and the alert resolves.

**Re-run isolation:** the alert's `namespace` label differs per run, so the
adapter's label-based dedup key (`agentic.openshift.io/alert-group-id`) is
unique per run and neither the active-run check nor `postRunDelay` (default 1h)
can suppress AgenticRun creation. The alert name stays fixed. AgenticRun has no
run-level namespace scope (removed in OLS-4346); execution permissions are only
those requested by Analysis, which for this fault is namespace-scoped ConfigMap
create in the throwaway namespace.

#### Phases

One `Ordered` Ginkgo container; Phase 2 depends on the AgenticRun found in
Phase 1.

**Phase 1 — adapter integration (no LLM).** Wait up to **5 minutes** (rule
creation to AgenticRun measured at 1.5–2.7 minutes on OCP: scrape, rule
evaluation, `for: 1m`, AlertManager `group_wait` and one adapter poll) for
exactly one AgenticRun in `openshift-lightspeed` with label
`agentic.openshift.io/alert-name=lightspeede2edeploymentunavailable`,
`spec.targetCluster` = `$MC_SPOKE_NAME`, and `spec.request` containing
`ls-alert-e2e-<id>` (the adapter renders the alert's `namespace` label and
description into the request). Assert:

- label `hub.openshift.io/spoke-cluster` is the adapter's spoke target ID for
  `$MC_SPOKE_NAME`: `spoke-<name>`, or a truncated name with a hash suffix when
  longer than 63 characters (adapter `SpokeTargetID`); it is not the bare
  SpokeCluster name
- labels `agentic.openshift.io/source`, `agentic.openshift.io/alert-severity=critical`
  and `agentic.openshift.io/alert-group-id` are set
- `spec.request` contains the alert name and `proof=<id>`
- `spec.analysis`, `spec.execution` and `spec.verification` reference agent
  `default`

AgenticRuns are read as `Unstructured` to avoid a Go module dependency on
`lightspeed-agentic-operator` types.

**Phase 2 — agentic stages (LLM).** Skipped when `SKIP_LLM_STAGES=true`. Wait,
each up to 10 minutes (the agentic operator's default analysis/execution step
timeout), for `Analyzed=True`, `Executed=True`, `Verified=True` on
the AgenticRun. Fail immediately if `Denied`, `Escalated`, `EmergencyStopped`
or any step condition becomes `False`. Then verify independently of the run's
own conditions:

- ConfigMap `e2e-app-config` in `ls-alert-e2e-<id>` on the spoke has
  `data.proof=<id>`
- Deployment `e2e-app` is `Available=True`
- Deployment `e2e-app` `metadata.generation` is unchanged since creation (the
  root cause was fixed, not worked around, e.g. by making the volume optional)

Single attempt, no retries. On failure, the suite logs the AgenticRun
conditions and step status, and the Analysis / Execution / Verification result
objects referenced by the run.

#### Cleanup

Deleting the namespace does not clear the alert immediately: without a resolved
notification, AlertManager keeps it active for about 4 minutes (4x the 1m resend
interval), and the adapter creates a new AgenticRun as soon as no matching run
exists. Existing fixture runs block duplicates (active-run and `postRunDelay`
dedup), so cleanup keeps them until the alert has cleared. Unconditionally
(registered right after the namespace is created), in order:

1. Resolve the fault: create ConfigMap `e2e-app-config` if absent and wait up to
   2 minutes for the Deployment to become `Available`, so Prometheus sends a
   resolved notification.
2. Wait until 4 minutes after the Deployment became `Available` (measured
   ~2.5 minutes for the alert to leave AlertManager).
3. Delete every fixture AgenticRun and wait for them to disappear (never strip
   finalizers; a stuck run is reported, not hidden).
4. Delete namespace `ls-alert-e2e-<id>` on the spoke.

If the fault cannot be resolved, delete the namespace first, wait 5 minutes for
AlertManager to expire the alert, then delete the fixture AgenticRuns. If the
PrometheusRule was never created, delete runs and namespace directly.

Shared prerequisites (HubConfig, SpokeCluster, ApprovalPolicy, Agent,
LLMProvider, adapter ConfigMap) are never deleted.

#### Mechanics

- Files: `test/e2e/alert_pipeline_suite_test.go` (suite entry, BeforeSuite
  prerequisites), `test/e2e/alert_pipeline_test.go` (phases, cleanup) and
  `test/e2e/alert_pipeline_helpers_test.go` (fixtures, Unstructured accessors).
- Build tag: `//go:build alert_pipeline_e2e`. Helpers shared with the
  `mc_e2e` / `mc_product_e2e` suite (e.g. env lookup, client construction) move
  to a file tagged for all three tags.
- Make target: `make alert-pipeline-e2e` (`go test -timeout 60m`); does not
  provision clusters.
- Cluster setup: `hack/alert-pipeline-setup.sh` (`make alert-pipeline-setup`)
  prepares a fresh OpenShift 5 cluster as hub and spoke with ACM (the
  single-cluster workaround below; `availabilityConfig: Basic` and unused ACM
  components disabled so it fits a ~3.5 CPU cluster; MCE stays enabled), deploys the agentic operator and hub, and
  satisfies all prerequisites. Inputs: `KUBECONFIG`, `HUB_IMAGE`, `OPENAI_API_KEY`
  (or `SKIP_LLM_SETUP=true`). Firing critical alerts are silenced and listed,
  never failing the setup; the adapter stays disabled until they are silenced.
- Timeouts: 5m (AgenticRun created), 10m each for `Analyzed`, `Executed`,
  `Verified`, up to ~9m cleanup. Measured on OCP 5.0 with `gpt-6.1-sol`:
  Phase 1 only ~7m, full run ~11m; one analysis took ~6m (190k input tokens).
- Single-cluster runs: with ACM/MCE as the inventory, the hub's self-import
  (`local-cluster`) is excluded from discovery. Disable hub self-management
  (`MultiClusterHub.spec.disableHubSelfManagement: true`) and import the hub as
  an ordinary ManagedCluster under another name (e.g. `hub-self`, without the
  `local-cluster` label); discovery then creates `SpokeCluster/hub-self`.
  Validated on OCP 5.0.2 with ACM 5.0. Test-environment only; it does not
  exercise cross-cluster networking.

#### Acceptance

- `SKIP_LLM_STAGES=true make alert-pipeline-e2e` passes on an ephemeral OCP
  hub + spoke without LLM credentials.
- `make alert-pipeline-e2e` passes with a configured `Agent/default` and real
  provider.
- Cleanup runs on failure and leaves no AgenticRun or test namespace behind.
- Repeated runs against the same clusters each produce a new AgenticRun.

## Mechanics

Follows the agentic-operator convention: build-tag-gated Go tests, `make`
targets, one behavioral spec.

### Build tags

New tags, distinct from the operator's `e2e` / `product_e2e` tags:

```go
//go:build mc_e2e           // T1
//go:build mc_product_e2e   // T2
```

### Make targets

```bash
make mc-e2e            # T1: brings up kind hub + spokes, mock agent
make mc-product-e2e    # T2: expects hub + spoke kubeconfigs in env, real credentials
```

### Cluster access

Tests never provision clusters. CI hands them kubeconfigs via the shared
contract defined in the parent spec (`MC_HUB_KUBECONFIG`,
`MC_SPOKE_KUBECONFIGS`, and the `E2E_PROVIDER*` set for T2). The test binary is
identical across T1 and T2 — only the source of the kubeconfigs differs.

For T1, `hack/mc-kind-up.sh` creates the kind topology and writes these
variables. For T2, CI populates them from the HyperShift steps.

## CI wiring

### T1 — path-scoped presubmit

Single container/VM, no cloud profile. `make mc-e2e` brings up the kind hub +
N kind spokes via `hack/mc-kind-up.sh`, runs the `mc_e2e` suite with the mock
agent, and tears kind down. Prior art: the `ocm/e2e/kind` workflow in
`openshift/release` (OCM is a hub / managed-cluster product using kind the same
way).

T1 MUST run per-PR and block merge when a PR touches the hub risk paths below.
It MAY be skipped otherwise (Prow `run_if_changed`); the exact regex lives in
`openshift/release`.

**Hub risk paths:** credential broker, SpokeCluster controller, fleet
coordination, cross-cluster / remote-client code, and the `mc_e2e` tests or
`hack/mc-kind-up.sh`.

### T2 — periodic

Self-managed hub + HyperShift hosted spokes; nightly/weekly, non-blocking
(firewatch → Jira). The release-repo YAML is authored when the code exists; see
the parent spec's CI section for the target job shape and step-registry prior
art (the hub provisions its own management cluster rather than reusing the shared
Test-Platform HyperShift cluster).

## Constraints

- T1 requires a container/VM able to run nested kind clusters; no cloud
  credentials.
- T2 requires cloud credentials (hub provisioning + HyperShift) and real LLM
  provider credentials.
- The `mc_e2e` and `mc_product_e2e` test binaries MUST behave identically given
  the same `MC_HUB_KUBECONFIG` / `MC_SPOKE_KUBECONFIGS` — provisioning is the
  only difference between T1 and T2. The `alert_pipeline_e2e` suite is
  excluded: it is OpenShift-only, single-spoke, and uses `MC_SPOKE_KUBECONFIG`
  / `MC_SPOKE_NAME`.
- Kubeconfig-registered SpokeCluster setup MUST be idempotent (re-running setup
  must converge).

## Future work

- `[PLANNED]` Spoke-local mode tiers: MirrorAgenticRun sync and approval routing.
- `[PLANNED]` Embedded adapter tiers: hub watches AgenticRun CRs created on the
  spoke.
- `[PLANNED]` Fleet-wide alert deduplication tests (fleet-coordination rule 11).
