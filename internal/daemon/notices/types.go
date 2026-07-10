package notices

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

var (
	ErrNotFound          = errors.New("notice not found")
	ErrConflict          = errors.New("notice conflict")
	ErrInvalidNotice     = errors.New("invalid notice")
	ErrInvalidTransition = errors.New("invalid notice transition")
)

type State = protocol.NoticeState
type Severity = protocol.NoticeSeverity
type RetentionClass = protocol.NoticeRetentionClass
type Scope = protocol.NoticeScope
type Source = protocol.NoticeSource
type Cause = protocol.NoticeCause
type Action = protocol.NoticeAction

const (
	StateActive    = protocol.NoticeStateActive
	StateResolved  = protocol.NoticeStateResolved
	StateDismissed = protocol.NoticeStateDismissed
	StateExpired   = protocol.NoticeStateExpired

	SeverityInfo    = protocol.NoticeSeverityInfo
	SeveritySuccess = protocol.NoticeSeveritySuccess
	SeverityWarning = protocol.NoticeSeverityWarning
	SeverityError   = protocol.NoticeSeverityError

	RetentionTransient = protocol.NoticeRetentionTransient
	RetentionAudit     = protocol.NoticeRetentionAudit
	RetentionError     = protocol.NoticeRetentionError
	RetentionRecovery  = protocol.NoticeRetentionRecovery
)

type Record struct {
	NoticeID          string
	ProjectID         string
	Scope             Scope
	Source            *Source
	Severity          Severity
	Category          string
	State             State
	Read              bool
	Title             string
	Summary           string
	Detail            string
	Cause             *Cause
	Actions           []Action
	DedupeKey         string
	OccurrenceCount   int
	FirstOccurrenceAt time.Time
	LastOccurrenceAt  time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ResolvedAt        *time.Time
	DismissedAt       *time.Time
	ExpiresAt         *time.Time
	RetentionClass    RetentionClass
}

type Candidate struct {
	NoticeID       string
	ProjectID      string
	Scope          Scope
	Source         *Source
	Severity       Severity
	Category       string
	Title          string
	Summary        string
	Detail         string
	Cause          *Cause
	Actions        []Action
	DedupeKey      string
	OccurredAt     time.Time
	ExpiresAt      *time.Time
	RetentionClass RetentionClass
}

type Query struct {
	ProjectID    string
	States       []State
	Read         *bool
	Severity     Severity
	Category     string
	ScopeType    string
	ScopeID      string
	OperationID  string
	DedupeKey    string
	UpdatedAfter *time.Time
	Limit        int
}

type UpdateParams struct {
	ProjectID string
	NoticeID  string
	Read      *bool
	State     State
	Now       time.Time
}

type ExpireQuery struct {
	ProjectID string
	Now       time.Time
	Limit     int
}

func NormalizeCandidate(candidate Candidate) (Record, error) {
	now := candidate.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	noticeID := strings.TrimSpace(candidate.NoticeID)
	if noticeID == "" {
		noticeID = NewNoticeID(now)
	}
	projectID := strings.TrimSpace(candidate.ProjectID)
	if projectID == "" {
		return Record{}, fmt.Errorf("%w: missing project_id", ErrInvalidNotice)
	}
	scopeType := strings.TrimSpace(candidate.Scope.Type)
	if scopeType == "" {
		return Record{}, fmt.Errorf("%w: missing scope.type", ErrInvalidNotice)
	}
	severity := candidate.Severity
	if severity == "" {
		severity = SeverityInfo
	}
	if !severity.Valid() {
		return Record{}, fmt.Errorf("%w: unsupported severity %q", ErrInvalidNotice, severity)
	}
	category := strings.TrimSpace(candidate.Category)
	if category == "" {
		return Record{}, fmt.Errorf("%w: missing category", ErrInvalidNotice)
	}
	title := strings.TrimSpace(candidate.Title)
	if title == "" {
		return Record{}, fmt.Errorf("%w: missing title", ErrInvalidNotice)
	}
	summary := strings.TrimSpace(candidate.Summary)
	if summary == "" {
		summary = title
	}
	retention := candidate.RetentionClass
	if retention == "" {
		retention = defaultRetentionForSeverity(severity)
	}
	return Record{
		NoticeID:  noticeID,
		ProjectID: projectID,
		Scope: Scope{
			Type: scopeType,
			ID:   strings.TrimSpace(candidate.Scope.ID),
		},
		Source:            cloneSource(candidate.Source),
		Severity:          severity,
		Category:          category,
		State:             StateActive,
		Read:              false,
		Title:             title,
		Summary:           summary,
		Detail:            strings.TrimSpace(candidate.Detail),
		Cause:             cloneCause(candidate.Cause),
		Actions:           cloneActions(candidate.Actions),
		DedupeKey:         strings.TrimSpace(candidate.DedupeKey),
		OccurrenceCount:   1,
		FirstOccurrenceAt: now,
		LastOccurrenceAt:  now,
		CreatedAt:         now,
		UpdatedAt:         now,
		ExpiresAt:         cloneTime(candidate.ExpiresAt),
		RetentionClass:    retention,
	}, nil
}

func ValidateTransition(from, to State) error {
	if to == "" || from == to {
		return nil
	}
	switch from {
	case StateActive:
		if to == StateResolved || to == StateDismissed {
			return nil
		}
	case StateResolved:
		if to == StateDismissed {
			return nil
		}
	case StateDismissed:
		if to == StateActive {
			return nil
		}
	case StateExpired:
		return fmt.Errorf("%w: expired notices are terminal", ErrInvalidTransition)
	default:
		return fmt.Errorf("%w: unknown current state %q", ErrInvalidTransition, from)
	}
	return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
}

func ApplyLifecycle(record Record, params UpdateParams) (Record, bool, error) {
	next := record
	changed := false
	now := params.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if params.Read != nil && next.Read != *params.Read {
		next.Read = *params.Read
		changed = true
	}
	if params.State != "" && params.State != next.State {
		if err := ValidateTransition(next.State, params.State); err != nil {
			return Record{}, false, err
		}
		next.State = params.State
		switch params.State {
		case StateActive:
			next.ResolvedAt = nil
			next.DismissedAt = nil
			next.Read = false
		case StateResolved:
			resolvedAt := now
			next.ResolvedAt = &resolvedAt
		case StateDismissed:
			dismissedAt := now
			next.DismissedAt = &dismissedAt
		}
		changed = true
	}
	if changed {
		next.UpdatedAt = now
	}
	return next, changed, nil
}

func ApplyDedupe(existing, incoming Record) Record {
	next := existing
	next.Source = cloneSource(incoming.Source)
	next.Severity = incoming.Severity
	next.Category = incoming.Category
	next.Read = false
	next.Title = incoming.Title
	next.Summary = incoming.Summary
	next.Detail = incoming.Detail
	next.Cause = cloneCause(incoming.Cause)
	next.Actions = cloneActions(incoming.Actions)
	next.OccurrenceCount++
	next.LastOccurrenceAt = incoming.LastOccurrenceAt
	next.UpdatedAt = incoming.UpdatedAt
	next.ExpiresAt = cloneTime(incoming.ExpiresAt)
	next.RetentionClass = incoming.RetentionClass
	return next
}

// ApplyProjection refreshes daemon-owned presentation fields while preserving
// the user's read/dismissed lifecycle. Unlike occurrence dedupe, a projection
// refresh is idempotent and must not manufacture another occurrence.
func ApplyProjection(existing, incoming Record) (Record, bool) {
	next := existing
	next.Scope = incoming.Scope
	next.Source = cloneSource(incoming.Source)
	next.Severity = incoming.Severity
	next.Category = incoming.Category
	next.Title = incoming.Title
	next.Summary = incoming.Summary
	next.Detail = incoming.Detail
	next.Cause = cloneCause(incoming.Cause)
	next.Actions = cloneActions(incoming.Actions)
	next.DedupeKey = incoming.DedupeKey
	next.ExpiresAt = cloneTime(incoming.ExpiresAt)
	next.RetentionClass = incoming.RetentionClass
	if recordsEqualForProjection(existing, next) {
		return existing, false
	}
	next.UpdatedAt = incoming.UpdatedAt
	return next, true
}

func recordsEqualForProjection(a, b Record) bool {
	return reflect.DeepEqual(a, b)
}

func NewNoticeID(now time.Time) string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err == nil {
		return fmt.Sprintf("notice-%d-%s", now.UnixNano(), hex.EncodeToString(suffix[:]))
	}
	return fmt.Sprintf("notice-%d", now.UnixNano())
}

func ToProtocol(record Record) protocol.NoticeRecord {
	return protocol.NoticeRecord{
		NoticeID:          record.NoticeID,
		ProjectID:         naming.ProjectID(protocol.NormalizeProjectID(record.ProjectID)),
		Scope:             record.Scope,
		Source:            cloneSource(record.Source),
		Severity:          record.Severity,
		Category:          record.Category,
		State:             record.State,
		Read:              record.Read,
		Title:             record.Title,
		Summary:           record.Summary,
		Detail:            record.Detail,
		Cause:             cloneCause(record.Cause),
		Actions:           cloneActions(record.Actions),
		DedupeKey:         record.DedupeKey,
		OccurrenceCount:   record.OccurrenceCount,
		FirstOccurrenceAt: record.FirstOccurrenceAt,
		LastOccurrenceAt:  record.LastOccurrenceAt,
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
		ResolvedAt:        cloneTime(record.ResolvedAt),
		DismissedAt:       cloneTime(record.DismissedAt),
		ExpiresAt:         cloneTime(record.ExpiresAt),
		RetentionClass:    record.RetentionClass,
	}
}

func defaultRetentionForSeverity(severity Severity) RetentionClass {
	switch severity {
	case SeverityError:
		return RetentionError
	case SeverityWarning:
		return RetentionAudit
	default:
		return RetentionTransient
	}
}

func cloneSource(source *Source) *Source {
	if source == nil {
		return nil
	}
	copied := *source
	return &copied
}

func cloneCause(cause *Cause) *Cause {
	if cause == nil {
		return nil
	}
	copied := *cause
	return &copied
}

func cloneActions(actions []Action) []Action {
	if len(actions) == 0 {
		return nil
	}
	copied := make([]Action, len(actions))
	copy(copied, actions)
	return copied
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := value.UTC()
	return &copied
}
