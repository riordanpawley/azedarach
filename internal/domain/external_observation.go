package domain

import (
	"fmt"
	"strings"
	"time"
)

// ExternalObservationDisposition distinguishes disposable current-state
// products from observations explicitly admitted by a future semantic
// authority. Daemon pollers may only produce the current disposition.
type ExternalObservationDisposition string

const (
	ExternalObservationCurrent  ExternalObservationDisposition = "current"
	ExternalObservationMaterial ExternalObservationDisposition = "material"

	ExternalObservationAuthorityTmux         = "tmux"
	ExternalObservationProductSessionRuntime = "session_runtime"
)

// ExternalObservationProvenance describes the authority and admission status
// of an external observation without turning polling cadence into history.
type ExternalObservationProvenance struct {
	Authority                string
	Product                  string
	Disposition              ExternalObservationDisposition
	ObservedAt               time.Time
	CanonicalEventAdmitted   bool
	SemanticSequenceAdvanced bool
}

// CurrentTmuxObservationProvenance constructs provenance for a disposable
// current-state tmux product. It is deliberately incapable of admitting a
// canonical event or advancing a semantic sequence.
func CurrentTmuxObservationProvenance(observedAt time.Time) ExternalObservationProvenance {
	return ExternalObservationProvenance{
		Authority:   ExternalObservationAuthorityTmux,
		Product:     ExternalObservationProductSessionRuntime,
		Disposition: ExternalObservationCurrent,
		ObservedAt:  observedAt.UTC(),
	}
}

// ValidateCurrentExternalObservation rejects payload classes that are noisy
// samples rather than complete current-state projection values.
func ValidateCurrentExternalObservation(provenance ExternalObservationProvenance, payloadClass string) error {
	if strings.TrimSpace(provenance.Authority) == "" || strings.TrimSpace(provenance.Product) == "" || provenance.ObservedAt.IsZero() {
		return fmt.Errorf("external observation provenance is incomplete")
	}
	if provenance.Disposition != ExternalObservationCurrent {
		return fmt.Errorf("daemon observer may only publish current external observations")
	}
	if provenance.CanonicalEventAdmitted || provenance.SemanticSequenceAdvanced {
		return fmt.Errorf("current external observation cannot admit a canonical event or advance semantic sequence")
	}
	switch strings.TrimSpace(payloadClass) {
	case ExternalObservationProductSessionRuntime:
		return nil
	case "timer_tick", "heartbeat", "activity_sample", "raw_terminal_bytes", "routine_poll", "session.runtime_observed":
		return fmt.Errorf("external observation payload class %q is not admissible as a current-state projection", payloadClass)
	default:
		return fmt.Errorf("unknown external observation payload class %q", payloadClass)
	}
}
