package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGitPaneActions(t *testing.T) {
	tests := []struct{ key, want string }{{"r", "git_pane_refresh"}, {"p", "git_pane_pull"}, {"P", "git_pane_push"}}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			o := NewGitPaneOverlay("main")
			_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)})
			msg, ok := cmd().(SelectionMsg)
			if !ok || msg.Key != tt.want {
				t.Fatalf("message = %#v, want SelectionMsg key %q", msg, tt.want)
			}
		})
	}
}

func TestGitPaneResponsiveSize(t *testing.T) {
	o := NewGitPaneOverlay("main")
	o.ApplyWindowSize(tea.WindowSizeMsg{Width: 54, Height: 18})
	w, h := o.Size()
	if w > 54 || h > 18 {
		t.Fatalf("size = %dx%d exceeds viewport", w, h)
	}
}
