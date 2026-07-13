# dec-13: Use one signed semantic event sequence per authority and derived projection deltas

- Created: 2026-07-13
- Updated: 2026-07-13

## Rationale

Give each durable authority exactly one canonical sequence: one root sequence for user-global config, registry, views, and preferences, plus one independent sequence per project. Project deltas and root/project projections carry source ranges or vector cursors instead of allocating a competing scalar order. Receipt-bearing signed batches preserve idempotency and tamper evidence, while live tmux, Git, and filesystem observations remain externally authoritative.

## Context

Investigation dgv reconciles whole-authority-plane event sourcing with dew incremental project projections, an event-sourced root user database, exact-match client/service protocol upgrades, permanent compact semantic history, signed recovery, and honest root/project genesis imports.

## Consequences

Root and project authorities have independent identity, sequence, hash chain, signing keys, transactions, snapshots, and recovery. Root owns user-global config/registry/views/preferences and composes project-qualified read models at a vector cursor; projects retain their own canonical facts and outboxes. There is no cross-authority atomic batch or total order. Client/service protocol identifiers match exactly; stored-event upcasters remain required. Implementation is gated on config-authority inventory, separate migration-reviewed root/project stores, root and session shadow parity, recovery drills, and continue/stop decisions.

## Links

- applies-to issue:dgv
