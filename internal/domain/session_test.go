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
