package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCloseFailureDialogActions(t *testing.T) {
	dialog := NewCloseFailureDialog("az-1", "dirty worktree", CloseFailureDialogOptions{
		PreviousStatus:          "in_review",
		TargetStatus:            "closed",
		CloseCleanChildren:      true,
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
}
