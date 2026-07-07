package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCloseFailureDialogActions(t *testing.T) {
	dialog := NewCloseFailureDialog("az-1", "merge preflight would conflict in README.md", CloseFailureDialogOptions{
		PreviousStatus:          "in_review",
		TargetStatus:            "closed",
		CloseCleanChildren:      true,
		AllowAIMerge:            true,
		AllowForceWorktree:      true,
		AllowCloseCleanChildren: true,
	})

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd == nil {
		t.Fatal("expected force action command")
	}
	selection, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("message = %T, want SelectionMsg", cmd())
	}
	action, ok := selection.Value.(CloseFailureActionMsg)
	if !ok {
		t.Fatalf("selection value = %T, want CloseFailureActionMsg", selection.Value)
	}
	if action.TaskID != "az-1" || action.Action != CloseFailureActionForceWorktree || !action.ForceWorktree || !action.CloseCleanChildren {
		t.Fatalf("force action = %+v, want task, force, and preserved close-clean-children", action)
	}

	_, cmd = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if cmd == nil {
		t.Fatal("expected close-clean-children action command")
	}
	selection = cmd().(SelectionMsg)
	action = selection.Value.(CloseFailureActionMsg)
	if action.Action != CloseFailureActionCloseCleanChildren || !action.CloseCleanChildren {
		t.Fatalf("close-clean action = %+v, want close clean children", action)
	}

	_, cmd = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected AI merge action command")
	}
	selection = cmd().(SelectionMsg)
	action = selection.Value.(CloseFailureActionMsg)
	if action.Action != CloseFailureActionAIMerge || action.TaskID != "az-1" {
		t.Fatalf("AI merge action = %+v, want ai_merge for az-1", action)
	}
}

func TestCloseFailureDialogDoesNotOfferAIMergeForDirtyState(t *testing.T) {
	dialog := NewCloseFailureDialog("az-1", "base worktree has local changes: README.md", CloseFailureDialogOptions{
		PreviousStatus: "in_review",
		TargetStatus:   "closed",
		AllowAIMerge:   true,
	})

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd != nil {
		t.Fatal("unexpected AI merge action for dirty-state close failure")
	}
	for _, binding := range dialog.StatusBindings() {
		if binding.Key == "a" {
			t.Fatalf("unexpected AI merge status binding: %+v", binding)
		}
	}
	view := dialog.View()
	if strings.Contains(view, "AI merge") {
		t.Fatalf("view exposes AI merge for dirty-state failure: %q", view)
	}
}

func TestCloseFailureDialogAllowsActiveSessionOverride(t *testing.T) {
	dialog := NewCloseFailureDialog("az-1", "cannot close issue az-1: session activity is busy (source: hooks). Next: wait for the session projection to report idle/done/terminal activity or intentionally stop the session, then retry", CloseFailureDialogOptions{
		PreviousStatus:          "in_review",
		TargetStatus:            "closed",
		AllowActiveSessionRetry: true,
	})

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd == nil {
		t.Fatal("expected active-session override command")
	}
	selection, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("message = %T, want SelectionMsg", cmd())
	}
	action, ok := selection.Value.(CloseFailureActionMsg)
	if !ok {
		t.Fatalf("selection value = %T, want CloseFailureActionMsg", selection.Value)
	}
	if action.TaskID != "az-1" || action.Action != CloseFailureActionAllowActiveSession || !action.AllowActiveSession {
		t.Fatalf("active-session action = %+v, want task and allow-active-session", action)
	}
	view := dialog.View()
	if !strings.Contains(view, "Force active close") || strings.Contains(view, "Force cleanup") {
		t.Fatalf("view should expose active-session override only: %q", view)
	}
}
