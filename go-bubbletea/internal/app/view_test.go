package app

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

func TestViewWithToastKeepsStatusBarVisible(t *testing.T) {
	m := newTestModel()
	m.width = 100
	m.height = 24
	m.loading = false
	m.addToast(types.Toast{
		Message: "test toast",
		Expires: time.Now().Add(time.Hour),
	})

	view := m.View()

	if strings.Contains(view, "test toast") && strings.Contains(strings.Split(strings.TrimRight(view, "\n"), "\n")[0], "test toast") {
		t.Fatalf("expected no floating toast overlay in board content")
	}

	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected non-empty rendered view")
	}
	firstLine := lines[0]
	if !strings.Contains(firstLine, "Open (") {
		t.Fatalf("expected board column headers on first line; first line=%q", firstLine)
	}
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "NORMAL") {
		t.Fatalf("expected status bar on final line to include mode label; last line=%q", lastLine)
	}
	if !strings.Contains(lastLine, "ui.toast") && !strings.Contains(lastLine, "test toast") {
		t.Fatalf("expected status bar ticker to include latest event context; last line=%q", lastLine)
	}
}

func TestLayerWithinHeightTransparent_IgnoresANSISpaceOnlyLines(t *testing.T) {
	m := newTestModel()

	bottom := "line-1\nline-2\nline-3"
	opaqueText := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("toast")
	ansiSpaces := lipgloss.NewStyle().Background(lipgloss.Color("8")).Render("      ")
	top := strings.Join([]string{ansiSpaces, opaqueText, ansiSpaces}, "\n")

	got := m.layerWithinHeightTransparent(bottom, top, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "line-1") {
		t.Fatalf("expected first line to stay from bottom layer, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "toast") {
		t.Fatalf("expected middle line to use top overlay text, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "line-3") {
		t.Fatalf("expected third line to stay from bottom layer, got %q", lines[2])
	}
}

type testOverlay struct{}

func (o *testOverlay) View() string                            { return "test overlay" }
func (o *testOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return o, nil }
func (o *testOverlay) Init() tea.Cmd                           { return nil }
func (o *testOverlay) Title() string                           { return "Test" }
func (o *testOverlay) Size() (int, int)                        { return 20, 10 }
