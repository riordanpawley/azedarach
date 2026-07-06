package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandNoticeList   = "notice.list"
	CommandNoticeGet    = "notice.get"
	CommandNoticeUpdate = "notice.update"
	CommandNoticeAction = "notice.action"
)

const (
	EventNoticeCreated = "notice.created"
	EventNoticeUpdated = "notice.updated"
	EventNoticeExpired = "notice.expired"
	EventNoticeDeleted = "notice.deleted"
)

type NoticeSeverity string

const (
	NoticeSeverityInfo    NoticeSeverity = "info"
	NoticeSeveritySuccess NoticeSeverity = "success"
	NoticeSeverityWarning NoticeSeverity = "warning"
	NoticeSeverityError   NoticeSeverity = "error"
)

type NoticeState string

const (
	NoticeStateActive    NoticeState = "active"
	NoticeStateResolved  NoticeState = "resolved"
	NoticeStateDismissed NoticeState = "dismissed"
	NoticeStateExpired   NoticeState = "expired"
)

type NoticeRetentionClass string

const (
	NoticeRetentionTransient NoticeRetentionClass = "transient"
	NoticeRetentionAudit     NoticeRetentionClass = "audit"
	NoticeRetentionError     NoticeRetentionClass = "error"
	NoticeRetentionRecovery  NoticeRetentionClass = "recovery"
)

type NoticeScope struct {
	Type string `json:"type" msgpack:"type"`
	ID   string `json:"id,omitempty" msgpack:"id,omitempty"`
}

type NoticeSource struct {
	OperationID    naming.OperationID `json:"operation_id,omitempty" msgpack:"operation_id,omitempty"`
	OperationKind  string             `json:"operation_kind,omitempty" msgpack:"operation_kind,omitempty"`
	OperationState OperationState     `json:"operation_state,omitempty" msgpack:"operation_state,omitempty"`
	RequestID      naming.RequestID   `json:"request_id,omitempty" msgpack:"request_id,omitempty"`
	Producer       string             `json:"producer,omitempty" msgpack:"producer,omitempty"`
}

type NoticeCause struct {
	Code             string    `json:"code,omitempty" msgpack:"code,omitempty"`
	Message          string    `json:"message,omitempty" msgpack:"message,omitempty"`
	Retryable        bool      `json:"retryable,omitempty" msgpack:"retryable,omitempty"`
	RawDiagnosticRef string    `json:"raw_diagnostic_ref,omitempty" msgpack:"raw_diagnostic_ref,omitempty"`
	ErrorCode        ErrorCode `json:"error_code,omitempty" msgpack:"error_code,omitempty"`
}

type NoticeAction struct {
	ActionID             string      `json:"action_id" msgpack:"action_id"`
	Kind                 string      `json:"kind" msgpack:"kind"`
	Label                string      `json:"label" msgpack:"label"`
	Enabled              bool        `json:"enabled" msgpack:"enabled"`
	DisabledReason       string      `json:"disabled_reason,omitempty" msgpack:"disabled_reason,omitempty"`
	RequiresConfirmation bool        `json:"requires_confirmation,omitempty" msgpack:"requires_confirmation,omitempty"`
	Inputs               []string    `json:"inputs,omitempty" msgpack:"inputs,omitempty"`
	TargetScope          NoticeScope `json:"target_scope,omitempty" msgpack:"target_scope,omitempty"`
}

type NoticeRecord struct {
	NoticeID          string               `json:"notice_id" msgpack:"notice_id"`
	ProjectID         naming.ProjectID     `json:"project_id" msgpack:"project_id"`
	Scope             NoticeScope          `json:"scope" msgpack:"scope"`
	Source            *NoticeSource        `json:"source,omitempty" msgpack:"source,omitempty"`
	Severity          NoticeSeverity       `json:"severity" msgpack:"severity"`
	Category          string               `json:"category" msgpack:"category"`
	State             NoticeState          `json:"state" msgpack:"state"`
	Read              bool                 `json:"read" msgpack:"read"`
	Title             string               `json:"title" msgpack:"title"`
	Summary           string               `json:"summary" msgpack:"summary"`
	Detail            string               `json:"detail,omitempty" msgpack:"detail,omitempty"`
	Cause             *NoticeCause         `json:"cause,omitempty" msgpack:"cause,omitempty"`
	Actions           []NoticeAction       `json:"actions,omitempty" msgpack:"actions,omitempty"`
	DedupeKey         string               `json:"dedupe_key,omitempty" msgpack:"dedupe_key,omitempty"`
	OccurrenceCount   int                  `json:"occurrence_count" msgpack:"occurrence_count"`
	FirstOccurrenceAt time.Time            `json:"first_occurrence_at" msgpack:"first_occurrence_at"`
	LastOccurrenceAt  time.Time            `json:"last_occurrence_at" msgpack:"last_occurrence_at"`
	CreatedAt         time.Time            `json:"created_at" msgpack:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at" msgpack:"updated_at"`
	ResolvedAt        *time.Time           `json:"resolved_at,omitempty" msgpack:"resolved_at,omitempty"`
	DismissedAt       *time.Time           `json:"dismissed_at,omitempty" msgpack:"dismissed_at,omitempty"`
	ExpiresAt         *time.Time           `json:"expires_at,omitempty" msgpack:"expires_at,omitempty"`
	RetentionClass    NoticeRetentionClass `json:"retention_class" msgpack:"retention_class"`
}

type NoticeListRequestBody struct {
	ProjectID    naming.ProjectID   `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	States       []NoticeState      `json:"states,omitempty" msgpack:"states,omitempty"`
	Read         *bool              `json:"read,omitempty" msgpack:"read,omitempty"`
	Severity     NoticeSeverity     `json:"severity,omitempty" msgpack:"severity,omitempty"`
	Category     string             `json:"category,omitempty" msgpack:"category,omitempty"`
	ScopeType    string             `json:"scope_type,omitempty" msgpack:"scope_type,omitempty"`
	ScopeID      string             `json:"scope_id,omitempty" msgpack:"scope_id,omitempty"`
	OperationID  naming.OperationID `json:"operation_id,omitempty" msgpack:"operation_id,omitempty"`
	UpdatedAfter *time.Time         `json:"updated_after,omitempty" msgpack:"updated_after,omitempty"`
	Limit        int                `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type NoticeListResponseBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Notices   []NoticeRecord   `json:"notices" msgpack:"notices"`
}

type NoticeGetRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	NoticeID  string           `json:"notice_id" msgpack:"notice_id"`
}

type NoticeGetResponseBody struct {
	Notice NoticeRecord `json:"notice" msgpack:"notice"`
}

type NoticeUpdateRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	NoticeID  string           `json:"notice_id" msgpack:"notice_id"`
	Read      *bool            `json:"read,omitempty" msgpack:"read,omitempty"`
	State     NoticeState      `json:"state,omitempty" msgpack:"state,omitempty"`
}

type NoticeUpdateResponseBody struct {
	Notice NoticeRecord `json:"notice" msgpack:"notice"`
}

type NoticeActionRequestBody struct {
	ProjectID naming.ProjectID  `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	NoticeID  string            `json:"notice_id" msgpack:"notice_id"`
	ActionID  string            `json:"action_id" msgpack:"action_id"`
	Input     map[string]string `json:"input,omitempty" msgpack:"input,omitempty"`
}

type NoticeActionResponseBody struct {
	Notice NoticeRecord `json:"notice" msgpack:"notice"`
}

type NoticeEventBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Revision  uint64           `json:"revision" msgpack:"revision"`
	NoticeID  string           `json:"notice_id" msgpack:"notice_id"`
	State     NoticeState      `json:"state" msgpack:"state"`
	Notice    *NoticeRecord    `json:"notice,omitempty" msgpack:"notice,omitempty"`
	UpdatedAt time.Time        `json:"updated_at" msgpack:"updated_at"`
}

func (s NoticeSeverity) Valid() bool {
	switch s {
	case NoticeSeverityInfo, NoticeSeveritySuccess, NoticeSeverityWarning, NoticeSeverityError:
		return true
	default:
		return false
	}
}

func (s NoticeState) Valid() bool {
	switch s {
	case NoticeStateActive, NoticeStateResolved, NoticeStateDismissed, NoticeStateExpired:
		return true
	default:
		return false
	}
}
