# Configuring spoke baseline reader roles

Hub-managed agentic runs use reader bindings to the spoke seed ServiceAccount
`openshift-lightspeed-managed/lightspeed-agent`. Configure the hub-managed baseline
through HubConfig defaults and additive SpokeCluster roles. Referenced ClusterRoles
must already exist on every applicable spoke; the hub never manages their definitions.

## Fleet defaults

Omitting `agentRBAC.defaultClusterRoles` selects `cluster-reader` and
`cluster-monitoring-view`. An explicit list **replaces** those defaults. An explicit
empty list (`defaultClusterRoles: []`) selects no fleet defaults.

```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: secret
  agentRBAC:
    defaultClusterRoles:
      - cluster-reader
      - cluster-monitoring-view
```

## Add a spoke-specific reader

Install the role on the spoke first, using an administrator identity:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: performance-profile-reader
rules:
  - apiGroups: [performance.openshift.io]
    resources: [performanceprofiles]
    verbs: [get, list, watch]
```

Then register or update the spoke on the hub:

```yaml
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: production
spec:
  apiServer: https://api.production.example.com:6443
  credentialSource:
    secret:
      name: production-credentials
      namespace: openshift-lightspeed
  agentRBAC:
    additionalClusterRoles:
      - performance-profile-reader
```

Spoke additions cannot subtract fleet defaults. Duplicate names produce one binding.
Omitted or empty additions add nothing. MCE discovery preserves this field on
existing auto-discovered SpokeClusters. Configuration updates reconcile without
restarting the hub operator.

## Reader policy and permissions

Every non-exempt configured role must grant **only get/list/watch** and must not grant core Secret or RBAC-resource access. Wildcards
that cover prohibited access are rejected. One disallowed rule rejects the entire
role; permissions are not filtered into a new role. Aggregated roles are validated
using their current effective rules. Split useful reader rules from unsafe rules
into a separate administrator-managed ClusterRole when necessary.

**Default-role exceptions:** ClusterRoles named exactly `cluster-reader` or
`cluster-monitoring-view` bypass permission-content validation. This case-sensitive
exception applies regardless of configuration origin and remains valid after
administrator edits, replacement, or aggregation changes. Custom roles listed in
`defaultClusterRoles`, near-match names, and copies under other names are not exempt.
Exempt roles must still exist and pass API authorization and binding ownership checks.
Administrators own review of these two roles: their current permissions can grant
writes or Secret/RBAC access to every remote phase independently of execution approval.

The standing kubeconfig identity is distinct from the seed and per-step identities.
It needs permission to read referenced ClusterRoles, list/get/create/update/delete
baseline ClusterRoleBindings, and grant the referenced roles under Kubernetes
bind/escalation checks, in addition to existing provisioning permissions. This
feature does not automatically widen standing credentials or grant cluster-admin.
The existing MCE provisioning role lacks binding list/update permissions; arrange
appropriate administrator-managed access before relying on this feature in MCE
mode. Do not edit the discovery-owned ManifestWork: discovery reconciles its spec.

Missing roles (including exempt roles), unsafe non-exempt roles, ownership collisions,
and insufficient API permissions result
in `Provisioned=False` and `Ready=False`, with a message identifying the cause.
Errors are retried. Inspect `SpokeCluster.status.conditions` and operator logs.
The complete role set is validated before baseline bindings are changed, but API
failures during reconciliation can leave a partial update; that is never reported
as successful provisioning.

## Ownership, cleanup, and execution boundary

Bindings carry `hub.openshift.io/component: baseline-reader` and
`hub.openshift.io/spoke-uid: <registration UID>`. The two built-in binding names stay
`lightspeed-hub:cluster-reader` and `lightspeed-hub:cluster-monitoring-view`.
Custom binding names use a deterministic role-name hash. Names are not proof of
ownership: unmarked bindings and bindings owned by another registration are never
adopted, repaired, or deleted. No legacy migration is provided. Resolve collisions
explicitly after inspecting their ownership; do not blindly delete existing access.

The hub repairs owned binding drift and removes obsolete owned bindings. Decommission
and HubConfig cleanup discover owned bindings independently of current configuration.
Administrator bindings and ClusterRole definitions are preserved.

Deploy the compatible agentic-operator remote seed-binding discovery change before
relying on custom baseline roles. That operator must independently validate roles
with the same two name-based exceptions and create isolated per-step grants; it must not mutate the seed bindings. Eligible
administrator-created seed bindings can also contribute access, so CR fields are
not an exclusive permission allowlist. Empty fleet defaults with no additions are
valid hub configuration, but a run still needs an eligible seed binding.

Validation is preflight, not continuous enforcement. Changing role rules can change
existing grants through Kubernetes RBAC. Removing a source binding affects future
phase setup, not already-created per-step grants. Updates are not atomic fleet-wide
revocation. Approved execution permissions, approval behavior, and sandbox behavior
are unchanged. OLS-4685 provides hub unit/controller coverage; real cross-cluster
acceptance is delivered by the separate integration-test task.
