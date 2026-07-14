# dec-13: Use one signed semantic event sequence per authority and derived projection deltas

- Created: 2026-07-13
- Updated: 2026-07-14

## Rationale

Give each durable authority one canonical ordered index while giving each aggregate instance one logical stream. Each accepted command normally targets one aggregate stream, may append several ordered semantic facts, and uses expected stream revision for optimistic concurrency. Cross-aggregate and cross-authority work uses process managers/sagas rather than multi-stream atomicity. Projection deltas carry source ranges instead of allocating a competing order; replay/subscriptions consume individual events with at-least-once delivery and atomic projection checkpoints.

## Context

Investigation dgv reconciles whole durable-control-plane event sourcing with Greg Young guidance, dew incremental projections, user-global synchronization, device-local state, exact-match client/service protocol, permanent historical upcasters, signed event-commit chains, and honest genesis imports.

## Consequences

User, device, and project authorities retain independent identity, sequence, hash chain, signing keys, transactions, snapshots, and recovery. The authority sequence is an index over aggregate streams, not a giant aggregate. SQLite capability does not authorize multi-aggregate commands. Internal events are separated from external integration contracts; aggregate snapshots are measurement-gated and derived; projection consumers are idempotent and checkpoint atomically. Hash/signature and exact-protocol rules remain Azedarach extensions.

## Links

- applies-to issue:dgv
