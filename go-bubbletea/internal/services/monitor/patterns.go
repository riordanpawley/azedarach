package monitor

import (
	"regexp"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// StatePattern represents a pattern with priority for state detection
type StatePattern struct {
	State    domain.SessionState
	Pattern  *regexp.Regexp
	Priority int
}

// PatternMatch represents a matched pattern with context
type PatternMatch struct {
	State      domain.SessionState
	Pattern    string
	Line       string
	LineNumber int
	Priority   int
	Confidence float64
}

// Pattern priority levels (higher = checked first)
const (
	PriorityError   = 100
	PriorityDone    = 80
	PriorityWaiting = 90
	PriorityBusy    = 60
)

// State patterns ordered by priority (highest to lowest)
// Based on TypeScript StateDetector patterns with comprehensive Claude Code output coverage
var statePatterns = []StatePattern{
	// ============================================================================
	// WAITING PATTERNS (Priority: 90)
	// User input prompts, confirmations, and interactive questions
	// ============================================================================

	// Standard y/n prompts
	{domain.SessionWaiting, regexp.MustCompile(`(?i)\[y/n\]`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)\[Y/n\]`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)\[yes/no\]`), PriorityWaiting},

	// Question patterns
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Do you want to`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Would you like`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Continue\?`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Proceed\?`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Approve\?`), PriorityWaiting},

	// AskUserQuestion tool - numbered choices and "Other" option
	{domain.SessionWaiting, regexp.MustCompile(`(?im)^\s*\d+\.\s+Other\b`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Other\s*\(describe`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)select.*option`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)choose.*option`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)enter.*number`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)type.*number.*select`), PriorityWaiting},

	// Input prompts
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Press Enter`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)Press any key`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)waiting for.*input`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)waiting for.*response`), PriorityWaiting},
	{domain.SessionWaiting, regexp.MustCompile(`(?i)AskUserQuestion`), PriorityWaiting},

	// ============================================================================
	// ERROR PATTERNS (Priority: 100)
	// Errors, exceptions, failures, and error conditions
	// ============================================================================

	// Generic error patterns
	{domain.SessionError, regexp.MustCompile(`Error:`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`Exception:`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`Failed:`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`FAILED`), PriorityError},

	// Stack traces and panics
	{domain.SessionError, regexp.MustCompile(`(?i)panic:`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)fatal error`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)stack trace:`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?m)^\s*at\s+.*:\d+:\d+`), PriorityError}, // Stack trace line

	// File system errors
	{domain.SessionError, regexp.MustCompile(`ENOENT`), PriorityError},   // File not found
	{domain.SessionError, regexp.MustCompile(`EACCES`), PriorityError},   // Permission denied
	{domain.SessionError, regexp.MustCompile(`EEXIST`), PriorityError},   // File exists
	{domain.SessionError, regexp.MustCompile(`EISDIR`), PriorityError},   // Is a directory
	{domain.SessionError, regexp.MustCompile(`ENOTDIR`), PriorityError},  // Not a directory
	{domain.SessionError, regexp.MustCompile(`EMFILE`), PriorityError},   // Too many open files
	{domain.SessionError, regexp.MustCompile(`ENOSPC`), PriorityError},   // No space left
	{domain.SessionError, regexp.MustCompile(`(?i)permission denied`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)file not found`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)no such file`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)access denied`), PriorityError},

	// Network errors
	{domain.SessionError, regexp.MustCompile(`ECONNREFUSED`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`ECONNRESET`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`ETIMEDOUT`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`ENETUNREACH`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)connection refused`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)connection reset`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)network.*unreachable`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)timeout`), PriorityError},

	// API and authentication errors
	{domain.SessionError, regexp.MustCompile(`(?i)rate limit`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)429.*too many requests`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)401.*unauthorized`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)403.*forbidden`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)authentication failed`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)invalid.*token`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)unauthorized`), PriorityError},

	// Command errors
	{domain.SessionError, regexp.MustCompile(`(?i)command not found`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)command failed`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)exit status [1-9]`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)exit code [1-9]`), PriorityError},

	// Compilation errors
	{domain.SessionError, regexp.MustCompile(`(?i)compilation failed`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)build failed`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)syntax error`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)type error`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)parse error`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)cannot find module`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)module not found`), PriorityError},

	// Test failures
	{domain.SessionError, regexp.MustCompile(`(?i)test.*failed`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)tests? FAILED`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)\d+ failing`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)assertion.*failed`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)expected.*but got`), PriorityError},

	// Runtime errors
	{domain.SessionError, regexp.MustCompile(`(?i)null pointer`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)undefined is not`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)cannot read property`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)segmentation fault`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)out of memory`), PriorityError},
	{domain.SessionError, regexp.MustCompile(`(?i)stack overflow`), PriorityError},

	// ============================================================================
	// DONE PATTERNS (Priority: 80)
	// Task completion, success messages, and completion indicators
	// ============================================================================

	// Completion messages
	{domain.SessionDone, regexp.MustCompile(`(?i)Task completed`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)Successfully`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)Done\.`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)Done!`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)Finished`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)All tasks complete`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)All done`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)completed successfully`), PriorityDone},

	// Git operations
	{domain.SessionDone, regexp.MustCompile(`(?m)^\[[\w-]+\s+[a-f0-9]{7}\]`), PriorityDone}, // [branch abcd123] commit message
	{domain.SessionDone, regexp.MustCompile(`(?i)committed.*file.*changed`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)pushed to.*origin`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)pull request created`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)PR created`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)successfully merged`), PriorityDone},

	// Test success
	{domain.SessionDone, regexp.MustCompile(`(?i)All tests pass`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)tests? passed`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)\d+ passing`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`✓.*completed`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`✓.*passed`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`✓.*success`), PriorityDone},

	// Build success
	{domain.SessionDone, regexp.MustCompile(`(?i)build.*successful`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)build.*complete`), PriorityDone},
	{domain.SessionDone, regexp.MustCompile(`(?i)compiled successfully`), PriorityDone},

	// ============================================================================
	// BUSY PATTERNS (Priority: 60)
	// Active processing, work in progress
	// ============================================================================

	// Progress indicators
	{domain.SessionBusy, regexp.MustCompile(`(?i)Processing\.\.\.`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Working on`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)In progress`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Loading`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Building`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Compiling`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Installing`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Downloading`), PriorityBusy},

	// File operations
	{domain.SessionBusy, regexp.MustCompile(`(?i)Reading file`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Writing file`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Creating file`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Editing file`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Modifying`), PriorityBusy},

	// Test execution (not errors)
	{domain.SessionBusy, regexp.MustCompile(`(?i)Running tests?`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Executing tests?`), PriorityBusy},

	// Command execution
	{domain.SessionBusy, regexp.MustCompile(`(?i)Running command`), PriorityBusy},
	{domain.SessionBusy, regexp.MustCompile(`(?i)Executing`), PriorityBusy},
}

// DetectionResult represents a state detection result with confidence
type DetectionResult struct {
	State      domain.SessionState
	Match      *PatternMatch
	Confidence float64
}

// DetectState analyzes session output and determines the current state
// It checks the last 100 lines for state patterns in priority order.
// Returns SessionBusy if no patterns match and output is non-empty.
func DetectState(output string) domain.SessionState {
	result := DetectStateWithContext(output)
	return result.State
}

// DetectStateWithContext analyzes session output and returns detailed detection information
// including the matched pattern, line context, and confidence score.
func DetectStateWithContext(output string) DetectionResult {
	// Check last 100 lines for patterns
	lines := strings.Split(output, "\n")
	startLine := 0
	if len(lines) > 100 {
		startLine = len(lines) - 100
		lines = lines[startLine:]
	}

	// Track best match by priority
	var bestMatch *PatternMatch

	// Check each line against patterns
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		for _, sp := range statePatterns {
			if sp.Pattern.MatchString(line) {
				// Calculate confidence based on line recency (more recent = higher confidence)
				linePosition := float64(i) / float64(len(lines))
				confidence := 0.5 + (linePosition * 0.5) // 0.5 to 1.0

				match := &PatternMatch{
					State:      sp.State,
					Pattern:    sp.Pattern.String(),
					Line:       line,
					LineNumber: startLine + i,
					Priority:   sp.Priority,
					Confidence: confidence,
				}

				// Keep highest priority match (or higher confidence if same priority)
				if bestMatch == nil ||
					match.Priority > bestMatch.Priority ||
					(match.Priority == bestMatch.Priority && match.Confidence > bestMatch.Confidence) {
					bestMatch = match
				}
			}
		}
	}

	// Return best match if found
	if bestMatch != nil {
		return DetectionResult{
			State:      bestMatch.State,
			Match:      bestMatch,
			Confidence: bestMatch.Confidence,
		}
	}

	// Default to busy if we have non-empty output
	if strings.TrimSpace(output) != "" {
		return DetectionResult{
			State:      domain.SessionBusy,
			Match:      nil,
			Confidence: 0.3, // Low confidence for default state
		}
	}

	// Idle if completely empty
	return DetectionResult{
		State:      domain.SessionIdle,
		Match:      nil,
		Confidence: 1.0,
	}
}
