package protocol

const CommandDaemonWatchClients = "daemon.watch_clients"

type DaemonWatchClientsCommandBody struct {
	IncludeExpired bool `json:"include_expired,omitempty" msgpack:"include_expired,omitempty"`
}

type DaemonWatchClientsResult struct {
	GeneratedAtUTC      string        `json:"generated_at_utc" msgpack:"generated_at_utc"`
	ActiveWindowSeconds int64         `json:"active_window_seconds" msgpack:"active_window_seconds"`
	Clients             []WatchClient `json:"clients" msgpack:"clients"`
}

type WatchClient struct {
	ClientInvocationID string `json:"client_invocation_id,omitempty" msgpack:"client_invocation_id,omitempty"`
	ClientPID          int    `json:"client_pid" msgpack:"client_pid"`
	ClientPPID         int    `json:"client_ppid" msgpack:"client_ppid"`
	ProjectID          string `json:"project_id" msgpack:"project_id"`
	CommandShape       string `json:"command_shape" msgpack:"command_shape"`
	LastCommand        string `json:"last_command" msgpack:"last_command"`
	ClientCWD          string `json:"client_cwd,omitempty" msgpack:"client_cwd,omitempty"`
	ClientActiveIssue  string `json:"client_active_issue,omitempty" msgpack:"client_active_issue,omitempty"`
	FirstSeenUTC       string `json:"first_seen_utc" msgpack:"first_seen_utc"`
	LastSeenUTC        string `json:"last_seen_utc" msgpack:"last_seen_utc"`
	AgeSeconds         int64  `json:"age_seconds" msgpack:"age_seconds"`
	IdleSeconds        int64  `json:"idle_seconds" msgpack:"idle_seconds"`
	SeenCount          int64  `json:"seen_count" msgpack:"seen_count"`
	Active             bool   `json:"active" msgpack:"active"`
	OrphanCandidate    bool   `json:"orphan_candidate" msgpack:"orphan_candidate"`
}
