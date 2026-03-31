package operations

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound         = errors.New("operation not found")
	ErrIntakeClosed     = errors.New("operation intake closed")
	ErrInvalidOperation = errors.New("invalid operation")
	ErrOperationActive  = errors.New("operation already active")
)

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateDone      State = "done"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Record struct {
	ID            string
	ProjectID     string
	IssueID       string
	Kind          string
	DedupeKey     string
	ResourceKeys  []string
	State         State
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	ErrorMessage  string
	ResultPayload []byte
}

type Query struct {
	ProjectID string
	IssueID   string
	Kind      string
	States    []State
	Limit     int
}

type UpdateParams struct {
	ID            string
	ToState       State
	StartedAt     *time.Time
	FinishedAt    *time.Time
	ErrorMessage  *string
	ResultPayload []byte
}

type Store interface {
	Create(context.Context, Record) (Record, error)
	Get(context.Context, string) (Record, error)
	List(context.Context, Query) ([]Record, error)
	Update(context.Context, UpdateParams) (Record, error)
}

type Runner func(context.Context) ([]byte, error)

type SubmitRequest struct {
	ID                 string
	ProjectID          string
	IssueID            string
	Kind               string
	DedupeKey          string
	ResourceKeys       []string
	RecentDedupeWindow time.Duration
}

type SubmitResult struct {
	Record  Record
	Deduped bool
}
