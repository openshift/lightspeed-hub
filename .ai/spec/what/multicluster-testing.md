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

### T2 — multicluster-product-e2e

Full-fidelity fleet on real clusters with a real provider. Everything T1
asserts, plus real hosted-spoke connectivity and the real ROSA-HCP identity path
end-to-end (hub on the management cluster, spokes as hosted clusters).

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
  only difference between T1 and T2.
- Kubeconfig-registered SpokeCluster setup MUST be idempotent (re-running setup
  must converge).

## Future work

- `[PLANNED]` Spoke-local mode tiers: MirrorAgenticRun sync and approval routing.
- `[PLANNED]` Embedded adapter tiers: hub watches AgenticRun CRs created on the
  spoke.
- `[PLANNED]` Fleet-wide alert deduplication tests (fleet-coordination rule 11).
