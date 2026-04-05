package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// RuntimeProjectionSchemaVersion identifies the runtime projection payload schema contract.
const RuntimeProjectionSchemaVersion uint16 = 1

// RuntimeProjectionSnapshotPayload is the deterministic snapshot contract for board/workspace runtime state.
//
// The field order is part of the contract: schema_version -> protocol_version -> snapshot_revision -> project_id -> projections.
type RuntimeProjectionSnapshotPayload struct {
	SchemaVersion    uint16              `json:"schema_version" msgpack:"schema_version"`
	ProtocolVersion  Version             `json:"protocol_version" msgpack:"protocol_version"`
	SnapshotRevision uint64              `json:"snapshot_revision" msgpack:"snapshot_revision"`
	ProjectID        string              `json:"project_id" msgpack:"project_id"`
	Projections      []RuntimeProjection `json:"projections" msgpack:"projections"`
}

// RuntimeProjectionEventBody is the stream contract for a single runtime projection change.
type RuntimeProjectionEventBody struct {
	ProjectID  string            `json:"project_id" msgpack:"project_id"`
	Revision   uint64            `json:"revision" msgpack:"revision"`
	Projection RuntimeProjection `json:"projection" msgpack:"projection"`
}

// RuntimeProjection captures the daemon-authoritative runtime state rendered on board and workspace surfaces.
type RuntimeProjection struct {
	ProjectID string                    `json:"project_id" msgpack:"project_id"`
	IssueID   naming.IssueID            `json:"issue_id" msgpack:"issue_id"`
	Worktree  RuntimeWorktreeProjection `json:"worktree" msgpack:"worktree"`
	Git       RuntimeGitProjection      `json:"git" msgpack:"git"`
	Session   RuntimeSessionProjection  `json:"session" msgpack:"session"`
	Agent     RuntimeAgentProjection    `json:"agent" msgpack:"agent"`
}

// RuntimeGitProjection captures git status signals used for board badges and workspace summaries.
type RuntimeGitProjection struct {
	HasUncommittedChanges bool                        `json:"has_uncommitted_changes" msgpack:"has_uncommitted_changes"`
	GitAdditions          int                         `json:"git_additions" msgpack:"git_additions"`
	GitDeletions          int                         `json:"git_deletions" msgpack:"git_deletions"`
	GitAheadCount         int                         `json:"git_ahead_count" msgpack:"git_ahead_count"`
	GitBehindCount        int                         `json:"git_behind_count" msgpack:"git_behind_count"`
	ActiveOperation       *RuntimeOperationProjection `json:"active_operation,omitempty" msgpack:"active_operation,omitempty"`
}

// RuntimeOperationProjection captures the active long-running operation metadata rendered in the UI.
type RuntimeOperationProjection struct {
	OperationID     string         `json:"operation_id" msgpack:"operation_id"`
	State           OperationState `json:"state" msgpack:"state"`
	ProgressPercent int            `json:"progress_percent" msgpack:"progress_percent"`
	Message         string         `json:"message,omitempty" msgpack:"message,omitempty"`
}

// RuntimeSessionProjection captures tmux/session lifecycle signals used by the workspace detail panel.
type RuntimeSessionProjection struct {
	HasSession bool                  `json:"has_session" msgpack:"has_session"`
	SessionID  naming.SessionID      `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	State      SessionLifecycleState `json:"state,omitempty" msgpack:"state,omitempty"`
	StartedAt  *time.Time            `json:"started_at,omitempty" msgpack:"started_at,omitempty"`
	UpdatedAt  *time.Time            `json:"updated_at,omitempty" msgpack:"updated_at,omitempty"`
	Worktree   string                `json:"worktree,omitempty" msgpack:"worktree,omitempty"`
}

// RuntimeAgentProjection captures the current agent/runtime status surfaced alongside session state.
type RuntimeAgentProjection struct {
	Status    string           `json:"status,omitempty" msgpack:"status,omitempty"`
	SessionID naming.SessionID `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	UpdatedAt *time.Time       `json:"updated_at,omitempty" msgpack:"updated_at,omitempty"`
}

// RuntimeWorktreeProjection captures the worktree identity and health metadata used by the UI.
type RuntimeWorktreeProjection struct {
	Exists             bool       `json:"exists" msgpack:"exists"`
	Path               string     `json:"path,omitempty" msgpack:"path,omitempty"`
	Branch             string     `json:"branch,omitempty" msgpack:"branch,omitempty"`
	Healthy            bool       `json:"healthy" msgpack:"healthy"`
	GitStatusUpdatedAt *time.Time `json:"git_status_updated_at,omitempty" msgpack:"git_status_updated_at,omitempty"`
}
