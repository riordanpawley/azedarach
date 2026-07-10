package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestBoardViewOverlaySelectsCurrentView(t *testing.T) {
	records := []domain.BoardViewRecord{
		{ProjectID: "proj", View: domain.DefaultBoardView(), BuiltIn: true},
		{ProjectID: "proj", View: domain.PlanningBoardView(), BuiltIn: true},
		{ProjectID: "proj", View: domain.OrchestrationBoardView(), BuiltIn: true},
	}
	o := NewBoardViewOverlay(records, "orchestration")

	view := o.View()
	if !strings.Contains(view, "Planning") || !strings.Contains(view, "Orchestration") || !strings.Contains(view, "built-in") {
		t.Fatalf("overlay view missing board view rows:\n%s", view)
	}

	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter command = nil")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("message = %T, want SelectionMsg", cmd())
	}
	if msg.Key != BoardViewSelectKey {
		t.Fatalf("selection key = %q, want %q", msg.Key, BoardViewSelectKey)
	}
	selected, ok := msg.Value.(BoardViewSelectMsg)
	if !ok {
		t.Fatalf("selection value = %T, want BoardViewSelectMsg", msg.Value)
	}
	if selected.ViewID != "orchestration" {
		t.Fatalf("selected view = %q, want orchestration", selected.ViewID)
	}
}

func TestBoardViewOverlayStatusBindingsDescribeWorkingActions(t *testing.T) {
	o := NewBoardViewOverlay(nil, "")
	bindings := o.StatusBindings()
	got := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		got[binding.Key] = binding.Description
	}
	for key, description := range map[string]string{
		"j/k":   "view",
		"Enter": "select",
		"Esc":   "close",
	} {
		if got[key] != description {
			t.Fatalf("status binding %q = %q, want %q (all=%+v)", key, got[key], description, bindings)
		}
	}
}
