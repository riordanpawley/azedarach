package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	applyCommandTaskCreate       = "task.create"
	applyCommandTaskUpdateStatus = "task.update_status"
	applyCommandTaskUpdate       = "task.update_details"
	applyCommandTaskDelete       = "task.delete"
	applyCommandTaskArchive      = "task.archive"
)

type applyTaskCreateBody struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Type        string  `json:"type"`
	Priority    string  `json:"priority"`
	ParentID    *string `json:"parent_id,omitempty"`
}

type applyTaskUpdateBody struct {
	TaskID      string `json:"task_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Priority    string `json:"priority"`
}

type applyTaskStatusBody struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type applyTaskIDBody struct {
	TaskID string `json:"task_id"`
}

// ValidateApplyRequestBody parses and validates a bulk apply request payload.
func ValidateApplyRequestBody(body []byte) (*protocol.ApplyRequestBody, error) {
	var req protocol.ApplyRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, &protocol.ApplyValidationError{
			Code:    protocol.ErrorCodeInvalidRequest,
			Message: fmt.Sprintf("invalid apply request body: %v", err),
		}
	}

	if err := ValidateApplyRequest(req); err != nil {
		return nil, err
	}

	return &req, nil
}

// ValidateApplyRequest validates the normalized bulk apply request contract.
func ValidateApplyRequest(req protocol.ApplyRequestBody) error {
	if req.SchemaVersion != protocol.ApplySchemaVersion {
		return &protocol.ApplyValidationError{
			Code:    protocol.ErrorCodeInvalidRequest,
			Message: fmt.Sprintf("unsupported apply schema version: %d", req.SchemaVersion),
		}
	}

	if req.SnapshotRevision == 0 {
		return &protocol.ApplyValidationError{
			Code:    protocol.ErrorCodeInvalidRequest,
			Message: "missing or invalid snapshot revision",
		}
	}

	if len(req.Operations) == 0 {
		return &protocol.ApplyValidationError{
			Code:    protocol.ErrorCodeInvalidRequest,
			Message: "apply request requires at least one operation",
		}
	}

	diagnostics := make([]protocol.ApplyValidationDiagnostic, 0)
	for i, op := range req.Operations {
		diagnostics = append(diagnostics, validateApplyOperation(i, op)...)
	}

	if len(diagnostics) > 0 {
		return &protocol.ApplyValidationError{
			Code:        protocol.ErrorCodeInvalidRequest,
			Message:     "apply request failed validation",
			Diagnostics: diagnostics,
		}
	}

	return nil
}

func validateApplyOperation(index int, op protocol.ApplyOperationBody) []protocol.ApplyValidationDiagnostic {
	if op.Command == "" {
		return []protocol.ApplyValidationDiagnostic{{
			Index:   index,
			Code:    protocol.ApplyValidationCodeInvalidOperationCommand,
			Field:   "command",
			Message: "missing required field: command",
		}}
	}

	if !isSupportedApplyCommand(op.Command) {
		return []protocol.ApplyValidationDiagnostic{{
			Index:   index,
			Code:    protocol.ApplyValidationCodeInvalidOperationCommand,
			Field:   "command",
			Message: fmt.Sprintf("unsupported apply command: %s", op.Command),
		}}
	}

	if len(op.Body) == 0 || string(op.Body) == "null" {
		return []protocol.ApplyValidationDiagnostic{{
			Index:   index,
			Code:    protocol.ApplyValidationCodeInvalidOperationBody,
			Field:   "body",
			Message: "missing required field: body",
		}}
	}

	switch op.Command {
	case applyCommandTaskCreate:
		var payload applyTaskCreateBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return []protocol.ApplyValidationDiagnostic{{
				Index:   index,
				Code:    protocol.ApplyValidationCodeInvalidOperationBody,
				Field:   "body",
				Message: fmt.Sprintf("invalid task create body: %v", err),
			}}
		}
		return missingCreateDiagnostics(index, payload)
	case applyCommandTaskUpdate:
		var payload applyTaskUpdateBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return []protocol.ApplyValidationDiagnostic{{
				Index:   index,
				Code:    protocol.ApplyValidationCodeInvalidOperationBody,
				Field:   "body",
				Message: fmt.Sprintf("invalid task update body: %v", err),
			}}
		}
		return missingUpdateDiagnostics(index, payload)
	case applyCommandTaskUpdateStatus:
		var payload applyTaskStatusBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return []protocol.ApplyValidationDiagnostic{{
				Index:   index,
				Code:    protocol.ApplyValidationCodeInvalidOperationBody,
				Field:   "body",
				Message: fmt.Sprintf("invalid task status body: %v", err),
			}}
		}
		return missingStatusDiagnostics(index, payload)
	case applyCommandTaskDelete, applyCommandTaskArchive:
		var payload applyTaskIDBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return []protocol.ApplyValidationDiagnostic{{
				Index:   index,
				Code:    protocol.ApplyValidationCodeInvalidOperationBody,
				Field:   "body",
				Message: fmt.Sprintf("invalid task id body: %v", err),
			}}
		}
		return missingTaskIDDiagnostics(index, payload)
	default:
		return []protocol.ApplyValidationDiagnostic{{
			Index:   index,
			Code:    protocol.ApplyValidationCodeInvalidOperationCommand,
			Field:   "command",
			Message: fmt.Sprintf("unsupported apply command: %s", op.Command),
		}}
	}
}

func isSupportedApplyCommand(command string) bool {
	switch command {
	case applyCommandTaskCreate,
		applyCommandTaskUpdateStatus,
		applyCommandTaskUpdate,
		applyCommandTaskDelete,
		applyCommandTaskArchive:
		return true
	default:
		return false
	}
}

func missingCreateDiagnostics(index int, payload applyTaskCreateBody) []protocol.ApplyValidationDiagnostic {
	diagnostics := make([]protocol.ApplyValidationDiagnostic, 0, 4)
	if payload.Title == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "title"))
	}
	if payload.Description == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "description"))
	}
	if payload.Type == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "type"))
	}
	if payload.Priority == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "priority"))
	}
	return diagnostics
}

func missingUpdateDiagnostics(index int, payload applyTaskUpdateBody) []protocol.ApplyValidationDiagnostic {
	diagnostics := make([]protocol.ApplyValidationDiagnostic, 0, 5)
	if payload.TaskID == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "task_id"))
	}
	if payload.Title == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "title"))
	}
	if payload.Description == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "description"))
	}
	if payload.Type == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "type"))
	}
	if payload.Priority == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "priority"))
	}
	return diagnostics
}

func missingStatusDiagnostics(index int, payload applyTaskStatusBody) []protocol.ApplyValidationDiagnostic {
	diagnostics := make([]protocol.ApplyValidationDiagnostic, 0, 2)
	if payload.TaskID == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "task_id"))
	}
	if payload.Status == "" {
		diagnostics = append(diagnostics, missingFieldDiagnostic(index, "status"))
	}
	return diagnostics
}

func missingTaskIDDiagnostics(index int, payload applyTaskIDBody) []protocol.ApplyValidationDiagnostic {
	if payload.TaskID == "" {
		return []protocol.ApplyValidationDiagnostic{missingFieldDiagnostic(index, "task_id")}
	}
	return nil
}

func missingFieldDiagnostic(index int, field string) protocol.ApplyValidationDiagnostic {
	return protocol.ApplyValidationDiagnostic{
		Index:   index,
		Code:    protocol.ApplyValidationCodeMissingField,
		Field:   field,
		Message: fmt.Sprintf("missing required field: %s", field),
	}
}
