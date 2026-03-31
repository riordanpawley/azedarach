package protocol

import (
	"encoding/json"
	"fmt"
)

// CommandTaskBulkApply is the daemon command used to validate and apply a JSON batch mutation payload.
const CommandTaskBulkApply = "task.bulk.apply"

// ApplySchemaVersion identifies the apply payload schema contract.
const ApplySchemaVersion uint16 = 1

// ApplyRequestBody is the JSON request payload for bulk apply operations.
type ApplyRequestBody struct {
	SchemaVersion    uint16               `json:"schema_version" msgpack:"schema_version"`
	SnapshotRevision uint64               `json:"snapshot_revision" msgpack:"snapshot_revision"`
	DryRun           bool                 `json:"dry_run" msgpack:"dry_run"`
	Operations       []ApplyOperationBody `json:"operations" msgpack:"operations"`
}

// ApplyOperationBody is one item in a bulk apply request.
//
// The command determines how the embedded body is validated and later executed.
type ApplyOperationBody struct {
	Command string          `json:"command" msgpack:"command"`
	Body    json.RawMessage `json:"body,omitempty" msgpack:"body,omitempty"`
}

// ApplyValidationCode identifies typed validation failures for bulk apply requests.
type ApplyValidationCode string

const (
	ApplyValidationCodeInvalidSchemaVersion    ApplyValidationCode = "invalid_schema_version"
	ApplyValidationCodeInvalidSnapshotRevision ApplyValidationCode = "invalid_snapshot_revision"
	ApplyValidationCodeMissingOperations       ApplyValidationCode = "missing_operations"
	ApplyValidationCodeInvalidOperationCommand ApplyValidationCode = "invalid_operation_command"
	ApplyValidationCodeInvalidOperationBody    ApplyValidationCode = "invalid_operation_body"
	ApplyValidationCodeMissingField            ApplyValidationCode = "missing_field"
)

// ApplyValidationDiagnostic is a typed validation failure for one item in the request.
type ApplyValidationDiagnostic struct {
	Index   int                 `json:"index" msgpack:"index"`
	Code    ApplyValidationCode `json:"code" msgpack:"code"`
	Field   string              `json:"field,omitempty" msgpack:"field,omitempty"`
	Message string              `json:"message" msgpack:"message"`
}

// ApplyValidationError is returned when a bulk apply request fails validation.
type ApplyValidationError struct {
	Code        ErrorCode                   `json:"code" msgpack:"code"`
	Message     string                      `json:"message" msgpack:"message"`
	Diagnostics []ApplyValidationDiagnostic `json:"diagnostics,omitempty" msgpack:"diagnostics,omitempty"`
}

// Error satisfies the error interface.
func (e *ApplyValidationError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Diagnostics) == 0 {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (%d diagnostics)", e.Code, e.Message, len(e.Diagnostics))
}

// Valid reports whether the validation code is part of the protocol taxonomy.
func (c ApplyValidationCode) Valid() bool {
	switch c {
	case ApplyValidationCodeInvalidSchemaVersion,
		ApplyValidationCodeInvalidSnapshotRevision,
		ApplyValidationCodeMissingOperations,
		ApplyValidationCodeInvalidOperationCommand,
		ApplyValidationCodeInvalidOperationBody,
		ApplyValidationCodeMissingField:
		return true
	default:
		return false
	}
}
