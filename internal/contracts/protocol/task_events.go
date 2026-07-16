package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	EventTaskCreated  = "task.created"
	EventTaskUpdated  = "task.updated"
	EventTaskDeleted  = "task.deleted"
	EventTaskArchived = "task.archived"
	EventTaskRestored = "task.restored"
)

// TaskEventBody carries the changed task row so clients can update board projections without waiting for a snapshot read.
type TaskEventBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	TaskID    naming.IssueID   `json:"task_id" msgpack:"task_id"`
	Task      *domain.Task     `json:"task,omitempty" msgpack:"task,omitempty"`
	UpdatedAt time.Time        `json:"updated_at" msgpack:"updated_at"`
}

type TaskEventPayloadFilter struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// TaskEventsRequest carries the typed event-history query across the daemon
// authority boundary. ID bounds are exclusive.
type TaskEventsRequest struct {
	TaskID        naming.IssueID           `json:"task_id"`
	Types         []string                 `json:"event_types,omitempty"`
	Order         string                   `json:"order,omitempty"`
	Limit         int                      `json:"limit,omitempty"`
	AfterID       int64                    `json:"after_id,omitempty"`
	BeforeID      int64                    `json:"before_id,omitempty"`
	Source        string                   `json:"source,omitempty"`
	SourceCommand string                   `json:"source_command,omitempty"`
	OperationID   string                   `json:"operation_id,omitempty"`
	SessionID     string                   `json:"session_id,omitempty"`
	WorktreePath  string                   `json:"worktree_path,omitempty"`
	ObservedSince *time.Time               `json:"observed_since,omitempty"`
	ObservedUntil *time.Time               `json:"observed_until,omitempty"`
	Query         string                   `json:"query,omitempty"`
	PayloadEquals []TaskEventPayloadFilter `json:"payload_equals,omitempty"`
}

type TaskEventsPage struct {
	Events       []domain.IssueObservationEvent `json:"events"`
	Order        string                         `json:"order"`
	Limit        int                            `json:"limit"`
	HasMore      bool                           `json:"has_more"`
	FirstID      int64                          `json:"first_id,omitempty"`
	LastID       int64                          `json:"last_id,omitempty"`
	NextAfterID  *int64                         `json:"next_after_id,omitempty"`
	NextBeforeID *int64                         `json:"next_before_id,omitempty"`
}
