# Constraints

Project-wide invariants. If an agent violates any of these, the system is wrong.

1. The hub MUST NOT require manual per-spoke configuration steps beyond initial spoke registration. Spoke onboarding (adapter deployment, credential setup, health monitoring) must be automated.
2. The hub MUST NOT directly execute commands on spoke clusters. All spoke-side operations go through the spoke's own agentic operator and RBAC.
3. SpokeCluster CRs are the single source of truth for spoke registration state. No side-channel registration mechanisms.
4. The hub MUST tolerate spoke unavailability gracefully — a disconnected spoke must not block operations on other spokes or the hub itself.
5. All cross-cluster communication MUST use mTLS. No plaintext cluster-to-cluster traffic.
6. Commit messages and PR titles MUST start with `OLS-XXXX` (Jira ticket reference).
7. Fork-based git workflow: push to your fork, PR against `origin/main`.
