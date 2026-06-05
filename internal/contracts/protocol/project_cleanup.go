package protocol

import "github.com/riordanpawley/azedarach/internal/naming"

// CommandProjectCleanup asks the daemon to execute project maintenance cleanup categories.
const CommandProjectCleanup = "project.cleanup"

// ProjectCleanupRequestBody identifies cleanup categories to run for a project.
type ProjectCleanupRequestBody struct {
	ProjectID  naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Categories []string         `json:"categories" msgpack:"categories"`
}

// ProjectCleanupResponseBody summarizes daemon-owned project cleanup results.
type ProjectCleanupResponseBody struct {
	ProjectID        naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Deleted          int              `json:"deleted" msgpack:"deleted"`
	Archived         int              `json:"archived" msgpack:"archived"`
	WorktreesRemoved int              `json:"worktrees_removed" msgpack:"worktrees_removed"`
	SessionsCleaned  int              `json:"sessions_cleaned" msgpack:"sessions_cleaned"`
}
