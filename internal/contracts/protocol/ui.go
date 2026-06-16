package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandUIOpenTaskWorkspace = "ui.open_task_workspace"
	CommandUIOpenTaskDrillDown = "ui.open_task_drill_down"
	CommandUIStateGet          = "ui.state.get"
	CommandUIStateSet          = "ui.state.set"

	EventUICommandRequested = "ui.command.requested"

	UICommandOpenTaskWorkspace = "ui.open_task_workspace"
	UICommandOpenTaskDrillDown = "ui.open_task_drill_down"
)

const (
	UIStateKeyTMUXSelectorLastActiveTab = "tmux.selector.last_active_tab"
	UIStateKeyUIViewMode                = "ui.view_mode"
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

type UIStateGetRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	Key       string           `json:"key" msgpack:"key"`
}

type UIStateSetRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	Key       string           `json:"key" msgpack:"key"`
	Value     string           `json:"value" msgpack:"value"`
}

type UIStateResponseBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Key       string           `json:"key" msgpack:"key"`
	Value     string           `json:"value,omitempty" msgpack:"value,omitempty"`
	Found     bool             `json:"found" msgpack:"found"`
	UpdatedAt time.Time        `json:"updated_at,omitempty" msgpack:"updated_at,omitempty"`
}
