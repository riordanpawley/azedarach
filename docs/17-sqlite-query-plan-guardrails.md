# SQLite Query-Plan Guardrails

## Purpose

SQLite is the daemon's durable projection and issue-store backend. Hot reads must keep their query shape explicit enough that a schema or SQL refactor cannot silently turn a focused projection read into broad domain hydration or a full table scan.

## Guarded Hot Reads

| Read path | Expected access path |
| --- | --- |
| Issue content search candidate IDs | `issue_search_fts` virtual table, then `issues` rowid lookup |
| Runtime projection hydration by issue IDs | `idx_daemon_session_projections_project_issue`, `idx_daemon_session_observations_project_issue`, `daemon_worktree_projections` primary key, and `idx_issue_external_refs_issue_active` |
| Board/runtime summary snapshots | active issue ordering through `idx_issues_deleted_updated`, plus project-scoped runtime projection indexes |
| Dependency context expansion | `idx_dependencies_issue_active_type` for dependencies and `idx_dependencies_depends_on_active_type` for dependents |
| Graph readiness context | `idx_issue_graph_closure_ancestor` and `idx_dependencies_issue_active_type` |
| Parent ancestor lookup | `idx_issue_graph_closure_descendant` |
| Metadata runtime projection | runtime projection indexes plus `idx_dependencies_issue_active_type` for parent lookup |

Keep this inventory aligned with `TestSQLiteHotQueryPlansUseExpectedIndexes` in `internal/services/issues/client_test.go`.

## Test Rules

- For a new hot SQLite read, add or extend an `EXPLAIN QUERY PLAN` assertion before depending on it from daemon, CLI, or TUI active paths.
- Assert named indexes or virtual table access that represent the intended access path.
- Also assert against accidental scans when the table is expected to be reached through an index.
- Prefer testing production query-builder functions. If a query is inline and becomes hot, extract a small unexported builder rather than duplicating SQL in the test.
- Keep assertions on stable plan details such as index names. Avoid overfitting to node IDs or full planner output ordering.

## Projection Policy

Add or extend a projection/read model instead of hydrating full domain tasks when any of these are true:

- The caller needs only IDs, status, parentage, runtime attachment, or timestamps.
- The read runs repeatedly from board refresh, orchestration readiness, daemon reconciliation, or mailbox/session polling.
- The result is filtered to a root graph, a known set of issue IDs, or a project runtime projection.
- Loading long-form fields such as description, design, notes, or acceptance is not required for the decision being made.
- The query needs graph or runtime facts that already have durable projections.

Hydrating full `domain.Task` bodies is appropriate for user-facing detail views, edit flows, or final responses where the long-form fields are actually displayed or mutated.

## Logging

Hot issue-store reads emit structured debug logs with:

- `event=sqlite.query.completed`
- `service=azedarach.issue_store`
- `dependency.name=sqlite`
- `dependency.operation`
- `dependency.duration_ms`
- `outcome`
- `row_count`

Failures use warning level with `error_class=sqlite_query`. Do not log raw SQL values or long-form issue content.
