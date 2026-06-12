package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestValidateApplyRequestBody(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 7,
		DryRun:           true,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]string{
					"title":       "Add task",
					"description": "Draft task",
					"type":        "task",
					"priority":    "high",
				}),
			},
		},
	}

	body := mustJSON(t, req)
	got, err := ValidateApplyRequestBody(body)
	if err != nil {
		t.Fatalf("ValidateApplyRequestBody() error = %v", err)
	}
	if got.SchemaVersion != protocol.ApplySchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, protocol.ApplySchemaVersion)
	}
	if got.SnapshotRevision != 7 {
		t.Fatalf("SnapshotRevision = %d, want 7", got.SnapshotRevision)
	}
	if !got.DryRun {
		t.Fatalf("DryRun = %v, want true", got.DryRun)
	}
}

func TestValidateApplyRequest_InvalidTopLevel(t *testing.T) {
	tests := []struct {
		name string
		req  protocol.ApplyRequestBody
		want string
	}{
		{
			name: "schema version",
			req: protocol.ApplyRequestBody{
				SchemaVersion:    protocol.ApplySchemaVersion + 1,
				SnapshotRevision: 1,
				Operations:       []protocol.ApplyOperationBody{{Command: applyCommandTaskDelete, Body: mustApplyJSON(t, map[string]string{"task_id": "T-1"})}},
			},
			want: "unsupported apply schema version: 3",
		},
		{
			name: "snapshot revision",
			req: protocol.ApplyRequestBody{
				SchemaVersion:    protocol.ApplySchemaVersion,
				SnapshotRevision: 0,
				Operations:       []protocol.ApplyOperationBody{{Command: applyCommandTaskDelete, Body: mustApplyJSON(t, map[string]string{"task_id": "T-1"})}},
			},
			want: "missing or invalid snapshot revision",
		},
		{
			name: "missing operations",
			req: protocol.ApplyRequestBody{
				SchemaVersion:    protocol.ApplySchemaVersion,
				SnapshotRevision: 1,
			},
			want: "apply request requires at least one operation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateApplyRequest(tc.req)
			if err == nil {
				t.Fatal("expected validation error")
			}

			vErr, ok := err.(*protocol.ApplyValidationError)
			if !ok {
				t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
			}
			if vErr.Code != protocol.ErrorCodeInvalidRequest {
				t.Fatalf("Code = %q, want %q", vErr.Code, protocol.ErrorCodeInvalidRequest)
			}
			if vErr.Message != tc.want {
				t.Fatalf("Message = %q, want %q", vErr.Message, tc.want)
			}
			if len(vErr.Diagnostics) != 0 {
				t.Fatalf("Diagnostics = %+v, want none", vErr.Diagnostics)
			}
		})
	}
}

func TestValidateApplyRequest_PerItemDiagnostics(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 99,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskUpdateStatus,
				Body:    mustApplyJSON(t, map[string]string{"status": "in_review"}),
			},
			{
				Command: applyCommandTaskDelete,
				Body:    mustApplyJSON(t, map[string]string{}),
			},
			{
				Command: "task.relabel",
				Body:    mustApplyJSON(t, map[string]string{"task_id": "T-3"}),
			},
		},
	}

	err := ValidateApplyRequest(req)
	if err == nil {
		t.Fatal("expected validation error")
	}

	vErr, ok := err.(*protocol.ApplyValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
	}

	if got, want := len(vErr.Diagnostics), 3; got != want {
		t.Fatalf("Diagnostics len = %d, want %d", got, want)
	}

	if vErr.Diagnostics[0].Index != 0 {
		t.Fatalf("diag[0].Index = %d, want 0", vErr.Diagnostics[0].Index)
	}
	if vErr.Diagnostics[0].Field != "task_id" {
		t.Fatalf("diag[0].Field = %q, want task_id", vErr.Diagnostics[0].Field)
	}
	if vErr.Diagnostics[0].Code != protocol.ApplyValidationCodeMissingField {
		t.Fatalf("diag[0].Code = %q, want %q", vErr.Diagnostics[0].Code, protocol.ApplyValidationCodeMissingField)
	}

	if vErr.Diagnostics[1].Index != 1 {
		t.Fatalf("diag[1].Index = %d, want 1", vErr.Diagnostics[1].Index)
	}
	if vErr.Diagnostics[1].Field != "task_id" {
		t.Fatalf("diag[1].Field = %q, want task_id", vErr.Diagnostics[1].Field)
	}

	if vErr.Diagnostics[2].Index != 2 {
		t.Fatalf("diag[2].Index = %d, want 2", vErr.Diagnostics[2].Index)
	}
	if vErr.Diagnostics[2].Code != protocol.ApplyValidationCodeInvalidOperationCommand {
		t.Fatalf("diag[2].Code = %q, want %q", vErr.Diagnostics[2].Code, protocol.ApplyValidationCodeInvalidOperationCommand)
	}
}

func TestValidateApplyRequestRejectsStatusCloseMutation(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 99,
		Operations: []protocol.ApplyOperationBody{{
			Command: applyCommandTaskUpdateStatus,
			Body:    mustApplyJSON(t, map[string]string{"task_id": "az-1", "status": "closed"}),
		}},
	}

	err := ValidateApplyRequest(req)
	if err == nil {
		t.Fatal("expected validation error")
	}
	vErr, ok := err.(*protocol.ApplyValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
	}
	if got, want := len(vErr.Diagnostics), 1; got != want {
		t.Fatalf("Diagnostics len = %d, want %d", got, want)
	}
	diag := vErr.Diagnostics[0]
	if diag.Index != 0 || diag.Field != "status" || diag.Code != protocol.ApplyValidationCodeInvalidOperationBody || !strings.Contains(diag.Message, "task.close") {
		t.Fatalf("diagnostic = %+v, want status task.close rejection", diag)
	}
}

func TestValidateApplyRequestRejectsDuplicateCreateRefs(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 99,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]string{
					"title":       "First",
					"description": "One",
					"type":        "task",
					"priority":    "P2",
					"ref":         "same",
				}),
			},
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]string{
					"title":       "Second",
					"description": "Two",
					"type":        "task",
					"priority":    "P2",
					"ref":         " same ",
				}),
			},
		},
	}

	err := ValidateApplyRequest(req)
	if err == nil {
		t.Fatal("expected validation error")
	}
	vErr, ok := err.(*protocol.ApplyValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
	}
	if got, want := len(vErr.Diagnostics), 1; got != want {
		t.Fatalf("Diagnostics len = %d, want %d", got, want)
	}
	diag := vErr.Diagnostics[0]
	if diag.Index != 1 || diag.Field != "ref" || diag.Code != protocol.ApplyValidationCodeInvalidOperationBody || !strings.Contains(diag.Message, `duplicate ref "same" already used by operation 0`) {
		t.Fatalf("diagnostic = %+v, want duplicate ref rejection", diag)
	}
}

func TestValidateApplyRequestRejectsUnresolvedParentRef(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 99,
		DryRun:           true,
		Operations: []protocol.ApplyOperationBody{{
			Command: applyCommandTaskCreate,
			Body: mustApplyJSON(t, map[string]string{
				"title":       "Child",
				"description": "Nested",
				"type":        "task",
				"priority":    "P2",
				"parent_ref":  "missing",
			}),
		}},
	}

	err := ValidateApplyRequest(req)
	if err == nil {
		t.Fatal("expected validation error")
	}
	vErr, ok := err.(*protocol.ApplyValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
	}
	if got, want := len(vErr.Diagnostics), 1; got != want {
		t.Fatalf("Diagnostics len = %d, want %d", got, want)
	}
	diag := vErr.Diagnostics[0]
	if diag.Index != 0 || diag.Field != "parent_ref" || diag.Code != protocol.ApplyValidationCodeInvalidOperationBody || !strings.Contains(diag.Message, `unresolved parent_ref "missing"`) {
		t.Fatalf("diagnostic = %+v, want unresolved parent_ref rejection", diag)
	}
}

func TestValidateApplyRequestRejectsParentIDAndParentRefTogether(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 99,
		DryRun:           true,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]string{
					"title":       "Parent",
					"description": "Root",
					"type":        "epic",
					"priority":    "P2",
					"ref":         "parent",
				}),
			},
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]string{
					"title":       "Child",
					"description": "Nested",
					"type":        "task",
					"priority":    "P2",
					"parent_id":   "az-explicit",
					"parent_ref":  "parent",
				}),
			},
		},
	}

	err := ValidateApplyRequest(req)
	if err == nil {
		t.Fatal("expected validation error")
	}
	vErr, ok := err.(*protocol.ApplyValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
	}
	if got, want := len(vErr.Diagnostics), 1; got != want {
		t.Fatalf("Diagnostics len = %d, want %d", got, want)
	}
	diag := vErr.Diagnostics[0]
	if diag.Index != 1 || diag.Field != "parent_ref" || diag.Code != protocol.ApplyValidationCodeInvalidOperationBody || !strings.Contains(diag.Message, "parent_id and parent_ref are mutually exclusive") {
		t.Fatalf("diagnostic = %+v, want parent_id/parent_ref rejection", diag)
	}
}

func TestValidateApplyRequest_CommandBodyDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		req    protocol.ApplyRequestBody
		fields []string
	}{
		{
			name: "task create",
			req: protocol.ApplyRequestBody{
				SchemaVersion:    protocol.ApplySchemaVersion,
				SnapshotRevision: 8,
				Operations: []protocol.ApplyOperationBody{{
					Command: applyCommandTaskCreate,
					Body:    mustApplyJSON(t, map[string]string{}),
				}},
			},
			fields: []string{"title", "description", "type", "priority"},
		},
		{
			name: "task update",
			req: protocol.ApplyRequestBody{
				SchemaVersion:    protocol.ApplySchemaVersion,
				SnapshotRevision: 8,
				Operations: []protocol.ApplyOperationBody{{
					Command: applyCommandTaskUpdate,
					Body:    mustApplyJSON(t, map[string]string{}),
				}},
			},
			fields: []string{"task_id", "title", "description", "type", "priority"},
		},
		{
			name: "dependency add",
			req: protocol.ApplyRequestBody{
				SchemaVersion:    protocol.ApplySchemaVersion,
				SnapshotRevision: 8,
				Operations: []protocol.ApplyOperationBody{{
					Command: applyCommandDependencyAdd,
					Body:    mustApplyJSON(t, map[string]string{}),
				}},
			},
			fields: []string{"task_id", "depends_on_id", "type"},
		},
		{
			name: "dependency remove",
			req: protocol.ApplyRequestBody{
				SchemaVersion:    protocol.ApplySchemaVersion,
				SnapshotRevision: 8,
				Operations: []protocol.ApplyOperationBody{{
					Command: applyCommandDependencyRemove,
					Body:    mustApplyJSON(t, map[string]any{"confirm": false}),
				}},
			},
			fields: []string{"task_id", "depends_on_id", "type", "confirm"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateApplyRequest(tc.req)
			if err == nil {
				t.Fatal("expected validation error")
			}

			vErr, ok := err.(*protocol.ApplyValidationError)
			if !ok {
				t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
			}
			if len(vErr.Diagnostics) != len(tc.fields) {
				t.Fatalf("Diagnostics len = %d, want %d", len(vErr.Diagnostics), len(tc.fields))
			}
			for i, field := range tc.fields {
				if vErr.Diagnostics[i].Index != 0 {
					t.Fatalf("diag[%d].Index = %d, want 0", i, vErr.Diagnostics[i].Index)
				}
				if vErr.Diagnostics[i].Code != protocol.ApplyValidationCodeMissingField {
					t.Fatalf("diag[%d].Code = %q, want %q", i, vErr.Diagnostics[i].Code, protocol.ApplyValidationCodeMissingField)
				}
				if vErr.Diagnostics[i].Field != field {
					t.Fatalf("diag[%d].Field = %q, want %q", i, vErr.Diagnostics[i].Field, field)
				}
				if vErr.Diagnostics[i].Message != "missing required field: "+field {
					t.Fatalf("diag[%d].Message = %q, want missing required field: %s", i, vErr.Diagnostics[i].Message, field)
				}
			}
		})
	}
}

func TestValidateApplyRequestBody_InvalidJSON(t *testing.T) {
	_, err := ValidateApplyRequestBody([]byte(`{"schema_version":1,"snapshot_revision":1,"dry_run":false,"operations":[`))
	if err == nil {
		t.Fatal("expected JSON decode error")
	}

	vErr, ok := err.(*protocol.ApplyValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *protocol.ApplyValidationError", err)
	}
	if vErr.Message == "" {
		t.Fatal("expected error message")
	}
}

func mustApplyJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}

	return json.RawMessage(data)
}
