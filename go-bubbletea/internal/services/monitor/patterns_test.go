package monitor

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestDetectState_WaitingPatterns(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"y/n prompt", "Do you want to continue? [y/n]"},
		{"Y/n prompt", "Apply changes? [Y/n]"},
		{"yes/no prompt", "Proceed with operation? [yes/no]"},
		{"Do you want", "Do you want to run the tests?"},
		{"AskUserQuestion", "AskUserQuestion: Confirm deletion?"},
		{"Press Enter", "Press Enter to continue..."},
		{"waiting for", "waiting for user input"},
		{"Approve", "Approve? [yes/no]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := DetectState(tt.output)
			if state != domain.SessionWaiting {
				t.Errorf("DetectState(%q) = %v, want %v", tt.output, state, domain.SessionWaiting)
			}
		})
	}
}

func TestDetectState_DonePatterns(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"Task completed", "Task completed successfully"},
		{"Successfully completed", "Successfully completed all operations"},
		{"All done", "All done! Your changes are ready"},
		{"check mark completed", "✓ Build completed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := DetectState(tt.output)
			if state != domain.SessionDone {
				t.Errorf("DetectState(%q) = %v, want %v", tt.output, state, domain.SessionDone)
			}
		})
	}
}

func TestDetectState_ErrorPatterns(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"Error prefix", "Error: file not found"},
		{"Exception", "Exception: null pointer dereference"},
		{"panic", "panic: runtime error"},
		{"FAILED", "FAILED: tests did not pass"},
		{"fatal error", "fatal error: out of memory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := DetectState(tt.output)
			if state != domain.SessionError {
				t.Errorf("DetectState(%q) = %v, want %v", tt.output, state, domain.SessionError)
			}
		})
	}
}

func TestDetectState_PriorityOrder(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected domain.SessionState
	}{
		{
			name:     "Error takes priority over Done",
			output:   "Task completed\nError: something went wrong",
			expected: domain.SessionError,
		},
		{
			name:     "Error takes priority over Waiting",
			output:   "Do you want to continue? [y/n]\nError: connection failed",
			expected: domain.SessionError,
		},
		{
			name:     "Done takes priority over Waiting",
			output:   "Waiting for input\nTask completed successfully",
			expected: domain.SessionDone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := DetectState(tt.output)
			if state != tt.expected {
				t.Errorf("DetectState(%q) = %v, want %v", tt.output, state, tt.expected)
			}
		})
	}
}

func TestDetectState_DefaultToBusy(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"empty output", ""},
		{"normal output", "Processing files...\nBuilding project\nRunning tests"},
		{"no matching patterns", "Just some random text\nwithout any special keywords"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := DetectState(tt.output)
			if state != domain.SessionBusy {
				t.Errorf("DetectState(%q) = %v, want %v", tt.output, state, domain.SessionBusy)
			}
		})
	}
}

func TestDetectState_LineLimit(t *testing.T) {
	// Create output with 150 lines
	var lines []string
	for i := 0; i < 150; i++ {
		if i == 0 {
			// Put error pattern in first line (should be ignored)
			lines = append(lines, "Error: this is old")
		} else if i == 149 {
			// Put waiting pattern in last line (should be detected)
			lines = append(lines, "Do you want to continue? [y/n]")
		} else {
			lines = append(lines, "normal output line")
		}
	}
	output := strings.Join(lines, "\n")

	state := DetectState(output)
	if state != domain.SessionWaiting {
		t.Errorf("DetectState() should only check last 100 lines, got %v, want %v", state, domain.SessionWaiting)
	}
}

func TestDetectState_ExactlyOneHundredLines(t *testing.T) {
	// Create output with exactly 100 lines
	var lines []string
	for i := 0; i < 100; i++ {
		if i == 99 {
			lines = append(lines, "Task completed")
		} else {
			lines = append(lines, "normal output")
		}
	}
	output := strings.Join(lines, "\n")

	state := DetectState(output)
	if state != domain.SessionDone {
		t.Errorf("DetectState() = %v, want %v", state, domain.SessionDone)
	}
}

func TestDetectState_MultilinePatterns(t *testing.T) {
	output := `Starting task...
Processing files...
Running tests...
Do you want to continue? [y/n]
Waiting for response...`

	state := DetectState(output)
	if state != domain.SessionWaiting {
		t.Errorf("DetectState() should detect waiting pattern in multiline output, got %v, want %v", state, domain.SessionWaiting)
	}
}

func TestDetectState_CaseInsensitive(t *testing.T) {
	// Some patterns should be case-sensitive (like [y/n])
	// but some patterns might have case variations
	tests := []struct {
		name     string
		output   string
		expected domain.SessionState
	}{
		{"lowercase error", "error: something failed", domain.SessionBusy}, // Should not match (case-sensitive)
		{"uppercase ERROR", "ERROR: something failed", domain.SessionBusy},  // Should not match
		{"exact Error", "Error: something failed", domain.SessionError},     // Should match
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := DetectState(tt.output)
			if state != tt.expected {
				t.Errorf("DetectState(%q) = %v, want %v", tt.output, state, tt.expected)
			}
		})
	}
}
