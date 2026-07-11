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
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/logstream"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/editor"
	"github.com/riordanpawley/azedarach/internal/services/navigation"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
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
	diffPreviewMaxCharacters         = 200
	eventLogCapacity                 = 256
	notificationHistoryCapacity      = 100
	eventSummaryMaxRunes             = 140
	taskCloseMutationTimeout         = 10 * time.Minute
	worktreeCleanupMutationTimeout   = 2 * time.Minute
	orphanedWorktreeCleanupTimeout   = 2 * time.Minute
	issueScopedRuntimeReconcileLimit = 64
	maxBoardViewColumns              = 8
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
	ViewModeOverview
)

type orchestrationProjectOverview struct {
	Name             string
	Path             string
	ProjectID        string
	Tasks            []domain.Task
	Observations     []domain.WorkerObservation
	ObservationErrs  []string
	MailByTask       map[string]protocol.MailEvent
	Err              error
	Fallback         string
	Revision         uint64
	LastCheckedAt    time.Time
	Freshness        protocol.TaskListFreshness
	Snapshot         *protocol.OrchestrationSnapshot
	Session          *protocol.OrchestratorSessionResult
	OrchestrationErr error
}

type projectOrchestratorTarget struct {
	ProjectID   string
	ProjectPath string
	SocketPath  string
}

type projectOrchestratorActionRunner func(context.Context, projectOrchestratorTarget, string, protocol.OrchestratorSessionRequest) (protocol.OrchestratorSessionResult, error)

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

type pendingTaskDetails struct {
	title           string
	description     string
	design          string
	notes           string
	acceptance      string
	estimate        *int
	taskType        domain.TaskType
	priority        domain.Priority
	implementations []string
	updatedAt       time.Time
}

type editTaskDetailLoadedMsg struct {
	taskID string
	task   domain.Task
	err    error
}

type pendingOperationProgress struct {
	operationID   string
	kind          string
	state         protocol.OperationState
	percent       int
	message       string
	errorMessage  string
	action        string
	reason        string
	recovery      string
	currentStatus domain.Status
	targetStatus  domain.Status
	updatedAt     time.Time
}

type taskMutationFailure struct {
	operationID    string
	action         string
	message        string
	reason         string
	recovery       string
	previousStatus domain.Status
	currentStatus  domain.Status
	targetStatus   domain.Status
	updatedAt      time.Time
}

type daemonStreamMetrics struct {
	EventsDrained               uint64
	RefreshesCoalesced          uint64
	RuntimeProjectionsCoalesced uint64
	Rehydrates                  uint64
	MaxBatchSize                int
}

type notificationHistoryEntry struct {
	ID             string
	DaemonNoticeID string
	CreatedAt      time.Time
	Level          ToastLevel
	Category       string
	State          protocol.NoticeState
	Reference      string
	ScopeType      string
	ScopeID        string
	OperationID    string
	Message        string
	Detail         string
	Read           bool
	Dismissed      bool
	Actions        []protocol.NoticeAction
}

// Model is the main application state
type Model struct {
	// Core data
	tasks                []domain.Task
	boardView            domain.BoardView
	boardColumns         []domain.BoardViewColumnSnapshot
	boardOrdered         []domain.Task
	boardProjection      domain.BoardViewProjection
	sessions             map[string]*domain.Session
	suppressedTasks      map[string]struct{}
	pendingStatuses      map[string]pendingTaskStatus
	pendingDetails       map[string]pendingTaskDetails
	operationTaskID      map[string]string
	pendingOpsByTask     map[string]pendingOperationProgress
	pendingFailures      map[string]taskMutationFailure
	pendingCleanupOps    map[string]pendingWorktreeCleanupConfirmation
	pendingCleanup       *pendingWorktreeCleanupConfirmation
	pendingBulkCleanup   *pendingBulkCleanupConfirmation
	pendingClose         *pendingCloseCleanupConfirmation
	pendingReviewCascade *pendingReviewCascadeConfirmation

	// Navigation (using NavigationService)
	nav *navigation.Service

	// Editor state (mode, filter, sort, selections)
	editor *editor.Service

	// UI state
	overlayStack                        *overlay.Stack
	createTaskOverlay                   *overlay.CreateTaskOverlay
	viewMode                            ViewMode
	boardViews                          []domain.BoardViewRecord
	selectedBoardViewID                 string
	orchestrationOverview               []orchestrationProjectOverview
	orchestrationOverviewLoadedAt       time.Time
	orchestrationOverviewHiddenProjects int
	orchestrationOverviewHiddenTasks    int
	orchestrationOverviewBackendErrors  int
	orchestrationOverviewHiddenLabels   []string
	orchestrationOverviewCursor         int
	projectOrchestratorActionRunner     projectOrchestratorActionRunner
	jumpMode                            *overlay.JumpMode
	jumpTargets                         []string
	mergePickMode                       *mergePickState
	mouseDrag                           mouseDragState
	mouseTap                            mouseTapState
	viewportStarts                      [maxBoardViewColumns]int
	columnViewportStart                 int
	drillDownParentID                   string
	drillDownParentName                 string
	drillDownTrail                      []drillDownContext
	pendingCreatedTaskID                string
	pendingCreatedWorkspaceTaskID       string
	pendingUIOpenTaskID                 string
	pendingUIDrillDownTaskID            string
	openCreatedTaskInWorkspace          bool
	openSessionSelectorOnLoad           bool
	sessionTreeFilterOnly               bool
	runtimeSignalsByTask                map[string]board.RuntimeSignals
	runtimeSignalWorktreeByTask         map[string]string
	runtimeSignalBranchByTask           map[string]string

	// Project
	currentProject       string
	daemonProjectRouteID naming.ProjectID
	projects             []domain.Project
	repoDir              string
	runtimeRepoDir       string
	logFilePath          string

	// Toasts
	toasts                []Toast
	activeToastHistoryIDs []string
	notificationHistory   []notificationHistoryEntry
	notificationSeq       uint64
	feedback              feedbackProjection
	// Recoverable async failures surfaced in notifications/recovery overlay.
	recoveryNotifications   []asyncRecoveryNotification
	recoveryNotificationSeq uint64

	// Runtime event stream backing the event-log overlay.
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
	issueRefreshInFlight  bool
	issueRefreshPending   bool
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
	daemonEventsCancel        context.CancelFunc
	logStreamEventsCancel     context.CancelFunc
	daemonRevision            uint64
	daemonStreamMetrics       daemonStreamMetrics
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

	// Issue attachment service
	attachmentService overlay.ImageAttachmentService

	// Diagnostics service
	diagnosticsService overlay.DiagnosticsCollector

	// Logger
	logger *slog.Logger
}

// Option configures initial TUI behavior.
type Option func(*Model)

// WithSessionSelectorOnLoad opens the tmux session selector after the first task snapshot loads.
func WithSessionSelectorOnLoad() Option {
	return func(m *Model) {
		m.openSessionSelectorOnLoad = true
	}
}

// New creates a new application model with the given config
func New(cfg *config.Config) Model {
	return NewWithOptions(cfg)
}

// NewWithOptions creates a new application model with optional initial behavior.
func NewWithOptions(cfg *config.Config, opts ...Option) Model {
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
	if config.UseScopedDaemonRuntimeFor(repoDir) {
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
		pendingDetails:              make(map[string]pendingTaskDetails),
		operationTaskID:             make(map[string]string),
		pendingOpsByTask:            make(map[string]pendingOperationProgress),
		pendingFailures:             make(map[string]taskMutationFailure),
		pendingCleanupOps:           make(map[string]pendingWorktreeCleanupConfirmation),
		nav:                         navigation.NewService(),
		editor:                      editor.NewService(),
		overlayStack:                overlay.NewStack(),
		viewMode:                    ViewModeBoard, // Start with board view
		selectedBoardViewID:         domain.DefaultBoardViewID,
		runtimeSignalsByTask:        make(map[string]board.RuntimeSignals),
		runtimeSignalWorktreeByTask: make(map[string]string),
		runtimeSignalBranchByTask:   make(map[string]string),
		toasts:                      []Toast{},
		activeToastHistoryIDs:       []string{},
		notificationHistory:         []notificationHistoryEntry{},
		feedback:                    newFeedbackProjection(),
		recoveryNotifications:       []asyncRecoveryNotification{},
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
	logger.Debug("tui runtime initialized", "repo_dir", repoDir, "runtime_repo_dir", runtimeRepoDir, "daemon_socket", daemonSocketPath, "project", m.currentProject)
	m.refreshDaemonProjectRouteID()
	m.daemonClient.WithProjectRouteID(m.daemonProjectRouteIDValue())
	for _, opt := range opts {
		if opt != nil {
			opt(&m)
		}
	}
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

func (m *Model) applyPendingCreatedWorkspaceTask() {
	taskID := strings.TrimSpace(m.pendingCreatedWorkspaceTaskID)
	if taskID == "" {
		return
	}
	workspace, ok := m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		return
	}
	task, _, ok := m.taskAndSessionByID(taskID)
	if !ok || task == nil {
		if m.taskExists(taskID) {
			m.pendingCreatedWorkspaceTaskID = ""
		}
		return
	}
	columns := m.buildColumns()
	if m.nav.JumpToTaskByID(columns, task.ID.String()) {
		m.ensureCursorVisible(columns)
	}
	workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
	workspace.SyncTask(*task, m.tasks, m.pendingMutationForTask(task.ID.String()))
	m.pendingCreatedWorkspaceTaskID = ""
}

func (m Model) openCurrentTaskWorkspace() (tea.Model, tea.Cmd) {
	columns := m.buildColumns()
	task, _ := m.nav.GetCurrentTask(columns)
	if task == nil {
		task, _ = m.getCurrentTaskAndSession()
	}
	if task == nil {
		return m, nil
	}
	if m.taskWaitingHuman(task) {
		return m.openWaitingHumanRequest(task.ID.String())
	}
	return m.openTaskWorkspaceByID(task.ID.String())
}

func (m Model) openTaskWorkspaceByID(taskID string) (tea.Model, tea.Cmd) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return m, nil
	}
	task, _, ok := m.taskAndSessionByID(taskID)
	if !ok || task == nil {
		return m, nil
	}

	if workspace, ok := m.overlayStack.PromoteTaskWorkspace(taskID); ok {
		workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
		workspace.SyncTask(*task, m.tasks, m.pendingMutationForTask(taskID))
		if m.daemonClient == nil {
			return m, nil
		}
		return m, m.refreshTaskWorkspaceInBackgroundCmd(taskID)
	}

	m.overlayStack.RemoveTaskWorkspaces()
	workspace := overlay.NewTaskWorkspaceOverlay(*task, m.tasks, m.pendingMutationForTask(taskID), m.width, m.height)
	workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
	if m.daemonClient == nil {
		return m, m.openOverlay(workspace)
	}
	return m, tea.Batch(
		m.openOverlay(workspace),
		m.refreshTaskWorkspaceInBackgroundCmd(taskID),
	)
}

func (m Model) openEditTaskOverlay(task domain.Task) (tea.Model, tea.Cmd) {
	taskID := strings.TrimSpace(task.ID.String())
	if taskID == "" {
		return m, nil
	}
	if m.daemonClient == nil {
		return m, m.openOverlay(overlay.NewEditTaskOverlayWithImplOptionsAndAttachmentService(task, m.availableTaskImplementations(), m.attachmentService))
	}
	return m, m.loadEditTaskDetailCmd(taskID)
}

func (m Model) loadEditTaskDetailCmd(taskID string) tea.Cmd {
	projectID := m.daemonProjectID()
	return func() tea.Msg {
		msg := editTaskDetailLoadedMsg{taskID: taskID}
		if m.daemonClient == nil {
			msg.err = fmt.Errorf("daemon client unavailable")
			return msg
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snapshot, err := m.daemonClient.GetTaskSnapshotWithMode(ctx, taskID, daemonclient.ReadWaitModeExplicit)
		if err != nil {
			msg.err = err
			return msg
		}
		if m.shouldIgnoreDaemonSnapshot(projectID, snapshot.Revision) {
			msg.err = fmt.Errorf("stale issue detail snapshot")
			return msg
		}
		if err := snapshot.RequireFullDetails("edit task form"); err != nil {
			msg.err = err
			return msg
		}
		for _, candidate := range snapshot.Tasks {
			if taskIDKey(candidate.ID.String()) == taskIDKey(taskID) {
				msg.task = candidate
				return msg
			}
		}
		msg.err = fmt.Errorf("issue not found: %s", taskID)
		return msg
	}
}

func (m Model) enterDrillDownByID(taskID string) (tea.Model, tea.Cmd) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return m, nil
	}
	task, _, ok := m.taskAndSessionByID(taskID)
	if !ok || task == nil {
		return m, nil
	}
	children := m.getTaskChildren(task.ID.String())
	if len(children) == 0 {
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "No children to drill into (use Space for details/actions)",
			Expires: time.Now().Add(2 * time.Second),
		})
		return m, nil
	}
	m.overlayStack.Pop()
	m.enterDrillDown(task.ID.String(), task.Title)
	columns := m.buildColumns()
	m.nav.JumpToTaskByID(columns, children[0].ID.String())
	m.ensureCursorVisible(columns)
	issueIDs := make([]string, 0, len(children)+1)
	issueIDs = append(issueIDs, task.ID.String())
	for _, child := range children {
		issueIDs = append(issueIDs, child.ID.String())
	}
	return m, m.scheduleIssuesRefreshAfterIssueReconcileCmd(issueIDs)
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
	case "ctrl+g":
		if m.overlayStack.IsEmpty() && m.dismissLatestToast() {
			return m, nil
		}
	}

	if next, cmd, handled := m.routeTransientMode(msg); handled {
		return next, cmd
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
			return m, tea.Batch(m.scheduleIssuesRefreshAfterRuntimeReconcileCmd(), m.gitSyncService.FetchAndCheck())
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
		if m.editor.IsFilterActive() || m.sessionTreeFilterOnly {
			m.editor.ClearFilters()
			m.sessionTreeFilterOnly = false
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
	if m.viewMode == ViewModeOverview {
		if next, cmd, handled := m.handleOverviewModeKey(msg); handled {
			return next, cmd
		}
	}
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
	case overlay.OperationQueueHotkey:
		return m.openOperationQueueOverlay()
	case "O": // Orchestration overlay
		return m, m.openOrchestrationOverlay()
	case "X": // Bulk cleanup (Shift+X)
		taskCount := len(m.tasks)
		worktreeCount := len(m.sessions)
		sessionCount := len(m.sessions)
		cleanupOverlay := overlay.NewBulkCleanupOverlay(m.performCleanup, taskCount, worktreeCount, sessionCount)
		return m, m.openOverlay(cleanupOverlay)
	}

	action, ok := keybinds.LookupAction(types.ModeNormal, msg.String())
	if !ok {
		return m, nil
	}

	if m.applyBoardNavigationAction(action, columns) {
		return m, nil
	}

	switch action {
	case keybinds.ActionQuit:
		// Cleanup before quitting
		m.sessionMonitor.StopAll()
		return m, tea.Quit

	// Mode switches
	case keybinds.ActionEnterGoto:
		m.editor.EnterGoto()
		return m, nil

	case keybinds.ActionOpenWorkspace: // Space - open task panel (details + actions)
		return m.openCurrentTaskWorkspace()

	case keybinds.ActionEnterSearch: // Search
		m.editor.EnterSearch()
		return m, m.openOverlay(overlay.NewSearchOverlay())

	case keybinds.ActionOpenFilter: // Filter menu
		return m, m.openOverlay(overlay.NewFilterMenu(m.editor.GetFilter()))

	case keybinds.ActionToggleSessionTreeFilter:
		m.sessionTreeFilterOnly = !m.sessionTreeFilterOnly
		columns := m.buildColumns()
		m.ensureCursorVisible(columns)
		if m.sessionTreeFilterOnly {
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Showing issues with sessions in their tree",
				Expires: time.Now().Add(2 * time.Second),
			})
		} else {
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Session tree filter cleared",
				Expires: time.Now().Add(2 * time.Second),
			})
		}
		return m, nil

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
			return m.enterDrillDownByID(task.ID.String())
		}
		return m, nil

	case keybinds.ActionAttachSession: // Attach to selected issue session
		task, _ := m.getCurrentTaskAndSession()
		if task == nil {
			return m, nil
		}
		m.beginMutationFeedback(fmt.Sprintf("Attach queued for %s", task.ID))
		return m, m.attachSessionCmd(task.ID.String())

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
		return m, m.openOverlay(overlay.NewSettingsOverlayWithEditorAndConfigTarget(m.editor, m.config, m.configLocalSourcePath(), m.configSourcePath()))

	case keybinds.ActionOpenDiagnostic: // Diagnostics (Shift+D)
		diagPanel := overlay.NewDiagnosticsPanel(m.diagnosticsService, m.sessions)
		return m, m.openOverlay(diagPanel)

	case keybinds.ActionOpenRecovery:
		return m, m.openRecoveryOverlayCmd()

	case keybinds.ActionOpenNotificationHistory:
		cmd := (&m).openNotificationHistoryOverlayCmd()
		return m, cmd

	case keybinds.ActionOpenOperationQueue:
		return m.openOperationQueueOverlay()

	case keybinds.ActionOpenBoardViews:
		return m, m.loadBoardViewsCmd()

	case keybinds.ActionPullBase:
		baseBranch := strings.TrimSpace(m.resolveBaseBranch())
		if baseBranch == "" {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Base branch unavailable",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.beginMutationFeedback(fmt.Sprintf("Pulling %s in project root", baseBranch))
		return m, m.pullRootBaseBranchCmd()

	case keybinds.ActionOpenGitPane:
		pane := overlay.NewGitPaneOverlay(m.resolveBaseBranch())
		return m, tea.Batch(m.openOverlay(pane), m.gitPaneStatusCmd(true))

	case keybinds.ActionToggleView: // Toggle view mode
		switch m.viewMode {
		case ViewModeBoard:
			m.viewMode = ViewModeCompact
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Switched to compact view",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, m.persistUIViewModeCmd(m.viewMode)
		case ViewModeCompact:
			m.viewMode = ViewModeOverview
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Switched to orchestration overview",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, tea.Batch(m.persistUIViewModeCmd(m.viewMode), m.loadOrchestrationOverviewCmd())
		default:
			m.viewMode = ViewModeBoard
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Switched to board view",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, m.persistUIViewModeCmd(m.viewMode)
		}
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
		m.startJumpMode()
		return m, nil
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

func (m Model) handleJumpMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.clearJumpMode()
		return m, nil
	}

	next, cmd := m.jumpMode.Update(msg)
	if jump, ok := next.(*overlay.JumpMode); ok {
		m.jumpMode = jump
	}
	return m, cmd
}

func (m *Model) startJumpMode() {
	targets := m.runtimeSignalRefreshTasks()
	if len(targets) == 0 {
		m.clearJumpMode()
		return
	}

	m.jumpTargets = make([]string, 0, len(targets))
	for _, task := range targets {
		taskID := strings.TrimSpace(task.ID.String())
		if taskID != "" {
			m.jumpTargets = append(m.jumpTargets, taskID)
		}
	}
	if len(m.jumpTargets) == 0 {
		m.clearJumpMode()
		return
	}

	m.jumpMode = overlay.NewJumpModeWithChars(len(m.jumpTargets), m.config.Keyboard.JumpLabelChars)
}

func (m *Model) clearJumpMode() {
	m.jumpMode = nil
	m.jumpTargets = nil
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
	case "e":
		if task == nil {
			return m, nil
		}
		return m.openEditTaskOverlay(*task)
	case "b":
		return m, m.openMergeTargetSelection(task)
	case "m":
		m.beginMutationFeedback("Preparing merge")
		return m.mergeCurrentIssueIntoDefaultTarget(task)
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
		m.markMergeOperationPreparing(task.ID.String(), "", "preparing update")
		m.beginMutationFeedback(fmt.Sprintf("Update from base queued for %s", task.ID))
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
	// Navigation without selection toggle.
	case keybinds.ActionMoveDown:
		m.nav.MoveDown(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionMoveUp:
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

	// Half-page movement without selection toggle.
	case keybinds.ActionHalfPageDown:
		m.nav.HalfPageDown(columns, m.halfPage())
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionHalfPageUp:
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
		pos := m.nav.GetPosition(columns)
		if !pos.Valid || pos.Column < 0 || pos.Column >= len(columns) {
			return m, nil
		}
		for _, t := range columns[pos.Column].Tasks {
			m.editor.Select(t.ID.String())
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

	case keybinds.ActionDrillDown:
		if task != nil {
			return m.enterDrillDownByID(task.ID.String())
		}
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
	if msg.String() == "ctrl+g" {
		return m, func() tea.Msg { return overlay.CloseAllOverlaysMsg{} }
	}
	cmd := m.overlayStack.Update(msg)
	return m, cmd
}

// Message types for async operations

type issuesLoadedMsg struct {
	refreshSeq      uint64
	projectID       string
	scopedParentID  string
	tasks           []domain.Task
	boardView       domain.BoardView
	boardColumns    []domain.BoardViewColumnSnapshot
	boardOrdered    []domain.Task
	boardProjection domain.BoardViewProjection
	revision        uint64
	lastCheckedAt   time.Time
	freshness       protocol.TaskListFreshness
	events          <-chan protocol.EventEnvelope
	eventsCancel    context.CancelFunc
	daemonClient    *daemonclient.Client
	daemonSocket    string
	stale           bool
	freshnessHint   string
	reconcileWarn   error
}

type issuesErrorMsg struct {
	refreshSeq uint64
	projectID  string
	err        error
}

type projectSwitchResultMsg struct {
	switchSeq       uint64
	project         config.Project
	projectConfig   *config.Config
	tasks           []domain.Task
	boardView       domain.BoardView
	boardColumns    []domain.BoardViewColumnSnapshot
	boardOrdered    []domain.Task
	boardProjection domain.BoardViewProjection
	revision        uint64
	lastCheckedAt   time.Time
	freshness       protocol.TaskListFreshness
	events          <-chan protocol.EventEnvelope
	eventsCancel    context.CancelFunc
	daemonClient    *daemonclient.Client
	daemonSocket    string
	err             error
}

const (
	daemonStreamEventBatchLimit      = 256
	daemonStreamEventBatchMinDrain   = 16
	daemonStreamEventBatchTimeBudget = 2 * time.Millisecond
)

type daemonStreamEventMsg struct {
	stream <-chan protocol.EventEnvelope
	event  protocol.EventEnvelope
	events []protocol.EventEnvelope
}

type daemonStreamClosedMsg struct {
	stream <-chan protocol.EventEnvelope
}

type logStreamAttachedMsg struct {
	stream <-chan protocol.EventEnvelope
	cancel context.CancelFunc
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

type operationRecordsLoadedMsg struct {
	projectID string
	records   []protocol.OperationRecord
	err       error
}

type operationQueueLoadedMsg struct {
	projectID string
	snapshot  protocol.OperationQueueResponseBody
	err       error
}

type noticeRecordsLoadedMsg struct {
	projectID string
	notices   []protocol.NoticeRecord
	err       error
}

type noticeUpdateResultMsg struct {
	projectID string
	notice    protocol.NoticeRecord
	label     string
	err       error
}

type noticeActionResultMsg struct {
	projectID string
	notice    protocol.NoticeRecord
	label     string
	err       error
}

type notificationCopyDetailsResultMsg struct {
	err error
}

type uiViewModeLoadedMsg struct {
	viewMode ViewMode
	found    bool
	err      error
}

type uiViewModeSavedMsg struct {
	viewMode ViewMode
	err      error
}

type boardViewsLoadedMsg struct {
	views          []domain.BoardViewRecord
	selectedViewID string
	err            error
}

type boardViewSelectedMsg struct {
	viewID string
	err    error
}

type boardViewMutatedMsg struct {
	action string
	viewID string
	err    error
}

type orchestrationOverviewLoadedMsg struct {
	projects       []orchestrationProjectOverview
	hiddenProjects int
	hiddenTasks    int
	backendErrors  int
	hiddenLabels   []string
}

type projectOrchestratorActionMsg struct {
	projectID string
	action    string
	result    protocol.OrchestratorSessionResult
	err       error
}

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
	actionLabel string
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

type conflictResolveAgentResultMsg struct {
	issueID     string
	worktree    string
	windowName  string
	operationID string
	state       protocol.OperationState
	err         error
}

type operationCancelledMsg struct {
	taskID string
	record protocol.OperationRecord
	err    error
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

const visibleTerminalOperationTTL = 15 * time.Minute

func (m *Model) applyOperationRecords(records []protocol.OperationRecord) {
	if len(records) == 0 {
		return
	}
	for _, record := range records {
		m.applyOperationRecord(record, time.Now())
	}
	m.syncTaskWorkspaceOverlay()
}

func (m *Model) applyOperationRecord(record protocol.OperationRecord, now time.Time) {
	operationID := strings.TrimSpace(record.OperationID.String())
	if operationID == "" {
		return
	}
	if m.operationTaskID == nil {
		m.operationTaskID = make(map[string]string)
	}
	if m.pendingOpsByTask == nil {
		m.pendingOpsByTask = make(map[string]pendingOperationProgress)
	}
	taskID := m.resolveOperationTaskID(record.IssueID, record.ResourceKeys)
	if taskID == "" {
		taskID = m.operationTaskID[operationID]
	}
	if taskID == "" {
		return
	}
	m.operationTaskID[operationID] = taskID
	state := protocol.OperationState(record.State)
	if state == protocol.OperationStateDone {
		delete(m.pendingOpsByTask, taskIDKey(taskID))
		delete(m.operationTaskID, operationID)
		m.clearTaskMutationFailure(taskID)
		if m.keepPendingStatusUntilCloseProjection(taskID, operationID, record.Kind, now) {
			return
		}
		m.clearPendingTaskStatusForOperation(taskID, operationID)
		return
	}
	if operationStateTerminal(state) && !operationRecordRecentlyTerminal(record, now) {
		delete(m.pendingOpsByTask, taskIDKey(taskID))
		delete(m.operationTaskID, operationID)
		m.clearTaskMutationFailure(taskID)
		m.clearPendingTaskStatusForOperation(taskID, operationID)
		return
	}
	percent := 0
	message := ""
	if record.Progress != nil {
		percent = clampOperationPercent(record.Progress.Percent)
		message = strings.TrimSpace(record.Progress.Message)
	}
	if state == protocol.OperationStateRunning && percent == 0 {
		percent = 50
	}
	errorMessage := ""
	if record.Error != nil {
		errorMessage = strings.TrimSpace(record.Error.Message)
	}
	if state == protocol.OperationStateFailed && (errorMessage != "" || message != "") {
		if status, ok := m.taskStatusByID(taskID); ok {
			failure := operationMutationFailureDetails(taskID, status, record)
			errorMessage = failure.Message
			m.pendingOpsByTask[taskIDKey(taskID)] = pendingOperationProgress{
				operationID:   operationID,
				kind:          strings.TrimSpace(record.Kind),
				state:         state,
				percent:       percent,
				message:       errorMessage,
				errorMessage:  errorMessage,
				action:        failure.Action,
				reason:        failure.Reason,
				recovery:      failure.Recovery,
				currentStatus: failure.CurrentStatus,
				targetStatus:  failure.TargetStatus,
				updatedAt:     now,
			}
			return
		}
	}
	if errorMessage != "" {
		message = errorMessage
	}
	m.pendingOpsByTask[taskIDKey(taskID)] = pendingOperationProgress{
		operationID:  operationID,
		kind:         strings.TrimSpace(record.Kind),
		state:        state,
		percent:      percent,
		message:      message,
		errorMessage: errorMessage,
		updatedAt:    now,
	}
	if state == protocol.OperationStateCancelled {
		m.clearPendingTaskStatusForOperation(taskID, operationID)
	}
}

func (m *Model) keepPendingStatusUntilCloseProjection(taskID, operationID, kind string, now time.Time) bool {
	kind = strings.TrimSpace(kind)
	if kind != "" && kind != daemonclient.CommandTaskClose {
		return false
	}
	key := taskIDKey(taskID)
	if key == "" {
		return false
	}
	pending, ok := m.pendingStatuses[key]
	if !ok {
		return false
	}
	if strings.TrimSpace(operationID) == "" || strings.TrimSpace(pending.operationID) != strings.TrimSpace(operationID) {
		return false
	}
	if !terminalTaskStatusRequiresClose(pending.targetStatus) {
		return false
	}
	pending.state = protocol.OperationStateDone
	if now.IsZero() {
		now = time.Now()
	}
	pending.updatedAt = now
	m.pendingStatuses[key] = pending
	return true
}

func operationRecordRecentlyTerminal(record protocol.OperationRecord, now time.Time) bool {
	finishedAt := record.FinishedAt
	if finishedAt == nil || finishedAt.IsZero() {
		return true
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Sub((*finishedAt).UTC()) <= visibleTerminalOperationTTL
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
		m.applyOperationRecord(body.Operation, time.Now())
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
		current := m.pendingOpsByTask[taskIDKey(taskID)]
		m.pendingOpsByTask[taskIDKey(taskID)] = pendingOperationProgress{
			operationID:  body.OperationID.String(),
			kind:         current.kind,
			state:        body.State,
			percent:      clampOperationPercent(body.Progress.Percent),
			message:      strings.TrimSpace(body.Progress.Message),
			errorMessage: current.errorMessage,
			updatedAt:    time.Now(),
		}
		m.syncTaskWorkspaceOverlay()
	}
}

func (m *Model) handlePendingWorktreeCleanupOperationEvent(evt protocol.EventEnvelope) (tea.Cmd, bool) {
	if evt.Event != protocol.EventOperationDone &&
		evt.Event != protocol.EventOperationFailed &&
		evt.Event != protocol.EventOperationCancelled {
		return nil, false
	}

	var body protocol.OperationEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		return nil, false
	}
	opID := strings.TrimSpace(body.Operation.OperationID.String())
	if opID == "" {
		return nil, false
	}
	pending, ok := m.pendingCleanupOps[opID]
	if !ok {
		return nil, false
	}

	delete(m.pendingCleanupOps, opID)
	delete(m.pendingOpsByTask, taskIDKey(pending.taskID))
	delete(m.operationTaskID, opID)
	m.syncTaskWorkspaceOverlay()

	if evt.Event != protocol.EventOperationFailed || pending.force {
		return nil, false
	}
	reason := ""
	if body.Operation.Error != nil {
		reason = strings.TrimSpace(body.Operation.Error.Message)
	}
	if reason == "" && body.Operation.Progress != nil {
		reason = strings.TrimSpace(body.Operation.Progress.Message)
	}
	if !isDirtyWorktreeRemovalError(errors.New(reason)) {
		return nil, false
	}

	m.pendingCleanup = &pendingWorktreeCleanupConfirmation{
		taskID:       pending.taskID,
		deletedTask:  pending.deletedTask,
		force:        true,
		deleteBranch: pending.deleteBranch,
		branch:       pending.branch,
	}
	action := "cleanup worktree"
	if pending.deletedTask {
		action = "delete task and cleanup worktree"
	}
	confirm := overlay.NewConfirmDialogExplicitYN(
		"Force worktree cleanup?",
		fmt.Sprintf("Worktree has local changes.\n\nAction: %s\nTask: %s\n\nDetails: %s\n\nForce removal will discard modified/untracked files.\nProceed?", action, pending.taskID, reason),
	)
	return m.openOverlay(confirm), true
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
	scopedParentID := strings.TrimSpace(m.drillDownParentID)
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
					refreshSeq:     refreshSeq,
					projectID:      projectID,
					scopedParentID: scopedParentID,
					stale:          true,
					freshnessHint:  m.taskSnapshotTimeoutHint(timeoutErr),
				}
			}
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: err}
		}
		return issuesLoadedMsg{
			refreshSeq:      refreshSeq,
			projectID:       projectID,
			scopedParentID:  scopedParentID,
			tasks:           snapshot.Tasks,
			boardView:       snapshot.View,
			boardColumns:    snapshot.Columns,
			boardOrdered:    snapshot.Projection.OrderedTasks(),
			boardProjection: snapshot.Projection,
			revision:        snapshot.Revision,
			lastCheckedAt:   snapshot.LastCheckedAt,
			freshness:       snapshot.Freshness,
		}
	}
}

func (m *Model) scheduleIssuesRefreshCmd() tea.Cmd {
	return m.beginIssuesRefreshCmd(func() tea.Cmd {
		return m.loadIssuesCmd()
	})
}

func (m *Model) scheduleIssuesRefreshAfterRuntimeReconcileCmd() tea.Cmd {
	return m.beginIssuesRefreshCmd(func() tea.Cmd {
		return m.loadIssuesAfterRuntimeReconcileCmd()
	})
}

func (m *Model) scheduleIssuesRefreshAfterIssueReconcileCmd(issueIDs []string) tea.Cmd {
	return m.beginIssuesRefreshCmd(func() tea.Cmd {
		return m.loadIssuesAfterIssueReconcileCmd(issueIDs)
	})
}

func (m *Model) beginIssuesRefreshCmd(factory func() tea.Cmd) tea.Cmd {
	if m.issueRefreshInFlight {
		m.issueRefreshPending = true
		m.daemonStreamMetrics.RefreshesCoalesced++
		return nil
	}
	m.issueRefreshSeq++
	m.issueRefreshInFlight = true
	return factory()
}

func (m *Model) finishIssuesRefreshCmd(refreshSeq uint64) tea.Cmd {
	if refreshSeq == 0 || refreshSeq != m.issueRefreshSeq {
		return nil
	}
	m.issueRefreshInFlight = false
	if !m.issueRefreshPending {
		return nil
	}
	m.issueRefreshPending = false
	return m.scheduleIssuesRefreshCmd()
}

func (m Model) loadUIViewModeCmd() tea.Cmd {
	client := m.daemonClient
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := client.GetUIStateForProject(ctx, protocol.DefaultProjectID, protocol.UIStateKeyUIViewMode)
		if err != nil {
			return uiViewModeLoadedMsg{err: err}
		}
		mode, ok := viewModeFromPersistedValue(resp.Value)
		if !resp.Found || !ok {
			return uiViewModeLoadedMsg{found: false}
		}
		return uiViewModeLoadedMsg{viewMode: mode, found: true}
	}
}

func (m Model) persistUIViewModeCmd(mode ViewMode) tea.Cmd {
	client := m.daemonClient
	if client == nil {
		return nil
	}
	value, ok := persistedValueForViewMode(mode)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := client.SetUIStateForProject(ctx, protocol.DefaultProjectID, protocol.UIStateKeyUIViewMode, value)
		return uiViewModeSavedMsg{viewMode: mode, err: err}
	}
}

func (m Model) loadBoardViewsCmd() tea.Cmd {
	client := m.daemonClient
	if client == nil {
		return func() tea.Msg { return boardViewsLoadedMsg{err: fmt.Errorf("daemon client unavailable")} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := client.ListBoardViews(ctx)
		if err != nil {
			return boardViewsLoadedMsg{err: err}
		}
		return boardViewsLoadedMsg{
			views:          resp.Views,
			selectedViewID: resp.SelectedViewID,
		}
	}
}

func (m Model) selectBoardViewCmd(viewID string) tea.Cmd {
	client := m.daemonClient
	viewID = strings.TrimSpace(viewID)
	if client == nil {
		return func() tea.Msg {
			return boardViewSelectedMsg{viewID: viewID, err: fmt.Errorf("daemon client unavailable")}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := client.SelectBoardView(ctx, viewID)
		if err != nil {
			return boardViewSelectedMsg{viewID: viewID, err: err}
		}
		return boardViewSelectedMsg{viewID: resp.ViewID}
	}
}

func (m Model) saveBoardViewCmd(view domain.BoardView) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return boardViewMutatedMsg{action: "save", viewID: string(view.ID), err: fmt.Errorf("daemon client unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := m.daemonClient.SaveBoardView(ctx, view)
		return boardViewMutatedMsg{action: "save", viewID: string(resp.View.View.ID), err: err}
	}
}

func (m Model) deleteBoardViewCmd(viewID string) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return boardViewMutatedMsg{action: "delete", viewID: viewID, err: fmt.Errorf("daemon client unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := m.daemonClient.DeleteBoardView(ctx, viewID)
		return boardViewMutatedMsg{action: "delete", viewID: viewID, err: err}
	}
}

func persistedValueForViewMode(mode ViewMode) (string, bool) {
	switch mode {
	case ViewModeBoard:
		return "board", true
	case ViewModeCompact:
		return "compact", true
	case ViewModeOverview:
		return "overview", true
	default:
		return "", false
	}
}

func viewModeFromPersistedValue(value string) (ViewMode, bool) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "board":
		return ViewModeBoard, true
	case "compact":
		return ViewModeCompact, true
	case "overview", "orchestration":
		return ViewModeOverview, true
	default:
		return ViewModeBoard, false
	}
}

func (m Model) loadIssuesAfterRuntimeReconcileCmd() tea.Cmd {
	projectID := m.daemonProjectID()
	refreshSeq := m.issueRefreshSeq
	scopedParentID := strings.TrimSpace(m.drillDownParentID)
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
					refreshSeq:     refreshSeq,
					projectID:      projectID,
					scopedParentID: scopedParentID,
					stale:          true,
					freshnessHint:  m.taskSnapshotTimeoutHint(timeoutErr),
					reconcileWarn:  reconcileWarn,
				}
			}
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: err}
		}

		return issuesLoadedMsg{
			refreshSeq:      refreshSeq,
			projectID:       projectID,
			scopedParentID:  scopedParentID,
			tasks:           snapshot.Tasks,
			boardView:       snapshot.View,
			boardColumns:    snapshot.Columns,
			boardOrdered:    snapshot.Projection.OrderedTasks(),
			boardProjection: snapshot.Projection,
			revision:        snapshot.Revision,
			lastCheckedAt:   snapshot.LastCheckedAt,
			freshness:       snapshot.Freshness,
			reconcileWarn:   reconcileWarn,
		}
	}
}

func (m Model) loadIssuesAfterIssueReconcileCmd(issueIDs []string) tea.Cmd {
	projectID := m.daemonProjectID()
	refreshSeq := m.issueRefreshSeq
	scopedParentID := strings.TrimSpace(m.drillDownParentID)
	selected := make([]string, 0, len(issueIDs))
	seen := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		normalized := strings.TrimSpace(issueID)
		if normalized == "" || taskIDKey(normalized) == "main" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		selected = append(selected, normalized)
	}
	if len(selected) > issueScopedRuntimeReconcileLimit {
		selected = nil
	}
	return func() tea.Msg {
		if m.daemonClient == nil {
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: fmt.Errorf("daemon client unavailable")}
		}

		reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 8*time.Second)
		reconcileWarn := error(nil)
		if len(selected) > 0 {
			if _, err := m.daemonClient.ReconcileRuntimeIssues(reconcileCtx, selected); err != nil {
				reconcileWarn = err
			}
		}
		reconcileCancel()

		snapshotCtx, snapshotCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer snapshotCancel()
		snapshot, err := m.readTaskSnapshot(snapshotCtx, m.daemonClient)
		if err != nil {
			var timeoutErr *daemonclient.ReadWaitTimeoutError
			if errors.As(err, &timeoutErr) {
				return issuesLoadedMsg{
					refreshSeq:     refreshSeq,
					projectID:      projectID,
					scopedParentID: scopedParentID,
					stale:          true,
					freshnessHint:  m.taskSnapshotTimeoutHint(timeoutErr),
					reconcileWarn:  reconcileWarn,
				}
			}
			return issuesErrorMsg{refreshSeq: refreshSeq, projectID: projectID, err: err}
		}

		return issuesLoadedMsg{
			refreshSeq:      refreshSeq,
			projectID:       projectID,
			scopedParentID:  scopedParentID,
			tasks:           snapshot.Tasks,
			boardView:       snapshot.View,
			boardColumns:    snapshot.Columns,
			boardOrdered:    snapshot.Projection.OrderedTasks(),
			boardProjection: snapshot.Projection,
			revision:        snapshot.Revision,
			lastCheckedAt:   snapshot.LastCheckedAt,
			freshness:       snapshot.Freshness,
			reconcileWarn:   reconcileWarn,
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

func (m Model) shouldIgnoreDaemonSnapshot(projectID string, revision uint64) bool {
	if projectID = strings.TrimSpace(projectID); projectID != "" && projectID != m.daemonProjectID() {
		return true
	}
	return revision != 0 && revision < m.daemonRevision
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

func (m Model) scopedDaemonClientForSocket(socketPath, projectID string) *daemonclient.Client {
	routeID := naming.ProjectID(protocol.NormalizeProjectID(projectID))
	if parsed, err := naming.ParseProjectID(routeID.String()); err == nil {
		routeID = parsed
	}
	if m.daemonClient != nil {
		if strings.TrimSpace(socketPath) == "" || (strings.TrimSpace(m.daemonSocketPath) != "" && m.daemonSocketPath == socketPath) {
			return m.daemonClient.ScopedProjectRouteID(routeID)
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
	if parentID := strings.TrimSpace(m.drillDownParentID); parentID != "" {
		return client.GetChildBoardSnapshotWithMode(ctx, parentID, daemonclient.ReadWaitModeExplicit)
	}
	return client.BoardSnapshotWithMode(ctx, daemonclient.ReadWaitModeExplicit)
}

func (m Model) taskSnapshotTimeoutHint(timeoutErr *daemonclient.ReadWaitTimeoutError) string {
	if timeoutErr == nil {
		return ""
	}
	if strings.TrimSpace(m.drillDownParentID) != "" {
		return fmt.Sprintf("Drill-down child-board refresh timed out during task snapshot read after %s; keeping current local view", timeoutErr.Budget)
	}
	if strings.TrimSpace(timeoutErr.Hint) != "" {
		return timeoutErr.Hint
	}
	return timeoutErr.Error()
}

func (m Model) loadOperationsCmd() tea.Cmd {
	client := m.daemonClient
	projectID := m.daemonProjectID()
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		records, err := client.ListOperations(ctx, daemonclient.OperationListOptions{
			States: []protocol.OperationState{
				protocol.OperationStateQueued,
				protocol.OperationStateRunning,
				protocol.OperationStateFailed,
				protocol.OperationStateCancelled,
			},
			Limit: 100,
		})
		return operationRecordsLoadedMsg{projectID: projectID, records: records, err: err}
	}
}

func (m Model) openOperationQueueOverlay() (tea.Model, tea.Cmd) {
	overlayModel := overlay.NewLoadingOperationQueueOverlay(m.daemonProjectID())
	return m, tea.Batch(m.openOverlay(overlayModel), m.loadOperationQueueCmd())
}

func (m Model) loadOperationQueueCmd() tea.Cmd {
	client := m.daemonClient
	projectID := m.daemonProjectID()
	if client == nil {
		return func() tea.Msg {
			return operationQueueLoadedMsg{projectID: projectID, err: fmt.Errorf("daemon client unavailable")}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snapshot, err := client.OperationQueue(ctx, daemonclient.OperationListOptions{})
		return operationQueueLoadedMsg{projectID: projectID, snapshot: snapshot, err: err}
	}
}

func (m Model) loadFeedbackProjectionCmd() tea.Cmd {
	client := m.daemonClient
	projectID := m.daemonProjectID()
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		notices, err := client.ListNotices(ctx, daemonclient.NoticeListOptions{
			States: []protocol.NoticeState{
				protocol.NoticeStateActive,
				protocol.NoticeStateResolved,
				protocol.NoticeStateDismissed,
			},
			Limit: notificationHistoryCapacity,
		})
		return noticeRecordsLoadedMsg{projectID: projectID, notices: notices, err: err}
	}
}

func (m Model) markDaemonNoticesReadCmd(noticeIDs []string) tea.Cmd {
	client := m.daemonClient
	projectID := m.daemonProjectID()
	if client == nil || len(noticeIDs) == 0 {
		return nil
	}
	ids := append([]string(nil), noticeIDs...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		read := true
		var last protocol.NoticeRecord
		for _, id := range ids {
			notice, err := client.UpdateNotice(ctx, id, &read, "")
			if err != nil {
				return noticeUpdateResultMsg{projectID: projectID, err: err}
			}
			last = notice
		}
		return noticeUpdateResultMsg{projectID: projectID, notice: last}
	}
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
		streamCtx, streamCancel := context.WithCancel(context.Background())
		events, err := daemonClient.Subscribe(streamCtx, projectRouteID.String(), snapshot.Revision)
		if err != nil {
			streamCancel()
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
			switchSeq:       switchSeq,
			project:         project,
			projectConfig:   projectConfig,
			tasks:           snapshot.Tasks,
			boardView:       snapshot.View,
			boardColumns:    snapshot.Columns,
			boardOrdered:    snapshot.Projection.OrderedTasks(),
			boardProjection: snapshot.Projection,
			revision:        snapshot.Revision,
			lastCheckedAt:   snapshot.LastCheckedAt,
			freshness:       snapshot.Freshness,
			events:          events,
			eventsCancel:    streamCancel,
			daemonClient:    daemonClient,
			daemonSocket:    socketPath,
		}
	}
}

func (m Model) attachDaemonCmd() tea.Cmd {
	projectID := m.daemonProjectID()
	targetRepoDir := m.activeProjectPath()
	if runtimeRepoDir := strings.TrimSpace(m.runtimeRepoDir); runtimeRepoDir != "" && config.UseScopedDaemonRuntimeFor(runtimeRepoDir) {
		targetRepoDir = runtimeRepoDir
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
		streamCtx, streamCancel := context.WithCancel(context.Background())
		events, err := daemonClient.Subscribe(streamCtx, projectID, snapshot.Revision)
		if err != nil {
			streamCancel()
			if m.logger != nil {
				m.logger.Warn("daemon attach subscribe failed", "project_id", projectID, "revision", snapshot.Revision, "error", err)
			}
			return issuesErrorMsg{projectID: projectID, err: err}
		}
		if m.logger != nil {
			m.logger.Info("daemon attach success", "project_id", projectID, "target_repo_dir", targetRepoDir, "revision", snapshot.Revision, "task_count", len(snapshot.Tasks))
		}

		return issuesLoadedMsg{
			projectID:       projectID,
			tasks:           snapshot.Tasks,
			boardView:       snapshot.View,
			boardColumns:    snapshot.Columns,
			boardOrdered:    snapshot.Projection.OrderedTasks(),
			boardProjection: snapshot.Projection,
			revision:        snapshot.Revision,
			lastCheckedAt:   snapshot.LastCheckedAt,
			freshness:       snapshot.Freshness,
			events:          events,
			eventsCancel:    streamCancel,
			daemonClient:    daemonClient,
			daemonSocket:    socketPath,
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

		events := []protocol.EventEnvelope{evt}
		deadline := time.Now().Add(daemonStreamEventBatchTimeBudget)
		for len(events) < daemonStreamEventBatchLimit {
			select {
			case next, ok := <-stream:
				if !ok {
					return daemonStreamEventMsg{stream: stream, event: evt, events: events}
				}
				events = append(events, next)
				if len(events) >= daemonStreamEventBatchMinDrain && time.Now().After(deadline) {
					return daemonStreamEventMsg{stream: stream, event: evt, events: events}
				}
			default:
				return daemonStreamEventMsg{stream: stream, event: evt, events: events}
			}
		}
		return daemonStreamEventMsg{stream: stream, event: evt, events: events}
	}
}

func (m Model) attachLogStreamCmd() tea.Cmd {
	const noCatchupRevision = ^uint64(0)
	return func() tea.Msg {
		if m.daemonClient == nil {
			return logStreamAttachedMsg{err: fmt.Errorf("daemon client unavailable")}
		}
		streamCtx, streamCancel := context.WithCancel(context.Background())
		events, err := m.daemonClient.Subscribe(streamCtx, protocol.GlobalEventStreamProjectID, noCatchupRevision)
		if err != nil {
			streamCancel()
			return logStreamAttachedMsg{err: err}
		}
		return logStreamAttachedMsg{stream: events, cancel: streamCancel}
	}
}

func (m *Model) replaceDaemonEventStream(stream <-chan protocol.EventEnvelope, cancel context.CancelFunc) {
	if m.daemonEventsCancel != nil && m.daemonEvents != stream {
		m.daemonEventsCancel()
	}
	m.daemonEvents = stream
	m.daemonEventsCancel = cancel
}

func (m *Model) clearDaemonEventStream() {
	if m.daemonEventsCancel != nil {
		m.daemonEventsCancel()
	}
	m.daemonEvents = nil
	m.daemonEventsCancel = nil
}

func (m *Model) replaceLogStream(stream <-chan protocol.EventEnvelope, cancel context.CancelFunc) {
	if m.logStreamEventsCancel != nil && m.logStreamEvents != stream {
		m.logStreamEventsCancel()
	}
	m.logStreamEvents = stream
	m.logStreamEventsCancel = cancel
}

func (m *Model) clearLogStream() {
	if m.logStreamEventsCancel != nil {
		m.logStreamEventsCancel()
	}
	m.logStreamEvents = nil
	m.logStreamEventsCancel = nil
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
	m.reconcilePendingOperations()
	m.reconcilePendingMutationFailures()
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

func (m Model) projectByDaemonRouteID(projectID string) (config.Project, bool) {
	projectID = strings.TrimSpace(protocol.NormalizeProjectID(projectID))
	if projectID == "" || m.projectRegistry == nil {
		return config.Project{}, false
	}
	for _, project := range m.projectRegistry.Projects {
		if routeID, ok := daemonProjectRouteIDForPath(project.Path); ok && routeID.String() == projectID {
			return project, true
		}
		if protocol.NormalizeProjectID(project.Name) == projectID {
			return project, true
		}
	}
	return config.Project{}, false
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
	return candidates, 0
}

func (m Model) daemonCommandTimeout() time.Duration {
	if m.config != nil && m.config.Session.TimeoutMs > 0 {
		return time.Duration(m.config.Session.TimeoutMs) * time.Millisecond
	}
	return 30 * time.Second
}

func sessionStartActionLabel(yolo bool, startWork bool) string {
	if !startWork {
		return "Tmux shell start"
	}
	if yolo {
		return "AI session start (yolo)"
	}
	return "AI session start"
}

func normalizeSessionStartActionLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "Session start"
	}
	return label
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
		record, err := m.daemonClient.StartSessionOperation(ctx, daemonclient.StartSessionParams{
			IssueID:    issueID,
			BaseBranch: baseBranch,
			Yolo:       yolo,
			StartWork:  &startWorkValue,
			ImagePaths: m.sessionImagePaths(ctx, issueID),
		})
		if err != nil {
			return sessionErrorMsg{issueID: issueID, err: err}
		}
		return sessionStartedMsg{issueID: issueID, operationID: record.OperationID.String(), state: record.State, actionLabel: sessionStartActionLabel(yolo, startWork)}
	}
}

func (m Model) sessionImagePaths(ctx context.Context, issueID string) []string {
	if m.attachmentService == nil {
		return nil
	}
	attachments, err := m.attachmentService.List(ctx, issueID)
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("failed to list issue attachments for session start", "issue_id", issueID, "error", err)
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

		record, err := m.daemonClient.StopSessionOperation(ctx, issueID)
		if err != nil {
			return sessionErrorMsg{issueID: issueID, err: err}
		}
		return sessionStoppedMsg{issueID: issueID, operationID: record.OperationID.String(), state: record.State}
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
	case protocol.SessionLifecycleStateStarting, protocol.SessionLifecycleStateRunning:
		return domain.SessionBusy, true
	case protocol.SessionLifecycleStatePaused:
		return domain.SessionPaused, true
	case protocol.SessionLifecycleStateStopping:
		return domain.SessionBusy, true
	case protocol.SessionLifecycleStateStopped:
		return "", false
	default:
		return domain.SessionBusy, true
	}
}

// Helper methods

// currentColumn returns the tasks in the current column
func (m Model) currentColumn() []domain.Task {
	columns := m.buildColumns()
	pos := m.nav.GetPosition(columns)
	if !pos.Valid || pos.Column < 0 || pos.Column >= len(columns) {
		return nil
	}
	return columns[pos.Column].Tasks
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

func (m *Model) selectTaskByID(taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	columns := m.buildColumns()
	if m.nav.JumpToTaskByID(columns, taskID) {
		m.ensureCursorVisible(columns)
	}
}

func (m Model) eventLogFilePath() string {
	if strings.TrimSpace(m.logFilePath) != "" {
		return m.logFilePath
	}
	return resolveTUILogFilePath(m.config)
}

func (m Model) daemonLogFilePath() string {
	if runtimeRepoDir := strings.TrimSpace(m.runtimeRepoDir); runtimeRepoDir != "" && config.UseScopedDaemonRuntimeFor(runtimeRepoDir) {
		return filepath.Join(runtimeRepoDir, ".azedarach", logging.DaemonLogFileName)
	}
	if strings.TrimSpace(m.repoDir) == "" && config.UseScopedDaemonRuntimeFor("") {
		if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
			if worktreeRoot, rootErr := config.ResolveWorktreeRoot(cwd); rootErr == nil && strings.TrimSpace(worktreeRoot) != "" {
				return filepath.Join(worktreeRoot, ".azedarach", logging.DaemonLogFileName)
			}
		}
	}
	repoDir := strings.TrimSpace(m.repoDir)
	if repoDir == "" {
		repoDir = "."
	}
	return filepath.Join(config.SessionLogDirFor(m.config, repoDir), logging.DaemonLogFileName)
}

func resolveTUILogFilePath(cfg *config.Config) string {
	return filepath.Join(config.SessionLogDirFor(cfg, ""), logging.TUILogFileName)
}

func newTUILogger(logPath string) *slog.Logger {
	if runningUnderGoTest() {
		return logging.NewDiscardLogger(slog.LevelInfo)
	}
	return logging.NewTextFileLogger(logPath, slog.LevelInfo)
}

func runningUnderGoTest() bool {
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
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

func (m Model) configLocalSourcePath() string {
	return filepath.Join(filepath.Dir(m.configSourcePath()), config.LocalConfigFileName)
}

func (m Model) openSettingsEditorCmd(configPathOverride ...string) tea.Cmd {
	configPath := m.configSourcePath()
	if len(configPathOverride) > 0 && strings.TrimSpace(configPathOverride[0]) != "" {
		configPath = configPathOverride[0]
	}
	projectPath := strings.TrimSpace(m.repoDir)
	if projectPath == "" {
		projectPath = "."
	}

	editorName := strings.TrimSpace(os.Getenv("EDITOR"))
	if editorName == "" {
		editorName = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editorName == "" {
		editorName = "vim"
	}

	return func() tea.Msg {
		if strings.TrimSpace(os.Getenv("TMUX")) == "" || m.tmuxClient == nil {
			return overlay.SelectionMsg{
				Key:   "editor-error",
				Value: fmt.Errorf("settings editor unavailable outside tmux; run inside tmux and retry"),
			}
		}
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			return overlay.SelectionMsg{
				Key:   "editor-error",
				Value: fmt.Errorf("prepare settings directory: %w", err),
			}
		}
		popupCommand := fmt.Sprintf("cd %s && %s %s", shellSingleQuote(projectPath), shellSingleQuote(editorName), shellSingleQuote(configPath))
		if err := m.tmuxClient.DisplayPopup(context.Background(), "az.settings", "90%", "90%", popupCommand); err != nil {
			return overlay.SelectionMsg{
				Key:   "editor-error",
				Value: fmt.Errorf("open settings editor in tmux popup: %w", err),
			}
		}
		return overlay.SelectionMsg{
			Key:   "editor-closed",
			Value: configPath,
		}
	}
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
		ctx, endSpan := latencytrace.StartSpan(context.Background(), "dependency", "tail",
			"dependency.name", "tail",
			"dependency.operation", "follow_logs",
			"arg_count", len(args),
		)
		cmd := exec.CommandContext(ctx, "tail", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		endSpan(err)
		if err != nil {
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
		case logging.DaemonLogFileName:
			source = "daemon"
		case logging.TUILogFileName:
			source = "tui"
		case logging.CLILogFileName:
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
				Message: fmt.Sprintf("Attachment added but failed to append notes: %s", compactErrorMessage(err)),
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
	relativePath := strings.TrimSpace(att.Relative)
	if relativePath == "" {
		relativePath = filepath.ToSlash(filepath.Join(".azedarach", "attachments", filename))
	}
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

	_, endSpan := latencytrace.StartSpan(context.Background(), "dependency", "editor",
		"dependency.name", filepath.Base(editorName),
		"dependency.operation", "open_log",
		"arg_count", 1,
	)
	cmd := exec.Command(editorName, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return execProcess(cmd, func(err error) tea.Msg {
		endSpan(err)
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
	taskID       string
	deletedTask  bool
	force        bool
	deleteBranch bool
	branch       string
	operationID  string
	state        protocol.OperationState
	needsForce   bool
	reason       string
	err          error
}

type worktreeCleanupConfirmPromptMsg struct {
	projectID    string
	revision     uint64
	taskID       string
	deletedTask  bool
	force        bool
	deleteBranch bool
	branch       string
	task         domain.Task
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
	branchErr    error
	snapshotErr  error
}

type pendingWorktreeCleanupConfirmation struct {
	taskID       string
	deletedTask  bool
	force        bool
	deleteBranch bool
	branch       string
}

type bulkCleanupRisk struct {
	taskID    string
	dirty     bool
	ahead     int
	additions int
	deletions int
}

type bulkCleanupPreflightMsg struct {
	projectID      string
	revision       uint64
	taskIDs        []string
	deletedTasks   bool
	deleteBranch   bool
	refreshedTasks []domain.Task
	risks          []bulkCleanupRisk
	freshness      protocol.TaskListFreshness
	checkedAt      time.Time
	reconcileErr   error
	snapshotErr    error
}

type pendingBulkCleanupConfirmation struct {
	taskIDs      []string
	deletedTasks bool
	deleteBranch bool
}

type pendingCloseCleanupConfirmation struct {
	taskID                      string
	taskIDs                     []string
	closeTaskIDs                []string
	previousStatus              domain.Status
	targetStatus                domain.Status
	bulkMode                    string
	delta                       int
	summaries                   []closeCleanupTaskSummary
	closeCleanChildren          bool
	targetOnlyBlockedByChildren bool
}

type pendingReviewCascadeConfirmation struct {
	taskID         string
	previousStatus domain.Status
	childIDs       []string
}

type closeCleanupConfirmPreflightMsg struct {
	pending        pendingCloseCleanupConfirmation
	summaries      []closeCleanupTaskSummary
	refreshedTasks []domain.Task
	err            error
}

type closeCleanupTaskSummary struct {
	taskID      string
	hasWorktree bool
	hasSession  bool
	dirty       bool
	conflicted  bool
	conflicts   []string
	ahead       int
	behind      int
	additions   int
	deletions   int
}

type refreshTaskWorkspaceResultMsg struct {
	projectID     string
	revision      uint64
	taskID        string
	hasTask       bool
	task          domain.Task
	tasks         []domain.Task
	lastCheckedAt time.Time
	freshness     protocol.TaskListFreshness
	reconcileErr  error
	snapshotErr   error
	decisionLinks []overlay.DecisionLinkSummary
	decisionErr   error
}

func (m Model) refreshTaskWorkspaceInBackgroundCmd(taskID string) tea.Cmd {
	projectID := m.daemonProjectID()
	return func() tea.Msg {
		msg := refreshTaskWorkspaceResultMsg{projectID: projectID, taskID: taskID}
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
		snapshot, err := m.daemonClient.GetTaskSnapshotWithMode(snapshotCtx, msg.taskID, daemonclient.ReadWaitModeExplicit)
		if err != nil {
			msg.snapshotErr = err
			return msg
		}
		if err := snapshot.RequireFullDetails("task workspace refresh"); err != nil {
			msg.snapshotErr = err
			return msg
		}

		msg.tasks = snapshot.Tasks
		msg.revision = snapshot.Revision
		msg.lastCheckedAt = snapshot.LastCheckedAt
		msg.freshness = snapshot.Freshness

		// Fetch decision links for this issue. Failure here is non-fatal — the rest of the
		// refresh continues without a Decisions section. The decision feature is new and
		// the daemon may not yet expose it on older builds.
		if issueID != "" && taskIDKey(issueID) != "main" {
			decisionCtx, decisionCancel := context.WithTimeout(context.Background(), 5*time.Second)
			result, decisionErr := m.daemonClient.ListDecisionLinks(decisionCtx, daemonclient.DecisionLinkListRequest{
				TargetKind:       daemonclient.DecisionTargetIssue,
				TargetID:         issueID,
				IncludeDecisions: true,
			})
			decisionCancel()
			if decisionErr != nil {
				msg.decisionErr = decisionErr
			} else {
				msg.decisionLinks = decisionLinkSummariesFrom(result)
			}
		}

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

// decisionLinkSummariesFrom maps a daemonclient response into the overlay-local
// DecisionLinkSummary type. Decisions are joined by slug so the rendered row can
// show the title and status alongside the link relation.
func decisionLinkSummariesFrom(result daemonclient.DecisionLinkListResult) []overlay.DecisionLinkSummary {
	if len(result.Links) == 0 {
		return nil
	}
	decisionsByID := make(map[string]daemonclient.Decision, len(result.Decisions))
	for _, d := range result.Decisions {
		decisionsByID[d.ID] = d
	}
	out := make([]overlay.DecisionLinkSummary, 0, len(result.Links))
	for _, link := range result.Links {
		summary := overlay.DecisionLinkSummary{
			DecisionID: link.DecisionID,
			Relation:   string(link.Relation),
			Note:       link.Note,
		}
		if d, ok := decisionsByID[link.DecisionID]; ok {
			summary.DecisionTitle = d.Title
		}
		out = append(out, summary)
	}
	return out
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

func (m Model) cleanupWorktreeCmd(taskID string, deleteTask bool, force bool, deleteBranch bool) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, deleteBranch: deleteBranch, err: fmt.Errorf("daemon client unavailable")}
		}

		if deleteTask {
			deleteCtx, deleteCancel := context.WithTimeout(context.Background(), worktreeCleanupMutationTimeout)
			_, err := m.daemonClient.DeleteTaskWithOptions(deleteCtx, taskID, daemonclient.TaskDeleteOptions{
				Cleanup:       true,
				ForceWorktree: force,
			})
			deleteCancel()
			if err != nil {
				if pending, ok := pendingOperationDetails(err); ok {
					return worktreeCleanupResultMsg{
						taskID:       taskID,
						deletedTask:  true,
						force:        force,
						deleteBranch: deleteBranch,
						operationID:  pending.OperationID,
						state:        pending.State,
					}
				}
				if !force && isDirtyWorktreeRemovalError(err) {
					return worktreeCleanupResultMsg{
						taskID:       taskID,
						deletedTask:  true,
						force:        force,
						deleteBranch: deleteBranch,
						needsForce:   true,
						reason:       strings.TrimSpace(err.Error()),
					}
				}
				return worktreeCleanupResultMsg{taskID: taskID, deletedTask: true, force: force, deleteBranch: deleteBranch, err: err}
			}
			return worktreeCleanupResultMsg{taskID: taskID, deletedTask: true, force: force, deleteBranch: deleteBranch}
		}

		// Always ask daemon to stop first; local projection may be stale.
		m.sessionMonitor.Stop(taskID)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), worktreeCleanupMutationTimeout)
		_, stopErr := m.daemonClient.StopSession(stopCtx, taskID)
		stopCancel()
		if stopErr != nil {
			if !isSessionAlreadyStoppedError(stopErr) && !isSessionStopSkippableDuringCleanup(stopErr) {
				return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, deleteBranch: deleteBranch, err: stopErr}
			}
		}

		removeCtx, removeCancel := context.WithTimeout(context.Background(), worktreeCleanupMutationTimeout)
		result, err := m.daemonClient.RemoveWorktreeWithOptions(removeCtx, taskID, daemonclient.WorktreeRemoveOptions{
			Force:        force,
			DeleteBranch: deleteBranch,
		})
		removeCancel()
		if err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return worktreeCleanupResultMsg{
					taskID:       taskID,
					deletedTask:  deleteTask,
					force:        force,
					deleteBranch: deleteBranch,
					operationID:  pending.OperationID,
					state:        pending.State,
				}
			}
			if !force && isDirtyWorktreeRemovalError(err) {
				return worktreeCleanupResultMsg{
					taskID:       taskID,
					deletedTask:  deleteTask,
					force:        force,
					deleteBranch: deleteBranch,
					needsForce:   true,
					reason:       strings.TrimSpace(err.Error()),
				}
			}
			return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, deleteBranch: deleteBranch, err: err}
		}

		return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, deleteBranch: result.BranchDeleted, branch: result.Branch}
	}
}

func (m Model) requestWorktreeCleanupConfirmationCmd(taskID string, deleteTask bool) tea.Cmd {
	projectID := m.daemonProjectID()
	return func() tea.Msg {
		msg := worktreeCleanupConfirmPromptMsg{
			projectID:    projectID,
			taskID:       taskID,
			deletedTask:  deleteTask,
			deleteBranch: deleteTask,
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
		msg.revision = snapshot.Revision
		msg.freshness = snapshot.Freshness
		msg.checkedAt = snapshot.LastCheckedAt

		for _, task := range snapshot.Tasks {
			if taskIDKey(task.ID.String()) != taskIDKey(taskID) {
				continue
			}
			msg.task = task
			msg.hasTask = true
			msg.hasWorktree = task.HasWorktree
			msg.ahead = task.GitAheadCount
			msg.behind = task.GitBehindCount
			msg.additions = task.GitAdditions
			msg.deletions = task.GitDeletions
			msg.dirty = task.HasUncommittedChanges
			msg.force = msg.dirty
			break
		}
		if worktrees, err := m.daemonClient.ListWorktrees(ctx); err != nil {
			msg.branchErr = err
		} else {
			for _, worktree := range worktrees {
				if taskIDKey(worktree.IssueID) == taskIDKey(taskID) {
					msg.branch = strings.TrimSpace(worktree.Branch)
					break
				}
			}
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
			fmt.Sprintf("- Branch: %s", cleanupBranchLabel(msg.branch)),
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
	if msg.branchErr != nil {
		lines = append(lines, fmt.Sprintf("Note: branch lookup warning: %v", msg.branchErr))
	}

	if msg.force {
		lines = append(lines, "", "Force removal will discard modified/untracked files.")
	}

	if msg.deletedTask {
		lines = append(lines, "", "Press Y to delete the task, worktree, and branch, or N to cancel.")
	} else {
		lines = append(lines, "", "Press Y to delete the worktree only, B to delete the worktree and branch, or N to cancel.")
	}
	return strings.Join(lines, "\n")
}

func cleanupBranchLabel(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "unknown"
	}
	return branch
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
	layout := m.boardColumnLayout(columns)
	pos := m.nav.GetPosition(columns)
	columnWidth := layout.ColumnWidth
	if pos.Valid {
		columnWidth = layout.WidthForColumn(pos.Column)
	}
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
	layout := m.boardColumnLayout(columns)
	cardWidth := board.CardContentWidth(layout.WidthForColumn(pos.Column))
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

func (m Model) boardColumnLayout(columns []board.Column) board.ColumnLayout {
	return board.NewColumnLayout(len(columns), m.width, m.columnViewportStart)
}

func (m *Model) ensureColumnVisible(pos navigation.Position, totalColumns int) {
	layout := board.NewColumnLayout(totalColumns, m.width, m.columnViewportStart)
	if pos.Valid {
		layout = layout.WithColumnVisible(pos.Column)
	}
	m.columnViewportStart = layout.ViewportStart
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
	if (filter == nil || !filter.IsActive()) && !m.sessionTreeFilterOnly {
		return "F:none"
	}

	parts := make([]string, 0, 7)
	if filter != nil {
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
	}
	if m.sessionTreeFilterOnly {
		parts = append(parts, "tree:session")
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
	if m.boardView.Options.SortPolicy == domain.BoardViewSortHumanAttention && !m.editor.IsSortExplicit() {
		field = "attention+" + field
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
	now := time.Now().UTC()
	historyID := m.recordNotificationHistory(toast, now)
	if historyID == "" {
		return
	}
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
		EmittedAt: now,
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
	case protocol.EventTaskRestored:
		return "Task restored"
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
	m.feedback.expireLocalToasts(now)
	m.refreshFeedbackProjectionOutputs(now)
}

func (m *Model) dismissLatestToast() bool {
	if len(m.toasts) == 0 {
		return false
	}
	if len(m.activeToastHistoryIDs) == 0 {
		m.toasts = m.toasts[:len(m.toasts)-1]
		return true
	}
	if len(m.activeToastHistoryIDs) == len(m.toasts) {
		m.markNotificationDismissed(m.activeToastHistoryIDs[len(m.activeToastHistoryIDs)-1])
	} else if len(m.activeToastHistoryIDs) > len(m.toasts) {
		m.activeToastHistoryIDs = m.activeToastHistoryIDs[:len(m.toasts)-1]
		m.refreshFeedbackProjectionOutputs(time.Now())
	} else {
		m.toasts = m.toasts[:len(m.toasts)-1]
	}
	return true
}

// Git operation commands

type fetchAndMergeResultMsg struct {
	worktree    string
	issueID     string
	project     asyncRecoveryProjectContext
	attachAfter bool
	result      *daemonclient.MergeResult
	stage       string
	operationID string
	state       protocol.OperationState
	err         error
}

type pullBaseResultMsg struct {
	worktree    string
	remote      string
	baseBranch  string
	operationID string
	state       protocol.OperationState
	err         error
}

type gitPaneStatusMsg struct {
	status daemonclient.GitStatus
	err    error
}
type gitPanePushResultMsg struct {
	branch, operationID string
	state               protocol.OperationState
	err                 error
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
	return m.bulkMoveStatusCmdWithOptions(taskIDs, delta, daemonclient.TaskStatusOptions{})
}

func (m Model) bulkMoveStatusCmdWithOptions(taskIDs []string, delta int, opts daemonclient.TaskStatusOptions) tea.Cmd {
	return func() tea.Msg {
		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)
		pendingOps := make([]bulkTaskPendingOperation, 0)

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

			action, ok := shiftedTaskLifecycleAction(*currentTask, delta)
			if !ok {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "cannot move beyond lifecycle action bounds"})
				continue
			}
			newStatus := action.LegacyStatus()

			var err error
			if action.IsLifecycleOnly() {
				err = m.updateTaskLifecycleWithTimeout(taskID, action.Lifecycle, 10*time.Second)
			} else {
				err = m.updateTaskStatusWithTimeoutOptions(taskID, newStatus, 10*time.Second, opts)
			}
			if err != nil {
				if pending, ok := pendingOperationDetails(err); ok {
					pendingOps = append(pendingOps, bulkTaskPendingOperation{
						taskID:         taskID,
						action:         "task_move",
						previousStatus: currentTask.Status,
						targetStatus:   newStatus,
						operationID:    pending.OperationID,
						state:          pending.State,
					})
					continue
				}
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: err.Error()})
				continue
			}

			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed, pending: pendingOps}
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

func (m Model) bulkCleanupWorktreeCmd(taskIDs []string, deleteTask bool, deleteBranch bool) tea.Cmd {
	return func() tea.Msg {
		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)
		pendingOps := make([]bulkTaskPendingOperation, 0)

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

			if deleteTask {
				deleteCtx, deleteCancel := context.WithTimeout(context.Background(), worktreeCleanupMutationTimeout)
				_, err := m.daemonClient.DeleteTaskWithOptions(deleteCtx, taskID, daemonclient.TaskDeleteOptions{Cleanup: true})
				deleteCancel()
				if err != nil {
					if pending, ok := pendingOperationDetails(err); ok {
						pendingOps = append(pendingOps, bulkTaskPendingOperation{
							taskID:      taskID,
							action:      "worktree_cleanup",
							deletedTask: true,
							operationID: pending.OperationID,
							state:       pending.State,
						})
						continue
					}
					failed++
					reason := err.Error()
					if isDirtyWorktreeRemovalError(err) {
						reason = fmt.Sprintf("%s (single-task cleanup supports force)", strings.TrimSpace(err.Error()))
					}
					issues = append(issues, bulkTaskIssue{taskID: taskID, reason: reason})
					continue
				}
				updated++
				continue
			}

			// Always ask daemon to stop first; local projection may be stale.
			m.sessionMonitor.Stop(taskID)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), worktreeCleanupMutationTimeout)
			_, stopErr := m.daemonClient.StopSession(stopCtx, taskID)
			stopCancel()
			if stopErr != nil && !isSessionAlreadyStoppedError(stopErr) && !isSessionStopSkippableDuringCleanup(stopErr) {
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: stopErr.Error()})
				continue
			}

			removeCtx, removeCancel := context.WithTimeout(context.Background(), worktreeCleanupMutationTimeout)
			_, removeErr := m.daemonClient.RemoveWorktreeWithOptions(removeCtx, taskID, daemonclient.WorktreeRemoveOptions{
				DeleteBranch: deleteBranch,
			})
			removeCancel()
			if removeErr != nil {
				if pending, ok := pendingOperationDetails(removeErr); ok {
					pendingOps = append(pendingOps, bulkTaskPendingOperation{
						taskID:      taskID,
						action:      "worktree_cleanup",
						deletedTask: deleteTask,
						operationID: pending.OperationID,
						state:       pending.State,
					})
					continue
				}
				failed++
				reason := removeErr.Error()
				if isDirtyWorktreeRemovalError(removeErr) {
					reason = fmt.Sprintf("%s (single-task cleanup supports force)", strings.TrimSpace(removeErr.Error()))
				}
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: reason})
				continue
			}

			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed, pending: pendingOps}
	}
}

func (m Model) bulkCleanupPreflightCmd(taskIDs []string, deleteTask bool) tea.Cmd {
	selected := append([]string(nil), taskIDs...)
	projectID := m.daemonProjectID()
	return func() tea.Msg {
		msg := bulkCleanupPreflightMsg{
			projectID:    projectID,
			taskIDs:      selected,
			deletedTasks: deleteTask,
			deleteBranch: deleteTask,
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
		msg.revision = snapshot.Revision
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
			msg.refreshedTasks = append(msg.refreshedTasks, task)
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
	return m.bulkSetStatusCmdWithOptions(taskIDs, status, daemonclient.TaskStatusOptions{})
}

func (m Model) bulkSetStatusCmdWithOptions(taskIDs []string, status domain.Status, opts daemonclient.TaskStatusOptions) tea.Cmd {
	return func() tea.Msg {
		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)
		pendingOps := make([]bulkTaskPendingOperation, 0)

		for _, taskID := range taskIDs {
			if !m.taskExists(taskID) {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "task not found"})
				continue
			}
			previousStatus, _ := m.taskStatusByID(taskID)
			err := m.updateTaskStatusWithTimeoutOptions(taskID, status, 10*time.Second, opts)
			if err != nil {
				if pending, ok := pendingOperationDetails(err); ok {
					pendingOps = append(pendingOps, bulkTaskPendingOperation{
						taskID:         taskID,
						action:         "task_move",
						previousStatus: previousStatus,
						targetStatus:   status,
						operationID:    pending.OperationID,
						state:          pending.State,
					})
					continue
				}
				failed++
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: err.Error()})
				continue
			}
			updated++
		}

		return bulkStatusResultMsg{updated: updated, issues: issues, failed: failed, pending: pendingOps}
	}
}

func (m Model) bulkSetLifecycleCmd(taskIDs []string, lifecycle domain.IssueWorkflow) tea.Cmd {
	return func() tea.Msg {
		updated := 0
		failed := 0
		issues := make([]bulkTaskIssue, 0)

		for _, taskID := range taskIDs {
			if !m.taskExists(taskID) {
				issues = append(issues, bulkTaskIssue{taskID: taskID, reason: "task not found"})
				continue
			}
			if err := m.updateTaskLifecycleWithTimeout(taskID, lifecycle, 10*time.Second); err != nil {
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

func bulkStatusSummaryHasCloseGuardGuidance(issues []bulkTaskIssue) bool {
	for _, item := range issues {
		reason := strings.ToLower(item.reason)
		if strings.Contains(reason, "cannot close issue") ||
			strings.Contains(reason, "next:") ||
			strings.Contains(reason, "close guard") ||
			strings.Contains(reason, "moved closed blockers back for cleanup") {
			return true
		}
	}
	return false
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
	if msg.deletedTasks {
		lines = append(lines, "", "Press Y to delete tasks, worktrees, and branches, or N to cancel.")
	} else {
		lines = append(lines, "", "Press Y to delete worktrees only, B to delete worktrees and branches, or N to cancel.")
	}
	return strings.Join(lines, "\n")
}

func (m Model) saveTaskCmd(msg overlay.TaskCreatedMsg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if msg.ID != "" {
			details := pendingTaskDetails{
				title:           msg.Title,
				description:     msg.Description,
				design:          msg.Design,
				notes:           msg.Notes,
				acceptance:      msg.Acceptance,
				estimate:        cloneIntPtr(msg.Estimate),
				taskType:        msg.Type,
				priority:        msg.Priority,
				implementations: append([]string(nil), msg.Implementations...),
				updatedAt:       time.Now(),
			}
			if m.daemonClient == nil {
				return taskCreatedResultMsg{taskID: msg.ID, err: fmt.Errorf("daemon client unavailable"), isUpdate: true}
			}
			design := msg.Design
			notes := msg.Notes
			acceptance := msg.Acceptance
			var lifecycle *domain.IssueWorkflow
			if msg.Lifecycle != "" {
				lifecycle = &msg.Lifecycle
			}
			err := m.daemonClient.UpdateTaskDetails(ctx, msg.ID, daemonclient.TaskUpdateParams{
				Title:           msg.Title,
				Description:     msg.Description,
				Design:          &design,
				Notes:           &notes,
				Acceptance:      &acceptance,
				Estimate:        msg.Estimate,
				EstimateSet:     true,
				Type:            msg.Type,
				Priority:        msg.Priority,
				Lifecycle:       lifecycle,
				Implementations: msg.Implementations,
			})
			if err != nil {
				return taskCreatedResultMsg{taskID: msg.ID, err: err, isUpdate: true}
			}
			return taskCreatedResultMsg{taskID: msg.ID, isUpdate: true, updateDetails: &details}
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
			Lifecycle:       msg.Lifecycle,
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
	closeContext   closeFailureOperationContext
	previousStatus domain.Status
	newStatus      domain.Status
	opts           daemonclient.TaskStatusOptions
	err            error
}

type taskLifecycleResultMsg struct {
	taskID        string
	previousTask  domain.Task
	targetAction  taskLifecycleAction
	targetLegacy  domain.Status
	targetDisplay string
	err           error
}

type taskOwnershipResultMsg struct {
	taskID string
	action string
	task   domain.Task
	err    error
}

type closeFailureOperationContext struct {
	projectID      string
	projectName    string
	projectPath    string
	daemonSocket   string
	baseBranch     string
	parentID       string
	sourceWorktree string
}

func (m Model) issueOwnershipCmd(taskID string, action string, force bool) tea.Cmd {
	taskID = strings.TrimSpace(taskID)
	action = strings.TrimSpace(action)
	return func() tea.Msg {
		if m.daemonClient == nil {
			return taskOwnershipResultMsg{taskID: taskID, action: action, err: fmt.Errorf("daemon client unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req := daemonclient.TaskOwnershipRequest{
			OwnerID:   tuiIssueOwnerID(),
			OwnerKind: "human",
			Force:     force,
		}
		var (
			task domain.Task
			err  error
		)
		switch action {
		case "claim":
			task, err = m.daemonClient.ClaimTaskOwnership(ctx, taskID, req)
		case "release":
			task, err = m.daemonClient.ReleaseTaskOwnership(ctx, taskID, req)
		default:
			err = fmt.Errorf("unknown ownership action %q", action)
		}
		return taskOwnershipResultMsg{taskID: taskID, action: action, task: task, err: err}
	}
}

func tuiIssueOwnerID() string {
	for _, key := range []string{"AZEDARACH_AUDIT_ACTOR", "USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "tui"
}

// moveTaskStatusCmd updates a single task's status.
func (m Model) moveTaskStatusCmd(taskID string, previousStatus, newStatus domain.Status) tea.Cmd {
	return m.moveTaskStatusCmdWithOptions(taskID, previousStatus, newStatus, daemonclient.TaskStatusOptions{})
}

func (m Model) moveTaskStatusCmdWithOptions(taskID string, previousStatus, newStatus domain.Status, opts daemonclient.TaskStatusOptions) tea.Cmd {
	closeContext := m.closeFailureOperationContext(taskID)
	return func() tea.Msg {
		err := m.updateTaskStatusWithTimeoutOptions(taskID, newStatus, 5*time.Second, opts)
		if err != nil {
			return taskStatusResultMsg{
				taskID:         taskID,
				closeContext:   closeContext,
				previousStatus: previousStatus,
				newStatus:      newStatus,
				opts:           opts,
				err:            err,
			}
		}

		return taskStatusResultMsg{
			taskID:         taskID,
			closeContext:   closeContext,
			previousStatus: previousStatus,
			newStatus:      newStatus,
			opts:           opts,
		}
	}
}

func (m Model) moveTaskLifecycleCmd(task domain.Task, action taskLifecycleAction) tea.Cmd {
	targetLegacy := action.LegacyStatus()
	return func() tea.Msg {
		err := m.updateTaskLifecycleWithTimeout(task.ID.String(), action.Lifecycle, 5*time.Second)
		return taskLifecycleResultMsg{
			taskID:        task.ID.String(),
			previousTask:  task,
			targetAction:  action,
			targetLegacy:  targetLegacy,
			targetDisplay: action.DisplayName(),
			err:           err,
		}
	}
}

func (m Model) moveTaskStatusCascadeChildrenCmd(taskID string, previousStatus, newStatus domain.Status) tea.Cmd {
	return func() tea.Msg {
		opts := daemonclient.TaskStatusOptions{CascadeChildren: true}
		err := m.updateTaskStatusWithTimeoutOptions(taskID, newStatus, 5*time.Second, opts)
		if err != nil {
			return taskStatusResultMsg{
				taskID:         taskID,
				previousStatus: previousStatus,
				newStatus:      newStatus,
				opts:           opts,
				err:            err,
			}
		}

		return taskStatusResultMsg{
			taskID:         taskID,
			previousStatus: previousStatus,
			newStatus:      newStatus,
			opts:           opts,
		}
	}
}

func (m Model) updateTaskStatusWithTimeout(taskID string, status domain.Status, defaultTimeout time.Duration) error {
	return m.updateTaskStatusWithTimeoutOptions(taskID, status, defaultTimeout, daemonclient.TaskStatusOptions{})
}

func (m Model) updateTaskStatusWithTimeoutOptions(taskID string, status domain.Status, defaultTimeout time.Duration, opts daemonclient.TaskStatusOptions) error {
	if m.daemonClient == nil {
		return fmt.Errorf("daemon client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskStatusMutationTimeout(status, defaultTimeout))
	defer cancel()
	if terminalTaskStatusRequiresClose(status) {
		closeOpts := taskStatusOptionsForStatus(status)
		closeOpts.ForceWorktree = opts.ForceWorktree
		closeOpts.IgnoreAhead = opts.IgnoreAhead
		closeOpts.CloseCleanChildren = opts.CloseCleanChildren
		closeOpts.AllowActiveSession = opts.AllowActiveSession
		return m.daemonClient.UpdateTaskStatusWithOptions(ctx, taskID, status, closeOpts)
	}
	return m.daemonClient.UpdateTaskStatusWithOptions(ctx, taskID, status, opts)
}

func (m Model) updateTaskLifecycleWithTimeout(taskID string, lifecycle domain.IssueWorkflow, defaultTimeout time.Duration) error {
	if m.daemonClient == nil {
		return fmt.Errorf("daemon client unavailable")
	}
	task, _, ok := m.taskAndSessionByID(taskID)
	if !ok || task == nil {
		return fmt.Errorf("task not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return m.daemonClient.UpdateTaskDetails(ctx, taskID, daemonclient.TaskUpdateParams{
		Title:       task.Title,
		Description: task.Description,
		Type:        task.Type,
		Priority:    task.Priority,
		Lifecycle:   &lifecycle,
	})
}

func (m Model) reviewCascadeChildIDs(parentID string) []string {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	tasksByID := make(map[string]domain.Task, len(m.tasks))
	childrenByParent := make(map[string][]string)
	for _, task := range m.tasks {
		id := strings.TrimSpace(task.ID.String())
		if id == "" {
			continue
		}
		tasksByID[id] = task
		if task.ParentID != nil {
			parent := strings.TrimSpace(task.ParentID.String())
			if parent != "" {
				childrenByParent[parent] = append(childrenByParent[parent], id)
			}
		}
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyParentChild || string(dep.Type) == "parent_child" {
				parent := strings.TrimSpace(dep.ID.String())
				if parent != "" {
					childrenByParent[parent] = append(childrenByParent[parent], id)
				}
			}
		}
	}
	seen := make(map[string]struct{})
	queue := append([]string(nil), childrenByParent[parentID]...)
	out := make([]string, 0, len(queue))
	for len(queue) > 0 {
		childID := queue[0]
		queue = queue[1:]
		if _, ok := seen[childID]; ok {
			continue
		}
		seen[childID] = struct{}{}
		child, ok := tasksByID[childID]
		if ok {
			switch child.IssueDisplayPhase() {
			case domain.IssueDisplayReview, domain.IssueDisplayDone, domain.IssueDisplayCancelled:
				// Already review-ready or terminal from the user's perspective.
			default:
				out = append(out, childID)
			}
		}
		queue = append(queue, childrenByParent[childID]...)
	}
	sort.Strings(out)
	return out
}

func formatReviewCascadeConfirmPrompt(pending pendingReviewCascadeConfirmation) string {
	lines := []string{
		fmt.Sprintf("Parent: %s", pending.taskID),
		fmt.Sprintf("Children to move: %d", len(pending.childIDs)),
		"",
	}
	for _, childID := range pending.childIDs {
		lines = append(lines, "- "+childID)
	}
	lines = append(lines, "", "Request review for these child issues, then request review for the parent?")
	return strings.Join(lines, "\n")
}

func taskStatusMutationTimeout(status domain.Status, defaultTimeout time.Duration) time.Duration {
	if terminalTaskStatusRequiresClose(status) {
		return taskCloseMutationTimeout
	}
	return defaultTimeout
}

type taskLifecycleAction struct {
	Lifecycle domain.IssueWorkflow
	Status    domain.Status
	Phase     domain.IssueDisplayPhase
}

func (a taskLifecycleAction) IsZero() bool {
	return a.Lifecycle == "" && a.Status == "" && a.Phase == ""
}

func (a taskLifecycleAction) LegacyStatus() domain.Status {
	if a.Status != "" {
		return a.Status
	}
	switch a.Lifecycle {
	case domain.IssueWorkflowBacklog, domain.IssueWorkflowOpen:
		return domain.StatusOpen
	case domain.IssueWorkflowActive:
		return domain.StatusInProgress
	default:
		return ""
	}
}

func (a taskLifecycleAction) DisplayName() string {
	if a.Phase != "" {
		if a.Phase == domain.IssueDisplayReview {
			return "Review Requested"
		}
		return a.Phase.Label()
	}
	return statusDisplayName(a.LegacyStatus())
}

func (a taskLifecycleAction) RequiresClose() bool {
	return terminalTaskStatusRequiresClose(a.LegacyStatus())
}

func (a taskLifecycleAction) IsLifecycleOnly() bool {
	return a.Lifecycle != "" && a.Status == ""
}

func taskLifecycleActionForPhase(phase domain.IssueDisplayPhase) (taskLifecycleAction, bool) {
	switch phase {
	case domain.IssueDisplayBacklog:
		return taskLifecycleAction{Lifecycle: domain.IssueWorkflowBacklog, Phase: domain.IssueDisplayBacklog}, true
	case domain.IssueDisplayOpen:
		return taskLifecycleAction{Lifecycle: domain.IssueWorkflowOpen, Phase: domain.IssueDisplayOpen}, true
	case domain.IssueDisplayActive:
		return taskLifecycleAction{Status: domain.StatusInProgress, Phase: domain.IssueDisplayActive}, true
	case domain.IssueDisplayReview:
		return taskLifecycleAction{Status: domain.StatusInReview, Phase: domain.IssueDisplayReview}, true
	case domain.IssueDisplayDone:
		return taskLifecycleAction{Status: domain.StatusDone, Phase: domain.IssueDisplayDone}, true
	case domain.IssueDisplayCancelled:
		return taskLifecycleAction{Status: domain.StatusCancelled, Phase: domain.IssueDisplayCancelled}, true
	default:
		return taskLifecycleAction{}, false
	}
}

func taskActionPhase(task domain.Task) domain.IssueDisplayPhase {
	state, err := task.IssueState()
	if err != nil || state.IsZero() {
		return domain.IssueDisplayUnknown
	}
	return state.DisplayPhase()
}

func shiftedTaskLifecycleAction(task domain.Task, delta int) (taskLifecycleAction, bool) {
	phaseOrder := []domain.IssueDisplayPhase{
		domain.IssueDisplayBacklog,
		domain.IssueDisplayOpen,
		domain.IssueDisplayActive,
		domain.IssueDisplayReview,
		domain.IssueDisplayDone,
	}
	currentPhase := taskActionPhase(task)
	currentIdx := -1
	for i, phase := range phaseOrder {
		if phase == currentPhase {
			currentIdx = i
			break
		}
	}
	if currentIdx == -1 {
		return taskLifecycleAction{}, false
	}
	newIdx := currentIdx + delta
	if newIdx < 0 || newIdx >= len(phaseOrder) {
		return taskLifecycleAction{}, false
	}
	return taskLifecycleActionForPhase(phaseOrder[newIdx])
}

func exactTaskActionForKey(key string) (taskLifecycleAction, bool) {
	switch key {
	case "0":
		return taskLifecycleActionForPhase(domain.IssueDisplayBacklog)
	case "1":
		return taskLifecycleActionForPhase(domain.IssueDisplayOpen)
	case "2":
		return taskLifecycleActionForPhase(domain.IssueDisplayActive)
	case "3":
		return taskLifecycleActionForPhase(domain.IssueDisplayReview)
	case "4":
		return taskLifecycleActionForPhase(domain.IssueDisplayDone)
	case "5":
		return taskLifecycleActionForPhase(domain.IssueDisplayCancelled)
	default:
		return taskLifecycleAction{}, false
	}
}

func statusDisplayName(status domain.Status) string {
	switch status {
	case domain.StatusOpen:
		return "Open"
	case domain.StatusInProgress:
		return "In Progress"
	case domain.StatusInReview:
		return "In Review"
	case domain.StatusDone:
		return "Done"
	case domain.StatusCancelled:
		return "Cancelled"
	default:
		return status.String()
	}
}

func taskStatusOptionsForStatus(status domain.Status) daemonclient.TaskStatusOptions {
	switch status {
	case domain.StatusDone:
		return daemonclient.TaskStatusOptions{IntegrateBeforeClose: true, CloseOutcome: domain.IssueCloseCompleted}
	case domain.StatusCancelled:
		return daemonclient.TaskStatusOptions{CloseOutcome: domain.IssueCloseCancelled}
	default:
		return daemonclient.TaskStatusOptions{}
	}
}

func terminalTaskStatusRequiresClose(status domain.Status) bool {
	return status == domain.StatusDone || status == domain.StatusCancelled
}

func (m Model) closeFailureOperationContext(taskID string) closeFailureOperationContext {
	ctx := closeFailureOperationContext{
		projectID:    m.daemonProjectID(),
		projectName:  strings.TrimSpace(m.currentProject),
		projectPath:  strings.TrimSpace(m.activeProjectPath()),
		daemonSocket: strings.TrimSpace(m.daemonSocketPath),
		baseBranch:   strings.TrimSpace(m.resolveBaseBranch()),
	}
	if task, session, ok := m.taskAndSessionByID(taskID); ok {
		if task.ParentID != nil {
			ctx.parentID = strings.TrimSpace(task.ParentID.String())
		}
		if session != nil {
			ctx.sourceWorktree = strings.TrimSpace(session.Worktree)
		}
		if ctx.sourceWorktree == "" && task.Session != nil {
			ctx.sourceWorktree = strings.TrimSpace(task.Session.Worktree)
		}
	}
	return ctx
}

func (m Model) projectActionContext() overlay.ProjectActionContext {
	return overlay.ProjectActionContext{
		ProjectID:    m.daemonProjectID(),
		ProjectName:  strings.TrimSpace(m.currentProject),
		ProjectPath:  strings.TrimSpace(m.activeProjectPath()),
		DaemonSocket: strings.TrimSpace(m.daemonSocketPath),
		BaseBranch:   strings.TrimSpace(m.resolveBaseBranch()),
	}
}

func (m Model) closeFailureDialogCmd(msg taskStatusResultMsg) tea.Cmd {
	if msg.err == nil || !terminalTaskStatusRequiresClose(msg.newStatus) {
		return nil
	}
	closeContext := msg.closeContext
	if strings.TrimSpace(closeContext.projectID) == "" {
		closeContext = m.closeFailureOperationContext(msg.taskID)
	}
	options := overlay.CloseFailureDialogOptions{
		ProjectID:               closeContext.projectID,
		ProjectName:             closeContext.projectName,
		ProjectPath:             closeContext.projectPath,
		DaemonSocket:            closeContext.daemonSocket,
		BaseBranch:              closeContext.baseBranch,
		ParentID:                closeContext.parentID,
		SourceWorktree:          closeContext.sourceWorktree,
		PreviousStatus:          msg.previousStatus.String(),
		TargetStatus:            msg.newStatus.String(),
		ForceWorktree:           msg.opts.ForceWorktree,
		CloseCleanChildren:      msg.opts.CloseCleanChildren,
		AllowActiveSession:      msg.opts.AllowActiveSession,
		AllowAIMerge:            msg.newStatus == domain.StatusDone,
		AllowForceWorktree:      closeFailureSupportsForceWorktree(msg.err),
		AllowActiveSessionRetry: closeFailureSupportsActiveSessionRetry(msg.err),
		AllowCloseCleanChildren: closeFailureSupportsCloseCleanChildren(msg.err),
	}
	return m.openOverlay(overlay.NewCloseFailureDialog(msg.taskID, msg.err.Error(), options))
}

func closeFailureSupportsForceWorktree(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return isDirtyWorktreeRemovalError(err) ||
		strings.Contains(message, "dirty worktree") ||
		strings.Contains(message, "worktree has local changes") ||
		strings.Contains(message, "modified or untracked") ||
		strings.Contains(message, "force worktree") ||
		strings.Contains(message, "--force-worktree")
}

func closeFailureSupportsActiveSessionRetry(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "session activity is ") &&
		strings.Contains(message, "session projection to report idle/done/terminal activity")
}

func closeFailureSupportsCloseCleanChildren(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "unresolved child issues remain") ||
		strings.Contains(message, "close clean child") ||
		strings.Contains(message, "close-clean-children") ||
		strings.Contains(message, "clean unresolved child")
}

func (m Model) bulkMoveNeedsCloseCleanupConfirmation(taskIDs []string, delta int) bool {
	return len(m.bulkMoveCloseCleanupTaskIDs(taskIDs, delta)) > 0
}

func (m Model) bulkMoveCloseCleanupTaskIDs(taskIDs []string, delta int) []string {
	closeTaskIDs := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		task, _, ok := m.taskAndSessionByID(taskID)
		if !ok || task == nil {
			continue
		}
		next, ok := shiftedTaskLifecycleAction(*task, delta)
		if ok && next.RequiresClose() {
			closeTaskIDs = append(closeTaskIDs, taskID)
		}
	}
	return closeTaskIDs
}

func (m Model) confirmCloseCleanupCmd(pending pendingCloseCleanupConfirmation) tea.Cmd {
	title := "Confirm integrate and close?"
	if pending.targetStatus == domain.StatusCancelled {
		title = "Confirm cancel and close?"
	}
	if pendingCloseCleanupCount(pending) > 1 {
		title = "Confirm bulk integrate and close?"
		if pending.targetStatus == domain.StatusCancelled {
			title = "Confirm bulk cancel and close?"
		}
	}
	return m.openOverlay(overlay.NewConfirmDialogExplicitYNWithExtraKeys(title, formatCloseCleanupConfirmPrompt(pending), map[string]overlay.SelectionMsg{
		"c": {Key: "close_clean_children", Value: overlay.ConfirmResult{Confirmed: true}},
		"C": {Key: "close_clean_children", Value: overlay.ConfirmResult{Confirmed: true}},
	}))
}

func (m Model) prepareCloseCleanupConfirmation(pending pendingCloseCleanupConfirmation) pendingCloseCleanupConfirmation {
	pending.targetOnlyBlockedByChildren = closeCleanupTargetsHaveBlockingDescendants(m.tasks, pendingCloseCleanupTargetIDs(pending))
	return pending
}

func (m Model) confirmCloseCleanupPreflightCmd(pending pendingCloseCleanupConfirmation) tea.Cmd {
	return func() tea.Msg {
		msg := closeCleanupConfirmPreflightMsg{pending: pending}
		if m.daemonClient == nil {
			msg.err = fmt.Errorf("daemon client unavailable")
			return msg
		}

		taskIDs := pendingCloseCleanupTargetIDs(pending)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if len(taskIDs) > 0 {
			if _, err := m.daemonClient.ReconcileRuntimeIssues(ctx, taskIDs); err != nil {
				msg.err = err
				return msg
			}
		}
		snapshot, err := m.readTaskSnapshot(ctx, m.daemonClient)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.summaries = closeCleanupSummariesFromTasks(snapshot.Tasks, taskIDs)
		msg.refreshedTasks = append([]domain.Task(nil), snapshot.Tasks...)
		return msg
	}
}

func (m Model) closeCleanupSummaries(taskIDs []string) []closeCleanupTaskSummary {
	return closeCleanupSummariesFromTasks(m.tasks, taskIDs)
}

func closeCleanupSummariesFromTasks(tasks []domain.Task, taskIDs []string) []closeCleanupTaskSummary {
	if len(taskIDs) == 0 {
		return nil
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[taskIDKey(task.ID.String())] = task
	}
	summaries := make([]closeCleanupTaskSummary, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		task, ok := byID[taskIDKey(taskID)]
		if !ok {
			continue
		}
		summaries = append(summaries, closeCleanupSummary(task))
	}
	return summaries
}

func closeCleanupSummary(task domain.Task) closeCleanupTaskSummary {
	summary := closeCleanupTaskSummary{
		taskID:      task.ID.String(),
		hasWorktree: task.HasWorktree,
		hasSession:  task.HasTmuxSession || task.Session != nil,
		dirty:       task.HasUncommittedChanges,
		conflicted:  task.HasConflicts,
		conflicts:   append([]string(nil), task.ConflictFiles...),
		ahead:       task.GitAheadCount,
		behind:      task.GitBehindCount,
		additions:   task.GitAdditions,
		deletions:   task.GitDeletions,
	}
	return summary
}

func formatCloseCleanupConfirmPrompt(pending pendingCloseCleanupConfirmation) string {
	closeCount := pendingCloseCleanupCount(pending)
	selectedCount := len(pending.taskIDs)
	count := closeCount
	if count == 0 && strings.TrimSpace(pending.taskID) != "" {
		count = 1
	}
	target := strings.TrimSpace(pending.taskID)
	statusLine := "Status: closed"
	if pending.targetStatus == domain.StatusCancelled {
		statusLine = "Status: cancelled"
	}
	if pending.bulkMode == "move" && selectedCount > 0 {
		switch {
		case closeCount == selectedCount:
			target = fmt.Sprintf("%d selected tasks", selectedCount)
		case closeCount > 0:
			target = fmt.Sprintf("%d of %d selected tasks", closeCount, selectedCount)
			statusLine = "Status: moving right; closing subset will close"
		default:
			target = fmt.Sprintf("%d selected tasks", selectedCount)
			statusLine = "Status: moving right"
		}
	} else if count > 1 {
		target = fmt.Sprintf("%d selected tasks", count)
	}
	if target == "" {
		target = "selected task"
	}
	intro := "Closing issues integrates their branch, then cleans up sessions and worktrees."
	if pending.targetStatus == domain.StatusCancelled {
		intro = "Cancelling issues skips branch integration, then cleans up sessions and worktrees."
	}
	lines := []string{
		intro,
		"",
		fmt.Sprintf("Target: %s", target),
		statusLine,
		"",
	}
	lines = append(lines, formatCloseCleanupGitStateLines(pending.summaries, count, len(pending.taskIDs) > 0)...)
	lines = append(lines,
		"",
		terminalCloseEffectLine(pending.targetStatus),
		"Dirty or conflicted worktrees must be cleaned before close; daemon guards still block unmerged or unresolved child work.",
		"Press C to also close clean child issues with no projected session, dirty state, or diff.",
		"",
	)
	if pending.targetOnlyBlockedByChildren {
		lines = append(lines,
			"Target-only close is unavailable while child issues remain unresolved.",
			"Proceed? C closes the target plus clean children; N cancels.",
		)
	} else {
		lines = append(lines, "Proceed? Y closes only the target; C closes the target plus clean children.")
	}
	return strings.Join(lines, "\n")
}

func terminalCloseEffectLine(status domain.Status) string {
	if status == domain.StatusCancelled {
		return "This may stop active sessions and remove issue worktrees before writing the cancelled close outcome."
	}
	return "This may merge into the closest ancestor worktree branch, stop active sessions, and remove issue worktrees before closing."
}

func pendingCloseCleanupBlockedReason(pending pendingCloseCleanupConfirmation) string {
	summaries := pending.summaries
	if len(summaries) == 0 {
		return ""
	}
	blocked := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if !summary.dirty && !summary.conflicted {
			continue
		}
		state := "dirty"
		if summary.conflicted {
			state = "conflicted"
		}
		if summary.dirty && summary.conflicted {
			state = "dirty/conflicted"
		}
		if summary.taskID == "" {
			blocked = append(blocked, state)
			continue
		}
		blocked = append(blocked, fmt.Sprintf("%s %s", summary.taskID, state))
	}
	if len(blocked) == 0 {
		return ""
	}
	if len(blocked) > 3 {
		blocked = append(blocked[:3], fmt.Sprintf("%d more", len(blocked)-3))
	}
	return "Close blocked: clean up dirty/conflicted worktree state first (" + strings.Join(blocked, ", ") + ")"
}

func pendingCloseCleanupTargetIDs(pending pendingCloseCleanupConfirmation) []string {
	switch {
	case len(pending.closeTaskIDs) > 0:
		return append([]string(nil), pending.closeTaskIDs...)
	case len(pending.taskIDs) > 0:
		return append([]string(nil), pending.taskIDs...)
	case strings.TrimSpace(pending.taskID) != "":
		return []string{strings.TrimSpace(pending.taskID)}
	default:
		return nil
	}
}

func closeCleanupTargetsHaveBlockingDescendants(tasks []domain.Task, targetIDs []string) bool {
	if len(tasks) == 0 || len(targetIDs) == 0 {
		return false
	}

	targetSet := make(map[string]struct{}, len(targetIDs))
	for _, targetID := range targetIDs {
		key := taskIDKey(targetID)
		if key != "" {
			targetSet[key] = struct{}{}
		}
	}
	if len(targetSet) == 0 {
		return false
	}

	byID := make(map[string]domain.Task, len(tasks))
	childrenByParent := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		taskID := taskIDKey(task.ID.String())
		if taskID == "" {
			continue
		}
		byID[taskID] = task
		if task.ParentID != nil {
			parentID := taskIDKey(task.ParentID.String())
			if parentID != "" {
				childrenByParent[parentID] = append(childrenByParent[parentID], taskID)
			}
		}
		for _, dep := range task.Dependencies {
			depType := string(dep.Type)
			if depType != string(domain.DependencyParentChild) && depType != "parent_child" {
				continue
			}
			parentID := taskIDKey(dep.ID.String())
			if parentID != "" {
				childrenByParent[parentID] = append(childrenByParent[parentID], taskID)
			}
		}
	}

	queue := make([]string, 0, len(targetSet))
	for targetID := range targetSet {
		queue = append(queue, targetID)
	}
	seen := make(map[string]struct{}, len(queue))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		for _, childID := range childrenByParent[current] {
			if _, selected := targetSet[childID]; !selected {
				if task, ok := byID[childID]; ok && closeCleanupDescendantBlocksTargetOnly(task) {
					return true
				}
			}
			queue = append(queue, childID)
		}
	}
	return false
}

func closeCleanupDescendantBlocksTargetOnly(task domain.Task) bool {
	if !task.IssueClosed() {
		return true
	}
	if task.HasTmuxSession || task.Session != nil {
		return true
	}
	if task.HasWorktree {
		return true
	}
	if task.Session != nil && strings.TrimSpace(task.Session.Worktree) != "" {
		return true
	}
	return false
}

func formatCloseCleanupGitStateLines(summaries []closeCleanupTaskSummary, targetCount int, bulk bool) []string {
	if len(summaries) == 0 {
		return []string{
			"Git state (current board projection):",
			"- no projected worktree/git state for close target",
		}
	}
	if !bulk && targetCount <= 1 && len(summaries) == 1 {
		return []string{
			"Git state (current board projection):",
			fmt.Sprintf("- Worktree: %s", presentAbsent(summaries[0].hasWorktree)),
			fmt.Sprintf("- Session: %s", presentAbsent(summaries[0].hasSession)),
			fmt.Sprintf("- Changes: %s", closeChangeState(summaries[0])),
			fmt.Sprintf("- Base diff (+/-): +%d/-%d", summaries[0].additions, summaries[0].deletions),
			fmt.Sprintf("- Ahead/Behind: ↑%d/↓%d", summaries[0].ahead, summaries[0].behind),
			fmt.Sprintf("- Conflicts: %s", closeConflictState(summaries[0])),
		}
	}

	lines := []string{"Git state for closing targets (current board projection):"}
	limit := min(len(summaries), 5)
	for _, summary := range summaries[:limit] {
		lines = append(lines, fmt.Sprintf("- %s: %s", summary.taskID, closeCleanupCompactState(summary)))
	}
	if len(summaries) > limit {
		lines = append(lines, fmt.Sprintf("- ... %d more", len(summaries)-limit))
	}
	if targetCount > len(summaries) {
		lines = append(lines, fmt.Sprintf("- %d target(s) without projected git state", targetCount-len(summaries)))
	}
	return lines
}

func closeCleanupCompactState(summary closeCleanupTaskSummary) string {
	parts := make([]string, 0, 5)
	if summary.hasWorktree {
		parts = append(parts, "worktree")
	} else {
		parts = append(parts, "no worktree")
	}
	if summary.hasSession {
		parts = append(parts, "session")
	}
	parts = append(parts, closeChangeState(summary))
	if summary.ahead > 0 || summary.behind > 0 {
		parts = append(parts, fmt.Sprintf("↑%d/↓%d", summary.ahead, summary.behind))
	}
	if summary.conflicted {
		parts = append(parts, closeConflictState(summary))
	}
	return strings.Join(parts, ", ")
}

func closeChangeState(summary closeCleanupTaskSummary) string {
	if summary.dirty {
		return fmt.Sprintf("dirty (+%d/-%d)", summary.additions, summary.deletions)
	}
	if summary.additions > 0 || summary.deletions > 0 {
		return fmt.Sprintf("base diff (+%d/-%d)", summary.additions, summary.deletions)
	}
	return "clean"
}

func closeConflictState(summary closeCleanupTaskSummary) string {
	if !summary.conflicted {
		return "none"
	}
	if len(summary.conflicts) == 0 {
		return "present"
	}
	limit := min(len(summary.conflicts), 3)
	value := strings.Join(summary.conflicts[:limit], ", ")
	if len(summary.conflicts) > limit {
		value = fmt.Sprintf("%s, ... %d more", value, len(summary.conflicts)-limit)
	}
	return value
}

func presentAbsent(present bool) string {
	if present {
		return "present"
	}
	return "not detected"
}

func pendingCloseCleanupCount(pending pendingCloseCleanupConfirmation) int {
	if len(pending.closeTaskIDs) > 0 {
		return len(pending.closeTaskIDs)
	}
	if len(pending.taskIDs) > 0 {
		return len(pending.taskIDs)
	}
	if strings.TrimSpace(pending.taskID) != "" {
		return 1
	}
	return 0
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

func (m *Model) applyOptimisticTaskLifecycle(taskID string, lifecycle domain.IssueWorkflow) {
	state, err := domain.NewIssueState(domain.IssueStateParts{Workflow: lifecycle})
	if err != nil {
		return
	}
	for i := range m.tasks {
		if m.tasks[i].ID.String() == taskID {
			m.tasks[i].Status = domain.StatusOpen
			m.tasks[i].State = state
			m.tasks[i].Facts = domain.DeriveIssueFacts(domain.IssueFactsInput{
				Status:         m.tasks[i].Status,
				Priority:       m.tasks[i].Priority,
				State:          m.tasks[i].State,
				Session:        m.tasks[i].Session,
				HasTmuxSession: m.tasks[i].HasTmuxSession,
			})
			break
		}
	}
	m.reconcileCursorAfterIssuesRefresh()
	m.syncTaskWorkspaceOverlay()
}

func (m *Model) rollbackOptimisticTask(task domain.Task) {
	for i := range m.tasks {
		if m.tasks[i].ID.String() == task.ID.String() {
			m.tasks[i] = task
			break
		}
	}
	m.reconcileCursorAfterIssuesRefresh()
	m.syncTaskWorkspaceOverlay()
}

func (m *Model) rollbackTaskStatus(taskID string, previousStatus domain.Status) {
	m.applyOptimisticTaskStatus(taskID, previousStatus)
	m.clearPendingTaskStatus(taskID)
}

func (m *Model) markTaskStatusPending(taskID string, previousStatus, targetStatus domain.Status, operationID string, state protocol.OperationState) {
	if m.pendingStatuses == nil {
		m.pendingStatuses = make(map[string]pendingTaskStatus)
	}
	m.clearTaskMutationFailure(taskID)
	m.pendingStatuses[taskIDKey(taskID)] = pendingTaskStatus{
		previousStatus: previousStatus,
		targetStatus:   targetStatus,
		operationID:    operationID,
		state:          state,
		action:         "task_move",
		updatedAt:      time.Now(),
	}
}

func (m *Model) beginTaskStatusMoveFeedback(taskID string, previousStatus, targetStatus domain.Status) {
	m.markTaskStatusPending(taskID, previousStatus, targetStatus, "", protocol.OperationStateQueued)
	m.applyOptimisticTaskStatus(taskID, targetStatus)
	m.syncTaskWorkspaceOverlay()
}

func (m *Model) markTaskOperationPending(taskID, action, operationID string, state protocol.OperationState) {
	if m.pendingStatuses == nil {
		m.pendingStatuses = make(map[string]pendingTaskStatus)
	}
	key := taskIDKey(taskID)
	if key == "" {
		return
	}
	m.clearTaskMutationFailure(taskID)
	current := m.pendingStatuses[key]
	if action == "session_start" && current.targetStatus == "" {
		if status, ok := m.taskStatusByID(taskID); ok && !(domain.Task{Status: status}).IssueClosed() {
			current.previousStatus = status
			current.targetStatus = domain.StatusInProgress
		}
	}
	current.operationID = operationID
	current.state = state
	current.action = action
	current.updatedAt = time.Now()
	m.pendingStatuses[key] = current
	m.applyPendingStatusOverlays()
}

func (m *Model) beginTaskMutationFeedback(taskID, action, label string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	m.markTaskOperationPending(taskID, action, "", protocol.OperationStateQueued)
	m.syncTaskWorkspaceOverlay()
	m.addToast(Toast{
		Level:   ToastInfo,
		Message: fmt.Sprintf("%s queued for %s", strings.TrimSpace(label), taskID),
		Expires: time.Now().Add(3 * time.Second),
	})
}

func (m *Model) markTaskGitOperationPending(taskID, kind, operationID string, state protocol.OperationState) {
	key := taskIDKey(taskID)
	if key == "" {
		return
	}
	m.clearTaskMutationFailure(taskID)
	if m.pendingOpsByTask == nil {
		m.pendingOpsByTask = make(map[string]pendingOperationProgress)
	}
	current := m.pendingOpsByTask[key]
	if strings.TrimSpace(operationID) == "" {
		operationID = current.operationID
	}
	if strings.TrimSpace(kind) == "" {
		kind = current.kind
	}
	if strings.TrimSpace(kind) == "" {
		kind = "git.merge"
	}
	percent := current.percent
	if state == protocol.OperationStateRunning && percent == 0 {
		percent = 50
	}
	m.pendingOpsByTask[key] = pendingOperationProgress{
		operationID: strings.TrimSpace(operationID),
		kind:        strings.TrimSpace(kind),
		state:       state,
		percent:     percent,
		message:     current.message,
		updatedAt:   time.Now(),
	}
}

func (m *Model) markTaskGitOperationPreparing(taskID, message string) {
	key := taskIDKey(taskID)
	if key == "" {
		return
	}
	m.clearTaskMutationFailure(taskID)
	if m.pendingOpsByTask == nil {
		m.pendingOpsByTask = make(map[string]pendingOperationProgress)
	}
	m.pendingOpsByTask[key] = pendingOperationProgress{
		kind:      "git.merge",
		state:     protocol.OperationState("preparing"),
		message:   strings.TrimSpace(message),
		updatedAt: time.Now(),
	}
}

func (m *Model) markTaskMutationFailed(taskID, action, message string) {
	m.markTaskStatusMutationFailed(taskID, action, "", "", message)
}

func (m *Model) markTaskStatusMutationFailed(taskID, action string, previousStatus, targetStatus domain.Status, message string) {
	m.markTaskMutationFailure(mutationFailureDetails{
		TaskID:         strings.TrimSpace(taskID),
		Action:         strings.TrimSpace(action),
		PreviousStatus: previousStatus,
		CurrentStatus:  previousStatus,
		TargetStatus:   targetStatus,
		Message:        strings.TrimSpace(message),
	})
}

func (m *Model) markTaskMutationFailure(details mutationFailureDetails) {
	taskID := strings.TrimSpace(details.TaskID)
	key := taskIDKey(taskID)
	if key == "" {
		return
	}
	if m.pendingFailures == nil {
		m.pendingFailures = make(map[string]taskMutationFailure)
	}
	delete(m.pendingOpsByTask, key)
	failure := taskMutationFailure{
		action:         strings.TrimSpace(details.Action),
		message:        strings.TrimSpace(details.Message),
		reason:         strings.TrimSpace(details.Reason),
		recovery:       strings.TrimSpace(details.Recovery),
		previousStatus: details.PreviousStatus,
		currentStatus:  details.CurrentStatus,
		targetStatus:   details.TargetStatus,
		updatedAt:      time.Now(),
	}
	m.feedback.setLocalFailure(key, failure)
	m.refreshFeedbackProjectionOutputs(time.Now())
}

func (m *Model) markMergeOperationPending(sourceID, targetID, operationID string, state protocol.OperationState) {
	m.markTaskGitOperationPending(sourceID, "git.merge", operationID, state)
	if targetKey := taskIDKey(targetID); targetKey != "" && targetKey != "base" && targetKey != taskIDKey(sourceID) {
		m.markTaskGitOperationPending(targetID, "git.merge", operationID, state)
	}
}

func (m *Model) markMergeOperationPreparing(sourceID, targetID, message string) {
	m.markTaskGitOperationPreparing(sourceID, message)
	if targetKey := taskIDKey(targetID); targetKey != "" && targetKey != "base" && targetKey != taskIDKey(sourceID) {
		m.markTaskGitOperationPreparing(targetID, message)
	}
	m.syncTaskWorkspaceOverlay()
}

func (m *Model) clearLocalMergeOperationPending(sourceID, targetID string) {
	m.clearLocalTaskGitOperationPending(sourceID)
	if targetKey := taskIDKey(targetID); targetKey != "" && targetKey != "base" && targetKey != taskIDKey(sourceID) {
		m.clearLocalTaskGitOperationPending(targetID)
	}
	m.syncTaskWorkspaceOverlay()
}

func (m *Model) clearLocalTaskGitOperationPending(taskID string) {
	key := taskIDKey(taskID)
	if key == "" || len(m.pendingOpsByTask) == 0 {
		return
	}
	current, ok := m.pendingOpsByTask[key]
	if !ok {
		return
	}
	if strings.TrimSpace(current.operationID) != "" {
		return
	}
	if strings.TrimSpace(current.kind) != "git.merge" {
		return
	}
	delete(m.pendingOpsByTask, key)
}

func (m Model) taskStatusByID(taskID string) (domain.Status, bool) {
	key := taskIDKey(taskID)
	if key == "" {
		return "", false
	}
	for _, task := range m.tasks {
		if taskIDKey(task.ID.String()) == key {
			return task.Status, true
		}
	}
	return "", false
}

func (m *Model) beginMutationFeedback(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	m.addToast(Toast{
		Level:   ToastInfo,
		Message: message,
		Expires: time.Now().Add(3 * time.Second),
	})
}

func (m *Model) clearPendingTaskStatus(taskID string) {
	if len(m.pendingStatuses) == 0 {
		return
	}
	delete(m.pendingStatuses, taskIDKey(taskID))
}

func (m *Model) clearTaskMutationFailure(taskID string) {
	m.feedback.clearLocalFailure(taskID)
	m.refreshFeedbackProjectionOutputs(time.Now())
}

func (m *Model) clearPendingTaskStatusForOperation(taskID, operationID string) {
	key := taskIDKey(taskID)
	if key == "" || len(m.pendingStatuses) == 0 {
		return
	}
	pending, ok := m.pendingStatuses[key]
	if !ok {
		return
	}
	if strings.TrimSpace(operationID) != "" && strings.TrimSpace(pending.operationID) != strings.TrimSpace(operationID) {
		return
	}
	delete(m.pendingStatuses, key)
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
		progress.FailureAction = op.action
		progress.FailureReason = op.reason
		progress.FailureRecovery = op.recovery
		progress.CurrentStatus = op.currentStatus
		if progress.PreviousStatus == "" {
			progress.PreviousStatus = op.currentStatus
		}
		if op.targetStatus != "" {
			progress.TargetStatus = op.targetStatus
		}
		if strings.TrimSpace(op.errorMessage) != "" {
			progress.ProgressMessage = op.errorMessage
		}
	}
	if failure, ok := m.pendingFailures[key]; ok {
		if progress.OperationID == "" {
			progress.OperationID = failure.operationID
		}
		progress.State = string(protocol.OperationStateFailed)
		progress.ProgressMessage = strings.TrimSpace(failure.message)
		progress.PreviousStatus = failure.previousStatus
		progress.CurrentStatus = failure.currentStatus
		progress.TargetStatus = failure.targetStatus
		progress.FailureAction = failure.action
		progress.FailureReason = failure.reason
		progress.FailureRecovery = failure.recovery
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

func (m Model) activePendingOperationForTask(taskID string) (string, bool) {
	key := taskIDKey(taskID)
	if key == "" {
		return "", false
	}
	if op, ok := m.pendingOpsByTask[key]; ok && pendingOperationCanCancel(op.operationID, op.state) {
		return strings.TrimSpace(op.operationID), true
	}
	if pending, ok := m.pendingStatuses[key]; ok && pendingOperationCanCancel(pending.operationID, pending.state) {
		return strings.TrimSpace(pending.operationID), true
	}
	return "", false
}

func pendingOperationCanCancel(operationID string, state protocol.OperationState) bool {
	if strings.TrimSpace(operationID) == "" {
		return false
	}
	switch state {
	case protocol.OperationStateQueued, protocol.OperationStateRunning:
		return true
	default:
		return false
	}
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

func (m *Model) applySingleTaskWorkspaceRefresh(taskID string, refreshed domain.Task) (domain.Task, bool) {
	m.reconcilePendingStatuses()
	m.reconcilePendingMutationFailures()
	m.reconcilePendingTaskDetailsFromFullTasks([]domain.Task{refreshed})
	return m.applyTaskRefresh(taskID, refreshed, true)
}

func (m *Model) applyTaskRefreshes(refreshed []domain.Task) {
	m.reconcilePendingStatuses()
	m.reconcilePendingMutationFailures()
	updated := false
	for _, task := range refreshed {
		if _, ok := m.applyTaskRefresh(task.ID.String(), task, false); ok {
			updated = true
		}
	}
	if updated {
		m.syncProjectionIndexesFromTasks()
		m.refreshFeedbackProjectionOutputs(time.Now())
		m.reconcileCursorAfterIssuesRefresh()
	}
}

func (m *Model) applyTaskRefresh(taskID string, refreshed domain.Task, syncAfter bool) (domain.Task, bool) {
	key := taskIDKey(taskID)
	if key == "" {
		return domain.Task{}, false
	}
	for i := range m.tasks {
		if taskIDKey(m.tasks[i].ID.String()) != key {
			continue
		}
		refreshed = m.applyPendingStatusOverlayToTask(refreshed)
		m.tasks[i] = refreshed
		m.tasks[i].Session = cloneSession(m.tasks[i].Session)
		m.reconcilePendingMutationFailures()
		m.refreshFeedbackProjectionOutputs(time.Now())
		if syncAfter {
			m.syncProjectionIndexesFromTasks()
			m.reconcileCursorAfterIssuesRefresh()
		}
		return m.tasks[i], true
	}
	return domain.Task{}, false
}

func (m *Model) applyPendingStatusOverlayToTask(task domain.Task) domain.Task {
	key := taskIDKey(task.ID.String())
	if key == "" || len(m.pendingStatuses) == 0 {
		return task
	}
	pending, ok := m.pendingStatuses[key]
	if !ok || pending.targetStatus == "" {
		return task
	}
	if task.Status == pending.targetStatus {
		if pending.action != "session_start" {
			delete(m.pendingStatuses, key)
		}
		return task
	}
	task.Status = pending.targetStatus
	return task
}

func (m *Model) applyPendingStatusOverlays() {
	if len(m.pendingStatuses) == 0 {
		return
	}
	for i := range m.tasks {
		m.tasks[i] = m.applyPendingStatusOverlayToTask(m.tasks[i])
	}
}

func (m *Model) markPendingTaskDetails(taskID string, details pendingTaskDetails) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if m.pendingDetails == nil {
		m.pendingDetails = make(map[string]pendingTaskDetails)
	}
	if details.updatedAt.IsZero() {
		details.updatedAt = time.Now()
	}
	details.implementations = append([]string(nil), details.implementations...)
	details.estimate = cloneIntPtr(details.estimate)
	m.pendingDetails[taskIDKey(taskID)] = details
}

func (m *Model) applyPendingTaskDetailOverlays() {
	if len(m.pendingDetails) == 0 {
		return
	}
	for i := range m.tasks {
		key := taskIDKey(m.tasks[i].ID.String())
		pending, ok := m.pendingDetails[key]
		if !ok {
			continue
		}
		m.tasks[i].Title = pending.title
		m.tasks[i].Description = pending.description
		m.tasks[i].Design = pending.design
		m.tasks[i].Notes = pending.notes
		m.tasks[i].Acceptance = pending.acceptance
		m.tasks[i].Estimate = cloneIntPtr(pending.estimate)
		m.tasks[i].Type = pending.taskType
		m.tasks[i].Priority = pending.priority
		m.tasks[i].Implementations = append([]string(nil), pending.implementations...)
	}
}

func (m *Model) reconcilePendingTaskDetails() {
	if len(m.pendingDetails) == 0 {
		return
	}
	taskByID := make(map[string]domain.Task, len(m.tasks))
	for _, task := range m.tasks {
		taskByID[task.ID.String()] = task
	}

	const stalePendingTTL = 2 * time.Minute
	now := time.Now()
	for key, pending := range m.pendingDetails {
		_, ok := taskByID[string(key)]
		if !ok {
			delete(m.pendingDetails, key)
			continue
		}
		if !pending.updatedAt.IsZero() && now.Sub(pending.updatedAt) > stalePendingTTL {
			delete(m.pendingDetails, key)
		}
	}
}

func (m *Model) reconcilePendingTaskDetailsFromFullTasks(tasks []domain.Task) {
	if len(m.pendingDetails) == 0 || len(tasks) == 0 {
		return
	}
	for _, task := range tasks {
		key := taskIDKey(task.ID.String())
		pending, ok := m.pendingDetails[key]
		if !ok {
			continue
		}
		if taskDetailsMatchPending(task, pending) {
			delete(m.pendingDetails, key)
		}
	}
}

func taskDetailsMatchPending(task domain.Task, pending pendingTaskDetails) bool {
	return task.Title == pending.title &&
		task.Description == pending.description &&
		task.Design == pending.design &&
		task.Notes == pending.notes &&
		task.Acceptance == pending.acceptance &&
		intPtrEqual(task.Estimate, pending.estimate) &&
		task.Type == pending.taskType &&
		task.Priority == pending.priority &&
		stringSlicesEqual(task.Implementations, pending.implementations)
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

func (m *Model) reconcilePendingOperations() {
	if len(m.pendingOpsByTask) == 0 {
		return
	}

	taskByID := make(map[string]domain.Task, len(m.tasks))
	for _, task := range m.tasks {
		taskByID[taskIDKey(task.ID.String())] = task
	}

	const stalePendingTTL = 2 * time.Minute
	now := time.Now()

	for key, pending := range m.pendingOpsByTask {
		task, ok := taskByID[key]
		if !ok {
			delete(m.pendingOpsByTask, key)
			continue
		}

		if !pending.updatedAt.IsZero() {
			ttl := stalePendingTTL
			if pending.state == protocol.OperationStateFailed || pending.state == protocol.OperationStateCancelled {
				ttl = visibleTerminalOperationTTL
			}
			if now.Sub(pending.updatedAt) > ttl {
				delete(m.pendingOpsByTask, key)
				continue
			}
		}

		switch strings.TrimSpace(pending.kind) {
		case "session.start":
			if task.Session != nil || task.HasTmuxSession {
				delete(m.pendingOpsByTask, key)
			}
		case "session.stop":
			if task.Session == nil && !task.HasTmuxSession {
				delete(m.pendingOpsByTask, key)
			}
		}
	}
}

func (m *Model) reconcilePendingMutationFailures() {
	if len(m.feedback.localFailures) == 0 {
		return
	}

	taskByID := make(map[string]domain.Task, len(m.tasks))
	for _, task := range m.tasks {
		taskByID[taskIDKey(task.ID.String())] = task
	}

	const staleFailureTTL = visibleTerminalOperationTTL
	now := time.Now()

	for key, failure := range m.feedback.localFailures {
		task, ok := taskByID[key]
		if !ok {
			delete(m.feedback.localFailures, key)
			continue
		}
		if !failure.updatedAt.IsZero() && now.Sub(failure.updatedAt) > staleFailureTTL {
			delete(m.feedback.localFailures, key)
			continue
		}
		if failure.targetStatus != "" && task.Status == failure.targetStatus {
			delete(m.feedback.localFailures, key)
			continue
		}
		if failure.previousStatus != "" && task.Status != failure.previousStatus {
			delete(m.feedback.localFailures, key)
		}
	}
	m.refreshFeedbackProjectionOutputs(now)
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
	updateDetails     *pendingTaskDetails
	attachmentWarning string
}

func (m Model) attachStagedAttachments(ctx context.Context, issueID string, paths []string) string {
	if strings.TrimSpace(issueID) == "" || len(paths) == 0 || m.attachmentService == nil {
		return ""
	}

	failed := make([]string, 0)
	noteFailures := make([]string, 0)
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}

		attached, err := m.attachmentService.Attach(ctx, issueID, path)
		if err != nil {
			failed = append(failed, filepath.Base(path))
			if m.logger != nil {
				m.logger.Warn("staged attachment attach failed", "issue_id", issueID, "source_path", path, "error", err)
			}
			continue
		}

		if attached != nil && m.daemonClient != nil {
			if line := formatAttachmentNoteLine(attached); strings.TrimSpace(line) != "" {
				if err := m.daemonClient.AppendTaskNotes(ctx, issueID, line); err != nil {
					noteFailures = append(noteFailures, filepath.Base(path)+": "+compactErrorMessage(err))
					if m.logger != nil {
						m.logger.Warn("staged attachment note append failed", "issue_id", issueID, "source_path", path, "attachment_id", attached.ID, "error", err)
					}
				}
			}
		}

		_ = os.Remove(path)
	}

	if len(failed) == 0 && len(noteFailures) == 0 {
		return ""
	}
	warnings := make([]string, 0, 2)
	if len(failed) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d attachment(s) failed: %s", len(failed), strings.Join(failed, ", ")))
	}
	if len(noteFailures) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d attachment note update(s) failed: %s", len(noteFailures), strings.Join(noteFailures, ", ")))
	}
	return "Task created, but " + strings.Join(warnings, "; ")
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
				break
			}
		}
		detailSnapshot, err := m.daemonClient.GetTaskSnapshotWithMode(ctx, msg.issueID, daemonclient.ReadWaitModeExplicit)
		if err != nil {
			return prCreatedResultMsg{err: fmt.Errorf("load issue detail for PR generation: %w", err)}
		}
		if err := detailSnapshot.RequireFullDetails("PR generation issue detail read"); err != nil {
			return prCreatedResultMsg{err: fmt.Errorf("load issue detail for PR generation: %w", err)}
		}
		for i := range detailSnapshot.Tasks {
			if detailSnapshot.Tasks[i].ID.String() == msg.issueID {
				if title := strings.TrimSpace(detailSnapshot.Tasks[i].Title); title != "" {
					issueTitle = title
				}
				issueDescription = strings.TrimSpace(detailSnapshot.Tasks[i].Description)
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
		project := m.asyncRecoveryProjectContext()

		resolvedWorktree := strings.TrimSpace(worktreeHint)
		var err error
		if resolvedWorktree == "" {
			resolvedWorktree, err = m.resolveIssueWorktreePathFromDaemon(ctx, issueID)
		}
		if resolvedWorktree == "" {
			return fetchAndMergeResultMsg{
				issueID:     issueID,
				project:     project,
				attachAfter: attachAfter,
				err:         fmt.Errorf("no active session/worktree - start session first"),
			}
		}
		if err != nil {
			return fetchAndMergeResultMsg{
				issueID:     issueID,
				project:     project,
				attachAfter: attachAfter,
				err:         fmt.Errorf("no active session/worktree - start session first"),
			}
		}

		return m.fetchAndMergeCmd(resolvedWorktree, m.resolveBaseBranch(), issueID, attachAfter)()
	}
}

func (m Model) pullRootBaseBranchCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		worktree := strings.TrimSpace(m.repoDir)
		if worktree == "" {
			return pullBaseResultMsg{err: fmt.Errorf("project root unavailable")}
		}
		baseBranch := strings.TrimSpace(m.resolveBaseBranch())
		if baseBranch == "" {
			return pullBaseResultMsg{worktree: worktree, err: fmt.Errorf("base branch unavailable")}
		}
		if m.daemonClient == nil {
			return pullBaseResultMsg{worktree: worktree, baseBranch: baseBranch, err: fmt.Errorf("daemon client unavailable")}
		}

		remote := "origin"
		resp, err := m.daemonClient.GitPullBase(ctx, worktree, remote, baseBranch)
		if err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return pullBaseResultMsg{
					worktree:    worktree,
					remote:      remote,
					baseBranch:  baseBranch,
					operationID: pending.OperationID,
					state:       pending.State,
				}
			}
			return pullBaseResultMsg{worktree: worktree, remote: remote, baseBranch: baseBranch, err: err}
		}
		if strings.TrimSpace(resp.Remote) != "" {
			remote = strings.TrimSpace(resp.Remote)
		}
		if strings.TrimSpace(resp.Branch) != "" {
			baseBranch = strings.TrimSpace(resp.Branch)
		}
		if strings.TrimSpace(resp.Worktree) != "" {
			worktree = strings.TrimSpace(resp.Worktree)
		}
		return pullBaseResultMsg{worktree: worktree, remote: remote, baseBranch: baseBranch}
	}
}

func (m Model) gitPaneStatusCmd(refresh bool) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return gitPaneStatusMsg{err: fmt.Errorf("daemon client unavailable")}
		}
		worktree := strings.TrimSpace(m.repoDir)
		if worktree == "" {
			return gitPaneStatusMsg{err: fmt.Errorf("project root unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()
		var status daemonclient.GitStatus
		var err error
		if refresh {
			status, err = m.daemonClient.GitStatusRefresh(ctx, worktree)
		} else {
			status, err = m.daemonClient.GitStatus(ctx, worktree)
		}
		return gitPaneStatusMsg{status: status, err: err}
	}
}

func (m Model) pushRootBaseBranchCmd() tea.Cmd {
	return func() tea.Msg {
		branch := strings.TrimSpace(m.resolveBaseBranch())
		if branch == "" {
			return gitPanePushResultMsg{err: fmt.Errorf("base branch unavailable")}
		}
		worktree := strings.TrimSpace(m.repoDir)
		if worktree == "" {
			return gitPanePushResultMsg{branch: branch, err: fmt.Errorf("project root unavailable")}
		}
		if m.daemonClient == nil {
			return gitPanePushResultMsg{branch: branch, err: fmt.Errorf("daemon client unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()
		_, err := m.daemonClient.GitPush(ctx, worktree, "origin", branch)
		if pending, ok := pendingOperationDetails(err); ok {
			return gitPanePushResultMsg{branch: branch, operationID: pending.OperationID, state: pending.State}
		}
		return gitPanePushResultMsg{branch: branch, err: err}
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

func (m Model) cancelTaskOperationCmd(taskID, operationID string) tea.Cmd {
	taskID = strings.TrimSpace(taskID)
	operationID = strings.TrimSpace(operationID)
	return func() tea.Msg {
		if operationID == "" {
			return operationCancelledMsg{taskID: taskID, err: fmt.Errorf("operation id is required")}
		}
		if m.daemonClient == nil {
			return operationCancelledMsg{taskID: taskID, err: fmt.Errorf("daemon client unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		record, err := m.daemonClient.CancelOperation(ctx, operationID, "cancelled from TUI")
		return operationCancelledMsg{taskID: taskID, record: record, err: err}
	}
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

func (m Model) checkMergePreflight(ctx context.Context, sourceID, targetID, sourceWorktree, targetWorktree, targetRef, sourceBranch string, refreshStatus bool, ignoreSourceDirty bool) *mergePreflightFailureMsg {
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
	conflictFiles := make([]string, 0, 8)

	statusForWorktree := func(worktree string) (daemonclient.GitStatus, error) {
		if refreshStatus {
			return m.daemonClient.GitStatusRefresh(ctx, worktree)
		}
		return m.daemonClient.GitStatus(ctx, worktree)
	}

	sourceStatus, sourceErr := statusForWorktree(sourceWorktree)
	if sourceErr != nil {
		reasons = append(reasons, fmt.Sprintf("Could not read source status (%s): %v", sourceID, sourceErr))
	} else if !ignoreSourceDirty && hasMergeBlockingStatusChanges(sourceStatus) {
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
		resp, err := m.daemonClient.GitMergePreflightWithOptions(ctx, daemonclient.GitMergePreflightRequest{
			SourceID:          sourceID,
			SourceWorktree:    sourceWorktree,
			TargetID:          targetID,
			TargetWorktree:    targetWorktree,
			TargetRef:         targetRef,
			SourceBranch:      sourceBranch,
			IgnoreSourceDirty: ignoreSourceDirty,
		})
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
			if len(resp.ConflictFiles) > 0 {
				conflictFiles = append(conflictFiles[:0], resp.ConflictFiles...)
			}
		}
	}

	if len(reasons) == 0 {
		return nil
	}
	return &mergePreflightFailureMsg{
		context:        m.projectActionContext(),
		sourceID:       sourceID,
		sourceWorktree: sourceWorktree,
		targetID:       targetID,
		targetWorktree: targetWorktree,
		reasons:        reasons,
		sourceFiles:    sourceFiles,
		targetFiles:    targetFiles,
		conflictFiles:  conflictFiles,
		targetRef:      targetRef,
		sourceBranch:   sourceBranch,
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
			strings.TrimSpace(selection.TargetRef),
			strings.TrimSpace(selection.SourceBranch),
			true,
			selection.IgnoreSourceDirty,
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

type devServerResultMsg struct {
	issueID string
	server  overlay.DevServerInfo
	err     error
}

func (m Model) toggleDevServer(serverID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if m.daemonClient == nil {
			return devServerResultMsg{issueID: serverID, err: fmt.Errorf("daemon client unavailable")}
		}
		srv, err := m.daemonClient.ToggleDevServer(ctx, serverID)
		if err != nil {
			return devServerResultMsg{issueID: serverID, err: err}
		}
		return devServerResultMsg{issueID: serverID, server: overlay.DevServerInfo{
			ID:     srv.ID,
			Name:   srv.Name,
			Port:   srv.Port,
			Status: srv.Status,
			Uptime: srv.Uptime,
		}}
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
			return devServerResultMsg{issueID: serverID, err: fmt.Errorf("daemon client unavailable")}
		}
		srv, err := m.daemonClient.RestartDevServer(ctx, serverID)
		if err != nil {
			return devServerResultMsg{issueID: serverID, err: err}
		}
		return devServerResultMsg{issueID: serverID, server: overlay.DevServerInfo{
			ID:     srv.ID,
			Name:   srv.Name,
			Port:   srv.Port,
			Status: srv.Status,
			Uptime: srv.Uptime,
		}}
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
		if task.Session == nil && !task.HasTmuxSession {
			continue
		}

		state := domain.SessionIdle
		activity := ""
		activitySource := ""
		var startedAt *time.Time
		worktree := ""
		if task.Session != nil {
			state = task.Session.State
			activity = task.Session.Activity
			activitySource = task.Session.ActivitySource
			startedAt = task.Session.StartedAt
			worktree = task.Session.Worktree
		}
		totalCount, activeCount, pausedCount := sessionAggregateCounts(task.Session)

		sessions = append(sessions, overlay.SessionInfo{
			IssueID:               task.ID.String(),
			TaskTitle:             task.Title,
			IssueStatus:           task.Status,
			State:                 state,
			Activity:              activity,
			ActivitySource:        activitySource,
			TotalCount:            totalCount,
			ActiveCount:           activeCount,
			PausedCount:           pausedCount,
			StartedAt:             startedAt,
			Worktree:              worktree,
			HasTmuxSession:        task.Session != nil || task.HasTmuxSession,
			HasWorktree:           task.HasWorktree,
			GitAheadCount:         task.GitAheadCount,
			GitBehindCount:        task.GitBehindCount,
			HasUncommittedChanges: task.HasUncommittedChanges,
			HasConflicts:          task.HasConflicts,
			GitAdditions:          task.GitAdditions,
			GitDeletions:          task.GitDeletions,
			RecentOutput:          "", // TODO: Capture recent output from tmux
		})
	}

	if len(sessions) == 0 {
		for _, session := range m.sessions {
			if session == nil {
				continue
			}
			sessions = append(sessions, overlay.SessionInfo{
				IssueID:        session.IssueID.String(),
				TaskTitle:      session.IssueID.String(),
				IssueStatus:    domain.StatusInProgress,
				State:          session.State,
				Activity:       session.Activity,
				ActivitySource: session.ActivitySource,
				TotalCount:     session.TotalCount,
				ActiveCount:    session.ActiveCount,
				PausedCount:    session.PausedCount,
				StartedAt:      session.StartedAt,
				Worktree:       session.Worktree,
				HasTmuxSession: true,
				HasWorktree:    strings.TrimSpace(session.Worktree) != "",
			})
		}
	}

	// Create overlay with callbacks
	orchOverlay := overlay.NewOrchestrationOverlay(
		sessions,
		// onAttach
		func(issueID string) tea.Cmd {
			return m.attachSessionCmd(issueID)
		},
		// onKill
		func(issueID string) tea.Cmd {
			return m.stopSessionCmd(issueID)
		},
		// onRefresh
		func() tea.Cmd {
			return m.loadIssuesCmd()
		},
		// onOpenWorkspace
		func(issueID string) tea.Cmd {
			return func() tea.Msg {
				return overlay.SelectionMsg{Key: "task_workspace_open_task", Value: issueID}
			}
		},
		// onDrillDown
		func(issueID string) tea.Cmd {
			return func() tea.Msg {
				return overlay.SelectionMsg{Key: "task_workspace_drill_down", Value: issueID}
			}
		},
	)

	return m.openOverlay(orchOverlay)
}

func sessionAggregateCounts(session *domain.Session) (total, active, paused int) {
	if session == nil {
		return 0, 0, 0
	}
	return session.TotalCount, session.ActiveCount, session.PausedCount
}

// performCleanup executes cleanup operations for selected categories
func (m Model) performCleanup(ctx context.Context, categoryIDs []string) (overlay.CleanupResult, error) {
	if m.daemonClient == nil {
		return overlay.CleanupResult{}, fmt.Errorf("daemon client unavailable")
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, worktreeCleanupMutationTimeout)
	defer cancel()
	result, err := m.daemonClient.CleanupProject(cleanupCtx, categoryIDs)
	if err != nil {
		return overlay.CleanupResult{}, err
	}
	return overlay.CleanupResult{
		Deleted:          result.Deleted,
		Archived:         result.Archived,
		WorktreesRemoved: result.WorktreesRemoved,
		SessionsCleaned:  result.SessionsCleaned,
	}, nil
}
