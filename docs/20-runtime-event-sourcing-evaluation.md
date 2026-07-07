# Runtime Event Sourcing Evaluation

## Scope

This note evaluates whether Azedarach should move runtime state to full event
sourcing. Runtime state means the daemon-owned session, worktree, git status,
agent activity, and operation facts rendered by the board/workspace and used by
runtime invariants.

## Current Model

Azedarach currently uses durable current-state projections plus revisioned event
publication:

- SQLite stores current session/worktree/git runtime projections in
  `daemon_session_projections`, `daemon_session_observations`,
  `daemon_session_activity_evidence`, and `daemon_worktree_projections`.
- `internal/daemon/runtime_projection_writer.go` is the ordered writer for
  projection persistence and projection event publication.
- Clients consume snapshots plus revisioned stream events. Gaps trigger
  rehydrate from daemon snapshots rather than replay from a durable runtime
  event log.
- Runtime invariants explicitly choose `projection`, `tmux`, or `hybrid` source
  policy. `tmux`, the filesystem, and git remain external live authorities for
  facts that cannot be derived safely from stale local history.

There is not currently a durable append-only runtime event log that can rebuild
all runtime projections from genesis. Issue observation events and mailbox
events are audit/evidence records, not the source of truth for runtime
projection state.

## Evaluation

Full event sourcing is attractive for auditability, deterministic replay, and
post-failure diagnosis. It would also make revision persistence more explicit:
instead of the daemon holding only in-memory stream revisions and durable
current projections, a runtime ledger could persist every accepted runtime fact
and derive projections from that ledger.

The cost is high for Azedarach's current runtime domain:

- Many runtime facts are observations of external mutable systems. Tmux session
  liveness, filesystem worktree presence, and git status can change outside the
  daemon. Replaying historical daemon events cannot prove current truth without
  reconciling against those external sources.
- The existing invariant model intentionally distinguishes `tmux`,
  `projection`, and `hybrid` checks. Replacing that with event replay would blur
  authority unless replay is treated only as projection hydration, not as truth.
- Runtime telemetry is noisy and partly latest-wins. Persisting every activity,
  git status, and projection coalescing edge would increase storage, migration,
  retention, and backpressure complexity without improving most board reads.
- Existing clients already have the main convergence property they need:
  snapshot plus revisioned events, rehydrate on gaps, and daemon-owned writes.
- Recovery still needs live reconciliation. Even with an event log, daemon
  startup must compare projected intent with tmux/worktree/git reality before
  claiming runtime state is current.

## Decision

Do not move runtime state to full event sourcing now.

Keep the daemon's durable current-state projections as the read model and
preserve the explicit invariant source matrix. Treat runtime stream events as a
client convergence mechanism, not as a durable replay authority.

If a concrete replay/audit consumer appears, add a narrow append-only runtime
evidence ledger behind the daemon writer rather than replacing projections. The
ledger should:

- record durable facts at authoritative write points, with project ID, issue ID,
  event type, payload, source, correlation ID, and revision or monotonic event ID
- be replayable only into projections, never used alone for `tmux` or `hybrid`
  invariants
- preserve latest-wins projection reads as the hot path
- define retention and compaction before enabling high-volume telemetry events
- keep reconciliation as the startup and repair authority for external runtime
  drift

## Follow-Up Triggers

Revisit this decision when at least one of these is true:

- operators need a durable forensic timeline for runtime/session incidents
- runtime revisions must survive daemon restart as a user-visible contract
- a cross-process race cannot be solved by snapshot rehydrate plus reconciliation
- analytics/debug tooling needs historical runtime transitions beyond current
  issue observation events

Until then, prefer focused improvements to projection freshness, reconcile
coverage, observation evidence, and debug visibility.
