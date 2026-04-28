// Package app contains the main application model and TEA implementation.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/buildinfo"
	autoclient "github.com/riordanpawley/azedarach/internal/client"
	"github.com/riordanpawley/azedarach/internal/client/appdeps"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/daemonprocess"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/logstream"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/editor"
	"github.com/riordanpawley/azedarach/internal/services/navigation"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/eventticker"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// Re-export Mode type and constants for convenience
type Mode = types.Mode

const (
	ModeNormal = types.ModeNormal
	ModeSelect = types.ModeSelect
	ModeSearch = types.ModeSearch
	ModeGoto   = types.ModeGoto
	ModeAction = types.ModeAction
)

const (
	diffPreviewMaxCharacters       = 200
	eventTickerCapacity            = 64
	eventLogCapacity               = 256
	eventSummaryMaxRunes           = 140
	orphanedWorktreeCleanupTimeout = 2 * time.Minute
)

var ansiEscapeLinePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
var diffStatInsertionsPattern = regexp.MustCompile(`(\d+)\s+insertion`)
var diffStatDeletionsPattern = regexp.MustCompile(`(\d+)\s+deletion`)
var executablePath = os.Executable
var lookupPath = exec.LookPath
var processArgs = func() []string { return os.Args }
var workingDir = os.Getwd
var execProcess = tea.ExecProcess
var newScopedDaemonClient = func(socketPath, projectID string, readWaitPolicy daemonclient.ReadWaitPolicy) *daemonclient.Client {
	return daemonclient.New(transport.NewClient(socketPath)).
		WithProjectID(projectID).
		WithReadWaitPolicy(readWaitPolicy)
}

// Re-export Toast type and constants for convenience
type Toast = types.Toast
type ToastLevel = types.ToastLevel

const (
	ToastInfo    = types.ToastInfo
	ToastSuccess = types.ToastSuccess
	ToastWarning = types.ToastWarning
	ToastError   = types.ToastError
)

// Re-export navigation types for compatibility
type Position = navigation.Position

// ViewMode represents the current view mode
type ViewMode int

const (
	ViewModeBoard ViewMode = iota
	ViewModeCompact
)

type drillDownContext struct {
	parentID   string
	parentName string
}

type pendingTaskStatus struct {
	previousStatus domain.Status
	targetStatus   domain.Status
	operationID    string
	state          protocol.OperationState
	action         string
	updatedAt      time.Time
}

type pendingOperationProgress struct {
	operationID string
	state       protocol.OperationState
	percent     int
	message     string
}

// Model is the main application state
type Model struct {
	// Core data
	tasks              []domain.Task
	sessions           map[string]*domain.Session
	suppressedTasks    map[string]struct{}
	pendingStatuses    map[string]pendingTaskStatus
	operationTaskID    map[string]string
	pendingOpsByTask   map[string]pendingOperationProgress
	pendingCleanup     *pendingWorktreeCleanupConfirmation
	pendingBulkCleanup *pendingBulkCleanupConfirmation

	// Navigation (using NavigationService)
	nav *navigation.Service

	// Editor state (mode, filter, sort, selections)
	editor *editor.Service

	// UI state
	overlayStack                *overlay.Stack
	createTaskOverlay           *overlay.CreateTaskOverlay
	viewMode                    ViewMode
	viewportStarts              [board.DefaultColumnCount]int
	columnViewportStart         int
	drillDownParentID           string
	drillDownParentName         string
	drillDownTrail              []drillDownContext
	pendingCreatedTaskID        string
	runtimeSignalsByTask        map[string]board.RuntimeSignals
	runtimeSignalWorktreeByTask map[string]string

	// Project
	currentProject       string
	daemonProjectRouteID naming.ProjectID
	projects             []domain.Project
	repoDir              string
	runtimeRepoDir       string
	logFilePath          string

	// Toasts
	toasts []Toast

	// Runtime event stream (status ticker + event-log overlay source)
	eventTicker   *eventticker.Ring
	runtimeEvents []protocol.EventEnvelope

	// Terminal size
	width  int
	height int

	// Styles
	styles *styles.Styles

	// Configuration
	config *config.Config

	// Loading state
	loading               bool
	boardRefreshing       bool
	issueRefreshSeq       uint64
	projectSwitchSeq      uint64
	projectSwitchInFlight bool
	spinner               spinner.Model
	lastRefresh           time.Time
	taskSnapshotFreshness protocol.TaskListFreshness
	taskSnapshotCheckedAt time.Time
	hasRefreshLoop        bool

	// Shared daemon client for task-domain operations
	daemonClient              *daemonclient.Client
	daemonSocketPath          string
	daemonEvents              <-chan protocol.EventEnvelope
	logStreamEvents           <-chan protocol.EventEnvelope
	daemonRevision            uint64
	lastDaemonReattachAttempt time.Time
	lastLogStreamReattachAt   time.Time
	logStreamReconnectQueued  bool

	// Session management services
	sessionMonitor appdeps.SessionMonitorService
	tmuxAvailable  bool
	tmuxClient     appdeps.TmuxService

	// Git services
	gitClient      diff.DiffClient
	gitSyncService appdeps.GitSyncService
	isOnline       bool

	// Project registry
	projectRegistry *config.ProjectsRegistry

	// Image attachment service
	attachmentService overlay.ImageAttachmentService

	// Diagnostics service
	diagnosticsService overlay.DiagnosticsCollector

	// Logger
	logger *slog.Logger
}

// New creates a new application model with the given config
func New(cfg *config.Config) Model {
	// Initialize spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Blue)

	// Resolve repository directory for local services and daemon routing.
	repoDir, err := os.Getwd()
	if err != nil {
		repoDir = "."
	}
	daemonSocketPath := config.DaemonSocketPathFor(repoDir)
	runtimeRepoDir := repoDir
	if scopedDaemonRuntimeEnabledForJustRun() {
		if normalizedRuntimeRepoDir, normalizeErr := config.ResolveWorktreeRoot(repoDir); normalizeErr == nil {
			runtimeRepoDir = normalizedRuntimeRepoDir
		}
	}
	if normalizedRepoDir, normalizeErr := config.ResolveProjectRoot(repoDir); normalizeErr == nil {
		repoDir = normalizedRepoDir
	}
	logFilePath := resolveTUILogFilePath(cfg)
	logger := newTUILogger(logFilePath)
	slog.SetDefault(logger)
	if err != nil {
		logger.Error("failed to get current directory", "error", err)
	}
	daemonClient := daemonclient.New(transport.NewClient(daemonSocketPath))
	deps := appdeps.Build(cfg, repoDir, logger)

	m := Model{
		tasks:                       []domain.Task{},
		sessions:                    make(map[string]*domain.Session),
		pendingStatuses:             make(map[string]pendingTaskStatus),
		operationTaskID:             make(map[string]string),
		pendingOpsByTask:            make(map[string]pendingOperationProgress),
		nav:                         navigation.NewService(),
		editor:                      editor.NewService(),
		overlayStack:                overlay.NewStack(),
		viewMode:                    ViewModeBoard, // Start with board view
		runtimeSignalsByTask:        make(map[string]board.RuntimeSignals),
		runtimeSignalWorktreeByTask: make(map[string]string),
		toasts:                      []Toast{},
		eventTicker:                 eventticker.NewRing(eventTickerCapacity),
		runtimeEvents:               []protocol.EventEnvelope{},
		styles:                      styles.New(),
		config:                      cfg,
		loading:                     true, // Start with loading state
		spinner:                     s,
		daemonClient:                daemonClient,
		daemonSocketPath:            daemonSocketPath,
		sessionMonitor:              deps.SessionMonitor,
		gitClient:                   deps.GitDiffClient,
		gitSyncService:              deps.GitSyncService,
		projectRegistry:             deps.ProjectRegistry,
		isOnline:                    deps.IsOnline,
		attachmentService:           deps.AttachmentService,
		diagnosticsService:          deps.DiagnosticsService,
		logger:                      logger,
		tmuxAvailable:               deps.TmuxAvailable,
		tmuxClient:                  deps.TmuxClient,
		repoDir:                     repoDir,
		runtimeRepoDir:              runtimeRepoDir,
		logFilePath:                 logFilePath,
		currentProject:              resolveInitialProjectName(deps.ProjectRegistry, repoDir),
	}
	logger.Info("tui runtime initialized", "repo_dir", repoDir, "runtime_repo_dir", runtimeRepoDir, "daemon_socket", daemonSocketPath, "project", m.currentProject)
	m.refreshDaemonProjectRouteID()
	m.daemonClient.WithProjectRouteID(m.daemonProjectRouteIDValue())
	return m
}

// Init returns the initial command for the application
func (m *Model) reconcileCursorAfterIssuesRefresh() {
	columns := m.buildColumns()
	pos := m.nav.GetPosition(columns)
	if !pos.Valid || pos.Column < 0 || pos.Column >= len(columns) {
		return
	}
	col := columns[pos.Column]
	if pos.Task < 0 || pos.Task >= len(col.Tasks) {
		return
	}
	m.nav.SelectTask(col.Tasks[pos.Task].ID.String(), pos.Column)
	m.ensureCursorVisible(columns)
}

func (m *Model) applyPendingCreatedTaskSelection() {
	taskID := strings.TrimSpace(m.pendingCreatedTaskID)
	if taskID == "" {
		return
	}

	columns := m.buildColumns()
	if m.nav.JumpToTaskByID(columns, taskID) {
		m.ensureCursorVisible(columns)
		m.pendingCreatedTaskID = ""
		return
	}

	// Clear the pending jump once the task has hydrated but is not selectable
	// in the current board projection (for example, hidden by board semantics).
	if m.taskExists(taskID) {
		m.pendingCreatedTaskID = ""
	}
}

// handleKey processes keyboard input based on current mode
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Always-available global keys.
	switch msg.String() {
	case "ctrl+c":
		// Cleanup before quitting
		m.sessionMonitor.StopAll()
		return m, tea.Quit
	case "ctrl+l":
		// Force redraw
		return m, tea.ClearScreen
	}

	// Freeze board interactions while switching project contexts.
	if m.projectSwitchInFlight {
		return m, nil
	}

	// Remaining global keys (work in any mode)
	switch msg.String() {
	case "r":
		if m.editor.GetMode() != ModeAction {
			m.boardRefreshing = true
			m.issueRefreshSeq++
			return m, tea.Batch(m.loadIssuesAfterRuntimeReconcileCmd(), m.gitSyncService.FetchAndCheck())
		}
	}

	// Escape closes overlay or exits non-normal modes
	if msg.String() == "esc" {
		if !m.overlayStack.IsEmpty() {
			m.overlayStack.Pop()
			return m, nil
		}
		if m.isDrillDownActive() {
			exitedParentID := m.exitCurrentDrillDown()
			columns := m.buildColumns()
			if exitedParentID != "" {
				m.nav.JumpToTaskByID(columns, exitedParentID)
			}
			m.ensureCursorVisible(columns)
			return m, nil
		}
		if m.editor.IsSelect() {
			m.editor.ClearSelection()
			m.editor.EnterNormal()
			return m, nil
		}
		if m.editor.IsSearch() {
			m.editor.ClearSearch()
			m.editor.EnterNormal()
			return m, nil
		}
		if !m.editor.IsNormal() {
			m.editor.EnterNormal()
			return m, nil
		}
		if m.editor.IsFilterActive() {
			m.editor.ClearFilters()
			columns := m.buildColumns()
			m.ensureCursorVisible(columns)
		}
		return m, nil
	}
	// Mode-specific handling
	switch m.editor.GetMode() {
	case ModeNormal:
		return m.handleNormalMode(msg)
	case ModeGoto:
		return m.handleGotoMode(msg)
	case ModeSearch:
		return m.handleSearchMode(msg)
	case ModeAction:
		return m.handleActionMode(msg)
	case ModeSelect:
		return m.handleSelectMode(msg)
	default:
		return m, nil
	}
}

// handleNormalMode processes keyboard input in normal mode
func (m Model) handleNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	columns := m.buildColumns()
	switch msg.String() {
	case overlay.EventLogHotkey:
		return m, tea.Batch(
			m.openOverlay(overlay.NewEventLogOverlayWithLogFiles(
				m.runtimeEvents,
				m.eventLogFilePath(),
				m.daemonLogFilePath(),
			)),
			m.loadHookLogEventsCmd(),
		)
	case "O": // Orchestration overlay
		return m, m.openOrchestrationOverlay()
	case "X": // Bulk cleanup (Shift+X)
		// Count tasks, worktrees, and sessions for estimates
		taskCount := len(m.tasks)
		worktreeCount := len(m.sessions) // Estimate: active sessions have worktrees
		sessionCount := 0
		for _, session := range m.sessions {
			if session.State == domain.SessionIdle || session.State == domain.SessionPaused {
				sessionCount++
			}
		}
		cleanupOverlay := overlay.NewBulkCleanupOverlay(m.performCleanup, taskCount, worktreeCount, sessionCount)
		return m, m.openOverlay(cleanupOverlay)
	}

	action, ok := keybinds.LookupAction(types.ModeNormal, msg.String())
	if !ok {
		return m, nil
	}

	switch action {
	case keybinds.ActionQuit:
		// Cleanup before quitting
		m.sessionMonitor.StopAll()
		return m, tea.Quit

	// Vertical navigation
	case keybinds.ActionMoveDown:
		m.nav.MoveDown(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionMoveUp:
		m.nav.MoveUp(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	// Horizontal navigation
	case keybinds.ActionMoveLeft:
		m.nav.MoveLeft(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionMoveRight:
		m.nav.MoveRight(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	// Half-page scroll
	case keybinds.ActionHalfPageDown:
		m.nav.HalfPageDown(columns, m.halfPage())
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionHalfPageUp:
		m.nav.HalfPageUp(columns, m.halfPage())
		m.ensureCursorVisible(columns)
		return m, nil

	// Mode switches
	case keybinds.ActionEnterGoto:
		m.editor.EnterGoto()
		return m, nil

	case keybinds.ActionOpenWorkspace: // Space - open task panel (details + actions)
		columns := m.buildColumns()
		task, _ := m.nav.GetCurrentTask(columns)
		if task == nil {
			task, _ = m.getCurrentTaskAndSession()
		}
		if task != nil {
			// Resolve from authoritative task projection to avoid opening the
			// workspace with a stale navigation-copy task payload.
			if latestTask, _, ok := m.taskAndSessionByID(task.ID.String()); ok && latestTask != nil {
				task = latestTask
			}
			workspace := overlay.NewTaskWorkspaceOverlay(*task, m.tasks, m.pendingMutationForTask(task.ID.String()), m.width, m.height)
			workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
			if m.daemonClient == nil {
				return m, m.openOverlay(workspace)
			}
			return m, tea.Batch(
				m.openOverlay(workspace),
				m.refreshTaskWorkspaceInBackgroundCmd(task.ID.String()),
			)
		}
		return m, nil

	case keybinds.ActionEnterSearch: // Search
		m.editor.EnterSearch()
		return m, m.openOverlay(overlay.NewSearchOverlay())

	case keybinds.ActionOpenFilter: // Filter menu
		return m, m.openOverlay(overlay.NewFilterMenu(m.editor.GetFilter()))

	case keybinds.ActionOpenSort: // Sort menu
		return m, m.openOverlay(overlay.NewSortMenu(m.editor.GetSort()))

	case keybinds.ActionEnterSelect: // Visual select
		m.editor.EnterSelect()
		return m, nil

	case keybinds.ActionOpenHelp: // Help
		return m, m.openOverlay(overlay.NewHelpOverlay())

	case keybinds.ActionDrillDown: // Drill into children
		task, _ := m.getCurrentTaskAndSession()
		if task != nil {
			children := m.getTaskChildren(task.ID.String())
			if len(children) > 0 {
				m.enterDrillDown(task.ID.String(), task.Title)
				columns := m.buildColumns()
				m.nav.JumpToTaskByID(columns, children[0].ID.String())
				m.ensureCursorVisible(columns)
				return m, nil
			}
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "No children to drill into (use Space for details/actions)",
				Expires: time.Now().Add(2 * time.Second),
			})
		}
		return m, nil

	case keybinds.ActionCreateTask: // Create task
		var parentID *string
		if drillDownParentID := strings.TrimSpace(m.drillDownParentID); drillDownParentID != "" {
			parentID = &drillDownParentID
		}
		if m.createTaskOverlay == nil {
			m.createTaskOverlay = overlay.NewCreateTaskOverlayWithParentImplOptionsAndAttachmentService(parentID, m.availableTaskImplementations(), m.attachmentService)
		} else {
			m.createTaskOverlay.SetAttachmentService(m.attachmentService)
			m.createTaskOverlay.SetParentID(parentID)
		}
		return m, m.openOverlay(m.createTaskOverlay)

	case keybinds.ActionOpenSettings: // Settings
		return m, m.openOverlay(overlay.NewSettingsOverlayWithEditorAndConfig(m.editor, m.config, m.configSourcePath()))

	case keybinds.ActionOpenDiagnostic: // Diagnostics (Shift+D)
		diagPanel := overlay.NewDiagnosticsPanel(m.diagnosticsService, m.sessions)
		return m, m.openOverlay(diagPanel)

	case keybinds.ActionToggleView: // Toggle view mode
		if m.viewMode == ViewModeBoard {
			m.viewMode = ViewModeCompact
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Switched to compact view",
				Expires: time.Now().Add(2 * time.Second),
			})
		} else {
			m.viewMode = ViewModeBoard
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Switched to board view",
				Expires: time.Now().Add(2 * time.Second),
			})
		}
		return m, nil
	}

	return m, nil
}

// handleGotoMode processes keyboard input in goto mode
func (m Model) handleGotoMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	columns := m.buildColumns()
	// Always return to normal mode after processing
	m.editor.EnterNormal()

	action, ok := keybinds.LookupAction(types.ModeGoto, msg.String())
	if !ok {
		return m, nil
	}

	switch action {
	case keybinds.ActionGotoTop:
		// Go to top of column
		m.nav.GotoTop(columns)
		m.ensureCursorVisible(columns)
	case keybinds.ActionGotoBottom:
		// Go to end of column
		m.nav.GotoBottom(columns)
		m.ensureCursorVisible(columns)
	case keybinds.ActionGotoFirstCol:
		// Go to first column
		m.nav.GotoFirstColumn(columns)
		m.ensureCursorVisible(columns)
	case keybinds.ActionGotoLastCol:
		// Go to last column
		m.nav.GotoLastColumn(columns)
		m.ensureCursorVisible(columns)
	case keybinds.ActionGotoJump:
		// Jump mode - quick navigation with labels for VISIBLE tasks only
		// Calculate visible tasks per column based on screen height/card footprint.
		visibleStart, visibleEnd := m.boardVisibleColumnRange(columns)
		visibleColumns := columns[visibleStart:visibleEnd]
		columnCount := len(visibleColumns)
		if columnCount < 1 {
			columnCount = board.DefaultColumnCount
		}
		columnWidth := m.width / columnCount
		cardWidth := board.CardContentWidth(columnWidth)
		linesPerCard := board.CardLineFootprint(m.styles, cardWidth)
		availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
		visiblePerColumn := availableHeight / linesPerCard
		if visiblePerColumn < 1 {
			visiblePerColumn = 1
		}

		// Count visible tasks (capped by actual task count per column)
		visibleCount := 0
		for _, col := range visibleColumns {
			colVisible := len(col.Tasks)
			if colVisible > visiblePerColumn {
				colVisible = visiblePerColumn
			}
			visibleCount += colVisible
		}
		return m, m.openOverlay(overlay.NewJumpModeWithChars(visibleCount, m.config.Keyboard.JumpLabelChars))
	case keybinds.ActionGotoProjects:
		// Project selector
		return m, m.openOverlay(overlay.NewProjectSelectorWithOptions(
			m.projectRegistry,
			overlay.WithInitialCursor(m.projectSelectorCursor()),
			overlay.WithCurrentProjectName(m.currentProject),
		))
	case keybinds.ActionGotoSpec:
		// Dedicated Spec workspace
		return m, m.openOverlay(overlay.NewSpecWorkspaceOverlay(m.currentProject))
	}

	return m, nil
}

// handleSearchMode processes keyboard input in search mode
func (m Model) handleSearchMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editor.ClearSearch()
		m.editor.EnterNormal()
	case "enter":
		m.editor.EnterNormal()
	}

	return m, nil
}

// handleActionMode processes keyboard input in action mode
func (m Model) handleActionMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	task, session := m.getCurrentTaskAndSession()
	switch msg.String() {
	case "b":
		return m, m.openMergeTargetSelection(task)
	case "m":
		return m, m.followOnMergeSelectionCmd(task, session)
	case "u":
		if task == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No focused issue to update",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		return m, m.updateFromBaseCmd(task.ID.String(), worktreeHint, false)
	case "P":
		m.addToast(Toast{
			Level: ToastWarning,
			Message: "Action unavailable in go-bubbletea action mode; no git operation was started. " +
				"If your repository has an active merge/rebase/cherry-pick/revert state, run git status and continue/abort it first.",
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil
	}
	return m, nil
}

// handleSelectMode processes keyboard input in select mode
func (m Model) handleSelectMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	columns := m.buildColumns()
	task, _ := m.getCurrentTaskAndSession()
	action, ok := keybinds.LookupAction(types.ModeSelect, msg.String())
	if !ok {
		return m, nil
	}
	switch action {
	// Navigation with selection toggle
	case keybinds.ActionMoveDown:
		// Keep current task selected, then move down.
		if task != nil {
			m.editor.Select(task.ID.String())
		}
		m.nav.MoveDown(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionMoveUp:
		// Keep current task selected, then move up.
		if task != nil {
			m.editor.Select(task.ID.String())
		}
		m.nav.MoveUp(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	// Horizontal movement (no selection toggle)
	case keybinds.ActionMoveLeft:
		m.nav.MoveLeft(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionMoveRight:
		m.nav.MoveRight(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	// Half-page movement with selection toggle
	case keybinds.ActionHalfPageDown:
		if task != nil {
			m.editor.Select(task.ID.String())
		}
		m.nav.HalfPageDown(columns, m.halfPage())
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionHalfPageUp:
		if task != nil {
			m.editor.Select(task.ID.String())
		}
		m.nav.HalfPageUp(columns, m.halfPage())
		m.ensureCursorVisible(columns)
		return m, nil

	// Toggle current selection without moving.
	case keybinds.ActionSelectToggle:
		if task != nil {
			m.editor.ToggleSelection(task.ID.String())
		}
		return m, nil

	// Select all in current column
	case keybinds.ActionSelectColumnAll:
		status := m.nav.GetCurrentStatus(columns)
		for _, t := range m.tasks {
			if t.Status == status {
				m.editor.Select(t.ID.String())
			}
		}
		return m, nil

	// Select all visible tasks
	case keybinds.ActionSelectAllVisible:
		filteredTasks := m.editor.ApplyFilter(m.tasks)
		m.editor.SelectAll(filteredTasks)
		return m, nil

	// Invert visible selection
	case keybinds.ActionSelectInvert:
		for _, t := range m.editor.ApplyFilter(m.tasks) {
			if m.editor.IsSelected(t.ID.String()) {
				m.editor.Deselect(t.ID.String())
			} else {
				m.editor.Select(t.ID.String())
			}
		}
		return m, nil

	// Clear selection
	case keybinds.ActionSelectClear:
		m.editor.ClearSelection()
		return m, nil

	// Exit select mode and clear selection
	case keybinds.ActionSelectExit:
		m.editor.ClearSelection()
		m.editor.EnterNormal()
		return m, nil

	// Bulk action menu for selected tasks.
	case keybinds.ActionSelectBulk:
		if m.editor.HasSelection() {
			selectedIDs := m.editor.GetSelectedTasksList()
			return m, m.openOverlay(overlay.NewBulkActionMenu(selectedIDs, len(selectedIDs)))
		}
		return m, nil
	}

	return m, nil
}

// handleOverlayKey routes keyboard messages to the overlay stack
func (m Model) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd := m.overlayStack.Update(msg)
	return m, cmd
}

// Message types for async operations

type issuesLoadedMsg struct {
	refreshSeq    uint64
	projectID     string
	tasks         []domain.Task
	revision      uint64
	lastCheckedAt time.Time
	freshness     protocol.TaskListFreshness
	events        <-chan protocol.EventEnvelope
	daemonClient  *daemonclient.Client
	daemonSocket  string
	stale         bool
	freshnessHint string
	reconcileWarn error
}

type issuesErrorMsg struct {
	refreshSeq uint64
	projectID  string
	err        error
}

type projectSwitchResultMsg struct {
	switchSeq     uint64
	project       config.Project
	projectConfig *config.Config
	tasks         []domain.Task
	revision      uint64
	lastCheckedAt time.Time
	freshness     protocol.TaskListFreshness
	events        <-chan protocol.EventEnvelope
	daemonClient  *daemonclient.Client
	daemonSocket  string
	err           error
}

type daemonStreamEventMsg struct {
	stream <-chan protocol.EventEnvelope
	event  protocol.EventEnvelope
}

type daemonStreamClosedMsg struct {
	stream <-chan protocol.EventEnvelope
}

type logStreamAttachedMsg struct {
	stream <-chan protocol.EventEnvelope
	err    error
}

type logStreamEventMsg struct {
	stream <-chan protocol.EventEnvelope
	event  protocol.EventEnvelope
}

type logStreamClosedMsg struct {
	stream <-chan protocol.EventEnvelope
}

type logStreamReconnectMsg struct{}

type hookLogLoadedMsg struct {
	events []protocol.HookLogEvent
	err    error
}

type tickMsg time.Time

type daemonEventDecision int

const (
	daemonEventIgnore daemonEventDecision = iota
	daemonEventRefreshSnapshot
	daemonEventRehydrate
)

type sessionStartedMsg struct {
	issueID     string
	operationID string
	state       protocol.OperationState
}

type sessionStoppedMsg struct {
	issueID     string
	operationID string
	state       protocol.OperationState
}

type sessionErrorMsg struct {
	issueID string
	err     error
}

type conflictResolveFallbackMsg struct {
	issueID string
	err     error
}

func pendingOperationDetails(err error) (*daemonclient.OperationPendingError, bool) {
	var pending *daemonclient.OperationPendingError
	if !errors.As(err, &pending) {
		return nil, false
	}
	return pending, true
}

func operationStateTerminal(state protocol.OperationState) bool {
	switch state {
	case protocol.OperationStateDone,
		protocol.OperationStateFailed,
		protocol.OperationStateCancelled:
		return true
	default:
		return false
	}
}

func formatPendingOperationMessage(action, issueID, operationID string, state protocol.OperationState) string {
	if issueID != "" {
		return fmt.Sprintf("%s %s for %s (operation %s)", action, state, issueID, operationID)
	}
	return fmt.Sprintf("%s %s (operation %s)", action, state, operationID)
}

func (m *Model) applyOperationProgressEvent(evt protocol.EventEnvelope) {
	switch evt.Event {
	case protocol.EventOperationQueued, protocol.EventOperationRunning, protocol.EventOperationDone, protocol.EventOperationFailed, protocol.EventOperationCancelled:
		var body protocol.OperationEventBody
		if err := json.Unmarshal(evt.Body, &body); err != nil {
			return
		}
		if strings.TrimSpace(body.Operation.OperationID.String()) == "" {
			return
		}
		taskID := m.resolveOperationTaskID(body.Operation.IssueID, body.Operation.ResourceKeys)
		if taskID == "" {
			taskID = m.operationTaskID[body.Operation.OperationID.String()]
		}
		if taskID == "" {
			return
		}
		m.operationTaskID[body.Operation.OperationID.String()] = taskID
		state := protocol.OperationState(body.Operation.State)
		if operationStateTerminal(state) {
			delete(m.pendingOpsByTask, taskIDKey(taskID))
			delete(m.operationTaskID, body.Operation.OperationID.String())
			m.syncTaskWorkspaceOverlay()
			return
		}
		percent := 0
		switch state {
		case protocol.OperationStateRunning:
			percent = 50
		}
		m.pendingOpsByTask[taskIDKey(taskID)] = pendingOperationProgress{
			operationID: body.Operation.OperationID.String(),
			state:       state,
			percent:     percent,
		}
		m.syncTaskWorkspaceOverlay()
	case protocol.EventOperationProgress:
		var body protocol.OperationProgressEventBody
		if err := json.Unmarshal(evt.Body, &body); err != nil {
			return
		}
		if strings.TrimSpace(body.OperationID.String()) == "" {
			return
		}
		taskID := m.operationTaskID[body.OperationID.String()]
		if taskID == "" {
			return
		}
		if operationStateTerminal(body.State) {
			delete(m.pendingOpsByTask, taskIDKey(taskID))
			delete(m.operationTaskID, body.OperationID.String())
			m.syncTaskWorkspaceOverlay()
			return
		}
		m.pendingOpsByTask[taskIDKey(taskID)] = pendingOperationProgress{
			operationID: body.OperationID.String(),
			state:       body.State,
			percent:     clampOperationPercent(body.Progress.Percent),
			message:     strings.TrimSpace(body.Progress.Message),
		}
		m.syncTaskWorkspaceOverlay()
	}
}

func clampOperationPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func (m Model) resolveOperationTaskID(issueID naming.IssueID, resourceKeys []string) string {
	trimmedIssueID := strings.TrimSpace(issueID.String())
	if taskID := m.lookupTaskID(trimmedIssueID); taskID != "" {
		return taskID
	}
	if taskID := m.lookupTaskIDByWorktree(trimmedIssueID); taskID != "" {
		return taskID
	}
	for _, key := range resourceKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if strings.HasPrefix(key, "issue:") {
			parts := strings.Split(key, ":")
			if len(parts) > 0 {
				candidate := strings.TrimSpace(parts[len(parts)-1])
				if taskID := m.lookupTaskID(candidate); taskID != "" {
					return taskID
				}
			}
		}
		if strings.HasPrefix(key, "worktree:") {
			worktree := strings.TrimPrefix(key, "worktree:")
			if taskID := m.lookupTaskIDByWorktree(worktree); taskID != "" {
				return taskID
			}
		}
	}
	return ""
}

func (m Model) lookupTaskID(candidate string) string {
	key := taskIDKey(candidate)
	if key == "" {
		return ""
	}
	for _, task := range m.tasks {
		if taskIDKey(task.ID.String()) == key {
			return task.ID.String()
		}
	}
	return ""
}

func (m Model) lookupTaskIDByWorktree(worktree string) string {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return ""
	}
	for taskID, path := range m.runtimeSignalWorktreeByTask {
		if strings.TrimSpace(path) == worktree {
			return taskID
		}
	}
	for taskID, session := range m.sessions {
		if session == nil {
			continue
		}
		if strings.TrimSpace(session.Worktree) == worktree {
			return taskID
		}
	}
	for _, task := range m.tasks {
		if task.Session != nil && strings.TrimSpace(task.Session.Worktree) == worktree {
			return task.ID.String()
		}
	}
	return ""
}

// Commands

// loadIssuesCmd returns a command that fetches issues from the CLI
func (m Model) loadIssuesCmd() tea.Cmd {
	projectID := m.daemonProjectID()
	refreshSeq := m.issueRefreshSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if m.daemonClient == nil {
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: fmt.Errorf("daemon client unavailable")}
		}

		snapshot, err := m.readTaskSnapshot(ctx, m.daemonClient)
		if err != nil {
			var timeoutErr *daemonclient.ReadWaitTimeoutError
			if errors.As(err, &timeoutErr) {
				return issuesLoadedMsg{
					refreshSeq:    refreshSeq,
					projectID:     projectID,
					stale:         true,
					freshnessHint: timeoutErr.Hint,
				}
			}
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: err}
		}
		return issuesLoadedMsg{
			refreshSeq:    refreshSeq,
			projectID:     projectID,
			tasks:         snapshot.Tasks,
			revision:      snapshot.Revision,
			lastCheckedAt: snapshot.LastCheckedAt,
			freshness:     snapshot.Freshness,
		}
	}
}

func (m Model) loadIssuesAfterRuntimeReconcileCmd() tea.Cmd {
	projectID := m.daemonProjectID()
	refreshSeq := m.issueRefreshSeq
	return func() tea.Msg {
		if m.daemonClient == nil {
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: fmt.Errorf("daemon client unavailable")}
		}

		reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 8*time.Second)
		reconcileWarn := error(nil)
		if _, err := m.daemonClient.ReconcileRuntime(reconcileCtx); err != nil {
			reconcileWarn = err
		}
		reconcileCancel()

		snapshotCtx, snapshotCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer snapshotCancel()
		snapshot, err := m.readTaskSnapshot(snapshotCtx, m.daemonClient)
		if err != nil {
			var timeoutErr *daemonclient.ReadWaitTimeoutError
			if errors.As(err, &timeoutErr) {
				return issuesLoadedMsg{
					refreshSeq:    refreshSeq,
					projectID:     projectID,
					stale:         true,
					freshnessHint: timeoutErr.Hint,
					reconcileWarn: reconcileWarn,
				}
			}
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: err}
		}

		return issuesLoadedMsg{
			refreshSeq:    refreshSeq,
			projectID:     projectID,
			tasks:         snapshot.Tasks,
			revision:      snapshot.Revision,
			lastCheckedAt: snapshot.LastCheckedAt,
			freshness:     snapshot.Freshness,
			reconcileWarn: reconcileWarn,
		}
	}
}

func (m Model) loadHookLogEventsCmd() tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return hookLogLoadedMsg{err: fmt.Errorf("daemon client unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		events, err := m.daemonClient.ListHookLogEvents(ctx, 200)
		if err != nil {
			return hookLogLoadedMsg{err: err}
		}
		return hookLogLoadedMsg{events: events}
	}
}

func parseDiffStatTotals(diffStat string) (int, int) {
	var additions, deletions int
	insertionMatches := diffStatInsertionsPattern.FindAllStringSubmatch(diffStat, -1)
	for _, insertionMatch := range insertionMatches {
		if len(insertionMatch) != 2 {
			continue
		}
		if parsed, err := strconv.Atoi(insertionMatch[1]); err == nil {
			additions += parsed
		}
	}
	deletionMatches := diffStatDeletionsPattern.FindAllStringSubmatch(diffStat, -1)
	for _, deletionMatch := range deletionMatches {
		if len(deletionMatch) != 2 {
			continue
		}
		if parsed, err := strconv.Atoi(deletionMatch[1]); err == nil {
			deletions += parsed
		}
	}
	return additions, deletions
}

func taskIDKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (m *Model) suppressTaskHydration(taskID string) {
	key := taskIDKey(taskID)
	if key == "" {
		return
	}
	if m.suppressedTasks == nil {
		m.suppressedTasks = make(map[string]struct{})
	}
	m.suppressedTasks[key] = struct{}{}
}

func (m Model) isTaskHydrationSuppressed(taskID string) bool {
	if len(m.suppressedTasks) == 0 {
		return false
	}
	_, ok := m.suppressedTasks[taskIDKey(taskID)]
	return ok
}

func (m Model) filterSuppressedHydratedTasks(tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 || len(m.suppressedTasks) == 0 {
		return tasks
	}

	filtered := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if m.isTaskHydrationSuppressed(task.ID.String()) {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func removeTaskByID(tasks []domain.Task, taskID string) []domain.Task {
	if len(tasks) == 0 {
		return tasks
	}

	target := taskIDKey(taskID)
	filtered := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if taskIDKey(task.ID.String()) == target {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func (m Model) daemonClientForSocket(socketPath, projectID string) *daemonclient.Client {
	routeID := naming.ProjectID(protocol.NormalizeProjectID(projectID))
	if parsed, err := naming.ParseProjectID(routeID.String()); err == nil {
		routeID = parsed
	}
	if m.daemonClient != nil {
		if strings.TrimSpace(m.daemonSocketPath) == "" || m.daemonSocketPath == socketPath {
			return m.daemonClient.WithProjectRouteID(routeID)
		}
	}
	readWaitPolicy := daemonclient.DefaultReadWaitPolicy()
	if m.daemonClient != nil {
		readWaitPolicy = m.daemonClient.ReadWaitPolicy()
	}
	return newScopedDaemonClient(socketPath, routeID.String(), readWaitPolicy)
}

func (m Model) readTaskSnapshot(ctx context.Context, client *daemonclient.Client) (daemonclient.TaskSnapshot, error) {
	if client == nil {
		return daemonclient.TaskSnapshot{}, fmt.Errorf("daemon client unavailable")
	}
	return client.ListTasksSnapshotWithMode(ctx, daemonclient.ReadWaitModeExplicit)
}

func (m Model) switchProjectCmd(project config.Project) tea.Cmd {
	switchSeq := m.projectSwitchSeq
	return func() tea.Msg {
		if m.daemonClient == nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project:   project,
				err:       fmt.Errorf("daemon client unavailable"),
			}
		}
		if strings.TrimSpace(project.Path) == "" {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project:   project,
				err:       fmt.Errorf("project %q has empty path", project.Name),
			}
		}
		projectConfig, err := config.LoadConfig(project.Path)
		if err != nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project:   project,
				err:       fmt.Errorf("load config for project %q: %w", project.Name, err),
			}
		}
		startedAt := time.Now()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		socketPath := strings.TrimSpace(m.daemonSocketPath)
		if socketPath == "" {
			socketPath = config.DaemonSocketPathFor(m.activeProjectPath())
		}
		projectRouteID, ok := daemonProjectRouteIDForPath(project.Path)
		if !ok {
			projectRouteID = naming.ProjectID(protocol.NormalizeProjectID(project.Name))
		}
		daemonClient := m.daemonClientForSocket(socketPath, projectRouteID.String())

		snapshot, err := m.readTaskSnapshot(ctx, daemonClient)
		if err != nil {
			if m.logger != nil {
				m.logger.Warn("project switch snapshot failed", "from_project", m.currentProject, "to_project", project.Name, "socket", socketPath, "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
			}
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project:   project,
				err:       err,
			}
		}
		events, err := daemonClient.Subscribe(context.Background(), projectRouteID.String(), snapshot.Revision)
		if err != nil {
			if m.logger != nil {
				m.logger.Warn("project switch subscribe failed", "to_project", project.Name, "to_project_route", projectRouteID.String(), "revision", snapshot.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
			}
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project:   project,
				err:       err,
			}
		}
		if m.logger != nil {
			m.logger.Info("project switch snapshot loaded", "from_project", m.currentProject, "to_project", project.Name, "to_project_route", projectRouteID.String(), "revision", snapshot.Revision, "task_count", len(snapshot.Tasks), "elapsed_ms", time.Since(startedAt).Milliseconds())
		}

		return projectSwitchResultMsg{
			switchSeq:     switchSeq,
			project:       project,
			projectConfig: projectConfig,
			tasks:         snapshot.Tasks,
			revision:      snapshot.Revision,
			lastCheckedAt: snapshot.LastCheckedAt,
			freshness:     snapshot.Freshness,
			events:        events,
			daemonClient:  daemonClient,
			daemonSocket:  socketPath,
		}
	}
}

func (m Model) attachDaemonCmd() tea.Cmd {
	projectID := m.daemonProjectID()
	targetRepoDir := m.activeProjectPath()
	if scopedDaemonRuntimeEnabledForJustRun() && strings.TrimSpace(m.runtimeRepoDir) != "" {
		targetRepoDir = m.runtimeRepoDir
	}
	return func() tea.Msg {
		if m.daemonClient == nil {
			return issuesErrorMsg{projectID: projectID, err: fmt.Errorf("daemon client unavailable")}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		socketPath := config.DaemonSocketPathFor(targetRepoDir)
		daemonClient := m.daemonClientForSocket(socketPath, projectID)
		launcher := daemonprocess.NewLauncher(targetRepoDir, socketPath).WithLogger(m.logger)
		if bin := resolveDaemonBinaryForRepo(targetRepoDir); bin != "" {
			launcher.BinPath = bin
		}

		// Avoid unconditional daemon replacement on every reattach attempt.
		// EnsureAttached will start or replace only when protocol handshake
		// indicates it is required.

		orch := autoclient.NewAutostartOrchestrator(autoclient.NewDaemonHandshaker(daemonClient), launcher)
		ack, err := orch.EnsureAttached(ctx, protocol.Hello{
			ProtocolVersion: protocol.CurrentVersion,
			ClientName:      "tui",
			ClientVersion:   buildinfo.VersionString(),
			Capabilities:    []string{"snapshot", "subscribe"},
		})
		if err != nil {
			if m.logger != nil {
				m.logger.Warn("daemon attach failed", "project_id", projectID, "target_repo_dir", targetRepoDir, "socket", socketPath, "error", err)
			}
			return issuesErrorMsg{projectID: projectID, err: fmt.Errorf("daemon attach: %w", err)}
		}
		if !ack.Accepted {
			return issuesErrorMsg{projectID: projectID, err: fmt.Errorf("daemon handshake rejected: %s", ack.Reason)}
		}

		snapshot, err := m.readTaskSnapshot(ctx, daemonClient)
		if err != nil {
			if m.logger != nil {
				m.logger.Warn("daemon attach snapshot failed", "project_id", projectID, "target_repo_dir", targetRepoDir, "error", err)
			}
			return issuesErrorMsg{projectID: projectID, err: err}
		}

		events, err := daemonClient.Subscribe(context.Background(), projectID, snapshot.Revision)
		if err != nil {
			if m.logger != nil {
				m.logger.Warn("daemon attach subscribe failed", "project_id", projectID, "revision", snapshot.Revision, "error", err)
			}
			return issuesErrorMsg{projectID: projectID, err: err}
		}
		if m.logger != nil {
			m.logger.Info("daemon attach success", "project_id", projectID, "target_repo_dir", targetRepoDir, "revision", snapshot.Revision, "task_count", len(snapshot.Tasks))
		}

		return issuesLoadedMsg{
			projectID:     projectID,
			tasks:         snapshot.Tasks,
			revision:      snapshot.Revision,
			lastCheckedAt: snapshot.LastCheckedAt,
			freshness:     snapshot.Freshness,
			events:        events,
			daemonClient:  daemonClient,
			daemonSocket:  socketPath,
		}
	}
}

func (m Model) activeProjectPath() string {
	if strings.TrimSpace(m.repoDir) != "" {
		return m.repoDir
	}
	if m.projectRegistry != nil && m.currentProject != "" {
		if project, err := m.projectRegistry.Get(m.currentProject); err == nil && strings.TrimSpace(project.Path) != "" {
			return project.Path
		}
	}
	return "."
}

func (m *Model) rebindProjectContext(project config.Project, projectConfig *config.Config) {
	if projectConfig != nil {
		m.config = projectConfig
	}
	if m.config == nil {
		m.config = config.DefaultConfig()
	}
	m.currentProject = project.Name
	m.repoDir = project.Path
	m.refreshDaemonProjectRouteID()
	m.rebuildProjectScopedServices()
	if m.daemonClient != nil {
		m.daemonClient.WithProjectRouteID(m.daemonProjectRouteIDValue())
	}
}

func (m *Model) rebuildProjectScopedServices() {
	deps := appdeps.Build(m.config, m.repoDir, m.logger)
	m.gitSyncService = deps.GitSyncService
	m.gitClient = deps.GitDiffClient
	m.attachmentService = deps.AttachmentService
	if m.createTaskOverlay != nil {
		m.createTaskOverlay.SetAttachmentService(m.attachmentService)
	}
	m.diagnosticsService = deps.DiagnosticsService
	m.projectRegistry = deps.ProjectRegistry
}

func (m Model) waitForDaemonEventCmd() tea.Cmd {
	stream := m.daemonEvents
	return func() tea.Msg {
		if stream == nil {
			return nil
		}

		evt, ok := <-stream
		if !ok {
			return daemonStreamClosedMsg{stream: stream}
		}

		return daemonStreamEventMsg{stream: stream, event: evt}
	}
}

func (m Model) attachLogStreamCmd() tea.Cmd {
	const noCatchupRevision = ^uint64(0)
	return func() tea.Msg {
		if m.daemonClient == nil {
			return logStreamAttachedMsg{err: fmt.Errorf("daemon client unavailable")}
		}
		events, err := m.daemonClient.Subscribe(context.Background(), protocol.GlobalEventStreamProjectID, noCatchupRevision)
		if err != nil {
			return logStreamAttachedMsg{err: err}
		}
		return logStreamAttachedMsg{stream: events}
	}
}

func (m Model) queueLogStreamReconnectCmd(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return logStreamReconnectMsg{}
	})
}

func (m Model) waitForLogStreamEventCmd() tea.Cmd {
	stream := m.logStreamEvents
	return func() tea.Msg {
		if stream == nil {
			return nil
		}
		evt, ok := <-stream
		if !ok {
			return logStreamClosedMsg{stream: stream}
		}
		return logStreamEventMsg{stream: stream, event: evt}
	}
}

func (m *Model) applySessionProjectionEvent(evt protocol.EventEnvelope) {
	if evt.Event != protocol.EventSessionUpdated {
		return
	}

	var body protocol.SessionProjectionEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		if m.logger != nil {
			m.logger.Warn("decode session projection event failed", "event", evt.Event, "revision", evt.Revision, "error", err)
		}
		return
	}
	if projectID := strings.TrimSpace(body.ProjectID.String()); projectID != "" && projectID != m.daemonProjectID() {
		return
	}
	m.applyRuntimeProjectionFromSessionEvent(body)
	m.reconcilePendingStatuses()
}

func (m Model) reduceDaemonEvent(evt protocol.EventEnvelope) daemonEventDecision {
	cursor := protocol.StreamCursor{Revision: m.daemonRevision}
	switch reconnect.DecideProjectionAction(cursor, evt) {
	case reconnect.ProjectionReconciliationIgnore:
		return daemonEventIgnore
	case reconnect.ProjectionReconciliationRehydrate:
		return daemonEventRehydrate
	default:
		return daemonEventRefreshSnapshot
	}
}

func (m *Model) refreshDaemonProjectRouteID() {
	m.daemonProjectRouteID = m.computeDaemonProjectRouteID()
}

func (m Model) daemonProjectRouteIDValue() naming.ProjectID {
	return m.computeDaemonProjectRouteID()
}

func (m Model) daemonProjectID() string {
	return m.daemonProjectRouteIDValue().String()
}

func (m Model) computeDaemonProjectRouteID() naming.ProjectID {
	if m.currentProject != "" && m.projectRegistry != nil {
		if project, err := m.projectRegistry.Get(m.currentProject); err == nil {
			if projectID, ok := daemonProjectRouteIDForPath(project.Path); ok {
				return projectID
			}
		}
	}
	if projectPath := strings.TrimSpace(m.activeProjectPath()); projectPath != "" {
		if m.currentProject == "" || strings.EqualFold(filepath.Base(projectPath), strings.TrimSpace(m.currentProject)) {
			if projectID, ok := daemonProjectRouteIDForPath(projectPath); ok {
				return projectID
			}
		}
	}
	if normalizedCurrent := protocol.NormalizeProjectID(m.currentProject); strings.TrimSpace(normalizedCurrent) != "" {
		if projectID, err := naming.ParseProjectID(normalizedCurrent); err == nil {
			return projectID
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if projectID, ok := daemonProjectRouteIDForPath(cwd); ok {
			return projectID
		}
		if fallback, parseErr := naming.ParseProjectID(protocol.NormalizeProjectID(filepath.Base(cwd))); parseErr == nil {
			return fallback
		}
	}
	defaultProjectID, err := naming.ParseProjectID(protocol.DefaultProjectID)
	if err == nil {
		return defaultProjectID
	}
	return naming.ProjectID(protocol.DefaultProjectID)
}

func daemonProjectIDForPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	projectID, err := config.ProjectIDForRoot(trimmed)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(projectID)
}

func daemonProjectRouteIDForPath(path string) (naming.ProjectID, bool) {
	projectID := daemonProjectIDForPath(path)
	if strings.TrimSpace(projectID) == "" {
		return "", false
	}
	parsed, err := naming.ParseProjectID(projectID)
	if err != nil {
		return "", false
	}
	return parsed, true
}

func resolveDaemonBinaryForRepo(repoDir string) string {
	if sibling := resolveDaemonBinaryNearInvokedAz(); sibling != "" {
		return sibling
	}
	if sibling := resolveDaemonBinaryNearExecutable(); sibling != "" {
		return sibling
	}
	if cwdBin := resolveDaemonBinaryFromWorkingDir(); cwdBin != "" {
		return cwdBin
	}
	if strings.TrimSpace(repoDir) == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(repoDir, "bin", "azd"),
		// Monorepo root launch path: repo contains go-bubbletea implementation subdir.
		filepath.Join(repoDir, "go-bubbletea", "bin", "azd"),
	}
	for _, bin := range candidates {
		if _, err := os.Stat(bin); err == nil {
			return bin
		}
	}
	return ""
}

func resolveDaemonBinaryNearExecutable() string {
	exe, err := executablePath()
	if err != nil || strings.TrimSpace(exe) == "" {
		return ""
	}

	dir := filepath.Dir(exe)
	candidate := filepath.Join(dir, "azd")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func resolveDaemonBinaryNearInvokedAz() string {
	args := processArgs()
	candidates := make([]string, 0, 2)
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		candidates = append(candidates, args[0])
	}
	candidates = append(candidates, "az")

	for _, candidate := range candidates {
		resolved, err := resolveCommandPath(candidate)
		if err != nil || strings.TrimSpace(resolved) == "" {
			continue
		}
		azd := filepath.Join(filepath.Dir(resolved), "azd")
		if _, err := os.Stat(azd); err == nil {
			return azd
		}
	}

	return ""
}

func resolveDaemonBinaryFromWorkingDir() string {
	cwd, err := workingDir()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return ""
	}
	candidate := filepath.Join(cwd, "bin", "azd")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func resolveCommandPath(command string) (string, error) {
	if command == "" {
		return "", errors.New("empty command")
	}
	if filepath.IsAbs(command) {
		if _, err := os.Stat(command); err != nil {
			return "", err
		}
		return command, nil
	}
	return lookupPath(command)
}

func resolveInitialProjectName(registry *config.ProjectsRegistry, cwd string) string {
	if registry != nil && cwd != "" {
		if project := registry.FindByPath(cwd); project != nil && project.Name != "" {
			return project.Name
		}
		if project := findProjectByCwdBasenamePrefix(registry, cwd); project != nil && project.Name != "" {
			return project.Name
		}
	}
	if registry != nil {
		if project := registry.GetDefault(); project != nil && project.Name != "" {
			return project.Name
		}
	}
	if cwd != "" {
		return filepath.Base(cwd)
	}
	return "default"
}

func findProjectByCwdBasenamePrefix(registry *config.ProjectsRegistry, cwd string) *config.Project {
	if registry == nil || len(registry.Projects) == 0 || strings.TrimSpace(cwd) == "" {
		return nil
	}

	pathBases := make([]string, 0, 8)
	for p := filepath.Clean(cwd); ; p = filepath.Dir(p) {
		base := strings.ToLower(filepath.Base(p))
		if base != "" && base != "." && base != string(filepath.Separator) {
			pathBases = append(pathBases, base)
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}

	bestIndex := -1
	bestLen := 0

	for i := range registry.Projects {
		project := registry.Projects[i]
		candidates := []string{
			strings.ToLower(strings.TrimSpace(project.Name)),
			strings.ToLower(strings.TrimSpace(filepath.Base(filepath.Clean(project.Path)))),
		}

		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			for _, base := range pathBases {
				if base == candidate || strings.HasPrefix(base, candidate+"-") || strings.HasPrefix(base, candidate+"_") {
					if len(candidate) > bestLen {
						bestLen = len(candidate)
						bestIndex = i
					}
				}
			}
		}
	}

	if bestIndex < 0 {
		return nil
	}
	return &registry.Projects[bestIndex]
}

func (m Model) projectSelectorCursor() int {
	if m.projectRegistry == nil || len(m.projectRegistry.Projects) == 0 {
		return 0
	}

	target := m.currentProject
	if target == "" {
		target = resolveInitialProjectName(m.projectRegistry, m.repoDir)
	}

	for i, project := range m.projectRegistry.Projects {
		if project.Name == target {
			return i
		}
	}

	return 0
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) resolveBaseBranch() string {
	baseBranch := m.config.Git.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}
	return baseBranch
}

func (m Model) taskAndSessionByID(issueID string) (*domain.Task, *domain.Session, bool) {
	targetID := taskIDKey(issueID)
	if targetID == "" {
		return nil, nil, false
	}

	for i := range m.tasks {
		if taskIDKey(m.tasks[i].ID.String()) != targetID {
			continue
		}
		task := &m.tasks[i]
		return task, cloneSession(task.Session), true
	}
	return nil, nil, false
}

func (m Model) originBranchForSelection(selectedID string) string {
	baseBranch := m.resolveBaseBranch()
	if selectedID == "" || selectedID == baseBranch {
		return baseBranch
	}
	if strings.HasPrefix(selectedID, "az/") {
		return selectedID
	}
	return fmt.Sprintf("az/%s", selectedID)
}

func (m Model) sessionOriginCandidates(task *domain.Task) ([]overlay.MergeTarget, int) {
	baseBranch := m.resolveBaseBranch()
	candidates := []overlay.MergeTarget{
		{
			ID:          baseBranch,
			Label:       baseBranch,
			IsMain:      true,
			HasWorktree: false,
		},
	}

	upstreamCount := 0
	for _, candidate := range m.getFollowOnMergeCandidates(task) {
		if !candidate.target.HasWorktree {
			continue
		}
		upstreamCount++
		candidates = append(candidates, overlay.MergeTarget{
			ID:          candidate.target.ID,
			Label:       candidate.target.Label,
			Status:      candidate.target.Status,
			HasWorktree: true,
		})
	}

	return candidates, upstreamCount
}

func (m Model) daemonCommandTimeout() time.Duration {
	if m.config != nil && m.config.Session.TimeoutMs > 0 {
		return time.Duration(m.config.Session.TimeoutMs) * time.Millisecond
	}
	return 30 * time.Second
}

// startSessionCmd requests daemon-owned lifecycle start and lets daemon snapshots rebuild the local projection.
func (m Model) startSessionCmd(issueID string, baseBranch string, yolo bool, startWork bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()
		startWorkValue := startWork

		if baseBranch == "" {
			baseBranch = m.resolveBaseBranch()
		}
		if m.daemonClient == nil {
			return sessionErrorMsg{issueID: issueID, err: fmt.Errorf("daemon client unavailable")}
		}
		if _, err := m.daemonClient.StartSession(ctx, daemonclient.StartSessionParams{
			IssueID:    issueID,
			BaseBranch: baseBranch,
			Yolo:       yolo,
			StartWork:  &startWorkValue,
			ImagePaths: m.sessionImagePaths(ctx, issueID),
		}); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return sessionStartedMsg{issueID: issueID, operationID: pending.OperationID, state: pending.State}
			}
			return sessionErrorMsg{issueID: issueID, err: err}
		}

		return sessionStartedMsg{issueID: issueID}
	}
}

func (m Model) sessionImagePaths(ctx context.Context, issueID string) []string {
	if m.attachmentService == nil {
		return nil
	}
	attachments, err := m.attachmentService.List(ctx, issueID)
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("failed to list issue image attachments for session start", "issue_id", issueID, "error", err)
		}
		return nil
	}

	paths := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		path := strings.TrimSpace(attachment.Path)
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// stopSessionCmd requests daemon-owned lifecycle stop and lets daemon snapshots rebuild the local projection.
func (m Model) stopSessionCmd(issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if m.daemonClient == nil {
			return sessionErrorMsg{issueID: issueID, err: fmt.Errorf("daemon client unavailable")}
		}

		// Stop local projection observer state; lifecycle authority remains daemon-owned.
		m.sessionMonitor.Stop(issueID)

		if _, err := m.daemonClient.StopSession(ctx, issueID); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return sessionStoppedMsg{issueID: issueID, operationID: pending.OperationID, state: pending.State}
			}
			return sessionErrorMsg{issueID: issueID, err: err}
		}

		return sessionStoppedMsg{issueID: issueID}
	}
}

func cloneSession(session *domain.Session) *domain.Session {
	if session == nil {
		return nil
	}

	cloned := *session
	if session.StartedAt != nil {
		startedAt := *session.StartedAt
		cloned.StartedAt = &startedAt
	}
	if session.DevServer != nil {
		devServer := *session.DevServer
		cloned.DevServer = &devServer
	}
	return &cloned
}

func projectSessionLifecycleState(state protocol.SessionLifecycleState) (domain.SessionState, bool) {
	switch state {
	case protocol.SessionLifecycleStateStarting, protocol.SessionLifecycleStateAttached:
		return domain.SessionBusy, true
	case protocol.SessionLifecycleStatePaused:
		return domain.SessionPaused, true
	case protocol.SessionLifecycleStateStopped:
		return "", false
	default:
		return domain.SessionBusy, true
	}
}

// Helper methods

// currentColumn returns the tasks in the current column
func (m Model) currentColumn() []domain.Task {
	return m.tasksInColumn(m.columnStatus())
}

// columnStatus returns the status for the current column
func (m Model) columnStatus() domain.Status {
	columns := m.buildColumns()
	return m.nav.GetCurrentStatus(columns)
}

// tasksInColumn returns all tasks with the given status
func (m Model) tasksInColumn(status domain.Status) []domain.Task {
	var filtered []domain.Task
	for _, task := range m.tasks {
		if task.Status == status {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

// sortTasksInColumn returns sorted tasks with the given status from a filtered list
func (m Model) sortTasksInColumn(filteredTasks []domain.Task, status domain.Status) []domain.Task {
	var inColumn []domain.Task
	for _, task := range filteredTasks {
		if task.Status == status {
			inColumn = append(inColumn, task)
		}
	}

	sortState := m.editor.GetSort()
	if sortState != nil && sortState.Field == domain.SortBySession {
		activeDescendantSessionByTask := buildActiveDescendantSessionByTask(m.tasks)
		if len(activeDescendantSessionByTask) > 0 {
			for i := range inColumn {
				if activeDescendantSessionByTask[inColumn[i].ID.String()] {
					inColumn[i].HasTmuxSession = true
				}
			}
		}
	}
	// Apply sort
	return m.editor.ApplySort(inColumn)
}

// getCurrentTaskAndSession returns the currently selected task and its session
func (m Model) getCurrentTaskAndSession() (*domain.Task, *domain.Session) {
	columns := m.buildColumns()
	cursor := m.nav.GetCursor()
	if task, session := m.nav.GetCurrentTask(columns); task != nil {
		if cursor == nil || cursor.TaskID == "" || task.ID.String() == cursor.TaskID {
			if latestTask, latestSession, ok := m.taskAndSessionByID(task.ID.String()); ok && latestTask != nil {
				return latestTask, latestSession
			}
			return task, session
		}
	}

	// Cursor can target a task hidden by current filters (for example child
	// issues hidden from board). In that case, resolve directly from full task
	// set so actions/drill-down flows still operate on the selected task ID.
	if cursor == nil || cursor.TaskID == "" {
		return nil, nil
	}
	for i := range m.tasks {
		if m.tasks[i].ID.String() == cursor.TaskID {
			task := m.tasks[i]
			return &task, task.Session
		}
	}
	return nil, nil
}

// handleBulkAction handles bulk action menu selections
func isTaskWorkspaceOverlay(current overlay.Overlay) bool {
	if current == nil {
		return false
	}
	_, ok := current.(*overlay.TaskWorkspaceOverlay)
	return ok
}

func (m Model) eventLogFilePath() string {
	if strings.TrimSpace(m.logFilePath) != "" {
		return m.logFilePath
	}
	return resolveTUILogFilePath(m.config)
}

func (m Model) daemonLogFilePath() string {
	if scopedDaemonRuntimeEnabledForJustRun() && strings.TrimSpace(m.runtimeRepoDir) != "" {
		return filepath.Join(m.runtimeRepoDir, ".azedarach", "daemon.log")
	}
	if scopedDaemonRuntimeEnabledForJustRun() {
		if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
			if worktreeRoot, rootErr := config.ResolveWorktreeRoot(cwd); rootErr == nil && strings.TrimSpace(worktreeRoot) != "" {
				return filepath.Join(worktreeRoot, ".azedarach", "daemon.log")
			}
		}
	}
	repoDir := strings.TrimSpace(m.repoDir)
	if repoDir == "" {
		repoDir = "."
	}
	return filepath.Join(repoDir, ".azedarach", "daemon.log")
}

func resolveTUILogFilePath(cfg *config.Config) string {
	if scopedDaemonRuntimeEnabledForJustRun() {
		if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
			if worktreeRoot, rootErr := config.ResolveWorktreeRoot(cwd); rootErr == nil && strings.TrimSpace(worktreeRoot) != "" {
				return filepath.Join(worktreeRoot, ".azedarach", "az.log")
			}
		}
	}

	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	baseDir := strings.TrimSpace(cfg.Session.LogDir)
	if baseDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
			baseDir = filepath.Join(homeDir, ".azedarach", "logs")
		} else {
			baseDir = filepath.Join(".", ".azedarach", "logs")
		}
	}
	return filepath.Join(baseDir, "az.log")
}

func scopedDaemonRuntimeEnabledForJustRun() bool {
	mode := strings.TrimSpace(strings.ToLower(os.Getenv("AZEDARACH_DAEMON_SCOPE")))
	source := strings.TrimSpace(strings.ToLower(os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE")))
	modeEnabled := mode == "worktree" || mode == "scoped" || mode == "local"
	return modeEnabled && source == "just-run"
}

func newTUILogger(logPath string) *slog.Logger {
	return logging.NewTextFileLogger(logPath, slog.LevelInfo)
}

func (m Model) configSourcePath() string {
	base, err := config.ResolveConfigBase(m.repoDir)
	if err != nil {
		base = m.repoDir
	}
	if strings.TrimSpace(base) == "" {
		base = "."
	}
	return filepath.Join(base, config.ConfigDirName, config.ConfigFileName)
}

func (m Model) openLogStreamCmd(logPaths ...string) tea.Cmd {
	return func() tea.Msg {
		paths := make([]string, 0, len(logPaths))
		for _, logPath := range logPaths {
			path := strings.TrimSpace(logPath)
			if path == "" {
				continue
			}
			paths = append(paths, path)
		}
		if len(paths) == 0 {
			return overlay.SelectionMsg{Key: "event-log-error", Value: errors.New("log file path is empty")}
		}
		availablePaths := make([]string, 0, len(paths))
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return overlay.SelectionMsg{
					Key:   "event-log-error",
					Value: fmt.Errorf("log file unavailable: %w", err),
				}
			}
			availablePaths = append(availablePaths, path)
		}
		if len(availablePaths) == 0 {
			return overlay.SelectionMsg{
				Key:   "event-log-error",
				Value: errors.New("no log files are available to stream"),
			}
		}

		sourceSpecs := inferLogSourceSpecsFromPaths(availablePaths)
		sources := make([]string, 0, len(sourceSpecs))
		for _, source := range sourceSpecs {
			sources = append(sources, source.Name)
		}
		if len(sources) > 0 {
			sourceList := strings.Join(sources, ",")
			// Keep a short history buffer before follow mode starts so the stream
			// opens with context while preserving per-line source prefixes.
			if strings.TrimSpace(os.Getenv("TMUX")) != "" && m.tmuxClient != nil {
				popupCommand := fmt.Sprintf("az log --lines 200 --source %s", shellSingleQuote(sourceList))
				if err := m.tmuxClient.DisplayPopup(context.Background(), "az.logs", "90%", "90%", popupCommand); err != nil {
					return overlay.SelectionMsg{
						Key:   "event-log-error",
						Value: fmt.Errorf("stream logs in tmux popup: %w", err),
					}
				}
				return overlay.SelectionMsg{Key: "event-log-opened", Value: strings.Join(availablePaths, ", ")}
			}
			entries, err := logstream.ReadLastMerged(sourceSpecs, 200)
			if err != nil {
				return overlay.SelectionMsg{
					Key:   "event-log-error",
					Value: fmt.Errorf("read log stream history: %w", err),
				}
			}
			for _, entry := range entries {
				fmt.Fprintln(os.Stdout, logstream.FormatLine(entry.Source, entry.RawLine, time.Local))
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			if err := logstream.Follow(ctx, sourceSpecs, 250*time.Millisecond, func(entry logstream.Entry) error {
				_, writeErr := fmt.Fprintln(os.Stdout, logstream.FormatLine(entry.Source, entry.RawLine, time.Local))
				return writeErr
			}); err != nil {
				return overlay.SelectionMsg{
					Key:   "event-log-error",
					Value: fmt.Errorf("stream logs: %w", err),
				}
			}
			return overlay.SelectionMsg{Key: "event-log-opened", Value: strings.Join(availablePaths, ", ")}
		}

		// Fallback for unknown/custom paths where source labels cannot be inferred.
		args := make([]string, 0, len(availablePaths)+3)
		args = append(args, "-n", "+1", "-F")
		args = append(args, availablePaths...)
		cmd := exec.Command("tail", args...)
		if strings.TrimSpace(os.Getenv("TMUX")) != "" && m.tmuxClient != nil {
			quoted := make([]string, 0, len(availablePaths))
			for _, path := range availablePaths {
				quoted = append(quoted, shellSingleQuote(path))
			}
			popupCommand := fmt.Sprintf("tail -n +1 -F %s", strings.Join(quoted, " "))
			if err := m.tmuxClient.DisplayPopup(context.Background(), "az.logs", "90%", "90%", popupCommand); err != nil {
				return overlay.SelectionMsg{
					Key:   "event-log-error",
					Value: fmt.Errorf("stream logs in tmux popup: %w", err),
				}
			}
			return overlay.SelectionMsg{Key: "event-log-opened", Value: strings.Join(availablePaths, ", ")}
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return overlay.SelectionMsg{
				Key:   "event-log-error",
				Value: fmt.Errorf("stream logs: %w", err),
			}
		}
		return overlay.SelectionMsg{Key: "event-log-opened", Value: strings.Join(availablePaths, ", ")}
	}
}

func inferLogSourcesFromPaths(paths []string) []string {
	specs := inferLogSourceSpecsFromPaths(paths)
	sources := make([]string, 0, len(specs))
	for _, spec := range specs {
		sources = append(sources, spec.Name)
	}
	return sources
}

func inferLogSourceSpecsFromPaths(paths []string) []logstream.SourceSpec {
	if len(paths) == 0 {
		return nil
	}
	sources := make([]logstream.SourceSpec, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		base := strings.ToLower(strings.TrimSpace(filepath.Base(path)))
		source := ""
		switch base {
		case "daemon.log":
			source = "daemon"
		case "az.log":
			source = "tui"
		case "az-cli.log":
			source = "cli"
		}
		if source == "" {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		sources = append(sources, logstream.SourceSpec{
			Name: source,
			Path: path,
		})
	}
	return sources
}

func shellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func compactErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")
}

func (m Model) appendAttachmentNoteCmd(att *attachment.Attachment) tea.Cmd {
	if att == nil || m.daemonClient == nil {
		return nil
	}
	issueID := strings.TrimSpace(att.IssueID)
	filename := strings.TrimSpace(att.Filename)
	if issueID == "" || filename == "" {
		return nil
	}
	line := formatAttachmentNoteLine(att)
	if strings.TrimSpace(line) == "" {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.daemonClient.AppendTaskNotes(ctx, issueID, line); err != nil {
			return Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Image attached but failed to append notes: %s", compactErrorMessage(err)),
				Expires: time.Now().Add(6 * time.Second),
			}
		}
		return nil
	}
}

func formatAttachmentNoteLine(att *attachment.Attachment) string {
	if att == nil {
		return ""
	}
	issueID := strings.TrimSpace(att.IssueID)
	filename := strings.TrimSpace(att.Filename)
	if issueID == "" || filename == "" {
		return ""
	}
	relativePath := filepath.ToSlash(filepath.Join(".azedarach", "images", issueID, filename))
	source := "file"
	if strings.HasPrefix(strings.ToLower(filename), "clipboard-") {
		source = "clipboard"
	}
	timestamp := att.Created.Local().Format("2006-01-02 15:04:05")
	return fmt.Sprintf("📎 [%s](%s) (%s, %s)", filename, relativePath, source, timestamp)
}

func (m Model) openLogEditorCmd(logPath string) tea.Cmd {
	path := strings.TrimSpace(logPath)
	if path == "" {
		return func() tea.Msg {
			return overlay.SelectionMsg{Key: "event-log-error", Value: errors.New("log file path is empty")}
		}
	}
	if _, err := os.Stat(path); err != nil {
		return func() tea.Msg {
			return overlay.SelectionMsg{
				Key:   "event-log-error",
				Value: fmt.Errorf("log file unavailable: %w", err),
			}
		}
	}

	editorName := strings.TrimSpace(os.Getenv("EDITOR"))
	if editorName == "" {
		editorName = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editorName == "" {
		editorName = "vim"
	}

	cmd := exec.Command(editorName, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return execProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return overlay.SelectionMsg{
				Key:   "event-log-error",
				Value: fmt.Errorf("open log editor: %w", err),
			}
		}
		return overlay.SelectionMsg{Key: "event-log-opened", Value: path}
	})
}

type taskDeletedResultMsg struct {
	taskID string
	err    error
}

type worktreeCleanupResultMsg struct {
	taskID      string
	deletedTask bool
	force       bool
	needsForce  bool
	reason      string
	err         error
}

type worktreeCleanupConfirmPromptMsg struct {
	taskID       string
	deletedTask  bool
	freshness    protocol.TaskListFreshness
	checkedAt    time.Time
	hasSnapshot  bool
	hasTask      bool
	hasWorktree  bool
	dirty        bool
	ahead        int
	behind       int
	additions    int
	deletions    int
	reconcileErr error
	snapshotErr  error
}

type pendingWorktreeCleanupConfirmation struct {
	taskID      string
	deletedTask bool
	force       bool
}

type bulkCleanupRisk struct {
	taskID    string
	dirty     bool
	ahead     int
	additions int
	deletions int
}

type bulkCleanupPreflightMsg struct {
	taskIDs      []string
	deletedTasks bool
	risks        []bulkCleanupRisk
	freshness    protocol.TaskListFreshness
	checkedAt    time.Time
	reconcileErr error
	snapshotErr  error
}

type pendingBulkCleanupConfirmation struct {
	taskIDs      []string
	deletedTasks bool
}

type refreshTaskWorkspaceResultMsg struct {
	taskID        string
	hasTask       bool
	task          domain.Task
	tasks         []domain.Task
	lastCheckedAt time.Time
	freshness     protocol.TaskListFreshness
	reconcileErr  error
	snapshotErr   error
}

func (m Model) refreshTaskWorkspaceInBackgroundCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		msg := refreshTaskWorkspaceResultMsg{taskID: taskID}
		if m.daemonClient == nil {
			return msg
		}

		issueID := strings.TrimSpace(taskID)
		if issueID != "" && taskIDKey(issueID) != "main" {
			reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 8*time.Second)
			if _, err := m.daemonClient.ReconcileRuntimeIssues(reconcileCtx, []string{issueID}); err != nil {
				msg.reconcileErr = err
			}
			reconcileCancel()
		}

		snapshotCtx, snapshotCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer snapshotCancel()
		snapshot, err := m.readTaskSnapshot(snapshotCtx, m.daemonClient)
		if err != nil {
			msg.snapshotErr = err
			return msg
		}

		msg.tasks = snapshot.Tasks
		msg.lastCheckedAt = snapshot.LastCheckedAt
		msg.freshness = snapshot.Freshness
		for _, candidate := range snapshot.Tasks {
			if candidate.ID.String() == msg.taskID {
				msg.hasTask = true
				msg.task = candidate
				return msg
			}
		}
		msg.snapshotErr = fmt.Errorf("task %s not found in refreshed snapshot", msg.taskID)
		return msg
	}
}

func (m Model) deleteTaskCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if m.daemonClient == nil {
			return taskDeletedResultMsg{taskID: taskID, err: fmt.Errorf("daemon client unavailable")}
		}

		err := m.daemonClient.ArchiveTask(ctx, taskID)
		return taskDeletedResultMsg{taskID: taskID, err: err}
	}
}

func (m Model) cleanupWorktreeCmd(taskID string, deleteTask bool, force bool) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, err: fmt.Errorf("daemon client unavailable")}
		}

		// Always ask daemon to stop first; local projection may be stale.
		m.sessionMonitor.Stop(taskID)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, stopErr := m.daemonClient.StopSession(stopCtx, taskID)
		stopCancel()
		if stopErr != nil {
			if !isSessionAlreadyStoppedError(stopErr) && !isSessionStopSkippableDuringCleanup(stopErr) {
				return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, err: stopErr}
			}
		}

		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := m.daemonClient.RemoveWorktreeWithOptions(removeCtx, taskID, force)
		removeCancel()
		if err != nil {
			if !force && isDirtyWorktreeRemovalError(err) {
				return worktreeCleanupResultMsg{
					taskID:      taskID,
					deletedTask: deleteTask,
					force:       force,
					needsForce:  true,
					reason:      strings.TrimSpace(err.Error()),
				}
			}
			return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, err: err}
		}

		if deleteTask {
			deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := m.daemonClient.DeleteTask(deleteCtx, taskID)
			deleteCancel()
			if err != nil {
				return worktreeCleanupResultMsg{taskID: taskID, deletedTask: true, force: force, err: err}
			}
		}

		return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force}
	}
}

func (m Model) requestWorktreeCleanupConfirmationCmd(taskID string, deleteTask bool) tea.Cmd {
	return func() tea.Msg {
		msg := worktreeCleanupConfirmPromptMsg{
			taskID:      taskID,
			deletedTask: deleteTask,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if m.daemonClient == nil {
			msg.snapshotErr = fmt.Errorf("daemon client unavailable")
			return msg
		}

		if issueID := strings.TrimSpace(taskID); issueID != "" {
			if _, err := m.daemonClient.ReconcileRuntimeIssues(ctx, []string{issueID}); err != nil {
				msg.reconcileErr = err
			}
		}

		snapshot, err := m.readTaskSnapshot(ctx, m.daemonClient)
		if err != nil {
			msg.snapshotErr = err
			return msg
		}

		msg.hasSnapshot = true
		msg.freshness = snapshot.Freshness
		msg.checkedAt = snapshot.LastCheckedAt

		for _, task := range snapshot.Tasks {
			if taskIDKey(task.ID.String()) != taskIDKey(taskID) {
				continue
			}
			msg.hasTask = true
			msg.hasWorktree = task.HasWorktree
			msg.ahead = task.GitAheadCount
			msg.behind = task.GitBehindCount
			msg.additions = task.GitAdditions
			msg.deletions = task.GitDeletions
			msg.dirty = task.HasUncommittedChanges
			break
		}

		return msg
	}
}

func formatWorktreeCleanupConfirmPrompt(msg worktreeCleanupConfirmPromptMsg) string {
	action := "cleanup worktree"
	if msg.deletedTask {
		action = "delete task and cleanup worktree"
	}

	lines := []string{
		fmt.Sprintf("Action: %s", action),
		fmt.Sprintf("Task: %s", msg.taskID),
		"",
		"Git state (after priority reconcile):",
	}

	if msg.snapshotErr != nil {
		lines = append(lines, fmt.Sprintf("- unavailable (%v)", msg.snapshotErr))
	} else if !msg.hasTask {
		lines = append(lines, "- task not found in refreshed snapshot")
	} else {
		worktreeState := "not detected"
		if msg.hasWorktree {
			worktreeState = "present"
		}
		changeState := "clean"
		if msg.dirty {
			changeState = fmt.Sprintf("dirty (+%d/-%d)", msg.additions, msg.deletions)
		}
		lines = append(lines,
			fmt.Sprintf("- Worktree: %s", worktreeState),
			fmt.Sprintf("- Changes: %s", changeState),
			fmt.Sprintf("- Base diff (+/-): +%d/-%d", msg.additions, msg.deletions),
			fmt.Sprintf("- Ahead/Behind: ↑%d/↓%d", msg.ahead, msg.behind),
		)
		if msg.hasSnapshot && !msg.checkedAt.IsZero() {
			lines = append(lines, fmt.Sprintf("- Snapshot: %s at %s", msg.freshness, msg.checkedAt.Local().Format("2006-01-02 15:04:05")))
		}
	}

	if msg.reconcileErr != nil {
		lines = append(lines, "", fmt.Sprintf("Note: reconcile warning: %v", msg.reconcileErr))
	}

	lines = append(lines, "", "Proceed?")
	return strings.Join(lines, "\n")
}

func isDirtyWorktreeRemovalError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "contains modified or untracked files") ||
		strings.Contains(message, "use --force to delete it")
}

func isSessionAlreadyStoppedError(err error) bool {
	if err == nil {
		return false
	}
	var cmdErr *daemonclient.CommandError
	if errors.As(err, &cmdErr) && cmdErr.Code == protocol.ErrorCodeInvalidRequest {
		message := strings.ToLower(strings.TrimSpace(cmdErr.Message))
		return strings.Contains(message, "no active session found") ||
			strings.Contains(message, "session not found")
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "no active session found") ||
		strings.Contains(message, "session not found")
}

func isSessionStopSkippableDuringCleanup(err error) bool {
	if err == nil {
		return false
	}
	var cmdErr *daemonclient.CommandError
	if errors.As(err, &cmdErr) && cmdErr.Code == protocol.ErrorCodeTimeout {
		message := strings.ToLower(strings.TrimSpace(cmdErr.Message))
		return strings.Contains(message, "refresh runtime state before mutation") ||
			strings.Contains(message, "wait runtime reconcile")
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "refresh runtime state before mutation") &&
		(strings.Contains(message, "wait runtime reconcile") ||
			strings.Contains(message, "deadline exceeded") ||
			strings.Contains(message, "context canceled"))
}

// NOTE: clampTaskIndex and clampTaskIndexForColumn have been removed.
// The ID-based Cursor now handles bounds clamping internally via
// MoveVertical, MoveHorizontal, and FindPosition methods.

// halfPage calculates half-page scroll distance based on terminal height
func (m Model) halfPage() int {
	cardsPerColumn := m.boardVisibleCards(m.buildColumns())
	half := cardsPerColumn / 2
	if half < 1 {
		return 1
	}
	return half
}

func (m Model) boardVisibleCards(columns []board.Column) int {
	availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	if availableHeight < 1 {
		return 1
	}
	columnCount := m.boardVisibleColumnCount(len(columns))
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	cardWidth := board.CardContentWidth(columnWidth)
	linesPerCard := board.CardLineFootprint(m.styles, cardWidth)
	if linesPerCard < 1 {
		linesPerCard = 1
	}
	visibleCards := availableHeight / linesPerCard
	if availableHeight%linesPerCard != 0 {
		visibleCards++
	}
	if visibleCards < 1 {
		return 1
	}
	return visibleCards
}

func clampInt(v int, low int, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func (m *Model) ensureCursorVisible(columns []board.Column) {
	pos := m.nav.GetPosition(columns)
	m.ensureColumnVisible(pos, len(columns))
	if !pos.Valid || pos.Column < 0 || pos.Column >= len(columns) || pos.Column >= len(m.viewportStarts) {
		return
	}

	availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	if availableHeight < 1 {
		availableHeight = 1
	}
	columnCount := m.boardVisibleColumnCount(len(columns))
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	cardWidth := board.CardContentWidth(columnWidth)
	linesPerCard := board.CardLineFootprint(m.styles, cardWidth)
	if linesPerCard < 1 {
		linesPerCard = 1
	}

	taskCount := len(columns[pos.Column].Tasks)
	if taskCount <= 0 {
		m.viewportStarts[pos.Column] = 0
		return
	}

	start := clampInt(m.viewportStarts[pos.Column], 0, taskCount-1)
	for i := 0; i < 8; i++ {
		windowStart, windowEnd := board.VisibleTaskWindow(taskCount, start, availableHeight, linesPerCard)

		if pos.Task < windowStart {
			start = pos.Task
			continue
		}
		if pos.Task >= windowEnd {
			windowSize := windowEnd - windowStart
			if windowSize < 1 {
				windowSize = 1
			}
			start = pos.Task - windowSize + 1
			if start < 0 {
				start = 0
			}
			continue
		}
		start = windowStart
		break
	}

	start = clampInt(start, 0, taskCount-1)
	m.viewportStarts[pos.Column] = start
}

func (m Model) boardVisibleColumnCount(totalColumns int) int {
	return board.VisibleColumnCount(totalColumns, m.width)
}

func (m Model) boardVisibleColumnRange(columns []board.Column) (int, int) {
	totalColumns := len(columns)
	if totalColumns == 0 {
		return 0, 0
	}
	visibleColumns := m.boardVisibleColumnCount(totalColumns)
	if visibleColumns < 1 {
		visibleColumns = 1
	}
	start := m.columnViewportStart
	maxStart := totalColumns - visibleColumns
	if maxStart < 0 {
		maxStart = 0
	}
	start = clampInt(start, 0, maxStart)
	end := start + visibleColumns
	if end > totalColumns {
		end = totalColumns
	}
	return start, end
}

func (m *Model) ensureColumnVisible(pos navigation.Position, totalColumns int) {
	if totalColumns <= 0 {
		m.columnViewportStart = 0
		return
	}

	visibleColumns := m.boardVisibleColumnCount(totalColumns)
	if visibleColumns < 1 {
		visibleColumns = 1
	}
	maxStart := totalColumns - visibleColumns
	if maxStart < 0 {
		maxStart = 0
	}
	start := clampInt(m.columnViewportStart, 0, maxStart)
	if !pos.Valid || pos.Column < 0 || pos.Column >= totalColumns {
		m.columnViewportStart = start
		return
	}

	if pos.Column < start {
		start = pos.Column
	} else if pos.Column >= start+visibleColumns {
		start = pos.Column - visibleColumns + 1
	}
	m.columnViewportStart = clampInt(start, 0, maxStart)
}

func (m Model) selectionSummary() string {
	selected := m.editor.GetSelectedTasks()
	if len(selected) == 0 {
		return ""
	}

	filtered := m.editor.ApplyFilter(m.tasks)
	visible := make(map[string]struct{}, len(filtered))
	for _, task := range filtered {
		visible[task.ID.String()] = struct{}{}
	}

	hiddenCount := 0
	for taskID := range selected {
		if _, ok := visible[taskID]; !ok {
			hiddenCount++
		}
	}

	if hiddenCount > 0 {
		return fmt.Sprintf("Selected: %d (%d hidden)", len(selected), hiddenCount)
	}
	return fmt.Sprintf("Selected: %d", len(selected))
}

func (m Model) filterSummary() string {
	filter := m.editor.GetFilter()
	if filter == nil || !filter.IsActive() {
		return "F:none"
	}

	parts := make([]string, 0, 7)
	if query := strings.TrimSpace(filter.SearchQuery); query != "" {
		parts = append(parts, "q="+query)
	}
	if count := len(filter.Status); count > 0 {
		parts = append(parts, fmt.Sprintf("st:%d", count))
	}
	if count := len(filter.Priority); count > 0 {
		parts = append(parts, fmt.Sprintf("pr:%d", count))
	}
	if count := len(filter.Type); count > 0 {
		parts = append(parts, fmt.Sprintf("ty:%d", count))
	}
	if count := len(filter.SessionState); count > 0 {
		parts = append(parts, fmt.Sprintf("ss:%d", count))
	}
	if !filter.HideEpicChildren {
		parts = append(parts, "children:show")
	}
	if filter.AgeMaxDays != nil {
		parts = append(parts, fmt.Sprintf("age<=%dd", *filter.AgeMaxDays))
	}
	if len(parts) == 0 {
		return "F:active"
	}
	return "F:" + strings.Join(parts, ",")
}

func (m Model) sortSummary() string {
	sortState := m.editor.GetSort()
	if sortState == nil {
		return "S:git_diff/asc"
	}

	field := strings.TrimSpace(string(sortState.Field))
	if field == "" {
		field = string(domain.SortByGitDiff)
	}
	order := "asc"
	if sortState.Order == domain.SortDesc {
		order = "desc"
	}
	return fmt.Sprintf("S:%s/%s", field, order)
}

// renderLoading renders a centered loading spinner with message
func (m Model) renderLoading() string {
	content := lipgloss.JoinVertical(
		lipgloss.Center,
		m.spinner.View(),
		"Loading issues...",
	)

	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		content,
	)
}

// addToast adds a toast notification to the list
func (m *Model) addToast(toast Toast) {
	m.toasts = append(m.toasts, toast)
	kind := protocol.EnvelopeKind("info")
	switch toast.Level {
	case ToastSuccess:
		kind = protocol.EnvelopeKind("success")
	case ToastWarning:
		kind = protocol.EnvelopeKind("warning")
	case ToastError:
		kind = protocol.EnvelopeKind("error")
	}
	m.recordRuntimeEvent(protocol.EventEnvelope{
		Revision:  m.daemonRevision,
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Event:     "ui.toast",
		Kind:      kind,
		Body:      []byte(toast.Message),
		EmittedAt: time.Now().UTC(),
	})
}

func (m *Model) recordRuntimeEvent(evt protocol.EventEnvelope) {
	if evt.EmittedAt.IsZero() {
		evt.EmittedAt = time.Now().UTC()
	}
	if evt.ProjectID == "" {
		evt.ProjectID = naming.ProjectID(m.daemonProjectID())
	}
	m.runtimeEvents = append(m.runtimeEvents, evt)
	if len(m.runtimeEvents) > eventLogCapacity {
		m.runtimeEvents = append([]protocol.EventEnvelope(nil), m.runtimeEvents[len(m.runtimeEvents)-eventLogCapacity:]...)
	}
	if m.eventTicker != nil {
		summary := runtimeEventSummary(evt)
		if summary != "" {
			m.eventTicker.Push(summary)
		}
	}
}

func runtimeEventSummary(evt protocol.EventEnvelope) string {
	eventName := strings.TrimSpace(evt.Event)
	body := compactSummaryText(string(evt.Body))
	eventLabel := humanizeRuntimeEventName(eventName)
	if eventName == "ui.toast" && body != "" {
		return truncateSummary(body)
	}
	switch {
	case eventLabel != "" && body != "":
		if strings.EqualFold(eventLabel, body) {
			return truncateSummary(body)
		}
		return truncateSummary(eventLabel + ": " + body)
	case body != "":
		return truncateSummary(body)
	case eventLabel != "":
		return truncateSummary(eventLabel)
	default:
		return truncateSummary(strings.TrimSpace(string(evt.Kind)))
	}
}

func humanizeRuntimeEventName(eventName string) string {
	eventName = strings.TrimSpace(eventName)
	switch eventName {
	case "":
		return ""
	case "ui.toast":
		return ""
	case protocol.EventTaskCreated:
		return "Task created"
	case protocol.EventTaskUpdated:
		return "Task updated"
	case protocol.EventTaskDeleted:
		return "Task deleted"
	case protocol.EventTaskArchived:
		return "Task archived"
	case "session.started":
		return "Session started"
	case "session.stopped":
		return "Session stopped"
	case protocol.EventWorktreeProjectionUpdated:
		return "Worktree projection updated"
	case protocol.EventGitStatusUpdated:
		return "Git status updated"
	}

	tokens := strings.FieldsFunc(strings.ToLower(eventName), func(r rune) bool {
		return r == '.' || r == '_' || r == '-'
	})
	if len(tokens) > 2 && tokens[1] == "event" {
		tokens = append(tokens[:1], tokens[2:]...)
	}
	if len(tokens) == 0 {
		return ""
	}
	if tokens[0] == "ui" && len(tokens) > 1 {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return ""
	}

	tokens[0] = strings.ToUpper(tokens[0][:1]) + tokens[0][1:]
	return strings.Join(tokens, " ")
}

func compactSummaryText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateSummary(value string) string {
	runes := []rune(value)
	if len(runes) <= eventSummaryMaxRunes {
		return value
	}
	return string(runes[:eventSummaryMaxRunes-1]) + "…"
}

// expireToasts removes expired toasts from the list
func (m *Model) expireToasts() {
	now := time.Now()
	filtered := make([]Toast, 0, len(m.toasts))

	for _, toast := range m.toasts {
		if toast.Expires.After(now) {
			filtered = append(filtered, toast)
		}
	}

	m.toasts = filtered
}

// Git operation commands

type fetchAndMergeResultMsg struct {
	worktree    string
	issueID     string
	attachAfter bool
	result      *daemonclient.MergeResult
	stage       string
	operationID string
	state       protocol.OperationState
	err         error
}

type createPRResultMsg struct {
	issueID string
	cmd     string
	err     error
}

type openPRResultMsg struct {
	issueID string
	err     error
}

type helixOpenResultMsg struct {
	issueID     string
	opened      bool
	commandHint string
	err         error
}

// fetchAndMergeCmd fetches and merges from the specified branch
func (m Model) bulkMoveStatusCmd(taskIDs []string, delta int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		statusOrder := []domain.Status{
			domain.StatusOpen,
			domain.StatusInProgress,
			domain.StatusBlocked,
			domain.StatusDone,
		}

		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)

		for _, taskID := range taskIDs {
			// Find the task to get current status
			var currentTask *domain.Task
			for i := range m.tasks {
				if m.tasks[i].ID.String() == taskID {
					currentTask = &m.tasks[i]
					break
				}
			}

			if currentTask == nil {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "task not found"})
				continue
			}

			// Find current status index
			currentIdx := -1
			for i, s := range statusOrder {
				if s == currentTask.Status {
					currentIdx = i
					break
				}
			}

			if currentIdx == -1 {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "invalid status"})
				continue
			}

			// Calculate new status
			newIdx := currentIdx + delta
			if newIdx < 0 || newIdx >= len(statusOrder) {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "cannot move beyond status bounds"})
				continue
			}

			newStatus := statusOrder[newIdx]

			// Update via daemon client
			if m.daemonClient == nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "daemon client unavailable"})
				continue
			}
			err := m.daemonClient.UpdateTaskStatus(ctx, taskID, newStatus)
			if err != nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: err.Error()})
				continue
			}

			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed}
	}
}

func (m Model) bulkDeleteCmd(taskIDs []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)

		for _, taskID := range taskIDs {
			if !m.taskExists(taskID) {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "task not found"})
				continue
			}
			if m.daemonClient == nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "daemon client unavailable"})
				continue
			}
			err := m.daemonClient.DeleteTask(ctx, taskID)
			if err != nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: err.Error()})
				continue
			}
			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed}
	}
}

func (m Model) bulkArchiveCmd(taskIDs []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)

		for _, taskID := range taskIDs {
			if !m.taskExists(taskID) {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "task not found"})
				continue
			}
			if m.daemonClient == nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "daemon client unavailable"})
				continue
			}
			err := m.daemonClient.ArchiveTask(ctx, taskID)
			if err != nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: err.Error()})
				continue
			}
			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed}
	}
}

func (m Model) bulkCleanupWorktreeCmd(taskIDs []string, deleteTask bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)

		for _, taskID := range taskIDs {
			if !m.taskExists(taskID) {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "task not found"})
				continue
			}
			if m.daemonClient == nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "daemon client unavailable"})
				continue
			}

			// Always ask daemon to stop first; local projection may be stale.
			m.sessionMonitor.Stop(taskID)
			_, stopErr := m.daemonClient.StopSession(ctx, taskID)
			if stopErr != nil && !isSessionAlreadyStoppedError(stopErr) && !isSessionStopSkippableDuringCleanup(stopErr) {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: stopErr.Error()})
				continue
			}

			removeErr := m.daemonClient.RemoveWorktreeWithOptions(ctx, taskID, false)
			if removeErr != nil {
				failed++
				reason := removeErr.Error()
				if isDirtyWorktreeRemovalError(removeErr) {
					reason = fmt.Sprintf("%s (single-task cleanup supports force)", strings.TrimSpace(removeErr.Error()))
				}
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: reason})
				continue
			}

			if deleteTask {
				if err := m.daemonClient.DeleteTask(ctx, taskID); err != nil {
					failed++
					issues = append(issues, bulkTaskIssue{taskID: taskID, reason: err.Error()})
					continue
				}
			}

			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed}
	}
}

func (m Model) bulkCleanupPreflightCmd(taskIDs []string, deleteTask bool) tea.Cmd {
	selected := append([]string(nil), taskIDs...)
	return func() tea.Msg {
		msg := bulkCleanupPreflightMsg{
			taskIDs:      selected,
			deletedTasks: deleteTask,
		}
		if len(selected) == 0 {
			return msg
		}
		if m.daemonClient == nil {
			msg.snapshotErr = fmt.Errorf("daemon client unavailable")
			return msg
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if _, err := m.daemonClient.ReconcileRuntimeIssues(ctx, selected); err != nil {
			msg.reconcileErr = err
		}

		snapshot, err := m.readTaskSnapshot(ctx, m.daemonClient)
		if err != nil {
			msg.snapshotErr = err
			return msg
		}
		msg.freshness = snapshot.Freshness
		msg.checkedAt = snapshot.LastCheckedAt

		tasksByID := make(map[string]domain.Task, len(snapshot.Tasks))
		for _, task := range snapshot.Tasks {
			tasksByID[taskIDKey(task.ID.String())] = task
		}

		for _, taskID := range selected {
			task, ok := tasksByID[taskIDKey(taskID)]
			if !ok {
				continue
			}
			if !task.HasUncommittedChanges && task.GitAheadCount <= 0 {
				continue
			}
			msg.risks = append(msg.risks, bulkCleanupRisk{
				taskID:    task.ID.String(),
				dirty:     task.HasUncommittedChanges,
				ahead:     task.GitAheadCount,
				additions: task.GitAdditions,
				deletions: task.GitDeletions,
			})
		}

		return msg
	}
}

// bulkSetStatusCmd sets all selected tasks to a specific status
func (m Model) bulkSetStatusCmd(taskIDs []string, status domain.Status) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)

		for _, taskID := range taskIDs {
			if !m.taskExists(taskID) {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "task not found"})
				continue
			}
			if m.daemonClient == nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "daemon client unavailable"})
				continue
			}
			err := m.daemonClient.UpdateTaskStatus(ctx, taskID, status)
			if err != nil {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: err.Error()})
				continue
			}
			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed}
	}
}

func (m Model) taskExists(taskID string) bool {
	for i := range m.tasks {
		if m.tasks[i].ID.String() == taskID {
			return true
		}
	}
	return false
}

func (m Model) availableTaskImplementations() []string {
	seen := make(map[string]struct{})
	impls := make([]string, 0, 4)
	for i := range m.tasks {
		for _, impl := range m.tasks[i].Implementations {
			value := strings.TrimSpace(impl)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			impls = append(impls, value)
		}
	}
	if len(impls) == 0 {
		impls = append(impls, "default")
	}
	sort.Strings(impls)
	return impls
}

func summarizeBulkIssues(issues []bulkTaskIssue) string {
	if len(issues) == 0 {
		return ""
	}

	parts := make([]string, 0, len(issues))
	for _, item := range issues {
		parts = append(parts, fmt.Sprintf("%s: %s", item.taskID, item.reason))
	}
	return strings.Join(parts, "; ")
}

func formatBulkCleanupPreflightPrompt(msg bulkCleanupPreflightMsg) string {
	action := "cleanup worktrees"
	if msg.deletedTasks {
		action = "delete tasks and cleanup worktrees"
	}
	lines := []string{
		fmt.Sprintf("Action: %s", action),
		fmt.Sprintf("Selected tasks: %d", len(msg.taskIDs)),
		"",
	}

	if msg.snapshotErr != nil {
		lines = append(lines, fmt.Sprintf("Preflight unavailable: %v", msg.snapshotErr))
	} else if len(msg.risks) == 0 {
		lines = append(lines, "No selected tasks are currently dirty or ahead.")
	} else {
		lines = append(lines, "Selected tasks with dirty/ahead git state:")
		for _, risk := range msg.risks {
			stateParts := make([]string, 0, 2)
			if risk.dirty {
				stateParts = append(stateParts, fmt.Sprintf("dirty (+%d/-%d)", risk.additions, risk.deletions))
			}
			if risk.ahead > 0 {
				stateParts = append(stateParts, fmt.Sprintf("ahead %d", risk.ahead))
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", risk.taskID, strings.Join(stateParts, ", ")))
		}
	}

	if !msg.checkedAt.IsZero() {
		lines = append(lines, "", fmt.Sprintf("Snapshot: %s at %s", msg.freshness, msg.checkedAt.Local().Format("2006-01-02 15:04:05")))
	}
	if msg.reconcileErr != nil {
		lines = append(lines, fmt.Sprintf("Reconcile warning: %v", msg.reconcileErr))
	}
	lines = append(lines, "", "Proceed?")
	return strings.Join(lines, "\n")
}

func (m Model) saveTaskCmd(msg overlay.TaskCreatedMsg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if msg.ID != "" {
			if m.daemonClient == nil {
				return taskCreatedResultMsg{taskID: msg.ID, err: fmt.Errorf("daemon client unavailable"), isUpdate: true}
			}
			err := m.daemonClient.UpdateTaskDetails(ctx, msg.ID, daemonclient.TaskUpdateParams{
				Title:           msg.Title,
				Description:     msg.Description,
				Type:            msg.Type,
				Priority:        msg.Priority,
				Implementations: msg.Implementations,
			})
			return taskCreatedResultMsg{taskID: msg.ID, err: err, isUpdate: true}
		}

		if m.daemonClient == nil {
			return taskCreatedResultMsg{err: fmt.Errorf("daemon client unavailable")}
		}

		var parentID *naming.IssueID
		if msg.ParentID != nil {
			parsedParentID, parseErr := naming.ParseIssueID(strings.TrimSpace(*msg.ParentID))
			if parseErr != nil {
				return taskCreatedResultMsg{taskID: "", err: fmt.Errorf("invalid parent_id: %w", parseErr), isUpdate: false}
			}
			parentID = &parsedParentID
		}
		taskID, err := m.daemonClient.CreateTask(ctx, daemonclient.TaskCreateParams{
			Title:           msg.Title,
			Description:     msg.Description,
			Type:            msg.Type,
			Priority:        msg.Priority,
			Status:          msg.Status,
			Assignee:        msg.Assignee,
			Labels:          msg.Labels,
			Implementations: msg.Implementations,
			Design:          msg.Design,
			Notes:           msg.Notes,
			Acceptance:      msg.Acceptance,
			Estimate:        msg.Estimate,
			ParentID:        parentID,
		})
		if err != nil {
			return taskCreatedResultMsg{taskID: taskID, err: err, isUpdate: false}
		}

		attachmentWarning := m.attachStagedAttachments(ctx, taskID, msg.AttachmentPaths)
		return taskCreatedResultMsg{
			taskID:            taskID,
			err:               nil,
			isUpdate:          false,
			attachmentWarning: attachmentWarning,
		}
	}
}

// Single task status result
type taskStatusResultMsg struct {
	taskID         string
	previousStatus domain.Status
	newStatus      domain.Status
	err            error
}

// moveTaskStatusCmd updates a single task's status.
func (m Model) moveTaskStatusCmd(taskID string, previousStatus, newStatus domain.Status) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Update via daemon client
		if m.daemonClient == nil {
			return taskStatusResultMsg{
				taskID:         taskID,
				previousStatus: previousStatus,
				newStatus:      newStatus,
				err:            fmt.Errorf("daemon client unavailable"),
			}
		}
		err := m.daemonClient.UpdateTaskStatus(ctx, taskID, newStatus)
		if err != nil {
			return taskStatusResultMsg{
				taskID:         taskID,
				previousStatus: previousStatus,
				newStatus:      newStatus,
				err:            err,
			}
		}

		return taskStatusResultMsg{
			taskID:         taskID,
			previousStatus: previousStatus,
			newStatus:      newStatus,
		}
	}
}

func shiftedTaskStatus(current domain.Status, delta int) (domain.Status, bool) {
	statusOrder := []domain.Status{
		domain.StatusOpen,
		domain.StatusInProgress,
		domain.StatusBlocked,
		domain.StatusDone,
	}
	currentIdx := -1
	for i, status := range statusOrder {
		if status == current {
			currentIdx = i
			break
		}
	}
	if currentIdx == -1 {
		return "", false
	}
	newIdx := currentIdx + delta
	if newIdx < 0 || newIdx >= len(statusOrder) {
		return "", false
	}
	return statusOrder[newIdx], true
}

func (m *Model) applyOptimisticTaskStatus(taskID string, status domain.Status) {
	for i := range m.tasks {
		if m.tasks[i].ID.String() == taskID {
			m.tasks[i].Status = status
			break
		}
	}
	m.reconcileCursorAfterIssuesRefresh()
}

func (m *Model) rollbackTaskStatus(taskID string, previousStatus domain.Status) {
	m.applyOptimisticTaskStatus(taskID, previousStatus)
	m.clearPendingTaskStatus(taskID)
}

func (m *Model) markTaskStatusPending(taskID string, previousStatus, targetStatus domain.Status, operationID string, state protocol.OperationState) {
	if m.pendingStatuses == nil {
		m.pendingStatuses = make(map[string]pendingTaskStatus)
	}
	m.pendingStatuses[taskIDKey(taskID)] = pendingTaskStatus{
		previousStatus: previousStatus,
		targetStatus:   targetStatus,
		operationID:    operationID,
		state:          state,
		action:         "task_move",
		updatedAt:      time.Now(),
	}
}

func (m *Model) markTaskOperationPending(taskID, action, operationID string, state protocol.OperationState) {
	if m.pendingStatuses == nil {
		m.pendingStatuses = make(map[string]pendingTaskStatus)
	}
	key := taskIDKey(taskID)
	current := m.pendingStatuses[key]
	current.operationID = operationID
	current.state = state
	current.action = action
	current.updatedAt = time.Now()
	m.pendingStatuses[key] = current
}

func (m *Model) clearPendingTaskStatus(taskID string) {
	if len(m.pendingStatuses) == 0 {
		return
	}
	delete(m.pendingStatuses, taskIDKey(taskID))
}

func (m Model) pendingMutationForTask(taskID string) *overlay.TaskMutationProgress {
	key := taskIDKey(taskID)
	progress := &overlay.TaskMutationProgress{}
	if pending, ok := m.pendingStatuses[key]; ok {
		progress.OperationID = pending.operationID
		progress.State = string(pending.state)
		progress.PreviousStatus = pending.previousStatus
		progress.TargetStatus = pending.targetStatus
	}
	if op, ok := m.pendingOpsByTask[key]; ok {
		if progress.OperationID == "" {
			progress.OperationID = op.operationID
		}
		progress.State = string(op.state)
		progress.ProgressPercent = op.percent
		progress.ProgressMessage = op.message
	}
	if runtime, ok := m.runtimeSignalsByTask[key]; ok {
		if progress.OperationID == "" {
			progress.OperationID = strings.TrimSpace(runtime.PendingOperationID)
		}
		if strings.TrimSpace(progress.State) == "" {
			progress.State = strings.TrimSpace(runtime.PendingOperationState)
		}
		if progress.ProgressPercent == 0 {
			progress.ProgressPercent = runtime.PendingOperationPercent
		}
	}
	if progress.OperationID == "" && strings.TrimSpace(progress.State) == "" {
		return nil
	}
	return progress
}

func (m *Model) syncTaskWorkspaceOverlay() {
	current := m.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		return
	}

	taskID := strings.TrimSpace(workspace.TaskID())
	if taskID == "" {
		return
	}

	var task *domain.Task
	for i := range m.tasks {
		if m.tasks[i].ID.String() == taskID {
			task = &m.tasks[i]
			break
		}
	}
	if task == nil {
		return
	}
	taskView := *task
	if path := strings.TrimSpace(m.runtimeSignalWorktreeByTask[taskID]); path != "" {
		if taskView.Session == nil {
			taskView.Session = &domain.Session{IssueID: naming.IssueID(taskView.ID)}
		}
		if strings.TrimSpace(taskView.Session.Worktree) == "" {
			taskView.Session.Worktree = path
		}
	}

	workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
	workspace.SyncTask(taskView, m.tasks, m.pendingMutationForTask(taskID))
}

func (m *Model) applyPendingStatusOverlays() {
	if len(m.pendingStatuses) == 0 {
		return
	}
	for i := range m.tasks {
		key := taskIDKey(m.tasks[i].ID.String())
		pending, ok := m.pendingStatuses[key]
		if !ok {
			continue
		}
		if pending.targetStatus == "" {
			continue
		}
		if m.tasks[i].Status == pending.targetStatus {
			delete(m.pendingStatuses, key)
			continue
		}
		m.tasks[i].Status = pending.targetStatus
	}
}

func (m *Model) reconcilePendingStatuses() {
	if len(m.pendingStatuses) == 0 {
		return
	}

	taskByID := make(map[string]domain.Task, len(m.tasks))
	for _, task := range m.tasks {
		taskByID[task.ID.String()] = task
	}

	const stalePendingTTL = 2 * time.Minute
	now := time.Now()

	for key, pending := range m.pendingStatuses {
		taskID := string(key)
		task, ok := taskByID[taskID]
		if !ok {
			delete(m.pendingStatuses, key)
			continue
		}

		if !pending.updatedAt.IsZero() && now.Sub(pending.updatedAt) > stalePendingTTL {
			delete(m.pendingStatuses, key)
			continue
		}

		switch pending.action {
		case "session_start":
			if task.Session != nil || task.HasTmuxSession {
				delete(m.pendingStatuses, key)
			}
		case "session_stop":
			if task.Session == nil && !task.HasTmuxSession {
				delete(m.pendingStatuses, key)
			}
		}
	}
}

// Phase 6 helper methods

// getTaskChildren returns all tasks that are children of the given parent issue.
func (m Model) getTaskChildren(parentID string) []domain.Task {
	var children []domain.Task
	for _, task := range m.tasks {
		if task.ID.String() == parentID {
			continue
		}
		if task.ParentID != nil && task.ParentID.String() == parentID {
			children = append(children, task)
			continue
		}
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyParentChild && dep.ID.String() == parentID {
				children = append(children, task)
				break
			}
		}
	}
	return children
}

type taskCreatedResultMsg struct {
	taskID            string
	err               error
	isUpdate          bool
	attachmentWarning string
}

func (m Model) attachStagedAttachments(ctx context.Context, issueID string, paths []string) string {
	if strings.TrimSpace(issueID) == "" || len(paths) == 0 || m.attachmentService == nil {
		return ""
	}

	failed := make([]string, 0)
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}

		attached, err := m.attachmentService.Attach(ctx, issueID, path)
		if err != nil {
			failed = append(failed, filepath.Base(path))
			continue
		}

		if attached != nil && m.daemonClient != nil {
			if line := formatAttachmentNoteLine(attached); strings.TrimSpace(line) != "" {
				_ = m.daemonClient.AppendTaskNotes(ctx, issueID, line)
			}
		}

		_ = os.Remove(path)
	}

	if len(failed) == 0 {
		return ""
	}
	return fmt.Sprintf("Task created, but %d image attachment(s) failed: %s", len(failed), strings.Join(failed, ", "))
}

func (m Model) createTaskCmd(msg overlay.TaskCreatedMsg) tea.Cmd {
	return m.saveTaskCmd(msg)
}

// PR creation with overlay

type prCreatedResultMsg struct {
	url   string
	title string
	err   error
}

type openPROverlayResultMsg struct {
	branch   string
	issueID  string
	worktree string
	err      error
}

// openPROverlayCmd resolves branch/worktree context for automated PR creation.
func (m Model) openPROverlayCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		resolvedWorktree := strings.TrimSpace(worktree)
		if resolvedWorktree == "" {
			if fallback, resolveErr := m.resolveIssueWorktreePath(ctx, issueID); resolveErr == nil {
				resolvedWorktree = strings.TrimSpace(fallback)
			}
		}

		branch, err := m.resolveWorktreeBranch(ctx, resolvedWorktree, issueID)
		if err != nil {
			return openPROverlayResultMsg{err: err}
		}
		return openPROverlayResultMsg{
			branch:   branch,
			issueID:  issueID,
			worktree: resolvedWorktree,
		}
	}
}

func (m Model) createPRWithAICmd(msg openPROverlayResultMsg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if m.daemonClient == nil {
			return prCreatedResultMsg{err: fmt.Errorf("daemon client unavailable")}
		}

		issueTitle := ""
		issueDescription := ""
		for i := range m.tasks {
			if m.tasks[i].ID.String() == msg.issueID {
				issueTitle = strings.TrimSpace(m.tasks[i].Title)
				issueDescription = strings.TrimSpace(m.tasks[i].Description)
				break
			}
		}

		baseBranch := strings.TrimSpace(m.resolveBaseBranch())
		if baseBranch == "" {
			baseBranch = "main"
		}

		generated, err := generatePRContent(ctx, prGenerationRequest{
			Worktree:         msg.worktree,
			IssueID:          msg.issueID,
			IssueTitle:       issueTitle,
			IssueDescription: issueDescription,
			Branch:           msg.branch,
			BaseBranch:       baseBranch,
			Tool:             strings.TrimSpace(m.config.CLITool),
		})
		if err != nil {
			return prCreatedResultMsg{err: err}
		}

		draft := true
		if m.config != nil {
			draft = m.config.PR.DraftByDefault
		}
		result, err := m.daemonClient.CreatePullRequest(ctx, daemonclient.CreatePullRequestParams{
			Title:      generated.Title,
			Body:       generated.Body,
			Branch:     msg.branch,
			BaseBranch: baseBranch,
			Draft:      draft,
			IssueID:    msg.issueID,
		})
		if err != nil {
			return prCreatedResultMsg{err: err}
		}

		return prCreatedResultMsg{
			url:   result.PullRequest.URL,
			title: generated.Title,
		}
	}
}

// createPRWithOverlayCmd creates a PR using the pr workflow service
func (m Model) createPRWithOverlayCmd(msg overlay.PRCreatedMsg) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		if m.daemonClient == nil {
			return prCreatedResultMsg{err: fmt.Errorf("daemon client unavailable")}
		}

		result, err := m.daemonClient.CreatePullRequest(ctx, daemonclient.CreatePullRequestParams{
			Title:      msg.Title,
			Body:       msg.Body,
			Branch:     msg.Branch,
			BaseBranch: msg.BaseBranch,
			Draft:      msg.Draft,
			IssueID:    msg.IssueID,
		})
		if err != nil {
			return prCreatedResultMsg{err: err}
		}

		return prCreatedResultMsg{url: result.PullRequest.URL}
	}
}

type branchBehindMsg struct {
	issueID       string
	worktree      string
	commitsBehind int
	err           error
}

func (m Model) checkBranchBehindCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resolvedWorktree := strings.TrimSpace(worktree)
		if resolvedWorktree == "" {
			if fallback, err := m.resolveIssueWorktreePath(ctx, issueID); err == nil {
				resolvedWorktree = strings.TrimSpace(fallback)
			}
		}
		if resolvedWorktree == "" {
			// If no worktree can be resolved we can't compute behind/ahead; callers
			// should continue with attach flow without warning noise.
			return branchBehindMsg{issueID: issueID, worktree: "", commitsBehind: 0}
		}

		baseBranch := strings.TrimSpace(m.resolveBaseBranch())
		remote := "origin"

		if m.daemonClient == nil {
			return branchBehindMsg{issueID: issueID, worktree: resolvedWorktree, err: fmt.Errorf("daemon client unavailable")}
		}

		result, err := m.daemonClient.CheckBranchBehind(ctx, daemonclient.BranchBehindCheckParams{
			Worktree:   resolvedWorktree,
			BaseBranch: baseBranch,
			Remote:     remote,
		})
		if err != nil {
			return branchBehindMsg{issueID: issueID, worktree: resolvedWorktree, err: err}
		}

		return branchBehindMsg{
			issueID:       issueID,
			worktree:      resolvedWorktree,
			commitsBehind: result.CommitsBehind,
		}
	}
}

func (m Model) updateFromBaseCmd(issueID, worktreeHint string, attachAfter bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		resolvedWorktree := strings.TrimSpace(worktreeHint)
		var err error
		if resolvedWorktree == "" {
			resolvedWorktree, err = m.resolveIssueWorktreePathFromDaemon(ctx, issueID)
		}
		if resolvedWorktree == "" {
			return fetchAndMergeResultMsg{
				issueID:     issueID,
				attachAfter: attachAfter,
				err:         fmt.Errorf("no active session/worktree - start session first"),
			}
		}
		if err != nil {
			return fetchAndMergeResultMsg{
				issueID:     issueID,
				attachAfter: attachAfter,
				err:         fmt.Errorf("no active session/worktree - start session first"),
			}
		}

		return m.fetchAndMergeCmd(resolvedWorktree, m.resolveBaseBranch(), issueID, attachAfter)()
	}
}

func (m Model) listDaemonWorktrees(ctx context.Context) ([]daemonclient.Worktree, error) {
	if m.daemonClient == nil {
		return nil, fmt.Errorf("daemon client unavailable")
	}

	worktrees, err := m.daemonClient.ListWorktrees(ctx)
	if err != nil {
		return nil, err
	}
	return worktrees, nil
}

func (m Model) resolveIssueWorktreePath(ctx context.Context, issueID string) (string, error) {
	if issueID == "" {
		return "", fmt.Errorf("issue ID is required")
	}
	if session := m.sessionForIssue(issueID); session != nil && session.Worktree != "" {
		return session.Worktree, nil
	}
	worktrees, err := m.listDaemonWorktrees(ctx)
	if err != nil {
		return "", err
	}
	if wt, ok := findDaemonWorktree(worktrees, "", issueID); ok && wt.Path != "" {
		return wt.Path, nil
	}
	return "", fmt.Errorf("worktree not found for issue %s", issueID)
}

func (m Model) resolveIssueWorktreePathFromDaemon(ctx context.Context, issueID string) (string, error) {
	if issueID == "" {
		return "", fmt.Errorf("issue ID is required")
	}
	worktrees, err := m.listDaemonWorktrees(ctx)
	if err != nil {
		return "", err
	}
	if wt, ok := findDaemonWorktree(worktrees, "", issueID); ok && wt.Path != "" {
		return wt.Path, nil
	}
	return "", fmt.Errorf("worktree not found for issue %s", issueID)
}

func (m Model) resolveIssueSessionStateFromSnapshot(ctx context.Context, issueID string) (domain.SessionState, bool, error) {
	if issueID == "" {
		return domain.SessionIdle, false, fmt.Errorf("issue ID is required")
	}
	if m.daemonClient == nil {
		return domain.SessionIdle, false, fmt.Errorf("daemon client unavailable")
	}

	snapshot, err := m.readTaskSnapshot(ctx, m.daemonClient)
	if err != nil {
		return domain.SessionIdle, false, err
	}
	for _, task := range snapshot.Tasks {
		if task.ID.String() != issueID {
			continue
		}
		if task.Session == nil {
			return domain.SessionIdle, false, nil
		}
		return task.Session.State, true, nil
	}
	return domain.SessionIdle, false, nil
}

func summarizeStatusChangeCounts(status daemonclient.GitStatus) string {
	parts := make([]string, 0, 5)
	if n := len(status.Staged); n > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", n))
	}
	if n := len(status.Modified); n > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", n))
	}
	if n := len(status.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("%d added", n))
	}
	if n := len(status.Deleted); n > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", n))
	}
	if len(parts) == 0 {
		return "working tree has changes"
	}
	return strings.Join(parts, ", ")
}

func hasMergeBlockingStatusChanges(status daemonclient.GitStatus) bool {
	return len(status.Staged) > 0 ||
		len(status.Modified) > 0 ||
		len(status.Added) > 0 ||
		len(status.Deleted) > 0
}

func dirtyFilesFromStatus(status daemonclient.GitStatus) []string {
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, len(status.Staged)+len(status.Modified)+len(status.Added)+len(status.Deleted))
	appendUnique := func(files []string) {
		for _, file := range files {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			out = append(out, file)
		}
	}
	appendUnique(status.Staged)
	appendUnique(status.Modified)
	appendUnique(status.Added)
	appendUnique(status.Deleted)
	sort.Strings(out)
	return out
}

func parseMergePreflightConflictFiles(output string) []string {
	conflicts := make([]string, 0)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "CONFLICT") {
			continue
		}
		if strings.Contains(line, "Merge conflict in ") {
			parts := strings.Split(line, "Merge conflict in ")
			if len(parts) >= 2 {
				if file := strings.TrimSpace(parts[1]); file != "" {
					conflicts = append(conflicts, file)
				}
			}
			continue
		}
		if idx := strings.Index(line, "): "); idx != -1 {
			rest := line[idx+3:]
			var file string
			if idx2 := strings.Index(rest, " deleted in "); idx2 != -1 {
				file = strings.TrimSpace(rest[:idx2])
			} else if idx2 := strings.Index(rest, " modified in "); idx2 != -1 {
				file = strings.TrimSpace(rest[:idx2])
			}
			if file != "" {
				conflicts = append(conflicts, file)
			}
		}
	}
	return conflicts
}

func predictsMergeConflicts(output string, err error) bool {
	if strings.Contains(output, "CONFLICT") {
		return true
	}
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "CONFLICT")
}

func mergePreflightReconcileIssueIDs(sourceID, targetID string) []string {
	seen := make(map[string]struct{}, 2)
	ids := make([]string, 0, 2)
	appendIssue := func(issueID string) {
		normalized := strings.TrimSpace(issueID)
		if normalized == "" || taskIDKey(normalized) == "main" {
			return
		}
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		ids = append(ids, normalized)
	}
	appendIssue(sourceID)
	appendIssue(targetID)
	return ids
}

func (m Model) checkMergePreflight(ctx context.Context, sourceID, targetID, sourceWorktree, targetWorktree, targetRef, sourceBranch string, refreshStatus bool) *mergePreflightFailureMsg {
	if m.daemonClient == nil {
		return nil
	}

	if refreshStatus {
		issueIDs := mergePreflightReconcileIssueIDs(sourceID, targetID)
		if len(issueIDs) > 0 {
			if _, err := m.daemonClient.ReconcileRuntimeIssues(ctx, issueIDs); err != nil && m.logger != nil {
				m.logger.Warn("merge preflight issue reconcile failed", "source_id", sourceID, "target_id", targetID, "error", err)
			}
		}
	}

	reasons := make([]string, 0, 2)
	sourceFiles := make([]string, 0, 8)
	targetFiles := make([]string, 0, 8)

	statusForWorktree := func(worktree string) (daemonclient.GitStatus, error) {
		return m.daemonClient.GitStatus(ctx, worktree)
	}

	sourceStatus, sourceErr := statusForWorktree(sourceWorktree)
	if sourceErr != nil {
		reasons = append(reasons, fmt.Sprintf("Could not read source status (%s): %v", sourceID, sourceErr))
	} else if hasMergeBlockingStatusChanges(sourceStatus) {
		reasons = append(reasons, fmt.Sprintf("Source %s is not clean: %s", sourceID, summarizeStatusChangeCounts(sourceStatus)))
		sourceFiles = dirtyFilesFromStatus(sourceStatus)
	}

	targetStatus, targetErr := statusForWorktree(targetWorktree)
	if targetErr != nil {
		reasons = append(reasons, fmt.Sprintf("Could not read target status (%s): %v", targetID, targetErr))
	} else if hasMergeBlockingStatusChanges(targetStatus) {
		reasons = append(reasons, fmt.Sprintf("Target %s is not clean: %s", targetID, summarizeStatusChangeCounts(targetStatus)))
		targetFiles = dirtyFilesFromStatus(targetStatus)
	}

	targetRef = strings.TrimSpace(targetRef)
	sourceBranch = strings.TrimSpace(sourceBranch)
	if len(reasons) == 0 && targetRef != "" && sourceBranch != "" {
		resp, err := m.daemonClient.GitMergePreflight(ctx, sourceID, sourceWorktree, targetID, targetWorktree, targetRef, sourceBranch)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("Could not predict merge conflicts (%s -> %s): %v", sourceID, targetID, err))
		} else if !resp.Clean {
			if len(resp.Reasons) > 0 {
				reasons = append(reasons, resp.Reasons...)
			} else if len(resp.ConflictFiles) > 0 {
				reasons = append(reasons, fmt.Sprintf("Merge would conflict in %d files: %s", len(resp.ConflictFiles), strings.Join(resp.ConflictFiles, ", ")))
			} else {
				reasons = append(reasons, "Merge would conflict; merge and resolve main into the source branch first")
			}
			if len(resp.SourceFiles) > 0 {
				sourceFiles = append(sourceFiles[:0], resp.SourceFiles...)
			}
			if len(resp.TargetFiles) > 0 {
				targetFiles = append(targetFiles[:0], resp.TargetFiles...)
			}
		}
	}

	if len(reasons) == 0 {
		return nil
	}
	return &mergePreflightFailureMsg{
		sourceID:       sourceID,
		sourceWorktree: sourceWorktree,
		targetID:       targetID,
		targetWorktree: targetWorktree,
		reasons:        reasons,
		sourceFiles:    sourceFiles,
		targetFiles:    targetFiles,
	}
}

func (m Model) refreshMergePreflightCmd(selection overlay.MergePreflightRefreshSelection) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if m.daemonClient == nil {
			return mergePreflightRefreshResultMsg{err: fmt.Errorf("daemon client unavailable")}
		}
		preflight := m.checkMergePreflight(
			ctx,
			strings.TrimSpace(selection.SourceID),
			strings.TrimSpace(selection.TargetID),
			strings.TrimSpace(selection.SourceWorktree),
			strings.TrimSpace(selection.TargetWorktree),
			"",
			"",
			true,
		)
		if preflight != nil {
			return *preflight
		}
		return mergePreflightRefreshResultMsg{cleared: true}
	}
}

func (m Model) discardChangesCmd(side, worktree string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if m.daemonClient == nil {
			return mergePreflightActionResultMsg{
				action:   "discard",
				side:     side,
				worktree: worktree,
				err:      fmt.Errorf("daemon client unavailable"),
			}
		}

		_, err := m.daemonClient.GitDiscardChanges(ctx, worktree)
		if err != nil {
			err = daemonCommandMessage(err)
		}
		return mergePreflightActionResultMsg{
			action:   "discard",
			side:     side,
			worktree: worktree,
			err:      err,
		}
	}
}

func (m Model) commitChangesCmd(side, worktree string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		if m.daemonClient == nil {
			return mergePreflightActionResultMsg{
				action:   "commit",
				side:     side,
				worktree: worktree,
				err:      fmt.Errorf("daemon client unavailable"),
			}
		}

		if status, err := m.daemonClient.GitStatus(ctx, worktree); err == nil && !status.HasChanges {
			return mergePreflightActionResultMsg{
				action:   "commit",
				side:     side,
				worktree: worktree,
				err:      fmt.Errorf("no changes to commit"),
			}
		}

		if _, err := m.daemonClient.GitCheckpointCommit(ctx, worktree, daemonclient.DefaultCheckpointMessage); err != nil {
			return mergePreflightActionResultMsg{
				action:   "commit",
				side:     side,
				worktree: worktree,
				err:      daemonCommandMessage(err),
			}
		}
		return mergePreflightActionResultMsg{
			action:   "commit",
			side:     side,
			worktree: worktree,
		}
	}
}

func daemonCommandMessage(err error) error {
	if err == nil {
		return nil
	}
	var cmdErr *daemonclient.CommandError
	if errors.As(err, &cmdErr) {
		return fmt.Errorf("%s", strings.TrimSpace(cmdErr.Message))
	}
	return err
}

func findDaemonWorktree(worktrees []daemonclient.Worktree, worktreePath, issueID string) (daemonclient.Worktree, bool) {
	for _, wt := range worktrees {
		if worktreePath != "" && wt.Path == worktreePath {
			return wt, true
		}
		if issueID != "" && strings.EqualFold(wt.IssueID, issueID) {
			return wt, true
		}
	}
	return daemonclient.Worktree{}, false
}

func (m Model) resolveWorktreeBranch(ctx context.Context, worktree, issueID string) (string, error) {
	worktrees, err := m.listDaemonWorktrees(ctx)
	if err != nil {
		if issueID != "" {
			return m.originBranchForSelection(issueID), nil
		}
		return "", err
	}

	if wt, ok := findDaemonWorktree(worktrees, worktree, issueID); ok {
		return wt.Branch, nil
	}

	if issueID != "" {
		return m.originBranchForSelection(issueID), nil
	}

	return "", fmt.Errorf("worktree branch not found")
}

func (m Model) getDevServerInfo(issueID string) []overlay.DevServerInfo {
	if m.daemonClient == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv, err := m.daemonClient.DevServerStatus(ctx, issueID)
	if err != nil {
		return nil
	}
	return []overlay.DevServerInfo{{
		ID:     srv.ID,
		Name:   srv.Name,
		Port:   srv.Port,
		Status: srv.Status,
		Uptime: srv.Uptime,
	}}
}

func (m Model) toggleDevServer(serverID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if m.daemonClient == nil {
			return sessionErrorMsg{issueID: serverID, err: fmt.Errorf("daemon client unavailable")}
		}
		if _, err := m.daemonClient.ToggleDevServer(ctx, serverID); err != nil {
			return sessionErrorMsg{issueID: serverID, err: err}
		}
		return nil
	}
}

func (m Model) viewDevServer(serverID string) tea.Cmd {
	return m.attachSessionCmd("devserver-" + serverID)
}

func (m Model) restartDevServer(serverID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if m.daemonClient == nil {
			return sessionErrorMsg{issueID: serverID, err: fmt.Errorf("daemon client unavailable")}
		}
		if _, err := m.daemonClient.RestartDevServer(ctx, serverID); err != nil {
			return sessionErrorMsg{issueID: serverID, err: err}
		}
		return nil
	}
}

// Merge helpers

func (m Model) getMergeCandidates(source *domain.Task) []overlay.MergeTarget {
	candidates := []overlay.MergeTarget{
		{
			ID:     "main",
			Label:  "main branch",
			IsMain: true,
		},
	}

	// Add sibling tasks that have worktrees
	for _, task := range m.tasks {
		if task.ID == source.ID {
			continue
		}

		// Check if task has an active session (and thus a worktree)
		hasSession := task.Session != nil

		candidates = append(candidates, overlay.MergeTarget{
			ID:          task.ID.String(),
			Label:       task.Title,
			IsMain:      false,
			Status:      task.Status,
			HasWorktree: hasSession,
		})
	}

	return candidates
}

// View rendering helpers

// renderBoardView renders the kanban board view
func (m Model) openOrchestrationOverlay() tea.Cmd {
	// Gather session information
	var sessions []overlay.SessionInfo
	for _, task := range m.tasks {
		if task.Session != nil {
			sessions = append(sessions, overlay.SessionInfo{
				IssueID:      task.ID.String(),
				TaskTitle:    task.Title,
				State:        task.Session.State,
				StartedAt:    task.Session.StartedAt,
				Worktree:     task.Session.Worktree,
				RecentOutput: "", // TODO: Capture recent output from tmux
			})
		}
	}

	// Create overlay with callbacks
	orchOverlay := overlay.NewOrchestrationOverlay(
		sessions,
		// onAttach
		func(issueID string) tea.Cmd {
			return func() tea.Msg {
				if !m.tmuxAvailable {
					return Toast{
						Level:   ToastWarning,
						Message: fmt.Sprintf("tmux attach-session -t %s is unavailable outside tmux; launch az inside tmux to use tmux actions", issueID),
						Expires: time.Now().Add(8 * time.Second),
					}
				}

				// Show attach instructions
				return Toast{
					Level:   ToastInfo,
					Message: fmt.Sprintf("Run: tmux attach-session -t %s", issueID),
					Expires: time.Now().Add(5 * time.Second),
				}
			}
		},
		// onKill
		func(issueID string) tea.Cmd {
			return m.stopSessionCmd(issueID)
		},
		// onRefresh
		func() tea.Cmd {
			return m.loadIssuesCmd()
		},
	)

	return m.openOverlay(orchOverlay)
}

// performCleanup executes cleanup operations for selected categories
func (m Model) performCleanup(ctx context.Context, categoryIDs []string) (overlay.CleanupResult, error) {
	result := overlay.CleanupResult{}

	for _, id := range categoryIDs {
		switch id {
		case "delete_old_done":
			cutoff := time.Now().AddDate(0, 0, -30)
			deleted := 0
			for _, task := range m.tasks {
				if task.Status == domain.StatusDone && task.UpdatedAt.Before(cutoff) {
					if m.daemonClient == nil {
						m.logger.Warn("daemon client unavailable for delete", "id", task.ID)
						continue
					}
					err := m.daemonClient.DeleteTask(ctx, task.ID.String())
					if err != nil {
						m.logger.Warn("failed to delete task", "id", task.ID, "error", err)
						continue
					}
					deleted++
				}
			}
			result.Deleted = deleted

		case "archive_done":
			archived := 0
			for _, task := range m.tasks {
				if task.Status == domain.StatusDone {
					if m.daemonClient == nil {
						m.logger.Warn("daemon client unavailable for archive", "id", task.ID)
						continue
					}
					err := m.daemonClient.ArchiveTask(ctx, task.ID.String())
					if err != nil {
						m.logger.Warn("failed to archive task", "id", task.ID, "error", err)
						continue
					}
					archived++
				}
			}
			result.Archived = archived

		case "remove_orphaned_worktrees":
			if m.daemonClient == nil {
				m.logger.Warn("daemon client unavailable for orphaned worktree cleanup")
				continue
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, orphanedWorktreeCleanupTimeout)
			removed, err := m.daemonClient.CleanupOrphanedWorktrees(cleanupCtx)
			cleanupCancel()
			if err != nil {
				m.logger.Warn("failed to clean orphaned worktrees", "error", err)
				continue
			}
			result.WorktreesRemoved = removed

		case "clean_stale_sessions":
			// Clean sessions inactive for >24 hours
			cleaned := 0
			cutoff := time.Now().Add(-24 * time.Hour)
			for issueID, session := range m.sessions {
				if session.StartedAt != nil && session.StartedAt.Before(cutoff) {
					if session.State == domain.SessionIdle || session.State == domain.SessionPaused {
						// Stop and clean up stale session
						m.sessionMonitor.Stop(issueID)
						if m.daemonClient != nil {
							_, _ = m.daemonClient.StopSession(ctx, issueID)
						}
						cleaned++
					}
				}
			}
			result.SessionsCleaned = cleaned
		}
	}

	return result, nil
}
