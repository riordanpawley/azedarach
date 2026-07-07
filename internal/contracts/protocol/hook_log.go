package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandHookLogAppend = "hook.log.append"
	CommandHookLogList   = "hook.log.list"

	EventHookLogAppended = "hook.log.appended"
)

type HookLogAppendCommandBody struct {
	Event HookLogEvent `json:"event" msgpack:"event"`
}

type HookLogListCommandBody struct {
	Limit int `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type HookLogEvent struct {
	ProjectID  naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID    naming.IssueID   `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	Hook       string           `json:"hook,omitempty" msgpack:"hook,omitempty"`
	Command    string           `json:"command,omitempty" msgpack:"command,omitempty"`
	Worktree   string           `json:"worktree,omitempty" msgpack:"worktree,omitempty"`
	Source     string           `json:"source,omitempty" msgpack:"source,omitempty"`
	Level      string           `json:"level,omitempty" msgpack:"level,omitempty"`
	Message    string           `json:"message" msgpack:"message"`
	ElapsedMS  int64            `json:"elapsed_ms,omitempty" msgpack:"elapsed_ms,omitempty"`
	ExitStatus *int             `json:"exit_status,omitempty" msgpack:"exit_status,omitempty"`
	Blocking   *bool            `json:"blocking,omitempty" msgpack:"blocking,omitempty"`
	CreatedAt  time.Time        `json:"created_at" msgpack:"created_at"`
}
