package protocol

// CommandWorktreeCleanupOrphaned is the daemon command used to remove orphaned worktrees.
const CommandWorktreeCleanupOrphaned = "worktree.cleanup_orphaned"

// CleanupOrphanedRequestBody is the command payload for orphaned worktree cleanup.
type CleanupOrphanedRequestBody struct {
	ProjectID string `json:"project_id" msgpack:"project_id"`
}

// CleanupOrphanedResponseBody is the response payload for orphaned worktree cleanup.
type CleanupOrphanedResponseBody struct {
	ProjectID        string `json:"project_id" msgpack:"project_id"`
	WorktreesRemoved int    `json:"worktrees_removed" msgpack:"worktrees_removed"`
}
