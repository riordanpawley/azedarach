# Cross-project user database

The global daemon owns one user-level SQLite database at
`~/.azedarach/azedarach.db`. Its path is resolved explicitly by
`config.UserDBPath`; repository discovery must never derive it. Worktree-scoped
daemons do not open this database.

Each registered project's `<canonical-root>/.azedarach/azedarach.db` remains
authoritative for project-local lifecycle and business state. The user database
contains a project-scoped read model for configurable views. Refresh never
holds write transactions in both databases and never writes authority state
back to a project database.

## Projection contract

- `projects` records canonical identity, paths, versions, checkpoints,
  freshness, registration, and errors.
- `project_issue_projection` materializes issue fields and derived domain facts
  used by typed view predicates and sort rules.
- `project_session_projection` carries durable session metadata; live tmux
  remains authoritative for runtime presence.
- `user_views` and `user_view_selections` store reusable definitions and
  per-consumer defaults.
- Global protocol results use typed `(project_id, issue_id)` identities, so
  colliding issue IDs remain distinct.

SQLite performs candidate filtering compiled from typed view definitions. The
shared domain evaluator verifies placement and ordering, keeping indexed storage
behavior aligned with domain policy.

## Projection delivery and failure behavior

Startup reconciles the registry, bootstraps only uninitialized project vector
components, and resumes each initialized project from its durable transitional
delivery cursor. Verified issue deltas update bounded keyed rows and advance that
project's cursor, source vector, semantic hash, and projector identity in one
user-database transaction. Independently sourced runtime, ownership,
interaction, and worktree materializations update bounded keyed rows without
advancing or being ordered by the issue delivery cursor.

Routine mutations never schedule dirty-project full replacement. Full export is
reserved for first bootstrap, explicit operator rebuild, and isolated
gap/incompatibility recovery; those paths verify a stable delta head before
publishing. Failed delivery retains the last good rows and vector component and
exposes the project as stale or unavailable.
Removed registrations retain diagnostic catalog health but are excluded from
view items. Routine global views and the tmux selector query only the user
database; project-by-project fan-out and `ATTACH DATABASE` are not active paths.
