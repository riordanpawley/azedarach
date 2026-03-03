package errors

import (
	"fmt"
	"strings"
	"testing"
)

func TestConstructorsAndPredicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		create    func() error
		predicate func(error) bool
		kind      Kind
	}{
		{
			name:      "auth",
			create:    func() error { return NewAuth("beads.login", "token expired", nil) },
			predicate: IsAuth,
			kind:      KindAuth,
		},
		{
			name:      "offline",
			create:    func() error { return NewOffline("beads.ready", "network down", nil) },
			predicate: IsOffline,
			kind:      KindOffline,
		},
		{
			name:      "conflict",
			create:    func() error { return NewConflict("git.merge", "branch diverged", nil) },
			predicate: IsConflict,
			kind:      KindConflict,
		},
		{
			name:      "timeout",
			create:    func() error { return NewTimeout("tmux.capture", "command timed out", nil) },
			predicate: IsTimeout,
			kind:      KindTimeout,
		},
		{
			name:      "lock contention",
			create:    func() error { return NewLockContention("git.push", "index locked", nil) },
			predicate: IsLockContention,
			kind:      KindLockContention,
		},
		{
			name:      "unexpected output",
			create:    func() error { return NewUnexpectedOutput("gh.pr.view", "invalid json", nil) },
			predicate: IsUnexpectedOutput,
			kind:      KindUnexpectedOutput,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.create()
			if !tc.predicate(err) {
				t.Fatalf("predicate failed for %s", tc.name)
			}
			if !IsKind(err, tc.kind) {
				t.Fatalf("kind check failed for %s", tc.name)
			}
		})
	}
}

func TestPredicatesMatchWrappedErrors(t *testing.T) {
	t.Parallel()

	inner := fmt.Errorf("transport failure")
	err := NewOffline("beads.ready", "service unavailable", inner)
	wrapped := fmt.Errorf("request failed: %w", err)

	if !IsOffline(wrapped) {
		t.Fatalf("expected wrapped error to match IsOffline")
	}
	if IsAuth(wrapped) {
		t.Fatalf("did not expect wrapped offline error to match IsAuth")
	}
}

func TestErrorFormattingIncludesOperationAndKind(t *testing.T) {
	t.Parallel()

	err := NewConflict("git.merge", "branch diverged", fmt.Errorf("conflict in foo.go"))

	got := err.Error()
	if got == "" {
		t.Fatalf("expected non-empty error string")
	}

	mustContain := []string{"conflict", "git.merge", "branch diverged", "foo.go"}
	for _, part := range mustContain {
		if !strings.Contains(got, part) {
			t.Fatalf("expected %q in error string %q", part, got)
		}
	}
}
