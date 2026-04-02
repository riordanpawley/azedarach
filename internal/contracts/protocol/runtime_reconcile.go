package protocol

// CommandRuntimeReconcile is the daemon command used to repair runtime/session/worktree consistency.
const CommandRuntimeReconcile = "runtime.reconcile"

// RuntimeReconcileRequestBody is the command payload for runtime reconciliation.
type RuntimeReconcileRequestBody struct {
	ProjectID string `json:"project_id" msgpack:"project_id"`
}

// RuntimeReconcileResponseBody is the deterministic response payload for runtime reconciliation.
type RuntimeReconcileResponseBody struct {
	ProjectID             string `json:"project_id" msgpack:"project_id"`
	WorktreesRefreshed    int    `json:"worktrees_refreshed" msgpack:"worktrees_refreshed"`
	RecreatedTmuxSessions int    `json:"recreated_tmux_sessions" msgpack:"recreated_tmux_sessions"`
	AlignedDaemonSessions int    `json:"aligned_daemon_sessions" msgpack:"aligned_daemon_sessions"`
}
