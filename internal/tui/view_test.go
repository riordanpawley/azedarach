package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/toast"
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
	if strings.Contains(lastLine, "test toast") || strings.Contains(lastLine, "ui.toast") {
		t.Fatalf("expected status bar to omit full notification text; last line=%q", lastLine)
	}
	if !strings.Contains(view, "test toast") {
		t.Fatalf("expected floating notification stack to include toast message; view=%q", view)
	}
}

func TestViewWithToastsRendersBoundedFloatingStack(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	expires := time.Now().Add(time.Hour)
	for _, message := range []string{"first notice", "second notice", "third notice", "fourth notice"} {
		m.addToast(types.Toast{
			Message: message,
			Expires: expires,
		})
	}

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.height {
		t.Fatalf("view with toast stack is too tall: got %d lines, want <= %d", len(lines), m.height)
	}
	if strings.Contains(view, "first notice") {
		t.Fatalf("expected oldest toast to be hidden from bounded stack; view=%q", view)
	}
	for _, message := range []string{"second notice", "third notice", "fourth notice"} {
		if !strings.Contains(view, message) {
			t.Fatalf("expected visible stack to include %q; view=%q", message, view)
		}
	}
	lastLine := lines[len(lines)-1]
	if strings.Contains(lastLine, "fourth notice") {
		t.Fatalf("expected footer to stay free of notification text; last line=%q", lastLine)
	}
}

func TestViewWithToastFitsNarrowViewport(t *testing.T) {
	m := newTestModel()
	m.width = 32
	m.height = 12
	m.loading = false
	m.addToast(types.Toast{
		Level:   types.ToastError,
		Message: "mutation failed because the daemon returned a long validation message",
		Expires: time.Now().Add(time.Hour),
	})

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.height {
		t.Fatalf("narrow view with toast is too tall: got %d lines, want <= %d", len(lines), m.height)
	}
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "NORMAL") {
		t.Fatalf("expected footer to remain visible on narrow viewport; last line=%q", lastLine)
	}
	if strings.Contains(lastLine, "mutation failed") {
		t.Fatalf("expected footer to omit notification text on narrow viewport; last line=%q", lastLine)
	}
	if !strings.Contains(view, "mutation failed") {
		t.Fatalf("expected wrapped floating notification in narrow viewport; view=%q", view)
	}
}

func TestLayerNotificationStackWithLargeToastKeepsTopContentVisible(t *testing.T) {
	m := newTestModel()
	m.width = 100
	contentHeight := 23
	m.toasts = []types.Toast{{
		Level:   types.ToastError,
		Message: "operation failed: " + strings.Repeat("very-large-validation-error-detail ", 80),
		Expires: time.Now().Add(time.Hour),
	}}
	stack := toast.New(m.styles).RenderWithin(m.toasts, m.width, notificationStackHeight(contentHeight))
	if !strings.Contains(stack, "...") {
		t.Fatalf("expected direct toast render to show truncation marker; heightLimit=%d frame=%d stack=%q", notificationStackHeight(contentHeight), m.styles.ToastError.GetVerticalFrameSize(), stack)
	}
	contentLines := make([]string, 0, contentHeight)
	contentLines = append(contentLines, "Open (2)"+strings.Repeat(" ", 92))
	for len(contentLines) < contentHeight {
		contentLines = append(contentLines, strings.Repeat("board row ", 12))
	}
	content := strings.Join(contentLines, "\n")

	rendered := m.layerNotificationStack(content, m.width, contentHeight)
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(lines) > contentHeight {
		t.Fatalf("content with large toast is too tall: got %d lines, want <= %d", len(lines), contentHeight)
	}
	if !strings.Contains(lines[0], "Open (") {
		t.Fatalf("expected board header to remain visible above large toast; first line=%q", lines[0])
	}
	if !strings.Contains(rendered, "...") {
		t.Fatalf("expected large toast preview to show truncation marker; rendered=%q", rendered)
	}
}

func TestToastLayerIsOpaqueWithinToastRectangle(t *testing.T) {
	m := newTestModel()
	m.width = 36
	m.height = 8
	m.loading = false
	m.toasts = []types.Toast{{
		Level:   types.ToastInfo,
		Message: "queued for cug",
		Expires: time.Now().Add(time.Hour),
	}}

	content := strings.Join([]string{
		strings.Repeat("x", m.width),
		strings.Repeat("x", m.width),
		strings.Repeat("x", m.width),
		strings.Repeat("x", m.width),
		strings.Repeat("x", m.width),
		strings.Repeat("x", m.width),
		strings.Repeat("x", m.width),
		strings.Repeat("x", m.width),
	}, "\n")

	rendered := m.layerNotificationStack(content, m.width, m.height)
	for _, line := range strings.Split(ansi.Strip(rendered), "\n") {
		if strings.Contains(line, "queuedxfor") || strings.Contains(line, "forxcug") {
			t.Fatalf("toast interior leaked underlying content through spaces: %q", line)
		}
	}
}

func TestViewWithOverlayRendersToastAboveOverlay(t *testing.T) {
	m := newTestModel()
	m.width = 40
	m.height = 12
	m.loading = false
	m.overlayStack.Push(&testOverlay{})
	m.addToast(types.Toast{
		Level:   types.ToastInfo,
		Message: "toast over overlay",
		Expires: time.Now().Add(time.Hour),
	})

	view := m.View()
	if !strings.Contains(view, "toast over overlay") {
		t.Fatalf("expected toast to render above overlay; view=%q", view)
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

func TestViewWithModalOverlaySkipsBoardBackdropRender(t *testing.T) {
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
	if strings.Contains(lines[0], "Open (") {
		t.Fatalf("expected modal overlay backdrop to avoid full board render; first line=%q", lines[0])
	}
	if !strings.Contains(view, "test overlay") {
		t.Fatalf("expected modal overlay content to remain visible")
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
	if !strings.Contains(last, "ctrl+g") || !strings.Contains(last, "close all") {
		t.Fatalf("expected status bar to include close-all hint; last line=%q", last)
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

func TestView_TypedTreeLayoutUsesDaemonProjectionOrder(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.width = 120
	m.height = 24
	m.tasks = m.tasks[:2]
	m.boardView = domain.DefaultBoardView()
	m.boardView.Layout = domain.BoardViewLayoutTreeList
	m.boardOrdered = []domain.Task{m.tasks[1], m.tasks[0]}

	view := m.View()
	if strings.Contains(strings.Split(view, "\n")[0], "Open (") {
		t.Fatalf("tree-list view rendered column board: %q", view)
	}
	first, second := strings.Index(view, "Task 2"), strings.Index(view, "Task 1")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("projected order not preserved: Task 2=%d Task 1=%d\n%s", first, second, view)
	}
}

func TestView_TypedHorizontalGridUsesDistinctRenderer(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.width = 120
	m.height = 24
	m.tasks = m.tasks[:2]
	m.boardView = domain.DefaultBoardView()
	m.boardView.Layout = domain.BoardViewLayoutHorizontalGrid
	m.boardOrdered = append([]domain.Task(nil), m.tasks...)
	view := m.View()
	if strings.Contains(strings.Split(view, "\n")[0], "Open (") {
		t.Fatalf("grid rendered board columns:\n%s", view)
	}
	if !strings.Contains(view, "Task 1") || !strings.Contains(view, "Task 2") {
		t.Fatalf("grid omitted tasks:\n%s", view)
	}
}

func TestView_TypedHorizontalGridFitsNarrowViewport(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.width = 12
	m.height = 12
	m.tasks = m.tasks[:1]
	m.boardView = domain.DefaultBoardView()
	m.boardView.Layout = domain.BoardViewLayoutHorizontalGrid
	m.boardOrdered = append([]domain.Task(nil), m.tasks...)
	for _, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("line width = %d > %d: %q", width, m.width, line)
		}
	}
}

func TestView_ProjectOrchestratorStatusFitsDefaultAndNarrow(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{{"default", 120, 30}, {"narrow", 52, 18}} {
		t.Run(size.name, func(t *testing.T) {
			m := newTestModel()
			m.width, m.height, m.loading = size.width, size.height, false
			project := projectOrchestratorSnapshot{
				Name: "azedarach", ProjectID: "project-a",
				Snapshot: &protocol.OrchestrationSnapshot{
					Lifecycle:   domain.OrchestratorWorking,
					Capacity:    protocol.OrchestrationCapacity{TotalCountingCapacityCount: 2},
					Constraints: protocol.OrchestrationConstraints{AgentCapacity: 4},
					Runnable:    []string{"dci", "dcj"}, Reviews: []protocol.OrchestrationCandidate{{IssueID: "dck"}},
					Interactions: []domain.InteractionRequest{{ID: "request-1"}}, OwnershipConflicts: []protocol.OrchestrationCandidate{{IssueID: "dcm"}},
					Health:       protocol.OrchestrationHealth{Healthy: false, Diagnostics: []string{"watch cursor stale"}},
					RecentEvents: []protocol.MailEvent{{Type: "worker-progress", Body: "implemented typed projection"}},
				},
				Session: &protocol.OrchestratorSessionResult{Lifecycle: domain.OrchestratorWorking, Live: true},
			}
			m.projectOrchestrator = &project

			view := normalizeTUIGolden(ansi.Strip(m.View()))
			wants := []string{}
			if size.name == "default" {
				wants = []string{"orchestrator working live=true", "workers 2/4"}
			}
			for _, want := range wants {
				if !strings.Contains(view, want) {
					t.Fatalf("normal chrome missing %q:\n%s", want, view)
				}
			}
			if size.name == "default" && !strings.Contains(view, "O: orchestra") {
				t.Fatalf("normal chrome missing orchestrator key hint:\n%s", view)
			}
			for i, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
				if got := lipgloss.Width(line); got > size.width {
					t.Fatalf("line %d width=%d, want <=%d: %q", i, got, size.width, line)
				}
			}
		})
	}
}

func TestProjectOrchestratorKeyOpensLoadingDetailsBeforeSnapshotArrives(t *testing.T) {
	m := newTestModel()
	updatedAny, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	updated := updatedAny.(Model)
	if cmd == nil {
		t.Fatal("O did not schedule overlay open and snapshot refresh")
	}
	if _, ok := updated.overlayStack.Current().(*overlay.ProjectOrchestratorOverlay); !ok {
		t.Fatalf("O opened %T, want project orchestrator details", updated.overlayStack.Current())
	}
}

func assertTUIGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if updateDir := strings.TrimSpace(os.Getenv("AZEDARACH_TUI_GOLDEN_OUTPUT_DIR")); updateDir != "" {
		if err := os.MkdirAll(updateDir, 0o755); err != nil {
			t.Fatalf("create golden output dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(updateDir, name), []byte(got), 0o644); err != nil {
			t.Fatalf("write generated golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("render differs from %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

func normalizeTUIGolden(view string) string {
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestView_RendersFreshnessIndicatorAcrossBoardAndCompactViews(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.taskSnapshotCheckedAt = time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	m.taskSnapshotFreshness = protocol.TaskListFreshnessStale

	boardView := m.View()
	if !strings.Contains(boardView, "stale 11:02:00") {
		t.Fatalf("board view = %q, want compact freshness indicator", boardView)
	}
	if !strings.Contains(strings.Split(strings.TrimRight(boardView, "\n"), "\n")[0], "Open (") {
		t.Fatalf("expected board headers to remain on first line, got %q", strings.Split(strings.TrimRight(boardView, "\n"), "\n")[0])
	}

	updated, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	compactModel := updated.(Model)
	compactView := compactModel.View()
	if !strings.Contains(compactView, "stale 11:02:00") {
		t.Fatalf("compact view = %q, want compact freshness indicator", compactView)
	}
}

func TestView_TaskWorkspaceShowsFreshnessTimestampAndStatus(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.taskSnapshotCheckedAt = time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	m.taskSnapshotFreshness = protocol.TaskListFreshnessFresh

	workspace := overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30)
	workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
	m.overlayStack.Push(workspace)

	view := m.View()
	if !strings.Contains(view, "Freshness:") || !strings.Contains(view, "fresh") {
		t.Fatalf("view = %q, want freshness status in detail pane", view)
	}
	if !strings.Contains(view, "Checked:") || !strings.Contains(view, "2026-04-02 11:02:00") {
		t.Fatalf("view = %q, want freshness timestamp in detail pane", view)
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
