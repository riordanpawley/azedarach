package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
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
	for _, want := range []string{"j/k", "move", "enter", "task workspace"} {
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
	if !strings.Contains(compactView, "Switched to compact view") && !strings.Contains(compactView, "ui.toast") {
		t.Fatalf("expected compact view footer to reflect view-mode toast, got %q", compactView)
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
	for _, want := range []string{"dirty", "+12/-3", "ahead 1 behind 0", "worker-progress: wired mailbox progress into overview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "older notes should not win") || strings.Contains(view, "acceptance should not show") {
		t.Fatalf("overview should show one latest progress line, got:\n%s", view)
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
	for _, want := range []string{"Chefy", "progress", "activity: busy"} {
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
			Name:  "alpha",
			Err:   errors.New("Linear read sync timed out after 2s; showing local-first data"),
			Tasks: []domain.Task{task},
		},
	}

	view := m.View()
	for _, want := range []string{"degraded: 2 backend", "alpha", "degraded: backend timeout", task.ID.String(), "busy", "git dirty +5/-1", "progress", "local task progress should remain visible"} {
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
	for _, want := range []string{"hidden/degraded projects", "Chefy degraded: backend timeout", "otel-tui no sessions"} {
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
			m.orchestrationOverview = []orchestrationProjectOverview{
				{Name: "alpha", Tasks: m.tasks[:2]},
				{Name: "beta", Tasks: m.tasks[2:]},
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
		})
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
