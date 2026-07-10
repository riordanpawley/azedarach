package protocol

const (
	CommandAIAccountBackup   = "ai.account.backup"
	CommandAIAccountList     = "ai.account.list"
	CommandAIAccountStatus   = "ai.account.status"
	CommandAIAccountActivate = "ai.account.activate"
	CommandAIAccountDelete   = "ai.account.delete"
)

type AIAccountProvider string

const (
	AIAccountProviderClaude AIAccountProvider = "claude"
	AIAccountProviderCodex  AIAccountProvider = "codex"
)

func (p AIAccountProvider) Valid() bool {
	return p == AIAccountProviderClaude || p == AIAccountProviderCodex
}

type AIAccountProfile struct {
	Provider AIAccountProvider `json:"provider" msgpack:"provider"`
	Name     string            `json:"name" msgpack:"name"`
	Active   bool              `json:"active" msgpack:"active"`
	System   bool              `json:"system,omitempty" msgpack:"system,omitempty"`
}

type AIAccountProviderStatus struct {
	Provider      AIAccountProvider `json:"provider" msgpack:"provider"`
	Authenticated bool              `json:"authenticated" msgpack:"authenticated"`
	ActiveProfile string            `json:"active_profile,omitempty" msgpack:"active_profile,omitempty"`
}

type AIAccountBackupRequestBody struct {
	Provider AIAccountProvider `json:"provider" msgpack:"provider"`
	Name     string            `json:"name" msgpack:"name"`
	Force    bool              `json:"force,omitempty" msgpack:"force,omitempty"`
}

type AIAccountBackupResponseBody struct {
	Profile AIAccountProfile `json:"profile" msgpack:"profile"`
}

type AIAccountListRequestBody struct {
	Provider AIAccountProvider `json:"provider,omitempty" msgpack:"provider,omitempty"`
}

type AIAccountListResponseBody struct {
	Profiles []AIAccountProfile `json:"profiles" msgpack:"profiles"`
}

type AIAccountStatusRequestBody struct {
	Provider AIAccountProvider `json:"provider,omitempty" msgpack:"provider,omitempty"`
}

type AIAccountStatusResponseBody struct {
	Providers []AIAccountProviderStatus `json:"providers" msgpack:"providers"`
}

type AIAccountActivateRequestBody struct {
	Provider          AIAccountProvider `json:"provider" msgpack:"provider"`
	Name              string            `json:"name" msgpack:"name"`
	ReloadCodexDaemon bool              `json:"reload_daemon,omitempty" msgpack:"reload_daemon,omitempty"`
}

type AIAccountCodexDaemonReload struct {
	Supported         bool     `json:"supported" msgpack:"supported"`
	NativeDaemon      bool     `json:"native_daemon,omitempty" msgpack:"native_daemon,omitempty"`
	NativeRestarted   bool     `json:"native_restarted,omitempty" msgpack:"native_restarted,omitempty"`
	InspectionFailed  bool     `json:"inspection_failed,omitempty" msgpack:"inspection_failed,omitempty"`
	DetectedPIDs      []int    `json:"detected_pids,omitempty" msgpack:"detected_pids,omitempty"`
	ReloadedPIDs      []int    `json:"reloaded_pids,omitempty" msgpack:"reloaded_pids,omitempty"`
	FailedPIDs        []int    `json:"failed_pids,omitempty" msgpack:"failed_pids,omitempty"`
	UnattributedCount int      `json:"unattributed_count,omitempty" msgpack:"unattributed_count,omitempty"`
	Subcommands       []string `json:"subcommands,omitempty" msgpack:"subcommands,omitempty"`
}

type AIAccountActivateResponseBody struct {
	Profile                  AIAccountProfile            `json:"profile" msgpack:"profile"`
	RestartExistingProcesses bool                        `json:"restart_existing_processes" msgpack:"restart_existing_processes"`
	SafetyBackupProfile      string                      `json:"safety_backup_profile,omitempty" msgpack:"safety_backup_profile,omitempty"`
	OutgoingResnapshotted    string                      `json:"outgoing_resnapshotted,omitempty" msgpack:"outgoing_resnapshotted,omitempty"`
	FreshLivePreserved       bool                        `json:"fresh_live_preserved,omitempty" msgpack:"fresh_live_preserved,omitempty"`
	CodexDaemonReload        *AIAccountCodexDaemonReload `json:"codex_daemon_reload,omitempty" msgpack:"codex_daemon_reload,omitempty"`
}

type AIAccountDeleteRequestBody struct {
	Provider AIAccountProvider `json:"provider" msgpack:"provider"`
	Name     string            `json:"name" msgpack:"name"`
	Confirm  bool              `json:"confirm" msgpack:"confirm"`
}

type AIAccountDeleteResponseBody struct {
	Provider AIAccountProvider `json:"provider" msgpack:"provider"`
	Name     string            `json:"name" msgpack:"name"`
	Deleted  bool              `json:"deleted" msgpack:"deleted"`
}
