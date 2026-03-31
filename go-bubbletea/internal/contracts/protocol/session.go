package protocol

import "time"

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
	SessionID string                `json:"session_id" msgpack:"session_id"`
	IssueID   string                `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	State     SessionLifecycleState `json:"state" msgpack:"state"`
	UpdatedAt time.Time             `json:"updated_at" msgpack:"updated_at"`
}

type SessionProjectionEventBody struct {
	ProjectID string            `json:"project_id" msgpack:"project_id"`
	Revision  uint64            `json:"revision" msgpack:"revision"`
	Session   SessionProjection `json:"session" msgpack:"session"`
}
