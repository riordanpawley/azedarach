package daemon

import "testing"

func TestInvariantSourceMatrixIncludesExpectedRuntimeInvariants(t *testing.T) {
	matrix := invariantSourceMatrix()
	expected := map[daemonInvariantID]daemonInvariantSource{
		daemonInvariantSessionStartConflict:   daemonInvariantSourceTmux,
		daemonInvariantSessionAttachTarget:    daemonInvariantSourceTmux,
		daemonInvariantSessionStopTargets:     daemonInvariantSourceTmux,
		daemonInvariantSessionReconcile:       daemonInvariantSourceHybrid,
		daemonInvariantTaskListFreshness:      daemonInvariantSourceProjection,
		daemonInvariantRuntimeKnownProjectIDs: daemonInvariantSourceProjection,
	}
	for id, want := range expected {
		got, ok := matrix[id]
		if !ok {
			t.Fatalf("missing invariant %q in source matrix", id)
		}
		if got != want {
			t.Fatalf("source matrix[%q] = %q, want %q", id, got, want)
		}
	}
}

func TestSourceForInvariantDefaultsToProjection(t *testing.T) {
	if got, want := sourceForInvariant(daemonInvariantID("unknown.invariant")), daemonInvariantSourceProjection; got != want {
		t.Fatalf("sourceForInvariant(unknown) = %q, want %q", got, want)
	}
}
