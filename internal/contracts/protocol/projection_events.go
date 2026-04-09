package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	EventWorktreeProjectionUpdated = "worktree.projection.updated"
	EventGitStatusUpdated          = "git.status.updated"
)

type ProjectionUpdateEventBody struct {
	ProjectID naming.ProjectID            `json:"project_id" msgpack:"project_id"`
	IssueID   naming.IssueID              `json:"issue_id" msgpack:"issue_id"`
	Worktree  string                      `json:"worktree,omitempty" msgpack:"worktree,omitempty"`
	UpdatedAt time.Time                   `json:"updated_at" msgpack:"updated_at"`
	Runtime   *RuntimeProjectionEventBody `json:"runtime,omitempty" msgpack:"runtime,omitempty"`
}
