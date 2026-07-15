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
	CommandSessionRestartAll      = "session.restart_all"
	CommandSessionCapture         = "session.capture"
)

type SessionLifecycleState string
type SessionRole string
type SessionScopeKind string

const (
	SessionRoleWorker         SessionRole      = "worker"
	SessionRoleOrchestrator   SessionRole      = "orchestrator"
	SessionRoleAdvisor        SessionRole      = "advisor"
	SessionScopeIssue         SessionScopeKind = "issue"
	SessionScopeOrchestration SessionScopeKind = "orchestration"
	SessionScopeInteraction   SessionScopeKind = "interaction"
)

const (
	SessionLifecycleStateStarting SessionLifecycleState = "starting"
	SessionLifecycleStateRunning  SessionLifecycleState = "running"
	SessionLifecycleStateStopping SessionLifecycleState = "stopping"
	// SessionLifecycleStateAttached is a deprecated compatibility alias. Use
	// SessionLifecycleStateRunning for emitted protocol payloads.
	SessionLifecycleStateAttached SessionLifecycleState = SessionLifecycleStateRunning
	SessionLifecycleStatePaused   SessionLifecycleState = "paused"
	SessionLifecycleStateStopped  SessionLifecycleState = "stopped"
)

type SessionProjection struct {
	SessionID naming.SessionID      `json:"session_id" msgpack:"session_id"`
	IssueID   naming.IssueID        `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	Role      SessionRole           `json:"role,omitempty" msgpack:"role,omitempty"`
	ScopeKind SessionScopeKind      `json:"scope_kind,omitempty" msgpack:"scope_kind,omitempty"`
	ScopeID   string                `json:"scope_id,omitempty" msgpack:"scope_id,omitempty"`
	State     SessionLifecycleState `json:"state" msgpack:"state"`
	UpdatedAt time.Time             `json:"updated_at" msgpack:"updated_at"`
}

// ManagedAgentIdentity is the wire representation of the exact managed pane
// and process incarnation. LogicalPaneID remains stable across restarts;
// TmuxPaneID, PanePID, and AgentIncarnation fence stale observations.
type ManagedAgentIdentity struct {
	LogicalPaneID    string `json:"logical_pane_id" msgpack:"logical_pane_id"`
	TmuxPaneID       string `json:"tmux_pane_id" msgpack:"tmux_pane_id"`
	PanePID          int    `json:"pane_pid" msgpack:"pane_pid"`
	AgentIncarnation string `json:"agent_incarnation" msgpack:"agent_incarnation"`
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

type SessionRestartAllRequestBody struct {
	ProjectID  naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	ForceBusy  bool             `json:"force_busy,omitempty" msgpack:"force_busy,omitempty"`
	Yolo       bool             `json:"yolo,omitempty" msgpack:"yolo,omitempty"`
	ImagePaths []string         `json:"image_paths,omitempty" msgpack:"image_paths,omitempty"`
}

type SessionRestartAllItem struct {
	ProjectID      naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID        naming.IssueID   `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	SessionID      naming.SessionID `json:"session_id" msgpack:"session_id"`
	Activity       string           `json:"activity" msgpack:"activity"`
	ActivitySource string           `json:"activity_source,omitempty" msgpack:"activity_source,omitempty"`
	TmuxReady      bool             `json:"tmux_ready" msgpack:"tmux_ready"`
	ActiveIntent   bool             `json:"active_intent" msgpack:"active_intent"`
	Restarted      bool             `json:"restarted" msgpack:"restarted"`
	Skipped        bool             `json:"skipped,omitempty" msgpack:"skipped,omitempty"`
	Reason         string           `json:"reason,omitempty" msgpack:"reason,omitempty"`
	Error          string           `json:"error,omitempty" msgpack:"error,omitempty"`
}

type SessionRestartAllResponseBody struct {
	ProjectID  naming.ProjectID        `json:"project_id" msgpack:"project_id"`
	ProjectIDs []naming.ProjectID      `json:"project_ids,omitempty" msgpack:"project_ids,omitempty"`
	ForceBusy  bool                    `json:"force_busy" msgpack:"force_busy"`
	Restarted  int                     `json:"restarted" msgpack:"restarted"`
	Skipped    int                     `json:"skipped" msgpack:"skipped"`
	Failed     int                     `json:"failed" msgpack:"failed"`
	Sessions   []SessionRestartAllItem `json:"sessions" msgpack:"sessions"`
}

type SessionCaptureRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID   naming.IssueID   `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	SessionID naming.SessionID `json:"session_id" msgpack:"session_id"`
	Lines     int              `json:"lines,omitempty" msgpack:"lines,omitempty"`
}

type SessionCaptureResponseBody struct {
	ProjectID  naming.ProjectID `json:"project_id" msgpack:"project_id"`
	IssueID    naming.IssueID   `json:"issue_id" msgpack:"issue_id"`
	SessionID  naming.SessionID `json:"session_id" msgpack:"session_id"`
	Lines      int              `json:"lines" msgpack:"lines"`
	Output     string           `json:"output" msgpack:"output"`
	CapturedAt time.Time        `json:"captured_at" msgpack:"captured_at"`
}
