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
	Provider AIAccountProvider `json:"provider" msgpack:"provider"`
	Name     string            `json:"name" msgpack:"name"`
}

type AIAccountActivateResponseBody struct {
	Profile                  AIAccountProfile `json:"profile" msgpack:"profile"`
	RestartExistingProcesses bool             `json:"restart_existing_processes" msgpack:"restart_existing_processes"`
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
