# dec-9: Board views are typed projections over issue facts

- Created: 2026-07-09
- Updated: 2026-07-09

## Rationale

Use a small durable issue lifecycle as the single source of truth, then derive board placement from validated typed predicates over issue state, review readiness, session/activity projection, closed outcome, and operation/delegation facts. Ordered columns use first-match-wins so multi-match placement is deterministic and view-specific.

## Context

cyk expands issue lifecycle redesign to include persistent configurable board views. The design must avoid making presentation columns durable issue statuses.

## Consequences

Board views can show lifecycle, waiting-human, waiting-AI, review, done, cancelled, or other typed columns without adding impossible issue states. Mutation commands must target explicit lifecycle/review/close facts instead of arbitrary query-column state. Persisted view JSON must decode into validated discriminated Go types before use.

## Links

- applies-to issue:cyk
- applies-to requirement:cyk-configurable-board-views
