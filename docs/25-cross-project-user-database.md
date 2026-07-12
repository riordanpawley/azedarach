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

## Refresh and failure behavior

Startup and explicit rebuild reconcile the registry. Successful project
mutations enqueue a coalesced refresh. Runtime reconcile repairs and reports
projection health. Each refresh reads the project snapshot before atomically
replacing that project's user-database rows and checkpoint.

Failed refreshes retain the last good rows and expose the project as unavailable.
Removed registrations retain diagnostic catalog health but are excluded from
view items. Routine global views and the tmux selector query only the user
database; project-by-project fan-out and `ATTACH DATABASE` are not active paths.
