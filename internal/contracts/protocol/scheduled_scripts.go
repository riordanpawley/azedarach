package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// CommandScheduledScriptsStatus asks the daemon for configured scheduled script status.
const CommandScheduledScriptsStatus = "scheduled_scripts.status"

type ScheduledScriptsStatusRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Names     []string         `json:"names,omitempty" msgpack:"names,omitempty"`
}

type ScheduledScriptsStatusResponseBody struct {
	ProjectID naming.ProjectID        `json:"project_id" msgpack:"project_id"`
	Scripts   []ScheduledScriptStatus `json:"scripts" msgpack:"scripts"`
}

type ScheduledScriptStatus struct {
	Name           string     `json:"name" msgpack:"name"`
	Command        string     `json:"command" msgpack:"command"`
	Enabled        bool       `json:"enabled" msgpack:"enabled"`
	Running        bool       `json:"running" msgpack:"running"`
	AllowOverlap   bool       `json:"allow_overlap" msgpack:"allow_overlap"`
	CWD            string     `json:"cwd" msgpack:"cwd"`
	Interval       string     `json:"interval" msgpack:"interval"`
	Schedule       string     `json:"schedule,omitempty" msgpack:"schedule,omitempty"`
	TimeoutMs      int        `json:"timeout_ms" msgpack:"timeout_ms"`
	NextRunAt      *time.Time `json:"next_run_at,omitempty" msgpack:"next_run_at,omitempty"`
	LastStartedAt  *time.Time `json:"last_started_at,omitempty" msgpack:"last_started_at,omitempty"`
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty" msgpack:"last_finished_at,omitempty"`
	LastDurationMs int64      `json:"last_duration_ms,omitempty" msgpack:"last_duration_ms,omitempty"`
	LastExitCode   int        `json:"last_exit_code,omitempty" msgpack:"last_exit_code,omitempty"`
	LastError      string     `json:"last_error,omitempty" msgpack:"last_error,omitempty"`
	LastOutput     string     `json:"last_output,omitempty" msgpack:"last_output,omitempty"`
	LastLogPath    string     `json:"last_log_path,omitempty" msgpack:"last_log_path,omitempty"`
	RunCount       int        `json:"run_count" msgpack:"run_count"`
	SkipCount      int        `json:"skip_count" msgpack:"skip_count"`
}
