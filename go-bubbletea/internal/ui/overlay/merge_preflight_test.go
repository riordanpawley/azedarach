package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMergePreflightOverlayRefreshSelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", ".", []string{"Target main is not clean"}, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("expected refresh command")
	}
	msg := cmd()
	selection, ok := msg.(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", msg)
	}
	if selection.Key != "merge_preflight_refresh" {
		t.Fatalf("selection key = %q, want merge_preflight_refresh", selection.Key)
	}
}

func TestMergePreflightOverlayAbortSelection(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", ".", []string{"Target main is not clean"}, true)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected abort command")
	}
	msg := cmd()
	selection, ok := msg.(SelectionMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SelectionMsg", msg)
	}
	if selection.Key != "merge_preflight_abort" {
		t.Fatalf("selection key = %q, want merge_preflight_abort", selection.Key)
	}
	if wt, ok := selection.Value.(string); !ok || wt != "." {
		t.Fatalf("selection value = %#v, want \".\"", selection.Value)
	}
}

func TestMergePreflightOverlayClose(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", ".", nil, false)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected close command")
	}
	if _, ok := cmd().(CloseOverlayMsg); !ok {
		t.Fatalf("msg type = %T, want CloseOverlayMsg", cmd())
	}
}

func TestMergePreflightOverlayViewContainsReasons(t *testing.T) {
	o := NewMergePreflightOverlay("az-1", "main", ".", []string{"Source az-1 is not clean: 1 modified"}, true)
	view := o.View()
	if !strings.Contains(view, "Merge blocked: az-1 -> main") {
		t.Fatalf("view missing title: %q", view)
	}
	if !strings.Contains(view, "Source az-1 is not clean") {
		t.Fatalf("view missing reason: %q", view)
	}
}
