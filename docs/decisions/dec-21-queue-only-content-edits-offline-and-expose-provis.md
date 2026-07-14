# dec-21: Queue only content edits offline and expose provisional state

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Allow offline creation and editing of content records while requiring online authority for lifecycle, relationships, review, integration, membership, and other invariant-changing operations. Render optimistic changes immediately but visibly mark them pending until accepted.

## Context

Local-first responsiveness and offline drafting are valuable, but authority-changing commands cannot safely be merged after independent execution.

## Consequences

Clients keep a durable pending-command outbox. Reconnect refreshes canonical state before rebasing and submitting pending work, then reports accepted, transformed, conflicted, and rejected outcomes. A rejection blocks only causally dependent pending commands.

## Links

- applies-to issue:dda
