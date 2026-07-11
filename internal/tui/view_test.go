package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
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

func TestView_OrchestrationOverviewStatusBarShowsOverviewHints(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	task := m.tasks[0]
	task.HasTmuxSession = true
	m.orchestrationOverview = []orchestrationProjectOverview{{Name: "azedarach", Tasks: []domain.Task{task}}}

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected non-empty rendered view")
	}
	lastLine := lines[len(lines)-1]
	for _, want := range []string{"↑/↓", "j/k", "row", "←/→", "h/l", "project", "Home/End", "g/G", "top/end", "Enter", "open"} {
		if !strings.Contains(lastLine, want) {
			t.Fatalf("overview status bar missing %q; last line=%q", want, lastLine)
		}
	}
	if strings.Contains(lastLine, "g:goto") {
		t.Fatalf("overview status bar should not show board goto hint; last line=%q", lastLine)
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
	if !strings.Contains(compactView, "Switched to compact view") {
		t.Fatalf("expected compact view to render floating view-mode toast, got %q", compactView)
	}

	updated, _ = compactModel.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	overviewModel := updated.(Model)
	overviewView := overviewModel.View()
	overviewLines := strings.Split(strings.TrimRight(overviewView, "\n"), "\n")
	if len(overviewLines) == 0 {
		t.Fatal("expected overview view to render at least one line")
	}
	if !strings.Contains(overviewLines[0], "Orchestration") {
		t.Fatalf("expected orchestration overview header, got %q", overviewLines[0])
	}
	if !strings.Contains(overviewView, "sessions") {
		t.Fatalf("expected overview to include session summary, got %q", overviewView)
	}
	if got := getCursorPosition(overviewModel); got.Column != 0 || got.Task != 1 {
		t.Fatalf("cursor position changed in overview: got (%d,%d), want (0,1)", got.Column, got.Task)
	}

	updated, _ = overviewModel.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
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

func TestView_OrchestrationOverviewShowsProgressAndGitWithoutDumpingEverything(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	task := m.tasks[0]
	task.HasTmuxSession = true
	task.HasUncommittedChanges = true
	task.GitAdditions = 12
	task.GitDeletions = 3
	task.GitAheadCount = 1
	task.Notes = "older notes should not win"
	task.Acceptance = "acceptance should not show when mail exists"
	m.orchestrationOverview = []orchestrationProjectOverview{
		{
			Name:  "alpha",
			Tasks: []domain.Task{task},
			MailByTask: map[string]protocol.MailEvent{
				task.ID.String(): {
					IssueID: naming.IssueID(task.ID.String()),
					Type:    "worker-progress",
					Body:    "wired mailbox progress into overview\nextra detail",
					Seq:     7,
				},
			},
		},
	}

	view := m.View()
	for _, want := range []string{"dirty", "+12/-3", "ahead 1 behind 0", "mail", "worker-progress: wired mailbox progress into overview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "older notes should not win") || strings.Contains(view, "acceptance should not show") {
		t.Fatalf("overview should show one latest progress line, got:\n%s", view)
	}
}

func TestView_OrchestrationOverviewSummarizesWorkerEvidenceMail(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	task := m.tasks[0]
	task.HasTmuxSession = true
	m.orchestrationOverview = []orchestrationProjectOverview{{
		Name:  "alpha",
		Tasks: []domain.Task{task},
		MailByTask: map[string]protocol.MailEvent{
			task.ID.String(): {
				IssueID: naming.IssueID(task.ID.String()),
				Type:    "worker-progress",
				Body:    `{"schema":"worker_evidence.v1","summary":"Startup and investigation complete for fuo.","commands_run":["just test"],"key_assertions":["validation passed"],"review":{"status":"clean","findings":[]},"risks":["none"]}`,
				Seq:     9,
			},
		},
	}}

	view := ansi.Strip(m.View())
	for _, want := range []string{"mail", "worker-progress: evidence | Startup and investigation complete for fuo. | review clean | risks none"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing summarized evidence %q:\n%s", want, view)
		}
	}
	for _, notWant := range []string{`"schema"`, `"worker_evidence.v1"`, `"commands_run"`} {
		if strings.Contains(view, notWant) {
			t.Fatalf("overview should not render raw evidence JSON %q:\n%s", notWant, view)
		}
	}
}

func TestView_OrchestrationOverviewGroupsObservationRowsByActionability(t *testing.T) {
	m := newTestModel()
	m.width = 140
	m.height = 34
	m.loading = false
	m.viewMode = ViewModeOverview
	observedAt := time.Date(2026, time.July, 6, 1, 0, 0, 0, time.UTC)
	m.orchestrationOverviewLoadedAt = observedAt.Add(2 * time.Hour)

	waiting := domain.Task{
		ID:                    naming.IssueID("ctn"),
		Title:                 "Render observation-driven TUI overview",
		Status:                domain.StatusInProgress,
		Session:               &domain.Session{IssueID: naming.IssueID("ctn"), State: domain.SessionWaiting, Activity: "waiting"},
		HasTmuxSession:        true,
		HasUncommittedChanges: true,
		GitAdditions:          4,
		GitDeletions:          1,
	}
	review := domain.Task{
		ID:             naming.IssueID("ctp"),
		Title:          "Worker observation projection",
		Status:         domain.StatusInReview,
		Session:        &domain.Session{IssueID: naming.IssueID("ctp"), State: domain.SessionIdle, Activity: "idle"},
		HasTmuxSession: true,
	}
	blocked := domain.Task{
		ID:             naming.IssueID("ctf"),
		Title:          "Blocked dependency",
		Status:         domain.StatusInProgress,
		Session:        &domain.Session{IssueID: naming.IssueID("ctf"), State: domain.SessionIdle, Activity: "idle"},
		HasTmuxSession: true,
	}
	working := domain.Task{
		ID:             naming.IssueID("ctw"),
		Title:          "Busy worker",
		Status:         domain.StatusInProgress,
		Session:        &domain.Session{IssueID: naming.IssueID("ctw"), State: domain.SessionBusy, Activity: "busy"},
		HasTmuxSession: true,
	}
	cleanup := domain.Task{
		ID:             naming.IssueID("ctx"),
		Title:          "Closed worker cleanup",
		Status:         domain.StatusDone,
		Session:        &domain.Session{IssueID: naming.IssueID("ctx"), State: domain.SessionIdle, Activity: "idle"},
		HasTmuxSession: true,
	}
	m.orchestrationOverview = []orchestrationProjectOverview{{
		Name:  "azedarach",
		Tasks: []domain.Task{waiting, review, blocked, working, cleanup},
		Observations: []domain.WorkerObservation{
			{
				IssueID: "ctn",
				State:   domain.WorkerObservationWaitingHuman,
				Reason:  "active session is waiting for input",
				LastEvent: &domain.WorkerObservationEventSummary{
					Kind:    "issue_event",
					Type:    "human.input_requested",
					At:      observedAt,
					Summary: "worker asks which backend to trust",
				},
				EvidenceSummary: []string{"session waiting prompt"},
				NextActions:     []string{"answer worker prompt"},
			},
			{
				IssueID:         "ctp",
				State:           domain.WorkerObservationReviewReady,
				Reason:          "worker submitted integration evidence",
				EvidenceSummary: []string{"structured worker_evidence.v1"},
				NextActions:     []string{"validate evidence"},
			},
			{IssueID: "ctf", State: domain.WorkerObservationBlocked, Reason: "dependency is unresolved", NextActions: []string{"inspect blocker"}},
			{IssueID: "ctw", State: domain.WorkerObservationWorking, Reason: "session is busy", NextActions: []string{"monitor worker"}},
			{IssueID: "ctx", State: domain.WorkerObservationCleanupPending, Reason: "worker closed and session remains", NextActions: []string{"cleanup session"}},
		},
	}}

	view := ansi.Strip(m.View())
	needsIdx := strings.Index(view, "Needs You")
	reviewIdx := strings.Index(view, "Review Ready")
	blockedIdx := strings.Index(view, "Blocked/Failed/Stale")
	workingIdx := strings.Index(view, "Working")
	cleanupIdx := strings.Index(view, "Cleanup")
	if needsIdx < 0 || reviewIdx < 0 || blockedIdx < 0 || workingIdx < 0 || cleanupIdx < 0 {
		t.Fatalf("overview missing observation groups:\n%s", view)
	}
	if !(needsIdx < reviewIdx && reviewIdx < blockedIdx && blockedIdx < workingIdx && workingIdx < cleanupIdx) {
		t.Fatalf("overview groups out of order:\n%s", view)
	}
	for _, want := range []string{
		"attention: needs-you 1  review 1  blocked/stale 1  git 1",
		"needs 1  review 1  blocked/stale 1  git 1",
		"ctn needs-you age 2h0m0s session waiting git dirty +4/-1",
		"active session is waiting for input | next: answer worker prompt | evidence: session waiting prompt",
		"last: issue_event human.input_requested worker asks which backend to trust | Render observation-driven TUI overview",
		"ctp review-ready age unknown session idle",
		"worker submitted integration evidence | next: validate evidence | evidence: structured worker_evidence.v1",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing observation row detail %q:\n%s", want, view)
		}
	}
}

func TestView_OrchestrationOverviewHidesRowsWithoutRuntimeSessions(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	visible := domain.Task{
		ID:             naming.IssueID("ctn"),
		Title:          "Visible worker",
		Status:         domain.StatusInProgress,
		Session:        &domain.Session{IssueID: naming.IssueID("ctn"), State: domain.SessionBusy, Activity: "busy"},
		HasTmuxSession: true,
	}
	hidden := domain.Task{
		ID:     naming.IssueID("ctp"),
		Title:  "Review without runtime",
		Status: domain.StatusInReview,
	}
	m.orchestrationOverview = []orchestrationProjectOverview{{
		Name:  "azedarach",
		Tasks: []domain.Task{visible, hidden},
		Observations: []domain.WorkerObservation{
			{IssueID: "ctn", State: domain.WorkerObservationWorking, Reason: "active session is present"},
			{IssueID: "ctp", State: domain.WorkerObservationReviewReady, Reason: "issue is in_review"},
			{IssueID: "ctq", State: domain.WorkerObservationRunnable, Reason: "leaf worker has no blockers"},
		},
	}}

	view := ansi.Strip(m.View())
	for _, want := range []string{"1 projects", "1 sessions", "ctn", "active session is present"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing visible runtime row %q:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"ctp", "ctq", "Review Ready", "runnable", "issue is in_review", "leaf worker has no blockers"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("overview should hide non-session row %q:\n%s", notWant, view)
		}
	}

	refs := orchestrationOverviewTaskRefs(m.overviewProjectsForInteraction())
	if len(refs) != 1 || refs[0].Task.ID != visible.ID {
		t.Fatalf("overview refs = %+v, want only %s", refs, visible.ID)
	}
}

func TestOverviewMailParentsPrefersActiveIssueParent(t *testing.T) {
	parent := naming.IssueID("parent-1")
	tasks := []domain.Task{
		{ID: naming.IssueID("cqb"), HasTmuxSession: true},
		{ID: naming.IssueID("child-1"), ParentID: &parent, HasTmuxSession: true},
	}

	parents := overviewMailParents(tasks, "root-1")

	want := []string{"root-1", "cqb", "parent-1", "child-1"}
	if strings.Join(parents, ",") != strings.Join(want, ",") {
		t.Fatalf("mail parents = %v, want %v", parents, want)
	}
}

func TestParseOverviewSessionStatusTasksSkipsNonIssueShells(t *testing.T) {
	status := strings.Join([]string{
		"Active Sessions (3):",
		"",
		"ISSUE ID\tSTATUS\tACTIVITY\tTITLE",
		"-------\t------\t--------\t-----",
		"az\tunknown\tbusy\t(not in issues)",
		"cif\tin_progress\tidle\tEffect v3 to v4 migration",
		"dih\tin_review\tbusy\tinternationalization epic",
		"",
		"Use 'az attach <issue-id>' to attach to a session",
	}, "\n")

	tasks := parseOverviewSessionStatusTasks(status)

	if len(tasks) != 2 {
		t.Fatalf("parsed tasks = %+v, want 2 issue sessions", tasks)
	}
	if tasks[0].ID.String() != "cif" || tasks[0].Status != domain.StatusInProgress || tasks[0].Session.DisplayLabel() != "idle" {
		t.Fatalf("first parsed task = %+v", tasks[0])
	}
	if tasks[1].ID.String() != "dih" || tasks[1].Status != domain.StatusInReview || tasks[1].Session.DisplayLabel() != "busy" {
		t.Fatalf("second parsed task = %+v", tasks[1])
	}
}

func TestView_OrchestrationOverviewFallsBackToSessionActivityProgress(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	task := domain.Task{
		ID:             naming.IssueID("cif"),
		Title:          "Effect v3 to v4 migration",
		Status:         domain.StatusInProgress,
		Type:           domain.TypeTask,
		Session:        &domain.Session{IssueID: naming.IssueID("cif"), State: domain.SessionBusy, Activity: "busy"},
		HasTmuxSession: true,
	}
	m.orchestrationOverview = []orchestrationProjectOverview{{Name: "Chefy", Err: errors.New("Linear read sync timed out"), Tasks: []domain.Task{task}}}

	view := m.View()
	for _, want := range []string{"Chefy", "activity", "busy"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing activity progress fallback %q:\n%s", want, view)
		}
	}
}

func TestView_OrchestrationOverviewKeepsSessionsVisibleWhenBackendDegraded(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	m.orchestrationOverviewBackendErrors = 2
	m.orchestrationOverviewHiddenProjects = 1
	task := m.tasks[0]
	task.Status = domain.StatusInProgress
	task.Session = &domain.Session{
		IssueID:  task.ID,
		State:    domain.SessionBusy,
		Activity: string(domain.SessionBusy),
	}
	task.HasTmuxSession = true
	task.HasUncommittedChanges = true
	task.GitAdditions = 5
	task.GitDeletions = 1
	task.Notes = "local task progress should remain visible"
	m.orchestrationOverview = []orchestrationProjectOverview{
		{
			Name:     "alpha",
			Err:      errors.New("Linear read sync timed out after 2s; showing local-first data"),
			Fallback: "local state",
			Tasks:    []domain.Task{task},
		},
	}

	view := m.View()
	for _, want := range []string{"2 degraded", "alpha", "local state fallback  backend timeout", task.ID.String(), "busy", "git dirty +5/-1", "notes", "local task progress should remain visible"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing %q while backend degraded:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Linear read sync timed out") {
		t.Fatalf("overview should not promote optional backend timeout into card body:\n%s", view)
	}
}

func TestView_OrchestrationOverviewFallsBackToRuntimeSessions(t *testing.T) {
	m := newTestModel()
	m.width = 100
	m.height = 20
	m.loading = false
	m.viewMode = ViewModeOverview
	m.tasks = nil
	m.sessions = map[string]*domain.Session{
		"cqb": {
			IssueID:  naming.IssueID("cqb"),
			State:    domain.SessionWaiting,
			Activity: string(domain.SessionWaiting),
			Worktree: "/tmp/cqb",
		},
	}

	view := m.View()
	for _, want := range []string{"1 sessions", "cqb", "waiting"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing runtime session fallback %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "No active sessions") {
		t.Fatalf("overview should not render empty state when runtime sessions exist:\n%s", view)
	}
}

func TestView_OrchestrationOverviewShowsHiddenProjectLabels(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	task := m.tasks[0]
	task.HasTmuxSession = true
	m.orchestrationOverview = []orchestrationProjectOverview{{Name: "azedarach", Tasks: []domain.Task{task}}}
	m.orchestrationOverviewHiddenProjects = 2
	m.orchestrationOverviewBackendErrors = 1
	m.orchestrationOverviewHiddenLabels = []string{"Chefy degraded: backend timeout", "otel-tui no sessions"}

	view := m.View()
	for _, want := range []string{"hidden projects", "Chefy degraded: backend timeout", "otel-tui no sessions"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing hidden project label %q:\n%s", want, view)
		}
	}
}

func TestView_OrchestrationOverviewNavigationOpensSelectedTaskWorkspace(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	first := m.tasks[0]
	first.HasTmuxSession = true
	second := m.tasks[1]
	second.HasTmuxSession = true
	m.orchestrationOverview = []orchestrationProjectOverview{{Name: "azedarach", Tasks: []domain.Task{first, second}}}

	updatedAny, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyDown})
	updated := updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 1 {
		t.Fatalf("overview cursor = %d, want 1", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyUp})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 0 {
		t.Fatalf("overview cursor after up = %d, want 0", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 1 {
		t.Fatalf("overview cursor after j = %d, want 1", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 0 {
		t.Fatalf("overview cursor after k = %d, want 0", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnd})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 1 {
		t.Fatalf("overview cursor after end = %d, want 1", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyHome})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 0 {
		t.Fatalf("overview cursor after home = %d, want 0", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyDown})
	updated = updatedAny.(Model)
	openedAny, _ := updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	opened := openedAny.(Model)
	workspace, ok := opened.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace overlay, got %T", opened.overlayStack.Current())
	}
	if workspace.TaskID() != second.ID.String() {
		t.Fatalf("workspace task = %s, want %s", workspace.TaskID(), second.ID)
	}
}

func TestView_OrchestrationOverviewProjectNavigationJumpsBetweenProjects(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	firstProjectFirst := m.tasks[0]
	firstProjectFirst.HasTmuxSession = true
	firstProjectSecond := m.tasks[1]
	firstProjectSecond.HasTmuxSession = true
	secondProjectFirst := m.tasks[2]
	secondProjectFirst.HasTmuxSession = true
	secondProjectSecond := m.tasks[3]
	secondProjectSecond.HasTmuxSession = true
	m.orchestrationOverview = []orchestrationProjectOverview{
		{Name: "azedarach", Tasks: []domain.Task{firstProjectFirst, firstProjectSecond}},
		{Name: "Chefy", Tasks: []domain.Task{secondProjectFirst, secondProjectSecond}},
	}

	updatedAny, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	updated := updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 2 {
		t.Fatalf("overview cursor after right = %d, want 2", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyLeft})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 0 {
		t.Fatalf("overview cursor after left = %d, want 0", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 2 {
		t.Fatalf("overview cursor after l = %d, want 2", updated.orchestrationOverviewCursor)
	}
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 0 {
		t.Fatalf("overview cursor after h = %d, want 0", updated.orchestrationOverviewCursor)
	}
	updated.orchestrationOverviewCursor = 1
	updatedAny, _ = updated.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	updated = updatedAny.(Model)
	if updated.orchestrationOverviewCursor != 2 {
		t.Fatalf("overview cursor after right from second row = %d, want 2", updated.orchestrationOverviewCursor)
	}
}

func TestView_OrchestrationOverviewRemoteSelectionWarnsWhenWorkspaceUnavailable(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	remote := domain.Task{
		ID:             naming.IssueID("remote-1"),
		Title:          "Remote task",
		Status:         domain.StatusInProgress,
		Type:           domain.TypeTask,
		HasTmuxSession: true,
	}
	m.orchestrationOverview = []orchestrationProjectOverview{{Name: "remote-project", Tasks: []domain.Task{remote}}}

	updatedAny, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	updated := updatedAny.(Model)
	if cmd != nil {
		t.Fatalf("remote overview workspace command = %T, want nil", cmd)
	}
	if !updated.overlayStack.IsEmpty() {
		t.Fatalf("remote overview row should not open current-project workspace, got %T", updated.overlayStack.Current())
	}
	if len(updated.toasts) == 0 || !strings.Contains(updated.toasts[len(updated.toasts)-1].Message, "Switch to remote-project") {
		t.Fatalf("expected remote project warning toast, got %+v", updated.toasts)
	}
}

func TestView_OrchestrationOverviewEmptyConsumesWorkspaceKey(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	m.orchestrationOverviewLoadedAt = time.Date(2026, time.June, 24, 15, 0, 0, 0, time.UTC)

	updatedAny, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	updated := updatedAny.(Model)
	if cmd != nil {
		t.Fatalf("empty overview space command = %T, want nil", cmd)
	}
	if !updated.overlayStack.IsEmpty() {
		t.Fatalf("empty overview should not open board task workspace, got %T", updated.overlayStack.Current())
	}
}

func TestView_OrchestrationOverviewEmptyAfterLoadedSnapshot(t *testing.T) {
	m := newTestModel()
	m.width = 80
	m.height = 20
	m.loading = false
	m.viewMode = ViewModeOverview
	m.orchestrationOverviewLoadedAt = time.Date(2026, time.June, 24, 15, 0, 0, 0, time.UTC)

	view := m.View()
	if !strings.Contains(view, "0 sessions") {
		t.Fatalf("overview missing zero-session summary:\n%s", view)
	}
	if !strings.Contains(view, "No active sessions across registered projects.") {
		t.Fatalf("overview missing empty state:\n%s", view)
	}
	if strings.Contains(view, m.tasks[0].Title) {
		t.Fatalf("loaded empty overview should not fall back to non-session tasks:\n%s", view)
	}
}

func TestView_OrchestrationOverviewColumnCountAvoidsCrampedThreeColumnLayout(t *testing.T) {
	projects := []orchestrationProjectOverview{{Name: "one"}, {Name: "two"}, {Name: "three"}}
	if got := overviewColumnCount(projects, 153); got != 2 {
		t.Fatalf("columns at screenshot-width viewport = %d, want 2", got)
	}
	if got := overviewColumnCount(projects, 179); got != 2 {
		t.Fatalf("columns just below wide viewport = %d, want 2", got)
	}
	if got := overviewColumnCount(projects, 180); got != 3 {
		t.Fatalf("columns at wide viewport = %d, want 3", got)
	}
}

func TestView_OrchestrationOverviewFitsDefaultAndNarrowViewports(t *testing.T) {
	for _, tt := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "default", width: 120, height: 30},
		{name: "narrow", width: 52, height: 18},
		{name: "phone", width: 42, height: 14},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.width = tt.width
			m.height = tt.height
			m.loading = false
			m.viewMode = ViewModeOverview
			tasks := append([]domain.Task(nil), m.tasks...)
			for i := range tasks {
				tasks[i].HasTmuxSession = true
			}
			m.orchestrationOverview = []orchestrationProjectOverview{
				{Name: "alpha", Tasks: tasks[:2]},
				{Name: "beta", Tasks: tasks[2:]},
			}

			view := m.View()
			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			if len(lines) > tt.height {
				t.Fatalf("overview rendered %d lines, want <= %d\n%s", len(lines), tt.height, view)
			}
			if len(lines) == 0 || !strings.Contains(lines[0], "Orchestration") {
				if !strings.Contains(lines[0], "Orch") {
					t.Fatalf("overview missing header: %q", view)
				}
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > tt.width {
					t.Fatalf("line %d width = %d, want <= %d: %q", i, got, tt.width, line)
				}
			}
			if tt.name == "phone" {
				for _, line := range lines {
					if strings.Contains(line, "fresh") && strings.Contains(line, "updated") {
						t.Fatalf("phone header has colliding freshness/update labels: %q\n%s", line, view)
					}
				}
				if !strings.Contains(view, "┌") || !strings.Contains(view, "┐") {
					t.Fatalf("phone overview missing complete top border:\n%s", view)
				}
			}
			stripped := ansi.Strip(view)
			if !strings.Contains(stripped, "└") || !strings.Contains(stripped, "┘") {
				t.Fatalf("overview missing complete bottom border:\n%s", view)
			}
		})
	}
}

func TestView_OrchestrationOverviewObservationRowsFitNarrowViewport(t *testing.T) {
	m := newTestModel()
	m.width = 52
	m.height = 18
	m.loading = false
	m.viewMode = ViewModeOverview
	observedAt := time.Date(2026, time.July, 6, 4, 0, 0, 0, time.UTC)
	m.orchestrationOverviewLoadedAt = observedAt.Add(15 * time.Minute)
	m.orchestrationOverview = []orchestrationProjectOverview{{
		Name: "alpha",
		Tasks: []domain.Task{{
			ID:             naming.IssueID("ctn"),
			Title:          "Render overview",
			Status:         domain.StatusInProgress,
			Session:        &domain.Session{IssueID: naming.IssueID("ctn"), State: domain.SessionWaiting, Activity: "waiting"},
			HasTmuxSession: true,
		}},
		Observations: []domain.WorkerObservation{{
			IssueID: "ctn",
			State:   domain.WorkerObservationWaitingHuman,
			Reason:  "needs input",
			LastEvent: &domain.WorkerObservationEventSummary{
				Kind: "mailbox",
				Type: "worker-blocked",
				At:   observedAt,
			},
			NextActions: []string{"answer prompt"},
		}},
	}}

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.height {
		t.Fatalf("overview rendered %d lines, want <= %d\n%s", len(lines), m.height, view)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, m.width, line)
		}
	}
	stripped := ansi.Strip(view)
	for _, want := range []string{"Needs You", "age 15m0s", "next: answer prompt", "NORMAL"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("narrow overview missing %q:\n%s", want, stripped)
		}
	}
}

func TestView_OrchestrationOverviewCompactsHugeGitCounts(t *testing.T) {
	m := newTestModel()
	m.width = 140
	m.height = 24
	m.loading = false
	m.viewMode = ViewModeOverview
	task := m.tasks[0]
	task.HasTmuxSession = true
	task.GitAdditions = 1074031
	task.GitDeletions = 255402
	task.GitAheadCount = 1932
	task.GitBehindCount = 2
	m.orchestrationOverview = []orchestrationProjectOverview{{Name: "Chefy", Tasks: []domain.Task{task}}}

	view := m.View()
	for _, want := range []string{"+1.1M/-255.4k", "ahead 1.9k behind 2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing compact git count %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "1074031") || strings.Contains(view, "255402") {
		t.Fatalf("overview should compact huge git counts:\n%s", view)
	}
}

func TestView_ProjectOrchestratorStatusFitsDefaultAndNarrow(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{{"default", 120, 30}, {"narrow", 52, 18}} {
		t.Run(size.name, func(t *testing.T) {
			m := newTestModel()
			m.width, m.height, m.loading, m.viewMode = size.width, size.height, false, ViewModeOverview
			m.orchestrationOverviewLoadedAt = time.Date(2026, time.July, 11, 2, 0, 0, 0, time.UTC)
			m.orchestrationOverview = []orchestrationProjectOverview{{
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
			}}

			view := ansi.Strip(m.View())
			for _, want := range []string{"orchestrator working live=true", "workers 2/4", "ready 2", "review 1", "waiting-human 1", "owned-elsewhere 1", "watch cursor stale"} {
				if !strings.Contains(view, want) {
					t.Fatalf("project orchestration view missing %q:\n%s", want, view)
				}
			}
			for i, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
				if got := lipgloss.Width(line); got > size.width {
					t.Fatalf("line %d width=%d, want <=%d: %q", i, got, size.width, line)
				}
			}
		})
	}
}

func TestOverviewProjectOrchestratorActionsAndTypedUpdate(t *testing.T) {
	m := newTestModel()
	m.viewMode = ViewModeOverview
	m.orchestrationOverview = []orchestrationProjectOverview{{Name: "empty", ProjectID: "project-a", Snapshot: &protocol.OrchestrationSnapshot{}}}

	for _, key := range []string{"o", "A", "r"} {
		updated, cmd, consumed := m.handleOverviewModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if !consumed || cmd == nil {
			t.Fatalf("key %q consumed=%v cmd=%v, want consumed command", key, consumed, cmd)
		}
		m = updated
	}

	updatedAny, cmd := m.Update(projectOrchestratorActionMsg{
		projectID: "project-a", action: "start",
		result: protocol.OrchestratorSessionResult{Disposition: "started", Lifecycle: domain.OrchestratorWorking, Live: true},
	})
	updated := updatedAny.(Model)
	if cmd == nil || updated.orchestrationOverview[0].Session == nil || !updated.orchestrationOverview[0].Session.Live {
		t.Fatalf("typed update did not apply immediately: session=%+v cmd=%v", updated.orchestrationOverview[0].Session, cmd)
	}
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
