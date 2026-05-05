package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandUIOpenTaskWorkspace = "ui.open_task_workspace"

	EventUICommandRequested = "ui.command.requested"

	UICommandOpenTaskWorkspace = "ui.open_task_workspace"
)

type UICommandRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	IssueID   naming.IssueID   `json:"issue_id" msgpack:"issue_id"`
	Command   string           `json:"command" msgpack:"command"`
	RequestID string           `json:"request_id,omitempty" msgpack:"request_id,omitempty"`
	CreatedAt time.Time        `json:"created_at,omitempty" msgpack:"created_at,omitempty"`
}

type UICommandResponseBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	IssueID   naming.IssueID   `json:"issue_id" msgpack:"issue_id"`
	Command   string           `json:"command" msgpack:"command"`
	RequestID string           `json:"request_id" msgpack:"request_id"`
	CreatedAt time.Time        `json:"created_at" msgpack:"created_at"`
}

type UICommandEventBody = UICommandResponseBody
