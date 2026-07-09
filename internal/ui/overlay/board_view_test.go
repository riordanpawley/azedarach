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
		{ProjectID: "proj", View: domain.ActivityBoardView(), BuiltIn: true},
	}
	o := NewBoardViewOverlay(records, "activity")

	view := o.View()
	if !strings.Contains(view, "Activity") || !strings.Contains(view, "built-in") {
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
	if selected.ViewID != "activity" {
		t.Fatalf("selected view = %q, want activity", selected.ViewID)
	}
}
