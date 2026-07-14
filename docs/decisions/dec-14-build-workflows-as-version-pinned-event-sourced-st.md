# dec-14: Build workflows as version-pinned event-sourced state machines

- Created: 2026-07-14
- Updated: 2026-07-14
- Revised by: dec-15

## Rationale

Model each durable workflow instance as an aggregate stream inside its existing user, device, or project authority. Pure decide and reduce contracts turn typed signals into replayable workflow state, while post-commit runners execute durable actions, timers, human waits, and child workflows through idempotent commands. This reuses the canonical event sequence without turning projections, wake hints, or an operational queue into a second workflow authority.

## Context

Investigation dgv requires event sourcing to support a reusable event-driven workflow system defined by state schemas and reducers. The workflow layer must preserve deterministic replay, authority isolation, current daemon invariants, external-effect truth, and local-first recovery.

## Consequences

Initial definitions are typed compiled Go components with immutable ID/version/digest and declared capabilities; a general user-authored DSL is deferred. Each version has one checksum-pinned embedded manifest/artifact, and build dependency guards keep decisions, reducers, and migrations isolated from runtime adapters. Instances pin their definition version and migrate only through explicit events. Every definition/reducer version referenced by permanent history remains digest-pinned and available to runtime/restore replay; migration does not remove that obligation. Domain events, timers, human input, child outcomes, and action results become typed idempotent signals. Runners provide at-least-once attempts, require downstream idempotency or reconciliation, and surface uncertain effects. Fan-out, joins, retries, compensation, and causation depth are bounded; exhaustion becomes visible stalled/intervention state. Workflow semantic history is permanent, operational diagnostics are bounded, and cross-authority workflows use sagas rather than atomic commits.
