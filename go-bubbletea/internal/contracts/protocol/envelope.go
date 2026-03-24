package protocol

import "time"

// EnvelopeKind identifies the high-level message envelope category.
type EnvelopeKind string

const (
	EnvelopeKindCommand  EnvelopeKind = "command"
	EnvelopeKindResponse EnvelopeKind = "response"
	EnvelopeKindEvent    EnvelopeKind = "event"
)

// RequestEnvelope is the daemon command request shell.
type RequestEnvelope struct {
	ProtocolVersion Version      `json:"protocol_version" msgpack:"protocol_version"`
	RequestID       string       `json:"request_id" msgpack:"request_id"`
	Kind            EnvelopeKind `json:"kind" msgpack:"kind"`
	Command         string       `json:"command" msgpack:"command"`
	Body            []byte       `json:"body,omitempty" msgpack:"body,omitempty"`
}

// ResponseEnvelope is the daemon command response shell.
type ResponseEnvelope struct {
	ProtocolVersion Version        `json:"protocol_version" msgpack:"protocol_version"`
	RequestID       string         `json:"request_id" msgpack:"request_id"`
	Kind            EnvelopeKind   `json:"kind" msgpack:"kind"`
	OK              bool           `json:"ok" msgpack:"ok"`
	Error           *ErrorEnvelope `json:"error,omitempty" msgpack:"error,omitempty"`
	Body            []byte         `json:"body,omitempty" msgpack:"body,omitempty"`
}

// EventEnvelope is the daemon stream event shell.
type EventEnvelope struct {
	ProtocolVersion Version      `json:"protocol_version" msgpack:"protocol_version"`
	ProjectID       string       `json:"project_id" msgpack:"project_id"`
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
)

// ErrorEnvelope encodes typed cross-process error details.
type ErrorEnvelope struct {
	Code    ErrorCode `json:"code" msgpack:"code"`
	Message string    `json:"message" msgpack:"message"`
}
