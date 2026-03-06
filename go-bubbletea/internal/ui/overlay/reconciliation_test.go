package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
)

func TestNewSessionReconciliationOverlay_OrphanTmuxActions(t *testing.T) {
	mismatch := diagnostics.SessionMismatch{
		IssueID:          "az-44",
		Kind:             diagnostics.SessionMismatchKindOrphanTmux,
		IndicatorPresent: false,
		TmuxPresent:      true,
	}

	o := NewSessionReconciliationOverlay(mismatch, "attach")

	if o == nil {
		t.Fatal("expected overlay")
	}

	if !o.isActionEnabled(ReconciliationActionAdoptIndicator) {
		t.Fatal("expected adopt indicator to be enabled for orphan tmux mismatch")
	}
	if o.isActionEnabled(ReconciliationActionClearIndicator) {
		t.Fatal("expected clear indicator to be disabled for orphan tmux mismatch")
	}
	if !o.isActionEnabled(ReconciliationActionTerminateOrphanTmux) {
		t.Fatal("expected terminate orphan tmux to be enabled for orphan mismatch")
	}
}

func TestSessionReconciliationOverlay_SelectByDirectKey(t *testing.T) {
	mismatch := diagnostics.SessionMismatch{
		IssueID:          "az-44",
		Kind:             diagnostics.SessionMismatchKindStaleIndicator,
		IndicatorPresent: true,
		TmuxPresent:      false,
	}
	o := NewSessionReconciliationOverlay(mismatch, "attach")

	nextModel, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	o = nextModel.(*SessionReconciliationOverlay)

	if cmd == nil {
		t.Fatal("expected selection command from direct clear key")
	}

	msg := cmd()
	actionMsg, ok := msg.(SessionReconciliationActionMsg)
	if !ok {
		t.Fatalf("expected SessionReconciliationActionMsg, got %T", msg)
	}
	if actionMsg.Mismatch.IssueID != "az-44" {
		t.Fatalf("issue id = %q, want az-44", actionMsg.Mismatch.IssueID)
	}
	if actionMsg.Action != ReconciliationActionClearIndicator {
		t.Fatalf("action = %q, want %q", actionMsg.Action, ReconciliationActionClearIndicator)
	}
	if actionMsg.Trigger != "attach" {
		t.Fatalf("trigger = %q, want attach", actionMsg.Trigger)
	}
	if o.cursor < 0 {
		t.Fatalf("cursor unexpectedly invalid after direct key selection: %d", o.cursor)
	}
}

func TestSessionReconciliationOverlay_ViewContainsExplicitChoices(t *testing.T) {
	mismatch := diagnostics.SessionMismatch{
		IssueID:          "az-44",
		Kind:             diagnostics.SessionMismatchKindOrphanTmux,
		IndicatorPresent: false,
		TmuxPresent:      true,
	}
	o := NewSessionReconciliationOverlay(mismatch, "diagnostics")

	view := o.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}

	expected := []string{
		"Adopt indicator",
		"Clear indicator",
		"Terminate orphan tmux session",
	}
	for _, fragment := range expected {
		if !containsFragment(view, fragment) {
			t.Fatalf("view missing fragment %q:\n%s", fragment, view)
		}
	}
}

func containsFragment(s, fragment string) bool {
	return len(s) >= len(fragment) && (s == fragment || containsRunes(s, fragment))
}

func containsRunes(s, fragment string) bool {
	for i := 0; i+len(fragment) <= len(s); i++ {
		if s[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
