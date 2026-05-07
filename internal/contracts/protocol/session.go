package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	EventSessionUpdated = "session.updated"
)

const (
	CommandSessionResolveConflict = "session.resolve_conflict"
)

type SessionLifecycleState string

const (
	SessionLifecycleStateStarting SessionLifecycleState = "starting"
	SessionLifecycleStateAttached SessionLifecycleState = "attached"
	SessionLifecycleStatePaused   SessionLifecycleState = "paused"
	SessionLifecycleStateStopped  SessionLifecycleState = "stopped"
)

type SessionProjection struct {
	SessionID naming.SessionID      `json:"session_id" msgpack:"session_id"`
	IssueID   naming.IssueID        `json:"issue_id" msgpack:"issue_id"`
	State     SessionLifecycleState `json:"state" msgpack:"state"`
	UpdatedAt time.Time             `json:"updated_at" msgpack:"updated_at"`
}

type SessionProjectionEventBody struct {
	ProjectID naming.ProjectID            `json:"project_id" msgpack:"project_id"`
	Revision  uint64                      `json:"revision" msgpack:"revision"`
	Session   SessionProjection           `json:"session" msgpack:"session"`
	Runtime   *RuntimeProjectionEventBody `json:"runtime,omitempty" msgpack:"runtime,omitempty"`
}

type SessionResolveConflictRequestBody struct {
	ProjectID     naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID       naming.IssueID   `json:"issue_id" msgpack:"issue_id"`
	Worktree      string           `json:"worktree,omitempty" msgpack:"worktree,omitempty"`
	ConflictFiles []string         `json:"conflict_files,omitempty" msgpack:"conflict_files,omitempty"`
	Yolo          bool             `json:"yolo,omitempty" msgpack:"yolo,omitempty"`
	ImagePaths    []string         `json:"image_paths,omitempty" msgpack:"image_paths,omitempty"`
	Prompt        string           `json:"prompt,omitempty" msgpack:"prompt,omitempty"`
}

type SessionResolveConflictResponseBody struct {
	ProjectID     naming.ProjectID `json:"project_id" msgpack:"project_id"`
	IssueID       naming.IssueID   `json:"issue_id" msgpack:"issue_id"`
	SessionID     naming.SessionID `json:"session_id" msgpack:"session_id"`
	Worktree      string           `json:"worktree" msgpack:"worktree"`
	WindowName    string           `json:"window_name" msgpack:"window_name"`
	ConflictFiles []string         `json:"conflict_files,omitempty" msgpack:"conflict_files,omitempty"`
	ReusedSession bool             `json:"reused_session" msgpack:"reused_session"`
	ReusedWindow  bool             `json:"reused_window" msgpack:"reused_window"`
}
