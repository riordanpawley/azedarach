# Daemon-Owned Async Notice Architecture

## Purpose

Async user feedback must move from TUI-local presentation state to a daemon-owned
domain contract. The daemon owns notice identity, lifecycle, operation linkage,
dedupe, retention, and event publication. TUI clients render projections and
send lifecycle/action intent back through daemon protocol commands.

This document specifies the target contract for:

- `async-notice-daemon-contract`
- `async-notice-durable-store`
- `async-notice-tui-projection`
- `async-notice-action-center`
- `async-notice-validation`

The implementation is split across child issues:

- `ctx`: durable store and protocol shape
- `cty`: mutation/operation notice emission
- `ctz`: TUI projection migration
- `cua`: action center actions and routing
- `cub`: validation matrix

## Current V1 State

The current TUI keeps notification history in memory. `recordNotificationHistory`
creates local IDs such as `notice-1`, infers a reference with regexes, stores
level/message/read/dismissed fields in `m.notificationHistory`, and caps the
slice by local capacity. Opening the history overlay marks rows read locally.
Dismissal is also local.

That state is useful as the migration source because it already identifies the
surfaces users expect:

- card and workspace mutation failure markers
- floating toasts
- notification history
- footer attention counts

It is not sufficient as the durable contract:

- IDs are generated per TUI process.
- records disappear on TUI restart.
- multiple TUI clients cannot share read/dismissed state.
- source operation, typed cause, recovery action, and retention policy are not
  durable fields.
- card/workspace failure state, toast history, footer counts, and runtime event
  details are separate pipelines that can drift.

## Ownership

Daemon/domain owns:

- notice record creation and mutation
- source operation linkage
- dedupe and supersession
- lifecycle transitions
- durable SQLite persistence
- revision sequencing and event publication
- recovery action execution when an action mutates project state

Protocol/client owns:

- typed request, response, and event payloads
- compatibility/versioning for notice fields
- reconnect, resubscribe, and snapshot fallback behavior

TUI owns:

- local rendering state
- keyboard/mouse routing
- local-only actions such as opening an already-present task view or copying
  details to the clipboard
- conversion from daemon notice projection to card markers, workspace detail,
  floating toasts, history/action center rows, and footer counts

The TUI must not be the durable authority for notice IDs, read state, dismissed
state, resolved state, retry state, or retention.

## Notice Record

The daemon notice model should be a project-scoped domain record with these
fields. Protocol structs may group nested fields, but the serialized contract
must preserve the same semantics.

| Field | Semantics |
| --- | --- |
| `notice_id` | Stable daemon-generated ID. |
| `project_id` | Required project namespace and isolation boundary. |
| `scope` | Typed target such as project, task/issue, session, worktree, or operation. |
| `source` | Optional operation/request/event source with `operation_id`, operation kind, request ID, and producer. |
| `severity` | `info`, `success`, `warning`, or `error`. |
| `category` | Typed category such as `operation_failed`, `recovery_required`, `mutation_rejected`, `background_sync`, or `action_result`. |
| `state` | Lifecycle state: `active`, `resolved`, `dismissed`, or `expired`. |
| `read` | Durable attention flag independent of lifecycle state. |
| `title` | Short display title. |
| `summary` | One-line display summary. |
| `detail` | Optional long explanation for workspace/action-center detail. |
| `cause` | Typed cause code, message, retryability, and optional raw diagnostic reference. |
| `actions` | Typed action descriptors available for the current state. |
| `dedupe_key` | Stable domain key used to coalesce equivalent active notices. |
| `occurrence_count` | Number of coalesced occurrences. |
| `first_occurrence_at` | First time this dedupe group appeared. |
| `last_occurrence_at` | Latest time this dedupe group appeared. |
| `created_at` | Record creation time. |
| `updated_at` | Last lifecycle/content update time. |
| `resolved_at` | Set when state becomes `resolved`. |
| `dismissed_at` | Set when state becomes `dismissed`. |
| `expires_at` | Earliest time a terminal record may be garbage-collected. |
| `retention_class` | Domain retention policy class. |

The operation record remains the authority for execution state. A notice is the
user-facing feedback and action layer linked to an operation; it must not replace
`operation.get`, `operation.list`, or operation logs.

## Lifecycle

The lifecycle is durable and idempotent.

1. Producer emits a notice candidate from a daemon-owned mutation, operation, or
   recovery path.
2. Daemon computes the dedupe key from project, scope, category, source kind,
   target resource, and cause code.
3. If an active notice with the same dedupe key exists, daemon updates it,
   increments `occurrence_count`, refreshes `last_occurrence_at`, updates the
   latest source, and publishes a notice update event.
4. If no active match exists, daemon creates a new active unread notice and
   publishes a notice created event.
5. Read/dismiss/resolve actions mutate the daemon record and publish update
   events.
6. Terminal records remain queryable until their `expires_at` and retention class
   allow cleanup.

Allowed transitions:

- `active -> resolved`
- `active -> dismissed`
- `resolved -> dismissed`
- `dismissed -> active` only when the same unresolved condition recurs and the
  daemon intentionally reactivates the notice
- `resolved|dismissed -> expired` only by retention cleanup

Invalid transitions must be rejected by the daemon. Duplicate client commands
against the same final state should be idempotent.

Read state is not a lifecycle state. A notice can be active and read, active and
unread, dismissed and unread, or dismissed and read. Footer attention counts use
active unread notices unless a surface explicitly asks for historical counts.

## Operation Linkage

Operation-linked notices must include:

- `operation_id`
- operation kind
- operation state at notice creation/update
- issue/task scope when available
- resource keys when relevant
- the operation dedupe key when the operation has one
- enough log/detail reference data to route `open logs` or `copy details`

Operation lifecycle updates drive notice lifecycle:

- queued/running progress can update an existing active notice only when that
  notice category is progress-oriented
- failed operations create or update an active error notice
- cancelled operations create or update a warning notice only when user action is
  needed
- successful retry of the same operation intent resolves matching active failure
  notices for that dedupe scope

Stale operation completions must not resurrect obsolete failures. When two
operation records target the same dedupe scope, the daemon compares source
timestamps and operation generation before applying notice lifecycle changes.
Older terminal operation events are ignored if a newer successful operation has
already resolved the same notice scope.

## Recovery Actions

Actions are typed descriptors attached to notices. Each action has:

- `action_id`
- `kind`
- display label
- enabled/disabled state with reason
- confirmation requirement when it can mutate project state
- input requirements, if any
- target scope

Action kinds are split by authority:

- Daemon actions: retry operation, cancel operation, dismiss, dismiss all, mark
  read, mark all read, resolve, refresh snapshot, run recovery command.
- Client-local actions: open task/workspace, open logs view from already-fetched
  operation data, copy details.

The action center sends daemon actions through typed notice action commands. The
daemon validates current notice state, source operation state, resource
availability, and dedupe policy before executing. A rejected action updates or
creates an action-result notice with the typed cause.

## Store And Retention

The notice store should live beside other project-scoped daemon SQLite data and
must be isolated by `project_id`.

Minimum indexes:

- `(project_id, state, updated_at DESC)`
- `(project_id, read, state, updated_at DESC)`
- `(project_id, dedupe_key)` for active dedupe lookup
- `(project_id, operation_id)` for operation-linked notice lookup
- `(project_id, scope_type, scope_id, updated_at DESC)` for task/workspace
  projection

Retention is explicit per record:

- active notices are not eligible for cleanup
- terminal notices are eligible only after `expires_at`
- error and recovery notices must be retained at least as long as the linked
  operation record
- cleanup must preserve the newest records per project when count caps are used
- cleanup publishes deletion/expiry events or advances snapshot revision so
  clients can converge

The durable store must support multiple TUI clients. Concurrent read/dismiss
mutations use daemon transactions, publish one ordered revision stream, and are
safe to replay.

## Protocol Shape

The protocol should add notice-specific commands and events rather than encoding
notice changes as UI messages.

Commands:

- `notice.list`: list project notices by state, read flag, severity, category,
  scope, operation ID, updated-after cursor, and limit
- `notice.get`: fetch one notice by ID
- `notice.update`: mark read/unread, dismiss, resolve, or restore when allowed
- `notice.action`: execute a typed action by notice ID and action ID

Events:

- `notice.created`
- `notice.updated`
- `notice.expired`
- `notice.deleted`

Snapshots:

- project/task snapshots include enough notice projection data for the TUI to
  render attention markers without issuing per-card queries
- full notice lists remain queryable through `notice.list`

All notice events carry project ID, revision, notice ID, lifecycle state, and
enough changed fields for idempotent client application. Gap handling follows
the existing snapshot fallback pattern used by daemon stream consumers.

## TUI Projection

The TUI derives all async feedback surfaces from the notice/operation projection:

- task card marker: active notice scoped to the task/operation, severity, and
  operation state
- workspace/detail: selected task notices plus linked operation/cause/action
  detail
- floating stack: newly created or reactivated unread notices matching transient
  display policy
- history/action center: notice list sorted newest-first with filters and
  actions
- footer: compact active unread counts and route hints only

The TUI may keep ephemeral animation state, selection, scroll position, and
already-rendered toast timing locally. It must refresh from daemon projection on
restart and after stream gaps.

During migration, the existing local `notificationHistoryEntry` fields map to
daemon notice fields as follows:

| V1 field | Daemon notice field |
| --- | --- |
| local `ID` | `notice_id` |
| `CreatedAt` | `created_at` and `first_occurrence_at` |
| `Level` | `severity` |
| regex `Reference` | typed `scope` and optional `source` |
| `Message` | `summary` and optional `detail` |
| `Read` | `read` |
| `Dismissed` | `state=dismissed` |

The migration should keep a compatibility adapter only as a temporary bridge
inside TUI projection code. New behavior should consume daemon notices directly.

## Validation Matrix

Closure for the daemon-owned notice architecture requires tests or lint for
these cases:

| Risk | Required assertion |
| --- | --- |
| Daemon restart loses notices | Restart daemon and verify active/read/dismissed notice state reloads from SQLite. |
| TUI restart loses history | Restart TUI/client projection and verify notice history and footer counts rehydrate from daemon state. |
| Multiple TUI clients drift | Mark read/dismiss in one client and verify another client converges via stream event or snapshot fallback. |
| Stale operation failure resurrects | Complete an older failed operation after a newer success and verify the resolved notice is not reactivated. |
| Failure-then-success reconciliation drifts | Failed operation creates an active error notice; successful retry resolves it and clears task markers. |
| Notice flood overwhelms UI | Repeated equivalent failures coalesce by dedupe key and increment occurrence count instead of creating unbounded rows. |
| Narrow viewport regresses | Default and narrow notification/action center views remain within overlay sizing bounds. |
| Boundary drift returns | Guards fail if TUI/CLI writes durable notice lifecycle state directly or imports daemon store internals. |

## Migration Plan

1. Specify and link requirements (`ctw`).
2. Add notice protocol structs, commands, events, store migrations, and store
   tests (`ctx`).
3. Emit notices from daemon mutation and operation paths while preserving the
   existing operation APIs (`cty`).
4. Rewire TUI card/workspace/toast/history/footer surfaces to consume daemon
   notice projection, with a temporary adapter for current V1 fields (`ctz`).
5. Expand notification history into the action center using typed daemon actions
   for lifecycle and recovery mutations (`cua`).
6. Add restart, multi-client, stale-operation, flood/dedupe, viewport, and
   boundary validation (`cub`).
7. Remove the V1 TUI-local durable-history substitute once daemon projection is
   authoritative.

## Non-Goals

- Moving Bubble Tea rendering into the daemon.
- Replacing operation records or operation logs with notices.
- Making footer text carry full async feedback messages again.
- Implementing broad runtime code in the specification slice.
