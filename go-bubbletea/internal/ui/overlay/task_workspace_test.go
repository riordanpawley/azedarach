package overlay

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestTaskWorkspaceOverlay_View_NoLocalFooterHints(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	view := overlay.View()

	if strings.Contains(view, "Tab/h/l") {
		t.Fatalf("expected workspace overlay to omit local footer hints, got: %q", view)
	}
	if strings.Contains(view, "Enter/action key") {
		t.Fatalf("expected workspace overlay to omit local footer hints, got: %q", view)
	}
}

func TestTaskWorkspaceOverlay_View_HasOuterBorder(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	view := overlay.View()

	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Fatalf("expected workspace overlay to render an outer rounded border, got: %q", view)
	}
}

func TestTaskWorkspaceOverlay_UsesFullScreen(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	if !overlay.UsesFullScreen() {
		t.Fatalf("expected task workspace overlay to request full-screen rendering")
	}
}
