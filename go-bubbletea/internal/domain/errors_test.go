package domain

import (
	"errors"
	"testing"
)

func TestIssueTrackerError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  IssueTrackerError
		want string
	}{
		{
			name: "with issue ID",
			err:  IssueTrackerError{Op: "update", IssueID: "az-1", Message: "failed"},
			want: "issue tracker update [az-1]: failed",
		},
		{
			name: "with message only",
			err:  IssueTrackerError{Op: "list", Message: "timeout"},
			want: "issue tracker list: timeout",
		},
		{
			name: "with underlying error",
			err:  IssueTrackerError{Op: "create", Err: errors.New("connection refused")},
			want: "issue tracker create: connection refused",
		},
		{
			name: "minimal",
			err:  IssueTrackerError{Op: "search"},
			want: "issue tracker search failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("IssueTrackerError.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIssueTrackerError_Unwrap(t *testing.T) {
	underlying := errors.New("underlying error")
	err := &IssueTrackerError{Op: "test", Err: underlying}

	if unwrapped := err.Unwrap(); unwrapped != underlying {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, underlying)
	}
}
