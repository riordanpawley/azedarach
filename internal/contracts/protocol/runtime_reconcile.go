package protocol

import "github.com/riordanpawley/azedarach/internal/naming"

// CommandRuntimeReconcile is the daemon command used to repair runtime/session/worktree consistency.
const CommandRuntimeReconcile = "runtime.reconcile"

// CommandRuntimeReconcileIssue is the daemon command used to repair runtime/session/worktree consistency
// for one or more specific issues within a project.
const CommandRuntimeReconcileIssue = "runtime.reconcile_issue"

// RuntimeReconcileRequestBody is the command payload for runtime reconciliation.
type RuntimeReconcileRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
}

// RuntimeReconcileIssueRequestBody scopes runtime reconciliation to specific issues.
type RuntimeReconcileIssueRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	IssueIDs  []naming.IssueID `json:"issue_ids" msgpack:"issue_ids"`
}

// RuntimeReconcileResponseBody is the deterministic response payload for runtime reconciliation.
type RuntimeReconcileResponseBody struct {
	ProjectID             naming.ProjectID  `json:"project_id" msgpack:"project_id"`
	WorktreesRefreshed    int               `json:"worktrees_refreshed" msgpack:"worktrees_refreshed"`
	RecreatedTmuxSessions int               `json:"recreated_tmux_sessions" msgpack:"recreated_tmux_sessions"`
	AlignedDaemonSessions int               `json:"aligned_daemon_sessions" msgpack:"aligned_daemon_sessions"`
	InvariantSources      map[string]string `json:"invariant_sources,omitempty" msgpack:"invariant_sources,omitempty"`
}
