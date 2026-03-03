# ADR-002: Operation Orchestrator

## Status
Accepted

## Context
UI actions can trigger multi-step workflows (plan/apply/sync/conflict handling). Running these steps ad-hoc in update handlers makes retries and observability fragile.

## Decision
- Introduce an orchestrator that owns operation lifecycle: queued -> running -> done/failed.
- Emit operation summary state for probe snapshots.
- Keep UI responsible for intent dispatch and render-only state.

## Consequences
- One place for retry policy and cancellation.
- Probe payload has a consistent operation section.
- Requires explicit mapping from orchestrator events to Tea messages.
