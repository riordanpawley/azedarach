# Rootless Orchestrator Contracts

Orchestration scope is a typed domain value. Resolution applies this precedence:

1. an explicit `--root` selects rooted scope;
2. otherwise `AZEDARACH_ISSUE_ID` selects rooted scope;
3. otherwise the whole project is selected.

Flags and environment are startup inputs only. Durable orchestrator identity is
the project ID plus the resolved typed scope, so project and rooted singletons
cannot collide.

Project lifecycle has four states. `working` means executable work or runtime
activity remains. `quiescent` means no executable work is active; unresolved
human interactions are allowed and prevent completion. `complete-grace` means
all live issues are closed or backlog and there are no active sessions, review
requests, open/active issues, or unresolved interactions. After the configurable
`orchestration.completeGrace`, the singleton becomes `paused`, not destroyed.
Wake events are new open work, review requests, accepted human answers, and
recovery events. They are idempotent and coalesced by
`orchestration.wakeDebounce`.

The exact-scope lease persists `complete_since`, `last_wake_at`, and the last
wake reason. Completion changes clear and restart `complete_since`; daemon
restart therefore cannot shorten or extend grace. Wake updates are serialized
under the SQLite project write lock, so duplicate events from multiple daemons
are suppressed by the durable debounce timestamp.

`orchestration.scope_identity` uses refreshed durable projection state.
`orchestration.scope_singleton` is hybrid: refresh the durable exact-scope
lease, then compare its session identity with live tmux runtime.
`orchestration.project_completion` is hybrid: refresh durable issue, review,
interaction, orchestration, and session projections, then compare runtime
presence with live tmux. `runtime.reconcile` exposes both mappings.

## Multi-daemon stale-cache test plan

The runtime implementation must start two daemon instances over one durable
projection. Seed daemon A's cache with a complete project, mutate an issue,
review, interaction, or session projection through daemon B, and evaluate on A.
Each table case must prove A refreshes before evaluation and does not report
complete from stale memory. Hybrid cases additionally create or remove live tmux
state independently of the projection and prove neither source is used as a
fallback for the other. Repeat for wake-after-pause and grace-reset changes to
prove idempotent wake and timer reset behavior across processes.
