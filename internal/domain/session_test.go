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

func TestSessionDisplayPrefersAgentActivity(t *testing.T) {
	session := &Session{
		State:          SessionBusy,
		Activity:       "idle",
		ActivitySource: "hooks",
	}

	if got := session.DisplayIcon(); got != SessionIdle.Icon() {
		t.Fatalf("DisplayIcon() = %q, want idle icon", got)
	}
	if got := session.DisplayLabel(); got != "idle" {
		t.Fatalf("DisplayLabel() = %q, want idle", got)
	}
	if got := session.DisplayCode(); got != "I" {
		t.Fatalf("DisplayCode() = %q, want I", got)
	}
	if got, ok := session.DisplayState(); !ok || got != SessionIdle {
		t.Fatalf("DisplayState() = %q/%v, want idle/true", got, ok)
	}
}

func TestSessionDisplayFallsBackToLifecycleWithoutAgentActivity(t *testing.T) {
	session := &Session{State: SessionBusy}

	if got := session.DisplayIcon(); got != SessionBusy.Icon() {
		t.Fatalf("DisplayIcon() = %q, want busy icon", got)
	}
	if got := session.DisplayLabel(); got != "busy" {
		t.Fatalf("DisplayLabel() = %q, want busy", got)
	}
	if got := session.DisplayCode(); got != "B" {
		t.Fatalf("DisplayCode() = %q, want B", got)
	}
}

func TestSessionDisplayUnknownAgentActivity(t *testing.T) {
	session := &Session{
		State:          SessionBusy,
		Activity:       "unknown",
		ActivitySource: "none",
	}

	if got := session.DisplayIcon(); got != "?" {
		t.Fatalf("DisplayIcon() = %q, want ?", got)
	}
	if got := session.DisplayLabel(); got != "unknown" {
		t.Fatalf("DisplayLabel() = %q, want unknown", got)
	}
	if got := session.DisplayCode(); got != "?" {
		t.Fatalf("DisplayCode() = %q, want ?", got)
	}
}

func TestSessionDisplayNoAgentActivity(t *testing.T) {
	session := &Session{
		State:          SessionBusy,
		Activity:       "no-agent",
		ActivitySource: "session",
	}

	if got := session.DisplayIcon(); got != SessionIdle.Icon() {
		t.Fatalf("DisplayIcon() = %q, want idle icon", got)
	}
	if got := session.DisplayLabel(); got != "no-agent" {
		t.Fatalf("DisplayLabel() = %q, want no-agent", got)
	}
	if got := session.DisplayCode(); got != "N" {
		t.Fatalf("DisplayCode() = %q, want N", got)
	}
	if display, ok := session.DisplayState(); ok {
		t.Fatalf("DisplayState() = %q/%v, want no derived lifecycle state", display, ok)
	}
}

func TestSessionReviewReadyPhaseAllowsOnlyIdleDoneOrNoAgent(t *testing.T) {
	tests := []struct {
		name       string
		session    *Session
		hasSession bool
		want       bool
	}{
		{name: "no runtime", want: true},
		{name: "projected runtime without session detail", hasSession: true, want: false},
		{name: "idle", session: &Session{Activity: "idle"}, hasSession: true, want: true},
		{name: "done", session: &Session{Activity: "done"}, hasSession: true, want: true},
		{name: "no agent", session: &Session{Activity: "no-agent"}, hasSession: true, want: true},
		{name: "ended", session: &Session{Activity: "ended"}, hasSession: true, want: true},
		{name: "busy", session: &Session{Activity: "busy"}, hasSession: true, want: false},
		{name: "working", session: &Session{Activity: "working"}, hasSession: true, want: false},
		{name: "waiting human", session: &Session{Activity: "waiting_human"}, hasSession: true, want: false},
		{name: "waiting ai", session: &Session{Activity: "waiting_ai"}, hasSession: true, want: false},
		{name: "waiting tool", session: &Session{Activity: "waiting_tool"}, hasSession: true, want: false},
		{name: "unknown", session: &Session{Activity: "unknown"}, hasSession: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.session.AllowsReviewReadyPhase(tt.hasSession); got != tt.want {
				t.Fatalf("AllowsReviewReadyPhase() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSessionBlocksReviewHandoffForBusyAndWaitingActivity(t *testing.T) {
	for _, activity := range []string{"busy", "starting", "working", "waiting", "waiting_human", "waiting_ai", "waiting_tool"} {
		t.Run(activity, func(t *testing.T) {
			session := &Session{Activity: activity}
			if !session.BlocksReviewHandoff() {
				t.Fatalf("BlocksReviewHandoff() = false, want true")
			}
		})
	}

	for _, activity := range []string{"idle", "done", "no-agent", "ended", "paused", "error"} {
		t.Run(activity, func(t *testing.T) {
			session := &Session{Activity: activity}
			if session.BlocksReviewHandoff() {
				t.Fatalf("BlocksReviewHandoff() = true, want false")
			}
		})
	}
}
