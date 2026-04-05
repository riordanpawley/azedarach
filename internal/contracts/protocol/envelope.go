package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// EnvelopeKind identifies the high-level message envelope category.
type EnvelopeKind string

const (
	EnvelopeKindCommand  EnvelopeKind = "command"
	EnvelopeKindResponse EnvelopeKind = "response"
	EnvelopeKindEvent    EnvelopeKind = "event"
)

// Metadata carries request correlation and revision semantics.
type Metadata struct {
	ProjectID           string           `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	ClientID            string           `json:"client_id,omitempty" msgpack:"client_id,omitempty"`
	SessionID           naming.SessionID `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	CorrelationID       string           `json:"correlation_id,omitempty" msgpack:"correlation_id,omitempty"`
	LastAppliedRevision uint64           `json:"last_applied_revision,omitempty" msgpack:"last_applied_revision,omitempty"`
}

// RequestEnvelope is the daemon command request shell.
type RequestEnvelope struct {
	ProtocolVersion Version      `json:"protocol_version" msgpack:"protocol_version"`
	RequestID       naming.RequestID `json:"request_id" msgpack:"request_id"`
	Kind            EnvelopeKind `json:"kind" msgpack:"kind"`
	Meta            Metadata     `json:"meta,omitempty" msgpack:"meta,omitempty"`
	Command         string       `json:"command" msgpack:"command"`
	SentAt          time.Time    `json:"sent_at" msgpack:"sent_at"`
	Body            []byte       `json:"body,omitempty" msgpack:"body,omitempty"`
}

// ResponseEnvelope is the daemon command response shell.
type ResponseEnvelope struct {
	ProtocolVersion Version        `json:"protocol_version" msgpack:"protocol_version"`
	RequestID       naming.RequestID `json:"request_id" msgpack:"request_id"`
	Kind            EnvelopeKind   `json:"kind" msgpack:"kind"`
	Meta            Metadata       `json:"meta,omitempty" msgpack:"meta,omitempty"`
	Revision        uint64         `json:"revision,omitempty" msgpack:"revision,omitempty"`
	CompletedAt     time.Time      `json:"completed_at" msgpack:"completed_at"`
	OK              bool           `json:"ok" msgpack:"ok"`
	Error           *ErrorEnvelope `json:"error,omitempty" msgpack:"error,omitempty"`
	Body            []byte         `json:"body,omitempty" msgpack:"body,omitempty"`
}

// EventEnvelope is the daemon stream event shell.
type EventEnvelope struct {
	ProtocolVersion Version      `json:"protocol_version" msgpack:"protocol_version"`
	ProjectID       string       `json:"project_id" msgpack:"project_id"`
	Meta            Metadata     `json:"meta,omitempty" msgpack:"meta,omitempty"`
	Revision        uint64       `json:"revision" msgpack:"revision"`
	Event           string       `json:"event" msgpack:"event"`
	Kind            EnvelopeKind `json:"kind" msgpack:"kind"`
	EmittedAt       time.Time    `json:"emitted_at" msgpack:"emitted_at"`
	Body            []byte       `json:"body,omitempty" msgpack:"body,omitempty"`
}

// ErrorCode identifies typed daemon/client protocol error conditions.
type ErrorCode string

const (
	ErrorCodeUnknown            ErrorCode = "unknown"
	ErrorCodeInvalidRequest     ErrorCode = "invalid_request"
	ErrorCodeUnsupportedCommand ErrorCode = "unsupported_command"
	ErrorCodeIncompatible       ErrorCode = "incompatible_protocol"
	ErrorCodeUpgradeRequired    ErrorCode = "upgrade_required"
	ErrorCodeTimeout            ErrorCode = "timeout"
	ErrorCodeUnavailable        ErrorCode = "unavailable"
	ErrorCodeRevisionGap        ErrorCode = "revision_gap"
	ErrorCodeConflict           ErrorCode = "conflict"
	ErrorCodeInternal           ErrorCode = "internal"
)

// ErrorEnvelope encodes typed cross-process error details.
type ErrorEnvelope struct {
	Code      ErrorCode `json:"code" msgpack:"code"`
	Message   string    `json:"message" msgpack:"message"`
	Retryable bool      `json:"retryable" msgpack:"retryable"`
}

// Valid reports whether the error code is part of the protocol taxonomy.
func (c ErrorCode) Valid() bool {
	switch c {
	case ErrorCodeUnknown,
		ErrorCodeInvalidRequest,
		ErrorCodeUnsupportedCommand,
		ErrorCodeIncompatible,
		ErrorCodeUpgradeRequired,
		ErrorCodeTimeout,
		ErrorCodeUnavailable,
		ErrorCodeRevisionGap,
		ErrorCodeConflict,
		ErrorCodeInternal:
		return true
	default:
		return false
	}
}

// Retryable reports whether the caller should retry the operation.
func (c ErrorCode) Retryable() bool {
	switch c {
	case ErrorCodeTimeout, ErrorCodeUnavailable, ErrorCodeRevisionGap:
		return true
	default:
		return false
	}
}

// IsCompatibilityFailure reports whether the code represents a version mismatch.
func (c ErrorCode) IsCompatibilityFailure() bool {
	return c == ErrorCodeIncompatible || c == ErrorCodeUpgradeRequired
}
