package protocol

const (
	CommandRuntimeSignalIngest = "runtime.signal.ingest"

	RuntimeSignalSourceGitHook   = "git_hook"
	RuntimeSignalSourceAgentHook = "agent_hook"

	RuntimeSignalKindGitWorktreeChanged   = "git_worktree_changed"
	RuntimeSignalKindAgentActivityChanged = "agent_activity_changed"
)

// RuntimeSignalIngestCommandBody reports facts observed from a physical runtime.
// SessionID identifies the tmux runtime, not one desired logical worker,
// advisor, or orchestrator intent. The daemon fans observations into every
// linked intent; commands that mutate desired intent remain role/scope typed.
type RuntimeSignalIngestCommandBody struct {
	Source           string         `json:"source" msgpack:"source"`
	Kind             string         `json:"kind" msgpack:"kind"`
	ProjectID        string         `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID          string         `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	SessionID        string         `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	Worktree         string         `json:"worktree,omitempty" msgpack:"worktree,omitempty"`
	TmuxPane         string         `json:"tmux_pane,omitempty" msgpack:"tmux_pane,omitempty"`
	LogicalPaneID    string         `json:"logical_pane_id,omitempty" msgpack:"logical_pane_id,omitempty"`
	PanePID          int            `json:"pane_pid,omitempty" msgpack:"pane_pid,omitempty"`
	AgentIncarnation string         `json:"agent_incarnation,omitempty" msgpack:"agent_incarnation,omitempty"`
	Agent            string         `json:"agent,omitempty" msgpack:"agent,omitempty"`
	Hook             string         `json:"hook,omitempty" msgpack:"hook,omitempty"`
	Command          string         `json:"command,omitempty" msgpack:"command,omitempty"`
	Event            string         `json:"event,omitempty" msgpack:"event,omitempty"`
	Activity         string         `json:"activity,omitempty" msgpack:"activity,omitempty"`
	Level            string         `json:"level,omitempty" msgpack:"level,omitempty"`
	Message          string         `json:"message,omitempty" msgpack:"message,omitempty"`
	Log              bool           `json:"log,omitempty" msgpack:"log,omitempty"`
	ElapsedMS        int64          `json:"elapsed_ms,omitempty" msgpack:"elapsed_ms,omitempty"`
	ExitStatus       *int           `json:"exit_status,omitempty" msgpack:"exit_status,omitempty"`
	Blocking         *bool          `json:"blocking,omitempty" msgpack:"blocking,omitempty"`
	Payload          map[string]any `json:"payload,omitempty" msgpack:"payload,omitempty"`
}

type RuntimeSignalIngestResponseBody struct {
	Accepted            bool                        `json:"accepted" msgpack:"accepted"`
	SignalID            string                      `json:"signal_id" msgpack:"signal_id"`
	ProjectionRevisions []uint64                    `json:"projection_revisions,omitempty" msgpack:"projection_revisions,omitempty"`
	EnrichmentQueued    bool                        `json:"enrichment_queued,omitempty" msgpack:"enrichment_queued,omitempty"`
	Stages              []RuntimeSignalStageOutcome `json:"stages,omitempty" msgpack:"stages,omitempty"`
}

type RuntimeSignalStageOutcome struct {
	Name     string `json:"name" msgpack:"name"`
	OK       bool   `json:"ok" msgpack:"ok"`
	Revision uint64 `json:"revision,omitempty" msgpack:"revision,omitempty"`
	Message  string `json:"message,omitempty" msgpack:"message,omitempty"`
}
