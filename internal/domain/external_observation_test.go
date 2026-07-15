package domain

import (
	"testing"
	"time"
)

func TestCurrentTmuxObservationProvenanceAdmission(t *testing.T) {
	provenance := CurrentTmuxObservationProvenance(time.Unix(123, 0))
	if err := ValidateCurrentExternalObservation(provenance, ExternalObservationProductSessionRuntime); err != nil {
		t.Fatalf("validate current tmux observation: %v", err)
	}
	for _, payloadClass := range []string{"timer_tick", "heartbeat", "activity_sample", "raw_terminal_bytes", "routine_poll", "session.runtime_observed"} {
		if err := ValidateCurrentExternalObservation(provenance, payloadClass); err == nil {
			t.Fatalf("payload class %q was admitted", payloadClass)
		}
	}
	provenance.Disposition = ExternalObservationMaterial
	if err := ValidateCurrentExternalObservation(provenance, ExternalObservationProductSessionRuntime); err == nil {
		t.Fatal("material observation was admitted through current-state path")
	}
}
