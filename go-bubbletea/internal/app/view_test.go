package app

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/types"
)

func TestViewHeight(t *testing.T) {
	m := newTestModel()
	m.width = 80
	m.height = 24
	m.loading = false

	t.Run("normal view", func(t *testing.T) {
		view := m.View()
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if len(lines) > m.height {
			t.Errorf("Normal view is too tall: got %d lines, want %d", len(lines), m.height)
		}
	})

	t.Run("with overlay", func(t *testing.T) {
		m.overlayStack.Push(&testOverlay{})
		view := m.View()
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if len(lines) > m.height {
			t.Errorf("View with overlay is too tall: got %d lines, want %d", len(lines), m.height)
		}
	})

	t.Run("with toasts", func(t *testing.T) {
		m.overlayStack.Pop()
		m.toasts = append(m.toasts, types.Toast{
			Message: "test toast",
			Expires: time.Now().Add(time.Hour),
		})
		view := m.View()
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if len(lines) > m.height {
			t.Errorf("View with toasts is too tall: got %d lines, want %d", len(lines), m.height)
		}
	})
}

type testOverlay struct{}

func (o *testOverlay) View() string                            { return "test overlay" }
func (o *testOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return o, nil }
func (o *testOverlay) Init() tea.Cmd                           { return nil }
func (o *testOverlay) Title() string                           { return "Test" }
func (o *testOverlay) Size() (int, int)                        { return 20, 10 }
