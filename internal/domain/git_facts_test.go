package domain

import (
	"testing"
	"time"
)

func TestDeriveGitFactsObservationStates(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		hasWorktree bool
		statusState GitFactsStatusState
		observedAt  time.Time
		want        GitFactsAvailability
	}{
		{name: "absent capability or worktree", want: GitFactsUnavailable},
		{name: "not observed", hasWorktree: true, want: GitFactsUnavailable},
		{name: "invalid payload", hasWorktree: true, statusState: GitFactsStatusInvalid, observedAt: now.Add(-time.Second), want: GitFactsPartial},
		{name: "missing timestamp", hasWorktree: true, statusState: GitFactsStatusValid, want: GitFactsPartial},
		{name: "stale", hasWorktree: true, statusState: GitFactsStatusValid, observedAt: now.Add(-time.Minute), want: GitFactsStale},
		{name: "available", hasWorktree: true, statusState: GitFactsStatusValid, observedAt: now.Add(-time.Second), want: GitFactsAvailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveGitFactsObservation(tt.hasWorktree, tt.statusState, tt.observedAt, now, DefaultGitFactsStaleAfter)
			if got.Availability != tt.want {
				t.Fatalf("availability = %q, want %q", got.Availability, tt.want)
			}
		})
	}
}
