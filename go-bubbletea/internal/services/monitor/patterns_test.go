package monitor

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// ============================================================================
// WAITING PATTERN TESTS
// ============================================================================

func TestDetectState_WaitingPatterns(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		// Standard prompts
		{"y/n prompt", "Do you want to continue? [y/n]"},
		{"Y/n prompt", "Apply changes? [Y/n]"},
		{"yes/no prompt", "Proceed with operation? [yes/no]"},
		{"case insensitive y/n", "Continue? [Y/N]"},

		// Question patterns
		{"Do you want", "Do you want to run the tests?"},
		{"Would you like", "Would you like me to fix this?"},
		{"Continue question", "Continue? [y/n]"},
		{"Proceed question", "Proceed with deletion? [yes/no]"},
		{"Approve question", "Approve? [yes/no]"},

		// AskUserQuestion tool patterns
		{"AskUserQuestion tool", "AskUserQuestion: Confirm deletion?"},
		{"numbered Other option", "  1. Other"},
		{"Other describe", "Other (describe your choice)"},
		{"select option", "Please select an option from the list"},
		{"choose option", "Choose an option:"},
		{"enter number", "Enter a number (1-5):"},
		{"type number select", "Type a number to select your choice"},

		// Input prompts
		{"Press Enter", "Press Enter to continue..."},
		{"Press any key", "Press any key when ready"},
		{"waiting for input", "waiting for user input"},
		{"waiting for response", "waiting for your response"},
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

// ============================================================================
// ERROR PATTERN TESTS
// ============================================================================

func TestDetectState_ErrorPatterns_Generic(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"Error prefix", "Error: file not found"},
		{"Exception", "Exception: null pointer dereference"},
		{"Failed", "Failed: operation did not complete"},
		{"FAILED caps", "FAILED: tests did not pass"},
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

func TestDetectState_ErrorPatterns_StackTraces(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"panic", "panic: runtime error: invalid memory address"},
		{"fatal error", "fatal error: out of memory"},
		{"stack trace", "stack trace: goroutine 1"},
		{"stack trace line", "    at /home/user/file.go:42:15"},
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

func TestDetectState_ErrorPatterns_FileSystem(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"ENOENT", "Error: ENOENT: no such file or directory"},
		{"EACCES", "Error: EACCES: permission denied"},
		{"EEXIST", "Error: EEXIST: file already exists"},
		{"EISDIR", "Error: EISDIR: illegal operation on a directory"},
		{"ENOTDIR", "Error: ENOTDIR: not a directory"},
		{"EMFILE", "Error: EMFILE: too many open files"},
		{"ENOSPC", "Error: ENOSPC: no space left on device"},
		{"permission denied", "bash: /usr/bin/foo: Permission denied"},
		{"file not found", "Error: file not found at path"},
		{"no such file", "ls: cannot access 'foo': No such file or directory"},
		{"access denied", "Access denied to resource"},
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

func TestDetectState_ErrorPatterns_Network(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"ECONNREFUSED", "Error: ECONNREFUSED: connection refused"},
		{"ECONNRESET", "Error: ECONNRESET: connection reset by peer"},
		{"ETIMEDOUT", "Error: ETIMEDOUT: operation timed out"},
		{"ENETUNREACH", "Error: ENETUNREACH: network is unreachable"},
		{"connection refused text", "Failed to connect: connection refused"},
		{"connection reset text", "Connection reset by peer"},
		{"network unreachable", "Network is unreachable"},
		{"timeout text", "Request timeout after 30 seconds"},
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

func TestDetectState_ErrorPatterns_API(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"rate limit", "Error: rate limit exceeded"},
		{"429 too many requests", "HTTP 429: too many requests"},
		{"401 unauthorized", "HTTP 401: unauthorized access"},
		{"403 forbidden", "HTTP 403: forbidden resource"},
		{"authentication failed", "Authentication failed: invalid credentials"},
		{"invalid token", "Error: invalid access token"},
		{"unauthorized", "Unauthorized: please log in"},
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

func TestDetectState_ErrorPatterns_Commands(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"command not found", "bash: foo: command not found"},
		{"command failed", "Command failed with errors"},
		{"exit status 1", "Process exited with exit status 1"},
		{"exit code 2", "Command terminated with exit code 2"},
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

func TestDetectState_ErrorPatterns_Compilation(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"compilation failed", "Compilation failed with 3 errors"},
		{"build failed", "Build failed: errors during compilation"},
		{"syntax error", "SyntaxError: Unexpected token"},
		{"type error", "TypeError: Cannot read property 'foo' of undefined"},
		{"parse error", "ParseError: Invalid JSON at position 42"},
		{"cannot find module", "Error: Cannot find module 'express'"},
		{"module not found", "ModuleNotFoundError: No module named 'foo'"},
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

func TestDetectState_ErrorPatterns_Tests(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"test failed", "Test failed: expected true, got false"},
		{"tests FAILED", "tests FAILED"},
		{"failing count", "5 tests, 2 failing"},
		{"assertion failed", "AssertionError: assertion failed"},
		{"expected but got", "Expected 42 but got 24"},
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

func TestDetectState_ErrorPatterns_Runtime(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"null pointer", "panic: runtime error: null pointer dereference"},
		{"undefined is not", "TypeError: undefined is not a function"},
		{"cannot read property", "TypeError: Cannot read property 'x' of null"},
		{"segmentation fault", "Segmentation fault (core dumped)"},
		{"out of memory", "fatal error: out of memory"},
		{"stack overflow", "Error: stack overflow"},
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

// ============================================================================
// DONE PATTERN TESTS
// ============================================================================

func TestDetectState_DonePatterns_Completion(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"Task completed", "Task completed successfully"},
		{"Successfully", "Successfully updated 5 files"},
		{"Done period", "Done."},
		{"Done exclamation", "Done!"},
		{"Finished", "Finished processing all items"},
		{"All tasks complete", "All tasks complete!"},
		{"All done", "All done! Your changes are ready"},
		{"completed successfully", "Operation completed successfully"},
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

func TestDetectState_DonePatterns_Git(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"commit message", "[main abcd123] Add new feature"},
		{"commit with files", "committed 3 files changed, 42 insertions(+)"},
		{"pushed to origin", "Successfully pushed to origin/main"},
		{"pull request created", "Pull request created: #123"},
		{"PR created", "PR created successfully"},
		{"successfully merged", "Successfully merged pull request #123"},
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

func TestDetectState_DonePatterns_Tests(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"All tests pass", "All tests pass!"},
		{"tests passed", "42 tests passed"},
		{"passing count", "10 tests, 10 passing"},
		{"check mark completed", "✓ Build completed"},
		{"check mark passed", "✓ All tests passed"},
		{"check mark success", "✓ Operation successful"},
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

func TestDetectState_DonePatterns_Build(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"build successful", "Build successful in 3.2s"},
		{"build complete", "Build complete!"},
		{"compiled successfully", "Compiled successfully in 1.5s"},
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

// ============================================================================
// BUSY PATTERN TESTS
// ============================================================================

func TestDetectState_BusyPatterns_Progress(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"Processing", "Processing files..."},
		{"Working on", "Working on your request"},
		{"In progress", "Build in progress"},
		{"Loading", "Loading dependencies..."},
		{"Building", "Building project..."},
		{"Compiling", "Compiling source files..."},
		{"Installing", "Installing packages..."},
		{"Downloading", "Downloading artifacts..."},
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

func TestDetectState_BusyPatterns_FileOps(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"Reading file", "Reading file: src/main.go"},
		{"Writing file", "Writing file: output.txt"},
		{"Creating file", "Creating file: new.txt"},
		{"Editing file", "Editing file: config.json"},
		{"Modifying", "Modifying database schema"},
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

func TestDetectState_BusyPatterns_Commands(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"Running tests", "Running tests..."},
		{"Executing tests", "Executing test suite"},
		{"Running command", "Running command: go build"},
		{"Executing", "Executing build script"},
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

// ============================================================================
// PRIORITY AND EDGE CASE TESTS
// ============================================================================

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
			name:     "Error takes priority over Busy",
			output:   "Processing files...\nError: file not found",
			expected: domain.SessionError,
		},
		{
			name:     "Waiting takes priority over Done",
			output:   "Task completed\nDo you want to continue? [y/n]",
			expected: domain.SessionWaiting,
		},
		{
			name:     "Waiting takes priority over Busy",
			output:   "Processing files...\nDo you want to continue? [y/n]",
			expected: domain.SessionWaiting,
		},
		{
			name:     "Done takes priority over Busy",
			output:   "Processing files...\nTask completed successfully",
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

func TestDetectState_EmptyAndIdle(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected domain.SessionState
	}{
		{"empty string", "", domain.SessionIdle},
		{"only whitespace", "   \n\t\n   ", domain.SessionIdle},
		{"only newlines", "\n\n\n", domain.SessionIdle},
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
		{"normal output", "Processing files...\nBuilding project\nRunning tests"},
		{"no matching patterns", "Just some random text\nwithout any special keywords"},
		{"generic log output", "[INFO] Starting application\n[DEBUG] Connecting to database"},
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

// ============================================================================
// REAL WORLD CLAUDE CODE OUTPUT TESTS
// ============================================================================

func TestDetectState_RealWorldScenarios(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected domain.SessionState
	}{
		{
			name: "Claude asking permission",
			output: `I'll help you implement the feature.

Do you want to proceed with these changes? [y/n]`,
			expected: domain.SessionWaiting,
		},
		{
			name: "Test failure",
			output: `Running tests...
● Test Suite: utils
  ✗ should format date correctly
    Expected "2024-01-15" but got "2024-15-01"

5 tests, 4 passing, 1 failing`,
			expected: domain.SessionError,
		},
		{
			name: "Successful completion",
			output: `Writing changes to disk...
Running type check...
✓ All type checks passed

Task completed successfully!`,
			expected: domain.SessionDone,
		},
		{
			name: "File not found error",
			output: `Reading configuration...
Error: ENOENT: no such file or directory, open 'config.json'`,
			expected: domain.SessionError,
		},
		{
			name: "Git commit success",
			output: `Staging changes...
Creating commit...
[main abc1234] Implement user authentication
 3 files changed, 142 insertions(+), 5 deletions(-)`,
			expected: domain.SessionDone,
		},
		{
			name: "Build in progress",
			output: `Compiling TypeScript...
Building bundle...
Optimizing assets...`,
			expected: domain.SessionBusy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := DetectState(tt.output)
			if state != tt.expected {
				t.Errorf("DetectState() = %v, want %v\nOutput:\n%s", state, tt.expected, tt.output)
			}
		})
	}
}

// ============================================================================
// DETECTION WITH CONTEXT TESTS
// ============================================================================

func TestDetectStateWithContext_Confidence(t *testing.T) {
	tests := []struct {
		name           string
		output         string
		expectedState  domain.SessionState
		minConfidence  float64
		shouldHaveMatch bool
	}{
		{
			name:            "Error with high confidence",
			output:          "Some output\nMore output\nError: file not found",
			expectedState:   domain.SessionError,
			minConfidence:   0.8,
			shouldHaveMatch: true,
		},
		{
			name:            "Default busy with low confidence",
			output:          "Random text without patterns",
			expectedState:   domain.SessionBusy,
			minConfidence:   0.0,
			shouldHaveMatch: false,
		},
		{
			name:            "Idle with max confidence",
			output:          "",
			expectedState:   domain.SessionIdle,
			minConfidence:   1.0,
			shouldHaveMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectStateWithContext(tt.output)

			if result.State != tt.expectedState {
				t.Errorf("State = %v, want %v", result.State, tt.expectedState)
			}

			if result.Confidence < tt.minConfidence {
				t.Errorf("Confidence = %v, want >= %v", result.Confidence, tt.minConfidence)
			}

			if tt.shouldHaveMatch && result.Match == nil {
				t.Error("Expected match, got nil")
			}

			if !tt.shouldHaveMatch && result.Match != nil {
				t.Errorf("Expected no match, got %+v", result.Match)
			}
		})
	}
}

func TestDetectStateWithContext_MatchDetails(t *testing.T) {
	output := `Line 1
Line 2
Error: something failed
Line 4`

	result := DetectStateWithContext(output)

	if result.State != domain.SessionError {
		t.Errorf("State = %v, want %v", result.State, domain.SessionError)
	}

	if result.Match == nil {
		t.Fatal("Expected match, got nil")
	}

	if result.Match.Line != "Error: something failed" {
		t.Errorf("Match line = %q, want %q", result.Match.Line, "Error: something failed")
	}

	if result.Match.Priority != PriorityError {
		t.Errorf("Match priority = %v, want %v", result.Match.Priority, PriorityError)
	}

	if result.Match.State != domain.SessionError {
		t.Errorf("Match state = %v, want %v", result.Match.State, domain.SessionError)
	}
}

func TestDetectStateWithContext_RecencyBias(t *testing.T) {
	// More recent lines should have higher confidence
	output := `Error: old error at line 1
Normal output
Normal output
Normal output
Error: recent error at line 5`

	result := DetectStateWithContext(output)

	if result.Match == nil {
		t.Fatal("Expected match, got nil")
	}

	// Should match the more recent error
	if !strings.Contains(result.Match.Line, "recent error") {
		t.Errorf("Expected to match recent error, got: %q", result.Match.Line)
	}

	// Recent lines should have higher confidence
	if result.Confidence < 0.8 {
		t.Errorf("Expected high confidence for recent match, got: %v", result.Confidence)
	}
}
