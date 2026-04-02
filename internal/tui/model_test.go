package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/testprofile"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

type mockTmuxService struct {
	switchFn func(ctx context.Context, name string) error
	popupFn  func(ctx context.Context, title, width, height, command string) error
}

type probeOverlay struct {
	updated bool
	lastMsg tea.Msg
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

	// Disable placeholder data for tests so we can control the tasks
	m.usePlaceholder = false

	// Add some test tasks
	// Open column: az-1 (index 0), az-2 (index 1)
	// InProgress column: az-3 (index 0)
	// Blocked column: az-4 (index 0)
	// Done column: az-5 (index 0)
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug},
		{ID: "az-3", Title: "Task 3", Status: domain.StatusInProgress, Priority: domain.P0, Type: domain.TypeFeature},
		{ID: "az-4", Title: "Task 4", Status: domain.StatusBlocked, Priority: domain.P1, Type: domain.TypeTask},
		{ID: "az-5", Title: "Task 5", Status: domain.StatusDone, Priority: domain.P3, Type: domain.TypeTask},
	}

	m.height = 24 // Set a reasonable terminal height for testing
	m.width = 80

	return m
}

// Helper to get cursor position in a model
func getCursorPosition(m Model) Position {
	columns := m.buildColumns()
	return m.nav.GetPosition(columns)
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
	cfg := &config.Config{
		Session: config.SessionConfig{
			LogDir: "/tmp/azedarach-user-logs",
		},
	}

	got := resolveTUILogFilePath(cfg)
	want := filepath.Join("/tmp/azedarach-user-logs", "az.log")
	if got != want {
		t.Fatalf("resolveTUILogFilePath() = %q, want %q", got, want)
	}
}

func TestDaemonLogFilePath_UsesRepoDir(t *testing.T) {
	m := newTestModel()
	m.repoDir = "/tmp/worktree"

	got := m.daemonLogFilePath()
	want := filepath.Join("/tmp/worktree", ".azedarach", "daemon.log")
	if got != want {
		t.Fatalf("daemonLogFilePath() = %q, want %q", got, want)
	}
}

func TestOpenLogStreamCmd_SkipsMissingPathsWhenAnotherLogExists(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()

	tmpDir := t.TempDir()
	tuiLog := filepath.Join(tmpDir, "az.log")
	if err := os.WriteFile(tuiLog, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write tui log: %v", err)
	}
	missingDaemonLog := filepath.Join(tmpDir, "daemon.log")

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
	if !strings.Contains(popupCommand, shellSingleQuote(tuiLog)) {
		t.Fatalf("popup command = %q, want tui log path", popupCommand)
	}
	if strings.Contains(popupCommand, shellSingleQuote(missingDaemonLog)) {
		t.Fatalf("popup command unexpectedly included missing path: %q", popupCommand)
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

func TestResolveDaemonBinaryForRepo(t *testing.T) {
	t.Run("prefers azd sibling of invoked az command", func(t *testing.T) {
		repoDir := t.TempDir()
		azBinDir := t.TempDir()
		azPath := filepath.Join(azBinDir, "az")
		azdPath := filepath.Join(azBinDir, "azd")
		if err := os.WriteFile(azPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write az fixture: %v", err)
		}
		if err := os.WriteFile(azdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write azd fixture: %v", err)
		}

		origProcessArgs := processArgs
		origLookupPath := lookupPath
		t.Cleanup(func() {
			processArgs = origProcessArgs
			lookupPath = origLookupPath
		})

		processArgs = func() []string { return []string{"az"} }
		lookupPath = func(file string) (string, error) {
			if file == "az" {
				return azPath, nil
			}
			return "", fmt.Errorf("not found: %s", file)
		}

		if got := resolveDaemonBinaryForRepo(repoDir); got != azdPath {
			t.Fatalf("expected %q, got %q", azdPath, got)
		}
	})

	t.Run("prefers azd sibling of running az executable", func(t *testing.T) {
		repoDir := t.TempDir()
		execDir := t.TempDir()
		azPath := filepath.Join(execDir, "az")
		azdPath := filepath.Join(execDir, "azd")
		if err := os.WriteFile(azPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write az fixture: %v", err)
		}
		if err := os.WriteFile(azdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write azd fixture: %v", err)
		}

		origExecutablePath := executablePath
		origProcessArgs := processArgs
		origLookupPath := lookupPath
		t.Cleanup(func() { executablePath = origExecutablePath })
		t.Cleanup(func() { processArgs = origProcessArgs })
		t.Cleanup(func() { lookupPath = origLookupPath })
		processArgs = func() []string { return []string{"az"} }
		lookupPath = func(file string) (string, error) { return "", fmt.Errorf("not found: %s", file) }
		executablePath = func() (string, error) { return azPath, nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != azdPath {
			t.Fatalf("expected %q, got %q", azdPath, got)
		}
	})

	t.Run("falls back to repo local bin azd when executable sibling missing", func(t *testing.T) {
		repoDir := t.TempDir()
		binDir := filepath.Join(repoDir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir bin dir: %v", err)
		}
		azdPath := filepath.Join(binDir, "azd")
		if err := os.WriteFile(azdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write azd binary fixture: %v", err)
		}

		origExecutablePath := executablePath
		origProcessArgs := processArgs
		origLookupPath := lookupPath
		t.Cleanup(func() { executablePath = origExecutablePath })
		t.Cleanup(func() { processArgs = origProcessArgs })
		t.Cleanup(func() { lookupPath = origLookupPath })
		processArgs = func() []string { return []string{"az"} }
		lookupPath = func(file string) (string, error) { return "", fmt.Errorf("not found: %s", file) }
		executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "az"), nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != azdPath {
			t.Fatalf("expected %q, got %q", azdPath, got)
		}
	})

	t.Run("falls back to nested go-bubbletea bin azd from monorepo root", func(t *testing.T) {
		repoDir := t.TempDir()
		nestedBin := filepath.Join(repoDir, "go-bubbletea", "bin")
		if err := os.MkdirAll(nestedBin, 0o755); err != nil {
			t.Fatalf("mkdir nested bin dir: %v", err)
		}
		azdPath := filepath.Join(nestedBin, "azd")
		if err := os.WriteFile(azdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write nested azd fixture: %v", err)
		}

		origExecutablePath := executablePath
		origProcessArgs := processArgs
		origLookupPath := lookupPath
		origWorkingDir := workingDir
		t.Cleanup(func() { executablePath = origExecutablePath })
		t.Cleanup(func() { processArgs = origProcessArgs })
		t.Cleanup(func() { lookupPath = origLookupPath })
		t.Cleanup(func() { workingDir = origWorkingDir })
		processArgs = func() []string { return []string{"az"} }
		lookupPath = func(file string) (string, error) { return "", fmt.Errorf("not found: %s", file) }
		executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "az"), nil }
		workingDir = func() (string, error) { return t.TempDir(), nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != azdPath {
			t.Fatalf("expected %q, got %q", azdPath, got)
		}
	})

	t.Run("returns empty when neither source has azd", func(t *testing.T) {
		repoDir := t.TempDir()
		origExecutablePath := executablePath
		origProcessArgs := processArgs
		origLookupPath := lookupPath
		origWorkingDir := workingDir
		t.Cleanup(func() { executablePath = origExecutablePath })
		t.Cleanup(func() { processArgs = origProcessArgs })
		t.Cleanup(func() { lookupPath = origLookupPath })
		t.Cleanup(func() { workingDir = origWorkingDir })
		processArgs = func() []string { return []string{"az"} }
		lookupPath = func(file string) (string, error) { return "", fmt.Errorf("not found: %s", file) }
		executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "az"), nil }
		workingDir = func() (string, error) { return t.TempDir(), nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != "" {
			t.Fatalf("expected empty path, got %q", got)
		}
	})

	t.Run("falls back to cwd bin azd for just run workflows", func(t *testing.T) {
		repoDir := t.TempDir()
		cwd := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cwd, "bin"), 0o755); err != nil {
			t.Fatalf("mkdir cwd bin: %v", err)
		}
		cwdAzd := filepath.Join(cwd, "bin", "azd")
		if err := os.WriteFile(cwdAzd, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write cwd azd fixture: %v", err)
		}

		origExecutablePath := executablePath
		origProcessArgs := processArgs
		origLookupPath := lookupPath
		origWorkingDir := workingDir
		t.Cleanup(func() { executablePath = origExecutablePath })
		t.Cleanup(func() { processArgs = origProcessArgs })
		t.Cleanup(func() { lookupPath = origLookupPath })
		t.Cleanup(func() { workingDir = origWorkingDir })
		processArgs = func() []string { return []string{"az"} }
		lookupPath = func(file string) (string, error) { return "", fmt.Errorf("not found: %s", file) }
		executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "az"), nil }
		workingDir = func() (string, error) { return cwd, nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != cwdAzd {
			t.Fatalf("expected %q, got %q", cwdAzd, got)
		}
	})
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
			m.viewMode = ViewModeBoard
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
			if profile.Width >= 80 && !strings.Contains(view, "Space: task workspace") {
				t.Fatalf("expected board hints for %s profile, got: %s", profile.Name, view)
			}
		})
	}
}

func TestView_ShowsHiddenSelectionCount(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.viewMode = ViewModeBoard
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
	m.editor.SetSortField(domain.SortByPriority)
	m.editor.SetSortOrder(domain.SortDesc)

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	next := result.(Model)
	if next.editor.IsFilterActive() {
		t.Fatal("expected esc in normal mode to clear active filters")
	}
	if got := next.editor.GetSort(); got.Field != domain.SortByPriority || got.Order != domain.SortDesc {
		t.Fatalf("expected esc filter clear to preserve sort state, got field=%s order=%v", got.Field, got.Order)
	}
}

func TestRuntimeSignalRefreshTasks_BoardUsesRenderedWindow(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.viewMode = ViewModeBoard
	m.width = 80
	m.height = 9
	m.tasks = make([]domain.Task, 0, 12)
	for i := 1; i <= 12; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       fmt.Sprintf("az-%d", i),
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
	columnCount := m.boardVisibleColumnCount(len(columns))
	columnWidth := m.width / columnCount
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
	m.viewMode = ViewModeCompact
	m.width = 100
	m.height = 10
	m.tasks = make([]domain.Task, 0, 16)
	for i := 1; i <= 16; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       fmt.Sprintf("az-%02d", i),
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

func TestFollowOnMergeSelectionNoEligibleUpstreamShowsToast(t *testing.T) {
	m := newTestModel()
	parentID := "az-parent"
	childID := "az-child"
	m.tasks = []domain.Task{
		{
			ID:          parentID,
			Title:       "Parent",
			Status:      domain.StatusDone,
			Priority:    domain.P1,
			Type:        domain.TypeTask,
			HasWorktree: false,
		},
		{
			ID:          childID,
			Title:       "Child",
			Status:      domain.StatusInProgress,
			Priority:    domain.P1,
			Type:        domain.TypeTask,
			ParentID:    &parentID,
			HasWorktree: true,
		},
	}
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "m"})
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

func TestGetFollowOnMergeCandidatesRequiresUpstreamWorktree(t *testing.T) {
	m := newTestModel()
	parentID := "az-parent"
	childID := "az-child"

	makeTasks := func(parentHasWorktree bool) []domain.Task {
		return []domain.Task{
			{
				ID:          parentID,
				Title:       "Parent",
				Status:      domain.StatusDone,
				Priority:    domain.P1,
				Type:        domain.TypeTask,
				HasWorktree: parentHasWorktree,
			},
			{
				ID:          childID,
				Title:       "Child",
				Status:      domain.StatusInProgress,
				Priority:    domain.P1,
				Type:        domain.TypeTask,
				ParentID:    &parentID,
				HasWorktree: true,
			},
		}
	}

	t.Run("excludes parent without worktree", func(t *testing.T) {
		m.tasks = makeTasks(false)
		target := m.tasks[1]
		candidates := m.getFollowOnMergeCandidates(&target)
		if len(candidates) != 0 {
			t.Fatalf("expected no candidates without upstream worktree, got %+v", candidates)
		}
	})

	t.Run("includes parent with worktree", func(t *testing.T) {
		m.tasks = makeTasks(true)
		target := m.tasks[1]
		candidates := m.getFollowOnMergeCandidates(&target)
		if len(candidates) != 1 {
			t.Fatalf("expected one candidate with upstream worktree, got %+v", candidates)
		}
		if candidates[0].target.ID != parentID {
			t.Fatalf("candidate source id = %q, want %q", candidates[0].target.ID, parentID)
		}
	})
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

func TestHalfPageScroll(t *testing.T) {
	m := newTestModel()

	// Add more tasks to Open column for scrolling
	for i := 0; i < 10; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       string(rune('a' + i)),
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
				ID:       string(rune('a'+i%26)) + string(rune('A'+i/26)),
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
			ID:       string(rune('a' + i)),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	m.height = 24
	m.editor.EnterSelect()

	t.Run("ctrl+d moves selection in select mode", func(t *testing.T) {
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
	})

	t.Run("ctrl+u moves selection in select mode", func(t *testing.T) {
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

	t.Run("enter also opens bulk actions when selection exists", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		m.nav.SelectTask("az-1", 0)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyEnter})
		newModel := result.(Model)

		if _, ok := newModel.overlayStack.Current().(*overlay.BulkActionMenu); !ok {
			t.Fatalf("expected bulk action menu, got %T", newModel.overlayStack.Current())
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

	if view := logOverlay.View(); !strings.Contains(view, "ui.toast") {
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
			ID:       string(rune('a' + i)),
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
	columnCount := board.VisibleColumnCount(len(columns), m.width)
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))
	initialStart, initialEnd := board.VisibleTaskWindow(len(columns[0].Tasks), m.viewportStarts[0], availableHeight, linesPerCard)
	if initialEnd-initialStart < 2 {
		t.Fatalf("expected at least two visible tasks in initial window, got [%d,%d)", initialStart, initialEnd)
	}

	lastVisible := initialEnd - 1
	m.nav.SelectTask(columns[0].Tasks[lastVisible].ID, 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyUp})
	newModel := result.(Model)

	expectedTask := columns[0].Tasks[lastVisible-1].ID
	if got := newModel.nav.GetCursor().TaskID; got != expectedTask {
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
			ID:       fmt.Sprintf("open-%02d", i),
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
	columnCount := board.VisibleColumnCount(len(columns), m.width)
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))

	start := len(columns[0].Tasks) / 2
	windowStart, windowEnd := board.VisibleTaskWindow(len(columns[0].Tasks), start, availableHeight, linesPerCard)
	if windowEnd-windowStart < 2 {
		t.Fatalf("expected at least two visible tasks in test window, got [%d,%d)", windowStart, windowEnd)
	}

	m.viewportStarts[0] = start
	m.nav.SelectTask(columns[0].Tasks[windowEnd-1].ID, 0)

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
	if strings.Contains(view, "Blocked (1)") {
		t.Fatalf("expected blocked column to be out of view when window is on first two columns")
	}

	m.nav.SelectTask("az-4", 2)
	m.ensureCursorVisible(m.buildColumns())
	view = m.renderBoardView()
	if !strings.Contains(view, "In Progress (1)") || !strings.Contains(view, "Blocked (1)") {
		t.Fatalf("expected in progress and blocked columns in shifted narrow view")
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
			ID:       string(rune('a' + i)),
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
		// Start at task in Blocked column
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
				ID:       fmt.Sprintf("boundary-%d", i),
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

	t.Run("gw opens jump labels and selects a double-char target", func(t *testing.T) {
		jumpModel := newTestModel()
		jumpModel.width = 120
		jumpModel.height = 120
		jumpModel.config.Keyboard.JumpLabelChars = "abc"
		jumpModel.editor.EnterGoto()
		jumpModel.nav.SelectTask("az-1", 0)

		for i := 0; i < 5; i++ {
			jumpModel.tasks = append(jumpModel.tasks, domain.Task{
				ID:       fmt.Sprintf("jump-%02d", i),
				Title:    fmt.Sprintf("Jump Task %02d", i),
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}

		result, _ := jumpModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
		newModel := result.(Model)

		current := newModel.overlayStack.Current()
		if current == nil {
			t.Fatal("expected jump overlay to be pushed")
		}
		jump, ok := current.(*overlay.JumpMode)
		if !ok {
			t.Fatalf("overlay type = %T, want *overlay.JumpMode", current)
		}
		if got, want := jump.GetLabel(0), "a"; got != want {
			t.Fatalf("label 0 = %q, want %q", got, want)
		}
		if got, want := jump.GetLabel(3), "aa"; got != want {
			t.Fatalf("label 3 = %q, want %q", got, want)
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

		if cmd := newModel.overlayStack.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}); cmd != nil {
			t.Fatal("expected first jump key to wait for a two-char label")
		}
		cmd := newModel.overlayStack.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
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
		if !finalModel.overlayStack.IsEmpty() {
			t.Fatal("expected jump overlay to close after selection")
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

	closed, _ := m.Update(overlay.CloseOverlayMsg{})
	m = closed.(Model)
	if !m.overlayStack.IsEmpty() {
		t.Fatal("expected overlay stack to be empty after close")
	}

	reopened, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = reopened.(Model)

	second, ok := m.overlayStack.Current().(*overlay.CreateTaskOverlay)
	if !ok || second == nil {
		t.Fatalf("expected create overlay after reopen, got %T", m.overlayStack.Current())
	}
	if second != first {
		t.Fatal("expected create overlay state to persist across close/reopen")
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

	t.Run("tab still toggles board view in normal mode", func(t *testing.T) {
		m.editor.EnterNormal()
		m.viewMode = ViewModeBoard
		m.nav.SelectTask("az-2", 0)
		before := getCursorPosition(m)

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
		compactModel := result.(Model)

		if compactModel.viewMode != ViewModeCompact {
			t.Fatalf("expected tab to toggle to compact view, got %v", compactModel.viewMode)
		}
		if got := getCursorPosition(compactModel); got != before {
			t.Fatalf("cursor position changed after tab to compact view: before=%+v after=%+v", before, got)
		}
		if got := compactModel.toasts[len(compactModel.toasts)-1].Message; got != "Switched to compact view" {
			t.Fatalf("expected compact-view toast, got %q", got)
		}

		result, _ = compactModel.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
		boardModel := result.(Model)

		if boardModel.viewMode != ViewModeBoard {
			t.Fatalf("expected second tab to restore board view, got %v", boardModel.viewMode)
		}
		if got := getCursorPosition(boardModel); got != before {
			t.Fatalf("cursor position changed after tab back to board view: before=%+v after=%+v", before, got)
		}
		if got := boardModel.toasts[len(boardModel.toasts)-1].Message; got != "Switched to board view" {
			t.Fatalf("expected board-view toast, got %q", got)
		}
	})
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

	if got := m.View(); !strings.Contains(got, "Loading issues") {
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
	m.tasks = append(m.tasks,
		domain.Task{
			ID:       epicID,
			Title:    "Parent Epic",
			Status:   domain.StatusOpen,
			Priority: domain.P1,
			Type:     domain.TypeEpic,
		},
		domain.Task{
			ID:       childID,
			Title:    "Epic Child",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			ParentID: &epicID,
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
			renderedIDs = append(renderedIDs, task.ID)
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
	m.tasks = append(m.tasks,
		domain.Task{
			ID:       parentID,
			Title:    "Parent",
			Status:   domain.StatusOpen,
			Priority: domain.P1,
			Type:     domain.TypeEpic,
		},
		domain.Task{
			ID:       childID,
			Title:    "Child",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			ParentID: &parentID,
		},
		domain.Task{
			ID:       grandchildID,
			Title:    "Grandchild",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
			ParentID: &childID,
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
			renderedIDs = append(renderedIDs, task.ID)
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

func TestTaskDetailPanelIncludesTypedDependencies(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()

	currentID := "az-current"
	upstreamID := "az-upstream"
	downstreamID := "az-downstream"

	m.tasks = append(m.tasks,
		domain.Task{
			ID:       currentID,
			Title:    "Current task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: downstreamID, Type: domain.DependencyBlocks},
			},
		},
		domain.Task{
			ID:       upstreamID,
			Title:    "Upstream task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: currentID, Type: domain.DependencyRelatedTo},
			},
		},
		domain.Task{
			ID:       downstreamID,
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
	if !strings.Contains(view, "Outgoing") || !strings.Contains(view, "related <- az-upstream") {
		t.Fatalf("expected typed dependency detail in task panel, got %q", view)
	}
	if !strings.Contains(view, "Current task") {
		t.Fatalf("expected task title in task panel, got %q", view)
	}
	if !strings.Contains(view, "Actions") {
		t.Fatalf("expected action panel to render in task workspace, got %q", view)
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
	m.tasks = append(m.tasks, domain.Task{
		ID:       taskID,
		Title:    "Task with blocks dependency only",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Dependencies: []domain.Dependency{
			{ID: "az-upstream", Type: domain.DependencyBlocks},
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
	m.tasks = []domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-child-parent-id", Title: "Child by parent_id", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, ParentID: &parentID},
		{
			ID:       "az-child-dep",
			Title:    "Child by parent-child dep",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: parentID, Type: domain.DependencyParentChild},
			},
		},
		{
			ID:       "az-blocks-only",
			Title:    "Blocks-only issue",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: parentID, Type: domain.DependencyBlocks},
			},
		},
	}

	// Simulate user turning hide-child filter option off.
	m.editor.ToggleHideEpicChildren()

	columns := m.buildColumns()
	openTasks := columns[domain.StatusOpen.Column()].Tasks
	ids := make(map[string]struct{}, len(openTasks))
	for _, task := range openTasks {
		ids[task.ID] = struct{}{}
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

func TestIssuesLoadedPreservesLocalRuntimeOverlays(t *testing.T) {
	m := newTestModel()
	m.tasks[0].HasTmuxSession = true
	m.tasks[0].HasWorktree = true
	m.tasks[0].GitAheadCount = 2
	m.tasks[0].GitBehindCount = 7
	m.tasks[0].HasUncommittedChanges = true
	m.tasks[0].GitAdditions = 11
	m.tasks[0].GitDeletions = 4

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1 refreshed", Status: domain.StatusBlocked, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-6", Title: "Task 6", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeBug},
		},
		revision: 13,
	})
	newModel := result.(Model)

	task := newModel.tasks[0]
	if task.ID != "az-1" || task.Title != "Task 1 refreshed" || task.Status != domain.StatusBlocked {
		t.Fatalf("refreshed task = %+v", task)
	}
	if !task.HasTmuxSession || !task.HasWorktree || task.GitAheadCount != 2 || task.GitBehindCount != 7 || !task.HasUncommittedChanges || task.GitAdditions != 11 || task.GitDeletions != 4 {
		t.Fatalf("local overlay fields were not preserved: %+v", task)
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
			{ID: "az-1", Title: "Task 1 resurrected", Status: domain.StatusBlocked, Priority: domain.P0, Type: domain.TypeTask},
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

	m.tasks[0].Session = &domain.Session{IssueID: "az-1", Worktree: "/tmp/az-1"}
	m.nav.SelectTask("az-1", 0)

	result, _ := m.handleConflictResolution(overlay.ConflictResolutionMsg{ResolveWithClaude: true})
	newModel := result.(Model)
	if len(newModel.toasts) == 0 {
		t.Fatal("expected tmux-unavailable toast from conflict resolution")
	}
	lastToast := newModel.toasts[len(newModel.toasts)-1]
	if lastToast.Level != ToastWarning {
		t.Fatalf("conflict resolution toast level = %v, want warning", lastToast.Level)
	}
	if !strings.Contains(lastToast.Message, "unavailable outside tmux") {
		t.Fatalf("conflict resolution toast message = %q, want tmux-unavailable guidance", lastToast.Message)
	}
}

func TestHandleConflictResolution_ResolveWithClaudeAttachesSession(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true
	m.tasks[0].Session = &domain.Session{IssueID: "az-1", Worktree: "/tmp/az-1"}
	m.nav.SelectTask("az-1", 0)

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
			if body.SessionID != "az-1" {
				t.Fatalf("session id = %q, want az-1", body.SessionID)
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

	var switchTargets []string
	m.tmuxClient = mockTmuxService{
		switchFn: func(_ context.Context, target string) error {
			switchTargets = append(switchTargets, target)
			return nil
		},
	}

	result, cmd := m.handleConflictResolution(overlay.ConflictResolutionMsg{ResolveWithClaude: true})
	if cmd == nil {
		t.Fatal("expected attach command from resolve with Claude")
	}
	_ = result.(Model)

	msg := cmd()
	attached, ok := msg.(sessionAttachedMsg)
	if !ok {
		t.Fatalf("attach cmd returned %T, want sessionAttachedMsg", msg)
	}
	if attached.issueID != "az-1" {
		t.Fatalf("attached issue id = %q, want az-1", attached.issueID)
	}
	if !attached.switchedTmux {
		t.Fatal("expected tmux switch on conflict resolution attach")
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionAttach {
		t.Fatalf("requests = %v", transport.requests)
	}
	if len(switchTargets) == 0 || switchTargets[0] != "az-1" {
		t.Fatalf("switch targets = %v, want first target az-1", switchTargets)
	}
}

func TestHandleConflictResolution_ResolveWithClaudeDaemonUnavailableFallsBackToManualHint(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.tmuxAvailable = true
	m.daemonClient = nil
	m.tasks[0].Session = &domain.Session{IssueID: "az-1", Worktree: "/tmp/az-1"}
	m.nav.SelectTask("az-1", 0)

	result, cmd := m.handleConflictResolution(overlay.ConflictResolutionMsg{ResolveWithClaude: true})
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

func TestResolveConflictWithAICmd_AttachFailureReturnsManualFallbackMsg(t *testing.T) {
	t.Setenv("TMUX", "client")
	m := newTestModel()
	m.currentProject = "Chefy"
	m.tmuxAvailable = true

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionAttach {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionAttach)
			}
			return protocol.ResponseEnvelope{}, fmt.Errorf("daemon offline")
		},
	}
	m.daemonClient = daemonclient.New(transport)

	msg := m.resolveConflictWithAICmd("az-1")()
	fallback, ok := msg.(conflictResolveFallbackMsg)
	if !ok {
		t.Fatalf("resolveConflictWithAICmd returned %T, want conflictResolveFallbackMsg", msg)
	}
	if fallback.issueID != "az-1" {
		t.Fatalf("fallback issue id = %q, want az-1", fallback.issueID)
	}
	if fallback.err == nil || !strings.Contains(fallback.err.Error(), "daemon offline") {
		t.Fatalf("fallback err = %v, want daemon offline", fallback.err)
	}

	result, _ := m.Update(fallback)
	newModel := result.(Model)
	if len(newModel.toasts) == 0 {
		t.Fatal("expected warning toast for fallback guidance")
	}
	lastToast := newModel.toasts[len(newModel.toasts)-1]
	if lastToast.Level != ToastWarning {
		t.Fatalf("toast level = %v, want warning", lastToast.Level)
	}
	if !strings.Contains(lastToast.Message, "tmux attach-session -t az-1") {
		t.Fatalf("toast message = %q, want manual attach command", lastToast.Message)
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

func TestHandleSelection_AttachUsesTmuxPresenceWithoutSessionProjection(t *testing.T) {
	t.Setenv("TMUX", "")
	m := newTestModel()
	m.tasks[0].HasTmuxSession = true
	m.tasks[0].Session = nil
	m.tmuxAvailable = false
	m.nav.SelectTask(m.tasks[0].ID, 0)

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionAttach {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionAttach)
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

	_, cmd := m.handleSelection(overlay.SelectionMsg{Key: "a"})
	if cmd == nil {
		t.Fatal("expected attach command")
	}
	msg := cmd()
	if _, ok := msg.(sessionAttachedMsg); !ok {
		t.Fatalf("attach cmd returned %T, want sessionAttachedMsg", msg)
	}
}

func TestDaemonSessionUpdatedEventAllowsImmediateAttachFromWorkspace(t *testing.T) {
	t.Setenv("TMUX", "")
	m := newTestModel()
	issueID := m.tasks[0].ID
	m.tasks[0].Session = nil
	m.tmuxAvailable = false
	m.nav.SelectTask(issueID, 0)

	updatedAt := time.Date(2026, time.March, 31, 1, 2, 3, 0, time.UTC)
	body, err := json.Marshal(protocol.SessionProjectionEventBody{
		ProjectID: m.daemonProjectID(),
		Revision:  7,
		Session: protocol.SessionProjection{
			SessionID: "proj-az-1",
			IssueID:   issueID,
			State:     protocol.SessionLifecycleStateAttached,
			UpdatedAt: updatedAt,
		},
	})
	if err != nil {
		t.Fatalf("marshal session projection event: %v", err)
	}

	updatedAny, _ := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			Revision: 7,
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
		if _, behind := msg.(branchBehindMsg); !behind {
			t.Fatalf("attach cmd returned %T, want sessionAttachedMsg or branchBehindMsg", msg)
		}
	}
}

func TestHandleSelection_AttachFromTaskWorkspaceKeepsOverlayOpen(t *testing.T) {
	m := newTestModel()
	task := m.tasks[0]
	task.HasTmuxSession = true
	task.Session = nil
	m.tasks[0] = task
	m.nav.SelectTask(task.ID, 0)

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
	m.nav.SelectTask(task.ID, 0)

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
		ProjectID: m.daemonProjectID(),
		IssueID:   task.ID,
		Worktree:  "/tmp/az-1",
		UpdatedAt: updatedAt,
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: m.daemonProjectID(),
			Revision:  1,
			Projection: protocol.RuntimeProjection{
				ProjectID: m.daemonProjectID(),
				IssueID:   task.ID,
				Worktree: protocol.RuntimeWorktreeProjection{
					Exists:             true,
					Path:               "/tmp/az-1",
					Branch:             "riordan/az-1/task",
					Healthy:            true,
					GitStatusUpdatedAt: &updatedAt,
				},
				Git: protocol.RuntimeGitProjection{
					HasUncommittedChanges: true,
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
			ProjectID: m.daemonProjectID(),
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
	if got.Session == nil || got.Session.Worktree != "/tmp/az-1" || got.Session.State != domain.SessionBusy {
		t.Fatalf("task session = %+v, want busy session with runtime worktree", got.Session)
	}
	if session := updated.sessions[got.ID]; session == nil || session.Worktree != "/tmp/az-1" {
		t.Fatalf("projected session index = %+v, want /tmp/az-1", session)
	}
	if updated.daemonRevision != 1 {
		t.Fatalf("daemonRevision = %d, want 1", updated.daemonRevision)
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
	task := m.tasks[0]
	task.HasWorktree = false
	task.HasUncommittedChanges = false
	task.GitAdditions = 0
	task.GitDeletions = 0
	m.tasks[0] = task
	m.nav.SelectTask(task.ID, 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(task, m.tasks, nil, 120, 30))

	updatedAt := time.Date(2026, time.April, 1, 15, 0, 0, 0, time.UTC)
	body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
		ProjectID: m.daemonProjectID(),
		IssueID:   task.ID,
		Worktree:  "/tmp/wt-az-1",
		UpdatedAt: updatedAt,
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: m.daemonProjectID(),
			Revision:  2,
			Projection: protocol.RuntimeProjection{
				ProjectID: m.daemonProjectID(),
				IssueID:   task.ID,
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
			ProjectID: m.daemonProjectID(),
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

func TestRuntimeSignalsForBoardMarksAncestorCardsWithChildSessions(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"
	grandchildID := "az-grandchild"
	unrelatedID := "az-unrelated"

	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeEpic},
		{ID: childID, Title: "Child", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, ParentID: &parentID},
		{ID: grandchildID, Title: "Grandchild", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, ParentID: &childID, HasTmuxSession: true},
		{ID: unrelatedID, Title: "Unrelated", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
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

func TestSortTasksInColumnSessionSortPromotesAncestorOfActiveChildSession(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"
	otherID := "az-other"

	m := newTestModel()
	m.tasks = []domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeEpic},
		{ID: childID, Title: "Child", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask, ParentID: &parentID, HasTmuxSession: true},
		{ID: otherID, Title: "Other", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.editor.SetSort(&domain.Sort{Field: domain.SortBySession, Order: domain.SortAsc})

	filtered := m.boardVisibleTasks(m.tasks)
	sortedOpen := m.sortTasksInColumn(filtered, domain.StatusOpen)
	if len(sortedOpen) != 2 {
		t.Fatalf("sorted open tasks len = %d, want 2", len(sortedOpen))
	}
	if sortedOpen[0].ID != parentID {
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
	if progress.TargetStatus != "" {
		t.Fatalf("progress target status = %q, want empty", progress.TargetStatus)
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
			IssueID:     "/tmp/wt-az-1",
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
			IssueID:     "/tmp/wt-az-1",
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
		issueID      string
		resourceKeys []string
		wantTaskID   string
	}{
		{
			name:       "session lifecycle issue id",
			issueID:    "az-1",
			wantTaskID: "az-1",
		},
		{
			name:       "git merge uses worktree path issue id",
			issueID:    "/tmp/wt-az-1",
			wantTaskID: "az-1",
		},
		{
			name:         "git fetch uses worktree resource key",
			issueID:      "",
			resourceKeys: []string{"worktree:/tmp/wt-az-2"},
			wantTaskID:   "az-2",
		},
		{
			name:         "worktree remove uses issue resource key",
			issueID:      "__project__",
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
			IssueID:     "/tmp/wt-az-1",
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
			IssueID:     "az-2",
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
