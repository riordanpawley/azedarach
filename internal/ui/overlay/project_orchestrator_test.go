package overlay

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProjectOrchestratorOverlayGolden(t *testing.T) {
	for _, tc := range []struct {
		name     string
		viewport tea.WindowSizeMsg
	}{
		{name: "default", viewport: tea.WindowSizeMsg{Width: 120, Height: 36}},
		{name: "small", viewport: tea.WindowSizeMsg{Width: 42, Height: 12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewProjectOrchestratorOverlay(ProjectOrchestratorDetails{Project: "azedarach", Status: "orchestrator working live=true", Ready: 3, Review: 2, WaitingHuman: 1, OwnedElsewhere: 4}, nil)
			_, _ = o.Update(tc.viewport)
			assertOverlayGolden(t, filepath.Join("testdata", "project_orchestrator_"+tc.name+".golden"), o.View())
		})
	}
}

func TestProjectOrchestratorOverlayResponsiveSize(t *testing.T) {
	overlay := NewProjectOrchestratorOverlay(ProjectOrchestratorDetails{Project: "azedarach", Status: "orchestrator working"}, nil)
	for _, viewport := range []tea.WindowSizeMsg{{Width: 120, Height: 36}, {Width: 42, Height: 12}} {
		_, _ = overlay.Update(viewport)
		width, height := overlay.Size()
		if width > viewport.Width || height > viewport.Height {
			t.Fatalf("Size() = %dx%d exceeds viewport %dx%d", width, height, viewport.Width, viewport.Height)
		}
		view := overlay.View()
		if !strings.Contains(view, "azedarach") || !strings.Contains(view, "s start") {
			t.Fatalf("View() missing project or actions: %q", view)
		}
	}
}

func TestProjectOrchestratorOverlayRoutesActions(t *testing.T) {
	var action string
	overlay := NewProjectOrchestratorOverlay(ProjectOrchestratorDetails{}, func(next string) tea.Cmd {
		action = next
		return nil
	})
	_, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if action != "start" {
		t.Fatalf("start action = %q", action)
	}
	_, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if action != "attach" {
		t.Fatalf("attach action = %q", action)
	}
	_, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if action != "stop" {
		t.Fatalf("stop action = %q", action)
	}
}
