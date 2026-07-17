package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/testprofile"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

type mockTmuxService struct {
	switchFn func(ctx context.Context, name string) error
	popupFn  func(ctx context.Context, title, width, height, command string) error
}

func TestDecorateConfiguredTreeTasksRendersHierarchyWithoutMutatingProjection(t *testing.T) {
	parentID := naming.IssueID("parent")
	tasks := []domain.Task{
		{ID: parentID, Title: "Parent"},
		{ID: "child", Title: "Child", ParentID: &parentID},
	}
	decorated := decorateConfiguredTreeTasks(tasks, []domain.BoardViewProjectedItem{{Task: tasks[0]}, {Task: tasks[1], Depth: 1}})
	if got := decorated[1].Title; got != "└ Child" {
		t.Fatalf("child title = %q", got)
	}
	if tasks[1].Title != "Child" {
		t.Fatalf("projection task mutated: %q", tasks[1].Title)
	}
}

type probeOverlay struct {
	updated bool
	lastMsg tea.Msg
}

func TestAttachAdvisorSessionCmdSwitchesToDaemonSessionID(t *testing.T) {
	var target string
	m := newTestModel()
	m.tmuxClient = mockTmuxService{switchFn: func(_ context.Context, session string) error { target = session; return nil }}
	msg := m.attachAdvisorSessionCmd("az-advisor-interaction-42")()
	if result := msg.(advisorSessionAttachedMsg); result.err != nil {
		t.Fatal(result.err)
	}
	if target != "az-advisor-interaction-42" {
		t.Fatalf("tmux target = %q", target)
	}
}

func (p *probeOverlay) Init() tea.Cmd { return nil }

func (p *probeOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	p.updated = true
	p.lastMsg = msg
	return p, nil
}

func (p *probeOverlay) View() string { return "probe" }

func (p *probeOverlay) Title() string { return "probe" }

func (p *probeOverlay) Size() (width, height int) { return 10, 5 }

func (m mockTmuxService) SwitchClient(ctx context.Context, name string) error {
	if m.switchFn != nil {
		return m.switchFn(ctx, name)
	}
	return nil
}

func (m mockTmuxService) DisplayPopup(ctx context.Context, title, width, height, command string) error {
	if m.popupFn != nil {
		return m.popupFn(ctx, title, width, height, command)
	}
	return nil
}

type recordingGitSyncService struct {
	fetchCalls int
}

type testCreateOverlayAttachmentService struct{}

func (testCreateOverlayAttachmentService) List(context.Context, string) ([]attachment.Attachment, error) {
	return nil, nil
}

func (testCreateOverlayAttachmentService) AttachFromClipboard(context.Context, string) (*attachment.Attachment, error) {
	return nil, nil
}

func (testCreateOverlayAttachmentService) Attach(context.Context, string, string) (*attachment.Attachment, error) {
	return nil, nil
}

func (testCreateOverlayAttachmentService) Delete(context.Context, string, string) error {
	return nil
}

type testStagedAttachmentService struct {
	attached *attachment.Attachment
}

func (s testStagedAttachmentService) List(context.Context, string) ([]attachment.Attachment, error) {
	return nil, nil
}

func (s testStagedAttachmentService) Attach(context.Context, string, string) (*attachment.Attachment, error) {
	return s.attached, nil
}

func (s testStagedAttachmentService) AttachFromClipboard(context.Context, string) (*attachment.Attachment, error) {
	return nil, nil
}

func (s testStagedAttachmentService) Delete(context.Context, string, string) error {
	return nil
}

func (s *recordingGitSyncService) FetchAndCheck() tea.Cmd {
	s.fetchCalls++
	return nil
}

func (s *recordingGitSyncService) Pull() tea.Cmd {
	return nil
}

func (s *recordingGitSyncService) ShouldNotify(int) bool {
	return false
}

// Helper to create a test model with tasks
func newTestModel() Model {
	cfg := &config.Config{CLITool: "claude"}
	m := New(cfg)

	// Add some test tasks
	// Open column: az-1 (index 0), az-2 (index 1)
	// InProgress column: az-3 (index 0)
	// In Review column: az-4 (index 0)
	// Done column: az-5 (index 0)
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug},
		{ID: "az-3", Title: "Task 3", Status: domain.StatusInProgress, Priority: domain.P0, Type: domain.TypeFeature},
		{ID: "az-4", Title: "Task 4", Status: domain.StatusInReview, Priority: domain.P1, Type: domain.TypeTask},
		{ID: "az-5", Title: "Task 5", Status: domain.StatusDone, Priority: domain.P3, Type: domain.TypeTask},
	}

	m.height = 24 // Set a reasonable terminal height for testing
	m.width = 80

	return m
}

func TestBoardViewSaveSelectionRoutesThroughDaemonMutationCommand(t *testing.T) {
	m := newTestModel()
	// This test verifies message routing, not daemon integration. Avoid coupling
	// it to whichever user-global daemon happens to be running on the host.
	m.daemonClient = nil
	m.overlayStack.Push(overlay.NewBoardViewOverlay(nil, ""))
	view := domain.DefaultBoardView()
	view.ID = "custom"
	view.Title = "Custom"
	updatedAny, cmd := m.Update(overlay.SelectionMsg{Key: overlay.BoardViewSaveKey, Value: overlay.BoardViewSaveMsg{View: view}})
	updated := updatedAny.(Model)
	if !updated.overlayStack.IsEmpty() {
		t.Fatal("management overlay was not closed during mutation")
	}
	if cmd == nil {
		t.Fatal("save mutation command = nil")
	}
	msg := cmd().(boardViewMutatedMsg)
	if msg.action != "save" || msg.viewID != "custom" {
		t.Fatalf("mutation result=%+v", msg)
	}
}

func TestBoardViewMutationSuccessRefreshesViewsAndBoard(t *testing.T) {
	m := newTestModel()
	updatedAny, cmd := m.Update(boardViewMutatedMsg{action: "save", viewID: "custom", scope: m.currentBoardViewCommandScope()})
	updated := updatedAny.(Model)
	if !updated.boardRefreshing || cmd == nil {
		t.Fatalf("refreshing=%v cmd=%v", updated.boardRefreshing, cmd)
	}
	if len(updated.toasts) == 0 || updated.toasts[len(updated.toasts)-1].Level != ToastSuccess {
		t.Fatal("success toast missing")
	}
}

// Helper to get cursor position in a model
func getCursorPosition(m Model) Position {
	columns := m.buildColumns()
	return m.nav.GetPosition(columns)
}

func TestBuildColumnsUsesConfiguredBoardSnapshotColumns(t *testing.T) {
	m := newTestModel()
	view := domain.OrchestrationBoardView()
	m.boardView = view
	m.boardColumns = []domain.BoardViewColumnSnapshot{
		{
			Definition: domain.BoardColumn{ID: domain.BoardColumnWaitingAI, Title: "Waiting AI"},
			Tasks:      []domain.Task{m.tasks[0]},
		},
		{
			Definition: domain.BoardColumn{ID: domain.BoardColumnOpen, Title: "Open"},
			Tasks:      []domain.Task{m.tasks[1]},
		},
		{
			Definition: domain.BoardColumn{ID: domain.BoardColumnWaitingHuman, Title: "Waiting Human"},
		},
	}

	columns := m.buildColumns()
	if got, want := len(columns), 3; got != want {
		t.Fatalf("columns=%d want=%d: %+v", got, want, columns)
	}
	if got, want := columns[0].Title, "Waiting AI"; got != want {
		t.Fatalf("first column title=%q want=%q", got, want)
	}
	if got, want := columns[0].Tasks[0].ID.String(), "az-1"; got != want {
		t.Fatalf("first column task=%q want=%q", got, want)
	}
	if got, want := columns[1].Title, "Open"; got != want {
		t.Fatalf("second column title=%q want=%q", got, want)
	}
	if got, want := columns[2].Title, "Waiting Human"; got != want {
		t.Fatalf("third column title=%q want=%q", got, want)
	}
}

func TestBoardRendersAuthoritativeChildProgressWhenChildIsOmitted(t *testing.T) {
	m := newTestModel()
	parent := domain.Task{ID: "az-parent", Title: "Parent", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask}
	m.tasks = []domain.Task{parent}
	m.boardView = domain.DefaultBoardView()
	m.boardColumns = []domain.BoardViewColumnSnapshot{{Definition: m.boardView.Columns[0], Tasks: []domain.Task{parent}}}
	m.boardProjection = domain.BoardViewProjection{ChildProgress: []domain.BoardChildProgress{{ParentID: parent.ID, Done: 3, Total: 4}}}

	got := ansi.Strip(m.renderBoardView())
	if !strings.Contains(got, "[3/4]") {
		t.Fatalf("board did not render authoritative child progress:\n%s", got)
	}
}

func TestBuildColumnsHonorsConfiguredHiddenEmptyColumns(t *testing.T) {
	m := newTestModel()
	view := domain.OrchestrationBoardView()
	view.Options.HideEmptyColumns = true
	m.boardView = view
	m.boardColumns = []domain.BoardViewColumnSnapshot{
		{
			Definition: domain.BoardColumn{ID: domain.BoardColumnWaitingHuman, Title: "Waiting Human"},
		},
		{
			Definition: domain.BoardColumn{ID: domain.BoardColumnOpen, Title: "Open"},
			Tasks:      []domain.Task{m.tasks[0]},
		},
	}

	columns := m.buildColumns()
	if got, want := len(columns), 1; got != want {
		t.Fatalf("columns=%d want=%d: %+v", got, want, columns)
	}
	if got, want := columns[0].Title, "Open"; got != want {
		t.Fatalf("remaining column title=%q want=%q", got, want)
	}
}

func TestBoardViewportSupportsOrchestrationViewColumns(t *testing.T) {
	m := newTestModel()
	view := domain.OrchestrationBoardView()
	tasks := make([]domain.Task, len(view.Columns))
	snapshots := make([]domain.BoardViewColumnSnapshot, len(view.Columns))
	for i, column := range view.Columns {
		tasks[i] = domain.Task{
			ID:       naming.IssueID(fmt.Sprintf("az-%d", i+1)),
			Title:    fmt.Sprintf("Task %d", i+1),
			Status:   domain.StatusOpen,
			Priority: domain.P1,
			Type:     domain.TypeTask,
		}
		snapshots[i] = domain.BoardViewColumnSnapshot{
			Definition: column,
			Tasks:      []domain.Task{tasks[i]},
		}
	}
	m.tasks = tasks
	m.boardView = view
	m.boardColumns = snapshots
	m.width = 80
	m.height = 24

	columns := m.buildColumns()
	if got, want := len(columns), len(view.Columns); got != want {
		t.Fatalf("columns=%d want=%d", got, want)
	}
	if got, want := columns[0].Title, "Waiting Human"; got != want {
		t.Fatalf("activity column 0 title=%q want=%q", got, want)
	}
	if got, want := columns[1].Title, "Waiting AI"; got != want {
		t.Fatalf("activity column 1 title=%q want=%q", got, want)
	}
	if got, want := columns[2].Title, "Working"; got != want {
		t.Fatalf("activity column 2 title=%q want=%q", got, want)
	}

	lastColumn := len(columns) - 1
	m.nav.SelectTask(tasks[lastColumn].ID.String(), lastColumn)
	m.ensureCursorVisible(columns)
	start, end := m.boardColumnLayout(columns).Range()
	wantStart := len(columns) - board.VisibleColumnCount(len(columns), m.width)
	if got, want := start, wantStart; got != want {
		t.Fatalf("visible column start=%d want=%d", got, want)
	}
	if got, want := end, len(columns); got != want {
		t.Fatalf("visible column end=%d want=%d", got, want)
	}
}

func TestHelperMethods(t *testing.T) {
	m := newTestModel()

	t.Run("currentColumn", func(t *testing.T) {
		// Set cursor to task in Open column
		m.nav.SelectTask("az-1", 0)
		col := m.currentColumn()
		if len(col) != 2 {
			t.Errorf("Expected 2 tasks in Open column, got %d", len(col))
		}

		// Set cursor to task in InProgress column
		m.nav.SelectTask("az-3", 1)
		col = m.currentColumn()
		if len(col) != 1 {
			t.Errorf("Expected 1 task in In Progress column, got %d", len(col))
		}
	})

	t.Run("tasksInColumn", func(t *testing.T) {
		tasks := m.tasksInColumn(domain.StatusOpen)
		if len(tasks) != 2 {
			t.Errorf("Expected 2 tasks with StatusOpen, got %d", len(tasks))
		}

		tasks = m.tasksInColumn(domain.StatusDone)
		if len(tasks) != 1 {
			t.Errorf("Expected 1 task with StatusDone, got %d", len(tasks))
		}
	})

	t.Run("NavigationService.GetPosition", func(t *testing.T) {
		columns := m.buildColumns()

		// Test finding task by ID
		m.nav.SelectTask("az-1", 0)
		pos := m.nav.GetPosition(columns)
		if !pos.Valid {
			t.Error("Expected valid position for az-1")
		}
		if pos.Column != 0 || pos.Task != 0 {
			t.Errorf("Expected az-1 at (0,0), got (%d,%d)", pos.Column, pos.Task)
		}

		// Test finding second task in Open column
		m.nav.SelectTask("az-2", 0)
		pos = m.nav.GetPosition(columns)
		if pos.Column != 0 || pos.Task != 1 {
			t.Errorf("Expected az-2 at (0,1), got (%d,%d)", pos.Column, pos.Task)
		}

		// Test finding task in different column
		m.nav.SelectTask("az-4", 2)
		pos = m.nav.GetPosition(columns)
		if pos.Column != 2 || pos.Task != 0 {
			t.Errorf("Expected az-4 at (2,0), got (%d,%d)", pos.Column, pos.Task)
		}

		// Test fallback when task not found
		m.nav.SelectTask("nonexistent", 1)
		pos = m.nav.GetPosition(columns)
		if pos.Column != 1 {
			t.Errorf("Expected fallback to column 1, got %d", pos.Column)
		}
	})

	t.Run("halfPage", func(t *testing.T) {
		m.height = 24
		half := m.halfPage()
		// (24 - 3) / 4 = 5 cards, half = 2
		if half < 1 {
			t.Errorf("Expected at least 1, got %d", half)
		}

		m.height = 4
		half = m.halfPage()
		if half != 1 {
			t.Errorf("Expected minimum of 1, got %d", half)
		}
	})
}

func TestBuildColumnsUsesIssueDisplayPhases(t *testing.T) {
	m := New(&config.Config{CLITool: "claude"})
	m.tasks = []domain.Task{
		{ID: "az-backlog", Title: "Backlog", Status: domain.StatusOpen, State: mustModelIssueState(t, domain.IssueWorkflowBacklog), Priority: domain.P0, Type: domain.TypeTask},
		{ID: "az-open", Title: "Open", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-waiting-review", Title: "Waiting review", Status: domain.StatusInReview, Priority: domain.P2, Type: domain.TypeTask, Session: &domain.Session{Activity: string(domain.SessionWaiting)}},
		{ID: "az-idle-review", Title: "Idle review", Status: domain.StatusInReview, Priority: domain.P2, Type: domain.TypeTask, Session: &domain.Session{Activity: string(domain.SessionIdle)}},
		{ID: "az-done", Title: "Done", Status: domain.StatusDone, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-cancelled", Title: "Cancelled", Status: domain.StatusCancelled, Priority: domain.P2, Type: domain.TypeTask},
	}

	columns := m.buildColumns()
	titles := make([]string, 0, len(columns))
	tasksByTitle := map[string][]domain.Task{}
	for _, col := range columns {
		titles = append(titles, col.Title)
		tasksByTitle[col.Title] = col.Tasks
	}
	wantTitles := []string{"Backlog", "Open", "In Progress", "In Review", "Done", "Cancelled"}
	if !reflect.DeepEqual(titles, wantTitles) {
		t.Fatalf("column titles = %#v, want %#v", titles, wantTitles)
	}
	if got := tasksByTitle["Backlog"]; len(got) != 1 || got[0].ID.String() != "az-backlog" {
		t.Fatalf("backlog column tasks = %+v", got)
	}
	if got := tasksByTitle["Open"]; len(got) != 1 || got[0].ID.String() != "az-open" {
		t.Fatalf("open column tasks = %+v", got)
	}
	if got := tasksByTitle["In Progress"]; len(got) != 1 || got[0].ID.String() != "az-waiting-review" {
		t.Fatalf("in progress column tasks = %+v", got)
	}
	if got := tasksByTitle["In Review"]; len(got) != 1 || got[0].ID.String() != "az-idle-review" {
		t.Fatalf("in review column tasks = %+v", got)
	}
	if got := tasksByTitle["Cancelled"]; len(got) != 1 || got[0].ID.String() != "az-cancelled" {
		t.Fatalf("cancelled column tasks = %+v", got)
	}
}

func mustModelIssueState(t *testing.T, workflow domain.IssueWorkflow) domain.IssueState {
	t.Helper()
	state, err := domain.NewIssueState(domain.IssueStateParts{Workflow: workflow})
	if err != nil {
		t.Fatalf("NewIssueState(%s): %v", workflow, err)
	}
	return state
}

func TestRuntimeEventSummary_CompactsAndTruncates(t *testing.T) {
	evt := protocol.EventEnvelope{
		Event: "clipboard.error",
		Body:  []byte("line one\nline   two\r\nline three"),
	}

	summary := runtimeEventSummary(evt)
	if strings.Contains(summary, "\n") || strings.Contains(summary, "\r") {
		t.Fatalf("summary contains line breaks: %q", summary)
	}
	if !strings.Contains(summary, "line one line two line three") {
		t.Fatalf("summary was not compacted as expected: %q", summary)
	}

	longBody := strings.Repeat("x", eventSummaryMaxRunes+50)
	longSummary := runtimeEventSummary(protocol.EventEnvelope{
		Event: "ui.toast",
		Body:  []byte(longBody),
	})
	if len([]rune(longSummary)) > eventSummaryMaxRunes {
		t.Fatalf("long summary was not truncated: len=%d summary=%q", len([]rune(longSummary)), longSummary)
	}
	if !strings.HasSuffix(longSummary, "…") {
		t.Fatalf("truncated summary must end with ellipsis: %q", longSummary)
	}
}

func TestResolveTUILogFilePath_UsesSessionLogDir(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	t.Chdir(t.TempDir())
	cfg := &config.Config{
		Session: config.SessionConfig{
			LogDir: "/tmp/azedarach-user-logs",
		},
	}

	got := resolveTUILogFilePath(cfg)
	want := filepath.Join("/tmp/azedarach-user-logs", logging.TUILogFileName)
	if got != want {
		t.Fatalf("resolveTUILogFilePath() = %q, want %q", got, want)
	}
}

func TestResolveTUILogFilePath_UsesScopedWorktreeDirInManagedRunMode(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "managed-run")
	t.Setenv("PATH", "")
	t.Chdir(nested)

	cfg := &config.Config{
		Session: config.SessionConfig{
			LogDir: "/tmp/azedarach-user-logs",
		},
	}
	got := resolveTUILogFilePath(cfg)
	want := filepath.Join(worktree, ".azedarach", logging.TUILogFileName)
	if got != want {
		t.Fatalf("resolveTUILogFilePath() = %q, want %q", got, want)
	}
}

func TestNewWithOptions_DoesNotWriteTUILogsDuringTests(t *testing.T) {
	logDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = logDir

	_ = NewWithOptions(cfg)

	logPath := filepath.Join(logDir, logging.TUILogFileName)
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test TUI construction wrote diagnostic log file %s: %v", logPath, err)
	}
}

func TestDaemonLogFilePath_UsesSessionLogDir(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	m := newTestModel()
	m.repoDir = "/tmp/worktree"
	m.runtimeRepoDir = ""
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = "/tmp/azedarach-user-logs"
	m.config = cfg

	got := m.daemonLogFilePath()
	want := filepath.Join("/tmp/azedarach-user-logs", logging.DaemonLogFileName)
	if got != want {
		t.Fatalf("daemonLogFilePath() = %q, want %q", got, want)
	}
}

func TestDaemonLogFilePath_UsesScopedWorktreeDirInManagedRunMode(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "managed-run")
	t.Setenv("PATH", "")
	t.Chdir(nested)

	m := newTestModel()
	m.repoDir = repo

	got := m.daemonLogFilePath()
	want := filepath.Join(worktree, ".azedarach", logging.DaemonLogFileName)
	if got != want {
		t.Fatalf("daemonLogFilePath() = %q, want %q", got, want)
	}
}

func TestOpenLogStreamCmd_SkipsMissingPathsWhenAnotherLogExists(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()

	tmpDir := t.TempDir()
	tuiLog := filepath.Join(tmpDir, logging.TUILogFileName)
	if err := os.WriteFile(tuiLog, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write tui log: %v", err)
	}
	missingDaemonLog := filepath.Join(tmpDir, logging.DaemonLogFileName)

	var popupCommand string
	m.tmuxClient = mockTmuxService{
		popupFn: func(_ context.Context, title, width, height, command string) error {
			popupCommand = command
			return nil
		},
	}

	msg := m.openLogStreamCmd(missingDaemonLog, tuiLog)()
	selection, ok := msg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("openLogStreamCmd() returned %T, want overlay.SelectionMsg", msg)
	}
	if selection.Key != "event-log-opened" {
		t.Fatalf("selection key = %q, want %q", selection.Key, "event-log-opened")
	}
	if got, ok := selection.Value.(string); !ok || got != tuiLog {
		t.Fatalf("opened value = %#v, want %q", selection.Value, tuiLog)
	}
	if !strings.Contains(popupCommand, "az log --lines 200 --source 'tui'") {
		t.Fatalf("popup command = %q, want az log tui source stream", popupCommand)
	}
	if strings.Contains(popupCommand, "tail -n +1 -F") {
		t.Fatalf("popup command unexpectedly used tail fallback: %q", popupCommand)
	}
}

func TestInferLogSourcesFromPaths(t *testing.T) {
	paths := []string{
		"/tmp/project/.azedarach/azd.log",
		"/tmp/user/.azedarach/logs/az-tui.log",
		"/tmp/user/.azedarach/logs/az-cli.log",
		"/tmp/user/.azedarach/logs/az-tui.log",
		"/tmp/custom/debug.log",
	}

	got := inferLogSourcesFromPaths(paths)
	want := []string{"daemon", "tui", "cli"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inferLogSourcesFromPaths() = %v, want %v", got, want)
	}
}

func TestUpdate_ForwardsNonKeyMessagesToActiveOverlay(t *testing.T) {
	m := newTestModel()
	probe := &probeOverlay{}
	m.overlayStack.Push(probe)

	customMsg := struct{ name string }{name: "async-result"}
	updatedModel, _ := m.Update(customMsg)
	next, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("expected Model return type, got %T", updatedModel)
	}

	current, ok := next.overlayStack.Current().(*probeOverlay)
	if !ok {
		t.Fatalf("expected probe overlay on stack, got %T", next.overlayStack.Current())
	}
	if !current.updated {
		t.Fatal("expected non-key message to be forwarded to active overlay")
	}
	if got, ok := current.lastMsg.(struct{ name string }); !ok || got.name != customMsg.name {
		t.Fatalf("overlay received wrong message: %#v", current.lastMsg)
	}
}

func TestOverlayCtrlGClosesEntireStack(t *testing.T) {
	m := newTestModel()
	bottom := &probeOverlay{}
	top := &probeOverlay{}
	m.overlayStack.Push(bottom)
	m.overlayStack.Push(top)

	updated, cmd := m.handleOverlayKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd == nil {
		t.Fatal("expected ctrl+g to emit a close-all command")
	}
	next := updated.(Model)
	if top.updated {
		t.Fatal("expected ctrl+g to be handled globally before forwarding to top overlay")
	}

	closeMsg := cmd()
	if _, ok := closeMsg.(overlay.CloseAllOverlaysMsg); !ok {
		t.Fatalf("ctrl+g command emitted %T, want overlay.CloseAllOverlaysMsg", closeMsg)
	}
	closed, _ := next.Update(closeMsg)
	finalModel := closed.(Model)
	if !finalModel.overlayStack.IsEmpty() {
		t.Fatalf("expected ctrl+g to close the whole overlay stack, current=%T", finalModel.overlayStack.Current())
	}
}

func TestUpdate_OpenImagePreviewMsgPushesPreviewOverlay(t *testing.T) {
	m := newTestModel()
	m.overlayStack.Push(overlay.NewImageAttachOverlay("axu", m.attachmentService))

	updated, _ := m.Update(overlay.OpenImagePreviewMsg{
		IssueID:      "axu",
		InitialIndex: 0,
	})

	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model return type, got %T", updated)
	}
	if _, ok := next.overlayStack.Current().(*overlay.ImagePreviewOverlay); !ok {
		t.Fatalf("expected image preview overlay on stack, got %T", next.overlayStack.Current())
	}
}

func TestUpdate_OpenTaskImageAttachMsgPushesAttachOverlay(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(overlay.OpenTaskImageAttachMsg{IssueID: "axu"})

	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model return type, got %T", updated)
	}
	if _, ok := next.overlayStack.Current().(*overlay.ImageAttachOverlay); !ok {
		t.Fatalf("expected image attach overlay on stack, got %T", next.overlayStack.Current())
	}
}

func TestUpdate_AttachmentActionDeletedAddsToast(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(overlay.AttachmentActionMsg{Action: "deleted"})

	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model return type, got %T", updated)
	}
	if len(next.toasts) == 0 {
		t.Fatal("expected toast to be recorded")
	}
	last := next.toasts[len(next.toasts)-1]
	if !strings.Contains(last.Message, "Attachment deleted") {
		t.Fatalf("unexpected toast message: %q", last.Message)
	}
	if len(next.runtimeEvents) == 0 {
		t.Fatal("expected runtime event for toast")
	}
	if got := next.runtimeEvents[len(next.runtimeEvents)-1].Event; got != "ui.toast" {
		t.Fatalf("last runtime event = %q, want %q", got, "ui.toast")
	}
	next.width = 100
	next.height = 24
	next.loading = false
	view := next.View()
	lines := strings.Split(view, "\n")
	footer := lines[len(lines)-1]
	if strings.Contains(footer, "Attachment deleted") {
		t.Fatalf("toast event should not populate footer text, footer=%q", footer)
	}
	if !strings.Contains(footer, "1 notice (N)") {
		t.Fatalf("footer should route through notification indicator, footer=%q", footer)
	}
}

func TestNotificationHistoryRetainsOverflowAndDismissalState(t *testing.T) {
	m := newTestModel()
	m.width = 100
	m.height = 24
	m.loading = false
	expires := time.Now().Add(time.Hour)
	for _, message := range []string{"first notice for ctk", "second notice for ctk", "third notice for ctk", "fourth notice for ctk"} {
		m.addToast(Toast{
			Level:   ToastError,
			Message: message,
			Expires: expires,
		})
	}

	if got, want := len(m.notificationHistory), 4; got != want {
		t.Fatalf("notification history len = %d, want %d", got, want)
	}
	if got, want := m.notificationHistoryIndicator(), "4 errors (N)"; got != want {
		t.Fatalf("notification history indicator = %q, want %q", got, want)
	}
	view := m.View()
	if strings.Contains(view, "first notice for ctk") {
		t.Fatalf("oldest notice should be hidden from floating stack, view=%q", view)
	}
	if !strings.Contains(view, "4 errors (N)") {
		t.Fatalf("footer should route to history with compact error count, view=%q", view)
	}

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd != nil {
		t.Fatalf("dismiss latest toast cmd = %T, want nil", cmd)
	}
	next := updated.(Model)
	if len(next.toasts) != 3 {
		t.Fatalf("active toasts len = %d, want 3", len(next.toasts))
	}
	if len(next.notificationHistory) != 4 {
		t.Fatalf("notification history len after dismiss = %d, want 4", len(next.notificationHistory))
	}
	if !next.notificationHistory[len(next.notificationHistory)-1].Dismissed {
		t.Fatalf("latest history entry should be marked dismissed")
	}
}

func TestNotificationHistoryDrawerPreservesUnreadEntries(t *testing.T) {
	m := newTestModel()
	m.addToast(Toast{
		Level:   ToastWarning,
		Message: "mutation warning for az-1",
		Expires: time.Now().Add(time.Hour),
	})

	updated, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	if cmd == nil {
		t.Fatal("expected command to open notification history overlay")
	}
	m = updated.(Model)
	if _, ok := m.overlayStack.Current().(*overlay.NotificationHistoryOverlay); !ok {
		t.Fatalf("current overlay = %T, want notification history overlay", m.overlayStack.Current())
	}
	if got := m.notificationHistoryIndicator(); got == "" {
		t.Fatal("notification history indicator should remain until entries are marked read")
	}
	for _, entry := range m.notificationHistory {
		if entry.Read {
			t.Fatalf("notification history entry should remain unread: %+v", entry)
		}
	}
}

func TestNotificationHistoryRetainsExpiredToasts(t *testing.T) {
	m := newTestModel()
	m.addToast(Toast{
		Level:   ToastError,
		Message: "expired failure for az-1",
		Expires: time.Now().Add(-time.Minute),
	})

	m.expireToasts()

	if len(m.toasts) != 0 {
		t.Fatalf("active toasts len = %d, want 0", len(m.toasts))
	}
	if len(m.notificationHistory) != 1 {
		t.Fatalf("notification history len = %d, want 1", len(m.notificationHistory))
	}
	if got := m.notificationHistory[0].Message; got != "expired failure for az-1" {
		t.Fatalf("history message = %q, want expired toast message", got)
	}
	if got := m.notificationHistoryIndicator(); got != "1 error (N)" {
		t.Fatalf("notification history indicator = %q, want 1 error (N)", got)
	}
}

func TestSpinnerTickExpiresToastsWithoutIssueRefreshTick(t *testing.T) {
	m := newTestModel()
	m.addToast(Toast{
		Level:   ToastWarning,
		Message: "short lived notice for az-1",
		Expires: time.Now().Add(-time.Second),
	})
	if len(m.toasts) != 0 {
		t.Fatalf("expired toast should not materialize initially, got %+v", m.toasts)
	}

	m.addToast(Toast{
		Level:   ToastWarning,
		Message: "expires before next issue refresh",
		Expires: time.Now().Add(time.Hour),
	})
	if len(m.toasts) != 1 {
		t.Fatalf("active toasts len = %d, want 1", len(m.toasts))
	}
	m.feedback.localNotices[len(m.feedback.localNotices)-1].Toast.Expires = time.Now().Add(-time.Second)

	updated, _ := m.Update(spinner.TickMsg{})
	next := updated.(Model)
	if len(next.toasts) != 0 {
		t.Fatalf("active toasts after spinner tick = %+v, want none", next.toasts)
	}
	if len(next.notificationHistory) == 0 {
		t.Fatal("expected expired toast retained in notification history")
	}
}

func TestDaemonNoticeProjectionFeedsFeedbackSurfaces(t *testing.T) {
	now := time.Now().UTC()
	m := newTestModel()
	notice := protocol.NoticeRecord{
		NoticeID:  "notice-op-close",
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Scope:     protocol.NoticeScope{Type: "task", ID: "az-4"},
		Source: &protocol.NoticeSource{
			OperationID:    naming.OperationID("op-close"),
			OperationKind:  daemonclient.CommandTaskClose,
			OperationState: protocol.OperationStateFailed,
		},
		Severity:          protocol.NoticeSeverityError,
		Category:          "operation_failed",
		State:             protocol.NoticeStateActive,
		Title:             "Close failed",
		Summary:           "Could not move az-4 to Done",
		Detail:            "Resolve child blockers, then retry",
		Cause:             &protocol.NoticeCause{Code: "operation_failed", Message: "child issues remain unresolved"},
		OccurrenceCount:   1,
		FirstOccurrenceAt: now,
		LastOccurrenceAt:  now,
		CreatedAt:         now,
		UpdatedAt:         now,
		RetentionClass:    protocol.NoticeRetentionError,
	}

	m.applyFeedbackNoticeSnapshot([]protocol.NoticeRecord{notice})

	if got := m.notificationHistoryIndicator(); got != "1 error (N)" {
		t.Fatalf("notification indicator = %q, want daemon notice error count", got)
	}
	if len(m.notificationHistory) != 1 || m.notificationHistory[0].DaemonNoticeID != "notice-op-close" {
		t.Fatalf("notification history = %+v, want daemon notice row", m.notificationHistory)
	}
	if len(m.toasts) != 1 || m.toasts[0].Message != "Could not move az-4 to Done" {
		t.Fatalf("toasts = %+v, want daemon notice toast", m.toasts)
	}
	signals := m.runtimeSignalsForBoard()
	if got := signals["az-4"]; got.PendingOperationID != "op-close" || got.PendingOperationState != string(protocol.OperationStateFailed) {
		t.Fatalf("board signal = %+v, want failed daemon notice signal", got)
	}
	progress := m.pendingMutationForTask("az-4")
	if progress == nil {
		t.Fatal("expected daemon notice failure in workspace mutation progress")
	}
	if progress.OperationID != "op-close" ||
		progress.ProgressMessage != "Could not move az-4 to Done" ||
		progress.FailureReason != "child issues remain unresolved" ||
		progress.FailureRecovery != "Resolve child blockers, then retry" ||
		progress.CurrentStatus != domain.StatusInReview {
		t.Fatalf("workspace mutation progress = %+v, want daemon notice failure detail", progress)
	}

	refreshedTask := domain.Task{ID: "az-4", Title: "Task 4", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask}
	if _, ok := m.applyTaskRefresh("az-4", refreshedTask, true); !ok {
		t.Fatal("expected task refresh to apply")
	}
	progress = m.pendingMutationForTask("az-4")
	if progress == nil || progress.CurrentStatus != domain.StatusOpen {
		t.Fatalf("workspace mutation progress after task refresh = %+v, want refreshed Open status", progress)
	}
}

func TestDaemonNoticeEventDismissalClearsProjectedTaskFailure(t *testing.T) {
	now := time.Now().UTC()
	m := newTestModel()
	notice := protocol.NoticeRecord{
		NoticeID:          "notice-op-start",
		ProjectID:         naming.ProjectID(m.daemonProjectID()),
		Scope:             protocol.NoticeScope{Type: "issue", ID: "az-1"},
		Source:            &protocol.NoticeSource{OperationID: naming.OperationID("op-start"), OperationKind: daemonclient.CommandSessionStart, OperationState: protocol.OperationStateFailed},
		Severity:          protocol.NoticeSeverityError,
		Category:          "operation_failed",
		State:             protocol.NoticeStateActive,
		Title:             "Session start failed",
		Summary:           "Could not start az-1",
		Cause:             &protocol.NoticeCause{Message: "tmux unavailable"},
		OccurrenceCount:   1,
		FirstOccurrenceAt: now,
		LastOccurrenceAt:  now,
		CreatedAt:         now,
		UpdatedAt:         now,
		RetentionClass:    protocol.NoticeRetentionError,
	}
	m.applyFeedbackNoticeSnapshot([]protocol.NoticeRecord{notice})
	if progress := m.pendingMutationForTask("az-1"); progress == nil || progress.OperationID != "op-start" {
		t.Fatalf("initial progress = %+v, want daemon notice failure", progress)
	}

	dismissed := notice
	dismissed.State = protocol.NoticeStateDismissed
	dismissed.Read = true
	body, err := json.Marshal(protocol.NoticeEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Revision:  1,
		NoticeID:  dismissed.NoticeID,
		State:     protocol.NoticeStateDismissed,
		Notice:    &dismissed,
		UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("marshal notice event: %v", err)
	}

	m.applyDaemonStreamEvent(protocol.EventEnvelope{
		Revision:  1,
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Event:     protocol.EventNoticeUpdated,
		Body:      body,
	}, false)

	if got := m.notificationHistoryIndicator(); got != "" {
		t.Fatalf("notification indicator after dismiss = %q, want empty", got)
	}
	if progress := m.pendingMutationForTask("az-1"); progress != nil {
		t.Fatalf("progress after dismiss = %+v, want nil", progress)
	}
	if len(m.toasts) != 0 {
		t.Fatalf("toasts after dismiss = %+v, want none", m.toasts)
	}
}

func TestDaemonNoticeProjectionOrdersFloatingToastsByOccurrence(t *testing.T) {
	now := time.Now().UTC()
	m := newTestModel()
	makeNotice := func(id, summary string, offset time.Duration) protocol.NoticeRecord {
		created := now.Add(offset)
		return protocol.NoticeRecord{
			NoticeID:          id,
			ProjectID:         naming.ProjectID(m.daemonProjectID()),
			Scope:             protocol.NoticeScope{Type: "issue", ID: "az-1"},
			Severity:          protocol.NoticeSeverityWarning,
			Category:          "operation_failed",
			State:             protocol.NoticeStateActive,
			Title:             summary,
			Summary:           summary,
			OccurrenceCount:   1,
			FirstOccurrenceAt: created,
			LastOccurrenceAt:  created,
			CreatedAt:         created,
			UpdatedAt:         created,
			RetentionClass:    protocol.NoticeRetentionError,
		}
	}

	m.applyFeedbackNoticeSnapshot([]protocol.NoticeRecord{
		makeNotice("notice-3", "third daemon notice", 3*time.Second),
		makeNotice("notice-1", "first daemon notice", time.Second),
		makeNotice("notice-4", "fourth daemon notice", 4*time.Second),
		makeNotice("notice-2", "second daemon notice", 2*time.Second),
	})

	if got, want := len(m.toasts), 3; got != want {
		t.Fatalf("toasts len = %d, want %d", got, want)
	}
	gotMessages := []string{m.toasts[0].Message, m.toasts[1].Message, m.toasts[2].Message}
	wantMessages := []string{"second daemon notice", "third daemon notice", "fourth daemon notice"}
	if !reflect.DeepEqual(gotMessages, wantMessages) {
		t.Fatalf("toast messages = %v, want latest three in occurrence order %v", gotMessages, wantMessages)
	}
}

func TestDaemonNoticeSnapshotSurvivesTUIRestartMultipleClientsAndViewports(t *testing.T) {
	now := time.Now().UTC()
	base := newTestModel()
	notices := []protocol.NoticeRecord{
		testDaemonNotice(base, "notice-1", "az-1", protocol.NoticeSeverityInfo, "operation_info", "first durable notice", now),
		testDaemonNotice(base, "notice-2", "az-2", protocol.NoticeSeverityWarning, "operation_warning", "second durable notice", now.Add(time.Second)),
		testDaemonNotice(base, "notice-3", "az-3", protocol.NoticeSeverityInfo, "operation_info", "third durable notice", now.Add(2*time.Second)),
		testDaemonNotice(base, "notice-4", "az-4", protocol.NoticeSeverityError, "operation_failed", "failed to close az-4", now.Add(3*time.Second)),
	}
	notices[3].Title = "Close failed"
	notices[3].Detail = "Resolve blockers, then retry"
	notices[3].Cause = &protocol.NoticeCause{Code: "operation_failed", Message: "child blockers remain"}
	notices[3].Source = &protocol.NoticeSource{
		OperationID:    naming.OperationID("op-close-4"),
		OperationKind:  daemonclient.CommandTaskClose,
		OperationState: protocol.OperationStateFailed,
		Producer:       "daemon.operation",
	}

	firstClient := newTestModel()
	firstClient.width = 100
	firstClient.height = 24
	firstClient.loading = false
	firstClient.applyFeedbackNoticeSnapshot(notices)

	restartedClient := newTestModel()
	restartedClient.width = 100
	restartedClient.height = 24
	restartedClient.loading = false
	restartedClient.applyFeedbackNoticeSnapshot(notices)

	secondClient := newTestModel()
	secondClient.width = 48
	secondClient.height = 16
	secondClient.loading = false
	secondClient.applyFeedbackNoticeSnapshot(notices)

	for name, model := range map[string]Model{
		"first client":     firstClient,
		"restarted client": restartedClient,
		"second client":    secondClient,
	} {
		t.Run(name, func(t *testing.T) {
			if got, want := len(model.notificationHistory), 4; got != want {
				t.Fatalf("notification history len = %d, want %d", got, want)
			}
			if got := model.notificationHistoryIndicator(); got != "1 error / 3 notices (N)" {
				t.Fatalf("notification indicator = %q, want durable snapshot counts", got)
			}
			if got, want := len(model.toasts), 3; got != want {
				t.Fatalf("floating toasts len = %d, want bounded latest three", got)
			}
			if strings.Contains(toastMessages(model.toasts), "first durable notice") {
				t.Fatalf("oldest notice should be retained in history but hidden from floating stack: %+v", model.toasts)
			}
			progress := model.pendingMutationForTask("az-4")
			if progress == nil {
				t.Fatal("expected daemon notice to project task failure detail")
			}
			if progress.OperationID != "op-close-4" ||
				progress.ProgressMessage != "failed to close az-4" ||
				progress.FailureReason != "child blockers remain" ||
				progress.FailureRecovery != "Resolve blockers, then retry" ||
				progress.CurrentStatus != domain.StatusInReview {
				t.Fatalf("projected mutation progress = %+v, want durable failure detail", progress)
			}
		})
	}

	defaultView := firstClient.View()
	defaultLines := strings.Split(strings.TrimRight(defaultView, "\n"), "\n")
	if len(defaultLines) > firstClient.height {
		t.Fatalf("default viewport rendered %d lines, want <= %d", len(defaultLines), firstClient.height)
	}
	if !strings.Contains(defaultView, "failed to close az-4") || !strings.Contains(defaultView, "1 error / 3 notices (N)") {
		t.Fatalf("default viewport omitted durable notice surfaces; view=%q", defaultView)
	}
	if strings.Contains(defaultLines[len(defaultLines)-1], "failed to close az-4") {
		t.Fatalf("default footer contains full notice text: %q", defaultLines[len(defaultLines)-1])
	}

	narrowView := secondClient.View()
	narrowLines := strings.Split(strings.TrimRight(narrowView, "\n"), "\n")
	if len(narrowLines) > secondClient.height {
		t.Fatalf("narrow viewport rendered %d lines, want <= %d", len(narrowLines), secondClient.height)
	}
	if !strings.Contains(narrowView, "failed to close") {
		t.Fatalf("narrow viewport omitted wrapped durable notice; view=%q", narrowView)
	}
	if !strings.Contains(narrowLines[len(narrowLines)-1], "N!") && !strings.Contains(narrowLines[len(narrowLines)-1], "(N)") {
		t.Fatalf("narrow footer omitted compact notice route indicator: %q", narrowLines[len(narrowLines)-1])
	}
	for i, line := range narrowLines {
		if width := ansi.StringWidth(line); width > secondClient.width {
			t.Fatalf("narrow line %d width = %d, want <= %d: %q", i, width, secondClient.width, line)
		}
	}
}

func TestNotificationActionCenterMarkReadUsesDaemonNoticeUpdate(t *testing.T) {
	m := newTestModel()
	var gotBody protocol.NoticeUpdateRequestBody
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandNoticeUpdate {
				t.Fatalf("command = %q, want notice.update", req.Command)
			}
			if err := json.Unmarshal(req.Body, &gotBody); err != nil {
				t.Fatalf("unmarshal notice update request: %v", err)
			}
			respBody, err := json.Marshal(protocol.NoticeUpdateResponseBody{
				Notice: protocol.NoticeRecord{
					NoticeID:  gotBody.NoticeID,
					ProjectID: naming.ProjectID(m.daemonProjectID()),
					State:     protocol.NoticeStateActive,
					Read:      gotBody.Read != nil && *gotBody.Read,
				},
			})
			if err != nil {
				return protocol.ResponseEnvelope{}, err
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport).WithProjectID(m.daemonProjectID())

	_, cmd := m.handleNotificationActionCenterSelection(overlay.SelectionMsg{
		Key: "notification_mark_read",
		Value: overlay.NotificationActionCenterMsg{
			DaemonNoticeID: "notice-1",
			Read:           true,
		},
	})
	if cmd == nil {
		t.Fatal("expected notice update command")
	}
	msg, ok := cmd().(noticeUpdateResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("message = %T/%+v, want successful noticeUpdateResultMsg", msg, msg)
	}
	if gotBody.NoticeID != "notice-1" || gotBody.Read == nil || !*gotBody.Read || gotBody.State != "" {
		t.Fatalf("notice update body = %+v, want read-only update", gotBody)
	}
	if msg.label != "Marked read" {
		t.Fatalf("notice update label = %q, want Marked read", msg.label)
	}
}

func TestNotificationActionCenterNoticeUpdateFailureShowsToast(t *testing.T) {
	m := newTestModel()

	updatedAny, _ := m.Update(noticeUpdateResultMsg{
		projectID: m.daemonProjectID(),
		label:     "Marked read",
		err:       errors.New("daemon unavailable"),
	})
	updated := updatedAny.(Model)
	if len(updated.toasts) == 0 {
		t.Fatal("expected notice update failure toast")
	}
	if got := updated.toasts[len(updated.toasts)-1].Message; !strings.Contains(got, "Marked read failed") || !strings.Contains(got, "daemon unavailable") {
		t.Fatalf("toast = %q, want labeled notice update failure", got)
	}
}

func TestNotificationActionCenterRunsDaemonNoticeAction(t *testing.T) {
	m := newTestModel()
	var gotBody protocol.NoticeActionRequestBody
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandNoticeAction {
				t.Fatalf("command = %q, want notice.action", req.Command)
			}
			if err := json.Unmarshal(req.Body, &gotBody); err != nil {
				t.Fatalf("unmarshal notice action request: %v", err)
			}
			respBody, err := json.Marshal(protocol.NoticeActionResponseBody{
				Notice: protocol.NoticeRecord{
					NoticeID:  gotBody.NoticeID,
					ProjectID: naming.ProjectID(m.daemonProjectID()),
					State:     protocol.NoticeStateDismissed,
					Read:      true,
				},
			})
			if err != nil {
				return protocol.ResponseEnvelope{}, err
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport).WithProjectID(m.daemonProjectID())

	_, cmd := m.handleNotificationActionCenterSelection(overlay.SelectionMsg{
		Key: "notification_action",
		Value: overlay.NotificationActionCenterMsg{
			DaemonNoticeID: "notice-1",
			ActionID:       "dismiss",
			Kind:           "dismiss",
			Label:          "Dismiss",
		},
	})
	if cmd == nil {
		t.Fatal("expected notice action command")
	}
	msg, ok := cmd().(noticeActionResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("message = %T/%+v, want successful noticeActionResultMsg", msg, msg)
	}
	if gotBody.NoticeID != "notice-1" || gotBody.ActionID != "dismiss" {
		t.Fatalf("notice action body = %+v, want dismiss action", gotBody)
	}
}

func TestNotificationActionCenterLocalCopyDetailsUsesClipboardHelper(t *testing.T) {
	m := newTestModel()
	old := writeNotificationClipboardText
	var copied string
	writeNotificationClipboardText = func(_ context.Context, text string) error {
		copied = text
		return nil
	}
	defer func() { writeNotificationClipboardText = old }()

	_, cmd := m.handleNotificationActionCenterSelection(overlay.SelectionMsg{
		Key: "notification_action",
		Value: overlay.NotificationActionCenterMsg{
			ActionID: "copy_details",
			Kind:     "client.copy_details",
			Details:  "failure details\noperation: op-1",
		},
	})
	if cmd == nil {
		t.Fatal("expected copy details command")
	}
	msg, ok := cmd().(notificationCopyDetailsResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("message = %T/%+v, want successful notificationCopyDetailsResultMsg", msg, msg)
	}
	if copied != "failure details\noperation: op-1" {
		t.Fatalf("copied = %q, want notice details", copied)
	}
}

func TestNotificationActionCenterOpenTaskRoutesUseExistingWorkspaceRoute(t *testing.T) {
	for _, tt := range []struct {
		name     string
		actionID string
		kind     string
		scope    string
	}{
		{name: "open task", actionID: "open_task", kind: "client.open_task", scope: "task"},
		{name: "open workspace", actionID: "open_workspace", kind: "client.open_workspace", scope: "task"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.overlayStack.Push(overlay.NewNotificationHistoryOverlay(nil))

			updatedAny, _ := m.handleNotificationActionCenterSelection(overlay.SelectionMsg{
				Key: "notification_action",
				Value: overlay.NotificationActionCenterMsg{
					ActionID:  tt.actionID,
					Kind:      tt.kind,
					ScopeType: tt.scope,
					ScopeID:   "az-1",
				},
			})
			updated := updatedAny.(Model)
			if _, ok := updated.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); !ok {
				t.Fatalf("current overlay = %T, want TaskWorkspaceOverlay", updated.overlayStack.Current())
			}
		})
	}
}

func testDaemonNotice(m Model, id, taskID string, severity protocol.NoticeSeverity, category, summary string, occurredAt time.Time) protocol.NoticeRecord {
	return protocol.NoticeRecord{
		NoticeID:          id,
		ProjectID:         naming.ProjectID(m.daemonProjectID()),
		Scope:             protocol.NoticeScope{Type: "issue", ID: taskID},
		Severity:          severity,
		Category:          category,
		State:             protocol.NoticeStateActive,
		Title:             summary,
		Summary:           summary,
		OccurrenceCount:   1,
		FirstOccurrenceAt: occurredAt,
		LastOccurrenceAt:  occurredAt,
		CreatedAt:         occurredAt,
		UpdatedAt:         occurredAt,
		RetentionClass:    protocol.NoticeRetentionError,
	}
}

func toastMessages(toasts []Toast) string {
	messages := make([]string, 0, len(toasts))
	for _, toast := range toasts {
		messages = append(messages, toast.Message)
	}
	return strings.Join(messages, "\n")
}

func TestUpdate_AttachmentActionStagedAddsToast(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(overlay.AttachmentActionMsg{
		Action: "staged",
		Attachment: &attachment.Attachment{
			Filename: "clipboard.png",
		},
	})

	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model return type, got %T", updated)
	}
	if len(next.toasts) == 0 {
		t.Fatal("expected toast to be recorded")
	}
	last := next.toasts[len(next.toasts)-1]
	if !strings.Contains(last.Message, "Attachment staged for new task") {
		t.Fatalf("unexpected toast message: %q", last.Message)
	}
}

func TestAttachStagedAttachmentsReportsNoteAppendFailure(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskAppendNotes {
				return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command %q", req.Command)
			}
			return protocol.ResponseEnvelope{}, errors.New("append denied")
		},
	}
	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.attachmentService = testStagedAttachmentService{
		attached: &attachment.Attachment{
			ID:       "att-1",
			IssueID:  "az-123",
			Filename: "clip.png",
			Path:     filepath.Join(t.TempDir(), "attached.png"),
			Relative: ".azedarach/attachments/att-1-clip.png",
			MimeType: "image/png",
			Size:     4,
			Created:  time.Now(),
		},
	}

	sourcePath := filepath.Join(t.TempDir(), "clip.png")
	if err := os.WriteFile(sourcePath, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatalf("write staged source: %v", err)
	}

	warning := m.attachStagedAttachments(context.Background(), "az-123", []string{sourcePath})
	if !strings.Contains(warning, "attachment note update(s) failed") {
		t.Fatalf("warning = %q, want note update failure", warning)
	}
	if !strings.Contains(warning, "append denied") {
		t.Fatalf("warning = %q, want append error detail", warning)
	}
}

func TestCtrlGDismissesLatestToastWithoutOverlay(t *testing.T) {
	m := newTestModel()
	expires := time.Now().Add(time.Hour)
	m.toasts = []Toast{
		{Message: "first", Expires: expires},
		{Message: "second", Expires: expires},
	}

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd != nil {
		t.Fatalf("expected no command when dismissing toast, got %T", cmd)
	}
	next := updated.(Model)
	if len(next.toasts) != 1 {
		t.Fatalf("toasts len = %d, want 1", len(next.toasts))
	}
	if got := next.toasts[0].Message; got != "first" {
		t.Fatalf("remaining toast = %q, want first", got)
	}
}

func TestCtrlGClosesOverlayBeforeDismissingToast(t *testing.T) {
	m := newTestModel()
	m.overlayStack.Push(overlay.NewHelpOverlay())
	m.toasts = []Toast{{Message: "background notice", Expires: time.Now().Add(time.Hour)}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd == nil {
		t.Fatal("expected ctrl+g to emit close-all command")
	}
	next := updated.(Model)
	if len(next.toasts) != 1 {
		t.Fatalf("toasts len = %d, want 1", len(next.toasts))
	}
	if _, ok := cmd().(overlay.CloseAllOverlaysMsg); !ok {
		t.Fatalf("ctrl+g command emitted %T, want overlay.CloseAllOverlaysMsg", cmd())
	}
}

func TestView_CanonicalProfiles(t *testing.T) {
	profiles := []testprofile.Profile{
		testprofile.Smoke,
		testprofile.Integration,
		testprofile.Scale,
	}

	for _, profile := range profiles {
		t.Run(profile.Name, func(t *testing.T) {
			m := newTestModel()
			m.tasks = append([]domain.Task(nil), profile.Tasks...)
			m.config.Git.BaseBranch = profile.BaseBranch
			m.width = profile.Width
			m.height = profile.Height
			m.loading = false
			m.editor.EnterNormal()

			phases := m.computePhases()
			switch profile.Name {
			case testprofile.Smoke.Name:
				phase, ok := phases["az-smoke-child"]
				if !ok || phase.Phase != 1 {
					t.Fatalf("expected smoke dependency chain to produce phase 1 child, got: %+v", phases)
				}
			case testprofile.Integration.Name:
				phase, ok := phases["az-int-child"]
				if !ok || phase.Phase != 1 {
					t.Fatalf("expected integration dependency graph to produce phase 1 child, got: %+v", phases)
				}
			case testprofile.Scale.Name:
				phase, ok := phases["az-scale-hierarchy"]
				if !ok || phase.Phase != 2 {
					t.Fatalf("expected scale dependency graph to produce deeper phase chain, got: %+v", phases)
				}
			}

			view := m.View()
			if view == "" {
				t.Fatalf("expected %s profile view to render", profile.Name)
			}
			if profile.Width < 80 {
				if !strings.Contains(view, "NORMAL") && !strings.Contains(view, "N F:") {
					t.Fatalf("expected compact status bar mode/filter badge to remain visible, got: %s", view)
				}
			} else if !strings.Contains(view, "NORMAL") {
				t.Fatalf("expected status bar mode badge to remain visible, got: %s", view)
			}
			if profile.Width < 80 && strings.Contains(view, "Space: task workspace") {
				t.Fatalf("expected compact status bar without full hints for %s profile, got: %s", profile.Name, view)
			}
			if profile.Width >= 80 && !strings.Contains(view, "?: help") {
				t.Fatalf("expected help-first board hints for %s profile, got: %s", profile.Name, view)
			}
		})
	}
}

func TestView_ShowsHiddenSelectionCount(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.editor.Select("az-1")
	m.editor.Select("az-2")
	m.editor.ToggleStatusFilter(domain.StatusDone)

	view := m.View()
	if !strings.Contains(view, "Selected: 2 (2 hidden)") {
		t.Fatalf("view = %q, want hidden selection count", view)
	}
	if !strings.Contains(view, "NORMAL") {
		t.Fatalf("view = %q, want normal mode badge", view)
	}
}

func TestNoLegacyRuntimeRefreshAuthorityPathsRemain(t *testing.T) {
	forbiddenTokens := []string{
		"runtimeSignalRefreshedAtByTask",
		"runtimeSignalsBusy",
		"lastRuntimeRefresh",
		"runtimeSignalsLoadedMsg",
		"shouldRefreshRuntimeSignals",
		"applyRuntimeSignals",
		"refreshRuntimeSignalsCmd",
		"prioritizeRuntimeSignalTasks",
		"shouldUseCachedRuntimeSignals",
		"Loading runtime status...",
	}
	files := []string{
		"model.go",
		"model_update_loop.go",
		"model_view_render.go",
		"model_daemonclient_test.go",
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(data)
		for _, token := range forbiddenTokens {
			if strings.Contains(source, token) {
				t.Fatalf("%s still contains legacy runtime-refresh token %q", file, token)
			}
		}
	}
}

func TestView_ShowsBoardRefreshingIndicators(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.boardRefreshing = true

	view := m.View()
	if strings.Contains(view, "REFRESHING BOARD - please wait") {
		t.Fatalf("view = %q, should not show full-width refresh banner", view)
	}
	if strings.Contains(view, "!!! REFRESHING BOARD !!!") {
		t.Fatalf("view = %q, should not show extra refresh status text", view)
	}
	if !strings.Contains(view, "NORMAL") {
		t.Fatalf("view = %q, want mode badge while refreshing", view)
	}
}

func TestView_ShowsFilterAndSortSummaries(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.editor.SetSearchQuery("az-1")
	m.editor.SetSortField(domain.SortByUpdated)
	m.editor.SetSortOrder(domain.SortDesc)

	view := m.View()
	if !strings.Contains(view, "F:q=az-1") {
		t.Fatalf("view = %q, want filter summary in status bar", view)
	}
	if !strings.Contains(view, "S:updated/desc") {
		t.Fatalf("view = %q, want sort summary in status bar", view)
	}
}

func TestIssuesLoadedMsg_IgnoresStaleRefreshSequence(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.issueRefreshSeq = 3

	next, _ := m.Update(issuesLoadedMsg{
		refreshSeq: 2,
		projectID:  m.daemonProjectID(),
		tasks: []domain.Task{
			{ID: "new-1", Title: "New stale payload", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		},
	})

	updated := next.(Model)
	if len(updated.tasks) != len(m.tasks) {
		t.Fatalf("tasks length = %d, want %d (stale payload ignored)", len(updated.tasks), len(m.tasks))
	}
	if updated.tasks[0].ID != m.tasks[0].ID {
		t.Fatalf("tasks[0] = %q, want %q (stale payload ignored)", updated.tasks[0].ID, m.tasks[0].ID)
	}
}

func TestIssuesLoadedMsg_AcceptsUnsequencedDaemonReattachSnapshot(t *testing.T) {
	m := newTestModel()
	m.loading = true
	m.issueRefreshSeq = 5

	next, _ := m.Update(issuesLoadedMsg{
		refreshSeq: 0, // attachDaemonCmd/rehydrate path
		projectID:  m.daemonProjectID(),
		tasks: []domain.Task{
			{ID: "rehydrated-1", Title: "Rehydrated", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		},
		revision: 42,
	})

	updated := next.(Model)
	if len(updated.tasks) != 1 || updated.tasks[0].ID != "rehydrated-1" {
		t.Fatalf("tasks = %+v, want unsequenced reattach snapshot applied", updated.tasks)
	}
	if updated.daemonRevision != 42 {
		t.Fatalf("daemonRevision = %d, want 42", updated.daemonRevision)
	}
}

func TestEscClearsFiltersInNormalMode(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.editor.ToggleStatusFilter(domain.StatusDone)
	m.editor.SetSearchQuery("az-2")
	m.sessionTreeFilterOnly = true
	m.editor.SetSortField(domain.SortByPriority)
	m.editor.SetSortOrder(domain.SortDesc)

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	next := result.(Model)
	if next.editor.IsFilterActive() {
		t.Fatal("expected esc in normal mode to clear active filters")
	}
	if next.sessionTreeFilterOnly {
		t.Fatal("expected esc in normal mode to clear session tree filter")
	}
	if got := next.editor.GetSort(); got.Field != domain.SortByPriority || got.Order != domain.SortDesc {
		t.Fatalf("expected esc filter clear to preserve sort state, got field=%s order=%v", got.Field, got.Order)
	}
}

func TestRuntimeSignalRefreshTasks_BoardUsesRenderedWindow(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.width = 80
	m.height = 9
	m.tasks = make([]domain.Task, 0, 12)
	for i := 1; i <= 12; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(fmt.Sprintf("az-%d", i)),
			Title:    fmt.Sprintf("Task %d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}
	m.nav.SelectTask("az-6", 0)
	m.viewportStarts[0] = 4

	columns := m.buildColumns()
	openColumn := columns[domain.StatusOpen.Column()].Tasks
	bodyHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))
	start, end := board.VisibleTaskWindow(len(openColumn), m.viewportStarts[0], bodyHeight, linesPerCard)

	got := m.runtimeSignalRefreshTasks()
	if len(got) != end-start {
		t.Fatalf("runtimeSignalRefreshTasks len = %d, want %d", len(got), end-start)
	}
	for i := start; i < end; i++ {
		if got[i-start].ID != openColumn[i].ID {
			t.Fatalf("runtimeSignalRefreshTasks[%d] = %q, want %q", i-start, got[i-start].ID, openColumn[i].ID)
		}
	}
	if start > 0 && got[0].ID == openColumn[0].ID {
		t.Fatalf("expected off-screen head task %q to be excluded, got first %q", openColumn[0].ID, got[0].ID)
	}
}

func TestRuntimeSignalRefreshTasks_CompactUsesVisibleRows(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.boardView = domain.TreeBoardView()
	m.width = 100
	m.height = 10
	m.tasks = make([]domain.Task, 0, 16)
	for i := 1; i <= 16; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(fmt.Sprintf("az-%02d", i)),
			Title:    fmt.Sprintf("Task %d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}
	m.nav.SelectTask("az-12", 0)

	filtered := m.editor.ApplySort(m.boardVisibleTasks(m.tasks))
	visibleRows := board.BoardContentHeight(m.height) - 2
	if visibleRows < 1 {
		visibleRows = 1
	}
	cursorIdx := 11
	scrollOffset := 0
	if cursorIdx >= visibleRows {
		scrollOffset = cursorIdx - visibleRows + 1
	}
	maxOffset := len(filtered) - visibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if scrollOffset > maxOffset {
		scrollOffset = maxOffset
	}
	end := scrollOffset + visibleRows
	if end > len(filtered) {
		end = len(filtered)
	}

	got := m.runtimeSignalRefreshTasks()
	want := filtered[scrollOffset:end]
	if len(got) != len(want) {
		t.Fatalf("runtimeSignalRefreshTasks len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("runtimeSignalRefreshTasks[%d] = %q, want %q", i, got[i].ID, want[i].ID)
		}
	}
}

func TestStartJumpMode_TreeOnlyLabelsVisibleRows(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.boardView = domain.TreeBoardView()
	m.width = 100
	m.height = 10
	m.tasks = make([]domain.Task, 0, 16)
	for i := 1; i <= 16; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(fmt.Sprintf("az-%02d", i)),
			Title:    fmt.Sprintf("Task %d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}
	// Park the cursor near the bottom so tree-layout scrolling pushes the
	// head of the list off-screen. In the configured Tree layout, gw must
	// only label rows that the tree renderer actually drew.
	m.nav.SelectTask("az-14", 0)

	rendered := m.runtimeSignalRefreshTasks()
	if len(rendered) == 0 {
		t.Fatalf("expected tree view to render some rows")
	}
	if len(rendered) >= len(m.tasks) {
		t.Fatalf("expected some rows to be off-screen, got %d rendered of %d total", len(rendered), len(m.tasks))
	}

	m.startJumpMode()
	if m.jumpMode == nil {
		t.Fatalf("expected jump mode to be active")
	}
	if len(m.jumpTargets) != len(rendered) {
		t.Fatalf("jumpTargets len = %d, want %d (only currently rendered rows)", len(m.jumpTargets), len(rendered))
	}
	wantIDs := make(map[string]struct{}, len(rendered))
	for _, task := range rendered {
		wantIDs[task.ID.String()] = struct{}{}
	}
	for _, gotID := range m.jumpTargets {
		if _, ok := wantIDs[gotID]; !ok {
			t.Fatalf("jumpTargets includes off-screen task %q", gotID)
		}
	}
}

func TestNormalModeNavigation(t *testing.T) {
	m := newTestModel()

	t.Run("vertical navigation - down", func(t *testing.T) {
		// Start at first task in Open column (az-1)
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 1 {
			t.Errorf("Expected task index 1, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-2" {
			t.Errorf("Expected cursor on az-2, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("vertical navigation - up", func(t *testing.T) {
		// Start at second task in Open column (az-2)
		m.nav.SelectTask("az-2", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 0 {
			t.Errorf("Expected task index 0, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-1" {
			t.Errorf("Expected cursor on az-1, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("vertical navigation - up at boundary", func(t *testing.T) {
		// Start at first task in Open column (az-1)
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 0 {
			t.Errorf("Expected task index to stay at 0, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-1" {
			t.Errorf("Expected cursor to stay on az-1, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("horizontal navigation - right", func(t *testing.T) {
		// Start at second task in Open column (az-2, index 1)
		m.nav.SelectTask("az-2", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 1 {
			t.Errorf("Expected column 1, got %d", pos.Column)
		}
		// InProgress column only has 1 task (az-3), so task index should be 0
		if pos.Task != 0 {
			t.Errorf("Expected task index to be clamped to 0, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-3" {
			t.Errorf("Expected cursor on az-3, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("horizontal navigation - left", func(t *testing.T) {
		// Start at task in InProgress column (az-3)
		m.nav.SelectTask("az-3", 1)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 0 {
			t.Errorf("Expected column 0, got %d", pos.Column)
		}
	})

	t.Run("horizontal navigation - left at boundary", func(t *testing.T) {
		// Start at first task in Open column (az-1)
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 0 {
			t.Errorf("Expected column to stay at 0, got %d", pos.Column)
		}
	})

	t.Run("horizontal navigation - right at boundary", func(t *testing.T) {
		// Start at task in Done column (az-5)
		m.nav.SelectTask("az-5", 3)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 3 {
			t.Errorf("Expected column to stay at 3, got %d", pos.Column)
		}
	})

	t.Run("navigation works while loading", func(t *testing.T) {
		m := newTestModel()
		m.loading = true
		m.nav.SelectTask("az-1", 0)

		result, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		newModel := result.(Model)

		if cmd != nil {
			t.Fatalf("expected no command while navigating during loading, got %T", cmd)
		}
		if !newModel.loading {
			t.Fatal("expected loading state to remain true before hydration completes")
		}
		if newModel.nav.GetCursor().TaskID != "az-2" {
			t.Fatalf("expected navigation to advance to az-2 while loading, got %s", newModel.nav.GetCursor().TaskID)
		}
	})
}

func TestNormalModeCOpensCreateOverlay(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.nav.SelectTask("az-1", 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	if _, ok := current.(*overlay.CreateTaskOverlay); !ok {
		t.Fatalf("expected CreateTaskOverlay on normal-mode c, got %T", current)
	}
}

func TestActionSelectionCOpensCreateOverlay(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.nav.SelectTask("az-1", 0)

	result, _ := m.handleSelection(overlay.SelectionMsg{Key: "c"})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	if _, ok := current.(*overlay.CreateTaskOverlay); !ok {
		t.Fatalf("expected CreateTaskOverlay from action selection c, got %T", current)
	}
}

func TestActionModeEOpensEditOverlay(t *testing.T) {
	m := newTestModel()
	m.editor.EnterAction()
	m.nav.SelectTask("az-1", 0)

	result, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	if current != nil {
		t.Fatalf("expected action-mode e to load full detail before opening edit overlay, got %T", current)
	}
	if cmd == nil {
		t.Fatal("expected action-mode e to start full-detail load")
	}
}

func TestTaskWorkspaceCreateChildReplacesWorkspaceWithForm(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	parent := domain.Task{ID: "az-1", Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask}
	m.tasks = []domain.Task{parent}
	m.nav.SelectTask(parent.ID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(parent, m.tasks, nil, 120, 30))

	result, _ := m.handleSelection(overlay.SelectionMsg{Key: "c"})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	if _, ok := current.(*overlay.CreateTaskOverlay); !ok {
		t.Fatalf("expected create overlay on top, got %T", current)
	}
	newModel.overlayStack.Pop()
	if current := newModel.overlayStack.Current(); current != nil {
		t.Fatalf("expected no stacked workspace underneath create overlay, got %T", current)
	}
}

func TestFollowOnMergeSelectionNoEligibleUpstreamShowsToast(t *testing.T) {
	m := newTestModel()
	parentID := "az-parent"
	childID := "az-child"
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{
			ID:          parentIssueID,
			Title:       "Parent",
			Status:      domain.StatusDone,
			Priority:    domain.P1,
			Type:        domain.TypeTask,
			HasWorktree: false,
		},
		{
			ID:          childIssueID,
			Title:       "Child",
			Status:      domain.StatusInProgress,
			Priority:    domain.P1,
			Type:        domain.TypeTask,
			ParentID:    &parentIssueID,
			HasWorktree: true,
		},
	}
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleFollowOnMergeCandidates(followOnMergeCandidatesMsg{
		target:            m.tasks[1],
		mergeTargetToBase: false,
		candidates:        nil,
	})
	if cmd != nil {
		t.Fatalf("expected no merge command when no eligible upstream exists, got %T", cmd)
	}

	newModel := updated.(Model)
	if len(newModel.toasts) == 0 {
		t.Fatalf("expected warning toast when follow-on merge has no eligible upstream")
	}
	lastToast := newModel.toasts[len(newModel.toasts)-1]
	if !strings.Contains(lastToast.Message, "No eligible upstream sources") {
		t.Fatalf("unexpected toast message: %q", lastToast.Message)
	}
}

func TestTaskWorkspaceMergeUsesWorkspaceTask(t *testing.T) {
	m := newTestModel()
	boardTask := domain.Task{
		ID:          "az-board",
		Title:       "Board selection",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
		HasWorktree: false,
	}
	workspaceTask := domain.Task{
		ID:          "az-workspace",
		Title:       "Workspace task",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
		HasWorktree: true,
		Session: &domain.Session{
			IssueID:  "az-workspace",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-workspace",
		},
	}
	m.tasks = []domain.Task{boardTask, workspaceTask}
	m.nav.SelectTask(boardTask.ID.String(), boardTask.Status.Column())
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(workspaceTask, m.tasks, nil, 120, 30))

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "m"})
	if cmd == nil {
		t.Fatal("expected workspace merge command")
	}

	newModel := updated.(Model)
	if _, ok := newModel.pendingOpsByTask[taskIDKey(workspaceTask.ID.String())]; !ok {
		t.Fatalf("expected merge preparation on workspace task %s, got %+v", workspaceTask.ID, newModel.pendingOpsByTask)
	}
	if _, ok := newModel.pendingOpsByTask[taskIDKey(boardTask.ID.String())]; ok {
		t.Fatalf("merge preparation used board-selected task %s, got %+v", boardTask.ID, newModel.pendingOpsByTask)
	}
}

func TestTaskWorkspacePreservesDetailsAcrossSummaryRefresh(t *testing.T) {
	m := newTestModel()
	task := domain.Task{
		ID:          "az-1",
		Title:       "Workspace task",
		Description: "Long description stays visible",
		Notes:       "Detailed notes stay visible",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
	}
	m.tasks = []domain.Task{task}
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(task, m.tasks, nil, 120, 30))

	result, _ := m.Update(issuesLoadedMsg{
		refreshSeq: 1,
		projectID:  m.daemonProjectID(),
		tasks: []domain.Task{
			{
				ID:       "az-1",
				Title:    "Workspace task after merge",
				Status:   domain.StatusInReview,
				Priority: domain.P1,
				Type:     domain.TypeTask,
			},
		},
		revision: 1,
	})
	updated := result.(Model)

	workspace, ok := updated.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("current overlay = %T, want TaskWorkspaceOverlay", updated.overlayStack.Current())
	}
	view := workspace.View()
	if !strings.Contains(view, "Workspace task after merge") {
		t.Fatalf("workspace did not pick up summary refresh title:\n%s", view)
	}
	if !strings.Contains(view, "Long description stays visible") || !strings.Contains(view, "Detailed notes stay visible") {
		t.Fatalf("workspace lost full details after summary refresh:\n%s", view)
	}
	if updated.tasks[0].Description != "" || updated.tasks[0].Notes != "" {
		t.Fatalf("model task details after summary refresh = %+v, want board summary without long details", updated.tasks[0])
	}
}

func TestTaskWorkspacePreservesFullGraphContextAcrossSummaryRefresh(t *testing.T) {
	m := newTestModel()
	currentID := naming.IssueID("az-current")
	relatedID := naming.IssueID("az-related")
	current := domain.Task{
		ID:          currentID,
		Title:       "Current task",
		Description: "Full description stays visible",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
		Dependencies: []domain.Dependency{
			{ID: relatedID, Type: domain.DependencyRelatedTo},
		},
	}
	related := domain.Task{
		ID:       relatedID,
		Title:    "Related off-board task",
		Status:   domain.StatusOpen,
		Priority: domain.P3,
		Type:     domain.TypeTask,
	}
	m.tasks = []domain.Task{current}
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(current, m.tasks, nil, 120, 30))

	fullResult, _ := m.Update(refreshTaskWorkspaceResultMsg{
		projectID: m.daemonProjectID(),
		revision:  1,
		taskID:    currentID.String(),
		hasTask:   true,
		task:      current,
		tasks:     []domain.Task{current, related},
	})
	withFullContext := fullResult.(Model)

	summary := current
	summary.Title = "Current task after summary refresh"
	summary.Description = ""
	result, _ := withFullContext.Update(issuesLoadedMsg{
		refreshSeq: 2,
		projectID:  m.daemonProjectID(),
		tasks:      []domain.Task{summary},
		revision:   2,
	})
	updated := result.(Model)

	workspace, ok := updated.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("current overlay = %T, want TaskWorkspaceOverlay", updated.overlayStack.Current())
	}
	view := workspace.View()
	if !strings.Contains(view, "Current task after summary refresh") || !strings.Contains(view, "Full description stays visible") {
		t.Fatalf("workspace lost refreshed summary or full details:\n%s", view)
	}
	if !strings.Contains(view, "Related off-board task") {
		t.Fatalf("workspace lost full graph context after summary refresh:\n%s", view)
	}

	authoritativeResult, _ := updated.Update(refreshTaskWorkspaceResultMsg{
		projectID: m.daemonProjectID(),
		revision:  3,
		taskID:    currentID.String(),
		hasTask:   true,
		task:      summary,
		tasks:     []domain.Task{summary},
	})
	authoritative := authoritativeResult.(Model)
	workspace = authoritative.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	view = workspace.View()
	if strings.Contains(view, "Related off-board task") || strings.Contains(view, "Full description stays visible") {
		t.Fatalf("workspace retained stale full context after authoritative refresh:\n%s", view)
	}
}

func TestTaskWorkspaceIgnoresStaleFullDetailRefreshGeneration(t *testing.T) {
	m := newTestModel()
	task := domain.Task{
		ID:          "az-1",
		Title:       "Current detail",
		Description: "Current description",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
	}
	m.tasks = []domain.Task{task}
	m.taskWorkspaceRefreshSeq = 2
	workspace := overlay.NewTaskWorkspaceOverlay(task, m.tasks, nil, 120, 30)
	workspace.SyncDecisionLinks([]overlay.DecisionLinkSummary{{DecisionID: "current", DecisionTitle: "Current decision", Relation: "implements"}})
	m.overlayStack.Push(workspace)

	stale := task
	stale.Title = "Stale detail"
	stale.Description = "Stale description"
	result, _ := m.Update(refreshTaskWorkspaceResultMsg{
		projectID:  m.daemonProjectID(),
		refreshSeq: 1,
		revision:   3,
		taskID:     task.ID.String(),
		hasTask:    true,
		task:       stale,
		tasks:      []domain.Task{stale},
	})
	updated := result.(Model)

	workspace = updated.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	view := workspace.View()
	if !strings.Contains(view, "Current detail") || !strings.Contains(view, "Current description") {
		t.Fatalf("stale refresh generation overwrote current workspace:\n%s", view)
	}
	if strings.Contains(view, "Stale detail") || strings.Contains(view, "Stale description") {
		t.Fatalf("stale refresh generation remained visible:\n%s", view)
	}

	result, _ = updated.Update(refreshTaskWorkspaceDecisionResultMsg{
		projectID:     m.daemonProjectID(),
		refreshSeq:    1,
		taskID:        task.ID.String(),
		decisionLinks: []overlay.DecisionLinkSummary{{DecisionID: "stale", DecisionTitle: "Stale decision", Relation: "implements"}},
	})
	updated = result.(Model)
	view = updated.overlayStack.Current().(*overlay.TaskWorkspaceOverlay).View()
	if !strings.Contains(view, "Current decision") || strings.Contains(view, "Stale decision") {
		t.Fatalf("stale decision generation overwrote current links:\n%s", view)
	}

	toastCount := len(updated.toasts)
	result, _ = updated.Update(refreshTaskWorkspaceReconcileResultMsg{
		projectID:  m.daemonProjectID(),
		refreshSeq: 1,
		taskID:     task.ID.String(),
		err:        errors.New("stale runtime failure"),
	})
	updated = result.(Model)
	if len(updated.toasts) != toastCount {
		t.Fatalf("stale runtime generation produced user feedback: %+v", updated.toasts)
	}
}

func TestTaskWorkspaceFullRefreshAllowsClearedDetails(t *testing.T) {
	m := newTestModel()
	task := domain.Task{
		ID:          "az-1",
		Title:       "Workspace task",
		Description: "Long description should clear",
		Design:      "Design should clear",
		Notes:       "Notes should clear",
		Acceptance:  "Acceptance should clear",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
	}
	m.tasks = []domain.Task{task}
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(task, m.tasks, nil, 120, 30))

	cleared := domain.Task{
		ID:       "az-1",
		Title:    "Workspace task after full refresh",
		Status:   domain.StatusInReview,
		Priority: domain.P1,
		Type:     domain.TypeTask,
	}
	result, _ := m.Update(refreshTaskWorkspaceResultMsg{
		projectID: m.daemonProjectID(),
		revision:  1,
		taskID:    "az-1",
		hasTask:   true,
		task:      cleared,
		tasks:     []domain.Task{cleared},
	})
	updated := result.(Model)

	workspace, ok := updated.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("current overlay = %T, want TaskWorkspaceOverlay", updated.overlayStack.Current())
	}
	view := workspace.View()
	if !strings.Contains(view, "Workspace task after full refresh") {
		t.Fatalf("workspace did not pick up full refresh title:\n%s", view)
	}
	for _, stale := range []string{
		"Long description should clear",
		"Design should clear",
		"Notes should clear",
		"Acceptance should clear",
	} {
		if strings.Contains(view, stale) {
			t.Fatalf("workspace retained cleared full-detail field %q:\n%s", stale, view)
		}
	}
	if updated.tasks[0].Description != "" || updated.tasks[0].Notes != "" ||
		updated.tasks[0].Design != "" || updated.tasks[0].Acceptance != "" {
		t.Fatalf("model task details after full refresh = %+v, want cleared details", updated.tasks[0])
	}
}

func TestSettingsSaveErrorKeepsOverlayOpen(t *testing.T) {
	m := newTestModel()
	m.overlayStack.Push(overlay.NewDefaultSettingsOverlay())

	updated, _ := m.handleSelection(overlay.SelectionMsg{
		Key:   "settings-save-error",
		Value: fmt.Errorf("boom"),
	})
	newModel := updated.(Model)

	if newModel.overlayStack.IsEmpty() {
		t.Fatal("expected settings overlay to remain open after save error")
	}

	if got := len(newModel.toasts); got == 0 {
		t.Fatal("expected a toast to be recorded for settings save error")
	}
}

func TestSettingsEditorOpensCurrentProjectConfigInTmuxPopup(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	t.Setenv("EDITOR", "vim")

	projectDir := t.TempDir()
	wantConfigPath := filepath.Join(projectDir, config.ConfigDirName, config.ConfigFileName)

	var gotTitle, gotWidth, gotHeight, gotCommand string
	m := newTestModel()
	m.repoDir = projectDir
	m.tmuxClient = mockTmuxService{
		popupFn: func(_ context.Context, title, width, height, command string) error {
			gotTitle = title
			gotWidth = width
			gotHeight = height
			gotCommand = command
			return nil
		},
	}
	m.overlayStack.Push(overlay.NewDefaultSettingsOverlay())

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "editor"})
	if cmd == nil {
		t.Fatal("expected editor popup command")
	}
	msg, ok := cmd().(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("command message = %T, want overlay.SelectionMsg", msg)
	}
	if msg.Key != "editor-closed" {
		t.Fatalf("message key = %q, want editor-closed", msg.Key)
	}

	newModel := updated.(Model)
	if newModel.overlayStack.IsEmpty() {
		t.Fatal("expected settings overlay to remain open until editor-closed is handled")
	}
	if gotTitle != "az.settings" || gotWidth != "90%" || gotHeight != "90%" {
		t.Fatalf("popup title/size = %q %q %q, want az.settings 90%% 90%%", gotTitle, gotWidth, gotHeight)
	}
	if !strings.Contains(gotCommand, "cd "+shellSingleQuote(projectDir)) {
		t.Fatalf("popup command = %q, want current project cd", gotCommand)
	}
	if !strings.Contains(gotCommand, shellSingleQuote(wantConfigPath)) {
		t.Fatalf("popup command = %q, want config path %q", gotCommand, wantConfigPath)
	}
	if _, err := os.Stat(filepath.Dir(wantConfigPath)); err != nil {
		t.Fatalf("expected config directory to exist: %v", err)
	}
}

func TestSettingsEditorOpensRequestedConfigPath(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	t.Setenv("EDITOR", "vim")

	projectDir := t.TempDir()
	wantConfigPath := filepath.Join(projectDir, config.ConfigDirName, config.LocalConfigFileName)

	var gotCommand string
	m := newTestModel()
	m.repoDir = projectDir
	m.tmuxClient = mockTmuxService{
		popupFn: func(_ context.Context, title, width, height, command string) error {
			gotCommand = command
			return nil
		},
	}

	_, cmd := m.handleSelection(overlay.SelectionMsg{Key: "editor", Value: wantConfigPath})
	if cmd == nil {
		t.Fatal("expected editor popup command")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("expected editor completion message")
	}
	if !strings.Contains(gotCommand, shellSingleQuote(wantConfigPath)) {
		t.Fatalf("popup command = %q, want config path %q", gotCommand, wantConfigPath)
	}
}

func TestSettingsEditorRequiresTmuxPopup(t *testing.T) {
	t.Setenv("TMUX", "")

	m := newTestModel()
	m.repoDir = t.TempDir()
	m.tmuxClient = mockTmuxService{}

	_, cmd := m.handleSelection(overlay.SelectionMsg{Key: "editor"})
	if cmd == nil {
		t.Fatal("expected editor command")
	}
	msg, ok := cmd().(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("command message = %T, want overlay.SelectionMsg", msg)
	}
	if msg.Key != "editor-error" {
		t.Fatalf("message key = %q, want editor-error", msg.Key)
	}
}

func TestHalfPageScroll(t *testing.T) {
	m := newTestModel()

	// Add more tasks to Open column for scrolling
	for i := 0; i < 10; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(string(rune('a' + i))),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	t.Run("ctrl+d scrolls down", func(t *testing.T) {
		// Start at first task in Open column
		m.nav.SelectTask("az-1", 0)
		m.height = 24
		initialPos := getCursorPosition(m)
		initialTaskID := m.nav.GetCursor().TaskID

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlD})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task <= initialPos.Task {
			t.Errorf("Expected task index to increase, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+d, still on %s", initialTaskID)
		}
	})

	t.Run("ctrl+u scrolls up", func(t *testing.T) {
		// Start at task 'e' (index 5) in Open column
		m.nav.SelectTask("e", 0)
		m.height = 24
		initialPos := getCursorPosition(m)
		initialTaskID := m.nav.GetCursor().TaskID

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task >= initialPos.Task {
			t.Errorf("Expected task index to decrease, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+u, still on %s", initialTaskID)
		}
	})

	t.Run("ctrl+u at top stays at 0", func(t *testing.T) {
		// Start at first task
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task != 0 {
			t.Errorf("Expected task index to stay at 0, got %d", newPos.Task)
		}
	})

	t.Run("sustained down input stays responsive on large lists", func(t *testing.T) {
		m := newTestModel()
		for i := 0; i < 50; i++ {
			m.tasks = append(m.tasks, domain.Task{
				ID:       naming.IssueID(string(rune('a'+i%26)) + string(rune('A'+i/26))),
				Title:    "Extra Task",
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}
		m.height = 24
		m.nav.SelectTask("az-1", 0)

		seen := map[string]struct{}{"az-1": {}}
		for i := 0; i < 20; i++ {
			result, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyDown})
			if cmd != nil {
				t.Fatalf("expected no command during sustained scroll input, got %T", cmd)
			}
			nextModel, ok := result.(Model)
			if !ok {
				t.Fatalf("updated model type = %T, want Model", result)
			}
			m = nextModel
			got := m.nav.GetCursor().TaskID
			if got == "" {
				t.Fatal("expected cursor to remain on a task while scrolling")
			}
			seen[got] = struct{}{}
		}

		if len(seen) < 5 {
			t.Fatalf("expected cursor to advance across multiple tasks, saw %d distinct ids", len(seen))
		}
		if view := m.View(); view == "" {
			t.Fatal("expected view to remain renderable during sustained scroll input")
		}
	})
}

func TestSelectModeHalfPageNavigation(t *testing.T) {
	m := newTestModel()

	for i := 0; i < 10; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(string(rune('a' + i))),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	m.height = 24
	m.editor.EnterSelect()

	t.Run("ctrl+d moves cursor without selecting in select mode", func(t *testing.T) {
		m.nav.SelectTask("az-1", 0)
		initialTaskID := m.nav.GetCursor().TaskID
		initialPos := getCursorPosition(m)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyCtrlD})
		newModel := result.(Model)
		newPos := getCursorPosition(newModel)

		if newPos.Task <= initialPos.Task {
			t.Errorf("Expected task index to increase, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+d in select mode, still on %s", initialTaskID)
		}
		if newModel.editor.SelectionCount() != 0 {
			t.Fatalf("selection count = %d, want 0", newModel.editor.SelectionCount())
		}
	})

	t.Run("ctrl+u moves cursor without selecting in select mode", func(t *testing.T) {
		m.nav.SelectTask("e", 0)
		initialTaskID := m.nav.GetCursor().TaskID
		initialPos := getCursorPosition(m)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)
		newPos := getCursorPosition(newModel)

		if newPos.Task >= initialPos.Task {
			t.Errorf("Expected task index to decrease, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+u in select mode, still on %s", initialTaskID)
		}
		if newModel.editor.SelectionCount() != 0 {
			t.Fatalf("selection count = %d, want 0", newModel.editor.SelectionCount())
		}
	})
}

func TestSelectModeSelectionAndBulkEntry(t *testing.T) {
	t.Run("a toggles the current task selection", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.nav.SelectTask("az-1", 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		newModel := result.(Model)

		if !newModel.editor.IsSelected("az-1") {
			t.Fatal("expected a to select the current task")
		}
	})

	t.Run("5 toggles the current task selection", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		m.nav.SelectTask("az-1", 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
		newModel := result.(Model)

		if newModel.editor.IsSelected("az-1") {
			t.Fatal("expected 5 to deselect the current task")
		}
	})

	t.Run("space opens bulk actions when selection exists", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		m.nav.SelectTask("az-1", 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
		newModel := result.(Model)

		if _, ok := newModel.overlayStack.Current().(*overlay.BulkActionMenu); !ok {
			t.Fatalf("expected bulk action menu, got %T", newModel.overlayStack.Current())
		}
	})

	t.Run("enter drills into current parent instead of opening bulk actions", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		parentID := naming.IssueID("az-parent")
		childID := naming.IssueID("az-child")
		m.tasks = append(m.tasks,
			domain.Task{
				ID:       parentID,
				Title:    "Parent task",
				Status:   domain.StatusOpen,
				Priority: domain.P1,
				Type:     domain.TypeEpic,
			},
			domain.Task{
				ID:       childID,
				Title:    "Child task",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
				ParentID: &parentID,
			},
		)
		m.nav.SelectTask(parentID.String(), 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyEnter})
		newModel := result.(Model)

		if current := newModel.overlayStack.Current(); current != nil {
			t.Fatalf("expected no bulk action overlay after Enter drill-down, got %T", current)
		}
		if got := newModel.drillDownParentID; got != parentID.String() {
			t.Fatalf("drillDownParentID = %q, want %q", got, parentID)
		}
		columns := newModel.buildColumns()
		pos := newModel.nav.GetPosition(columns)
		if !pos.Valid {
			t.Fatalf("expected valid drill-down cursor position, got %+v", pos)
		}
		if got := columns[pos.Column].Tasks[pos.Task].ID.String(); got != childID.String() {
			t.Fatalf("selected task = %q, want child %q", got, childID)
		}
	})

	t.Run("move down in select mode keeps current task selected", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		m.nav.SelectTask("az-1", 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		newModel := result.(Model)

		if !newModel.editor.IsSelected("az-1") {
			t.Fatal("expected az-1 to remain selected after moving down in select mode")
		}
		if got := newModel.nav.GetCursor().TaskID; got != "az-2" {
			t.Fatalf("cursor task = %q, want az-2", got)
		}
	})

	t.Run("move down in select mode does not select an unselected task", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.nav.SelectTask("az-1", 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		newModel := result.(Model)

		if newModel.editor.SelectionCount() != 0 {
			t.Fatalf("selection count = %d, want 0", newModel.editor.SelectionCount())
		}
		if got := newModel.nav.GetCursor().TaskID; got != "az-2" {
			t.Fatalf("cursor task = %q, want az-2", got)
		}
	})

	t.Run("a then move then a builds multi-select", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.nav.SelectTask("az-1", 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		m = result.(Model)
		result, _ = m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = result.(Model)
		result, _ = m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		newModel := result.(Model)

		if !newModel.editor.IsSelected("az-1") || !newModel.editor.IsSelected("az-2") {
			t.Fatalf("expected az-1 and az-2 selected, got %+v", newModel.editor.GetSelectedTasksList())
		}
		if got := newModel.editor.SelectionCount(); got != 2 {
			t.Fatalf("selection count = %d, want 2", got)
		}
	})
}

func TestSearchOverlayLiveFilteringAndModeTransitions(t *testing.T) {
	m := newTestModel()

	updated, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if cmd == nil {
		t.Fatal("expected search overlay push command")
	}

	searchModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if !searchModel.editor.IsSearch() {
		t.Fatalf("expected search mode after '/'")
	}

	searchOverlay, ok := searchModel.overlayStack.Current().(*overlay.SearchOverlay)
	if !ok {
		t.Fatalf("expected SearchOverlay on stack, got %T", searchModel.overlayStack.Current())
	}

	for _, r := range []rune{'a', 'z', '-', '1'} {
		_, _ = searchOverlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	updated, _ = searchModel.Update(overlay.SearchMsg{Query: "az-1"})
	searchModel, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if got := searchModel.editor.GetFilter().SearchQuery; got != "az-1" {
		t.Fatalf("search query = %q, want az-1", got)
	}

	searchOverlay, ok = searchModel.overlayStack.Current().(*overlay.SearchOverlay)
	if !ok {
		t.Fatalf("expected SearchOverlay on stack, got %T", searchModel.overlayStack.Current())
	}
	if got := searchOverlay.View(); !strings.Contains(got, "1 matches") {
		t.Fatalf("expected live match count in search overlay view, got %q", got)
	}

	updated, _ = searchModel.Update(overlay.CloseOverlayMsg{})
	searchModel, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if !searchModel.editor.IsNormal() {
		t.Fatalf("expected normal mode after closing search overlay")
	}
	if !searchModel.overlayStack.IsEmpty() {
		t.Fatal("expected search overlay stack to be empty after close")
	}
}

func TestSearchModeEscClearsQuery(t *testing.T) {
	m := newTestModel()
	m.editor.EnterSearch()
	m.editor.SetSearchQuery("az-1")

	updated, _ := m.handleSearchMode(tea.KeyMsg{Type: tea.KeyEsc})
	searchModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if !searchModel.editor.IsNormal() {
		t.Fatalf("expected normal mode after Esc in search mode")
	}
	if got := searchModel.editor.GetFilter().SearchQuery; got != "" {
		t.Fatalf("search query = %q, want empty", got)
	}
}

func TestEventLogHotkeyPushesOverlay(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.addToast(Toast{
		Level:   ToastInfo,
		Message: "event seed",
		Expires: time.Now().Add(5 * time.Second),
	})

	updated, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})

	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	current := next.overlayStack.Current()
	logOverlay, ok := current.(*overlay.EventLogOverlay)
	if !ok {
		t.Fatalf("expected EventLogOverlay on stack, got %T", current)
	}

	model, _ := logOverlay.Update(tea.WindowSizeMsg{Width: 160, Height: 34})
	logOverlay = model.(*overlay.EventLogOverlay)
	if view := logOverlay.View(); !strings.Contains(view, "Toast") || !strings.Contains(view, "event seed") {
		t.Fatalf("expected event log to render runtime events, got %q", view)
	}
}

func TestRuntimeEventSummary_HumanizesToastAndTaskEvents(t *testing.T) {
	toast := runtimeEventSummary(protocol.EventEnvelope{
		Event: "ui.toast",
		Body:  []byte("Saved settings"),
	})
	if toast != "Saved settings" {
		t.Fatalf("toast summary = %q, want %q", toast, "Saved settings")
	}
	if strings.Contains(toast, "ui.toast") {
		t.Fatalf("toast summary should not expose raw event key: %q", toast)
	}

	task := runtimeEventSummary(protocol.EventEnvelope{
		Event: "task.updated",
		Body:  []byte("az-42"),
	})
	if task != "Task updated: az-42" {
		t.Fatalf("task summary = %q, want %q", task, "Task updated: az-42")
	}
}

func TestNormalModeUpFromBottom_DoesNotTopSnapViewport(t *testing.T) {
	m := newTestModel()

	for i := 0; i < 10; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(string(rune('a' + i))),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	m.height = 24
	m.viewportStarts[0] = 8

	columns := m.buildColumns()
	availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))
	initialStart, initialEnd := board.VisibleTaskWindow(len(columns[0].Tasks), m.viewportStarts[0], availableHeight, linesPerCard)
	if initialEnd-initialStart < 2 {
		t.Fatalf("expected at least two visible tasks in initial window, got [%d,%d)", initialStart, initialEnd)
	}

	lastVisible := initialEnd - 1
	m.nav.SelectTask(columns[0].Tasks[lastVisible].ID.String(), 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyUp})
	newModel := result.(Model)

	expectedTask := columns[0].Tasks[lastVisible-1].ID
	if got := newModel.nav.GetCursor().TaskID; got != expectedTask.String() {
		t.Fatalf("expected cursor to move up one task to %s, got %s", expectedTask, got)
	}
	if newModel.viewportStarts[0] != initialStart {
		t.Fatalf("expected viewport start to remain %d after first up from bottom, got %d", initialStart, newModel.viewportStarts[0])
	}
}

func TestNormalModeDown_KeepsCursorVisibleWithIndicators(t *testing.T) {
	m := newTestModel()

	for i := 0; i < 30; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(fmt.Sprintf("open-%02d", i)),
			Title:    fmt.Sprintf("Open Task %02d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}

	m.height = 24
	m.width = 80

	columns := m.buildColumns()
	if len(columns) == 0 || len(columns[0].Tasks) < 4 {
		t.Fatalf("expected enough open-column tasks for viewport test")
	}

	availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))

	start := len(columns[0].Tasks) / 2
	windowStart, windowEnd := board.VisibleTaskWindow(len(columns[0].Tasks), start, availableHeight, linesPerCard)
	if windowEnd-windowStart < 2 {
		t.Fatalf("expected at least two visible tasks in test window, got [%d,%d)", windowStart, windowEnd)
	}

	m.viewportStarts[0] = start
	m.nav.SelectTask(columns[0].Tasks[windowEnd-1].ID.String(), 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyDown})
	next := result.(Model)

	nextColumns := next.buildColumns()
	pos := next.nav.GetPosition(nextColumns)
	if !pos.Valid || pos.Column != 0 {
		t.Fatalf("expected valid cursor in open column after moving down; got %+v", pos)
	}

	nextStart, nextEnd := board.VisibleTaskWindow(len(nextColumns[0].Tasks), next.viewportStarts[0], availableHeight, linesPerCard)
	if pos.Task < nextStart || pos.Task >= nextEnd {
		t.Fatalf(
			"cursor task index %d not visible in indicator-aware window [%d,%d) with viewport start %d",
			pos.Task,
			nextStart,
			nextEnd,
			next.viewportStarts[0],
		)
	}
}

func TestNormalModeDown_NarrowShortSingleColumnKeepsFinalIssueVisible(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.width = 50
	m.tasks = make([]domain.Task, 0, 12)
	for i := 1; i <= 12; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(fmt.Sprintf("open-%02d", i)),
			Title:    fmt.Sprintf("Open Task %02d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}

	columns := m.buildColumns()
	columnCount := m.boardColumnLayout(columns).VisibleCount
	if columnCount != 1 {
		t.Fatalf("expected single-column board at width %d, got %d columns", m.width, columnCount)
	}
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(m.width/columnCount))
	m.height = linesPerCard + board.ColumnHeaderLines + board.BoardStatusBarLines
	m.nav.SelectTask("open-01", 0)
	m.ensureCursorVisible(columns)

	for i := 1; i < len(columns[0].Tasks); i++ {
		result, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyDown})
		if cmd != nil {
			t.Fatalf("expected no command during down navigation, got %T", cmd)
		}
		next, ok := result.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", result)
		}
		m = next
	}

	columns = m.buildColumns()
	pos := m.nav.GetPosition(columns)
	if !pos.Valid || pos.Column != 0 || pos.Task != len(columns[0].Tasks)-1 {
		t.Fatalf("expected cursor on final open issue, got %+v", pos)
	}

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "▶open-12") || !strings.Contains(view, "Open Task 12") {
		t.Fatalf("expected final issue to remain visible in narrow short board view:\n%s", view)
	}
}

func TestHorizontalColumnViewportFollowsCursorOnNarrowWidth(t *testing.T) {
	m := newTestModel()
	m.width = 80

	columns := m.buildColumns()
	m.nav.SelectTask("az-1", 0)
	m.ensureCursorVisible(columns)
	if m.columnViewportStart != 0 {
		t.Fatalf("expected initial column viewport start 0, got %d", m.columnViewportStart)
	}

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	m1 := result.(Model)
	if got := getCursorPosition(m1).Column; got != 1 {
		t.Fatalf("expected cursor column 1 after first right, got %d", got)
	}
	if m1.columnViewportStart != 0 {
		t.Fatalf("expected viewport to stay at 0 while cursor remains visible, got %d", m1.columnViewportStart)
	}

	result, _ = m1.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	m2 := result.(Model)
	if got := getCursorPosition(m2).Column; got != 2 {
		t.Fatalf("expected cursor column 2 after second right, got %d", got)
	}
	if m2.columnViewportStart != 1 {
		t.Fatalf("expected viewport to advance to 1 at right edge, got %d", m2.columnViewportStart)
	}

	result, _ = m2.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	m3 := result.(Model)
	if got := getCursorPosition(m3).Column; got != 3 {
		t.Fatalf("expected cursor column 3 after third right, got %d", got)
	}
	if m3.columnViewportStart != 2 {
		t.Fatalf("expected viewport to advance to 2 at right edge, got %d", m3.columnViewportStart)
	}

	result, _ = m3.handleNormalMode(tea.KeyMsg{Type: tea.KeyLeft})
	m4 := result.(Model)
	if got := getCursorPosition(m4).Column; got != 2 {
		t.Fatalf("expected cursor column 2 after first left, got %d", got)
	}
	if m4.columnViewportStart != 2 {
		t.Fatalf("expected viewport to stay at 2 while cursor remains visible, got %d", m4.columnViewportStart)
	}

	result, _ = m4.handleNormalMode(tea.KeyMsg{Type: tea.KeyLeft})
	m5 := result.(Model)
	if got := getCursorPosition(m5).Column; got != 1 {
		t.Fatalf("expected cursor column 1 after second left, got %d", got)
	}
	if m5.columnViewportStart != 1 {
		t.Fatalf("expected viewport to move back to 1 when crossing left edge, got %d", m5.columnViewportStart)
	}
}

func TestHorizontalColumnViewportFollowsCursorOnMediumWidth(t *testing.T) {
	m := newTestModel()
	m.width = 120

	columns := m.buildColumns()
	if got := m.boardColumnLayout(columns).VisibleCount; got != 3 {
		t.Fatalf("visible columns at medium width = %d, want 3", got)
	}

	m.nav.SelectTask("az-1", 0)
	m.ensureCursorVisible(columns)
	m.nav.SelectTask("az-5", 3)
	m.ensureCursorVisible(columns)

	if m.columnViewportStart != 1 {
		t.Fatalf("viewport start after selecting fourth column = %d, want 1", m.columnViewportStart)
	}
	start, end := m.boardColumnLayout(columns).Range()
	if start != 1 || end != 4 {
		t.Fatalf("visible range after selecting fourth column = [%d,%d), want [1,4)", start, end)
	}
}

func TestRenderBoardView_NarrowWidthShowsVisibleColumnWindow(t *testing.T) {
	m := newTestModel()
	m.width = 80
	m.height = 24
	m.loading = false

	m.nav.SelectTask("az-1", 0)
	m.ensureCursorVisible(m.buildColumns())
	view := m.renderBoardView()
	if !strings.Contains(view, "Open (2)") {
		t.Fatalf("expected open column header in narrow view")
	}
	if !strings.Contains(view, "In Progress (1)") {
		t.Fatalf("expected in progress column header in narrow view")
	}
	if strings.Contains(view, "In Review (1)") {
		t.Fatalf("expected in-review column to be out of view when window is on first two columns")
	}

	m.nav.SelectTask("az-4", 2)
	m.ensureCursorVisible(m.buildColumns())
	view = m.renderBoardView()
	if !strings.Contains(view, "In Progress (1)") || !strings.Contains(view, "In Review (1)") {
		t.Fatalf("expected in progress and in-review columns in shifted narrow view")
	}
	if strings.Contains(view, "Open (2)") {
		t.Fatalf("expected open column to be out of view after horizontal shift")
	}
}

func TestRenderBoardView_ReallyNarrowWidthUsesSingleColumn(t *testing.T) {
	m := newTestModel()
	m.width = 50
	m.height = 24
	m.loading = false

	m.nav.SelectTask("az-1", 0)
	m.ensureCursorVisible(m.buildColumns())
	view := m.renderBoardView()
	if !strings.Contains(view, "Open (2)") {
		t.Fatalf("expected open column header in single-column view")
	}
	if strings.Contains(view, "In Progress (1)") {
		t.Fatalf("expected in progress column to be out of view in single-column mode")
	}

	m.nav.SelectTask("az-3", 1)
	m.ensureCursorVisible(m.buildColumns())
	view = m.renderBoardView()
	if !strings.Contains(view, "In Progress (1)") {
		t.Fatalf("expected in progress column header after horizontal shift")
	}
	if strings.Contains(view, "Open (2)") {
		t.Fatalf("expected open column to be out of view after shifting to in progress")
	}
}

func TestGotoMode(t *testing.T) {
	m := newTestModel()

	// Add more tasks to Open column
	for i := 0; i < 5; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       naming.IssueID(string(rune('a' + i))),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	t.Run("g enters goto mode", func(t *testing.T) {
		m.editor.EnterNormal()
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		newModel := result.(Model)

		if newModel.editor.GetMode() != ModeGoto {
			t.Errorf("Expected ModeGoto, got %v", newModel.editor.GetMode())
		}
	})

	t.Run("gg goes to top", func(t *testing.T) {
		// Start at task 'e' (index 5) in Open column
		m.nav.SelectTask("e", 0)
		m.editor.EnterGoto()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 0 {
			t.Errorf("Expected task index 0, got %d", pos.Task)
		}
		if !newModel.editor.IsNormal() {
			t.Errorf("Expected to return to ModeNormal, got %v", newModel.editor.GetMode())
		}
	})

	t.Run("ge goes to end", func(t *testing.T) {
		// Start at first task in Open column
		m.nav.SelectTask("az-1", 0)
		m.editor.EnterGoto()
		col := m.currentColumn()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != len(col)-1 {
			t.Errorf("Expected task index %d, got %d", len(col)-1, pos.Task)
		}
	})

	t.Run("gh goes to first column", func(t *testing.T) {
		// Start at task in In Review column
		m.nav.SelectTask("az-4", 2)
		m.editor.EnterGoto()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 0 {
			t.Errorf("Expected column 0, got %d", pos.Column)
		}
	})

	t.Run("gl goes to last column", func(t *testing.T) {
		// Start at first task in Open column
		m.nav.SelectTask("az-1", 0)
		m.editor.EnterGoto()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 3 {
			t.Errorf("Expected column 3, got %d", pos.Column)
		}
	})

	t.Run("g boundary keys jump to expected positions", func(t *testing.T) {
		boundaryModel := newTestModel()
		for i := 0; i < 5; i++ {
			boundaryModel.tasks = append(boundaryModel.tasks, domain.Task{
				ID:       naming.IssueID(fmt.Sprintf("boundary-%d", i)),
				Title:    "Boundary Task",
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}

		boundaryModel.nav.SelectTask("az-1", 0)
		boundaryModel.editor.EnterGoto()
		result, _ := boundaryModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		next := result.(Model)
		pos := getCursorPosition(next)
		if pos.Column != 0 || pos.Task != len(boundaryModel.currentColumn())-1 {
			t.Fatalf("g e position = (%d,%d), want bottom of current column", pos.Column, pos.Task)
		}

		boundaryModel.nav.SelectTask("az-4", 2)
		boundaryModel.editor.EnterGoto()
		result, _ = boundaryModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		next = result.(Model)
		pos = getCursorPosition(next)
		if pos.Column != 0 {
			t.Fatalf("g h column = %d, want 0", pos.Column)
		}

		boundaryModel.nav.SelectTask("az-1", 0)
		boundaryModel.editor.EnterGoto()
		result, _ = boundaryModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		next = result.(Model)
		pos = getCursorPosition(next)
		if pos.Column != 3 {
			t.Fatalf("g l column = %d, want 3", pos.Column)
		}
	})

	t.Run("gw renders jump labels in place and selects a double-char target", func(t *testing.T) {
		jumpModel := newTestModel()
		jumpModel.width = 120
		jumpModel.height = 120
		jumpModel.loading = false
		jumpModel.config.Keyboard.JumpLabelChars = "abc"
		jumpModel.editor.EnterGoto()
		jumpModel.nav.SelectTask("az-1", 0)

		for i := 0; i < 5; i++ {
			jumpModel.tasks = append(jumpModel.tasks, domain.Task{
				ID:       naming.IssueID(fmt.Sprintf("jump-%02d", i)),
				Title:    fmt.Sprintf("Jump Task %02d", i),
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}

		result, _ := jumpModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
		newModel := result.(Model)

		if !newModel.overlayStack.IsEmpty() {
			t.Fatalf("expected jump labels without centered overlay, got %T", newModel.overlayStack.Current())
		}
		jump := newModel.jumpMode
		if jump == nil {
			t.Fatal("expected jump mode to be active")
		}
		if got, want := jump.GetLabel(0), "aa"; got != want {
			t.Fatalf("label 0 = %q, want %q", got, want)
		}
		if got, want := jump.GetLabel(3), "ba"; got != want {
			t.Fatalf("label 3 = %q, want %q", got, want)
		}
		view := ansi.Strip(newModel.View())
		if !strings.Contains(view, "aa") || !strings.Contains(view, "ba") {
			t.Fatalf("view missing in-place jump labels:\n%s", view)
		}

		seen := make(map[string]int, 7)
		for i := 0; i < 7; i++ {
			label := jump.GetLabel(i)
			if label == "" {
				t.Fatalf("label %d is empty", i)
			}
			if prev, ok := seen[label]; ok {
				t.Fatalf("label %q reused for indexes %d and %d", label, prev, i)
			}
			seen[label] = i
		}
		if got := len(seen); got != 7 {
			t.Fatalf("unique label count = %d, want 7", got)
		}

		result, cmd := newModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
		newModel = result.(Model)
		if cmd != nil {
			t.Fatal("expected first jump key to wait for a two-char label")
		}
		result, cmd = newModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		newModel = result.(Model)
		if cmd == nil {
			t.Fatal("expected second jump key to emit a selection command")
		}
		msg := cmd()
		selected, ok := msg.(overlay.JumpSelectedMsg)
		if !ok {
			t.Fatalf("jump command emitted %T, want overlay.JumpSelectedMsg", msg)
		}
		if got, want := selected.TaskIndex, 3; got != want {
			t.Fatalf("selected task index = %d, want %d", got, want)
		}

		result, _ = newModel.Update(msg)
		finalModel := result.(Model)
		pos := getCursorPosition(finalModel)
		if pos.Column != 0 || pos.Task != 3 {
			t.Fatalf("expected cursor on jump target at (0,3), got (%d,%d)", pos.Column, pos.Task)
		}
		if finalModel.jumpMode != nil {
			t.Fatal("expected jump mode to clear after selection")
		}
	})

	t.Run("gs opens spec workspace", func(t *testing.T) {
		m.editor.EnterGoto()
		m.nav.SelectTask("az-2", 0)
		before := getCursorPosition(m)

		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		newModel := result.(Model)

		if !newModel.editor.IsNormal() {
			t.Fatalf("expected goto mode to return to normal after opening spec workspace, got %v", newModel.editor.GetMode())
		}
		current := newModel.overlayStack.Current()
		if current == nil {
			t.Fatal("expected spec workspace overlay to be pushed")
		}
		if got := current.Title(); got != "Spec Workspace" {
			t.Fatalf("overlay title = %q, want Spec Workspace", got)
		}

		updated, _ := newModel.handleOverlayKey(tea.KeyMsg{Type: tea.KeyTab})
		cycled := updated.(Model)
		if got := cycled.overlayStack.Current().View(); got == "" {
			t.Fatal("expected spec workspace overlay to continue rendering after tab cycle")
		}

		_, closeCmd := cycled.handleOverlayKey(tea.KeyMsg{Type: tea.KeyEsc})
		if closeCmd == nil {
			t.Fatal("expected escape to emit a close command")
		}
		closeMsg := closeCmd()
		if _, ok := closeMsg.(overlay.CloseOverlayMsg); !ok {
			t.Fatalf("escape command emitted %T, want CloseOverlayMsg", closeMsg)
		}
		cycled.overlayStack.Update(closeMsg)
		finalModel := cycled
		if !finalModel.overlayStack.IsEmpty() {
			t.Fatal("expected spec workspace overlay to close on escape")
		}
		if finalPos := getCursorPosition(finalModel); finalPos != before {
			t.Fatalf("cursor position changed across spec workspace flow: before=%+v after=%+v", before, finalPos)
		}
	})
}

func TestSelectModeEntry(t *testing.T) {
	m := newTestModel()

	t.Run("v enters select mode from normal", func(t *testing.T) {
		m.editor.EnterNormal()
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		newModel := result.(Model)

		if newModel.editor.GetMode() != ModeSelect {
			t.Errorf("Expected ModeSelect, got %v", newModel.editor.GetMode())
		}
	})

	t.Run("escape exits select mode back to normal", func(t *testing.T) {
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		newModel := result.(Model)

		if !newModel.editor.IsNormal() {
			t.Errorf("Expected ModeNormal after escape from select, got %v", newModel.editor.GetMode())
		}
		if newModel.editor.HasSelection() {
			t.Fatal("expected selection to be cleared after escape from select")
		}
	})

	t.Run("v exits select mode and clears selection", func(t *testing.T) {
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		newModel := result.(Model)

		if !newModel.editor.IsNormal() {
			t.Errorf("Expected ModeNormal after v from select, got %v", newModel.editor.GetMode())
		}
		if newModel.editor.HasSelection() {
			t.Fatal("expected selection to be cleared after v from select")
		}
	})
}

func TestBulkActionMsgRoutesThroughUpdate(t *testing.T) {
	m := newTestModel()
	m.editor.EnterSelect()
	m.editor.Select("az-1")
	m.nav.SelectTask("az-1", 0)
	opened, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = opened.(Model)

	if _, ok := m.overlayStack.Current().(*overlay.BulkActionMenu); !ok {
		t.Fatalf("expected bulk action menu, got %T", m.overlayStack.Current())
	}

	updated, _ := m.Update(overlay.BulkActionMsg{
		Action:      "x",
		SelectedIDs: []string{"az-1"},
	})
	m = updated.(Model)

	if m.overlayStack.Current() != nil {
		t.Fatalf("expected bulk action overlay to close, got %T", m.overlayStack.Current())
	}
	if !m.editor.IsNormal() {
		t.Fatalf("expected normal mode after clear selection action, got %v", m.editor.GetMode())
	}
	if m.editor.HasSelection() {
		t.Fatal("expected selection to be cleared after bulk clear action")
	}
}

func TestCreateTaskOverlayPersistsAcrossCloseReopen(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()

	opened, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = opened.(Model)

	first, ok := m.overlayStack.Current().(*overlay.CreateTaskOverlay)
	if !ok || first == nil {
		t.Fatalf("expected create overlay, got %T", m.overlayStack.Current())
	}
	if first.ParentID() != nil {
		t.Fatalf("expected no parent outside drill-down, got %v", *first.ParentID())
	}

	closed, _ := m.Update(overlay.CloseOverlayMsg{})
	m = closed.(Model)
	if !m.overlayStack.IsEmpty() {
		t.Fatal("expected overlay stack to be empty after close")
	}
	m.drillDownParentID = "az-parent"

	reopened, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = reopened.(Model)

	second, ok := m.overlayStack.Current().(*overlay.CreateTaskOverlay)
	if !ok || second == nil {
		t.Fatalf("expected create overlay after reopen, got %T", m.overlayStack.Current())
	}
	if second != first {
		t.Fatal("expected create overlay state to persist across close/reopen")
	}
	parent := second.ParentID()
	if parent == nil || *parent != "az-parent" {
		t.Fatalf("expected parent to follow drill-down context, got %+v", parent)
	}

	closed, _ = m.Update(overlay.CloseOverlayMsg{})
	m = closed.(Model)
	m.drillDownParentID = ""

	reopened, _ = m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = reopened.(Model)
	third, ok := m.overlayStack.Current().(*overlay.CreateTaskOverlay)
	if !ok || third == nil {
		t.Fatalf("expected create overlay after second reopen, got %T", m.overlayStack.Current())
	}
	if third != first {
		t.Fatal("expected create overlay state to persist across second reopen")
	}
	if third.ParentID() != nil {
		t.Fatalf("expected parent to clear outside drill-down, got %v", *third.ParentID())
	}
}

func TestCreateTaskOverlayBindsAttachmentServiceInNormalMode(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.attachmentService = testCreateOverlayAttachmentService{}

	updated, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)

	current, ok := m.overlayStack.Current().(*overlay.CreateTaskOverlay)
	if !ok || current == nil {
		t.Fatalf("expected create overlay, got %T", m.overlayStack.Current())
	}
	if !current.HasAttachmentService() {
		t.Fatal("expected create overlay to have attachment service bound")
	}
}

func TestTaskCreatedResultMsgCreateDraftResetBehavior(t *testing.T) {
	t.Run("update does not clear create draft", func(t *testing.T) {
		m := newTestModel()
		m.createTaskOverlay = overlay.NewCreateTaskOverlay()

		updated, _ := m.Update(taskCreatedResultMsg{
			taskID:   "az-123",
			err:      nil,
			isUpdate: true,
		})
		next := updated.(Model)
		if next.createTaskOverlay == nil {
			t.Fatal("expected create draft to persist after task update")
		}
	})

	t.Run("successful create clears create draft", func(t *testing.T) {
		m := newTestModel()
		m.createTaskOverlay = overlay.NewCreateTaskOverlay()

		updated, _ := m.Update(taskCreatedResultMsg{
			taskID:   "az-new",
			err:      nil,
			isUpdate: false,
		})
		next := updated.(Model)
		if next.createTaskOverlay != nil {
			t.Fatal("expected create draft to clear after successful new task creation")
		}
	})
}

func TestTaskCreatedResultSelectsNewTaskAfterRefresh(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	createdResult, _ := m.Update(taskCreatedResultMsg{
		taskID:   "az-new",
		err:      nil,
		isUpdate: false,
	})
	createdModel := createdResult.(Model)
	if createdModel.pendingCreatedTaskID != "az-new" {
		t.Fatalf("pendingCreatedTaskID = %q, want %q", createdModel.pendingCreatedTaskID, "az-new")
	}

	refreshedResult, _ := createdModel.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-new", Title: "New Task", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask},
			{ID: "az-2", Title: "Task 2", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask},
		},
		revision: 42,
	})
	refreshedModel := refreshedResult.(Model)

	if got := refreshedModel.nav.GetCursor().TaskID; got != "az-new" {
		t.Fatalf("cursor task = %q, want %q", got, "az-new")
	}
	if refreshedModel.pendingCreatedTaskID != "" {
		t.Fatalf("pendingCreatedTaskID = %q, want cleared state", refreshedModel.pendingCreatedTaskID)
	}
}

func TestTaskCreatedResultOpensChildInWorkspaceAfterRefresh(t *testing.T) {
	m := newTestModel()
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	parent := domain.Task{ID: parentID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask}
	m.tasks = []domain.Task{parent}
	m.nav.SelectTask(parentID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(parent, m.tasks, nil, 120, 30))
	m.openCreatedTaskInWorkspace = true

	createdResult, _ := m.Update(taskCreatedResultMsg{
		taskID:   childID.String(),
		err:      nil,
		isUpdate: false,
	})
	createdModel := createdResult.(Model)
	if createdModel.pendingCreatedWorkspaceTaskID != childID.String() {
		t.Fatalf("pendingCreatedWorkspaceTaskID = %q, want %q", createdModel.pendingCreatedWorkspaceTaskID, childID)
	}

	refreshedResult, _ := createdModel.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			parent,
			{ID: childID, Title: "Child", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, ParentID: &parentID},
		},
		revision: 42,
	})
	refreshedModel := refreshedResult.(Model)

	current := refreshedModel.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace to remain open, got %T", current)
	}
	if got := workspace.TaskID(); got != childID.String() {
		t.Fatalf("workspace task ID = %q, want %q", got, childID)
	}
	if refreshedModel.pendingCreatedWorkspaceTaskID != "" {
		t.Fatalf("pendingCreatedWorkspaceTaskID = %q, want cleared state", refreshedModel.pendingCreatedWorkspaceTaskID)
	}
}

func TestTaskCreatedResultSelectsTaskAlreadyAppliedFromEvent(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.nav.SelectTask("az-1", 0)

	createdTask := domain.Task{ID: "az-new", Title: "New Task", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask}
	body, err := json.Marshal(protocol.TaskEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		TaskID:    "az-new",
		Task:      &createdTask,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal task event body: %v", err)
	}

	eventApplied, _ := m.Update(daemonStreamEventMsg{event: protocol.EventEnvelope{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Revision:  1,
		Event:     protocol.EventTaskCreated,
		Body:      body,
	}})
	eventModel := eventApplied.(Model)
	if got := eventModel.nav.GetCursor().TaskID; got != "az-1" {
		t.Fatalf("cursor before create result = %q, want existing selection", got)
	}

	createdResult, _ := eventModel.Update(taskCreatedResultMsg{
		taskID:   "az-new",
		err:      nil,
		isUpdate: false,
	})
	createdModel := createdResult.(Model)
	if got := createdModel.nav.GetCursor().TaskID; got != "az-new" {
		t.Fatalf("cursor task = %q, want az-new", got)
	}
	if createdModel.pendingCreatedTaskID != "" {
		t.Fatalf("pendingCreatedTaskID = %q, want cleared state", createdModel.pendingCreatedTaskID)
	}
}

func TestModeTransitions(t *testing.T) {
	m := newTestModel()

	t.Run("escape exits non-normal modes", func(t *testing.T) {
		modes := []Mode{ModeGoto, ModeSearch, ModeAction, ModeSelect}

		for _, mode := range modes {
			m.editor.SetMode(mode)
			if mode == ModeSearch {
				m.editor.SetSearchQuery("pending")
			}
			result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
			newModel := result.(Model)

			if !newModel.editor.IsNormal() {
				t.Errorf("Expected ModeNormal after escape from %v, got %v", mode, newModel.editor.GetMode())
			}
			if mode == ModeSearch && newModel.editor.GetFilter().SearchQuery != "" {
				t.Errorf("expected search query to clear on escape from search mode, got %q", newModel.editor.GetFilter().SearchQuery)
			}
		}
	})

	t.Run("global keys work in all modes", func(t *testing.T) {
		// Test ctrl+c (quit)
		m.editor.EnterGoto()
		_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Error("Expected quit command, got nil")
		}

		// Test ctrl+l (clear screen)
		m.editor.EnterAction()
		_, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlL})
		if cmd == nil {
			t.Error("Expected clear screen command, got nil")
		}
	})

	t.Run("r refreshes outside action mode", func(t *testing.T) {
		m.editor.EnterNormal()
		gitSync := &recordingGitSyncService{}
		m.gitSyncService = gitSync

		result, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		newModel := result.(Model)

		if cmd == nil {
			t.Fatal("expected refresh command, got nil")
		}
		if gitSync.fetchCalls != 1 {
			t.Fatalf("expected one git sync refresh call, got %d", gitSync.fetchCalls)
		}
		if !newModel.editor.IsNormal() {
			t.Fatalf("expected refresh to keep normal mode, got %v", newModel.editor.GetMode())
		}
	})

	t.Run("tab switches configured views without changing legacy modes", func(t *testing.T) {
		m.editor.EnterNormal()
		m.nav.SelectTask("az-2", 0)
		before := getCursorPosition(m)

		result, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
		updated := result.(Model)
		if cmd == nil {
			t.Fatal("Tab did not schedule configured-view selection")
		}
		if got := getCursorPosition(updated); got != before {
			t.Fatalf("cursor position changed while selecting next view: before=%+v after=%+v", before, got)
		}
	})

	t.Run("shift tab switches configured views without changing cursor position", func(t *testing.T) {
		m.editor.EnterNormal()
		m.nav.SelectTask("az-2", 0)
		before := getCursorPosition(m)

		result, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyShiftTab})
		updated := result.(Model)
		if cmd == nil {
			t.Fatal("Shift+Tab did not schedule configured-view selection")
		}
		if got := getCursorPosition(updated); got != before {
			t.Fatalf("cursor position changed while selecting previous view: before=%+v after=%+v", before, got)
		}
	})
}

func TestBoardViewCycleIndex(t *testing.T) {
	viewIDs := []string{"first", "middle", "last"}
	tests := []struct {
		name      string
		current   string
		direction int
		want      int
	}{
		{name: "next", current: "middle", direction: 1, want: 2},
		{name: "next wraps", current: "last", direction: 1, want: 0},
		{name: "previous", current: "middle", direction: -1, want: 0},
		{name: "previous wraps", current: "first", direction: -1, want: 2},
		{name: "missing current defaults to first", current: "missing", direction: -1, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boardViewCycleIndex(viewIDs, tt.current, tt.direction); got != tt.want {
				t.Fatalf("boardViewCycleIndex(%v, %q, %d) = %d, want %d", viewIDs, tt.current, tt.direction, got, tt.want)
			}
		})
	}
}

func TestActionModeUnavailablePRKeyFailsFastWithGuidance(t *testing.T) {
	keys := []string{"P"}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			m := newTestModel()
			m.editor.EnterAction()

			before := len(m.toasts)
			result, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			newModel := result.(Model)

			if cmd != nil {
				t.Fatalf("expected no command for action key %q, got %T", key, cmd)
			}
			if got := len(newModel.toasts); got != before+1 {
				t.Fatalf("expected one toast for key %q, got %d", key, got-before)
			}
			last := newModel.toasts[len(newModel.toasts)-1]
			if !strings.Contains(last.Message, "no git operation was started") {
				t.Fatalf("toast for %q missing no-op guidance: %q", key, last.Message)
			}
			if !strings.Contains(last.Message, "continue/abort") {
				t.Fatalf("toast for %q missing continue/abort guidance: %q", key, last.Message)
			}
		})
	}
}

func TestLoadingStateAcceptsImmediateInteraction(t *testing.T) {
	m := newTestModel()
	m.loading = true
	m.editor.EnterNormal()
	m.nav.SelectTask("az-1", 0)

	if got := m.View(); !strings.Contains(got, "Loading tickets") {
		t.Fatalf("expected loading view while hydrated state is pending, got %q", got)
	}

	t.Run("navigation works while loading", func(t *testing.T) {
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		if !newModel.loading {
			t.Fatal("expected loading state to remain active during immediate interaction")
		}
		pos := getCursorPosition(newModel)
		if pos.Column != 1 {
			t.Fatalf("expected cursor to move while loading, got column %d", pos.Column)
		}
	})

	t.Run("mode changes work while loading", func(t *testing.T) {
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		newModel := result.(Model)

		if !newModel.loading {
			t.Fatal("expected loading state to remain active during mode change")
		}
		if !newModel.editor.IsSelect() {
			t.Fatalf("expected select mode while loading, got %v", newModel.editor.GetMode())
		}
	})
}

func TestEpicDrillDownFlow(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.loading = false

	epicID := "az-epic"
	childID := "az-epic-child"
	epicIssueID := naming.IssueID(epicID)
	childIssueID := naming.IssueID(childID)
	m.tasks = append(m.tasks,
		domain.Task{
			ID:       epicIssueID,
			Title:    "Parent Epic",
			Status:   domain.StatusOpen,
			Priority: domain.P1,
			Type:     domain.TypeEpic,
		},
		domain.Task{
			ID:       childIssueID,
			Title:    "Epic Child",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			ParentID: &epicIssueID,
		},
	)

	m.nav.SelectTask(epicID, 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnter})
	newModel := result.(Model)

	if !newModel.overlayStack.IsEmpty() {
		t.Fatalf("expected drill-down board mode instead of overlay, got %T", newModel.overlayStack.Current())
	}
	if newModel.drillDownParentID != epicID {
		t.Fatalf("drillDownParentID = %q, want %q", newModel.drillDownParentID, epicID)
	}
	view := newModel.View()
	if !strings.Contains(view, "Children of "+epicID) {
		t.Fatalf("expected drill-down toolbar in view, got %q", view)
	}
	columns := newModel.buildColumns()
	var renderedIDs []string
	for _, column := range columns {
		for _, task := range column.Tasks {
			renderedIDs = append(renderedIDs, task.ID.String())
		}
	}
	if slices.Contains(renderedIDs, "az-1") {
		t.Fatalf("expected drill-down board to hide unrelated parent tasks, rendered IDs=%v", renderedIDs)
	}
	if !slices.Contains(renderedIDs, childID) {
		t.Fatalf("expected drill-down board to include child task, rendered IDs=%v", renderedIDs)
	}
	if pos := getCursorPosition(newModel); pos.Column != 0 || pos.Task != 0 {
		t.Fatalf("expected drill-down cursor to jump to first child, got %+v", pos)
	}

	updated, _ := newModel.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	closed := updated.(Model)
	if closed.isDrillDownActive() {
		t.Fatal("expected esc to exit drill-down board mode")
	}
	if finalPos := getCursorPosition(closed); finalPos.Column < 0 || finalPos.Task < 0 {
		t.Fatalf("cursor should remain on a valid board position after drill-down exit, got %+v", finalPos)
	}
}

func TestNestedDrillDownEscapePopsSingleLevel(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.loading = false

	parentID := "az-parent"
	childID := "az-child"
	grandchildID := "az-grandchild"
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	grandchildIssueID := naming.IssueID(grandchildID)
	m.tasks = append(m.tasks,
		domain.Task{
			ID:       parentIssueID,
			Title:    "Parent",
			Status:   domain.StatusOpen,
			Priority: domain.P1,
			Type:     domain.TypeEpic,
		},
		domain.Task{
			ID:       childIssueID,
			Title:    "Child",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			ParentID: &parentIssueID,
		},
		domain.Task{
			ID:       grandchildIssueID,
			Title:    "Grandchild",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
			ParentID: &childIssueID,
		},
	)

	m.nav.SelectTask(parentID, 0)

	first, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnter})
	afterFirstEnter := first.(Model)
	if got := afterFirstEnter.drillDownParentID; got != parentID {
		t.Fatalf("after first enter drillDownParentID = %q, want %q", got, parentID)
	}

	afterFirstEnter.nav.SelectTask(childID, 0)
	second, _ := afterFirstEnter.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnter})
	afterSecondEnter := second.(Model)
	if got := afterSecondEnter.drillDownParentID; got != childID {
		t.Fatalf("after second enter drillDownParentID = %q, want %q", got, childID)
	}
	if got := len(afterSecondEnter.drillDownTrail); got != 1 {
		t.Fatalf("expected one drill-down context in trail, got %d", got)
	}

	oneEsc, _ := afterSecondEnter.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	afterOneEsc := oneEsc.(Model)
	if got := afterOneEsc.drillDownParentID; got != parentID {
		t.Fatalf("after one esc drillDownParentID = %q, want %q", got, parentID)
	}
	if !afterOneEsc.isDrillDownActive() {
		t.Fatal("expected drill-down to remain active after exiting one nested level")
	}
	if got := len(afterOneEsc.drillDownTrail); got != 0 {
		t.Fatalf("expected drill-down trail to be empty after one esc, got %d", got)
	}

	columns := afterOneEsc.buildColumns()
	var renderedIDs []string
	for _, column := range columns {
		for _, task := range column.Tasks {
			renderedIDs = append(renderedIDs, task.ID.String())
		}
	}
	if !slices.Contains(renderedIDs, childID) {
		t.Fatalf("expected parent-level drill-down board to include child task, rendered IDs=%v", renderedIDs)
	}
	if slices.Contains(renderedIDs, grandchildID) {
		t.Fatalf("expected parent-level drill-down board to hide grandchild task, rendered IDs=%v", renderedIDs)
	}

	twoEsc, _ := afterOneEsc.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	afterTwoEsc := twoEsc.(Model)
	if afterTwoEsc.isDrillDownActive() {
		t.Fatal("expected second esc to exit final drill-down level")
	}
}

func TestTaskDetailPanelUsesGraphForTypedRelations(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()

	currentID := "az-current"
	upstreamID := "az-upstream"
	downstreamID := "az-downstream"
	currentIssueID := naming.IssueID(currentID)
	upstreamIssueID := naming.IssueID(upstreamID)
	downstreamIssueID := naming.IssueID(downstreamID)

	m.tasks = append(m.tasks,
		domain.Task{
			ID:       currentIssueID,
			Title:    "Current task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: downstreamIssueID, Type: domain.DependencyBlocks},
			},
		},
		domain.Task{
			ID:       upstreamIssueID,
			Title:    "Upstream task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: currentIssueID, Type: domain.DependencyRelatedTo},
			},
		},
		domain.Task{
			ID:       downstreamIssueID,
			Title:    "Downstream task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		},
	)

	m.nav.SelectTask(currentID, 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	taskWorkspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay on top, got %T", current)
	}
	view := taskWorkspace.View()
	if strings.Contains(view, "Dependencies") || strings.Contains(view, "Outgoing") || strings.Contains(view, "Incoming") {
		t.Fatalf("expected dependency summary to be omitted from task panel, got %q", view)
	}
	if !strings.Contains(view, "Graph") || !strings.Contains(view, "> az-downstream [Open] Downstream task") || !strings.Contains(view, "> az-upstream [Open] Upstream task") {
		t.Fatalf("expected typed relations in task graph, got %q", view)
	}
	if !strings.Contains(view, "Current task") {
		t.Fatalf("expected task title in task panel, got %q", view)
	}
	if !strings.Contains(view, "Actions") {
		t.Fatalf("expected action panel to render in task workspace, got %q", view)
	}
}

func TestSpaceWorkspaceUsesVisibleFilteredTaskWhenCursorTaskIDIsHidden(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.tasks = []domain.Task{
		{
			ID:       "az-hidden",
			Title:    "Hidden task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		},
		{
			ID:       "az-visible",
			Title:    "Visible task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		},
	}

	m.editor.SetSearchQuery("visible")
	cursor := m.nav.GetCursor()
	cursor.TaskID = "az-hidden"
	cursor.FallbackColumn = 0
	cursor.FallbackTask = 0

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	taskWorkspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay on top, got %T", current)
	}
	if got := taskWorkspace.TaskID(); got != "az-visible" {
		t.Fatalf("workspace task ID = %q, want %q", got, "az-visible")
	}
}

func TestTaskWorkspaceGraphNavigationOpensRelatedTask(t *testing.T) {
	m := newTestModel()
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	m.tasks = []domain.Task{
		{
			ID:     parentID,
			Title:  "Parent task",
			Status: domain.StatusOpen,
			Dependencies: []domain.Dependency{
				{ID: childID, Type: domain.DependencyBlocks},
			},
		},
		{
			ID:     childID,
			Title:  "Child task",
			Status: domain.StatusInProgress,
		},
	}
	m.nav.SelectTask(parentID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

	updated, _ := m.handleSelection(overlay.SelectionMsg{
		Key:   "task_workspace_open_task",
		Value: childID.String(),
	})
	next := updated.(Model)

	current := next.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace to remain open, got %T", current)
	}
	if got := workspace.TaskID(); got != childID.String() {
		t.Fatalf("workspace task ID = %q, want %q", got, childID)
	}
	if view := workspace.View(); !strings.Contains(view, "Child task") {
		t.Fatalf("workspace did not render selected child task, got %q", view)
	}
}

func TestTaskWorkspaceGraphNavigationOpensAndRefreshesOffBoardRelatedTask(t *testing.T) {
	m := newTestModel()
	currentID := naming.IssueID("az-current")
	relatedID := naming.IssueID("az-related")
	current := domain.Task{
		ID:     currentID,
		Title:  "Current task",
		Status: domain.StatusOpen,
		Dependencies: []domain.Dependency{
			{ID: relatedID, Type: domain.DependencyRelatedTo},
		},
	}
	related := domain.Task{ID: relatedID, Title: "Related off-board task", Status: domain.StatusInProgress}
	m.tasks = []domain.Task{current}
	m.nav.SelectTask(currentID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(current, []domain.Task{current, related}, nil, 120, 30))

	updated, _ := m.handleSelection(overlay.SelectionMsg{
		Key:   "task_workspace_open_task",
		Value: relatedID.String(),
	})
	next := updated.(Model)
	workspace, ok := next.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace to remain open, got %T", next.overlayStack.Current())
	}
	if got := workspace.TaskID(); got != relatedID.String() {
		t.Fatalf("workspace task ID = %q, want off-board relation %q", got, relatedID)
	}
	if view := workspace.View(); !strings.Contains(view, "Related off-board task") || !strings.Contains(view, "Current task") {
		t.Fatalf("workspace lost off-board task or graph context during navigation, got %q", view)
	}

	refreshedRelated := related
	refreshedRelated.Title = "Related off-board task refreshed"
	refreshedRelated.Description = "Full off-board details"
	updated, _ = next.Update(refreshTaskWorkspaceResultMsg{
		taskID:  relatedID.String(),
		hasTask: true,
		task:    refreshedRelated,
		tasks:   []domain.Task{refreshedRelated, current},
	})
	next = updated.(Model)
	workspace = next.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	view := workspace.View()
	if !strings.Contains(view, "Related off-board task refreshed") || !strings.Contains(view, "Full off-board details") {
		t.Fatalf("workspace did not apply full refresh for off-board relation, got %q", view)
	}
	if len(next.tasks) != 1 || next.tasks[0].ID != currentID {
		t.Fatalf("off-board navigation changed board projection state: %+v", next.tasks)
	}
}

func TestTaskWorkspaceRefreshPreservesDeeperBoardGraphContext(t *testing.T) {
	m := newTestModel()
	rootID := naming.IssueID("az-root")
	childID := naming.IssueID("az-child")
	grandchildID := naming.IssueID("az-grandchild")
	root := domain.Task{ID: rootID, Title: "Root", Status: domain.StatusOpen}
	child := domain.Task{ID: childID, Title: "Child", Status: domain.StatusOpen, ParentID: &rootID}
	grandchild := domain.Task{ID: grandchildID, Title: "Grandchild", Status: domain.StatusOpen, ParentID: &childID}
	m.tasks = []domain.Task{root, child, grandchild}
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(root, m.tasks, nil, 120, 30))

	refreshedRoot := root
	refreshedRoot.Title = "Root refreshed"
	updated, _ := m.Update(refreshTaskWorkspaceResultMsg{
		taskID:  rootID.String(),
		hasTask: true,
		task:    refreshedRoot,
		tasks:   []domain.Task{refreshedRoot, child},
	})
	next := updated.(Model)
	workspace := next.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	view := workspace.View()
	if !strings.Contains(view, "Root refreshed") || !strings.Contains(view, "Grandchild") {
		t.Fatalf("workspace refresh discarded deeper graph context already loaded by the board: %q", view)
	}
}

func TestTaskWorkspaceGraphNavigationRefreshPreservesGraphFocus(t *testing.T) {
	m := newTestModel()
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	m.tasks = []domain.Task{
		{
			ID:     parentID,
			Title:  "Parent task",
			Status: domain.StatusOpen,
			Dependencies: []domain.Dependency{
				{ID: childID, Type: domain.DependencyBlocks},
			},
		},
		{
			ID:     childID,
			Title:  "Child task",
			Status: domain.StatusInProgress,
		},
	}
	m.nav.SelectTask(parentID.String(), 0)
	workspace := overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30)
	model, _ := workspace.Update(tea.KeyMsg{Type: tea.KeyTab})
	workspace = model.(*overlay.TaskWorkspaceOverlay)
	m.overlayStack.Push(workspace)

	updated, _ := m.handleSelection(overlay.SelectionMsg{
		Key:   "task_workspace_open_task",
		Value: childID.String(),
	})
	next := updated.(Model)
	assertTaskWorkspaceGraphFocus(t, next)

	refreshedChild := m.tasks[1]
	refreshedChild.Title = "Child task refreshed"
	updated, _ = next.Update(refreshTaskWorkspaceResultMsg{
		taskID:  childID.String(),
		hasTask: true,
		task:    refreshedChild,
		tasks:   next.tasks,
	})
	next = updated.(Model)

	current := next.overlayStack.Current()
	refreshedWorkspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace to remain open, got %T", current)
	}
	if got := refreshedWorkspace.TaskID(); got != childID.String() {
		t.Fatalf("workspace task ID = %q, want %q", got, childID)
	}
	assertTaskWorkspaceGraphFocus(t, next)
	if view := refreshedWorkspace.View(); !strings.Contains(view, "Child task refreshed") {
		t.Fatalf("workspace did not render refreshed child task, got %q", view)
	}
}

func TestTaskWorkspaceDrillDownSelectionOpensChildBoard(t *testing.T) {
	m := newTestModel()
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	m.tasks = []domain.Task{
		{
			ID:     parentID,
			Title:  "Parent task",
			Status: domain.StatusOpen,
		},
		{
			ID:       childID,
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			ParentID: &parentID,
		},
	}
	m.nav.SelectTask(parentID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

	updated, _ := m.handleSelection(overlay.SelectionMsg{
		Key:   "task_workspace_drill_down",
		Value: parentID.String(),
	})
	next := updated.(Model)

	if current := next.overlayStack.Current(); current != nil {
		t.Fatalf("expected workspace to close before drill-down board opens, got %T", current)
	}
	if got := next.drillDownParentID; got != parentID.String() {
		t.Fatalf("drillDownParentID = %q, want %q", got, parentID)
	}
	columns := next.buildColumns()
	pos := next.nav.GetPosition(columns)
	if !pos.Valid || pos.Column >= len(columns) || pos.Task >= len(columns[pos.Column].Tasks) {
		t.Fatalf("expected valid drill-down child selection, got %+v", pos)
	}
	if got := columns[pos.Column].Tasks[pos.Task].ID.String(); got != childID.String() {
		t.Fatalf("selected task = %q, want %q", got, childID)
	}
}

func TestDrillDownBoardAttachKeyTargetsSelectedChild(t *testing.T) {
	m := newTestModel()
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	m.tasks = []domain.Task{
		{
			ID:     parentID,
			Title:  "Parent task",
			Status: domain.StatusOpen,
		},
		{
			ID:             childID,
			Title:          "Child task",
			Status:         domain.StatusInProgress,
			ParentID:       &parentID,
			HasTmuxSession: true,
		},
	}
	m.enterDrillDown(parentID.String(), "Parent task")
	m.nav.SelectTask(childID.String(), 1)

	updated, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected attach command from drill-down board")
	}
	next := updated.(Model)
	if len(next.toasts) == 0 {
		t.Fatal("expected attach feedback toast")
	}
	if got := next.toasts[len(next.toasts)-1].Message; got != "Attaching to az-child" {
		t.Fatalf("toast = %q, want attach feedback for selected child", got)
	}
}

func assertTaskWorkspaceGraphFocus(t *testing.T, m Model) {
	t.Helper()
	current := m.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace to remain open, got %T", current)
	}
	joined := ""
	for _, binding := range workspace.StatusBindings() {
		joined += binding.Key + " " + binding.Description + " "
	}
	if !strings.Contains(joined, "select relation") {
		t.Fatalf("expected task workspace to stay on graph focus, got bindings %q", joined)
	}
	if strings.Contains(joined, "j/k/↑/↓ scroll") {
		t.Fatalf("expected task workspace not to revert to detail scroll focus, got bindings %q", joined)
	}
}

func TestOpenTaskWorkspaceByIDDoesNotPushDuplicateCurrentWorkspace(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	updated, cmd := m.openTaskWorkspaceByID("az-1")
	if cmd == nil {
		t.Fatal("expected initial workspace open command")
	}
	m = updated.(Model)

	updated, _ = m.openTaskWorkspaceByID("az-1")
	m = updated.(Model)

	current := m.overlayStack.Pop()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace overlay, got %T", current)
	}
	if got := workspace.TaskID(); got != "az-1" {
		t.Fatalf("workspace task ID = %q, want az-1", got)
	}
	if next := m.overlayStack.Pop(); next != nil {
		t.Fatalf("expected a single workspace overlay on stack, got extra %T", next)
	}
}

func TestOpenTaskWorkspaceByIDReplacesExistingWorkspace(t *testing.T) {
	m := newTestModel()

	updated, _ := m.openTaskWorkspaceByID("az-1")
	m = updated.(Model)
	updated, _ = m.openTaskWorkspaceByID("az-2")
	m = updated.(Model)

	top, ok := m.overlayStack.Pop().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace on top")
	}
	if got := top.TaskID(); got != "az-2" {
		t.Fatalf("workspace task ID = %q, want az-2", got)
	}

	if extra := m.overlayStack.Pop(); extra != nil {
		t.Fatalf("expected one workspace after replacement, got extra %T", extra)
	}
}

func TestOpenTaskWorkspaceByIDReopenSameWorkspaceWithoutDuplicating(t *testing.T) {
	m := newTestModel()

	updated, _ := m.openTaskWorkspaceByID("az-1")
	m = updated.(Model)
	updated, _ = m.openTaskWorkspaceByID("az-1")
	m = updated.(Model)

	count := 0
	for {
		if m.overlayStack.Pop() == nil {
			break
		}
		count++
	}
	if count != 1 {
		t.Fatalf("expected exactly one workspace overlay on the stack, got %d", count)
	}
}

func TestSpaceWorkspaceUsesAuthoritativeTaskProjection(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.tasks = []domain.Task{
		{
			ID:                    "az-1",
			Title:                 "Authoritative task",
			Status:                domain.StatusInProgress,
			Priority:              domain.P2,
			Type:                  domain.TypeTask,
			HasWorktree:           true,
			HasUncommittedChanges: false,
			GitAdditions:          0,
			GitDeletions:          0,
		},
	}

	m.nav.SelectTask("az-1", 1)
	columns := m.buildColumns()
	taskFromNav, _ := m.nav.GetCurrentTask(columns)
	if taskFromNav == nil {
		t.Fatal("expected selected nav task")
	}
	taskFromNav.HasUncommittedChanges = true
	taskFromNav.GitAdditions = 163
	taskFromNav.GitDeletions = 1

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	taskWorkspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay on top, got %T", current)
	}
	view := taskWorkspace.View()
	if strings.Contains(view, "dirty (+163/-1") {
		t.Fatalf("workspace should use authoritative clean task projection, got %q", view)
	}
	if !strings.Contains(view, "Worktree:  clean") {
		t.Fatalf("workspace missing clean worktree summary, got %q", view)
	}
}

func TestEnterOnLeafTaskShowsDrillDownGuidanceToast(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.nav.SelectTask("az-1", 0)

	beforeToasts := len(m.toasts)
	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnter})
	newModel := result.(Model)

	if !newModel.overlayStack.IsEmpty() {
		t.Fatalf("expected no overlay for leaf drill-down enter, got %T", newModel.overlayStack.Current())
	}
	if len(newModel.toasts) != beforeToasts+1 {
		t.Fatalf("expected one guidance toast, got %d new toasts", len(newModel.toasts)-beforeToasts)
	}
	last := newModel.toasts[len(newModel.toasts)-1]
	if !strings.Contains(last.Message, "No children to drill into") {
		t.Fatalf("unexpected toast message: %q", last.Message)
	}
}

func TestEnterOnNonParentDependenciesDoesNotOpenChildDrillDown(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()

	taskID := "az-non-parent-rel"
	taskIssueID := naming.IssueID(taskID)
	m.tasks = append(m.tasks, domain.Task{
		ID:       taskIssueID,
		Title:    "Task with blocks dependency only",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Dependencies: []domain.Dependency{
			{ID: naming.IssueID("az-upstream"), Type: domain.DependencyBlocks},
		},
	})
	m.nav.SelectTask(taskID, 0)

	beforeToasts := len(m.toasts)
	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnter})
	newModel := result.(Model)

	if !newModel.overlayStack.IsEmpty() {
		t.Fatalf("expected no drill-down overlay for non-parent dependency, got %T", newModel.overlayStack.Current())
	}
	if len(newModel.toasts) != beforeToasts+1 {
		t.Fatalf("expected one guidance toast, got %d new toasts", len(newModel.toasts)-beforeToasts)
	}
	last := newModel.toasts[len(newModel.toasts)-1]
	if !strings.Contains(last.Message, "No children to drill into") {
		t.Fatalf("unexpected toast message: %q", last.Message)
	}
}

func TestBuildColumns_HidesParentChildEvenWhenFilterToggleIsOff(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()

	parentID := "az-parent"
	parentIssueID := naming.IssueID(parentID)
	m.tasks = []domain.Task{
		{ID: parentIssueID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: naming.IssueID("az-child-parent-id"), Title: "Child by parent_id", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, ParentID: &parentIssueID},
		{
			ID:       naming.IssueID("az-child-dep"),
			Title:    "Child by parent-child dep",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: parentIssueID, Type: domain.DependencyParentChild},
			},
		},
		{
			ID:       naming.IssueID("az-blocks-only"),
			Title:    "Blocks-only issue",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: parentIssueID, Type: domain.DependencyBlocks},
			},
		},
	}

	// Simulate user turning hide-child filter option off.
	m.editor.ToggleHideEpicChildren()

	columns := m.buildColumns()
	openTasks := columns[domain.StatusOpen.Column()].Tasks
	ids := make(map[string]struct{}, len(openTasks))
	for _, task := range openTasks {
		ids[task.ID.String()] = struct{}{}
	}

	if _, ok := ids[parentID]; !ok {
		t.Fatalf("parent task unexpectedly hidden: %+v", openTasks)
	}
	if _, ok := ids["az-blocks-only"]; !ok {
		t.Fatalf("blocks-only task unexpectedly hidden: %+v", openTasks)
	}
	if _, ok := ids["az-child-parent-id"]; ok {
		t.Fatalf("child by parent_id should be hidden from board: %+v", openTasks)
	}
	if _, ok := ids["az-child-dep"]; ok {
		t.Fatalf("child by parent-child dependency should be hidden from board: %+v", openTasks)
	}
}

func TestSessionTreeFilterShowsIssuesWithOwnOrDescendantSessionsDirectly(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.sessionTreeFilterOnly = true

	parentID := "az-parent"
	parentIssueID := naming.IssueID(parentID)
	childID := "az-child"
	childIssueID := naming.IssueID(childID)
	grandchildID := "az-grandchild"
	ownSessionID := "az-own-session"
	unrelatedID := "az-unrelated"
	m.tasks = []domain.Task{
		{ID: parentIssueID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: childIssueID, Title: "Child", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, ParentID: &parentIssueID},
		{
			ID:             naming.IssueID(grandchildID),
			Title:          "Grandchild",
			Status:         domain.StatusOpen,
			Priority:       domain.P2,
			Type:           domain.TypeTask,
			ParentID:       &childIssueID,
			HasTmuxSession: true,
		},
		{
			ID:       naming.IssueID(ownSessionID),
			Title:    "Own session",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Session:  &domain.Session{IssueID: naming.IssueID(ownSessionID), State: domain.SessionBusy},
		},
		{ID: naming.IssueID(unrelatedID), Title: "Unrelated", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}

	columns := m.buildColumns()
	openTasks := columns[domain.StatusOpen.Column()].Tasks
	ids := make(map[string]struct{}, len(openTasks))
	for _, task := range openTasks {
		ids[task.ID.String()] = struct{}{}
	}

	if _, ok := ids[parentID]; !ok {
		t.Fatalf("expected parent with descendant session to be visible: %+v", openTasks)
	}
	if _, ok := ids[ownSessionID]; !ok {
		t.Fatalf("expected task with own session to be visible: %+v", openTasks)
	}
	if _, ok := ids[childID]; !ok {
		t.Fatalf("expected child ancestor of session to be directly visible: %+v", openTasks)
	}
	if _, ok := ids[grandchildID]; !ok {
		t.Fatalf("expected grandchild with session to be directly visible: %+v", openTasks)
	}
	if _, ok := ids[unrelatedID]; ok {
		t.Fatalf("unrelated issue should be filtered out: %+v", openTasks)
	}
}

func TestSessionTreeFilterHotkeyTogglesSummaryAndVisibleTasks(t *testing.T) {
	m := newTestModel()
	m.loading = false
	activeID := "az-active"
	inactiveID := "az-inactive"
	m.tasks = []domain.Task{
		{ID: naming.IssueID(activeID), Title: "Active", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, HasTmuxSession: true},
		{ID: naming.IssueID(inactiveID), Title: "Inactive", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	next := result.(Model)
	if !next.sessionTreeFilterOnly {
		t.Fatal("expected t to enable session tree filter")
	}
	if got := next.filterSummary(); got != "F:tree:session" {
		t.Fatalf("filter summary = %q, want session tree token", got)
	}
	openTasks := next.buildColumns()[domain.StatusOpen.Column()].Tasks
	if len(openTasks) != 1 || openTasks[0].ID.String() != activeID {
		t.Fatalf("visible open tasks = %+v, want only %s", openTasks, activeID)
	}

	result, _ = next.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	next = result.(Model)
	if next.sessionTreeFilterOnly {
		t.Fatal("expected t to disable session tree filter")
	}
	if got := next.filterSummary(); got != "F:none" {
		t.Fatalf("filter summary after disable = %q, want F:none", got)
	}
}

func TestModeStrings(t *testing.T) {
	tests := []struct {
		mode     Mode
		expected string
	}{
		{ModeNormal, "NORMAL"},
		{ModeSelect, "SELECT"},
		{ModeSearch, "SEARCH"},
		{ModeGoto, "GOTO"},
		{ModeAction, "ACTION"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.mode.String() != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.mode.String())
			}
		})
	}
}

func TestBoardColumnsPrioritizeHumanAttentionBeforeConfiguredSort(t *testing.T) {
	m := newTestModel()
	m.boardView = domain.DefaultBoardView()
	m.tasks = []domain.Task{
		{ID: "ordinary", Status: domain.StatusInProgress, Priority: domain.P0, GitAdditions: 100},
		{ID: "waiting", Status: domain.StatusInProgress, Priority: domain.P4, Session: &domain.Session{Activity: "waiting-for-human"}},
	}

	columns := m.buildColumns()
	active := columns[domain.StatusInProgress.Column()].Tasks
	if len(active) != 2 || active[0].ID.String() != "waiting" || active[1].ID.String() != "ordinary" {
		t.Fatalf("active tasks = %+v, want waiting-human task before git-diff order", active)
	}
}

func TestBoardColumnsDoNotApplyAutomaticAttentionSortToCustomView(t *testing.T) {
	m := newTestModel()
	m.boardView = domain.DefaultBoardView()
	m.boardView.ID = "custom"
	m.boardView.Options.SortPolicy = domain.BoardViewSortDefault
	m.boardView.Sort = []domain.BoardViewSortRule{{Key: domain.BoardViewSortKeyGitDiff, Direction: domain.BoardViewSortDescending}}
	m.tasks = []domain.Task{
		{ID: "ordinary", Status: domain.StatusInProgress, GitAdditions: 100},
		{ID: "waiting", Status: domain.StatusInProgress, Session: &domain.Session{Activity: "waiting-for-human"}},
	}

	active := m.buildColumns()[1].Tasks
	if len(active) != 2 || active[0].ID.String() != "ordinary" {
		t.Fatalf("custom view tasks = %+v, want configured git-diff order", active)
	}
}

func TestBoardColumnsExplicitSortOverridesBuiltInAttentionPolicy(t *testing.T) {
	m := newTestModel()
	m.boardView = domain.DefaultBoardView()
	m.editor.SetSortField(domain.SortByPriority)
	m.editor.SetSortOrder(domain.SortAsc)
	m.tasks = []domain.Task{
		{ID: "ordinary", Status: domain.StatusInProgress, Priority: domain.P0},
		{ID: "waiting", Status: domain.StatusInProgress, Priority: domain.P4, Session: &domain.Session{Activity: "waiting-for-human"}},
	}

	active := m.buildColumns()[1].Tasks
	if len(active) != 2 || active[0].ID.String() != "ordinary" {
		t.Fatalf("explicitly sorted tasks = %+v, want priority order", active)
	}
}

func TestSortSummaryReportsAutomaticAttentionPolicyAndExplicitOverride(t *testing.T) {
	m := newTestModel()
	m.boardView = domain.DefaultBoardView()
	if got := m.sortSummary(); got != "S:attention+git_diff/asc" {
		t.Fatalf("automatic sort summary = %q", got)
	}
	m.editor.SetSortField(domain.SortByPriority)
	if got := m.sortSummary(); got != "S:priority/asc" {
		t.Fatalf("explicit sort summary = %q", got)
	}
}

func TestIssuesLoadedReconcilesSelection(t *testing.T) {
	m := newTestModel()
	m.editor.Select("az-1")
	m.editor.Select("ghost")

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug},
		},
		revision: 9,
	})
	newModel := result.(Model)

	if got := newModel.editor.SelectionCount(); got != 1 {
		t.Fatalf("selection count = %d, want 1", got)
	}
	if !newModel.editor.IsSelected("az-1") {
		t.Fatal("expected az-1 to remain selected after refresh")
	}
	if newModel.editor.IsSelected("ghost") {
		t.Fatal("expected ghost selection to be pruned after refresh")
	}
}

func TestIssuesLoadedKeepsCursorOnValidTaskAfterRefresh(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-5", 3)

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-2", Title: "Task 2", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeBug},
			{ID: "az-3", Title: "Task 3", Status: domain.StatusDone, Priority: domain.P2, Type: domain.TypeTask},
		},
		revision: 10,
	})
	newModel := result.(Model)

	cursor := newModel.nav.GetCursor()
	if cursor.TaskID == "" {
		t.Fatal("expected cursor to stay on a valid task after refresh")
	}
	validTaskIDs := map[string]struct{}{
		"az-1": {},
		"az-2": {},
		"az-3": {},
	}
	if _, ok := validTaskIDs[cursor.TaskID]; !ok {
		t.Fatalf("cursor task %q not present in refreshed task set", cursor.TaskID)
	}
}

func TestIssuesLoadedKeepsSelectedIssueInViewportAfterResort(t *testing.T) {
	m := newTestModel()
	m.height = 18
	m.width = 80
	m.editor.SetSortField(domain.SortByPriority)
	m.editor.SetSortOrder(domain.SortAsc)

	initialTasks := []domain.Task{
		{ID: "az-selected", Title: "Selected", Status: domain.StatusOpen, Priority: domain.P0, Type: domain.TypeTask},
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask},
		{ID: "az-3", Title: "Task 3", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask},
		{ID: "az-4", Title: "Task 4", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-5", Title: "Task 5", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-6", Title: "Task 6", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-7", Title: "Task 7", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask},
		{ID: "az-8", Title: "Task 8", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask},
		{ID: "az-9", Title: "Task 9", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask},
		{ID: "az-10", Title: "Task 10", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask},
		{ID: "az-11", Title: "Task 11", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask},
	}
	m.tasks = initialTasks
	m.nav.SelectTask("az-selected", domain.StatusOpen.Column())
	m.viewportStarts[domain.StatusOpen.Column()] = 0

	refreshedTasks := make([]domain.Task, len(initialTasks))
	copy(refreshedTasks, initialTasks)
	for i := range refreshedTasks {
		if refreshedTasks[i].ID == "az-selected" {
			refreshedTasks[i].Priority = domain.P4
			break
		}
	}

	result, _ := m.Update(issuesLoadedMsg{
		tasks:    refreshedTasks,
		revision: 15,
	})
	newModel := result.(Model)

	columns := newModel.buildColumns()
	pos := newModel.nav.GetPosition(columns)
	if !pos.Valid || pos.Column != domain.StatusOpen.Column() {
		t.Fatalf("expected valid cursor in open column after refresh; got %+v", pos)
	}
	if columns[pos.Column].Tasks[pos.Task].ID != "az-selected" {
		t.Fatalf("cursor selected %q, want az-selected", columns[pos.Column].Tasks[pos.Task].ID)
	}

	availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(newModel.height))
	if availableHeight < 1 {
		availableHeight = 1
	}
	columnCount := newModel.boardColumnLayout(columns).VisibleCount
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := newModel.width / columnCount
	linesPerCard := board.CardLineFootprint(newModel.styles, board.CardContentWidth(columnWidth))
	if linesPerCard < 1 {
		linesPerCard = 1
	}
	windowStart, windowEnd := board.VisibleTaskWindow(
		len(columns[pos.Column].Tasks),
		newModel.viewportStarts[pos.Column],
		availableHeight,
		linesPerCard,
	)
	if pos.Task < windowStart || pos.Task >= windowEnd {
		t.Fatalf(
			"selected task index %d not visible in window [%d,%d) with viewport start %d",
			pos.Task,
			windowStart,
			windowEnd,
			newModel.viewportStarts[pos.Column],
		)
	}
}

func TestIssuesLoadedPreservesSnapshotSessionTaskState(t *testing.T) {
	startedAt := time.Date(2026, time.March, 25, 10, 30, 0, 0, time.UTC)
	devServer := &domain.DevServer{Port: 4242, Command: "npm run dev", Running: true}
	sourceSession := &domain.Session{
		IssueID:   "az-1",
		State:     domain.SessionBusy,
		StartedAt: &startedAt,
		Worktree:  "/tmp/az-1",
		DevServer: devServer,
	}

	m := newTestModel()
	m.sessions["stale"] = &domain.Session{
		IssueID:  "stale",
		State:    domain.SessionPaused,
		Worktree: "/tmp/stale",
	}

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{
				ID:       "az-1",
				Title:    "Task 1",
				Status:   domain.StatusInProgress,
				Priority: domain.P1,
				Type:     domain.TypeTask,
				Session:  sourceSession,
			},
			{
				ID:       "az-2",
				Title:    "Task 2",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeBug,
			},
		},
		revision: 12,
	})
	newModel := result.(Model)

	got := newModel.tasks[0].Session
	if got == nil {
		t.Fatal("expected hydrated task session from daemon snapshot")
	}
	if got == sourceSession {
		t.Fatal("hydrated task session should clone daemon snapshot data, not alias it")
	}
	if got.IssueID != sourceSession.IssueID || got.State != sourceSession.State || got.Worktree != sourceSession.Worktree {
		t.Fatalf("hydrated task session = %+v, want %+v", got, sourceSession)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(startedAt) {
		t.Fatalf("startedAt = %+v, want %v", got.StartedAt, startedAt)
	}
	if got.DevServer == nil || got.DevServer == devServer {
		t.Fatalf("dev server projection = %+v, want cloned dev server", got.DevServer)
	}
	if got.DevServer.Port != devServer.Port || got.DevServer.Command != devServer.Command || got.DevServer.Running != devServer.Running {
		t.Fatalf("dev server projection = %+v, want %+v", got.DevServer, devServer)
	}
}

func TestIssuesLoadedUsesHydratedRuntimeOverlays(t *testing.T) {
	m := newTestModel()
	startedAt := time.Now().Add(-3 * time.Minute)
	m.tasks[0].Session = &domain.Session{
		IssueID:   naming.IssueID(m.tasks[0].ID),
		State:     domain.SessionBusy,
		StartedAt: &startedAt,
	}
	m.tasks[0].HasTmuxSession = true
	m.tasks[0].HasWorktree = true
	m.tasks[0].GitAheadCount = 2
	m.tasks[0].GitBehindCount = 7
	m.tasks[0].HasUncommittedChanges = true
	m.tasks[0].GitAdditions = 11
	m.tasks[0].GitDeletions = 4

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1 refreshed", Status: domain.StatusInReview, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-6", Title: "Task 6", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeBug},
		},
		revision: 13,
	})
	newModel := result.(Model)

	task := newModel.tasks[0]
	if task.ID != "az-1" || task.Title != "Task 1 refreshed" || task.Status != domain.StatusInReview {
		t.Fatalf("refreshed task = %+v", task)
	}
	if task.HasTmuxSession || task.HasWorktree || task.GitAheadCount != 0 || task.GitBehindCount != 0 || task.HasUncommittedChanges || task.GitAdditions != 0 || task.GitDeletions != 0 {
		t.Fatalf("task should reflect hydrated runtime projection, got: %+v", task)
	}
	if len(newModel.tasks) != 2 {
		t.Fatalf("task count = %d, want 2", len(newModel.tasks))
	}
	if newModel.tasks[1].ID != "az-6" {
		t.Fatalf("unexpected second task: %+v", newModel.tasks[1])
	}
}

func TestTaskDeletionSuppressesHydratedResurrection(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug},
	}

	updated, cmd := m.Update(taskDeletedResultMsg{taskID: "az-1"})
	if cmd == nil {
		t.Fatal("expected refresh command after delete")
	}
	deletedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if !deletedModel.isTaskHydrationSuppressed("az-1") {
		t.Fatal("expected deleted task to be tombstoned for later hydrates")
	}
	if len(deletedModel.tasks) != 1 || deletedModel.tasks[0].ID != "az-2" {
		t.Fatalf("tasks after delete = %+v, want only az-2", deletedModel.tasks)
	}

	result, _ := deletedModel.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1 resurrected", Status: domain.StatusInReview, Priority: domain.P0, Type: domain.TypeTask},
			{ID: "az-2", Title: "Task 2 refreshed", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeBug},
			{ID: "az-3", Title: "Task 3", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeFeature},
		},
		revision: 14,
	})
	refreshed := result.(Model)

	if len(refreshed.tasks) != 2 {
		t.Fatalf("tasks after hydrate = %+v, want 2 tasks", refreshed.tasks)
	}
	for _, task := range refreshed.tasks {
		if task.ID == "az-1" {
			t.Fatalf("deleted task resurrected by hydrate: %+v", refreshed.tasks)
		}
	}
	if refreshed.tasks[0].ID != "az-2" || refreshed.tasks[1].ID != "az-3" {
		t.Fatalf("refreshed tasks = %+v, want az-2 and az-3", refreshed.tasks)
	}
}

func TestIssuesLoadedStartsPeriodicRefreshLoop(t *testing.T) {
	m := newTestModel()
	m.hasRefreshLoop = false

	result, cmd := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		},
		revision: 11,
	})
	newModel := result.(Model)

	if !newModel.hasRefreshLoop {
		t.Fatal("expected issuesLoaded to start periodic refresh loop")
	}
	if cmd == nil {
		t.Fatal("expected periodic refresh command batch after issuesLoaded")
	}
}

func TestIssuesLoaded_IgnoresStaleProjectResponses(t *testing.T) {
	m := newTestModel()
	m.currentProject = "azedarach"
	originalTasks := append([]domain.Task(nil), m.tasks...)
	originalRevision := m.daemonRevision

	result, _ := m.Update(issuesLoadedMsg{
		projectID: "chefy",
		tasks: []domain.Task{
			{ID: "chefy-1", Title: "Chefy Task", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		},
		revision: 99,
	})
	newModel := result.(Model)

	if len(newModel.tasks) != len(originalTasks) {
		t.Fatalf("stale response should not replace tasks; got len=%d want=%d", len(newModel.tasks), len(originalTasks))
	}
	if newModel.daemonRevision != originalRevision {
		t.Fatalf("stale response should not update revision; got=%d want=%d", newModel.daemonRevision, originalRevision)
	}
}

func TestSnapshotBackedLocalRefreshesIgnoreOlderRevisions(t *testing.T) {
	projectID := newTestModel().daemonProjectID()

	t.Run("task workspace refresh", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.tasks = []domain.Task{{ID: "az-1", Title: "Current", Status: domain.StatusOpen, Type: domain.TypeTask}}
		m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

		result, cmd := m.Update(refreshTaskWorkspaceResultMsg{
			projectID: projectID,
			revision:  7,
			taskID:    "az-1",
			hasTask:   true,
			task:      domain.Task{ID: "az-1", Title: "Stale", Status: domain.StatusDone, Type: domain.TypeTask},
		})
		updated := result.(Model)
		if cmd != nil {
			t.Fatalf("stale workspace refresh command = %T, want nil", cmd)
		}
		if updated.tasks[0].Title != "Current" || updated.tasks[0].Status != domain.StatusOpen {
			t.Fatalf("stale workspace refresh applied task = %+v", updated.tasks[0])
		}
	})

	t.Run("cleanup confirmation", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.tasks = []domain.Task{{ID: "az-1", Title: "Current", Status: domain.StatusOpen, Type: domain.TypeTask}}

		result, cmd := m.Update(worktreeCleanupConfirmPromptMsg{
			projectID: "chefy",
			revision:  99,
			taskID:    "az-1",
			hasTask:   true,
			task:      domain.Task{ID: "az-1", Title: "Wrong project", Status: domain.StatusDone, Type: domain.TypeTask},
		})
		updated := result.(Model)
		if cmd != nil {
			t.Fatalf("cross-project cleanup confirm command = %T, want nil", cmd)
		}
		if updated.pendingCleanup != nil {
			t.Fatal("cross-project cleanup confirm should not set pending cleanup")
		}
		if updated.tasks[0].Title != "Current" || updated.tasks[0].Status != domain.StatusOpen {
			t.Fatalf("cross-project cleanup confirm applied task = %+v", updated.tasks[0])
		}

		result, cmd = m.Update(worktreeCleanupConfirmPromptMsg{
			projectID: projectID,
			revision:  7,
			taskID:    "az-1",
			hasTask:   true,
			task:      domain.Task{ID: "az-1", Title: "Stale", Status: domain.StatusDone, Type: domain.TypeTask},
		})
		updated = result.(Model)
		if cmd != nil {
			t.Fatalf("stale cleanup confirm command = %T, want nil", cmd)
		}
		if updated.pendingCleanup != nil {
			t.Fatal("stale cleanup confirm should not set pending cleanup")
		}
		if updated.tasks[0].Title != "Current" || updated.tasks[0].Status != domain.StatusOpen {
			t.Fatalf("stale cleanup confirm applied task = %+v", updated.tasks[0])
		}
	})

	t.Run("bulk cleanup preflight", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.tasks = []domain.Task{{ID: "az-1", Title: "Current", Status: domain.StatusOpen, Type: domain.TypeTask}}

		result, cmd := m.Update(bulkCleanupPreflightMsg{
			projectID:      projectID,
			revision:       7,
			taskIDs:        []string{"az-1"},
			refreshedTasks: []domain.Task{{ID: "az-1", Title: "Stale", Status: domain.StatusDone, Type: domain.TypeTask, HasUncommittedChanges: true}},
			risks:          []bulkCleanupRisk{{taskID: "az-1", dirty: true}},
		})
		updated := result.(Model)
		if cmd != nil {
			t.Fatalf("stale bulk cleanup preflight command = %T, want nil", cmd)
		}
		if updated.pendingBulkCleanup != nil {
			t.Fatal("stale bulk cleanup preflight should not set pending cleanup")
		}
		if updated.tasks[0].Title != "Current" || updated.tasks[0].Status != domain.StatusOpen {
			t.Fatalf("stale bulk cleanup preflight applied task = %+v", updated.tasks[0])
		}
	})
}

func TestTmuxActionsDegradeOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")

	m := newTestModel()
	m.daemonClient = nil

	msg := m.attachSessionCmd("az-1")()
	errMsg, ok := msg.(sessionErrorMsg)
	if !ok {
		t.Fatalf("attachSessionCmd() returned %T, want sessionErrorMsg", msg)
	}
	if errMsg.err == nil || !strings.Contains(errMsg.err.Error(), "daemon client unavailable") {
		t.Fatalf("attachSessionCmd() error = %v, want daemon client unavailable", errMsg.err)
	}

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionAttach {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionAttach)
			}
			var body struct {
				ProjectID string `json:"project_id"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal attach request: %v", err)
			}
			if body.SessionID != "devserver-devserver-1" {
				t.Fatalf("session id = %q, want devserver-devserver-1", body.SessionID)
			}
			respBody, err := json.Marshal(struct {
				Output string `json:"output"`
			}{Output: "attached"})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	msg = m.viewDevServer("devserver-1")()
	attached, ok := msg.(sessionAttachedMsg)
	if !ok {
		t.Fatalf("viewDevServer() returned %T, want sessionAttachedMsg", msg)
	}
	if attached.issueID != "devserver-devserver-1" {
		t.Fatalf("viewDevServer() issue = %q, want devserver-devserver-1", attached.issueID)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionAttach {
		t.Fatalf("requests = %v", transport.requests)
	}

}

func TestHandleConflictResolution_ResolveWithAgentLaunchesDaemonCommand(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true
	m.tasks[0].Session = &domain.Session{IssueID: "az-1", Worktree: "/tmp/az-1"}
	m.nav.SelectTask("az-1", 0)

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionResolveConflict {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
			}
			var body protocol.SessionResolveConflictRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal resolve request: %v", err)
			}
			if body.IssueID != "az-1" || body.Worktree != "/tmp/az-1" {
				t.Fatalf("resolve request = %+v, want az-1 /tmp/az-1", body)
			}
			if len(body.ConflictFiles) != 1 || body.ConflictFiles[0] != "conflict.go" {
				t.Fatalf("conflict files = %+v, want conflict.go", body.ConflictFiles)
			}
			respBody, err := json.Marshal(protocol.SessionResolveConflictResponseBody{
				ProjectID:     "Chefy",
				IssueID:       "az-1",
				SessionID:     "Chefy-az-1",
				Worktree:      "/tmp/az-1",
				WindowName:    "resolve-conflict",
				ConflictFiles: []string{"conflict.go"},
				ReusedSession: true,
				ReusedWindow:  false,
			})
			if err != nil {
				t.Fatalf("marshal resolve response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	result, cmd := m.handleConflictResolution(overlay.ConflictResolutionMsg{
		ResolveWithAgent: true,
		IssueID:          "az-1",
		Worktree:         "/tmp/az-1",
		ConflictFiles:    []string{"conflict.go"},
	})
	if cmd == nil {
		t.Fatal("expected daemon resolve command")
	}
	_ = result.(Model)

	msg := cmd()
	resolved, ok := msg.(conflictResolveAgentResultMsg)
	if !ok {
		t.Fatalf("resolve cmd returned %T, want conflictResolveAgentResultMsg", msg)
	}
	if resolved.issueID != "az-1" || resolved.windowName != "resolve-conflict" || resolved.err != nil {
		t.Fatalf("resolve result = %+v, want az-1 resolve-conflict nil error", resolved)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionResolveConflict {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestConflictDialogResolveWithAgentUsesMergeResultContext(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true
	m.nav.SelectTask("az-1", 0)

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionResolveConflict {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
			}
			var body protocol.SessionResolveConflictRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal resolve request: %v", err)
			}
			if body.IssueID != "az-2" || body.Worktree != "/tmp/az-2" {
				t.Fatalf("resolve body = %+v, want issue az-2 worktree /tmp/az-2", body)
			}
			if len(body.ConflictFiles) != 1 || body.ConflictFiles[0] != "conflict.go" {
				t.Fatalf("conflict files = %+v, want conflict.go", body.ConflictFiles)
			}
			respBody, err := json.Marshal(protocol.SessionResolveConflictResponseBody{
				ProjectID:     "Chefy",
				IssueID:       "az-2",
				SessionID:     "Chefy-az-2",
				Worktree:      "/tmp/az-2",
				WindowName:    "resolve-conflict",
				ConflictFiles: []string{"conflict.go"},
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	updatedAny, _ := m.Update(fetchAndMergeResultMsg{
		issueID:  "az-2",
		worktree: "/tmp/az-2",
		result: &git.MergeResult{
			HasConflicts:  true,
			ConflictFiles: []string{"conflict.go"},
		},
	})
	updated := updatedAny.(Model)
	current, ok := updated.overlayStack.Current().(*overlay.ConflictOverlay)
	if !ok {
		t.Fatalf("overlay = %T, want ConflictOverlay", updated.overlayStack.Current())
	}

	_, selectCmd := current.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if selectCmd == nil {
		t.Fatal("expected selection command")
	}
	selectMsg := selectCmd()
	selected, ok := selectMsg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("selection message = %T, want SelectionMsg", selectMsg)
	}
	if selected.Key != "agent" {
		t.Fatalf("selection key = %q, want agent", selected.Key)
	}
	resolution, ok := selected.Value.(overlay.ConflictResolutionMsg)
	if !ok {
		t.Fatalf("selection value = %T, want ConflictResolutionMsg", selected.Value)
	}
	if !resolution.ResolveWithAgent {
		t.Fatal("expected resolve-with-agent selection")
	}
	if resolution.IssueID != "az-2" {
		t.Fatalf("resolution issue id = %q, want az-2", resolution.IssueID)
	}
	if resolution.Worktree != "/tmp/az-2" {
		t.Fatalf("resolution worktree = %q, want /tmp/az-2", resolution.Worktree)
	}
	if len(resolution.ConflictFiles) != 1 || resolution.ConflictFiles[0] != "conflict.go" {
		t.Fatalf("resolution conflict files = %+v, want conflict.go", resolution.ConflictFiles)
	}
	nextAny, cmd := updated.Update(selected)
	if cmd == nil {
		t.Fatal("expected daemon resolve command from conflict resolution")
	}
	_ = nextAny.(Model)

	msg := cmd()
	resolved, ok := msg.(conflictResolveAgentResultMsg)
	if !ok {
		t.Fatalf("resolve command returned %T, want conflictResolveAgentResultMsg", msg)
	}
	if resolved.issueID != "az-2" || resolved.worktree != "/tmp/az-2" || resolved.windowName != "resolve-conflict" {
		t.Fatalf("resolve result = %+v, want az-2 /tmp/az-2 resolve-conflict", resolved)
	}
}

func TestMergePreflightAgentSelectionLaunchesPreflightPrompt(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionResolveConflict {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
			}
			var body protocol.SessionResolveConflictRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal resolve request: %v", err)
			}
			if body.IssueID != "az-1" || body.Worktree != "/tmp/az-1" {
				t.Fatalf("resolve body = %+v, want issue az-1 worktree /tmp/az-1", body)
			}
			if !strings.Contains(body.Prompt, "Auto-merge the blocked preflight for az-1 -> base") {
				t.Fatalf("prompt = %q, want preflight merge context", body.Prompt)
			}
			if !strings.Contains(body.Prompt, "merge main into az/az-1") {
				t.Fatalf("prompt = %q, want base-into-source instruction", body.Prompt)
			}
			if len(body.ConflictFiles) != 1 || body.ConflictFiles[0] != "conflict.go" {
				t.Fatalf("conflict files = %+v, want conflict.go", body.ConflictFiles)
			}
			respBody, err := json.Marshal(protocol.SessionResolveConflictResponseBody{
				ProjectID:     "Chefy",
				IssueID:       "az-1",
				SessionID:     "Chefy-az-1",
				Worktree:      "/tmp/az-1",
				WindowName:    "resolve-conflict",
				ConflictFiles: []string{"conflict.go"},
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	_, cmd := m.handleSelection(overlay.SelectionMsg{
		Key: "merge_preflight_agent",
		Value: overlay.MergePreflightAgentSelection{
			SourceID:       "az-1",
			TargetID:       "base",
			SourceWorktree: "/tmp/az-1",
			TargetWorktree: "/repo",
			TargetRef:      "main",
			SourceBranch:   "az/az-1",
			ConflictFiles:  []string{"conflict.go"},
		},
	})
	if cmd == nil {
		t.Fatal("expected resolve command")
	}
	msg := cmd()
	resolved, ok := msg.(conflictResolveAgentResultMsg)
	if !ok {
		t.Fatalf("resolve command returned %T, want conflictResolveAgentResultMsg", msg)
	}
	if resolved.issueID != "az-1" || resolved.worktree != "/tmp/az-1" || resolved.windowName != "resolve-conflict" {
		t.Fatalf("resolve result = %+v, want az-1 /tmp/az-1 resolve-conflict", resolved)
	}
}

func TestHandleConflictResolution_ResolveWithAgentDaemonUnavailableFallsBackToManualHint(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.tmuxAvailable = true
	m.daemonClient = nil
	m.tasks[0].Session = &domain.Session{IssueID: "az-1", Worktree: "/tmp/az-1"}
	m.nav.SelectTask("az-1", 0)

	result, cmd := m.handleConflictResolution(overlay.ConflictResolutionMsg{ResolveWithAgent: true})
	if cmd != nil {
		t.Fatal("expected no attach command when daemon is unavailable")
	}
	newModel := result.(Model)
	if len(newModel.toasts) == 0 {
		t.Fatal("expected warning toast when daemon is unavailable")
	}
	lastToast := newModel.toasts[len(newModel.toasts)-1]
	if lastToast.Level != ToastWarning {
		t.Fatalf("toast level = %v, want warning", lastToast.Level)
	}
	if !strings.Contains(lastToast.Message, "Daemon unavailable") {
		t.Fatalf("toast message = %q, want daemon unavailable guidance", lastToast.Message)
	}
	if !strings.Contains(lastToast.Message, "tmux attach-session -t az-1") {
		t.Fatalf("toast message = %q, want manual attach command", lastToast.Message)
	}
}

func TestResolveConflictWithAgentCmd_DaemonFailureReturnsResultError(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionResolveConflict {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
			}
			return protocol.ResponseEnvelope{}, fmt.Errorf("daemon offline")
		},
	}
	m.daemonClient = daemonclient.New(transport)

	msg := m.resolveConflictWithAgentCmd("az-1", "/tmp/az-1", []string{"conflict.go"})()
	result, ok := msg.(conflictResolveAgentResultMsg)
	if !ok {
		t.Fatalf("resolveConflictWithAgentCmd returned %T, want conflictResolveAgentResultMsg", msg)
	}
	if result.issueID != "az-1" {
		t.Fatalf("result issue id = %q, want az-1", result.issueID)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "daemon offline") {
		t.Fatalf("result err = %v, want daemon offline", result.err)
	}

	updated, _ := m.Update(result)
	newModel := updated.(Model)
	if len(newModel.toasts) == 0 {
		t.Fatal("expected error toast for daemon failure")
	}
	lastToast := newModel.toasts[len(newModel.toasts)-1]
	if lastToast.Level != ToastError {
		t.Fatalf("toast level = %v, want error", lastToast.Level)
	}
	if !strings.Contains(lastToast.Message, "daemon offline") {
		t.Fatalf("toast message = %q, want daemon offline", lastToast.Message)
	}
}

func TestResolveConflictWithAgentCmd_PendingOperationMarksTask(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionResolveConflict {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
			}
			body := []byte(`{"operation_id":"op-conflict","state":"running"}`)
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            body,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	msg := m.resolveConflictWithAgentCmd("az-1", "/tmp/az-1", []string{"conflict.go"})()
	started, ok := msg.(conflictResolveAgentResultMsg)
	if !ok {
		t.Fatalf("resolveConflictWithAgentCmd returned %T, want conflictResolveAgentResultMsg", msg)
	}
	if started.operationID != "op-conflict" || started.state != protocol.OperationStateRunning {
		t.Fatalf("pending result = %+v, want op-conflict running", started)
	}

	updatedAny, refreshCmd := m.Update(started)
	updated := updatedAny.(Model)
	if refreshCmd == nil {
		t.Fatal("expected refresh command after pending conflict operation")
	}
	progress := updated.pendingMutationForTask("az-1")
	if progress == nil || progress.OperationID != "op-conflict" || progress.State != string(protocol.OperationStateRunning) {
		t.Fatalf("pending progress = %+v, want op-conflict running", progress)
	}
}

func TestAttachSessionCmd_SwitchesTmuxClientWhenAvailable(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			respBody, err := json.Marshal(struct {
				Output string `json:"output"`
			}{Output: "attached"})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	var commands [][]string
	m.tmuxClient = mockTmuxService{
		switchFn: func(_ context.Context, target string) error {
			commands = append(commands, []string{"tmux", "switch-client", "-t", target})
			if len(commands) == 1 {
				return fmt.Errorf("switch failed")
			}
			return nil
		},
	}

	msg := m.attachSessionCmd("em")()
	attached, ok := msg.(sessionAttachedMsg)
	if !ok {
		t.Fatalf("attachSessionCmd returned %T, want sessionAttachedMsg", msg)
	}
	if !attached.switchedTmux {
		t.Fatal("expected tmux client to switch")
	}
	if len(commands) < 2 {
		t.Fatalf("expected fallback switch-client attempts, got %v", commands)
	}
	if got, want := strings.Join(commands[0], " "), "tmux switch-client -t em"; got != want {
		t.Fatalf("first switch command = %q, want %q", got, want)
	}
	if got, want := strings.Join(commands[1], " "), "tmux switch-client -t ch-em"; got != want {
		t.Fatalf("second switch command = %q, want %q", got, want)
	}
}

func TestAttachSessionCmd_SwitchesTmuxBeforeDaemonLifecycleSync(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true

	switched := false
	m.tmuxClient = mockTmuxService{
		switchFn: func(_ context.Context, target string) error {
			if target != "az-1" {
				t.Fatalf("switch target = %q, want az-1", target)
			}
			switched = true
			return nil
		},
	}

	switchedBeforeSync := false
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionAttach {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionAttach)
			}
			switchedBeforeSync = switched
			respBody, err := json.Marshal(struct {
				Output string `json:"output"`
			}{Output: "attached"})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	msg := m.attachSessionCmd("az-1")()
	attached, ok := msg.(sessionAttachedMsg)
	if !ok {
		t.Fatalf("attachSessionCmd returned %T, want sessionAttachedMsg", msg)
	}
	if !attached.switchedTmux {
		t.Fatal("expected tmux client to switch")
	}
	if !switchedBeforeSync {
		t.Fatal("daemon lifecycle sync ran before tmux client switch")
	}
}

func TestHandleSelection_AttachUsesTmuxPresenceWithoutSessionProjection(t *testing.T) {
	t.Setenv("TMUX", "")
	m := newTestModel()
	m.tasks[0].HasTmuxSession = true
	m.tasks[0].Session = nil
	m.tmuxAvailable = false
	m.nav.SelectTask(m.tasks[0].ID.String(), 0)

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{
					ProjectID: "proj-test",
					Worktrees: []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					}{{
						Path:    "/tmp/wt-az-1",
						Branch:  "riordan/az-1/topic",
						IssueID: m.tasks[0].ID.String(),
					}},
				})
				if err != nil {
					t.Fatalf("marshal worktree list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandSessionAttach:
				respBody, err := json.Marshal(struct {
					Output string `json:"output"`
				}{Output: "attached"})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			default:
				t.Fatalf("command = %q, want one of %q/%q", req.Command, daemonclient.CommandWorktreeList, daemonclient.CommandSessionAttach)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	_, cmd := m.handleSelection(overlay.SelectionMsg{Key: "a"})
	if cmd == nil {
		t.Fatal("expected attach command")
	}
	msg := cmd()
	if _, ok := msg.(sessionAttachedMsg); !ok {
		t.Fatalf("attach command returned %T, want sessionAttachedMsg", msg)
	}
}

func TestDaemonSessionUpdatedEventAllowsImmediateAttachFromWorkspace(t *testing.T) {
	t.Setenv("TMUX", "")
	m := newTestModel()
	issueID := m.tasks[0].ID
	m.tasks[0].Session = nil
	m.tmuxAvailable = false
	m.nav.SelectTask(issueID.String(), 0)

	updatedAt := time.Date(2026, time.March, 31, 1, 2, 3, 0, time.UTC)
	body, err := json.Marshal(protocol.SessionProjectionEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Revision:  1,
		Session: protocol.SessionProjection{
			SessionID: "proj-az-1",
			IssueID:   naming.IssueID(issueID),
			State:     protocol.SessionLifecycleStateAttached,
			UpdatedAt: updatedAt,
		},
	})
	if err != nil {
		t.Fatalf("marshal session projection event: %v", err)
	}

	updatedAny, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			Revision: 1,
			Event:    protocol.EventSessionUpdated,
			Body:     body,
		},
	})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want app.Model", updatedAny)
	}
	session := updated.tasks[0].Session
	if session == nil {
		t.Fatal("expected authoritative session event to hydrate task session")
	}
	if session.State != domain.SessionBusy {
		t.Fatalf("session state = %q, want %q", session.State, domain.SessionBusy)
	}
	if session.StartedAt == nil || !session.StartedAt.Equal(updatedAt) {
		t.Fatalf("session startedAt = %+v, want %v", session.StartedAt, updatedAt)
	}

	_, cmd := updated.handleSelection(overlay.SelectionMsg{Key: "a"})
	if cmd == nil {
		t.Fatal("expected attach command after session start")
	}
	msg := cmd()
	if _, ok := msg.(sessionAttachedMsg); !ok {
		if _, attachErr := msg.(sessionErrorMsg); !attachErr {
			t.Fatalf("attach cmd returned %T, want sessionAttachedMsg or sessionErrorMsg", msg)
		}
	}
}

func TestDevServerResultUpdatesOpenOverlay(t *testing.T) {
	m := newTestModel()
	m.overlayStack.Push(overlay.NewDevServerOverlay(
		[]overlay.DevServerInfo{{
			ID:     "az-1",
			Name:   "web",
			Port:   3000,
			Status: "stopped",
		}},
		"az-1",
		nil,
		nil,
		nil,
		nil,
	))

	updatedAny, _ := m.Update(devServerResultMsg{
		issueID: "az-1",
		server: overlay.DevServerInfo{
			ID:     "az-1",
			Name:   "web",
			Port:   3000,
			Status: "running",
			Uptime: 2 * time.Minute,
		},
	})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}

	current, ok := updated.overlayStack.Current().(*overlay.DevServerOverlay)
	if !ok {
		t.Fatalf("current overlay = %T, want DevServerOverlay", updated.overlayStack.Current())
	}
	view := current.View()
	if !strings.Contains(view, "●") || !strings.Contains(view, "2m") {
		t.Fatalf("devserver overlay view = %q, want running status and uptime", view)
	}
}

func TestHandleSelectionVOpensDevServerOverlay(t *testing.T) {
	m := newTestModel()
	task := m.tasks[0]
	m.nav.SelectTask(task.ID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(task, nil, nil, 120, 30))

	updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "V"})
	if cmd == nil {
		t.Fatal("expected dev server overlay command")
	}
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if _, ok := updated.overlayStack.Current().(*overlay.DevServerOverlay); !ok {
		t.Fatalf("current overlay = %T, want DevServerOverlay", updated.overlayStack.Current())
	}
}

func TestHandleSelection_AttachFromTaskWorkspaceKeepsOverlayOpen(t *testing.T) {
	m := newTestModel()
	task := m.tasks[0]
	task.HasTmuxSession = true
	task.Session = nil
	m.tasks[0] = task
	m.nav.SelectTask(task.ID.String(), 0)

	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(task, nil, nil, 120, 30))

	updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "a"})
	if cmd == nil {
		t.Fatal("expected attach command")
	}
	updated := updatedAny.(Model)

	current := updated.overlayStack.Current()
	if _, ok := current.(*overlay.TaskWorkspaceOverlay); !ok {
		t.Fatalf("expected task workspace overlay to remain open, got %T", current)
	}
}

func TestHandleSelection_AttachFromNonWorkspaceClosesOverlay(t *testing.T) {
	m := newTestModel()
	task := m.tasks[0]
	task.HasTmuxSession = true
	task.Session = nil
	m.tasks[0] = task
	m.nav.SelectTask(task.ID.String(), 0)

	m.overlayStack.Push(overlay.NewActionMenu(task, nil))

	updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "a"})
	if cmd == nil {
		t.Fatal("expected attach command")
	}
	updated := updatedAny.(Model)

	if !updated.overlayStack.IsEmpty() {
		t.Fatalf("expected non-workspace overlay to close, got %T", updated.overlayStack.Current())
	}
}

func TestDaemonStreamEventMsg_IgnoresDifferentProject(t *testing.T) {
	m := newTestModel()
	m.currentProject = "chefy"
	beforeEvents := len(m.runtimeEvents)
	beforeRevision := m.daemonRevision

	next, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			ProjectID: "az",
			Revision:  99,
			Event:     "task.updated",
		},
	})
	updated := next.(Model)

	if len(updated.runtimeEvents) != beforeEvents {
		t.Fatalf("runtimeEvents len = %d, want %d (cross-project event ignored)", len(updated.runtimeEvents), beforeEvents)
	}
	if updated.daemonRevision != beforeRevision {
		t.Fatalf("daemonRevision = %d, want %d (cross-project event ignored)", updated.daemonRevision, beforeRevision)
	}
}

func TestDaemonStreamEventMsg_GitStatusEventAppliesRuntimeProjectionDirectly(t *testing.T) {
	m := newTestModel()
	task := m.tasks[0]
	startedAt := time.Date(2026, time.April, 1, 13, 0, 0, 0, time.UTC)
	updatedAt := startedAt.Add(5 * time.Minute)
	body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		IssueID:   naming.IssueID(task.ID),
		Worktree:  "/tmp/az-1",
		UpdatedAt: updatedAt,
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  1,
			Projection: protocol.RuntimeProjection{
				ProjectID: naming.ProjectID(m.daemonProjectID()),
				IssueID:   naming.IssueID(task.ID),
				Worktree: protocol.RuntimeWorktreeProjection{
					Exists:             true,
					Path:               "/tmp/az-1",
					Branch:             "riordan/az-1/task",
					Healthy:            true,
					GitStatusUpdatedAt: &updatedAt,
				},
				Git: protocol.RuntimeGitProjection{
					HasUncommittedChanges: true,
					HasConflicts:          true,
					ConflictFiles:         []string{"conflict.go"},
					GitAdditions:          3,
					GitDeletions:          1,
					GitAheadCount:         2,
					GitBehindCount:        1,
					ActiveOperation: &protocol.RuntimeOperationProjection{
						OperationID:     "op-az-1",
						State:           protocol.OperationStateRunning,
						ProgressPercent: 40,
					},
				},
				Session: protocol.RuntimeSessionProjection{
					HasSession: true,
					SessionID:  "sess-az-1",
					State:      protocol.SessionLifecycleStateAttached,
					StartedAt:  &startedAt,
					UpdatedAt:  &updatedAt,
					Worktree:   "/tmp/az-1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal runtime projection event: %v", err)
	}

	next, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  1,
			Event:     protocol.EventGitStatusUpdated,
			Body:      body,
		},
	})
	updated := next.(Model)

	got := updated.tasks[0]
	if !got.HasWorktree || got.GitAheadCount != 2 || got.GitBehindCount != 1 {
		t.Fatalf("task runtime projection = %+v, want worktree+ahead/behind", got)
	}
	if !got.HasUncommittedChanges || got.GitAdditions != 3 || got.GitDeletions != 1 {
		t.Fatalf("task git runtime = %+v, want dirty diff stat", got)
	}
	if !got.HasConflicts || len(got.ConflictFiles) != 1 || got.ConflictFiles[0] != "conflict.go" {
		t.Fatalf("task conflicts = %+v, want conflict.go", got.ConflictFiles)
	}
	if got.Session == nil || got.Session.Worktree != "/tmp/az-1" || got.Session.State != domain.SessionBusy {
		t.Fatalf("task session = %+v, want busy session with runtime worktree", got.Session)
	}
	if session := updated.sessions[got.ID.String()]; session == nil || session.Worktree != "/tmp/az-1" {
		t.Fatalf("projected session index = %+v, want /tmp/az-1", session)
	}
	if updated.daemonRevision != 1 {
		t.Fatalf("daemonRevision = %d, want 1", updated.daemonRevision)
	}
}

func TestDaemonStreamEventMsg_PausedLifecycleIgnoresWorkingAgentProjection(t *testing.T) {
	m := newTestModel()
	task := m.tasks[0]
	startedAt := time.Date(2026, time.April, 1, 13, 0, 0, 0, time.UTC)
	updatedAt := startedAt.Add(5 * time.Minute)
	body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		IssueID:   naming.IssueID(task.ID),
		Worktree:  "/tmp/az-1",
		UpdatedAt: updatedAt,
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  1,
			Projection: protocol.RuntimeProjection{
				ProjectID: naming.ProjectID(m.daemonProjectID()),
				IssueID:   naming.IssueID(task.ID),
				Worktree: protocol.RuntimeWorktreeProjection{
					Exists:  true,
					Path:    "/tmp/az-1",
					Branch:  "riordan/az-1/task",
					Healthy: true,
				},
				Git: protocol.RuntimeGitProjection{
					ActiveOperation: &protocol.RuntimeOperationProjection{
						OperationID: "op-merge",
						State:       protocol.OperationStateRunning,
					},
				},
				Session: protocol.RuntimeSessionProjection{
					HasSession: true,
					SessionID:  "sess-az-1",
					State:      protocol.SessionLifecycleStatePaused,
					StartedAt:  &startedAt,
					UpdatedAt:  &updatedAt,
					Worktree:   "/tmp/az-1",
				},
				Agent: protocol.RuntimeAgentProjection{
					Status:    "working",
					SessionID: "sess-az-1",
					UpdatedAt: &updatedAt,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal runtime projection event: %v", err)
	}

	next, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  1,
			Event:     protocol.EventGitStatusUpdated,
			Body:      body,
		},
	})
	updated := next.(Model)

	got := updated.tasks[0]
	if got.Session == nil || got.Session.State != domain.SessionPaused {
		t.Fatalf("task session = %+v, want paused despite working agent projection", got.Session)
	}
	runtime := updated.runtimeSignalsForBoard()[got.ID.String()]
	if runtime.PendingOperationID != "op-merge" || runtime.PendingOperationState != string(protocol.OperationStateRunning) {
		t.Fatalf("runtime operation = %+v, want running op-merge", runtime)
	}
}

func TestDaemonStreamUICommandOpensTaskWorkspace(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 1
	m.nav.SelectTask("az-1", 0)
	body, err := json.Marshal(protocol.UICommandEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		IssueID:   "az-3",
		Command:   protocol.UICommandOpenTaskWorkspace,
		RequestID: "req-ui-open",
		CreatedAt: time.Date(2026, time.May, 5, 15, 45, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal ui command body: %v", err)
	}

	updatedAny, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  2,
			Event:     protocol.EventUICommandRequested,
			Body:      body,
		},
	})
	updated := updatedAny.(Model)

	current := updated.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay, got %T", current)
	}
	if workspace.TaskID() != "az-3" {
		t.Fatalf("workspace task = %q, want az-3", workspace.TaskID())
	}
	if updated.daemonRevision != 2 {
		t.Fatalf("daemon revision = %d, want 2", updated.daemonRevision)
	}
}

func TestDaemonStreamUICommandOpensTaskDrillDown(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 1
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	m.tasks = []domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusOpen},
		{ID: childID, Title: "Child", Status: domain.StatusInProgress, ParentID: &parentID},
	}
	body, err := json.Marshal(protocol.UICommandEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		IssueID:   parentID,
		Command:   protocol.UICommandOpenTaskDrillDown,
		RequestID: "req-ui-drill",
		CreatedAt: time.Date(2026, time.May, 5, 15, 45, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal ui command body: %v", err)
	}

	updatedAny, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  2,
			Event:     protocol.EventUICommandRequested,
			Body:      body,
		},
	})
	updated := updatedAny.(Model)

	if got := updated.drillDownParentID; got != parentID.String() {
		t.Fatalf("drillDownParentID = %q, want %q", got, parentID)
	}
	if current := updated.overlayStack.Current(); current != nil {
		t.Fatalf("expected no overlay after drill-down command, got %T", current)
	}
}

func TestDaemonStreamUICommandIgnoresUnknownCommand(t *testing.T) {
	m := newTestModel()
	body, err := json.Marshal(protocol.UICommandEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		IssueID:   "az-3",
		Command:   "ui.unknown",
	})
	if err != nil {
		t.Fatalf("marshal ui command body: %v", err)
	}

	updatedAny, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  1,
			Event:     protocol.EventUICommandRequested,
			Body:      body,
		},
	})
	updated := updatedAny.(Model)

	if current := updated.overlayStack.Current(); current != nil {
		t.Fatalf("expected no overlay for unknown command, got %T", current)
	}
	if updated.daemonRevision != 1 {
		t.Fatalf("daemon revision = %d, want 1", updated.daemonRevision)
	}
}

func TestRuntimeSignalsForBoardUsesTaskProjectionFields(t *testing.T) {
	m := newTestModel()
	startedAt := time.Date(2026, time.April, 1, 14, 0, 0, 0, time.UTC)
	m.tasks = []domain.Task{
		{
			ID:                    "az-1",
			Title:                 "Task",
			Status:                domain.StatusOpen,
			Priority:              domain.P2,
			Type:                  domain.TypeTask,
			HasTmuxSession:        true,
			HasWorktree:           true,
			GitAheadCount:         4,
			GitBehindCount:        1,
			HasUncommittedChanges: true,
			HasConflicts:          true,
			ConflictFiles:         []string{"conflict.go"},
			GitAdditions:          9,
			GitDeletions:          2,
			Session: &domain.Session{
				IssueID:   "az-1",
				State:     domain.SessionBusy,
				StartedAt: &startedAt,
				Worktree:  "/tmp/az-1",
			},
		},
	}

	signals := m.runtimeSignalsForBoard()
	got, ok := signals["az-1"]
	if !ok {
		t.Fatal("expected runtime signals for az-1")
	}
	if !got.HasTmuxSession || !got.HasWorktree || got.GitAheadCount != 4 || got.GitBehindCount != 1 {
		t.Fatalf("runtime signals = %+v, want task-projection fields", got)
	}
	if !got.HasUncommittedChanges || got.GitAdditions != 9 || got.GitDeletions != 2 {
		t.Fatalf("runtime signals = %+v, want git projection fields", got)
	}
	if !got.HasConflicts || len(got.ConflictFiles) != 1 || got.ConflictFiles[0] != "conflict.go" {
		t.Fatalf("runtime signals = %+v, want conflict projection fields", got)
	}
}

func TestRuntimeSignalsForBoardIncludesPendingMutationState(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.runtimeSignalsByTask = map[string]board.RuntimeSignals{
		"az-1": {HasWorktree: true},
	}
	m.pendingStatuses = map[string]pendingTaskStatus{
		taskIDKey("az-1"): {
			previousStatus: domain.StatusOpen,
			targetStatus:   domain.StatusInProgress,
			operationID:    "op-status",
			state:          protocol.OperationStateQueued,
		},
	}

	signals := m.runtimeSignalsForBoard()
	got, ok := signals["az-1"]
	if !ok {
		t.Fatal("expected runtime signals for az-1")
	}
	if got.PendingOperationState != string(protocol.OperationStateQueued) {
		t.Fatalf("pending state = %q, want %q", got.PendingOperationState, protocol.OperationStateQueued)
	}
	if got.PendingOperationID != "op-status" {
		t.Fatalf("pending operation id = %q, want %q", got.PendingOperationID, "op-status")
	}
}

func TestPendingMutationForTaskFallsBackToRuntimeSignals(t *testing.T) {
	m := newTestModel()
	m.runtimeSignalsByTask = map[string]board.RuntimeSignals{
		"az-1": {
			PendingOperationState:   "running",
			PendingOperationID:      "op-runtime",
			PendingOperationPercent: 33,
		},
	}

	progress := m.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected pending mutation progress from runtime signals")
	}
	if progress.OperationID != "op-runtime" {
		t.Fatalf("operation id = %q, want %q", progress.OperationID, "op-runtime")
	}
	if progress.State != "running" {
		t.Fatalf("state = %q, want %q", progress.State, "running")
	}
	if progress.ProgressPercent != 33 {
		t.Fatalf("progress percent = %d, want 33", progress.ProgressPercent)
	}
}

func TestRuntimeProjectionStreamSyncsOpenTaskWorkspaceOverlay(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 1
	task := m.tasks[0]
	task.HasWorktree = false
	task.HasUncommittedChanges = false
	task.GitAdditions = 0
	task.GitDeletions = 0
	m.tasks[0] = task
	m.nav.SelectTask(task.ID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(task, m.tasks, nil, 120, 30))

	updatedAt := time.Date(2026, time.April, 1, 15, 0, 0, 0, time.UTC)
	body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		IssueID:   task.ID,
		Worktree:  "/tmp/wt-az-1",
		UpdatedAt: updatedAt,
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  2,
			Projection: protocol.RuntimeProjection{
				ProjectID: naming.ProjectID(m.daemonProjectID()),
				IssueID:   naming.IssueID(task.ID),
				Worktree: protocol.RuntimeWorktreeProjection{
					Exists:             true,
					Path:               "/tmp/wt-az-1",
					Branch:             "riordan/az-1/task",
					Healthy:            true,
					GitStatusUpdatedAt: &updatedAt,
				},
				Git: protocol.RuntimeGitProjection{
					HasUncommittedChanges: true,
					GitAdditions:          3,
					GitDeletions:          1,
				},
				Session: protocol.RuntimeSessionProjection{
					HasSession: true,
					SessionID:  "sess-az-1",
					State:      protocol.SessionLifecycleStateAttached,
					UpdatedAt:  &updatedAt,
					Worktree:   "/tmp/wt-az-1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal runtime projection event: %v", err)
	}

	updatedAny, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  2,
			Event:     protocol.EventWorktreeProjectionUpdated,
			Body:      body,
		},
	})
	updated := updatedAny.(Model)

	current := updated.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay on stack, got %T", current)
	}
	view := workspace.View()
	if !strings.Contains(view, "Worktree:") {
		t.Fatalf("expected worktree summary row in detail panel after runtime projection, got: %q", view)
	}
	if !strings.Contains(view, "dirty (+3/-1)") {
		t.Fatalf("expected projected git status in detail panel, got: %q", view)
	}
}

func TestFormatWorktreeCleanupConfirmPromptKeepsCleanWhenOnlyBaseDiffPresent(t *testing.T) {
	checkedAt := time.Date(2026, time.April, 5, 7, 30, 0, 0, time.UTC)
	msg := worktreeCleanupConfirmPromptMsg{
		taskID:      "az-1",
		hasSnapshot: true,
		hasTask:     true,
		hasWorktree: true,
		dirty:       false,
		additions:   12,
		deletions:   4,
		ahead:       2,
		behind:      0,
		freshness:   protocol.TaskListFreshnessFresh,
		checkedAt:   checkedAt,
	}

	prompt := formatWorktreeCleanupConfirmPrompt(msg)
	if !strings.Contains(prompt, "- Changes: clean") {
		t.Fatalf("prompt missing clean changes state: %q", prompt)
	}
	if strings.Contains(prompt, "dirty (+12/-4)") {
		t.Fatalf("prompt incorrectly reported dirty from base diff counters: %q", prompt)
	}
	if !strings.Contains(prompt, "- Base diff (+/-): +12/-4") {
		t.Fatalf("prompt missing base diff summary: %q", prompt)
	}
}

func TestRuntimeSignalsForBoardMarksAncestorCardsWithChildSessions(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"
	grandchildID := "az-grandchild"
	unrelatedID := "az-unrelated"
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	grandchildIssueID := naming.IssueID(grandchildID)
	unrelatedIssueID := naming.IssueID(unrelatedID)
	startedAt := time.Now().Add(-2 * time.Minute)

	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: parentIssueID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeEpic},
		{ID: childIssueID, Title: "Child", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, ParentID: &parentIssueID},
		{
			ID:       grandchildIssueID,
			Title:    "Grandchild",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			ParentID: &childIssueID,
			Session: &domain.Session{
				IssueID:   grandchildIssueID,
				State:     domain.SessionBusy,
				StartedAt: &startedAt,
			},
		},
		{ID: unrelatedIssueID, Title: "Unrelated", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.runtimeSignalsByTask = map[string]board.RuntimeSignals{
		parentID:     {},
		childID:      {},
		grandchildID: {HasTmuxSession: true},
		unrelatedID:  {},
	}

	signals := m.runtimeSignalsForBoard()
	if !signals[parentID].HasDescendantTmuxSession {
		t.Fatalf("expected parent %q to show descendant session signal", parentID)
	}
	if !signals[childID].HasDescendantTmuxSession {
		t.Fatalf("expected child ancestor %q to show descendant session signal", childID)
	}
	if signals[unrelatedID].HasDescendantTmuxSession {
		t.Fatalf("did not expect unrelated task %q to show descendant session signal", unrelatedID)
	}
}

func TestRuntimeSignalsForBoardUsesTaskProjectionAsBaseline(t *testing.T) {
	startedAt := time.Now().Add(-5 * time.Minute)
	m := newTestModel()
	m.tasks = []domain.Task{
		{
			ID:                    "az-1",
			Title:                 "Projection task",
			Status:                domain.StatusOpen,
			Priority:              domain.P2,
			Type:                  domain.TypeTask,
			HasWorktree:           true,
			GitAheadCount:         2,
			GitBehindCount:        3,
			HasUncommittedChanges: true,
			GitAdditions:          5,
			GitDeletions:          1,
			Session: &domain.Session{
				IssueID:   "az-1",
				State:     domain.SessionBusy,
				StartedAt: &startedAt,
			},
		},
	}
	// Simulate stale runtime map values that disagree with hydrated task projection.
	m.runtimeSignalsByTask = map[string]board.RuntimeSignals{
		"az-1": {},
	}

	signals := m.runtimeSignalsForBoard()
	got := signals["az-1"]
	if !got.HasTmuxSession {
		t.Fatal("expected HasTmuxSession from task session baseline")
	}
	if !got.HasWorktree {
		t.Fatal("expected HasWorktree from task projection baseline")
	}
	if got.GitAheadCount != 2 || got.GitBehindCount != 3 {
		t.Fatalf("git ahead/behind = %d/%d, want 2/3", got.GitAheadCount, got.GitBehindCount)
	}
	if !got.HasUncommittedChanges || got.GitAdditions != 5 || got.GitDeletions != 1 {
		t.Fatalf("git change projection mismatch: %+v", got)
	}
}

func TestSortTasksInColumnSessionSortPromotesAncestorOfActiveChildSession(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"
	otherID := "az-other"
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	otherIssueID := naming.IssueID(otherID)
	startedAt := time.Now().Add(-2 * time.Minute)

	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: parentIssueID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeEpic},
		{
			ID:       childIssueID,
			Title:    "Child",
			Status:   domain.StatusInProgress,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			ParentID: &parentIssueID,
			Session: &domain.Session{
				IssueID:   childIssueID,
				State:     domain.SessionBusy,
				StartedAt: &startedAt,
			},
		},
		{ID: otherIssueID, Title: "Other", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.editor.SetSort(&domain.Sort{Field: domain.SortBySession, Order: domain.SortAsc})

	filtered := m.boardVisibleTasks(m.tasks)
	sortedOpen := m.sortTasksInColumn(filtered, domain.StatusOpen)
	if len(sortedOpen) != 2 {
		t.Fatalf("sorted open tasks len = %d, want 2", len(sortedOpen))
	}
	if sortedOpen[0].ID.String() != parentID {
		t.Fatalf("expected ancestor task %q to sort first, got %q", parentID, sortedOpen[0].ID)
	}
}

func TestPendingMutationForTaskBuildsOverlayProgress(t *testing.T) {
	m := newTestModel()
	m.pendingStatuses = map[string]pendingTaskStatus{
		taskIDKey("az-1"): {
			previousStatus: domain.StatusOpen,
			targetStatus:   domain.StatusInProgress,
			operationID:    "op-status",
			state:          protocol.OperationStateRunning,
		},
	}

	progress := m.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected pending mutation progress")
	}
	if progress.State != string(protocol.OperationStateRunning) {
		t.Fatalf("progress state = %q, want %q", progress.State, protocol.OperationStateRunning)
	}
	if progress.OperationID != "op-status" {
		t.Fatalf("progress operation id = %q, want %q", progress.OperationID, "op-status")
	}
	if progress.PreviousStatus != domain.StatusOpen || progress.TargetStatus != domain.StatusInProgress {
		t.Fatalf("progress statuses = %s->%s, want %s->%s", progress.PreviousStatus, progress.TargetStatus, domain.StatusOpen, domain.StatusInProgress)
	}
}

func TestLocalGitActivityMarkerBuildsBoardAndDetailProgress(t *testing.T) {
	m := newTestModel()
	m.markMergeOperationPreparing("az-1", mergeBaseTargetID, "preparing merge")

	signals := m.runtimeSignalsForBoard()["az-1"]
	if signals.PendingOperationID != "" {
		t.Fatalf("pending operation id = %q, want empty before daemon operation", signals.PendingOperationID)
	}
	if signals.PendingOperationState != "preparing" {
		t.Fatalf("pending operation state = %q, want preparing", signals.PendingOperationState)
	}

	progress := m.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected detail pending progress")
	}
	if progress.OperationID != "" || progress.State != "preparing" || progress.ProgressMessage != "preparing merge" {
		t.Fatalf("detail progress = %+v", progress)
	}
}

func TestRuntimeProjectionAppliesAgentActivityToSessionDisplay(t *testing.T) {
	m := newTestModel()
	startedAt := time.Date(2026, time.June, 15, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, time.June, 15, 10, 5, 0, 0, time.UTC)

	ok := m.applyRuntimeProjection(protocol.RuntimeProjection{
		ProjectID: "proj",
		IssueID:   "az-1",
		Worktree: protocol.RuntimeWorktreeProjection{
			Exists:  true,
			Path:    "/tmp/repo-az-1",
			Branch:  "riordan/az-1/task",
			Healthy: true,
		},
		Session: protocol.RuntimeSessionProjection{
			HasSession: true,
			SessionID:  "sess-1",
			State:      protocol.SessionLifecycleStateRunning,
			StartedAt:  &startedAt,
			UpdatedAt:  &updatedAt,
			Worktree:   "/tmp/repo-az-1",
		},
		Agent: protocol.RuntimeAgentProjection{
			Status:    "working",
			SessionID: "sess-1",
			UpdatedAt: &updatedAt,
		},
	})
	if !ok {
		t.Fatal("applyRuntimeProjection returned false")
	}

	session := m.tasks[0].Session
	if session == nil {
		t.Fatal("task session is nil")
	}
	if session.State != domain.SessionBusy {
		t.Fatalf("session lifecycle state = %s, want %s", session.State, domain.SessionBusy)
	}
	if session.Activity != "working" || session.ActivitySource != "agent" {
		t.Fatalf("session activity = %q/%q, want working/agent", session.Activity, session.ActivitySource)
	}
	if display, ok := session.DisplayState(); !ok || display != domain.SessionBusy {
		t.Fatalf("display state = %s/%v, want busy/true", display, ok)
	}
	if session.DisplayCode() != "B" {
		t.Fatalf("display code = %q, want B", session.DisplayCode())
	}

}

func TestRuntimeProjectionAppliesSessionSourcedNoAgentActivity(t *testing.T) {
	m := newTestModel()
	updatedAt := time.Date(2026, time.June, 15, 10, 5, 0, 0, time.UTC)

	ok := m.applyRuntimeProjection(protocol.RuntimeProjection{
		ProjectID: "proj",
		IssueID:   "az-1",
		Session: protocol.RuntimeSessionProjection{
			HasSession: true,
			SessionID:  "sess-1",
			State:      protocol.SessionLifecycleStateRunning,
			UpdatedAt:  &updatedAt,
		},
		Agent: protocol.RuntimeAgentProjection{
			Status:    "no-agent",
			Source:    "session",
			SessionID: "sess-1",
			UpdatedAt: &updatedAt,
		},
	})
	if !ok {
		t.Fatal("applyRuntimeProjection returned false")
	}

	session := m.tasks[0].Session
	if session == nil {
		t.Fatal("task session is nil")
	}
	if session.Activity != "no-agent" || session.ActivitySource != "session" {
		t.Fatalf("session activity = %q/%q, want no-agent/session", session.Activity, session.ActivitySource)
	}
	if got := session.DisplayCode(); got != "N" {
		t.Fatalf("display code = %q, want N", got)
	}
}

func TestSessionProjectionEventAppliesAgentActivityToSessionDisplay(t *testing.T) {
	m := newTestModel()
	startedAt := time.Date(2026, time.June, 15, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, time.June, 15, 10, 5, 0, 0, time.UTC)

	ok := m.applyRuntimeProjectionFromSessionEvent(protocol.SessionProjectionEventBody{
		ProjectID: "proj",
		Revision:  2,
		Session: protocol.SessionProjection{
			SessionID: "sess-1",
			IssueID:   "az-1",
			State:     protocol.SessionLifecycleStateRunning,
			UpdatedAt: updatedAt,
		},
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: "proj",
			Revision:  2,
			Projection: protocol.RuntimeProjection{
				ProjectID: "proj",
				IssueID:   "az-1",
				Session: protocol.RuntimeSessionProjection{
					HasSession: true,
					SessionID:  "sess-1",
					State:      protocol.SessionLifecycleStateRunning,
					StartedAt:  &startedAt,
					UpdatedAt:  &updatedAt,
				},
				Agent: protocol.RuntimeAgentProjection{
					Status:    "waiting",
					SessionID: "sess-1",
					UpdatedAt: &updatedAt,
				},
			},
		},
	})
	if !ok {
		t.Fatal("applyRuntimeProjectionFromSessionEvent returned false")
	}

	session := m.tasks[0].Session
	if session == nil {
		t.Fatal("task session is nil")
	}
	if session.State != domain.SessionBusy {
		t.Fatalf("session lifecycle state = %s, want %s", session.State, domain.SessionBusy)
	}
	if session.Activity != "waiting" || session.ActivitySource != "agent" {
		t.Fatalf("session activity = %q/%q, want waiting/agent", session.Activity, session.ActivitySource)
	}
	if display, ok := session.DisplayState(); !ok || display != domain.SessionWaiting {
		t.Fatalf("display state = %s/%v, want waiting/true", display, ok)
	}
	if session.DisplayCode() != "W" {
		t.Fatalf("display code = %q, want W", session.DisplayCode())
	}

}

func TestRuntimeProjectionWithoutAgentPreservesHookActivity(t *testing.T) {
	m := newTestModel()
	startedAt := time.Date(2026, time.June, 15, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, time.June, 15, 10, 5, 0, 0, time.UTC)
	m.tasks[0].Session = &domain.Session{
		IssueID:        m.tasks[0].ID,
		State:          domain.SessionBusy,
		Activity:       "idle",
		ActivitySource: "hooks",
		StartedAt:      &startedAt,
	}
	m.tasks[0].HasTmuxSession = true

	ok := m.applyRuntimeProjection(protocol.RuntimeProjection{
		ProjectID: "proj",
		IssueID:   "az-1",
		Session: protocol.RuntimeSessionProjection{
			HasSession: true,
			SessionID:  "sess-1",
			State:      protocol.SessionLifecycleStateRunning,
			StartedAt:  &startedAt,
			UpdatedAt:  &updatedAt,
		},
	})
	if !ok {
		t.Fatal("applyRuntimeProjection returned false")
	}
	session := m.tasks[0].Session
	if session == nil {
		t.Fatal("task session is nil after missing-agent projection")
	}
	if session.Activity != "idle" || session.ActivitySource != "hooks" {
		t.Fatalf("session activity after missing-agent projection = %q/%q, want idle/hooks", session.Activity, session.ActivitySource)
	}

	ok = m.applyRuntimeProjectionFromSessionEvent(protocol.SessionProjectionEventBody{
		ProjectID: "proj",
		Revision:  3,
		Session: protocol.SessionProjection{
			SessionID: "sess-1",
			IssueID:   "az-1",
			State:     protocol.SessionLifecycleStateRunning,
			UpdatedAt: updatedAt.Add(time.Minute),
		},
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: "proj",
			Revision:  3,
			Projection: protocol.RuntimeProjection{
				ProjectID: "proj",
				IssueID:   "az-1",
				Session: protocol.RuntimeSessionProjection{
					HasSession: true,
					SessionID:  "sess-1",
					State:      protocol.SessionLifecycleStateRunning,
					StartedAt:  &startedAt,
					UpdatedAt:  &updatedAt,
				},
			},
		},
	})
	if !ok {
		t.Fatal("applyRuntimeProjectionFromSessionEvent returned false")
	}
	session = m.tasks[0].Session
	if session == nil {
		t.Fatal("task session is nil after missing-agent session event")
	}
	if session.Activity != "idle" || session.ActivitySource != "hooks" {
		t.Fatalf("session activity after missing-agent session event = %q/%q, want idle/hooks", session.Activity, session.ActivitySource)
	}
}

func TestBulkStatusSummaryDetectsCloseGuardGuidance(t *testing.T) {
	tests := []struct {
		name   string
		issues []bulkTaskIssue
		want   bool
	}{
		{
			name: "close guard failure",
			issues: []bulkTaskIssue{{
				taskID: "az-1",
				reason: "cannot close issue: issue still has a worktree. Next: run az issue close --id az-1, " +
					"or clean up the worktree/session before closing.",
			}},
			want: true,
		},
		{
			name: "status repair guidance",
			issues: []bulkTaskIssue{{
				taskID: "az-2",
				reason: "Moved closed blockers back for cleanup: az-2 -> in_review.",
			}},
			want: true,
		},
		{
			name: "ordinary issue",
			issues: []bulkTaskIssue{{
				taskID: "az-3",
				reason: "permission denied",
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bulkStatusSummaryHasCloseGuardGuidance(tt.issues); got != tt.want {
				t.Fatalf("bulkStatusSummaryHasCloseGuardGuidance() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCloseCleanupBulkMovePromptDescribesClosingSubset(t *testing.T) {
	prompt := formatCloseCleanupConfirmPrompt(pendingCloseCleanupConfirmation{
		taskIDs:      []string{"az-1", "az-2", "az-3"},
		closeTaskIDs: []string{"az-3"},
		bulkMode:     "move",
		delta:        1,
		summaries: []closeCleanupTaskSummary{{
			taskID:      "az-3",
			hasWorktree: true,
			dirty:       true,
			ahead:       2,
			additions:   12,
			deletions:   4,
		}},
	})

	if !strings.Contains(prompt, "Target: 1 of 3 selected tasks") {
		t.Fatalf("prompt = %q, want closing subset count", prompt)
	}
	if !strings.Contains(prompt, "Status: moving right; closing subset will close") {
		t.Fatalf("prompt = %q, want mixed move-right status", prompt)
	}
	if !strings.Contains(prompt, "az-3: worktree, dirty (+12/-4), ↑2/↓0") {
		t.Fatalf("prompt = %q, want projected git state for closing subset", prompt)
	}
}

func TestCloseCleanupPromptSuppressesTargetOnlyWhenChildrenBlock(t *testing.T) {
	prompt := formatCloseCleanupConfirmPrompt(pendingCloseCleanupConfirmation{
		taskID:                      "az-parent",
		targetStatus:                domain.StatusDone,
		targetOnlyBlockedByChildren: true,
	})

	checks := []string{
		"Target-only close is unavailable while child issues remain unresolved.",
		"Proceed? C closes the target plus clean children; N cancels.",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("prompt = %q, want %q", prompt, check)
		}
	}
	if strings.Contains(prompt, "Y closes only the target") {
		t.Fatalf("prompt = %q, should not offer target-only close", prompt)
	}
}

func TestCloseCleanupTargetsHaveBlockingDescendants(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	tests := []struct {
		name    string
		tasks   []domain.Task
		targets []string
		want    bool
	}{
		{
			name: "unresolved child blocks target only",
			tasks: []domain.Task{
				{ID: parentID, Status: domain.StatusInReview},
				{ID: "az-child", ParentID: &parentID, Status: domain.StatusOpen},
			},
			targets: []string{"az-parent"},
			want:    true,
		},
		{
			name: "selected child does not block bulk target only",
			tasks: []domain.Task{
				{ID: parentID, Status: domain.StatusInReview},
				{ID: "az-child", ParentID: &parentID, Status: domain.StatusOpen},
			},
			targets: []string{"az-parent", "az-child"},
			want:    false,
		},
		{
			name: "done child without runtime does not block",
			tasks: []domain.Task{
				{ID: parentID, Status: domain.StatusInReview},
				{ID: "az-child", ParentID: &parentID, Status: domain.StatusDone},
			},
			targets: []string{"az-parent"},
			want:    false,
		},
		{
			name: "dependency child blocks",
			tasks: []domain.Task{
				{ID: parentID, Status: domain.StatusInReview},
				{ID: "az-child", Status: domain.StatusInProgress, Dependencies: []domain.Dependency{{ID: parentID, Type: domain.DependencyParentChild}}},
			},
			targets: []string{"az-parent"},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := closeCleanupTargetsHaveBlockingDescendants(tt.tasks, tt.targets)
			if got != tt.want {
				t.Fatalf("closeCleanupTargetsHaveBlockingDescendants() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPendingCloseCleanupBlockedReasonBlocksDirtyAndConflictedTargets(t *testing.T) {
	tests := []struct {
		name      string
		summaries []closeCleanupTaskSummary
		want      string
	}{
		{
			name:      "clean",
			summaries: []closeCleanupTaskSummary{{taskID: "az-1", ahead: 1}},
			want:      "",
		},
		{
			name:      "dirty",
			summaries: []closeCleanupTaskSummary{{taskID: "az-2", dirty: true}},
			want:      "Close blocked: clean up dirty/conflicted worktree state first (az-2 dirty)",
		},
		{
			name:      "conflicted",
			summaries: []closeCleanupTaskSummary{{taskID: "az-3", conflicted: true}},
			want:      "Close blocked: clean up dirty/conflicted worktree state first (az-3 conflicted)",
		},
		{
			name:      "dirty and conflicted",
			summaries: []closeCleanupTaskSummary{{taskID: "az-4", dirty: true, conflicted: true}},
			want:      "Close blocked: clean up dirty/conflicted worktree state first (az-4 dirty/conflicted)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pendingCloseCleanupBlockedReason(pendingCloseCleanupConfirmation{summaries: tt.summaries})
			if got != tt.want {
				t.Fatalf("pendingCloseCleanupBlockedReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPendingCloseCleanupTargetIDs(t *testing.T) {
	tests := []struct {
		name    string
		pending pendingCloseCleanupConfirmation
		want    []string
	}{
		{
			name:    "close task ids win",
			pending: pendingCloseCleanupConfirmation{taskIDs: []string{"az-1", "az-2"}, closeTaskIDs: []string{"az-2"}},
			want:    []string{"az-2"},
		},
		{
			name:    "bulk task ids",
			pending: pendingCloseCleanupConfirmation{taskIDs: []string{"az-1", "az-2"}},
			want:    []string{"az-1", "az-2"},
		},
		{
			name:    "single task id",
			pending: pendingCloseCleanupConfirmation{taskID: " az-3 "},
			want:    []string{"az-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pendingCloseCleanupTargetIDs(tt.pending)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("pendingCloseCleanupTargetIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReviewCascadeChildIDsIncludesUnreadyDescendants(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	legacyChildID := naming.IssueID("az-legacy")
	m := Model{
		tasks: []domain.Task{
			{ID: parentID, Status: domain.StatusInProgress},
			{ID: childID, Status: domain.StatusInProgress, ParentID: &parentID},
			{ID: "az-grandchild", Status: domain.StatusOpen, ParentID: &childID},
			{ID: "az-ready", Status: domain.StatusInReview, ParentID: &parentID},
			{ID: "az-closed", Status: domain.StatusDone, ParentID: &parentID},
			{ID: "az-cancelled", Status: domain.StatusCancelled, ParentID: &parentID},
			{
				ID:     legacyChildID,
				Status: domain.StatusOpen,
				Dependencies: []domain.Dependency{
					{ID: parentID, Type: domain.DependencyParentChild},
				},
			},
		},
	}

	got := m.reviewCascadeChildIDs(parentID.String())
	want := []string{"az-child", "az-grandchild", "az-legacy"}
	if !slices.Equal(got, want) {
		t.Fatalf("reviewCascadeChildIDs() = %v, want %v", got, want)
	}

	prompt := formatReviewCascadeConfirmPrompt(pendingReviewCascadeConfirmation{taskID: parentID.String(), childIDs: got})
	for _, wantText := range []string{"Parent: az-parent", "Children to move: 3", "- az-child", "- az-grandchild", "- az-legacy"} {
		if !strings.Contains(prompt, wantText) {
			t.Fatalf("prompt missing %q:\n%s", wantText, prompt)
		}
	}
}

func TestDirtyCloseConfirmationRefreshesAndClosesDialogBeforeBlocking(t *testing.T) {
	m := newTestModel()
	pending := pendingCloseCleanupConfirmation{
		taskID:         "az-4",
		previousStatus: domain.StatusInReview,
		targetStatus:   domain.StatusDone,
		summaries: []closeCleanupTaskSummary{{
			taskID: "az-4",
			dirty:  true,
		}},
	}
	m.pendingClose = &pending
	_ = m.overlayStack.Push(overlay.NewConfirmDialogExplicitYN("Confirm integrate and close?", "Proceed?"))

	updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "yes"})
	if cmd == nil {
		t.Fatal("expected close preflight refresh command")
	}
	updated := updatedAny.(Model)
	if updated.pendingClose != nil {
		t.Fatal("pending close confirmation should be cleared while refresh runs")
	}
	if !updated.overlayStack.IsEmpty() {
		t.Fatal("dirty close preflight should close stale confirmation dialog")
	}
	if len(updated.toasts) == 0 || !strings.Contains(updated.toasts[len(updated.toasts)-1].Message, "Refreshing close preflight") {
		t.Fatalf("toasts = %+v, want refresh notice", updated.toasts)
	}
}

func TestClosePreflightRefreshCleanTargetContinuesClose(t *testing.T) {
	m := newTestModel()
	pending := pendingCloseCleanupConfirmation{
		taskID:         "az-4",
		previousStatus: domain.StatusInReview,
		targetStatus:   domain.StatusDone,
		summaries: []closeCleanupTaskSummary{{
			taskID: "az-4",
			dirty:  true,
		}},
	}

	updatedAny, cmd := m.Update(closeCleanupConfirmPreflightMsg{
		pending: pending,
		summaries: []closeCleanupTaskSummary{{
			taskID: "az-4",
		}},
	})
	if cmd == nil {
		t.Fatal("expected refreshed clean close to continue to status command")
	}
	updated := updatedAny.(Model)
	if pending, ok := updated.pendingStatuses[taskIDKey("az-4")]; !ok || pending.targetStatus != domain.StatusDone {
		t.Fatalf("pending status = %+v ok=%t, want close queued", pending, ok)
	}
}

func TestClosePreflightRefreshWithChildBlockerReopensConfirmation(t *testing.T) {
	m := newTestModel()
	parentID := naming.IssueID("az-4")
	pending := pendingCloseCleanupConfirmation{
		taskID:         "az-4",
		previousStatus: domain.StatusInReview,
		targetStatus:   domain.StatusDone,
		summaries: []closeCleanupTaskSummary{{
			taskID: "az-4",
			dirty:  true,
		}},
	}

	updatedAny, cmd := m.Update(closeCleanupConfirmPreflightMsg{
		pending: pending,
		summaries: []closeCleanupTaskSummary{{
			taskID: "az-4",
		}},
		refreshedTasks: []domain.Task{
			{ID: parentID, Status: domain.StatusInReview},
			{ID: "az-child", ParentID: &parentID, Status: domain.StatusOpen},
		},
	})
	if cmd == nil {
		t.Fatal("expected refreshed child blocker to reopen close confirmation")
	}
	updated := updatedAny.(Model)
	if updated.pendingClose == nil || !updated.pendingClose.targetOnlyBlockedByChildren {
		t.Fatalf("pending close = %+v, want target-only blocked confirmation", updated.pendingClose)
	}
	if _, ok := updated.pendingStatuses[taskIDKey("az-4")]; ok {
		t.Fatal("target-only close should not be queued after refreshed child blocker")
	}
	if len(updated.toasts) == 0 || !strings.Contains(updated.toasts[len(updated.toasts)-1].Message, "Target-only close is blocked") {
		t.Fatalf("toasts = %+v, want target-only blocked warning", updated.toasts)
	}
}

func TestCloseCleanupPromptShowsSingleTaskGitState(t *testing.T) {
	prompt := formatCloseCleanupConfirmPrompt(pendingCloseCleanupConfirmation{
		taskID:       "az-4",
		targetStatus: domain.StatusDone,
		summaries: []closeCleanupTaskSummary{{
			taskID:      "az-4",
			hasWorktree: true,
			hasSession:  true,
			dirty:       true,
			conflicted:  true,
			conflicts:   []string{"internal/tui/model.go", "README.md", "go.mod", "go.sum"},
			ahead:       1,
			behind:      2,
			additions:   9,
			deletions:   3,
		}},
	})

	checks := []string{
		"Git state (current board projection):",
		"- Worktree: present",
		"- Session: present",
		"- Changes: dirty (+9/-3)",
		"- Base diff (+/-): +9/-3",
		"- Ahead/Behind: ↑1/↓2",
		"- Conflicts: internal/tui/model.go, README.md, go.mod, ... 1 more",
	}
	for _, want := range checks {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want %q", prompt, want)
		}
	}
}

func TestCloseCleanupSummariesUseProjectedTaskState(t *testing.T) {
	m := Model{
		tasks: []domain.Task{
			{
				ID:                    "az-1",
				HasWorktree:           true,
				HasTmuxSession:        true,
				HasUncommittedChanges: true,
				GitAheadCount:         3,
				GitBehindCount:        1,
				GitAdditions:          7,
				GitDeletions:          2,
			},
			{ID: "az-2"},
		},
	}

	summaries := m.closeCleanupSummaries([]string{"az-1", "missing"})
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v, want one projected task summary", summaries)
	}
	got := summaries[0]
	if got.taskID != "az-1" || !got.hasWorktree || !got.hasSession || !got.dirty ||
		got.ahead != 3 || got.behind != 1 || got.additions != 7 || got.deletions != 2 {
		t.Fatalf("summary = %+v, want projected git/session state", got)
	}
}

func TestHandleSelectionSessionMutationsShowImmediatePendingFeedback(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantAction string
		wantToast  string
	}{
		{name: "start work", key: "s", wantAction: "session_start", wantToast: "AI session start queued for az-1"},
		{name: "start tmux only", key: "t", wantAction: "session_start", wantToast: "Tmux shell start queued for az-1"},
		{name: "start work legacy shortcut", key: "S", wantAction: "session_start", wantToast: "AI session start queued for az-1"},
		{name: "start yolo", key: "!", wantAction: "session_start", wantToast: "AI session start (yolo) queued for az-1"},
		{name: "stop", key: "x", wantAction: "session_stop", wantToast: "Session stop queued for az-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.nav.SelectTask("az-1", 0)

			updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: tt.key})
			if cmd == nil {
				t.Fatal("expected session mutation command")
			}
			updated := updatedAny.(Model)

			pending, ok := updated.pendingStatuses[taskIDKey("az-1")]
			if !ok {
				t.Fatal("expected immediate pending mutation marker")
			}
			if pending.action != tt.wantAction {
				t.Fatalf("pending action = %q, want %q", pending.action, tt.wantAction)
			}
			if pending.state != protocol.OperationStateQueued {
				t.Fatalf("pending state = %q, want %q", pending.state, protocol.OperationStateQueued)
			}
			if pending.operationID != "" {
				t.Fatalf("pending operation id = %q, want empty before daemon response", pending.operationID)
			}

			signals := updated.runtimeSignalsForBoard()
			if got := signals["az-1"].PendingOperationState; got != string(protocol.OperationStateQueued) {
				t.Fatalf("board pending state = %q, want %q", got, protocol.OperationStateQueued)
			}
			progress := updated.pendingMutationForTask("az-1")
			if progress == nil || progress.State != string(protocol.OperationStateQueued) {
				t.Fatalf("pending mutation progress = %+v, want queued", progress)
			}
			if len(updated.toasts) == 0 {
				t.Fatal("expected immediate feedback toast")
			}
			if got := updated.toasts[len(updated.toasts)-1].Message; got != tt.wantToast {
				t.Fatalf("toast = %q, want %q", got, tt.wantToast)
			}
			if tt.key == "u" || tt.key == "m" {
				signals := updated.runtimeSignalsForBoard()["az-1"]
				if signals.PendingOperationState != "preparing" {
					t.Fatalf("board pending state = %q, want preparing", signals.PendingOperationState)
				}
				progress := updated.pendingMutationForTask("az-1")
				if progress == nil || progress.State != "preparing" || strings.TrimSpace(progress.ProgressMessage) == "" {
					t.Fatalf("detail progress = %+v, want preparing marker", progress)
				}
			}
		})
	}
}

func TestHandleSelectionXCancelsActiveTaskOperation(t *testing.T) {
	var cancelBody protocol.OperationCancelRequestBody
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandOperationCancel {
				t.Fatalf("command = %q, want %q", req.Command, protocol.CommandOperationCancel)
			}
			if err := json.Unmarshal(req.Body, &cancelBody); err != nil {
				t.Fatalf("decode cancel body: %v", err)
			}
			body, err := json.Marshal(protocol.OperationCancelResponseBody{
				Operation: protocol.OperationRecord{
					OperationID: cancelBody.OperationID,
					IssueID:     "az-1",
					Kind:        daemonclient.CommandSessionStart,
					State:       protocol.OperationStateCancelled,
				},
			})
			if err != nil {
				t.Fatalf("marshal cancel response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            body,
			}, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.nav.SelectTask("az-1", 0)
	m.markTaskOperationPending("az-1", "session_start", "op-session-start", protocol.OperationStateRunning)

	updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "x"})
	if cmd == nil {
		t.Fatal("expected cancel command")
	}
	updated := updatedAny.(Model)
	if got := updated.toasts[len(updated.toasts)-1].Message; got != "Cancelling operation op-session-start for az-1" {
		t.Fatalf("toast = %q, want cancellation feedback", got)
	}

	msg, ok := cmd().(operationCancelledMsg)
	if !ok {
		t.Fatalf("message = %T, want operationCancelledMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("cancel command error: %v", msg.err)
	}
	if cancelBody.OperationID.String() != "op-session-start" {
		t.Fatalf("operation id = %q, want op-session-start", cancelBody.OperationID)
	}
	for _, request := range transport.requests {
		if request == daemonclient.CommandSessionStop {
			t.Fatalf("unexpected session stop request: %v", transport.requests)
		}
	}

	afterAny, _ := updated.Update(msg)
	after := afterAny.(Model)
	if _, ok := after.pendingStatuses[taskIDKey("az-1")]; ok {
		t.Fatalf("expected optimistic pending session marker cleared, got %+v", after.pendingStatuses)
	}
	progress := after.pendingMutationForTask("az-1")
	if progress == nil || progress.State != string(protocol.OperationStateCancelled) {
		t.Fatalf("progress = %+v, want cancelled operation visible", progress)
	}
}

func TestOperationCancelRunningResponseKeepsPendingSessionMarker(t *testing.T) {
	m := newTestModel()
	m.markTaskOperationPending("az-1", "session_start", "op-session-start", protocol.OperationStateRunning)

	updatedAny, _ := m.Update(operationCancelledMsg{
		taskID: "az-1",
		record: protocol.OperationRecord{
			OperationID: "op-session-start",
			IssueID:     "az-1",
			Kind:        daemonclient.CommandSessionStart,
			State:       protocol.OperationStateRunning,
		},
	})
	updated := updatedAny.(Model)
	pending, ok := updated.pendingStatuses[taskIDKey("az-1")]
	if !ok {
		t.Fatal("expected pending session marker to remain while cancellation is still running")
	}
	if pending.operationID != "op-session-start" || pending.state != protocol.OperationStateRunning {
		t.Fatalf("pending marker = %+v, want running op-session-start", pending)
	}
	progress := updated.pendingMutationForTask("az-1")
	if progress == nil || progress.State != string(protocol.OperationStateRunning) {
		t.Fatalf("progress = %+v, want running operation still visible", progress)
	}
}

func TestHandleSelectionAsyncActionsShowImmediateFeedback(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantToast string
	}{
		{name: "attach", key: "a", wantToast: "Attaching to az-1"},
		{name: "update from base", key: "u", wantToast: "Update from base queued for az-1"},
		{name: "merge", key: "m", wantToast: "Preparing merge for az-1"},
		{name: "prepare pr", key: "P", wantToast: "Preparing PR for az-1"},
		{name: "open pr", key: "O", wantToast: "Opening PR for az-1"},
		{name: "abort merge", key: "M", wantToast: "Abort merge queued for az-1"},
		{name: "open helix", key: "H", wantToast: "Opening Helix for az-1"},
		{name: "cleanup preflight", key: "w", wantToast: "Cleanup preflight queued for az-1"},
		{name: "delete cleanup preflight", key: "W", wantToast: "Delete + cleanup preflight queued for az-1"},
		{name: "refresh workspace", key: "r", wantToast: "Refreshing az-1"},
		{name: "archive tombstone", key: "T", wantToast: "Archive queued for az-1"},
		{name: "archive delete", key: "d", wantToast: "Archive queued for az-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.nav.SelectTask("az-1", 0)

			updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: tt.key})
			if cmd == nil {
				t.Fatal("expected async command")
			}
			updated := updatedAny.(Model)
			if len(updated.toasts) == 0 {
				t.Fatal("expected immediate feedback toast")
			}
			if got := updated.toasts[len(updated.toasts)-1].Message; got != tt.wantToast {
				t.Fatalf("toast = %q, want %q", got, tt.wantToast)
			}
		})
	}
}

func TestHandleNormalModeAttachShortcutQueuesSelectedIssueAttach(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-3", 1)

	updatedAny, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected attach command")
	}
	updated := updatedAny.(Model)
	if len(updated.toasts) == 0 {
		t.Fatal("expected immediate feedback toast")
	}
	if got := updated.toasts[len(updated.toasts)-1].Message; got != "Attaching to az-3" {
		t.Fatalf("toast = %q, want %q", got, "Attaching to az-3")
	}
}

func TestHandleBulkActionShowsImmediateFeedback(t *testing.T) {
	tests := []struct {
		name      string
		action    string
		wantToast string
	}{
		{name: "move left", action: "h", wantToast: "Bulk lifecycle action queued for 2 task(s)"},
		{name: "move right", action: "l", wantToast: "Bulk lifecycle action queued for 2 task(s)"},
		{name: "backlog", action: "0", wantToast: "Bulk backlog update queued for 2 task(s)"},
		{name: "open", action: "1", wantToast: "Bulk open update queued for 2 task(s)"},
		{name: "in progress", action: "2", wantToast: "Bulk active update queued for 2 task(s)"},
		{name: "in review", action: "3", wantToast: "Bulk review request queued for 2 task(s)"},
		{name: "delete", action: "d", wantToast: "Bulk delete queued for 2 task(s)"},
		{name: "archive", action: "a", wantToast: "Bulk archive queued for 2 task(s)"},
		{name: "cleanup", action: "w", wantToast: "Bulk cleanup preflight queued for 2 task(s)"},
		{name: "delete cleanup", action: "W", wantToast: "Bulk delete + cleanup preflight queued for 2 task(s)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			updatedAny, cmd := m.handleBulkAction(overlay.BulkActionMsg{
				Action:      tt.action,
				SelectedIDs: []string{"az-1", "az-2"},
			})
			if cmd == nil {
				t.Fatal("expected bulk command")
			}
			updated := updatedAny.(Model)
			if len(updated.toasts) == 0 {
				t.Fatal("expected immediate feedback toast")
			}
			if got := updated.toasts[len(updated.toasts)-1].Message; got != tt.wantToast {
				t.Fatalf("toast = %q, want %q", got, tt.wantToast)
			}
		})
	}
}

func TestHandleBulkDoneActionShowsCloseCleanupConfirmation(t *testing.T) {
	m := newTestModel()
	updatedAny, cmd := m.handleBulkAction(overlay.BulkActionMsg{
		Action:      "4",
		SelectedIDs: []string{"az-1", "az-2"},
	})
	if cmd == nil {
		t.Fatal("expected confirmation command")
	}
	updated := updatedAny.(Model)
	if updated.pendingClose == nil || updated.pendingClose.bulkMode != "set" || updated.pendingClose.targetStatus != domain.StatusDone {
		t.Fatalf("pending close = %+v, want bulk done cleanup confirmation", updated.pendingClose)
	}
	if len(updated.toasts) != 0 {
		t.Fatalf("toasts = %+v, want confirmation before queued feedback", updated.toasts)
	}
}

func TestSubmittedOverlayMutationsShowImmediateFeedback(t *testing.T) {
	t.Run("create task", func(t *testing.T) {
		m := newTestModel()

		updatedAny, cmd := m.Update(overlay.TaskCreatedMsg{Title: "New task"})
		if cmd == nil {
			t.Fatal("expected save command")
		}
		updated := updatedAny.(Model)
		if got := updated.toasts[len(updated.toasts)-1].Message; got != "Creating task" {
			t.Fatalf("toast = %q, want Creating task", got)
		}
	})

	t.Run("edit task", func(t *testing.T) {
		m := newTestModel()

		updatedAny, cmd := m.Update(overlay.TaskCreatedMsg{ID: "az-1", Title: "Updated"})
		if cmd == nil {
			t.Fatal("expected save command")
		}
		updated := updatedAny.(Model)
		if got := updated.toasts[len(updated.toasts)-1].Message; got != "Saving task az-1" {
			t.Fatalf("toast = %q, want Saving task az-1", got)
		}
	})

	t.Run("create pr", func(t *testing.T) {
		m := newTestModel()

		updatedAny, cmd := m.Update(overlay.PRCreatedMsg{})
		if cmd == nil {
			t.Fatal("expected pr create command")
		}
		updated := updatedAny.(Model)
		if got := updated.toasts[len(updated.toasts)-1].Message; got != "Creating PR" {
			t.Fatalf("toast = %q, want Creating PR", got)
		}
	})
}

func TestSessionStartedPendingMarksBoardAndDetailProgress(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}

	updatedAny, _ := m.Update(sessionStartedMsg{
		issueID:     "az-1",
		operationID: "op-session",
		state:       protocol.OperationStateQueued,
	})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want app.Model", updatedAny)
	}

	pending, ok := updated.pendingStatuses[taskIDKey("az-1")]
	if !ok {
		t.Fatal("expected pending status entry for session start")
	}
	if pending.action != "session_start" {
		t.Fatalf("pending action = %q, want %q", pending.action, "session_start")
	}
	if pending.operationID != "op-session" {
		t.Fatalf("pending operation id = %q, want %q", pending.operationID, "op-session")
	}
	if pending.state != protocol.OperationStateQueued {
		t.Fatalf("pending state = %q, want %q", pending.state, protocol.OperationStateQueued)
	}
	if pending.targetStatus != domain.StatusInProgress {
		t.Fatalf("pending target status = %q, want %q", pending.targetStatus, domain.StatusInProgress)
	}
	if updated.tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("task status = %q, want optimistic %q", updated.tasks[0].Status, domain.StatusInProgress)
	}

	signals := updated.runtimeSignalsForBoard()
	got := signals["az-1"]
	if got.PendingOperationState != string(protocol.OperationStateQueued) {
		t.Fatalf("pending state = %q, want %q", got.PendingOperationState, protocol.OperationStateQueued)
	}
	if got.PendingOperationID != "op-session" {
		t.Fatalf("pending operation id = %q, want %q", got.PendingOperationID, "op-session")
	}

	progress := updated.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected pending mutation progress for session start")
	}
	if progress.State != string(protocol.OperationStateQueued) {
		t.Fatalf("progress state = %q, want %q", progress.State, protocol.OperationStateQueued)
	}
	if progress.OperationID != "op-session" {
		t.Fatalf("progress operation id = %q, want %q", progress.OperationID, "op-session")
	}
	if progress.TargetStatus != domain.StatusInProgress {
		t.Fatalf("progress target status = %q, want %q", progress.TargetStatus, domain.StatusInProgress)
	}
}

func TestApplyPendingStatusOverlaysIgnoresNonStatusPending(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.pendingStatuses = map[string]pendingTaskStatus{
		taskIDKey("az-1"): {
			action:      "session_start",
			operationID: "op-session",
			state:       protocol.OperationStateQueued,
			updatedAt:   time.Now(),
		},
	}

	m.applyPendingStatusOverlays()

	if m.tasks[0].Status != domain.StatusOpen {
		t.Fatalf("task status = %q, want %q", m.tasks[0].Status, domain.StatusOpen)
	}
}

func TestSessionStartPendingSurvivesPartialInProgressSnapshot(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.markTaskOperationPending("az-1", "session_start", "op-session", protocol.OperationStateQueued)

	updatedAny, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask},
		},
	})
	updated := updatedAny.(Model)

	if _, ok := updated.pendingStatuses[taskIDKey("az-1")]; !ok {
		t.Fatal("expected session_start pending marker to survive in_progress snapshot without session")
	}
	if updated.tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("task status = %q, want %q", updated.tasks[0].Status, domain.StatusInProgress)
	}
	progress := updated.pendingMutationForTask("az-1")
	if progress == nil || progress.OperationID != "op-session" || progress.State != string(protocol.OperationStateQueued) {
		t.Fatalf("pending mutation progress = %+v", progress)
	}
}

func TestReconcilePendingStatusesClearsSessionMarkersFromHydratedProjection(t *testing.T) {
	now := time.Now()

	t.Run("session start", func(t *testing.T) {
		m := newTestModel()
		m.tasks = []domain.Task{
			{
				ID:             "az-1",
				Title:          "Task",
				Status:         domain.StatusOpen,
				Priority:       domain.P2,
				Type:           domain.TypeTask,
				HasTmuxSession: true,
				Session:        &domain.Session{IssueID: "az-1", State: domain.SessionBusy},
			},
		}
		m.pendingStatuses = map[string]pendingTaskStatus{
			taskIDKey("az-1"): {
				action:      "session_start",
				operationID: "op-session",
				state:       protocol.OperationStateRunning,
				updatedAt:   now,
			},
		}

		m.reconcilePendingStatuses()
		if _, ok := m.pendingStatuses[taskIDKey("az-1")]; ok {
			t.Fatal("expected session_start pending marker to clear after session hydration")
		}
	})

	t.Run("session stop", func(t *testing.T) {
		m := newTestModel()
		m.tasks = []domain.Task{
			{
				ID:       "az-1",
				Title:    "Task",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		}
		m.pendingStatuses = map[string]pendingTaskStatus{
			taskIDKey("az-1"): {
				action:      "session_stop",
				operationID: "op-stop",
				state:       protocol.OperationStateRunning,
				updatedAt:   now,
			},
		}

		m.reconcilePendingStatuses()
		if _, ok := m.pendingStatuses[taskIDKey("az-1")]; ok {
			t.Fatal("expected session_stop pending marker to clear when session projection is absent")
		}
	})
}

func TestReconcilePendingOperationsClearsSessionProgressFromHydratedProjection(t *testing.T) {
	now := time.Now()

	t.Run("session start", func(t *testing.T) {
		m := newTestModel()
		m.tasks = []domain.Task{
			{
				ID:             "az-1",
				Title:          "Task",
				Status:         domain.StatusOpen,
				Priority:       domain.P2,
				Type:           domain.TypeTask,
				HasTmuxSession: true,
				Session:        &domain.Session{IssueID: "az-1", State: domain.SessionBusy},
			},
		}
		m.pendingOpsByTask = map[string]pendingOperationProgress{
			taskIDKey("az-1"): {
				operationID: "op-session",
				kind:        "session.start",
				state:       protocol.OperationStateRunning,
				percent:     50,
				updatedAt:   now,
			},
		}

		m.reconcilePendingOperations()
		if _, ok := m.pendingOpsByTask[taskIDKey("az-1")]; ok {
			t.Fatal("expected session.start pending operation to clear after session hydration")
		}
	})

	t.Run("session stop", func(t *testing.T) {
		m := newTestModel()
		m.tasks = []domain.Task{
			{
				ID:       "az-1",
				Title:    "Task",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		}
		m.pendingOpsByTask = map[string]pendingOperationProgress{
			taskIDKey("az-1"): {
				operationID: "op-stop",
				kind:        "session.stop",
				state:       protocol.OperationStateRunning,
				percent:     50,
				updatedAt:   now,
			},
		}

		m.reconcilePendingOperations()
		if _, ok := m.pendingOpsByTask[taskIDKey("az-1")]; ok {
			t.Fatal("expected session.stop pending operation to clear when session projection is absent")
		}
	})
}

func TestSyncProjectionIndexesDoesNotPreserveStaleRuntimePendingOperation(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask, HasWorktree: true},
	}
	m.runtimeSignalsByTask = map[string]board.RuntimeSignals{
		"az-1": {
			HasWorktree:             true,
			PendingOperationID:      "op-stale",
			PendingOperationState:   string(protocol.OperationStateRunning),
			PendingOperationPercent: 50,
		},
	}

	m.syncProjectionIndexesFromTasks()

	signals := m.runtimeSignalsByTask["az-1"]
	if signals.PendingOperationID != "" || signals.PendingOperationState != "" || signals.PendingOperationPercent != 0 {
		t.Fatalf("expected stale pending operation cleared, got %+v", signals)
	}
	if !signals.HasWorktree {
		t.Fatalf("expected non-operation runtime signal preserved, got %+v", signals)
	}
}

func TestPendingMutationForTaskIncludesOperationProgressPayload(t *testing.T) {
	m := newTestModel()
	m.pendingOpsByTask = map[string]pendingOperationProgress{
		taskIDKey("az-1"): {
			operationID: "op-merge",
			state:       protocol.OperationStateRunning,
			percent:     65,
			message:     "running git.merge",
		},
	}

	progress := m.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected pending mutation progress")
	}
	if progress.OperationID != "op-merge" {
		t.Fatalf("operation id = %q, want op-merge", progress.OperationID)
	}
	if progress.State != string(protocol.OperationStateRunning) {
		t.Fatalf("state = %q, want %q", progress.State, protocol.OperationStateRunning)
	}
	if progress.ProgressPercent != 65 {
		t.Fatalf("percent = %d, want 65", progress.ProgressPercent)
	}
	if progress.ProgressMessage != "running git.merge" {
		t.Fatalf("message = %q, want running git.merge", progress.ProgressMessage)
	}
}

func TestDaemonOperationProgressEventUpdatesRuntimeSignalsAndClearsOnDone(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.runtimeSignalWorktreeByTask = map[string]string{
		"az-1": "/tmp/wt-az-1",
	}

	startBody, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-merge",
			Kind:        "git.merge",
			State:       protocol.OperationStateRunning,
			ResourceKeys: []string{
				"worktree:/tmp/wt-az-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal operation event body: %v", err)
	}
	m.applyOperationProgressEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationRunning,
		Body:  startBody,
	})

	progressBody, err := json.Marshal(protocol.OperationProgressEventBody{
		OperationID: "op-merge",
		ProjectID:   "proj-1",
		State:       protocol.OperationStateRunning,
		Progress: protocol.OperationProgress{
			Message: "running git.merge",
			Percent: 72,
		},
	})
	if err != nil {
		t.Fatalf("marshal progress body: %v", err)
	}
	m.applyOperationProgressEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationProgress,
		Body:  progressBody,
	})

	signals := m.runtimeSignalsForBoard()
	got, ok := signals["az-1"]
	if !ok {
		t.Fatal("expected runtime signals for az-1")
	}
	if got.PendingOperationID != "op-merge" {
		t.Fatalf("pending op id = %q, want op-merge", got.PendingOperationID)
	}
	if got.PendingOperationState != string(protocol.OperationStateRunning) {
		t.Fatalf("pending state = %q, want running", got.PendingOperationState)
	}
	if got.PendingOperationPercent != 72 {
		t.Fatalf("pending percent = %d, want 72", got.PendingOperationPercent)
	}

	doneBody, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-merge",
			Kind:        "git.merge",
			State:       protocol.OperationStateDone,
			ResourceKeys: []string{
				"worktree:/tmp/wt-az-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal done body: %v", err)
	}
	m.applyOperationProgressEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationDone,
		Body:  doneBody,
	})

	cleared := m.runtimeSignalsForBoard()["az-1"]
	if cleared.PendingOperationID != "" || cleared.PendingOperationState != "" || cleared.PendingOperationPercent != 0 {
		t.Fatalf("expected pending op fields cleared, got %+v", cleared)
	}
}

func TestRuntimeSignalsForBoardHidesWorktreeOperationFailureWithoutWorktree(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-no-worktree", Title: "Open task without worktree", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-worktree", Title: "Open task with blocked worktree", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, HasWorktree: true},
	}
	m.pendingOpsByTask = map[string]pendingOperationProgress{
		taskIDKey("az-no-worktree"): {
			operationID: "op-stale-merge",
			kind:        daemonclient.CommandGitMerge,
			state:       protocol.OperationStateFailed,
			message:     "merge failed",
			updatedAt:   time.Now(),
		},
		taskIDKey("az-worktree"): {
			operationID: "op-real-merge",
			kind:        daemonclient.CommandGitMerge,
			state:       protocol.OperationStateFailed,
			message:     "merge failed",
			updatedAt:   time.Now(),
		},
	}

	viewForTask := func(task domain.Task) string {
		return board.Render(
			[]board.Column{{Title: "Open", Tasks: []domain.Task{task}}},
			board.Cursor{Column: 0, Task: 0},
			map[string]bool{},
			m.runtimeSignalsForBoard(),
			nil,
			nil,
			false,
			nil,
			0,
			m.styles,
			90,
			20,
		)
	}

	if got := ansi.Strip(viewForTask(m.tasks[0])); strings.Contains(got, "M:!") {
		t.Fatalf("no-worktree task rendered worktree operation marker:\n%s", got)
	}
	if got := ansi.Strip(viewForTask(m.tasks[1])); !strings.Contains(got, "M:!") {
		t.Fatalf("worktree-backed task did not render operation marker:\n%s", got)
	}

	m.pendingOpsByTask = nil
	m.pendingFailures = map[string]taskMutationFailure{
		taskIDKey("az-no-worktree"): {
			operationID: "op-stale-notice",
			action:      daemonclient.CommandGitMerge,
			message:     "merge failed",
			updatedAt:   time.Now(),
		},
		taskIDKey("az-worktree"): {
			operationID: "op-real-notice",
			action:      daemonclient.CommandGitMerge,
			message:     "merge failed",
			updatedAt:   time.Now(),
		},
	}

	if got := ansi.Strip(viewForTask(m.tasks[0])); strings.Contains(got, "M:!") {
		t.Fatalf("no-worktree task rendered worktree failure marker:\n%s", got)
	}
	if got := ansi.Strip(viewForTask(m.tasks[1])); !strings.Contains(got, "M:!") {
		t.Fatalf("worktree-backed task did not render failure marker:\n%s", got)
	}

	m.pendingFailures = nil
	m.runtimeSignalsByTask = map[string]board.RuntimeSignals{
		"az-no-worktree": {
			PendingOperationID:    "op-stale-runtime",
			PendingOperationState: string(protocol.OperationStateFailed),
		},
		"az-worktree": {
			HasWorktree:           true,
			PendingOperationID:    "op-real-runtime",
			PendingOperationState: string(protocol.OperationStateFailed),
		},
	}

	if got := ansi.Strip(viewForTask(m.tasks[0])); strings.Contains(got, "M:!") {
		t.Fatalf("no-worktree task rendered runtime worktree marker:\n%s", got)
	}
	if got := ansi.Strip(viewForTask(m.tasks[1])); !strings.Contains(got, "M:!") {
		t.Fatalf("worktree-backed task did not render runtime marker:\n%s", got)
	}
}

func TestDaemonOperationFailureEventRemainsVisibleWithReason(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask},
	}
	body, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-close",
			IssueID:     naming.IssueID("az-1"),
			Kind:        "git.merge",
			State:       protocol.OperationStateFailed,
			Error: &protocol.OperationError{
				Code:      protocol.ErrorCodeInternal,
				Message:   "merge failed: dirty worktree",
				Retryable: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal failure body: %v", err)
	}

	m.applyOperationProgressEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationFailed,
		Body:  body,
	})

	signals := m.runtimeSignalsForBoard()
	got := signals["az-1"]
	if got.PendingOperationID != "op-close" || got.PendingOperationState != string(protocol.OperationStateFailed) {
		t.Fatalf("pending signal = %+v, want failed op-close", got)
	}
	progress := m.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected failed operation in workspace mutation progress")
	}
	want := "Could not merge az-1. It stayed In Progress. Reason: close cleanup is blocked by local worktree changes. Next: commit, discard, or merge the worktree changes, then retry"
	if progress.ProgressMessage != want {
		t.Fatalf("progress message = %q, want %q", progress.ProgressMessage, want)
	}
	if progress.CurrentStatus != domain.StatusInProgress || progress.PreviousStatus != domain.StatusInProgress {
		t.Fatalf("progress status context = previous %q current %q, want trusted In Progress", progress.PreviousStatus, progress.CurrentStatus)
	}
	if progress.FailureReason != "close cleanup is blocked by local worktree changes" ||
		progress.FailureRecovery != "commit, discard, or merge the worktree changes, then retry" {
		t.Fatalf("progress failure details = reason %q recovery %q", progress.FailureReason, progress.FailureRecovery)
	}
}

func TestDaemonOperationFailureEventReportsIntegratedCleanupBlocked(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusInReview, Priority: domain.P2, Type: domain.TypeBug},
	}
	body, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-close",
			IssueID:     naming.IssueID("az-1"),
			Kind:        daemonclient.CommandTaskClose,
			State:       protocol.OperationStateFailed,
			Error: &protocol.OperationError{
				Code:      protocol.ErrorCodeInternal,
				Message:   "phase worktree_cleanup for issue az-1: fatal: contains modified or untracked files. Integration already completed: branch riordan/az-1/fix landed on main; cleanup/status remains. Next: repair the reported cleanup blocker, then retry close; retry will skip merge when the source is already reachable",
				Retryable: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal failure body: %v", err)
	}

	m.applyOperationProgressEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationFailed,
		Body:  body,
	})

	progress := m.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected failed operation in workspace mutation progress")
	}
	want := "Could not move az-1 to Done. It stayed In Review. Reason: code already landed; close cleanup is blocked by local worktree changes. Next: repair the reported cleanup blocker, then retry close; retry will skip merge when the source is already reachable"
	if progress.ProgressMessage != want {
		t.Fatalf("progress message = %q, want %q", progress.ProgressMessage, want)
	}
	if progress.FailureReason != "code already landed; close cleanup is blocked by local worktree changes" {
		t.Fatalf("failure reason = %q, want landed cleanup blocker", progress.FailureReason)
	}
}

func TestDaemonOperationFailureEventNormalizesProgressOnlyFailure(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusInReview, Priority: domain.P2, Type: domain.TypeTask},
	}
	body, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-close",
			IssueID:     naming.IssueID("az-1"),
			Kind:        daemonclient.CommandTaskClose,
			State:       protocol.OperationStateFailed,
			Progress: &protocol.OperationProgress{
				Message: "cannot close issue az-1: child issues remain unresolved: az-child",
				Percent: 40,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal failure body: %v", err)
	}

	m.applyOperationProgressEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationFailed,
		Body:  body,
	})

	progress := m.pendingMutationForTask("az-1")
	want := "Could not move az-1 to Done. It stayed In Review. Reason: Done is blocked by unresolved child issues. Next: press C to close clean children too, or resolve child issues and retry"
	if progress == nil || progress.ProgressMessage != want {
		t.Fatalf("progress = %+v, want %q", progress, want)
	}
	if progress.PreviousStatus != domain.StatusInReview ||
		progress.CurrentStatus != domain.StatusInReview ||
		progress.TargetStatus != domain.StatusDone {
		t.Fatalf("progress status context = previous %q current %q target %q, want In Review/In Review/Done", progress.PreviousStatus, progress.CurrentStatus, progress.TargetStatus)
	}
	if progress.FailureReason != "Done is blocked by unresolved child issues" ||
		progress.FailureRecovery != "press C to close clean children too, or resolve child issues and retry" {
		t.Fatalf("progress failure details = reason %q recovery %q", progress.FailureReason, progress.FailureRecovery)
	}
}

func TestDaemonSessionStartFailureEventRemainsVisibleOnTask(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	detail := "worktree create failed for az-1: failed to create worktree with git worktree add -b user/az-1/task /tmp/az-1 main: hook failed (rolled back worktree /tmp/az-1)"
	body, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-start",
			IssueID:     naming.IssueID("az-1"),
			Kind:        daemonclient.CommandSessionStart,
			State:       protocol.OperationStateFailed,
			Error: &protocol.OperationError{
				Code:      protocol.ErrorCodeInternal,
				Message:   detail,
				Retryable: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal failure body: %v", err)
	}

	m.applyDaemonStreamEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationFailed,
		Body:  body,
	}, false)

	signals := m.runtimeSignalsForBoard()
	got := signals["az-1"]
	if got.PendingOperationID != "op-start" || got.PendingOperationState != string(protocol.OperationStateFailed) {
		t.Fatalf("pending signal = %+v, want failed op-start", got)
	}
	progress := m.pendingMutationForTask("az-1")
	if progress == nil {
		t.Fatal("expected failed session start in task-local mutation progress")
	}
	if len(m.toasts) == 0 {
		t.Fatal("expected floating failure toast")
	}
	toast := m.toasts[len(m.toasts)-1].Message
	for _, want := range []string{"git worktree add", "hook failed", "rolled back worktree /tmp/az-1"} {
		if !strings.Contains(progress.ProgressMessage, want) {
			t.Fatalf("progress message = %q, want substring %q", progress.ProgressMessage, want)
		}
		if !strings.Contains(toast, want) {
			t.Fatalf("toast message = %q, want substring %q", toast, want)
		}
	}
}

func TestApplyOperationRecordsLoadsQueuedAndRecentFailedOperations(t *testing.T) {
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}

	m.applyOperationRecords([]protocol.OperationRecord{
		{
			OperationID: "op-start",
			IssueID:     naming.IssueID("az-1"),
			Kind:        "session.start",
			State:       protocol.OperationStateQueued,
		},
		{
			OperationID: "op-failed",
			IssueID:     naming.IssueID("az-2"),
			Kind:        "git.merge",
			State:       protocol.OperationStateFailed,
			Error:       &protocol.OperationError{Code: protocol.ErrorCodeInternal, Message: "conflict while merging"},
			FinishedAt:  &finished,
		},
	})

	signals := m.runtimeSignalsForBoard()
	if got := signals["az-1"]; got.PendingOperationID != "op-start" || got.PendingOperationState != string(protocol.OperationStateQueued) {
		t.Fatalf("az-1 pending = %+v, want queued op-start", got)
	}
	if got := signals["az-2"]; got.PendingOperationID != "" || got.PendingOperationState != "" {
		t.Fatalf("az-2 board signal = %+v, want no failed git marker without worktree", got)
	}
	progress := m.pendingMutationForTask("az-2")
	want := "Could not merge az-2. It stayed Open. Reason: the change conflicts with current daemon state. Next: refresh the task, resolve the reported blocker, then retry"
	if progress == nil || progress.ProgressMessage != want {
		t.Fatalf("az-2 progress = %+v, want %q", progress, want)
	}
}

func TestResolveOperationTaskIDCoversSessionGitAndWorktreeMutations(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.runtimeSignalWorktreeByTask = map[string]string{
		"az-1": "/tmp/wt-az-1",
		"az-2": "/tmp/wt-az-2",
	}

	cases := []struct {
		name         string
		issueID      naming.IssueID
		resourceKeys []string
		wantTaskID   string
	}{
		{
			name:       "session lifecycle issue id",
			issueID:    naming.IssueID("az-1"),
			wantTaskID: "az-1",
		},
		{
			name:         "git merge uses worktree resource key",
			resourceKeys: []string{"worktree:/tmp/wt-az-1"},
			wantTaskID:   "az-1",
		},
		{
			name:         "git fetch uses worktree resource key",
			resourceKeys: []string{"worktree:/tmp/wt-az-2"},
			wantTaskID:   "az-2",
		},
		{
			name:         "worktree remove uses issue resource key",
			resourceKeys: []string{"issue:proj:az-2"},
			wantTaskID:   "az-2",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := m.resolveOperationTaskID(tt.issueID, tt.resourceKeys)
			if got != tt.wantTaskID {
				t.Fatalf("resolveOperationTaskID(%q, %v) = %q, want %q", tt.issueID, tt.resourceKeys, got, tt.wantTaskID)
			}
		})
	}
}

func TestSyncTaskWorkspaceOverlayBackfillsSessionWorktreeFromRuntimeMap(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{
			ID:          "az-1",
			Title:       "runtime worktree fallback",
			Status:      domain.StatusInProgress,
			HasWorktree: true,
			Session: &domain.Session{
				IssueID: "az-1",
				State:   domain.SessionBusy,
			},
		},
	}
	m.runtimeSignalWorktreeByTask = map[string]string{
		"az-1": "/tmp/repo-az-1",
	}
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

	m.syncTaskWorkspaceOverlay()

	current := m.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace overlay, got %T", current)
	}
	view := workspace.View()
	if !strings.Contains(view, "/tmp/repo-az-1") {
		t.Fatalf("workspace view missing runtime worktree path: %q", view)
	}
}

func TestDaemonOperationLifecycleEventsTrackPendingForGitAndWorktreeMutations(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.runtimeSignalWorktreeByTask = map[string]string{
		"az-1": "/tmp/wt-az-1",
		"az-2": "/tmp/wt-az-2",
	}

	gitQueued, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-git-1",
			Kind:        "git.merge",
			State:       protocol.OperationStateQueued,
			ResourceKeys: []string{
				"worktree:/tmp/wt-az-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal git queued body: %v", err)
	}
	m.applyOperationProgressEvent(protocol.EventEnvelope{Event: protocol.EventOperationQueued, Body: gitQueued})

	worktreeRunning, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-wt-1",
			IssueID:     naming.IssueID("az-2"),
			Kind:        "worktree.remove",
			State:       protocol.OperationStateRunning,
			ResourceKeys: []string{
				"issue:proj:az-2",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal worktree running body: %v", err)
	}
	m.applyOperationProgressEvent(protocol.EventEnvelope{Event: protocol.EventOperationRunning, Body: worktreeRunning})

	signals := m.runtimeSignalsForBoard()
	if got := signals["az-1"]; got.PendingOperationID != "op-git-1" || got.PendingOperationState != string(protocol.OperationStateQueued) {
		t.Fatalf("az-1 pending = %+v, want queued op-git-1", got)
	}
	if got := signals["az-2"]; got.PendingOperationID != "op-wt-1" || got.PendingOperationState != string(protocol.OperationStateRunning) {
		t.Fatalf("az-2 pending = %+v, want running op-wt-1", got)
	}
}

func TestPendingWorktreeCleanupOperationFailureOpensForceConfirmation(t *testing.T) {
	m := newTestModel()
	m.pendingCleanupOps["op-cleanup"] = pendingWorktreeCleanupConfirmation{
		taskID:      "az-1",
		deletedTask: false,
		force:       false,
	}
	m.operationTaskID["op-cleanup"] = "az-1"
	m.pendingOpsByTask["az-1"] = pendingOperationProgress{
		operationID: "op-cleanup",
		state:       protocol.OperationStateRunning,
	}

	body, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-cleanup",
			IssueID:     naming.IssueID("az-1"),
			Kind:        daemonclient.CommandWorktreeRemove,
			State:       protocol.OperationStateFailed,
			Error: &protocol.OperationError{
				Code:      protocol.ErrorCodeInternal,
				Message:   "failed to remove worktree: git worktree remove /tmp/az-1 failed: exit status 128: fatal: '/tmp/az-1' contains modified or untracked files, use --force to delete it",
				Retryable: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal operation failed body: %v", err)
	}

	cmd, handled := m.handlePendingWorktreeCleanupOperationEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationFailed,
		Body:  body,
	})
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if cmd == nil {
		t.Fatal("expected force confirmation overlay command")
	}
	_ = cmd()
	if m.pendingCleanup == nil || !m.pendingCleanup.force || m.pendingCleanup.taskID != "az-1" {
		t.Fatalf("pending cleanup = %+v, want forced az-1 cleanup", m.pendingCleanup)
	}
	if _, ok := m.pendingCleanupOps["op-cleanup"]; ok {
		t.Fatal("pending cleanup operation was not cleared")
	}
	if _, ok := m.pendingOpsByTask["az-1"]; ok {
		t.Fatal("pending board operation was not cleared")
	}
}

func TestLoadProjectOrchestratorSnapshotCmd(t *testing.T) {
	m := newTestModel()
	if cmd := m.loadProjectOrchestratorSnapshotCmd(); cmd == nil {
		t.Fatal("current-project orchestrator snapshot command = nil")
	}

	if cmd := m.loadProjectOrchestratorSnapshotCmd(); cmd == nil {
		t.Fatal("snapshot refresh must not depend on a legacy mode")
	}
}

func TestProjectOrchestratorStopActionUsesProjectTarget(t *testing.T) {
	m := newTestModel()
	var gotAction string
	m.projectOrchestratorActionRunner = func(_ context.Context, target projectOrchestratorTarget, action string, request protocol.OrchestratorSessionRequest) (protocol.OrchestratorSessionResult, error) {
		gotAction = action
		if target.ProjectID != "project-stop" || request.Scope.Kind != domain.OrchestrationScopeProject {
			t.Fatalf("target=%+v request=%+v", target, request)
		}
		return protocol.OrchestratorSessionResult{Scope: request.Scope, Disposition: "stopped", Lifecycle: domain.OrchestratorPaused}, nil
	}
	cmd := m.projectOrchestratorActionCmd(projectOrchestratorSnapshot{ProjectID: "project-stop", Path: t.TempDir()}, "stop")
	msg, ok := cmd().(projectOrchestratorActionMsg)
	if !ok || msg.err != nil || msg.result.Disposition != "stopped" || gotAction != "stop" {
		t.Fatalf("message=%+v action=%q", msg, gotAction)
	}
}

func TestProjectOrchestratorRefreshFailurePreservesLastKnownSnapshot(t *testing.T) {
	m := newTestModel()
	known := projectOrchestratorSnapshot{ProjectID: m.daemonProjectID(), Snapshot: &protocol.OrchestrationSnapshot{Lifecycle: domain.OrchestratorWorking}}
	m.projectOrchestrator = &known
	updatedAny, _ := m.Update(projectOrchestratorLoadedMsg{project: projectOrchestratorSnapshot{ProjectID: m.daemonProjectID()}, err: errors.New("temporary read failure")})
	updated := updatedAny.(Model)
	if updated.projectOrchestrator == nil || updated.projectOrchestrator.Snapshot == nil || updated.projectOrchestrator.Snapshot.Lifecycle != domain.OrchestratorWorking {
		t.Fatalf("last-known orchestrator snapshot was discarded: %+v", updated.projectOrchestrator)
	}
}
