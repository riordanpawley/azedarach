package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTaskBulkCleanupProtocolRoundTripPreservesTimeoutDetails(t *testing.T) {
	timedOut := true
	want := TaskBulkCleanupResult{
		Action: "cancelled",
		Items: []TaskBulkCleanupItem{{
			TaskID: "az-1",
			Action: "cancelled",
			Success: true,
			Result: &TaskCloseResult{
				TaskID: "az-1",
				Status: "cancelled",
				Phases: []TaskClosePhaseTiming{{Name: "session_stop", ElapsedMS: 25, TimedOut: &timedOut}},
			},
		}},
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got TaskBulkCleanupResult
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Result == nil || len(got.Items[0].Result.Phases) != 1 {
		t.Fatalf("round trip result = %+v", got)
	}
	phase := got.Items[0].Result.Phases[0]
	if phase.TimedOut == nil || !*phase.TimedOut || phase.Elapsed() != 25*time.Millisecond {
		t.Fatalf("round trip phase = %+v", phase)
	}
}
