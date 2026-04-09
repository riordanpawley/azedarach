package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	EventSessionUpdated = "session.updated"
)

type SessionLifecycleState string

const (
	SessionLifecycleStateStarting SessionLifecycleState = "starting"
	SessionLifecycleStateAttached SessionLifecycleState = "attached"
	SessionLifecycleStatePaused   SessionLifecycleState = "paused"
	SessionLifecycleStateStopped  SessionLifecycleState = "stopped"
)

type SessionProjection struct {
	SessionID naming.SessionID      `json:"session_id" msgpack:"session_id"`
	IssueID   naming.IssueID        `json:"issue_id" msgpack:"issue_id"`
	State     SessionLifecycleState `json:"state" msgpack:"state"`
	UpdatedAt time.Time             `json:"updated_at" msgpack:"updated_at"`
}

type SessionProjectionEventBody struct {
	ProjectID naming.ProjectID            `json:"project_id" msgpack:"project_id"`
	Revision  uint64                      `json:"revision" msgpack:"revision"`
	Session   SessionProjection           `json:"session" msgpack:"session"`
	Runtime   *RuntimeProjectionEventBody `json:"runtime,omitempty" msgpack:"runtime,omitempty"`
}
