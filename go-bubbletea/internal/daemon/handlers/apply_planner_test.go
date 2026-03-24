package handlers

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestBuildApplyDryRunPreview_DeterministicAndIsolated(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 17,
		DryRun:           true,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskCreate,
				Body:    mustApplyPreviewJSON(t, map[string]string{"title": "First", "description": "Draft", "type": "task", "priority": "high"}),
			},
			{
				Command: applyCommandTaskDelete,
				Body:    mustApplyPreviewJSON(t, map[string]string{"task_id": "T-9"}),
			},
		},
	}

	preview1 := BuildApplyDryRunPreview(req)
	preview2 := BuildApplyDryRunPreview(req)

	if !reflect.DeepEqual(preview1, preview2) {
		t.Fatalf("preview mismatch on repeated build\npreview1=%+v\npreview2=%+v", preview1, preview2)
	}

	if got, want := preview1.SchemaVersion, protocol.ApplySchemaVersion; got != want {
		t.Fatalf("SchemaVersion = %d, want %d", got, want)
	}
	if got, want := preview1.SnapshotRevision, uint64(17); got != want {
		t.Fatalf("SnapshotRevision = %d, want %d", got, want)
	}
	if !preview1.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if got, want := len(preview1.Operations), 2; got != want {
		t.Fatalf("Operations len = %d, want %d", got, want)
	}
	if preview1.Operations[0].Index != 0 || preview1.Operations[1].Index != 1 {
		t.Fatalf("unexpected preview indexes: %+v", preview1.Operations)
	}

	originalFirst := append([]byte(nil), req.Operations[0].Body...)
	req.Operations[0].Body[0] = '!'
	if string(preview1.Operations[0].Body) != string(originalFirst) {
		t.Fatalf("preview body mutated when input changed: got %s, want %s", string(preview1.Operations[0].Body), string(originalFirst))
	}
	if string(req.Operations[0].Body) == string(originalFirst) {
		t.Fatal("expected caller mutation to affect only the request, not the preview")
	}
}

func TestEvaluateApplyRevisionGate(t *testing.T) {
	req := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 42,
		DryRun:           true,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskArchive,
				Body:    mustApplyPreviewJSON(t, map[string]string{"task_id": "T-7"}),
			},
		},
	}

	t.Run("match", func(t *testing.T) {
		got := EvaluateApplyRevisionGate(req, 42)
		if !got.Allowed {
			t.Fatal("Allowed = false, want true")
		}
		if got.Error != nil {
			t.Fatalf("Error = %+v, want nil", got.Error)
		}
		if got.CurrentRevision != 42 {
			t.Fatalf("CurrentRevision = %d, want 42", got.CurrentRevision)
		}
		if got.SnapshotRevision != 42 {
			t.Fatalf("SnapshotRevision = %d, want 42", got.SnapshotRevision)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		got := EvaluateApplyRevisionGate(req, 41)
		if got.Allowed {
			t.Fatal("Allowed = true, want false")
		}
		if got.Error == nil {
			t.Fatal("Error = nil, want revision gap envelope")
		}
		if got.Error.Code != protocol.ErrorCodeRevisionGap {
			t.Fatalf("Error.Code = %q, want %q", got.Error.Code, protocol.ErrorCodeRevisionGap)
		}
		if !got.Error.Retryable {
			t.Fatal("Error.Retryable = false, want true")
		}
		if got.Error.Message != "snapshot revision 42 does not match current revision 41" {
			t.Fatalf("Error.Message = %q, want deterministic mismatch message", got.Error.Message)
		}
	})
}

func mustApplyPreviewJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}

	return json.RawMessage(data)
}
