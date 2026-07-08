# Lightspeed Hub — Specifications

Multicluster hub operator for OpenShift Lightspeed. Manages a fleet of spoke clusters from a central hub, coordinating agentic operations, automating spoke onboarding, and aggregating observability data across the fleet.

## Structure

| Layer | Path | Purpose |
|---|---|---|
| **what/** | `.ai/spec/what/` | Behavioral rules. What the system must do. Implementation-agnostic. |
| **how/** | `.ai/spec/how/` | Codebase navigation. How the code is organized. Implementation-specific. |

## Scope

Covers the hub operator and its CRDs. Out of scope: spoke-side components (deployed by the classic lightspeed-operator), hub UI (lightspeed-hub-ui), and observability pipeline (lightspeed-otel-collector). For the parent product view, see the workspace-level `.ai/spec/` in the `ols/` root.

## Audience

AI agents. Content is optimized for precision and machine consumption.

## Quick Start

| Task | Start here |
|---|---|
| Understand the system | `what/system-overview.md` |
| Understand spoke lifecycle | `what/spoke-lifecycle.md` |
| Understand fleet coordination | `what/fleet-coordination.md` |

## Conventions

- **Rule numbering:** behavioral rules are numbered sequentially within each what/ file.
- **Planned changes:** unimplemented behavior is marked with `[PLANNED]` or `[PLANNED: TICKET-XXXX]` inline next to the rule it affects.
- **Authority:** what/ specs are authoritative for behavior. how/ specs are authoritative for implementation. When they conflict, what/ wins.

## Updating this spec

- **Adding a new component:** create `what/<component>.md` with behavioral rules and `how/<component>.md` with implementation navigation. Add to the quick-start table.
- **Adding rules to an existing component:** append numbered rules to the relevant section in the what/ file. Use `[PLANNED: TICKET]` for unimplemented behavior.
- **After implementation:** remove `[PLANNED]` markers from implemented rules. Update how/ files if code structure changed.
- **When to create a new file vs. extend an existing one:** if the new concern has its own lifecycle, configuration surface, and can be understood independently, it gets its own file. If it's a capability added to an existing component, it goes in that component's file.
