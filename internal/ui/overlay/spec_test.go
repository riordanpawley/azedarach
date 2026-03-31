package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSpecWorkspaceOverlayCyclesSections(t *testing.T) {
	overlay := NewSpecWorkspaceOverlay("demo")

	if overlay.Title() != "Spec Workspace" {
		t.Fatalf("title = %q, want Spec Workspace", overlay.Title())
	}
	if width, height := overlay.Size(); width != 82 || height != 18 {
		t.Fatalf("size = (%d,%d), want (82,18)", width, height)
	}
	if got := overlay.View(); !strings.Contains(got, "Requirements") {
		t.Fatalf("expected requirements section in view, got %q", got)
	}

	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatal("tab should not emit a command")
	}
	overlay = model.(*SpecWorkspaceOverlay)
	if got := overlay.View(); !strings.Contains(got, "Coverage") {
		t.Fatalf("expected coverage section in view after tab, got %q", got)
	}

	model, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if cmd != nil {
		t.Fatal("shift+tab should not emit a command")
	}
	overlay = model.(*SpecWorkspaceOverlay)
	if overlay.section != SpecWorkspaceRequirements {
		t.Fatalf("section = %v, want requirements after reverse cycle", overlay.section)
	}
}

func TestSpecWorkspaceOverlayEscCloses(t *testing.T) {
	overlay := NewSpecWorkspaceOverlay("demo")

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("escape should emit close command")
	}
	msg := cmd()
	if _, ok := msg.(CloseOverlayMsg); !ok {
		t.Fatalf("escape command emitted %T, want CloseOverlayMsg", msg)
	}
}
