package domain

import "testing"

func TestSessionState_Icon(t *testing.T) {
	tests := []struct {
		state SessionState
		want  string
	}{
		{SessionIdle, "○"},
		{SessionBusy, "●"},
		{SessionWaiting, "◐"},
		{SessionDone, "✓"},
		{SessionError, "✗"},
		{SessionPaused, "⏸"},
		{SessionState("unknown"), "?"},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.Icon(); got != tt.want {
				t.Errorf("SessionState.Icon() = %v, want %v", got, tt.want)
			}
		})
	}
}
