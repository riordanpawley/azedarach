# Runtime Event Sourcing Evaluation

## Scope

This note evaluates whether Azedarach should move runtime state, or the broader
daemon authority plane, to full event sourcing. Runtime state means the
daemon-owned session, worktree, git status, agent activity, and operation facts
rendered by the board/workspace and used by runtime invariants. The broader
authority plane also includes issue lifecycle, dependency graph, mailbox
evidence, observations, notices, decisions, specs, operations, and integration
state.

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

There is not currently a durable append-only event log that can rebuild all
runtime and issue projections from genesis. Issue observation events, spec and
decision audit logs, and mailbox events are audit/evidence records, not the
source of truth for the complete daemon state.

## Narrow Runtime Evaluation

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

This narrow runtime-only framing does not produce enough value by itself. It
adds write and retention complexity while leaving issue graph, orchestration, and
integration semantics on mutable tables.

## Whole-System Evaluation

If the question is whether Azedarach should event-source the whole daemon
authority plane, the tradeoff changes. Azedarach is already a local coordination
system: it cares about causality, replay, integration evidence, dependency
readiness, worker closeout, cross-process convergence, and restart recovery.
Those are exactly the areas where event sourcing can create durable leverage.

The useful model is not "events replace tmux truth." The useful model is "all
daemon-authoritative changes and all external observations enter through typed
events, and projections are rebuilt from those events." In that model:

- Commands validate intent and append durable domain events.
- Reconcile loops read tmux/git/filesystem, then append observation events.
- Projections remain the hot read path, but they are disposable read models.
- Runtime invariants read projections that were derived from event history plus
  the latest reconciliation observations.
- Live external systems remain observation authorities; they do not mutate
  projections except through event-producing daemon paths.

### Pros

- **Causality becomes explicit.** Issue status, session desired state, observed
  runtime drift, mailbox evidence, and closeout gates can be traced as one
  ordered history instead of scattered mutable rows plus side logs.
- **Replay becomes a first-class recovery tool.** A daemon restart can rebuild
  projections and then reconcile only the external facts that need live
  observation.
- **Multi-daemon races get easier to reason about.** A project-scoped append log
  can provide the durable ordering that in-memory stream revisions cannot
  preserve across process restarts.
- **Audit and debugging improve materially.** Worker integration, graph
  readiness, issue resource lifecycle, and runtime reconcile decisions can be
  explained from the same ledger.
- **Projection design gets cleaner.** Board, workspace, context-risk,
  orchestration status, notice counts, and runtime summaries can be separate
  read models fed by the same source.
- **Large future features become simpler.** Timeline UI, undo/repair tooling,
  provider-neutral worker backends, and postmortem diagnostics all benefit from
  durable typed history.

### Cons

- **Migration cost is high.** The current issue store, runtime projection store,
  notices, operations, specs, decisions, mailbox, and learning records would need
  a shared event contract or explicit bridge strategy.
- **Event design becomes product architecture.** Poor event names, missing
  causation IDs, or mixed command/result events would create long-lived debt.
- **Telemetry volume needs policy.** Git status, activity hooks, and operation
  progress can flood a ledger unless latest-wins compaction and retention are
  part of the design from the start.
- **External truth still requires reconciliation.** Event sourcing improves how
  observations are captured; it does not make tmux, git, or the filesystem
  derivable from old events.
- **Read-path performance must be protected.** Hot CLI/TUI reads should not
  replay event history; they need maintained projections and query-plan guards.
- **Schema evolution gets stricter.** Every event payload becomes a durable
  compatibility contract with upcasters or migration rules.

## Net Assessment

Event-sourcing only runtime telemetry is probably not worth it.

Event-sourcing the daemon authority plane can have more pros than cons if
Azedarach is willing to treat it as a major architecture migration, not a
storage refactor. The strongest reason is that Azedarach's core value is durable
coordination. The system already needs explainable causality across issue graph
state, worker evidence, runtime observations, and integration gates. A well
designed event log would make that causality explicit and reusable.

The deciding condition is whether the event log becomes the source for
daemon-authoritative facts while projections stay optimized read models. If the
implementation tries to event-source only the noisiest runtime surface, the cons
dominate. If it event-sources issue/orchestration/runtime authority together,
with reconciliation as an event producer and projections as derived state, the
pros can dominate.

## Recommended Next Step

Do not jump directly to rewriting all stores. First design and spike a vertical
slice that proves the hard parts:

1. Define a project-scoped event envelope with stream key, sequence, event type,
   schema version, payload, source, causation ID, correlation ID, actor, and
   observed/emitted timestamps.
2. Pick one cross-cutting flow, such as session start/stop/reconcile or worker
   closeout/integration readiness.
3. Append typed events for command intent, daemon result, and external
   observations.
4. Rebuild the current projection for that flow from the event log.
5. Prove restart replay, cross-process ordering, gap recovery, compaction, and
   query performance.

If that spike makes the code clearer and the validation stronger, continue with
a phased whole-system migration. If it only adds ceremony around existing
mutable tables, keep the current projection-first model.

## Follow-Up Triggers

Revisit the migration decision when at least one of these is true:

- operators need a durable forensic timeline for runtime/session incidents
- runtime revisions must survive daemon restart as a user-visible contract
- a cross-process race cannot be solved by snapshot rehydrate plus reconciliation
- analytics/debug tooling needs historical runtime transitions beyond current
  issue observation events
- orchestration/integration debugging needs one causal timeline across issue
  graph state, worker evidence, runtime observations, and closeout decisions

Until then, prefer focused improvements to projection freshness, reconcile
coverage, observation evidence, and debug visibility, but keep the whole-system
event-sourcing option open.
