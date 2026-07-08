# dec-7: Gate event sourcing migration on detailed risk controls

- Created: 2026-07-08
- Updated: 2026-07-08

## Rationale

The event-sourcing migration has enough authority, compatibility, persistence, and operational risk that implementation should proceed only through explicit workstreams, phase gates, rollback points, and risk mitigations. The detailed map makes risks reviewable before code changes and prevents a broad rewrite from outrunning recovery, stream compatibility, projection parity, and query-performance evidence.

## Context

Issue cro expanded the event-sourcing plan beyond the session lifecycle spike. docs/22-event-sourcing-detailed-map-and-risk-register.md now maps workstreams W0-W9, dependencies, gates, rollback points, risk mitigations, mitigation work items, and stop conditions.

## Consequences

Future implementation child issues should reference the detailed map, satisfy the relevant gate before moving to the next phase, and add tests for the named risk mitigations rather than relying on generic green test sweeps.

## Links

- applies-to issue:cro
