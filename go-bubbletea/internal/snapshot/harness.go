package snapshot

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// CriticalState captures profile-independent state that must stay stable.
type CriticalState struct {
	Mode           string
	Focus          string
	Overlay        string
	SelectionID    string
	OperationKind  string
	OperationState string
}

// Capture is a pinned snapshot for one test profile.
type Capture struct {
	Profile          string
	CapturedAt       time.Time
	State            CriticalState
	RawView          string
	NormalizedView   string
	NormalizedDigest string
}

// Mismatch describes one compare failure.
type Mismatch struct {
	Field    string
	Expected string
	Actual   string
}

// CompareResult is the output of comparing two captures.
type CompareResult struct {
	Equal      bool
	Mismatches []Mismatch
}

// Harness captures and compares profile-pinned snapshots.
type Harness struct{}

// NewHarness creates a deterministic snapshot harness.
func NewHarness() Harness {
	return Harness{}
}

// Capture creates a normalized snapshot for a profile.
func (h Harness) Capture(profile string, at time.Time, state CriticalState, view string) Capture {
	normalized := Normalize(view)

	return Capture{
		Profile:          profile,
		CapturedAt:       at,
		State:            state,
		RawView:          view,
		NormalizedView:   normalized,
		NormalizedDigest: buildDigest(normalized),
	}
}

// Compare performs profile-aware comparison across critical state and normalized content.
func (h Harness) Compare(expected Capture, actual Capture) CompareResult {
	var mismatches []Mismatch

	if expected.Profile != actual.Profile {
		mismatches = append(mismatches, Mismatch{Field: "profile", Expected: expected.Profile, Actual: actual.Profile})
	}

	if expected.State.Mode != actual.State.Mode {
		mismatches = append(mismatches, Mismatch{Field: "mode", Expected: expected.State.Mode, Actual: actual.State.Mode})
	}

	if expected.State.Focus != actual.State.Focus {
		mismatches = append(mismatches, Mismatch{Field: "focus", Expected: expected.State.Focus, Actual: actual.State.Focus})
	}

	if expected.State.Overlay != actual.State.Overlay {
		mismatches = append(mismatches, Mismatch{Field: "overlay", Expected: expected.State.Overlay, Actual: actual.State.Overlay})
	}

	if expected.State.SelectionID != actual.State.SelectionID {
		mismatches = append(mismatches, Mismatch{Field: "selectionID", Expected: expected.State.SelectionID, Actual: actual.State.SelectionID})
	}

	if expected.State.OperationKind != actual.State.OperationKind {
		mismatches = append(mismatches, Mismatch{Field: "operationKind", Expected: expected.State.OperationKind, Actual: actual.State.OperationKind})
	}

	if expected.State.OperationState != actual.State.OperationState {
		mismatches = append(mismatches, Mismatch{Field: "operationState", Expected: expected.State.OperationState, Actual: actual.State.OperationState})
	}

	if expected.NormalizedDigest != actual.NormalizedDigest {
		mismatches = append(mismatches, Mismatch{Field: "normalizedView", Expected: expected.NormalizedView, Actual: actual.NormalizedView})
	}

	return CompareResult{Equal: len(mismatches) == 0, Mismatches: mismatches}
}

// Normalize strips ANSI noise and enforces stable newline/spacing rules.
func Normalize(view string) string {
	if view == "" {
		return ""
	}

	stripped := ansi.Strip(view)
	stripped = strings.ReplaceAll(stripped, "\r\n", "\n")
	stripped = strings.ReplaceAll(stripped, "\r", "\n")

	lines := strings.Split(stripped, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}

	normalized := strings.Join(lines, "\n")
	normalized = strings.Trim(normalized, "\n")
	return normalized
}

func buildDigest(normalized string) string {
	if normalized == "" {
		return "0:0"
	}

	lineCount := 1
	for _, r := range normalized {
		if r == '\n' {
			lineCount++
		}
	}

	return strings.Join([]string{strconv.Itoa(lineCount), strconv.Itoa(len(normalized))}, ":")
}
