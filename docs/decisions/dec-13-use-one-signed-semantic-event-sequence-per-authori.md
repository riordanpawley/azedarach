# dec-13: Use one signed semantic event sequence per authority and derived projection deltas

- Created: 2026-07-13
- Updated: 2026-07-13

## Rationale

Give each durable authority exactly one canonical sequence: one user-global sequence, one device-local sequence per device, and one independent sequence per project. Authority deltas and cross-authority projections carry source ranges or vector cursors instead of allocating a competing scalar order. Accepted command batches include signed canonical results; rejected/no-op results remain in bounded idempotency storage unless explicitly promoted to semantic audit facts. Live tmux, Git, filesystem, and process observations remain externally authoritative.

## Context

Investigation dgv reconciles whole-authority-plane event sourcing with dew incremental project projections, user-global synchronization, device-local durable state, an exact-match client/service protocol, permanent historical-event upcasters, signed event chains, and honest user/device/project genesis imports.

## Consequences

User, device, and project authorities have independent identity, sequence, hash chain, signing keys, transactions, snapshots, and recovery. User owns portable views/preferences; device owns local registration/paths/capabilities; projects own shared project facts and outboxes. Cross-project read models use a user/device/project vector cursor with no cross-authority atomic batch or total order. Offline recovery is mandatory for projects and optional for user/device authorities. Config keys must be classified as project, user, device, secret, or ephemeral. Implementation is gated on authority inventory, separate migration-reviewed stores, user/device and session shadow parity, recovery drills, and continue/stop decisions.

## Links

- applies-to issue:dgv
