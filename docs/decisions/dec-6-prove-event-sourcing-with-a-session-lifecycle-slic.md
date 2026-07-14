# dec-6: Prove event sourcing with a session lifecycle slice

- Created: 2026-07-08
- Updated: 2026-07-08

## Rationale

The session lifecycle/reconcile flow exercises the hardest authority boundary: daemon intent, live tmux/worktree observations, projection freshness, revisioned streams, and hybrid invariants. If event sourcing cannot improve locality and validation there, broader issue/mailbox/notice migrations would add ceremony without enough leverage.

## Context

Issue cro planning now treats full event sourcing as a possible daemon authority-plane migration. The migration plan in docs/21-event-sourcing-migration-plan.md recommends an append-only event log, derived projections, event-producing reconciliation, and a session lifecycle spike before migrating issue graph, mailbox, operations, notices, specs, decisions, or learnings.

## Consequences

Implementation should start with event envelope and event-log infrastructure, then a session lifecycle/reconcile spike. Broader migrations remain gated on replay, restart, multi-daemon ordering, stream compatibility, compaction, and query-performance evidence from that slice.

## Links

- applies-to issue:cro
- applies-to issue:dgv — The recommended proof remains a session lifecycle/reconcile vertical slice unless dgv records stronger evidence to revise it.
