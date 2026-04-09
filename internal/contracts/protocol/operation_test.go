package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestOperationStateValid(t *testing.T) {
	cases := []struct {
		name  string
		state OperationState
		valid bool
	}{
		{name: "queued", state: OperationStateQueued, valid: true},
		{name: "running", state: OperationStateRunning, valid: true},
		{name: "done", state: OperationStateDone, valid: true},
		{name: "failed", state: OperationStateFailed, valid: true},
		{name: "cancelled", state: OperationStateCancelled, valid: true},
		{name: "unknown", state: OperationState("other"), valid: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.Valid(); got != tc.valid {
				t.Fatalf("Valid() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func TestOperationSubmitRequestBodyJSONRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	startedAt := now.Add(5 * time.Second)
	finishedAt := now.Add(10 * time.Second)

	want := OperationSubmitResponseBody{
		Created: true,
		Operation: OperationRecord{
			OperationID:  "op-1",
			ProjectID:    "proj",
			Kind:         "session.start",
			IssueID:      naming.IssueID("az-1"),
			DedupeKey:    "proj::az-1::session.start",
			ResourceKeys: []string{"issue:az-1", "session:az-1"},
			State:        OperationStateDone,
			Progress: &OperationProgress{
				Message: "completed",
				Current: 1,
				Total:   1,
				Unit:    "steps",
				Percent: 100,
			},
			Payload:    json.RawMessage(`{"base_branch":"main"}`),
			Result:     json.RawMessage(`{"session_id":"az-1"}`),
			EnqueuedAt: now,
			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal operation submit response: %v", err)
	}

	var got OperationSubmitResponseBody
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal operation submit response: %v", err)
	}

	if !got.Created {
		t.Fatal("Created = false, want true")
	}
	if got.Operation.OperationID != want.Operation.OperationID {
		t.Fatalf("OperationID = %q, want %q", got.Operation.OperationID, want.Operation.OperationID)
	}
	if got.Operation.State != OperationStateDone {
		t.Fatalf("State = %q, want %q", got.Operation.State, OperationStateDone)
	}
	if len(got.Operation.ResourceKeys) != 2 {
		t.Fatalf("ResourceKeys len = %d, want 2", len(got.Operation.ResourceKeys))
	}
	if string(got.Operation.Payload) != `{"base_branch":"main"}` {
		t.Fatalf("Payload = %s, want base branch JSON", string(got.Operation.Payload))
	}
	if got.Operation.Progress == nil || got.Operation.Progress.Percent != 100 {
		t.Fatalf("Progress = %+v, want percent 100", got.Operation.Progress)
	}
	if got.Operation.StartedAt == nil || !got.Operation.StartedAt.Equal(startedAt) {
		t.Fatalf("StartedAt = %v, want %v", got.Operation.StartedAt, startedAt)
	}
	if got.Operation.FinishedAt == nil || !got.Operation.FinishedAt.Equal(finishedAt) {
		t.Fatalf("FinishedAt = %v, want %v", got.Operation.FinishedAt, finishedAt)
	}
}

func TestOperationProgressEventBodyJSONRoundTrip(t *testing.T) {
	want := OperationProgressEventBody{
		OperationID: "op-2",
		ProjectID:   "proj",
		State:       OperationStateRunning,
		Progress: OperationProgress{
			Message: "fetching origin",
			Current: 2,
			Total:   4,
			Unit:    "steps",
			Percent: 50,
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal progress event: %v", err)
	}

	var got OperationProgressEventBody
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal progress event: %v", err)
	}

	if got.OperationID != want.OperationID {
		t.Fatalf("OperationID = %q, want %q", got.OperationID, want.OperationID)
	}
	if got.State != OperationStateRunning {
		t.Fatalf("State = %q, want %q", got.State, OperationStateRunning)
	}
	if got.Progress.Message != want.Progress.Message || got.Progress.Percent != 50 {
		t.Fatalf("Progress = %+v, want %+v", got.Progress, want.Progress)
	}
}
