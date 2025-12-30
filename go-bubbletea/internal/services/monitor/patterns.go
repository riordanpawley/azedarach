package monitor

import (
	"regexp"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// statePatterns maps session states to their detection patterns
// Patterns are checked in priority order: Error > Done > Waiting > Busy
var statePatterns = map[domain.SessionState][]*regexp.Regexp{
	domain.SessionWaiting: {
		regexp.MustCompile(`\[y/n\]`),
		regexp.MustCompile(`\[Y/n\]`),
		regexp.MustCompile(`\[yes/no\]`),
		regexp.MustCompile(`Do you want to`),
		regexp.MustCompile(`AskUserQuestion`),
		regexp.MustCompile(`Press Enter`),
		regexp.MustCompile(`waiting for`),
		regexp.MustCompile(`Approve\?`),
	},
	domain.SessionDone: {
		regexp.MustCompile(`Task completed`),
		regexp.MustCompile(`Successfully completed`),
		regexp.MustCompile(`All done`),
		regexp.MustCompile(`✓.*completed`),
	},
	domain.SessionError: {
		regexp.MustCompile(`Error:`),
		regexp.MustCompile(`Exception:`),
		regexp.MustCompile(`panic:`),
		regexp.MustCompile(`FAILED`),
		regexp.MustCompile(`fatal error`),
	},
}

// DetectState analyzes session output and determines the current state
// It checks the last 100 lines for state patterns in priority order.
// Returns SessionBusy if no patterns match.
func DetectState(output string) domain.SessionState {
	// Check last 100 lines for patterns
	lines := strings.Split(output, "\n")
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}
	recent := strings.Join(lines, "\n")

	// Check patterns in priority order: Error > Done > Waiting > Busy
	// Error state takes highest priority
	if patterns, ok := statePatterns[domain.SessionError]; ok {
		for _, p := range patterns {
			if p.MatchString(recent) {
				return domain.SessionError
			}
		}
	}

	// Done state takes second priority
	if patterns, ok := statePatterns[domain.SessionDone]; ok {
		for _, p := range patterns {
			if p.MatchString(recent) {
				return domain.SessionDone
			}
		}
	}

	// Waiting state takes third priority
	if patterns, ok := statePatterns[domain.SessionWaiting]; ok {
		for _, p := range patterns {
			if p.MatchString(recent) {
				return domain.SessionWaiting
			}
		}
	}

	// Default to busy if no patterns match
	return domain.SessionBusy
}
