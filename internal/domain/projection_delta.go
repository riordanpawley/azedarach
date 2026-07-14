package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ProjectionDeltaOperation describes how a keyed materialized value changes.
type ProjectionDeltaOperation string
type ProjectionKind string

const (
	ProjectionDeltaUpsert ProjectionDeltaOperation = "upsert"
	ProjectionDeltaDelete ProjectionDeltaOperation = "delete"
	ProjectionKindIssue   ProjectionKind           = "issue"
	// ProjectionKindSourceAdvance is an internal delivery marker. It advances
	// the transitional outbox without materializing a keyed value.
	ProjectionKindSourceAdvance ProjectionKind = "source-advance"
)

const (
	ProjectionDeliveryContract = "transitional-projection-delivery-v1"
	IssueProjectorID           = "issue-complete-value"
	IssueProjectorBuild        = "dgp-v1"
	IssueProjectorChecksum     = "c70f3774295f5f6a4e17d2317ca739734e5c60c3b0d9cb79a21ba73aebfcfd5b"
)

const IssueProjectionDeltaSchemaVersion = 1

// IssueProjectionDeltaPayload is the replayable semantic value for one issue
// key. Upserts carry the complete canonical issue projection, including its
// active relationships. Deletes carry the key as an explicit tombstone.
type IssueProjectionDeltaPayload struct {
	SchemaVersion int    `json:"schema_version"`
	IssueID       string `json:"issue_id"`
	Deleted       bool   `json:"deleted"`
	Issue         *Task  `json:"issue,omitempty"`
}

// CanonicalIssueProjectionTask removes independently authoritative runtime,
// source-control, and lease projections from the issue/relationship value.
func CanonicalIssueProjectionTask(task Task) Task {
	task.Session = nil
	task.HasTmuxSession = false
	task.HasWorktree = false
	task.GitAheadCount = 0
	task.GitBehindCount = 0
	task.HasUncommittedChanges = false
	task.HasConflicts = false
	task.ConflictFiles = nil
	task.GitAdditions = 0
	task.GitDeletions = 0
	task.Origin = ""
	task.PullRequest = nil
	task.RuntimeUpdatedAt = time.Time{}
	task.Ownership = nil
	task.CoordinationLeases = nil
	return task
}

func (o ProjectionDeltaOperation) Valid() bool {
	return o == ProjectionDeltaUpsert || o == ProjectionDeltaDelete
}

// ProjectionDelta is a durable, locally ordered delivery product derived from
// an authority commit. Cursor is never project_sequence or semantic history.
type ProjectionDelta struct {
	ProjectID      string
	Cursor         uint64
	Kind           ProjectionKind
	Key            string
	Operation      ProjectionDeltaOperation
	IdempotencyKey string
	Payload        json.RawMessage
	CommittedAt    time.Time
	Source         ProjectionSourceRange
}

type ProjectionSourceRange struct {
	Authority    string
	SourceFrom   string
	SourceTo     string
	TerminalHash string
	Transitional bool
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
	Sources   []ProjectionSourceRange
}

// ProjectionSourceForDelta recovers the best available provenance from the
// immutable transitional 0047 row. It does not promote delivery order to
// semantic history.
func ProjectionSourceForDelta(delta ProjectionDelta) ProjectionSourceRange {
	if delta.Kind == ProjectionKindSourceAdvance {
		var payload struct {
			Authority  string `json:"authority"`
			Position   string `json:"position"`
			SourceHash string `json:"source_hash"`
		}
		if json.Unmarshal(delta.Payload, &payload) == nil && strings.TrimSpace(payload.Authority) != "" {
			return ProjectionSourceRange{Authority: payload.Authority, SourceFrom: payload.Position, SourceTo: payload.Position, TerminalHash: payload.SourceHash, Transitional: true}
		}
	}
	if position, ok := strings.CutPrefix(delta.IdempotencyKey, "issue-observation:"); ok {
		return ProjectionSourceRange{Authority: "legacy_issue_observation", SourceFrom: position, SourceTo: position, Transitional: true}
	}
	authority := "legacy_authority_commit"
	if strings.HasPrefix(delta.IdempotencyKey, "sync-upsert:") {
		authority = "legacy_external_sync"
	}
	return ProjectionSourceRange{Authority: authority, SourceFrom: delta.IdempotencyKey, SourceTo: delta.IdempotencyKey, Transitional: true}
}

func MergeProjectionSourceRanges(deltas []ProjectionDelta) []ProjectionSourceRange {
	byAuthority := map[string]ProjectionSourceRange{}
	for _, delta := range deltas {
		source := delta.Source
		if source.Authority == "" {
			source = ProjectionSourceForDelta(delta)
		}
		current, found := byAuthority[source.Authority]
		if !found {
			byAuthority[source.Authority] = source
			continue
		}
		current.SourceTo = source.SourceTo
		if source.TerminalHash != "" {
			current.TerminalHash = source.TerminalHash
		}
		byAuthority[source.Authority] = current
	}
	authorities := make([]string, 0, len(byAuthority))
	for authority := range byAuthority {
		authorities = append(authorities, authority)
	}
	sort.Strings(authorities)
	result := make([]ProjectionSourceRange, 0, len(authorities))
	for _, authority := range authorities {
		result = append(result, byAuthority[authority])
	}
	return result
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
