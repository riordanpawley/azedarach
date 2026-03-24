package protocol

import (
	"encoding/json"
	"testing"
)

func TestApplyValidationCodeTaxonomy(t *testing.T) {
	cases := []struct {
		name  string
		code  ApplyValidationCode
		valid bool
	}{
		{name: "invalid schema version", code: ApplyValidationCodeInvalidSchemaVersion, valid: true},
		{name: "missing field", code: ApplyValidationCodeMissingField, valid: true},
		{name: "unknown literal", code: ApplyValidationCode("other"), valid: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.code.Valid(); got != tc.valid {
				t.Fatalf("Valid() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func TestApplyRequestBodyJSONRoundTrip(t *testing.T) {
	want := ApplyRequestBody{
		SchemaVersion:    ApplySchemaVersion,
		SnapshotRevision: 42,
		DryRun:           true,
		Operations: []ApplyOperationBody{
			{
				Command: CommandTaskBulkApply,
				Body:    json.RawMessage(`{"schema_version":1}`),
			},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal apply request: %v", err)
	}

	var got ApplyRequestBody
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal apply request: %v", err)
	}

	if got.SchemaVersion != want.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, want.SchemaVersion)
	}
	if got.SnapshotRevision != want.SnapshotRevision {
		t.Fatalf("SnapshotRevision = %d, want %d", got.SnapshotRevision, want.SnapshotRevision)
	}
	if got.DryRun != want.DryRun {
		t.Fatalf("DryRun = %v, want %v", got.DryRun, want.DryRun)
	}
	if len(got.Operations) != 1 {
		t.Fatalf("Operations len = %d, want 1", len(got.Operations))
	}
	if got.Operations[0].Command != CommandTaskBulkApply {
		t.Fatalf("Operations[0].Command = %q, want %q", got.Operations[0].Command, CommandTaskBulkApply)
	}
	if string(got.Operations[0].Body) != `{"schema_version":1}` {
		t.Fatalf("Operations[0].Body = %s, want {\"schema_version\":1}", string(got.Operations[0].Body))
	}
}

func TestApplyValidationErrorString(t *testing.T) {
	err := &ApplyValidationError{
		Code:    ErrorCodeInvalidRequest,
		Message: "apply request failed validation",
		Diagnostics: []ApplyValidationDiagnostic{
			{Index: 0, Code: ApplyValidationCodeMissingField, Field: "task_id", Message: "missing required field: task_id"},
		},
	}

	if got, want := err.Error(), "invalid_request: apply request failed validation (1 diagnostics)"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
