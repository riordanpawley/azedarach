package domain

import (
	"errors"
	"testing"
)

func TestBeadsError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  BeadsError
		want string
	}{
		{
			name: "with bead ID",
			err:  BeadsError{Op: "update", BeadID: "az-1", Message: "failed"},
			want: "beads update [az-1]: failed",
		},
		{
			name: "with message only",
			err:  BeadsError{Op: "list", Message: "timeout"},
			want: "beads list: timeout",
		},
		{
			name: "with underlying error",
			err:  BeadsError{Op: "create", Err: errors.New("connection refused")},
			want: "beads create: connection refused",
		},
		{
			name: "minimal",
			err:  BeadsError{Op: "search"},
			want: "beads search failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("BeadsError.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBeadsError_Unwrap(t *testing.T) {
	underlying := errors.New("underlying error")
	err := &BeadsError{Op: "test", Err: underlying}

	if unwrapped := err.Unwrap(); unwrapped != underlying {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, underlying)
	}
}
