# dec-13: Use one signed semantic event sequence and derived projection deltas

- Created: 2026-07-13
- Updated: 2026-07-13

## Rationale

A single project-scoped event sequence gives collaborative commands, deterministic replay, keyed deltas, bootstrap, and recovery one ordering authority. Projection deltas carry canonical source ranges instead of allocating a competing cursor; signed receipt-bearing batches preserve idempotency and tamper evidence, while live tmux, Git, and filesystem observations remain externally authoritative.

## Context

Investigation dgv reconciles whole-authority-plane event sourcing with dew incremental projections, exact-match client/service protocol upgrades, project-scoped authority databases, a non-authoritative root user registry/projection database, permanent compact semantic history, signed suffix recovery, and honest solo-to-team genesis import.

## Consequences

Canonical events, receipts, outboxes, signing, snapshots, and recovery stay project-scoped; the root database retains only local registry/preferences and project-qualified materializations/cursors, with no global event stream. Client and service protocol identifiers must match exactly and upgrade together; historical stored-event upcasters remain required. Implementation is gated on a session lifecycle/reconcile slice, migration-reviewed persistence, shadow parity, signed recovery drills, and an explicit continue/stop decision.

## Links

- applies-to issue:dgv
