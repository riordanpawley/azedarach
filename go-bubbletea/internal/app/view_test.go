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

func TestViewWithOverlayKeepsStatusBarVisible(t *testing.T) {
	m := newTestModel()
	m.width = 100
	m.height = 24
	m.loading = false
	m.overlayStack.Push(&testOverlay{})

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected non-empty rendered view")
	}
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "NORMAL") {
		t.Fatalf("expected status bar on final line to include mode label with overlay active; last line=%q", lastLine)
	}
}

func TestViewWithStatusModeOverlayUsesOverlayModeBadge(t *testing.T) {
	m := newTestModel()
	m.width = 100
	m.height = 24
	m.loading = false
	m.overlayStack.Push(&statusModeOverlay{})

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected non-empty rendered view")
	}
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "ACTION") {
		t.Fatalf("expected status bar to use overlay-provided mode; last line=%q", lastLine)
	}
}

func TestOverlayUsesAppFrame(t *testing.T) {
	if !overlayUsesAppFrame(&testOverlay{}) {
		t.Fatalf("expected default overlays to use app frame")
	}
	if overlayUsesAppFrame(&framelessOverlay{}) {
		t.Fatalf("expected frameless overlays to skip app frame")
	}
}

func TestView_TabToggleRendersCompactAndBoardSurfaces(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.editor.EnterNormal()
	m.nav.SelectTask("az-2", 0)

	boardView := m.View()
	boardLines := strings.Split(strings.TrimRight(boardView, "\n"), "\n")
	if len(boardLines) == 0 {
		t.Fatal("expected board view to render at least one line")
	}
	if !strings.Contains(boardLines[0], "Open (") {
		t.Fatalf("expected board headers on first line, got %q", boardLines[0])
	}

	updated, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	compactModel := updated.(Model)
	compactView := compactModel.View()
	compactLines := strings.Split(strings.TrimRight(compactView, "\n"), "\n")
	if len(compactLines) == 0 {
		t.Fatal("expected compact view to render at least one line")
	}
	if strings.Contains(compactLines[0], "Open (") {
		t.Fatalf("expected compact view to replace board headers, got %q", compactLines[0])
	}
	if !strings.Contains(compactLines[0], "#") || !strings.Contains(compactLines[0], "ID") || !strings.Contains(compactLines[0], "Title") {
		t.Fatalf("expected compact header row on first line, got %q", compactLines[0])
	}
	if got := getCursorPosition(compactModel); got.Column != 0 || got.Task != 1 {
		t.Fatalf("cursor position changed across tab toggle: got (%d,%d), want (0,1)", got.Column, got.Task)
	}
	if !strings.Contains(compactView, "Switched to compact view") && !strings.Contains(compactView, "ui.toast") {
		t.Fatalf("expected compact view footer to reflect view-mode toast, got %q", compactView)
	}

	updated, _ = compactModel.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	boardModel := updated.(Model)
	boardView = boardModel.View()
	boardLines = strings.Split(strings.TrimRight(boardView, "\n"), "\n")
	if len(boardLines) == 0 {
		t.Fatal("expected board view to render after toggling back")
	}
	if !strings.Contains(boardLines[0], "Open (") {
		t.Fatalf("expected board headers after toggling back, got %q", boardLines[0])
	}
	if got := getCursorPosition(boardModel); got.Column != 0 || got.Task != 1 {
		t.Fatalf("cursor position changed after toggling back: got (%d,%d), want (0,1)", got.Column, got.Task)
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

type statusModeOverlay struct{ testOverlay }

func (o *statusModeOverlay) StatusMode() types.Mode { return types.ModeAction }

type framelessOverlay struct{ statusModeOverlay }

func (o *framelessOverlay) View() string       { return "frame-free overlay" }
func (o *framelessOverlay) UsesAppFrame() bool { return false }
