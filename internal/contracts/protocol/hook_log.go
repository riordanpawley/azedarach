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
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	Hook      string           `json:"hook,omitempty" msgpack:"hook,omitempty"`
	Worktree  string           `json:"worktree,omitempty" msgpack:"worktree,omitempty"`
	Source    string           `json:"source,omitempty" msgpack:"source,omitempty"`
	Level     string           `json:"level,omitempty" msgpack:"level,omitempty"`
	Message   string           `json:"message" msgpack:"message"`
	CreatedAt time.Time        `json:"created_at" msgpack:"created_at"`
}
