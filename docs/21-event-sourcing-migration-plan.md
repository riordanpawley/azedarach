# Event Sourcing Migration Plan

## Goal

Move Azedarach toward event sourcing deliberately, only if a vertical slice
proves that durable typed history gives more leverage than the current mutable
projection model.

The target is not event-sourcing every low-level signal. The target is a deeper
daemon authority-plane module: command handlers append domain events, external
reconciliation appends observation events, and hot CLI/TUI reads use derived
projections.

## Authority Model

Event sourcing must preserve the current daemon ownership contract:

- Daemon commands own durable intent and lifecycle decisions.
- Tmux, git, filesystem, and hooks are external observation authorities.
- The event log owns durable ordering for daemon-accepted facts and observations.
- Projections own hot reads, query plans, and client snapshots.
- CLI/TUI own intent construction and rendering only.

Old events do not make live tmux/git/filesystem state true. They explain what
the daemon knew, intended, and observed. Reconcile remains required, but it
becomes an event producer instead of a direct projection mutator.

## Proposed Modules

### Event Log Module

Files likely involved:

- new `internal/daemon/events`
- new migration beside project-scoped daemon SQLite state
- `internal/contracts/protocol` for event envelope contracts when exposed over
  IPC

Interface:

- append one or more project events transactionally
- read by project sequence, stream key, event type, and time range
- expose latest committed sequence per project
- reject duplicate command/event IDs idempotently

Implementation:

- SQLite append-only table, project-scoped monotonic sequence, JSON payload,
  schema version, causation/correlation IDs, actor/source metadata
- indexes for replay, stream reads, issue/worktree/session lookup, and timeline
  debugging

Depth:

- callers should not know table layout, sequencing mechanics, idempotency
  checks, or payload storage rules

### Projector Module

Files likely involved:

- new `internal/daemon/projections`
- existing `internal/daemon/state/projection_store.go`
- existing issue-store read models under `internal/services/issues`

Interface:

- project events into named read models
- track per-projection offsets
- rebuild one projection from a checkpoint plus event range
- run consistency checks between event log and projection tables

Implementation:

- keeps existing hot tables where useful
- adds projection offset/checkpoint tables
- supports full rebuild in tests and bounded incremental apply in production

Depth:

- command handlers should append domain events, not hand-maintain every derived
  table

### Command Event Writer

Files likely involved:

- `internal/daemon/task_commands.go`
- `internal/daemon/session_commands.go`
- `internal/daemon/handlers/apply_executor.go`
- `internal/daemon/runtime_projection_writer.go`

Interface:

- validate command against current projections and invariants
- append accepted domain events
- optionally publish existing stream envelopes as compatibility output

Implementation:

- initially dual-writes: current mutable store path plus event log
- later flips selected flows so projections derive from events

Depth:

- command paths should stop knowing publication, revision, event persistence, and
  projection-write ordering as separate concepts

### Reconcile Observer

Files likely involved:

- `internal/daemon/runtime_reconcile.go`
- `internal/daemon/git_adapter.go`
- `internal/daemon/runtime_signal_commands.go`
- `internal/services/monitor/session.go`

Interface:

- read external truth
- append observation events
- let projectors update projections

Implementation:

- session/worktree/git observations become typed events with source metadata
- noisy activity and git status observations use compaction keys and retention
  policy

Depth:

- live external probes stay localized; projections should not be mutated directly
  by scattered runtime adapters

### Stream Bridge

Files likely involved:

- `internal/daemon/publish`
- `internal/daemon/daemon.go`
- `internal/client/daemonclient`
- `internal/tui/model_update_loop.go`

Interface:

- convert durable project events or projection changes into existing
  `protocol.EventEnvelope` streams
- preserve current snapshot-plus-revision client behavior during migration

Implementation:

- initially uses current in-memory hub as a compatibility fanout
- later can support durable catch-up from the event log when appropriate

Depth:

- clients should not need to know whether a stream event came from direct
  mutation or event-log projection

## Event Envelope Sketch

Every durable event should carry:

- `event_id`: stable unique ID for idempotency
- `project_id`
- `sequence`: monotonic per project
- `stream_key`: aggregate-ish key such as `issue:cro`, `session:<id>`,
  `worktree:<issue-id>`, `mailbox:<parent-id>`
- `event_type`
- `schema_version`
- `payload_json`
- `source`: `command`, `reconcile`, `hook`, `adapter`, `migration`
- `actor`
- `causation_id`
- `correlation_id`
- `command_id`
- `observed_at`
- `emitted_at`

The sequence is the durable ordering contract. Existing protocol revisions can
remain a client stream cursor while the migration is in progress, but the plan
should eventually decide whether protocol revisions become aliases of event
sequences or remain projection revisions.

## Event Taxonomy Shape

Start with event names that describe facts, not commands:

- `issue.created`
- `issue.details_changed`
- `issue.status_changed`
- `issue.deleted`
- `issue.dependency_added`
- `issue.dependency_removed`
- `session.intent_recorded`
- `session.external_observed`
- `session.lifecycle_aligned`
- `worktree.attached`
- `worktree.external_observed`
- `worktree.detached`
- `git.status_observed`
- `worker.mail_recorded`
- `worker.evidence_recorded`
- `operation.submitted`
- `operation.state_changed`
- `notice.recorded`
- `notice.lifecycle_changed`
- `spec.requirement_changed`
- `decision.recorded`

Do not encode implementation actions such as `update row` or UI outcomes such
as `card refreshed` as durable domain events.

## First Vertical Slice

Use session lifecycle and runtime reconcile as the first spike.

Reason:

- it exercises command intent, external live truth, projection freshness,
  revisioned streams, and hybrid invariants
- it is representative of the hardest authority problem
- it has existing focused tests around session projection, reconcile, restart,
  and stale-cache behavior

Scope:

- session start/stop/attach intent events
- tmux observation events from reconcile
- derived `daemon_session_projections` rows
- existing stream events preserved through the compatibility bridge
- full rebuild test for the session projection from events

Out of scope for the spike:

- replacing all issue mutation storage
- moving mailbox JSONL
- rewriting the TUI reducer
- event-sourcing high-volume git status telemetry beyond one sampled observation
  path

Acceptance criteria:

- command path appends durable events with idempotency keys
- projector derives the same session projection rows as the current path for the
  selected flow
- daemon restart rebuilds session projection from events before reconcile
- reconcile appends observation events and updates projection through the
  projector
- existing `Subscribe`/snapshot client behavior still works
- stale projection/race tests cover at least two daemon instances sharing a DB
- query-plan checks protect the new hot event and projection reads
- replay tests can delete and rebuild the selected projection table

## Migration Phases

### Phase 0: Contract And Inventory

- inventory mutable stores and append/audit stores
- define event envelope, naming rules, stream keys, and event schema versioning
- define projection offset/checkpoint contract
- decide retention classes: permanent domain history, compactable observation,
  and ephemeral telemetry

Gate:

- docs and tests define event envelope semantics before implementation

### Phase 1: Append-Only Store Beside Current Writes

- add event log schema and append/read module
- add idempotency support for command IDs
- write no projectors yet except minimal smoke-test projection
- expose diagnostic CLI/debug read only if needed for validation

Gate:

- appending events is transactional, ordered, idempotent, and isolated by project

### Phase 2: Session Lifecycle Spike

- dual-write session lifecycle events beside current session projection writes
- build a session projector that can rebuild `daemon_session_projections`
- convert one reconcile path to append observation events
- keep current protocol stream output stable

Gate:

- acceptance criteria from the first vertical slice pass

### Phase 3: Projection-First Flip For The Slice

- make the selected session projection derive from events on the active path
- remove direct projection mutation from that slice
- keep repair tooling to rebuild projections from events

Gate:

- direct mutation guard fails if the migrated slice bypasses the projector

### Phase 4: Issue Graph And Worker Evidence

- event-source issue lifecycle, status, dependency edges, mailbox/evidence, and
  closeout readiness
- move mailbox JSONL into event-backed read models or bridge it through durable
  events
- derive orchestration status and context-risk evidence from projections fed by
  the event log

Gate:

- worker closeout and integration readiness explain their decision from one
  causal timeline

### Phase 5: Operations And Notices

- convert daemon operations and notices to event-backed projections
- preserve existing notice and operation list/get commands
- define retention for terminal operation progress and notice lifecycle events

Gate:

- stale operation/notice ordering tests pass from event replay, not direct row
  mutation assumptions

### Phase 6: Specs, Decisions, Learnings

- reconcile existing audit logs with the shared event envelope
- decide whether current audit tables remain specialized projections or migrate
  into the shared project event log

Gate:

- decision/spec sync/import behavior remains compatible and auditable

### Phase 7: Cleanup And Enforcement

- remove dual-write paths after each projection is event-derived
- add boundary checks for direct projection mutation bypasses
- keep query-plan guardrails for event and projection reads
- document restore/rebuild operations

Gate:

- production paths no longer depend on transitional adapters for migrated flows

## Risks And Mitigations

| Risk | Mitigation |
| --- | --- |
| Event taxonomy churn creates permanent compatibility debt | Keep phase 0 taxonomy small; require schema tests and upcasters for changes. |
| Event log becomes a write bottleneck | Use project-scoped sequences, focused indexes, and batch append APIs. |
| Hot reads slow down because callers replay events | Preserve projection tables and query-plan guardrails. |
| External drift gets hidden by replay | Keep reconcile as observation authority and make stale-observation age visible. |
| Dual-write paths drift | Add event/projection parity tests for every migrated flow. |
| Telemetry floods storage | Define compactable observation streams and retention before broad telemetry migration. |
| Clients see revision semantics change | Keep stream bridge compatibility until event sequence semantics are explicitly accepted. |

## Recommended Work Graph

Create these child issues before implementation:

1. Define event envelope, stream keys, and retention classes.
2. Add durable project event log and idempotent append API.
3. Build projection offset/checkpoint infrastructure.
4. Spike session lifecycle event sourcing with projection rebuild.
5. Convert one reconcile path to observation events.
6. Preserve stream/snapshot compatibility from the session slice.
7. Add multi-daemon ordering and replay tests.
8. Write migration decision for whether to continue beyond the spike.

Do not start migrating issue graph, mailbox, operations, or notices until the
session lifecycle spike proves the interface is deep enough to improve locality
and leverage.
