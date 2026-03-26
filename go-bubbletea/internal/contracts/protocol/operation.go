package protocol

import (
	"encoding/json"
	"time"
)

const (
	CommandOperationSubmit = "operation.submit"
	CommandOperationGet    = "operation.get"
	CommandOperationList   = "operation.list"
	CommandOperationCancel = "operation.cancel"
)

const (
	EventOperationQueued    = "operation.queued"
	EventOperationRunning   = "operation.running"
	EventOperationProgress  = "operation.progress"
	EventOperationDone      = "operation.done"
	EventOperationFailed    = "operation.failed"
	EventOperationCancelled = "operation.cancelled"
)

type OperationState string

const (
	OperationStateQueued    OperationState = "queued"
	OperationStateRunning   OperationState = "running"
	OperationStateDone      OperationState = "done"
	OperationStateFailed    OperationState = "failed"
	OperationStateCancelled OperationState = "cancelled"
)

type OperationProgress struct {
	Message string `json:"message,omitempty" msgpack:"message,omitempty"`
	Current int64  `json:"current,omitempty" msgpack:"current,omitempty"`
	Total   int64  `json:"total,omitempty" msgpack:"total,omitempty"`
	Unit    string `json:"unit,omitempty" msgpack:"unit,omitempty"`
	Percent int    `json:"percent,omitempty" msgpack:"percent,omitempty"`
}

type OperationError struct {
	Code      ErrorCode `json:"code" msgpack:"code"`
	Message   string    `json:"message" msgpack:"message"`
	Retryable bool      `json:"retryable" msgpack:"retryable"`
}

type OperationRecord struct {
	OperationID  string             `json:"operation_id" msgpack:"operation_id"`
	ProjectID    string             `json:"project_id" msgpack:"project_id"`
	Kind         string             `json:"kind" msgpack:"kind"`
	IssueID      string             `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	DedupeKey    string             `json:"dedupe_key,omitempty" msgpack:"dedupe_key,omitempty"`
	ResourceKeys []string           `json:"resource_keys,omitempty" msgpack:"resource_keys,omitempty"`
	State        OperationState     `json:"state" msgpack:"state"`
	Progress     *OperationProgress `json:"progress,omitempty" msgpack:"progress,omitempty"`
	Payload      json.RawMessage    `json:"payload,omitempty" msgpack:"payload,omitempty"`
	Result       json.RawMessage    `json:"result,omitempty" msgpack:"result,omitempty"`
	Error        *OperationError    `json:"error,omitempty" msgpack:"error,omitempty"`
	EnqueuedAt   time.Time          `json:"enqueued_at" msgpack:"enqueued_at"`
	StartedAt    *time.Time         `json:"started_at,omitempty" msgpack:"started_at,omitempty"`
	FinishedAt   *time.Time         `json:"finished_at,omitempty" msgpack:"finished_at,omitempty"`
}

type OperationSubmitRequestBody struct {
	ProjectID    string          `json:"project_id" msgpack:"project_id"`
	Kind         string          `json:"kind" msgpack:"kind"`
	IssueID      string          `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	DedupeKey    string          `json:"dedupe_key,omitempty" msgpack:"dedupe_key,omitempty"`
	ResourceKeys []string        `json:"resource_keys,omitempty" msgpack:"resource_keys,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty" msgpack:"payload,omitempty"`
}

type OperationSubmitResponseBody struct {
	Created   bool            `json:"created" msgpack:"created"`
	Operation OperationRecord `json:"operation" msgpack:"operation"`
}

type OperationGetRequestBody struct {
	ProjectID   string `json:"project_id" msgpack:"project_id"`
	OperationID string `json:"operation_id" msgpack:"operation_id"`
}

type OperationGetResponseBody struct {
	Operation OperationRecord `json:"operation" msgpack:"operation"`
}

type OperationListRequestBody struct {
	ProjectID string           `json:"project_id" msgpack:"project_id"`
	IssueID   string           `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
	Kind      string           `json:"kind,omitempty" msgpack:"kind,omitempty"`
	States    []OperationState `json:"states,omitempty" msgpack:"states,omitempty"`
	Limit     int              `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type OperationListResponseBody struct {
	ProjectID  string            `json:"project_id" msgpack:"project_id"`
	Operations []OperationRecord `json:"operations" msgpack:"operations"`
}

type OperationCancelRequestBody struct {
	ProjectID   string `json:"project_id" msgpack:"project_id"`
	OperationID string `json:"operation_id" msgpack:"operation_id"`
	Reason      string `json:"reason,omitempty" msgpack:"reason,omitempty"`
}

type OperationCancelResponseBody struct {
	Cancelled bool            `json:"cancelled" msgpack:"cancelled"`
	Operation OperationRecord `json:"operation" msgpack:"operation"`
}

type OperationEventBody struct {
	Operation OperationRecord `json:"operation" msgpack:"operation"`
}

type OperationProgressEventBody struct {
	OperationID string            `json:"operation_id" msgpack:"operation_id"`
	ProjectID   string            `json:"project_id" msgpack:"project_id"`
	State       OperationState    `json:"state" msgpack:"state"`
	Progress    OperationProgress `json:"progress" msgpack:"progress"`
}

func (s OperationState) Valid() bool {
	switch s {
	case OperationStateQueued,
		OperationStateRunning,
		OperationStateDone,
		OperationStateFailed,
		OperationStateCancelled:
		return true
	default:
		return false
	}
}
