# Factored Issue State and Managed Runtime Rollout

## Scope

Issue state-model v2 separates durable lifecycle authority from the board and
CLI/TUI phases derived from that authority. The rollout is implemented through
the issue domain model, SQLite startup migration, issue-store read/write paths,
daemon readiness policy, and CLI/TUI display filters.

The current corrective requirement is `dec-lifecycle-runtime-state-product`.

## Durable State

The canonical domain authority is the product of three small enums:

- disposition: `backlog`, `ready`, `completed`, or `cancelled`;
- engagement: `idle`, `working`, or `review_requested`, and non-ready
  dispositions are constrained to idle;
- visibility: `live` or `archived`.

`open` is derived from `ready + idle`; it is not durable lifecycle authority.
Issue deletion is not part of this state product. Dependency-edge tombstones
remain separate relationship authority.

SQLite persists the canonical disposition/engagement/visibility product above.
The following columns are one-way, trigger-checked compatibility projections:

- `lifecycle_state`: `backlog`, `open`, `active`, or `closed`.
- `review_state`: `none` or `requested`; review is valid only for active
  workflow.
- `closed_outcome`: `none`, `completed`, or `cancelled`; closed workflow must
  have a non-`none` outcome.
- `archived_at`: archive transition timestamp; `visibility` is authority.
- dependency `tombstoned_at`: dependency-edge deletion authority.

Legacy `status`, `lifecycle_state`, `review_state`, and `closed_outcome` remain
compatibility projections for callers and existing integrations. Issue
`deleted_at` is constrained null; issue deletion is physical and dependency-edge
tombstones remain separate. Active decisions read only canonical fields.

Archive and tombstone semantics are intentionally separate:

- Archive hides/restores an issue through `visibility`; `archived_at` records time.
- Tombstone/delete removes or deactivates records and relationships through the
  relevant tombstone/delete path.
- Workflow transitions must not use archive or tombstone state as a substitute
  for lifecycle state.

## Derived Phases

Board, list, search, and status-filter surfaces derive presentation phases from
durable issue state plus runtime projection where review readiness depends on
session activity:

| Derived phase | Source |
| --- | --- |
| `backlog` | disposition `backlog`, engagement `idle` |
| `open` | disposition `ready`, engagement `idle` |
| `active` | disposition `ready`, engagement `working` |
| `review` | disposition `ready`, engagement `review_requested`, and review-ready runtime activity |
| `done` | disposition `completed` |
| `cancelled` | disposition `cancelled` |

Review handoff is a derived readiness phase, not a durable workflow. An issue
with `review_state=requested` remains operationally active while its session
activity is busy, waiting, starting, or otherwise not review-ready. Daemon
observation paths should surface review-ready only after the session projection
shows idle, done, no-agent, or equivalent complete activity.

## Startup Migration

Startup migration `0029_issue_state_model_v2` upgrades existing issue databases
to the v2 columns. Before changing schema, the migration creates a SQLite backup
with `VACUUM INTO` and writes an `issue:state_model_v2_cutover` metadata marker.

The marker states are:

- `in_progress`: startup was changing the schema and must not be retried as a
  normal repair.
- `failed`: startup rolled back and recorded backup/error details.
- `complete`: v2 rows were validated and `issue:state_model_version=2` is set.

If startup sees a partial or failed cutover marker, it refuses to continue before
generic issue-schema repair. Restore the recorded backup first, then retry with a
known-good database. Do not delete the marker to force startup; that bypasses the
rollback contract and can let later migrations run against a partially upgraded
schema.

Migration `0045` installs canonical state-product and resource guards, migrates
unambiguous ownership into typed coordination leases, and rewrites runtime
aggregate guards to read disposition and visibility. Legacy v2 columns are no
longer decision authority.

## Invariant Sources

- `session.issue_lifecycle_runtime` is `hybrid`: refresh factored issue state,
  then compare it with live tmux. A live runtime repairs `ready + idle` to
  `ready + working`. Backlog, terminal, or archived divergence returns an
  explicit invariant error and preserves the runtime for safe reconciliation.

State-model v2 does not change the daemon invariant source categories, but it
changes the durable projection fields those invariants must read:

- `task.close`, `task.close_preflight`, `task.delete`,
  `task.delete_preflight`, `task.graph_readiness`, and `task.complete_check`
  remain `hybrid`: refresh durable issue graph/state projection first, then
  compare it with live tmux/runtime attachment state.
- `task.review_handoff` remains `projection`: refresh durable issue state,
  revisioned material-decision change/acknowledgement observations, and session
  activity projections, then allow review only when decision revisions are
  current and active-issue self-handoff/external busy-equivalent gates pass.
- `task.integration_readiness`, `task.context_risk_closeout`,
  `task.merge_base_target`, `task.follow_on_merge_candidates`,
  `issue_resources.lifecycle`, and task-list freshness remain `projection`.

When reviewing or adding issue lifecycle logic, use the domain mapper in
`internal/domain/issue_state.go` and the issue-store helpers that hydrate from
v2 columns. SQL can select candidates, but final lifecycle semantics must stay
aligned with the domain model.

## Validation Checklist

Minimum rollout validation should include:

1. Domain mapper and transition tests for lifecycle, review, close outcomes,
   archive, and tombstone separation.
2. Migration tests for backup creation, injected failure rollback, partial
   marker refusal, and v2 row validation.
3. Issue-store tests proving reads and writes use v2 authority while preserving
   legacy compatibility mirrors.
4. Daemon readiness tests proving busy or waiting review handoffs are still
   active and idle/no-agent handoffs become review-ready.
5. CLI/TUI tests proving list/search/get filters and board columns use derived
   display phases.
6. Broad build/test/boundary gates after focused checks.
