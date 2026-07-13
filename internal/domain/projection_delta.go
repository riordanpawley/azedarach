package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ProjectionDeltaOperation describes how a keyed materialized value changes.
type ProjectionDeltaOperation string
type ProjectionKind string

const (
	ProjectionDeltaUpsert ProjectionDeltaOperation = "upsert"
	ProjectionDeltaDelete ProjectionDeltaOperation = "delete"
)

func (o ProjectionDeltaOperation) Valid() bool {
	return o == ProjectionDeltaUpsert || o == ProjectionDeltaDelete
}

// ProjectionDelta is the durable, project-ordered product of an authority commit.
type ProjectionDelta struct {
	ProjectID      string
	Cursor         uint64
	Kind           ProjectionKind
	Key            string
	Operation      ProjectionDeltaOperation
	IdempotencyKey string
	Payload        json.RawMessage
	CommittedAt    time.Time
}

// ProjectionValue is one keyed value as it existed at a declared cursor.
type ProjectionValue struct {
	Kind    ProjectionKind
	Key     string
	Payload json.RawMessage
}

type ProjectionSnapshot struct {
	ProjectID string
	Cursor    uint64
	Head      uint64
	Values    []ProjectionValue
}

var (
	ErrProjectionCanceled  = errors.New("projection watch canceled")
	ErrProjectionRetryable = errors.New("projection temporarily unavailable")
)

// ProjectionGapError reports a cursor that cannot be served sequentially.
type ProjectionGapError struct {
	ProjectID string
	Expected  uint64
	Actual    uint64
}

func (e *ProjectionGapError) Error() string {
	return fmt.Sprintf("projection cursor gap for %s: expected %d, got %d", e.ProjectID, e.Expected, e.Actual)
}

// ProjectionCanceledError preserves context cancellation as a typed domain error.
type ProjectionCanceledError struct{ Cause error }

func (e *ProjectionCanceledError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrProjectionCanceled.Error()
	}
	return ErrProjectionCanceled.Error() + ": " + e.Cause.Error()
}
func (e *ProjectionCanceledError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
func (e *ProjectionCanceledError) Is(target error) bool {
	return target == ErrProjectionCanceled || (e != nil && errors.Is(e.Cause, target))
}

type ProjectionRetryableError struct{ Cause error }

func (e *ProjectionRetryableError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrProjectionRetryable.Error()
	}
	return ErrProjectionRetryable.Error() + ": " + e.Cause.Error()
}
func (e *ProjectionRetryableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
func (e *ProjectionRetryableError) Is(target error) bool {
	return target == ErrProjectionRetryable || (e != nil && errors.Is(e.Cause, target))
}
