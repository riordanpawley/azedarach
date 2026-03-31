package app

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
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
	if strings.Contains(lastLine, "ui.toast") {
		t.Fatalf("expected status bar ticker to hide raw event key; last line=%q", lastLine)
	}
	if !strings.Contains(lastLine, "test toast") {
		t.Fatalf("expected status bar ticker to include toast message; last line=%q", lastLine)
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

func TestRenderedBlockSize_WithFramedOverlayExpandsDimensions(t *testing.T) {
	m := newTestModel()

	content := "test overlay"
	contentW, contentH := renderedBlockSize(content)
	framed := m.styles.Overlay.Width(contentW).Height(contentH).Render(content)
	framedW, framedH := renderedBlockSize(framed)

	if framedW <= contentW {
		t.Fatalf("expected framed overlay width to expand beyond content width (%d), got %d", contentW, framedW)
	}
	if framedH <= contentH {
		t.Fatalf("expected framed overlay height to expand beyond content height (%d), got %d", contentH, framedH)
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

func TestViewWithFullScreenOverlayReplacesBoardContent(t *testing.T) {
	m := newTestModel()
	m.width = 100
	m.height = 24
	m.loading = false
	m.overlayStack.Push(&fullScreenOverlay{})

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected non-empty rendered view")
	}
	first := lines[0]
	if strings.Contains(first, "Open (") {
		t.Fatalf("expected board headers to be replaced in full-screen overlay mode; first line=%q", first)
	}
	if !strings.Contains(view, "FULL-SCREEN CONTENT") {
		t.Fatalf("expected full-screen overlay content to be visible, got %q", view)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "ACTION") {
		t.Fatalf("expected status bar to remain visible with overlay mode badge; last line=%q", last)
	}
}

func TestViewWithOverlayStatusBindingsUsesOverlayHints(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.overlayStack.Push(&hintOverlay{})

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected non-empty rendered view")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "j/k") || !strings.Contains(last, "scroll") {
		t.Fatalf("expected status bar to include overlay-provided hints; last line=%q", last)
	}
}

func TestWindowSizeMsgForwardedToActiveOverlay(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	resize := &resizeAwareOverlay{}
	m.overlayStack.Push(resize)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 77, Height: 33})
	model := updated.(Model)
	got := model.overlayStack.Current().(*resizeAwareOverlay)
	if !got.seen {
		t.Fatalf("expected overlay to receive window size message")
	}
	if got.lastW != 77 || got.lastH != 33 {
		t.Fatalf("expected forwarded size 77x33, got %dx%d", got.lastW, got.lastH)
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

func TestMergeOverlayLine_PreservesOutsideSpan(t *testing.T) {
	bottom := "1111111111"
	top := "   XX     "
	got := mergeOverlayLine(bottom, top)
	if got != "111XX11111" {
		t.Fatalf("mergeOverlayLine result=%q want %q", got, "111XX11111")
	}
}

func TestNonSpaceBounds(t *testing.T) {
	left, right, ok := nonSpaceBounds("   abc  ")
	if !ok {
		t.Fatalf("expected bounds for non-space line")
	}
	if left != 3 || right != 6 {
		t.Fatalf("unexpected bounds: left=%d right=%d", left, right)
	}
}

func TestLayerCenteredOverlay_ReplacesOnlyOverlayRect(t *testing.T) {
	m := newTestModel()
	bottom := strings.Join([]string{
		"AAAAAAAAAA",
		"BBBBBBBBBB",
		"CCCCCCCCCC",
		"DDDDDDDDDD",
		"EEEEEEEEEE",
	}, "\n")
	overlay := strings.Join([]string{
		"XX  ",
		"X  X",
		"XXXX",
	}, "\n")

	got := m.layerCenteredOverlay(bottom, overlay, 10, 5, 4, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
	if lines[0] != "AAAAAAAAAA" || lines[4] != "EEEEEEEEEE" {
		t.Fatalf("expected rows outside overlay rect to remain unchanged, got %q / %q", lines[0], lines[4])
	}
	if lines[1] != "BBBXX  BBB" {
		t.Fatalf("unexpected line 1: %q", lines[1])
	}
	if lines[2] != "CCCX  XCCC" {
		t.Fatalf("unexpected line 2: %q", lines[2])
	}
	if lines[3] != "DDDXXXXDDD" {
		t.Fatalf("unexpected line 3: %q", lines[3])
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

type fullScreenOverlay struct{ statusModeOverlay }

func (o *fullScreenOverlay) View() string         { return "FULL-SCREEN CONTENT" }
func (o *fullScreenOverlay) UsesFullScreen() bool { return true }

type hintOverlay struct{ statusModeOverlay }

func (o *hintOverlay) View() string { return "HINT OVERLAY" }
func (o *hintOverlay) StatusBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "j/k", Description: "scroll"},
		{Key: "Esc", Description: "close"},
	}
}

type resizeAwareOverlay struct {
	testOverlay
	seen         bool
	lastW, lastH int
}

func (o *resizeAwareOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		o.seen = true
		o.lastW = sz.Width
		o.lastH = sz.Height
	}
	return o, nil
}
