# ADR-004: Conflict Strategy

## Status
Accepted

## Context
Concurrent operations and external changes (git/beads/tmux) can conflict with local UI intent.

## Decision
- Prefer optimistic execution with deterministic conflict detection.
- Pause operation at first conflict and surface a structured resolution prompt.
- Record selected strategy (retry/abort/manual) in operation summary for probes.

## Consequences
- Conflicts become observable and reproducible in e2e probes.
- Users get explicit decision points instead of silent divergence.
- Requires conflict metadata contracts between services and UI.
