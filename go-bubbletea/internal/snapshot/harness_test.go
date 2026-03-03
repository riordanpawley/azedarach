package snapshot

import (
	"testing"
	"time"
)

func TestNormalizeRemovesANSINoiseAndNormalizesWhitespace(t *testing.T) {
	input := "\x1b[31mHeader\x1b[0m\r\nline 1   \r\nline 2\t\r\n"
	got := Normalize(input)
	want := "Header\nline 1\nline 2"

	if got != want {
		t.Fatalf("Normalize() = %q, want %q", got, want)
	}
}

func TestHarnessCaptureAndCompareProfilePinned(t *testing.T) {
	h := NewHarness()
	now := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	state := CriticalState{Mode: "board", Focus: "tasks", Overlay: "", SelectionID: "az-1", OperationKind: "sync", OperationState: "running"}

	expected := h.Capture("compact", now, state, "\x1b[32mok\x1b[0m\nvalue")
	actual := h.Capture("compact", now, state, "ok\nvalue")

	result := h.Compare(expected, actual)
	if !result.Equal {
		t.Fatalf("Compare() unexpectedly mismatched: %+v", result.Mismatches)
	}

	profileMismatch := h.Compare(expected, h.Capture("wide", now, state, "ok\nvalue"))
	if profileMismatch.Equal {
		t.Fatal("Compare() expected profile mismatch")
	}
}

func TestHarnessCompareDetectsCriticalStateMismatch(t *testing.T) {
	h := NewHarness()
	now := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)

	expected := h.Capture("compact", now, CriticalState{Mode: "board", Focus: "tasks"}, "stable")
	actual := h.Capture("compact", now, CriticalState{Mode: "overlay", Focus: "tasks"}, "stable")

	result := h.Compare(expected, actual)
	if result.Equal {
		t.Fatal("Compare() expected mismatch")
	}

	foundMode := false
	for _, mismatch := range result.Mismatches {
		if mismatch.Field == "mode" {
			foundMode = true
			break
		}
	}

	if !foundMode {
		t.Fatalf("expected mode mismatch in %+v", result.Mismatches)
	}
}
