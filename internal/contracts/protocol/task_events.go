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
)

// TaskEventBody carries the changed task row so clients can update board projections without waiting for a snapshot read.
type TaskEventBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	TaskID    naming.IssueID   `json:"task_id" msgpack:"task_id"`
	Task      *domain.Task     `json:"task,omitempty" msgpack:"task,omitempty"`
	UpdatedAt time.Time        `json:"updated_at" msgpack:"updated_at"`
}
