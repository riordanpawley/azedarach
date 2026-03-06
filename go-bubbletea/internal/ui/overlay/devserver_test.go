package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type devServerToggleMsg struct{}

func TestDevServerOverlay_Update_EnterStartsServerWhenListEmpty(t *testing.T) {
	var toggledServerID string

	menu := NewDevServerOverlay(
		nil,
		"AZE-76",
		func(serverID string) tea.Cmd {
			toggledServerID = serverID
			return func() tea.Msg { return devServerToggleMsg{} }
		},
		nil,
		nil,
		nil,
	)

	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("expected enter to trigger toggle command when no dev servers are configured")
	}

	msg := cmd()
	if _, ok := msg.(devServerToggleMsg); !ok {
		t.Fatalf("expected devServerToggleMsg, got %T", msg)
	}
	if toggledServerID != "AZE-76" {
		t.Fatalf("expected toggle callback to use issue ID AZE-76, got %q", toggledServerID)
	}
}
