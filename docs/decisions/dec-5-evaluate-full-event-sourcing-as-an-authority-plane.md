# dec-5: Evaluate full event sourcing as an authority-plane migration

- Created: 2026-07-07
- Updated: 2026-07-07

## Rationale

The earlier runtime-only conclusion was too narrow. Event-sourcing only noisy runtime telemetry likely is not worth it, but event-sourcing the daemon authority plane can have more pros than cons if all daemon-authoritative changes and external observations enter through typed events, projections become derived read models, and reconciliation produces observation events instead of being bypassed.

## Context

Issue cro was reopened to consider massive Azedarach changes with clear authority boundaries. The net-value test now covers issue lifecycle, dependency graph, mailbox/evidence, operations, notices, runtime observations, and integration gates, not only current runtime projection tables.

## Links

- applies-to issue:cro
- revises decision:dec-4 — Supersedes the narrow runtime-only recommendation while preserving its warning that old events cannot replace live tmux/git/filesystem observation.
