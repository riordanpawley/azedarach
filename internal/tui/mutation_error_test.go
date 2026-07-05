package app

import (
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestFormatStatusMutationFailureNormalizesCommonDaemonErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "blocked done close",
			err: &daemonclient.CommandError{
				Code:    protocol.ErrorCodeConflict,
				Message: "cannot close issue az-4: child issues remain unresolved: az-child. Next: press C to close clean children too, or resolve child issues and retry",
			},
			want: "Could not move az-4 to Done. It stayed In Review. Reason: Done is blocked by unresolved child issues. Next: press C to close clean children too, or resolve child issues and retry",
		},
		{
			name: "daemon unavailable",
			err:  errors.New("daemon client unavailable"),
			want: "Could not move az-4 to Done. It stayed In Review. Reason: the daemon client is unavailable. Next: wait for daemon reconnect or run az daemon start, then retry",
		},
		{
			name: "invalid request",
			err: &daemonclient.CommandError{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "missing required field: task_id",
			},
			want: "Could not move az-4 to Done. It stayed In Review. Reason: the request was invalid: missing required field: task_id. Next: refresh the task and try the action again",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatusMutationFailure("az-4", domain.StatusInReview, domain.StatusDone, tt.err)
			if got != tt.want {
				t.Fatalf("message = %q, want %q", got, tt.want)
			}
		})
	}
}
