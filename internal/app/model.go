// Package app contains the main application model and TEA implementation.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	autoclient "github.com/riordanpawley/azedarach/internal/client"
	"github.com/riordanpawley/azedarach/internal/client/appdeps"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/daemonprocess"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/editor"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/linearsync"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
	"github.com/riordanpawley/azedarach/internal/services/navigation"
	"github.com/riordanpawley/azedarach/internal/services/network"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/compact"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/eventticker"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/statusbar"
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
	diffPreviewMaxCharacters = 200
	eventTickerCapacity      = 64
	eventLogCapacity         = 256
	eventSummaryMaxRunes     = 140
	runtimeSignalCacheTTL    = 45 * time.Second
)

var ansiEscapeLinePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
var diffStatInsertionsPattern = regexp.MustCompile(`(\d+)\s+insertion`)
var diffStatDeletionsPattern = regexp.MustCompile(`(\d+)\s+deletion`)
var executablePath = os.Executable
var lookupPath = exec.LookPath
var processArgs = func() []string { return os.Args }
var workingDir = os.Getwd
var runGitCommandFunc = runGitCommand

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
	tasks            []domain.Task
	sessions         map[string]*domain.Session
	suppressedTasks  map[string]struct{}
	pendingStatuses  map[string]pendingTaskStatus
	operationTaskID  map[string]string
	pendingOpsByTask map[string]pendingOperationProgress
	pendingCleanup   *pendingWorktreeCleanupConfirmation

	// Navigation (using NavigationService)
	nav *navigation.Service

	// Editor state (mode, filter, sort, selections)
	editor *editor.Service

	// UI state
	overlayStack                   *overlay.Stack
	createTaskOverlay              *overlay.CreateTaskOverlay
	viewMode                       ViewMode
	viewportStarts                 [board.DefaultColumnCount]int
	columnViewportStart            int
	drillDownParentID              string
	drillDownParentName            string
	drillDownTrail                 []drillDownContext
	pendingCreatedTaskID           string
	runtimeSignalsByTask           map[string]board.RuntimeSignals
	runtimeSignalRefreshedAtByTask map[string]time.Time
	runtimeSignalWorktreeByTask    map[string]string
	runtimeSignalsBusy             bool
	lastRuntimeRefresh             time.Time

	// Project
	currentProject string
	projects       []domain.Project
	repoDir        string
	logFilePath    string

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
	loading         bool
	boardRefreshing bool
	issueRefreshSeq uint64
	projectSwitchSeq uint64
	projectSwitchInFlight bool
	spinner         spinner.Model
	lastRefresh     time.Time
	hasRefreshLoop  bool

	// Shared daemon client for task-domain operations
	daemonClient     *daemonclient.Client
	daemonSocketPath string
	daemonEvents     <-chan protocol.EventEnvelope
	daemonRevision   uint64

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

	// Use placeholder data in Phase 1
	usePlaceholder bool
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
	if normalizedRepoDir, normalizeErr := config.ResolveProjectRoot(repoDir); normalizeErr == nil {
		repoDir = normalizedRepoDir
	}
	logFilePath := resolveTUILogFilePath(cfg)
	logger := newTUILogger(logFilePath)
	if err != nil {
		logger.Error("failed to get current directory", "error", err)
	}
	daemonClient := daemonclient.New(transport.NewClient(daemonSocketPath))
	deps := appdeps.Build(cfg, repoDir, logger)

	m := Model{
		tasks:                          []domain.Task{},
		sessions:                       make(map[string]*domain.Session),
		pendingStatuses:                make(map[string]pendingTaskStatus),
		operationTaskID:                make(map[string]string),
		pendingOpsByTask:               make(map[string]pendingOperationProgress),
		nav:                            navigation.NewService(),
		editor:                         editor.NewService(),
		overlayStack:                   overlay.NewStack(),
		viewMode:                       ViewModeBoard, // Start with board view
		runtimeSignalsByTask:           make(map[string]board.RuntimeSignals),
		runtimeSignalRefreshedAtByTask: make(map[string]time.Time),
		runtimeSignalWorktreeByTask:    make(map[string]string),
		toasts:                         []Toast{},
		eventTicker:                    eventticker.NewRing(eventTickerCapacity),
		runtimeEvents:                  []protocol.EventEnvelope{},
		styles:                         styles.New(),
		config:                         cfg,
		loading:                        true, // Start with loading state
		spinner:                        s,
		daemonClient:                   daemonClient,
		daemonSocketPath:               daemonSocketPath,
		sessionMonitor:                 deps.SessionMonitor,
		gitClient:                      deps.GitDiffClient,
		gitSyncService:                 deps.GitSyncService,
		projectRegistry:                deps.ProjectRegistry,
		isOnline:                       deps.IsOnline,
		attachmentService:              deps.AttachmentService,
		diagnosticsService:             deps.DiagnosticsService,
		logger:                         logger,
		usePlaceholder:                 false, // Use real data from local issue store
		tmuxAvailable:                  deps.TmuxAvailable,
		tmuxClient:                     deps.TmuxClient,
		repoDir:                        repoDir,
		logFilePath:                    logFilePath,
		currentProject:                 resolveInitialProjectName(deps.ProjectRegistry, repoDir),
	}
	m.daemonClient.WithProjectID(m.daemonProjectID())
	return m
}

// Init returns the initial command for the application
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.attachDaemonCmd(),
		m.gitSyncService.FetchAndCheck(),
	)
}

// Update handles incoming messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureCursorVisible(m.buildColumns())
		if !m.overlayStack.IsEmpty() {
			return m, m.overlayStack.Update(msg)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		// If overlay is open, route to overlay stack
		if !m.overlayStack.IsEmpty() {
			return m.handleOverlayKey(msg)
		}
		return m.handleKey(msg)

	// Overlay messages
	case overlay.CloseOverlayMsg:
		isSearchOverlay := false
		if current := m.overlayStack.Current(); current != nil {
			_, isSearchOverlay = current.(*overlay.SearchOverlay)
		}
		m.overlayStack.Pop()
		if isSearchOverlay {
			m.editor.EnterNormal()
		}
		return m, nil

	case overlay.SelectionMsg:
		if msg.Key == "git_pull" {
			m.overlayStack.Pop()
			return m, m.gitSyncService.Pull()
		}
		if msg.Key == "merge_attach" {
			m.overlayStack.Pop()
			issueID := msg.Value.(string)
			session := m.sessionForIssue(issueID)
			if session == nil {
				return m, nil
			}
			return m, m.fetchAndMergeCmd(session.Worktree, m.config.Git.BaseBranch, issueID, true)
		}
		if msg.Key == "skip_attach" {
			m.overlayStack.Pop()
			issueID := msg.Value.(string)
			return m, m.attachSessionCmd(issueID)
		}
		return m.handleSelection(msg)

	case overlay.BulkActionMsg:
		m.overlayStack.Pop()
		return m.handleBulkAction(msg)

	case overlay.SearchMsg:
		m.editor.SetSearchQuery(msg.Query)
		if current := m.overlayStack.Current(); current != nil {
			if searchOverlay, ok := current.(*overlay.SearchOverlay); ok {
				searchOverlay.SetMatchCount(len(m.editor.ApplyFilter(m.tasks)))
			}
		}
		return m, nil

	case issuesLoadedMsg:
		if msg.refreshSeq < m.issueRefreshSeq {
			return m, nil
		}
		if msg.projectID != "" && msg.projectID != m.daemonProjectID() {
			return m, nil
		}
		if msg.daemonClient != nil {
			m.daemonClient = msg.daemonClient
			if strings.TrimSpace(msg.daemonSocket) != "" {
				m.daemonSocketPath = msg.daemonSocket
			}
		}
		wasLoading := m.loading
		if msg.stale {
			m.loading = false
			m.boardRefreshing = false
			if wasLoading && msg.freshnessHint != "" {
				m.addToast(Toast{
					Level:   ToastWarning,
					Message: msg.freshnessHint,
					Expires: time.Now().Add(8 * time.Second),
				})
			}
			var cmds []tea.Cmd
			if !m.hasRefreshLoop {
				m.hasRefreshLoop = true
				cmds = append(cmds, tickEvery(2*time.Second))
			}
			if len(cmds) == 0 {
				return m, nil
			}
			return m, tea.Batch(cmds...)
		}
		tasks := m.filterSuppressedHydratedTasks(msg.tasks)
		m.tasks = linearsync.ReconcileHydratedTasks(m.tasks, tasks)
		for i := range m.tasks {
			m.tasks[i].Session = cloneSession(m.tasks[i].Session)
		}
		m.applyPendingStatusOverlays()
		m.applyRuntimeSignals()
		m.reconcilePendingStatuses()
		m.editor.ReconcileSelection(m.tasks)
		m.applyPendingCreatedTaskSelection()
		m.reconcileCursorAfterIssuesRefresh()
		m.syncTaskWorkspaceOverlay()
		if msg.revision > m.daemonRevision {
			m.daemonRevision = msg.revision
		}
		m.loading = false
		m.boardRefreshing = false
		m.lastRefresh = time.Now()
		// Show success toast on first load
		if wasLoading {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: "Issues loaded",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		var cmds []tea.Cmd
		if !m.hasRefreshLoop {
			m.hasRefreshLoop = true
			cmds = append(cmds, tickEvery(2*time.Second))
		}
		if msg.events != nil {
			m.daemonEvents = msg.events
			cmds = append(cmds, m.waitForDaemonEventCmd())
		}
		if m.shouldRefreshRuntimeSignals() {
			m.runtimeSignalsBusy = true
			cmds = append(cmds, m.refreshRuntimeSignalsCmd(m.runtimeSignalRefreshTasks()))
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case runtimeSignalsLoadedMsg:
		if msg.projectID != "" && msg.projectID != m.daemonProjectID() {
			return m, nil
		}
		m.runtimeSignalsBusy = false
		m.runtimeSignalsByTask = msg.signalsByTask
		m.runtimeSignalRefreshedAtByTask = msg.refreshedAtByTask
		m.runtimeSignalWorktreeByTask = msg.worktreeByTask
		m.lastRuntimeRefresh = msg.refreshedAt
		m.applyRuntimeSignals()
		return m, nil

	case issuesErrorMsg:
		if msg.refreshSeq < m.issueRefreshSeq {
			return m, nil
		}
		if msg.projectID != "" && msg.projectID != m.daemonProjectID() {
			return m, nil
		}
		m.addToast(Toast{
			Level:   ToastError,
			Message: msg.err.Error(),
			Expires: time.Now().Add(8 * time.Second),
		})
		m.loading = false
		m.boardRefreshing = false
		// Still schedule a refresh to retry
		return m, tickEvery(5 * time.Second)

	case tickMsg:
		// Expire old toasts and refresh issues
		m.expireToasts()
		m.boardRefreshing = true
		m.issueRefreshSeq++
		return m, tea.Batch(
			m.loadIssuesCmd(),
			m.gitSyncService.FetchAndCheck(),
		)

	case monitor.SessionStateMsg:
		if session := m.sessionForIssue(msg.IssueID); session != nil {
			oldState := session.State
			m.logger.Debug("session state updated", "issueID", msg.IssueID, "state", msg.State)

			if oldState != msg.State && msg.State == domain.SessionWaiting {
				fmt.Print("\a")
				m.addToast(Toast{
					Level:   ToastWarning,
					Message: fmt.Sprintf("Session %s is waiting for input", msg.IssueID),
					Expires: time.Now().Add(10 * time.Second),
				})
			}
		}
		return m, nil

	case sessionStartedMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			m.markTaskOperationPending(msg.issueID, "session_start", msg.operationID, msg.state)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage("Session start", msg.issueID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		m.clearPendingTaskStatus(msg.issueID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Session started: %s", msg.issueID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case sessionStoppedMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			m.markTaskOperationPending(msg.issueID, "session_stop", msg.operationID, msg.state)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage("Session stop", msg.issueID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		m.clearPendingTaskStatus(msg.issueID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Session stopped: %s", msg.issueID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case sessionErrorMsg:
		m.clearPendingTaskStatus(msg.issueID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastError,
			Message: fmt.Sprintf("Session error: %s - %v", msg.issueID, msg.err),
			Expires: time.Now().Add(5 * time.Second),
		})
		return m, nil

	case conflictResolveFallbackMsg:
		m.addToast(Toast{
			Level: ToastWarning,
			Message: fmt.Sprintf(
				"Could not attach via daemon (%v). Run: tmux attach-session -t %s (AI can help resolve)",
				msg.err,
				msg.issueID,
			),
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case daemonStreamEventMsg:
		if msg.stream != nil && msg.stream != m.daemonEvents {
			return m, nil
		}
		if projectID := strings.TrimSpace(msg.event.ProjectID); projectID != "" && projectID != m.daemonProjectID() {
			return m, m.waitForDaemonEventCmd()
		}
		m.recordRuntimeEvent(msg.event)
		m.applyOperationProgressEvent(msg.event)
		if msg.event.Event == protocol.EventSessionUpdated {
			m.applySessionProjectionEvent(msg.event)
		}
		switch m.reduceDaemonEvent(msg.event) {
		case daemonEventIgnore:
			return m, m.waitForDaemonEventCmd()
		case daemonEventRefreshSnapshot:
			cursor := protocol.StreamCursor{Revision: m.daemonRevision}
			m.daemonRevision = cursor.Advance(msg.event).Revision
			return m, tea.Batch(m.loadIssuesCmd(), m.waitForDaemonEventCmd())
		case daemonEventRehydrate:
			m.daemonEvents = nil
			return m, m.attachDaemonCmd()
		default:
			return m, m.waitForDaemonEventCmd()
		}

	case daemonStreamClosedMsg:
		if msg.stream != nil && msg.stream != m.daemonEvents {
			return m, nil
		}
		m.daemonEvents = nil
		return m, m.attachDaemonCmd()

	case network.StatusMsg:
		// Update online status
		m.isOnline = msg.Online
		m.logger.Debug("network status updated", "online", msg.Online)
		return m, nil

	case git.GitSyncMsg:
		if msg.Err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Git sync failed: %v", msg.Err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		if m.gitSyncService.ShouldNotify(msg.CommitsBehind) {
			return m, m.openOverlay(overlay.NewGitPullOverlay(msg.CommitsBehind))
		}
		return m, nil

	case mergeResultMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			action := "Merge"
			if msg.stage == "stop_session" {
				action = "Stop session"
			}
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage(action, msg.sourceID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Merge failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		if msg.result.HasConflicts {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Merge conflicts: %s", strings.Join(msg.result.ConflictFiles, ", ")),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.openOverlay(overlay.NewConflictDialog(msg.result.ConflictFiles))
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Successfully merged %s into %s", msg.sourceID, msg.targetID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, m.loadIssuesCmd()

	case mergePreflightFailureMsg:
		return m, m.overlayStack.Push(overlay.NewMergePreflightOverlay(
			msg.sourceID,
			msg.targetID,
			msg.sourceWorktree,
			msg.targetWorktree,
			msg.reasons,
			msg.sourceFiles,
			msg.targetFiles,
			strings.TrimSpace(msg.targetWorktree) != "",
		))

	case mergePreflightActionResultMsg:
		sideTitle := strings.Title(msg.side)
		switch msg.action {
		case "discard":
			if msg.err != nil {
				m.addToast(Toast{
					Level:   ToastError,
					Message: fmt.Sprintf("Discard %s changes failed: %v", sideTitle, msg.err),
					Expires: time.Now().Add(5 * time.Second),
				})
				return m, nil
			}
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Discarded %s changes", strings.ToLower(sideTitle)),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.loadIssuesCmd()
		case "commit":
			if msg.err != nil {
				m.addToast(Toast{
					Level:   ToastError,
					Message: fmt.Sprintf("Commit %s changes failed: %v", strings.ToLower(sideTitle), msg.err),
					Expires: time.Now().Add(6 * time.Second),
				})
				return m, nil
			}
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Committed %s changes", strings.ToLower(sideTitle)),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.loadIssuesCmd()
		default:
			return m, nil
		}

	case mergeTargetSelectionResolvedMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: msg.err.Error(),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		if msg.targetID == "main" {
			return m, m.mergeToMainCmd(msg.sourceWorktree, msg.sourceID)
		}
		return m, m.followOnMergeIntoTargetCmd(msg.sourceWorktree, msg.targetWorktree, msg.sourceID, msg.targetID, msg.targetState)

	case fetchAndMergeResultMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			action := "Merge"
			if msg.stage == "fetch" {
				action = "Fetch"
			}
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage(action, msg.issueID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Merge failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		if msg.result.HasConflicts {
			// Show conflict dialog
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Merge conflicts in %d files", len(msg.result.ConflictFiles)),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.openOverlay(overlay.NewConflictDialog(msg.result.ConflictFiles))
		}

		// Successful merge
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: "Updated from main successfully",
			Expires: time.Now().Add(3 * time.Second),
		})
		if msg.attachAfter {
			return m, m.attachSessionCmd(msg.issueID)
		}
		return m, nil

	case createPRResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to get branch info: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		// Show PR command in toast
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Run: %s", msg.cmd),
			Expires: time.Now().Add(10 * time.Second),
		})
		return m, nil

	case abortMergeResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to abort merge: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: "Merge aborted successfully",
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	// Phase 6: Advanced features
	case overlay.JumpSelectedMsg:
		// Close overlay
		m.overlayStack.Pop()

		// Jump to selected task by flat index
		columns := m.buildColumns()
		m.nav.JumpToTaskByIndex(columns, msg.TaskIndex)
		m.ensureCursorVisible(columns)
		return m, nil

	case overlay.ProjectSelectedMsg:
		// Close overlay
		m.overlayStack.Pop()
		m.loading = true
		m.boardRefreshing = true
		m.projectSwitchInFlight = true
		m.issueRefreshSeq++
		m.projectSwitchSeq++

		// Switch project runtime context and reload issues.
		return m, m.switchProjectCmd(msg.Project)

	case projectSwitchResultMsg:
		if msg.switchSeq != 0 && msg.switchSeq != m.projectSwitchSeq {
			return m, nil
		}
		if msg.err != nil {
			m.loading = false
			m.boardRefreshing = false
			m.projectSwitchInFlight = false
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Project switch failed: %v", msg.err),
				Expires: time.Now().Add(6 * time.Second),
			})
			return m, nil
		}
		if msg.daemonClient != nil {
			m.daemonClient = msg.daemonClient
			if strings.TrimSpace(msg.daemonSocket) != "" {
				m.daemonSocketPath = msg.daemonSocket
			}
		}

		m.rebindProjectContext(msg.project, msg.projectConfig)

		// Reuse the normal loaded-state reducer path.
		tasks := m.filterSuppressedHydratedTasks(msg.tasks)
		m.tasks = linearsync.ReconcileHydratedTasks(m.tasks, tasks)
		for i := range m.tasks {
			m.tasks[i].Session = cloneSession(m.tasks[i].Session)
		}
		m.editor.ReconcileSelection(tasks)
		m.applyPendingCreatedTaskSelection()
		m.reconcileCursorAfterIssuesRefresh()
		if msg.revision > m.daemonRevision {
			m.daemonRevision = msg.revision
		}
		m.loading = false
		m.boardRefreshing = false
		m.projectSwitchInFlight = false
		m.lastRefresh = time.Now()
		m.daemonEvents = msg.events

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Switched to project: %s", msg.project.Name),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, m.waitForDaemonEventCmd()

	case overlay.TaskCreatedMsg:
		m.overlayStack.Pop()
		return m, m.saveTaskCmd(msg)

	case overlay.OpenTaskImageAttachMsg:
		issueID := strings.TrimSpace(msg.IssueID)
		if issueID == "" {
			return m, nil
		}
		attachOverlay := overlay.NewImageAttachOverlay(issueID, m.attachmentService)
		return m, m.openOverlay(attachOverlay)

	case taskCreatedResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to create task: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		// Clear persisted create-draft state only after successful new-task creation.
		// Updates from edit overlays must not clear the "new task" draft cache.
		if !msg.isUpdate {
			m.createTaskOverlay = nil
			if taskID := strings.TrimSpace(msg.taskID); taskID != "" {
				m.pendingCreatedTaskID = taskID
			}
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task created: %s", msg.taskID),
			Expires: time.Now().Add(3 * time.Second),
		})

		// Reload issues to show new task
		return m, m.loadIssuesCmd()

	// PR creation overlay messages
	case overlay.PRCreatedMsg:
		m.overlayStack.Pop()
		return m, m.createPRWithOverlayCmd(msg)

	case prCreatedResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to create PR: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("PR created: %s", msg.url),
			Expires: time.Now().Add(5 * time.Second),
		})
		return m, nil

		// Image attachment messages
	case overlay.AttachmentActionMsg:
		if msg.Action == "attached" {
			filename := "image"
			if msg.Attachment != nil && strings.TrimSpace(msg.Attachment.Filename) != "" {
				filename = msg.Attachment.Filename
			}
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Image attached: %s", filename),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.appendAttachmentNoteCmd(msg.Attachment)
		} else if msg.Action == "error" && msg.Error != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Image attachment failed: %s", compactErrorMessage(msg.Error)),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil

	case overlay.OpenImagePreviewMsg:
		imageService, ok := m.attachmentService.(*attachment.Service)
		if !ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: "Image preview unavailable: unsupported attachment service",
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		preview := overlay.NewImagePreviewOverlay(msg.IssueID, imageService, msg.InitialIndex)
		return m, m.openOverlay(preview)

	// Cleanup executed result
	case overlay.CleanupExecutedMsg:
		m.overlayStack.Pop()
		if msg.Error != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Cleanup failed: %v", msg.Error),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		// Show success toast with results
		result := msg.Result
		var operations []string
		if result.Deleted > 0 {
			operations = append(operations, fmt.Sprintf("%d deleted", result.Deleted))
		}
		if result.Archived > 0 {
			operations = append(operations, fmt.Sprintf("%d archived", result.Archived))
		}
		if result.WorktreesRemoved > 0 {
			operations = append(operations, fmt.Sprintf("%d worktrees removed", result.WorktreesRemoved))
		}
		if result.SessionsCleaned > 0 {
			operations = append(operations, fmt.Sprintf("%d sessions cleaned", result.SessionsCleaned))
		}

		message := "Cleanup completed"
		if len(operations) > 0 {
			message = fmt.Sprintf("Cleanup: %s", strings.Join(operations, ", "))
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(5 * time.Second),
		})

		// Reload issues to reflect changes
		return m, m.loadIssuesCmd()

	case branchBehindMsg:
		if msg.err != nil {
			m.logger.Warn("failed to check branch distance", "issueID", msg.issueID, "error", msg.err)
			// Proceed to attach anyway if check fails.
			return m, m.attachSessionCmd(msg.issueID)
		}

		if msg.commitsBehind > 0 {
			m.clearPendingTaskStatus(msg.issueID)
			m.syncTaskWorkspaceOverlay()
			// Show merge choice overlay
			m.openOverlay(overlay.NewMergeChoiceOverlay(msg.issueID, msg.commitsBehind, m.config.Git.BaseBranch))
			return m, nil
		}

		// Not behind, attach directly
		return m, m.attachSessionCmd(msg.issueID)

	case sessionAttachedMsg:
		m.clearPendingTaskStatus(msg.issueID)
		m.syncTaskWorkspaceOverlay()
		message := fmt.Sprintf("Attached to session: %s", msg.issueID)
		if msg.switchedTmux {
			message = fmt.Sprintf("Switched to session: %s", msg.issueID)
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case openPROverlayResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to get branch info: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		return m, m.openOverlay(overlay.NewPRCreateOverlay(msg.branch, m.config.Git.BaseBranch, msg.issueID))

	case openPRResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to open PR: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Opened PR for %s", msg.issueID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case helixOpenResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to open Helix: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		if msg.opened {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Opened Helix for %s", msg.issueID),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: msg.commandHint,
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case taskDeletedResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to archive task: %v", msg.err),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.suppressTaskHydration(msg.taskID)
		m.tasks = removeTaskByID(m.tasks, msg.taskID)
		m.editor.ReconcileSelection(m.tasks)
		m.applyRuntimeSignals()
		m.reconcileCursorAfterIssuesRefresh()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task %s archived", msg.taskID),
			Expires: time.Now().Add(2 * time.Second),
		})
		return m, m.loadIssuesCmd()

	case worktreeCleanupResultMsg:
		if msg.needsForce {
			m.pendingCleanup = &pendingWorktreeCleanupConfirmation{
				taskID:      msg.taskID,
				deletedTask: msg.deletedTask,
			}
			action := "cleanup worktree"
			if msg.deletedTask {
				action = "delete task and cleanup worktree"
			}
			confirm := overlay.NewConfirmDialog(
				"Force worktree cleanup?",
				fmt.Sprintf("Worktree has local changes.\n\nAction: %s\nTask: %s\n\nDetails: %s\n\nForce removal will discard modified/untracked files.\nProceed?", action, msg.taskID, msg.reason),
			)
			return m, m.openOverlay(confirm)
		}
		m.pendingCleanup = nil
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Worktree cleanup failed: %v", msg.err),
				Expires: time.Now().Add(4 * time.Second),
			})
			return m, nil
		}

		message := fmt.Sprintf("Worktree cleaned for %s", msg.taskID)
		if msg.deletedTask {
			message = fmt.Sprintf("Task %s deleted and worktree cleaned", msg.taskID)
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, m.loadIssuesCmd()

	case taskStatusResultMsg:
		if msg.err != nil {
			if pending, ok := pendingOperationDetails(msg.err); ok {
				m.markTaskStatusPending(msg.taskID, msg.previousStatus, msg.newStatus, pending.OperationID, pending.State)
				m.syncTaskWorkspaceOverlay()
				m.addToast(Toast{
					Level:   ToastInfo,
					Message: formatPendingOperationMessage("Task move", msg.taskID, pending.OperationID, pending.State),
					Expires: time.Now().Add(5 * time.Second),
				})
				return m, m.loadIssuesCmd()
			}
			m.rollbackTaskStatus(msg.taskID, msg.previousStatus)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to update task: %v", msg.err),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.clearPendingTaskStatus(msg.taskID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task moved to %s", msg.newStatus),
			Expires: time.Now().Add(2 * time.Second),
		})

		if msg.newStatus == domain.StatusDone {
			if session := m.sessionForIssue(msg.taskID); session != nil {
				return m, tea.Batch(
					m.loadIssuesCmd(),
					m.openPROverlayCmd(session.Worktree, msg.taskID),
				)
			}
		}

		return m, m.loadIssuesCmd()

	case bulkStatusResultMsg:
		m.loading = false
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Bulk action failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		summary := fmt.Sprintf("Bulk action completed: %d updated", msg.updated)
		level := ToastSuccess
		if len(msg.issues) > 0 {
			level = ToastWarning
			summary = fmt.Sprintf("%s, %d reported issues (%s)", summary, len(msg.issues), summarizeBulkIssues(msg.issues))
		}
		if msg.failed > 0 {
			level = ToastWarning
			summary = fmt.Sprintf("%s, %d failed", summary, msg.failed)
		}
		m.addToast(Toast{
			Level:   level,
			Message: summary,
			Expires: time.Now().Add(3 * time.Second),
		})
		m.editor.ClearSelection()
		m.editor.EnterNormal()
		return m, m.loadIssuesCmd()
	}

	if !m.overlayStack.IsEmpty() {
		return m, m.overlayStack.Update(msg)
	}

	return m, nil
}

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
	m.nav.SelectTask(col.Tasks[pos.Task].ID, pos.Column)
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

// View renders the current state as a string
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	if m.loading {
		return m.renderLoading()
	}

	var mainView string
	if m.viewMode == ViewModeCompact {
		mainView = m.renderCompactView()
	} else {
		mainView = m.renderBoardView()
	}

	// Clamp board/compact content to the space above the footer to keep
	// column headers and card rows stable even when internal render paths
	// overproduce lines (for example via wrapped content or spacing styles).
	mainView = lipgloss.NewStyle().
		Height(board.BoardContentHeight(m.height)).
		MaxHeight(board.BoardContentHeight(m.height)).
		Render(mainView)

	sb := statusbar.New(m.statusBarMode(), m.width, m.styles)
	sb.SetEventTicker(m.eventTicker)
	sb.SetCurrentProject(m.daemonProjectID())
	sb.SetSelectionSummary(m.selectionSummary())
	sb.SetFilterSummary(m.filterSummary())
	sb.SetSortSummary(m.sortSummary())
	if m.boardRefreshing {
		sb.SetModeSuffix(m.spinner.View())
	} else if m.runtimeSignalsBusy {
		sb.SetLoadingIndicator("Loading runtime status...")
	}
	if current := m.overlayStack.Current(); current != nil {
		if hintOverlay, ok := current.(interface {
			StatusBindings() []keybinds.Binding
		}); ok {
			sb.SetHintBindings(hintOverlay.StatusBindings())
		}
	}
	statusBarView := sb.Render()

	contentHeight := board.BoardContentHeight(m.height)
	contentView := lipgloss.NewStyle().
		MaxWidth(m.width).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Render(mainView)

	if !m.overlayStack.IsEmpty() {
		current := m.overlayStack.Current()
		overlayView := current.View()
		if overlayUsesFullScreen(current) {
			contentView = lipgloss.NewStyle().
				Width(m.width).
				MaxWidth(m.width).
				Height(contentHeight).
				MaxHeight(contentHeight).
				Render(overlayView)
			return lipgloss.JoinVertical(lipgloss.Left, contentView, statusBarView)
		}

		overlayWidth, overlayHeight := current.Size()

		if overlayWidth == 0 {
			contentView = lipgloss.NewStyle().
				Height(contentHeight).
				MaxHeight(contentHeight).
				Render(lipgloss.JoinVertical(lipgloss.Left, contentView, overlayView))
		} else {
			title := current.Title()
			if title != "" && !overlayUsesInternalTitle(current) {
				titleView := m.styles.OverlayTitle.Render(title)
				overlayView = lipgloss.JoinVertical(lipgloss.Left, titleView, overlayView)
			}
			if overlayUsesAppFrame(current) {
				overlayView = m.styles.Overlay.
					Width(overlayWidth).
					Height(overlayHeight).
					Render(overlayView)
			} else {
				overlayView = lipgloss.NewStyle().
					Width(overlayWidth).
					Height(overlayHeight).
					Render(overlayView)
			}
			overlayWidth, overlayHeight = renderedBlockSize(overlayView)

			contentView = lipgloss.NewStyle().
				MaxWidth(m.width).
				Height(contentHeight).
				MaxHeight(contentHeight).
				Render(contentView)
			contentView = m.layerCenteredOverlay(contentView, overlayView, m.width, contentHeight, overlayWidth, overlayHeight)
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, contentView, statusBarView)
}

func (m Model) statusBarMode() types.Mode {
	mode := m.editor.GetMode()
	current := m.overlayStack.Current()
	if current == nil {
		return mode
	}
	if modeOverlay, ok := current.(interface {
		StatusMode() types.Mode
	}); ok {
		return modeOverlay.StatusMode()
	}
	return mode
}

func (m Model) openOverlay(o overlay.Overlay) tea.Cmd {
	return tea.Batch(
		m.overlayStack.Push(o),
		func() tea.Msg {
			return tea.WindowSizeMsg{Width: m.width, Height: m.height}
		},
	)
}

func (m Model) layer(bottom, top string) string {
	return m.layerWithinHeight(bottom, top, m.height)
}

func overlayUsesInternalTitle(current overlay.Overlay) bool {
	internalTitleOverlay, ok := current.(interface {
		UsesInternalTitle() bool
	})
	return ok && internalTitleOverlay.UsesInternalTitle()
}

func overlayUsesAppFrame(current overlay.Overlay) bool {
	appFrameOverlay, ok := current.(interface {
		UsesAppFrame() bool
	})
	if !ok {
		return true
	}
	return appFrameOverlay.UsesAppFrame()
}

func overlayUsesFullScreen(current overlay.Overlay) bool {
	fullScreenOverlay, ok := current.(interface {
		UsesFullScreen() bool
	})
	return ok && fullScreenOverlay.UsesFullScreen()
}

func (m Model) layerWithinHeight(bottom, top string, height int) string {
	if height < 1 {
		height = 1
	}

	bLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(bottom), "\n")
	tLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(top), "\n")

	res := make([]string, height)
	for i := 0; i < height; i++ {
		var b, t string
		if i < len(bLines) {
			b = bLines[i]
		}
		if i < len(tLines) {
			t = tLines[i]
		}

		if strings.TrimSpace(t) == "" {
			res[i] = b
		} else {
			res[i] = t
		}
	}

	return strings.Join(res, "\n")
}

func (m Model) layerWithinHeightTransparent(bottom, top string, height int) string {
	if height < 1 {
		height = 1
	}

	bLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(bottom), "\n")
	tLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(top), "\n")

	res := make([]string, height)
	for i := 0; i < height; i++ {
		var b, t string
		if i < len(bLines) {
			b = bLines[i]
		}
		if i < len(tLines) {
			t = tLines[i]
		}

		if lineIsVisuallyEmpty(t) {
			res[i] = b
		} else {
			res[i] = mergeOverlayLine(b, t)
		}
	}

	return strings.Join(res, "\n")
}

func mergeOverlayLine(bottom, top string) string {
	left, right, ok := nonSpaceBounds(top)
	if !ok {
		return bottom
	}
	bottomWidth := ansi.StringWidth(bottom)
	if bottomWidth == 0 {
		return top
	}
	if left < 0 {
		left = 0
	}
	if right > bottomWidth {
		right = bottomWidth
	}
	if left >= right {
		return bottom
	}
	return ansi.Cut(bottom, 0, left) + ansi.Cut(top, left, right) + ansi.Cut(bottom, right, bottomWidth)
}

func (m Model) layerCenteredOverlay(bottom, overlayView string, width, height, overlayWidth, overlayHeight int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if overlayWidth < 1 || overlayHeight < 1 {
		return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(bottom)
	}
	if overlayWidth > width {
		overlayWidth = width
	}
	if overlayHeight > height {
		overlayHeight = height
	}

	bLines := strings.Split(lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(bottom), "\n")
	oLines := strings.Split(lipgloss.NewStyle().Width(overlayWidth).Height(overlayHeight).MaxHeight(overlayHeight).Render(overlayView), "\n")

	x := max(0, (width-overlayWidth)/2)
	y := max(0, (height-overlayHeight)/2)
	res := make([]string, len(bLines))
	copy(res, bLines)

	for i := 0; i < overlayHeight && i < len(oLines); i++ {
		row := y + i
		if row < 0 || row >= len(res) {
			continue
		}
		base := res[row]
		overlaySlice := lipgloss.NewStyle().Width(overlayWidth).Render(ansi.Cut(oLines[i], 0, overlayWidth))
		res[row] = ansi.Cut(base, 0, x) + overlaySlice + ansi.Cut(base, x+overlayWidth, width)
	}

	return strings.Join(res, "\n")
}

func nonSpaceBounds(line string) (left int, right int, ok bool) {
	stripped := ansi.Strip(line)
	cellPos := 0
	left = -1
	right = -1
	for _, r := range stripped {
		width := ansi.StringWidth(string(r))
		if width < 1 {
			continue
		}
		if !unicode.IsSpace(r) {
			if left == -1 {
				left = cellPos
			}
			right = cellPos + width
		}
		cellPos += width
	}
	if left == -1 || right <= left {
		return 0, 0, false
	}
	return left, right, true
}

func lineIsVisuallyEmpty(line string) bool {
	withoutANSI := ansiEscapeLinePattern.ReplaceAllString(line, "")
	return strings.TrimSpace(withoutANSI) == ""
}

func renderedBlockSize(view string) (width, height int) {
	width = lipgloss.Width(view)
	height = lipgloss.Height(view)
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return width, height
}

// buildColumns converts tasks into board columns, applying filter and sort
func (m Model) buildColumns() []board.Column {
	// For Phase 1, use placeholder data
	if m.usePlaceholder {
		return board.CreatePlaceholderData()
	}

	// Apply filter to tasks and enforce board-level child hiding semantics.
	filteredTasks := m.boardVisibleTasks(m.tasks)

	// Build columns from filtered tasks
	return []board.Column{
		{Title: "Open", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusOpen)},
		{Title: "In Progress", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusInProgress)},
		{Title: "Blocked", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusBlocked)},
		{Title: "Done", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusDone)},
	}
}

func (m Model) boardVisibleTasks(tasks []domain.Task) []domain.Task {
	if m.isDrillDownActive() {
		filter := *m.editor.GetFilter()
		filter.HideEpicChildren = false
		filtered := filter.Apply(tasks)
		parentID := strings.TrimSpace(m.drillDownParentID)
		result := make([]domain.Task, 0, len(filtered))
		for _, task := range filtered {
			if isChildOfParent(task, parentID) {
				result = append(result, task)
			}
		}
		return result
	}

	filtered := m.editor.ApplyFilter(tasks)
	result := make([]domain.Task, 0, len(filtered))
	for _, task := range filtered {
		if task.ParentID != nil && strings.TrimSpace(*task.ParentID) != "" {
			continue
		}
		childByDependency := false
		for _, dep := range task.Dependencies {
			depType := strings.TrimSpace(string(dep.Type))
			if (depType == string(domain.DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID) != "" {
				childByDependency = true
				break
			}
		}
		if childByDependency {
			continue
		}
		result = append(result, task)
	}
	return result
}

func (m Model) runtimeSignalRefreshTasks() []domain.Task {
	if m.viewMode == ViewModeCompact {
		return m.compactRenderedTasks()
	}
	return m.boardRenderedTasks()
}

func (m Model) boardRenderedTasks() []domain.Task {
	columns := m.buildColumns()
	if len(columns) == 0 {
		return nil
	}
	visibleStart, visibleEnd := m.boardVisibleColumnRange(columns)
	visibleColumns := columns[visibleStart:visibleEnd]
	if len(visibleColumns) == 0 {
		return nil
	}

	bodyHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	columnCount := m.boardVisibleColumnCount(len(columns))
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	if columnWidth < 1 {
		columnWidth = 1
	}
	cardWidth := board.CardContentWidth(columnWidth)
	linesPerCard := board.CardLineFootprint(m.styles, cardWidth)
	if linesPerCard < 1 {
		linesPerCard = 1
	}

	rendered := make([]domain.Task, 0, len(m.tasks))
	seen := make(map[string]struct{}, len(m.tasks))
	for localColumn, col := range visibleColumns {
		globalColumn := visibleStart + localColumn
		viewportStart := 0
		if globalColumn >= 0 && globalColumn < len(m.viewportStarts) {
			viewportStart = m.viewportStarts[globalColumn]
		}
		start, end := board.VisibleTaskWindow(len(col.Tasks), viewportStart, bodyHeight, linesPerCard)
		for i := start; i < end; i++ {
			task := col.Tasks[i]
			if _, exists := seen[task.ID]; exists {
				continue
			}
			seen[task.ID] = struct{}{}
			rendered = append(rendered, task)
		}
	}
	return rendered
}

func (m Model) compactRenderedTasks() []domain.Task {
	filtered := m.editor.ApplySort(m.boardVisibleTasks(m.tasks))
	if len(filtered) == 0 {
		return nil
	}

	columns := m.buildColumns()
	pos := m.nav.GetPosition(columns)
	cursor := m.getFlatIndexFromPosition(pos, columns)
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(filtered) {
		cursor = len(filtered) - 1
	}

	visibleRows := board.BoardContentHeight(m.height) - 2
	if visibleRows < 1 {
		visibleRows = 1
	}

	scrollOffset := 0
	if cursor >= visibleRows {
		scrollOffset = cursor - visibleRows + 1
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
	return filtered[scrollOffset:end]
}

func isChildOfParent(task domain.Task, parentID string) bool {
	if parentID == "" {
		return false
	}
	if task.ParentID != nil && strings.TrimSpace(*task.ParentID) == parentID {
		return true
	}
	for _, dep := range task.Dependencies {
		depType := strings.TrimSpace(string(dep.Type))
		if (depType == string(domain.DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID) == parentID {
			return true
		}
	}
	return false
}

// handleKey processes keyboard input based on current mode
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys (work in any mode)
	switch msg.String() {
	case "ctrl+c":
		// Cleanup before quitting
		m.sessionMonitor.StopAll()
		return m, tea.Quit
	case "ctrl+l":
		// Force redraw
		return m, tea.ClearScreen
	case "r":
		if m.editor.GetMode() != ModeAction {
			m.boardRefreshing = true
			m.issueRefreshSeq++
			return m, tea.Batch(m.loadIssuesCmd(), m.gitSyncService.FetchAndCheck())
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
	if m.projectSwitchInFlight {
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
		return m, m.openOverlay(overlay.NewEventLogOverlayWithLogFiles(
			m.runtimeEvents,
			m.eventLogFilePath(),
			m.daemonLogFilePath(),
		))
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
		task, session := m.getCurrentTaskAndSession()
		if task != nil {
			return m, m.openOverlay(overlay.NewTaskWorkspaceOverlay(*task, session, m.tasks, m.pendingMutationForTask(task.ID), m.width, m.height))
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
			children := m.getTaskChildren(task.ID)
			if len(children) > 0 {
				m.enterDrillDown(task.ID, task.Title)
				columns := m.buildColumns()
				m.nav.JumpToTaskByID(columns, children[0].ID)
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
		if m.createTaskOverlay == nil {
			m.createTaskOverlay = overlay.NewCreateTaskOverlayWithParentAndImplOptions(nil, m.availableTaskImplementations())
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
	case "u", "P":
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
			m.editor.Select(task.ID)
		}
		m.nav.MoveDown(columns)
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionMoveUp:
		// Keep current task selected, then move up.
		if task != nil {
			m.editor.Select(task.ID)
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
			m.editor.Select(task.ID)
		}
		m.nav.HalfPageDown(columns, m.halfPage())
		m.ensureCursorVisible(columns)
		return m, nil

	case keybinds.ActionHalfPageUp:
		if task != nil {
			m.editor.Select(task.ID)
		}
		m.nav.HalfPageUp(columns, m.halfPage())
		m.ensureCursorVisible(columns)
		return m, nil

	// Toggle current selection without moving.
	case keybinds.ActionSelectToggle:
		if task != nil {
			m.editor.ToggleSelection(task.ID)
		}
		return m, nil

	// Select all in current column
	case keybinds.ActionSelectColumnAll:
		status := m.nav.GetCurrentStatus(columns)
		for _, t := range m.tasks {
			if t.Status == status {
				m.editor.Select(t.ID)
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
			if m.editor.IsSelected(t.ID) {
				m.editor.Deselect(t.ID)
			} else {
				m.editor.Select(t.ID)
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
	events        <-chan protocol.EventEnvelope
	daemonClient  *daemonclient.Client
	daemonSocket  string
	stale         bool
	freshnessHint string
}

type issuesErrorMsg struct {
	refreshSeq uint64
	projectID string
	err       error
}

type projectSwitchResultMsg struct {
	switchSeq     uint64
	project       config.Project
	projectConfig *config.Config
	tasks         []domain.Task
	revision      uint64
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
		if strings.TrimSpace(body.Operation.OperationID) == "" {
			return
		}
		taskID := m.resolveOperationTaskID(body.Operation.IssueID, body.Operation.ResourceKeys)
		if taskID == "" {
			taskID = m.operationTaskID[body.Operation.OperationID]
		}
		if taskID == "" {
			return
		}
		m.operationTaskID[body.Operation.OperationID] = taskID
		state := protocol.OperationState(body.Operation.State)
		if operationStateTerminal(state) {
			delete(m.pendingOpsByTask, taskIDKey(taskID))
			delete(m.operationTaskID, body.Operation.OperationID)
			m.syncTaskWorkspaceOverlay()
			return
		}
		percent := 0
		switch state {
		case protocol.OperationStateRunning:
			percent = 50
		}
		m.pendingOpsByTask[taskIDKey(taskID)] = pendingOperationProgress{
			operationID: body.Operation.OperationID,
			state:       state,
			percent:     percent,
		}
		m.syncTaskWorkspaceOverlay()
	case protocol.EventOperationProgress:
		var body protocol.OperationProgressEventBody
		if err := json.Unmarshal(evt.Body, &body); err != nil {
			return
		}
		if strings.TrimSpace(body.OperationID) == "" {
			return
		}
		taskID := m.operationTaskID[body.OperationID]
		if taskID == "" {
			return
		}
		if operationStateTerminal(body.State) {
			delete(m.pendingOpsByTask, taskIDKey(taskID))
			delete(m.operationTaskID, body.OperationID)
			m.syncTaskWorkspaceOverlay()
			return
		}
		m.pendingOpsByTask[taskIDKey(taskID)] = pendingOperationProgress{
			operationID: body.OperationID,
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

func (m Model) resolveOperationTaskID(issueID string, resourceKeys []string) string {
	trimmedIssueID := strings.TrimSpace(issueID)
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
		if taskIDKey(task.ID) == key {
			return task.ID
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
			return task.ID
		}
	}
	return ""
}

type runtimeSignalsLoadedMsg struct {
	projectID           string
	signalsByTask       map[string]board.RuntimeSignals
	refreshedAtByTask   map[string]time.Time
	worktreeByTask      map[string]string
	refreshedAt         time.Time
	partialFailureCount int
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

		snapshot, err := m.daemonClient.ListTasksSnapshot(ctx)
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
			refreshSeq: refreshSeq,
			projectID: projectID,
			tasks:     snapshot.Tasks,
			revision:  snapshot.Revision,
		}
	}
}

func (m Model) shouldRefreshRuntimeSignals() bool {
	if m.runtimeSignalsBusy {
		return false
	}
	if len(m.tasks) == 0 {
		return false
	}
	if len(m.runtimeSignalsByTask) == 0 {
		return true
	}
	return time.Since(m.lastRuntimeRefresh) >= 15*time.Second
}

func (m *Model) applyRuntimeSignals() {
	if len(m.runtimeSignalsByTask) == 0 || len(m.tasks) == 0 {
		return
	}
	for i := range m.tasks {
		signals, ok := m.runtimeSignalsByTask[m.tasks[i].ID]
		if !ok {
			continue
		}
		m.tasks[i].HasTmuxSession = signals.HasTmuxSession
		m.tasks[i].HasWorktree = signals.HasWorktree
		m.tasks[i].GitAheadCount = signals.GitAheadCount
		m.tasks[i].GitBehindCount = signals.GitBehindCount
		m.tasks[i].HasUncommittedChanges = signals.HasUncommittedChanges
		m.tasks[i].GitAdditions = signals.GitAdditions
		m.tasks[i].GitDeletions = signals.GitDeletions
	}
}

func (m Model) refreshRuntimeSignalsCmd(tasks []domain.Task) tea.Cmd {
	projectID := m.daemonProjectID()
	baseBranch := strings.TrimSpace(m.config.Git.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	prioritizedTasks := prioritizeRuntimeSignalTasks(tasks)
	return func() tea.Msg {
		if m.daemonClient == nil {
			return runtimeSignalsLoadedMsg{
				projectID:         projectID,
				signalsByTask:     map[string]board.RuntimeSignals{},
				refreshedAtByTask: map[string]time.Time{},
				worktreeByTask:    map[string]string{},
				refreshedAt:       time.Now(),
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()

		worktrees, err := m.daemonClient.ListWorktrees(ctx)
		if err != nil {
			return runtimeSignalsLoadedMsg{
				projectID:         projectID,
				signalsByTask:     map[string]board.RuntimeSignals{},
				refreshedAtByTask: map[string]time.Time{},
				worktreeByTask:    map[string]string{},
				refreshedAt:       time.Now(),
			}
		}

		worktreeByIssue := make(map[string]string, len(worktrees))
		for _, wt := range worktrees {
			if wt.IssueID == "" || wt.Path == "" {
				continue
			}
			worktreeByIssue[taskIDKey(wt.IssueID)] = wt.Path
		}

		signalsByTask := make(map[string]board.RuntimeSignals, len(prioritizedTasks))
		refreshedAtByTask := make(map[string]time.Time, len(prioritizedTasks))
		worktreeByTask := make(map[string]string, len(prioritizedTasks))
		partialFailures := 0
		now := time.Now()
		for _, task := range prioritizedTasks {
			cachedSignals, hasCachedSignals := m.runtimeSignalsByTask[task.ID]
			signals := board.RuntimeSignals{HasTmuxSession: task.Session != nil}
			worktreePath, hasWorktree := worktreeByIssue[taskIDKey(task.ID)]
			signals.HasWorktree = hasWorktree
			if !hasWorktree {
				worktreeByTask[task.ID] = ""
				signalsByTask[task.ID] = signals
				if refreshedAt, ok := m.runtimeSignalRefreshedAtByTask[task.ID]; ok {
					refreshedAtByTask[task.ID] = refreshedAt
				}
				continue
			}

			if hasCachedSignals {
				signals.HasUncommittedChanges = cachedSignals.HasUncommittedChanges
				signals.GitAdditions = cachedSignals.GitAdditions
				signals.GitDeletions = cachedSignals.GitDeletions
				signals.GitAheadCount = cachedSignals.GitAheadCount
				signals.GitBehindCount = cachedSignals.GitBehindCount
			}

			if hasCachedSignals && m.shouldUseCachedRuntimeSignals(task, worktreePath, now) {
				worktreeByTask[task.ID] = worktreePath
				if refreshedAt, ok := m.runtimeSignalRefreshedAtByTask[task.ID]; ok {
					refreshedAtByTask[task.ID] = refreshedAt
				}
				signalsByTask[task.ID] = signals
				continue
			}

			status, statusErr := m.daemonClient.GitStatus(ctx, worktreePath)
			refreshSucceeded := statusErr == nil
			if statusErr == nil {
				signals.HasUncommittedChanges = status.HasChanges
			} else {
				partialFailures++
			}

			diffStat, diffErr := m.daemonClient.GitDiffStat(ctx, worktreePath, baseBranch)
			if diffErr == nil {
				signals.GitAdditions, signals.GitDeletions = parseDiffStatTotals(diffStat)
			} else {
				partialFailures++
				refreshSucceeded = false
			}

			if m.shouldCompareAgainstRemote() {
				behind, behindErr := m.daemonClient.CheckBranchBehind(ctx, daemonclient.BranchBehindCheckParams{
					Worktree:   worktreePath,
					BaseBranch: baseBranch,
					Remote:     "origin",
				})
				if behindErr == nil {
					signals.GitAheadCount = behind.CommitsAhead
					signals.GitBehindCount = behind.CommitsBehind
				} else {
					partialFailures++
					refreshSucceeded = false
				}
			}
			if refreshSucceeded {
				refreshedAtByTask[task.ID] = now
			}
			worktreeByTask[task.ID] = worktreePath
			signalsByTask[task.ID] = signals
		}

		return runtimeSignalsLoadedMsg{
			projectID:           projectID,
			signalsByTask:       signalsByTask,
			refreshedAtByTask:   refreshedAtByTask,
			worktreeByTask:      worktreeByTask,
			refreshedAt:         now,
			partialFailureCount: partialFailures,
		}
	}
}

func prioritizeRuntimeSignalTasks(tasks []domain.Task) []domain.Task {
	prioritized := make([]domain.Task, len(tasks))
	copy(prioritized, tasks)
	sort.SliceStable(prioritized, func(i, j int) bool {
		leftActive := hasActiveTmuxSession(prioritized[i])
		rightActive := hasActiveTmuxSession(prioritized[j])
		if leftActive == rightActive {
			return false
		}
		return leftActive
	})
	return prioritized
}

func hasActiveTmuxSession(task domain.Task) bool {
	if task.Session != nil {
		return true
	}
	return task.HasTmuxSession
}

func (m Model) shouldCompareAgainstRemote() bool {
	return m.isOnline && strings.EqualFold(strings.TrimSpace(m.config.Git.WorkflowMode), "origin")
}

func (m Model) shouldUseCachedRuntimeSignals(task domain.Task, worktreePath string, now time.Time) bool {
	if hasActiveTmuxSession(task) {
		return false
	}
	cachedWorktreePath, ok := m.runtimeSignalWorktreeByTask[task.ID]
	if !ok || strings.TrimSpace(cachedWorktreePath) != strings.TrimSpace(worktreePath) {
		return false
	}
	refreshedAt, ok := m.runtimeSignalRefreshedAtByTask[task.ID]
	if !ok || refreshedAt.IsZero() {
		return false
	}
	return now.Sub(refreshedAt) < runtimeSignalCacheTTL
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
		if m.isTaskHydrationSuppressed(task.ID) {
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
		if taskIDKey(task.ID) == target {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func (m Model) daemonClientForSocket(socketPath, projectID string) *daemonclient.Client {
	if m.daemonClient != nil {
		if strings.TrimSpace(m.daemonSocketPath) == "" || m.daemonSocketPath == socketPath {
			return m.daemonClient.WithProjectID(projectID)
		}
	}
	return daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
}

func (m Model) switchProjectCmd(project config.Project) tea.Cmd {
	switchSeq := m.projectSwitchSeq
	return func() tea.Msg {
		if m.daemonClient == nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     fmt.Errorf("daemon client unavailable"),
			}
		}
		if strings.TrimSpace(project.Path) == "" {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     fmt.Errorf("project %q has empty path", project.Name),
			}
		}
		projectConfig, err := config.LoadConfig(project.Path)
		if err != nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     fmt.Errorf("load config for project %q: %w", project.Name, err),
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		socketPath := config.DaemonSocketPathFor(project.Path)
		daemonClient := m.daemonClientForSocket(socketPath, project.Name)
		launcher := daemonprocess.NewLauncher(project.Path, socketPath)
		if bin := resolveDaemonBinaryForRepo(project.Path); bin != "" {
			launcher.BinPath = bin
		}
		if err := launcher.Replace(ctx); err != nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     fmt.Errorf("restart daemon for project %q: %w", project.Name, err),
			}
		}

		hello := protocol.Hello{
			ProtocolVersion: protocol.CurrentVersion,
			ClientName:      "tui",
			ClientVersion:   "dev",
			Capabilities:    []string{"snapshot", "subscribe"},
		}
		orch := autoclient.NewAutostartOrchestrator(autoclient.NewDaemonHandshaker(daemonClient), launcher)
		ack, err := orch.EnsureAttached(ctx, hello)
		if err != nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     fmt.Errorf("attach daemon for project %q: %w", project.Name, err),
			}
		}
		if !ack.Accepted {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     fmt.Errorf("daemon handshake rejected: %s", ack.Reason),
			}
		}

		snapshot, err := daemonClient.ListTasksSnapshot(ctx)
		if err != nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     err,
			}
		}
		events, err := daemonClient.Subscribe(context.Background(), project.Name, snapshot.Revision)
		if err != nil {
			return projectSwitchResultMsg{
				switchSeq: switchSeq,
				project: project,
				err:     err,
			}
		}

		return projectSwitchResultMsg{
			switchSeq:     switchSeq,
			project:       project,
			projectConfig: projectConfig,
			tasks:         snapshot.Tasks,
			revision:      snapshot.Revision,
			events:        events,
			daemonClient:  daemonClient,
			daemonSocket:  socketPath,
		}
	}
}

func (m Model) attachDaemonCmd() tea.Cmd {
	projectID := m.daemonProjectID()
	targetRepoDir := m.activeProjectPath()
	return func() tea.Msg {
		if m.daemonClient == nil {
			return issuesErrorMsg{projectID: projectID, err: fmt.Errorf("daemon client unavailable")}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		socketPath := config.DaemonSocketPathFor(targetRepoDir)
		daemonClient := m.daemonClientForSocket(socketPath, projectID)
		launcher := daemonprocess.NewLauncher(targetRepoDir, socketPath)
		if bin := resolveDaemonBinaryForRepo(targetRepoDir); bin != "" {
			launcher.BinPath = bin
		}

		// Startup should bind daemon authority to the active project context
		// when we have an explicit daemon binary path to execute.
		// If no explicit binary is resolved (for example in isolated tests),
		// fall back to attach-only behavior.
		if launcher.BinPath != "" {
			if err := launcher.Replace(ctx); err != nil {
				return issuesErrorMsg{projectID: projectID, err: fmt.Errorf("daemon restart: %w", err)}
			}
		}

		orch := autoclient.NewAutostartOrchestrator(autoclient.NewDaemonHandshaker(daemonClient), launcher)
		ack, err := orch.EnsureAttached(ctx, protocol.Hello{
			ProtocolVersion: protocol.CurrentVersion,
			ClientName:      "tui",
			ClientVersion:   "dev",
			Capabilities:    []string{"snapshot", "subscribe"},
		})
		if err != nil {
			return issuesErrorMsg{projectID: projectID, err: fmt.Errorf("daemon attach: %w", err)}
		}
		if !ack.Accepted {
			return issuesErrorMsg{projectID: projectID, err: fmt.Errorf("daemon handshake rejected: %s", ack.Reason)}
		}

		snapshot, err := daemonClient.ListTasksSnapshot(ctx)
		if err != nil {
			return issuesErrorMsg{projectID: projectID, err: err}
		}

		events, err := daemonClient.Subscribe(context.Background(), projectID, snapshot.Revision)
		if err != nil {
			return issuesErrorMsg{projectID: projectID, err: err}
		}

		return issuesLoadedMsg{
			projectID:    projectID,
			tasks:        snapshot.Tasks,
			revision:     snapshot.Revision,
			events:       events,
			daemonClient: daemonClient,
			daemonSocket: socketPath,
		}
	}
}

func (m Model) activeProjectPath() string {
	if m.projectRegistry != nil && m.currentProject != "" {
		if project, err := m.projectRegistry.Get(m.currentProject); err == nil && strings.TrimSpace(project.Path) != "" {
			return project.Path
		}
	}
	return m.repoDir
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
	m.rebuildProjectScopedServices()
	if m.daemonClient != nil {
		m.daemonClient.WithProjectID(m.daemonProjectID())
	}
}

func (m *Model) rebuildProjectScopedServices() {
	deps := appdeps.Build(m.config, m.repoDir, m.logger)
	m.gitSyncService = deps.GitSyncService
	m.gitClient = deps.GitDiffClient
	m.attachmentService = deps.AttachmentService
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
	if projectID := strings.TrimSpace(body.ProjectID); projectID != "" && projectID != m.daemonProjectID() {
		return
	}

	issueID := strings.TrimSpace(body.Session.IssueID)
	if issueID == "" {
		return
	}

	nextState, hasSession := projectSessionLifecycleState(body.Session.State)
	updatedAt := body.Session.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	for i := range m.tasks {
		if m.tasks[i].ID != issueID {
			continue
		}

		if !hasSession {
			m.tasks[i].Session = nil
			m.tasks[i].HasTmuxSession = false
			m.reconcilePendingStatuses()
			m.syncTaskWorkspaceOverlay()
			return
		}

		next := cloneSession(m.tasks[i].Session)
		if next == nil {
			next = &domain.Session{IssueID: issueID}
		}
		next.IssueID = issueID
		next.State = nextState
		if next.StartedAt == nil {
			startedAt := updatedAt
			next.StartedAt = &startedAt
		}
		m.tasks[i].Session = next
		m.tasks[i].HasTmuxSession = true
		m.reconcilePendingStatuses()
		m.syncTaskWorkspaceOverlay()
		return
	}
}

func (m Model) reduceDaemonEvent(evt protocol.EventEnvelope) daemonEventDecision {
	cursor := protocol.StreamCursor{Revision: m.daemonRevision}
	switch cursor.Decide(evt) {
	case protocol.StreamProjectionDecisionIgnore:
		return daemonEventIgnore
	case protocol.StreamProjectionDecisionResync:
		return daemonEventRehydrate
	default:
		return daemonEventRefreshSnapshot
	}
}

func (m Model) daemonProjectID() string {
	if m.currentProject != "" {
		return m.currentProject
	}
	if m.projectRegistry != nil {
		if project := m.projectRegistry.GetDefault(); project != nil && project.Name != "" {
			return project.Name
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Base(cwd)
	}
	return "default"
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
func (m Model) startSessionCmd(issueID string, baseBranch string, yolo bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

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
	// Apply sort
	return m.editor.ApplySort(inColumn)
}

// getCurrentTaskAndSession returns the currently selected task and its session
func (m Model) getCurrentTaskAndSession() (*domain.Task, *domain.Session) {
	columns := m.buildColumns()
	cursor := m.nav.GetCursor()
	if task, session := m.nav.GetCurrentTask(columns); task != nil {
		if cursor == nil || cursor.TaskID == "" || task.ID == cursor.TaskID {
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
		if m.tasks[i].ID == cursor.TaskID {
			task := m.tasks[i]
			return &task, task.Session
		}
	}
	return nil, nil
}

// handleBulkAction handles bulk action menu selections
func (m Model) handleBulkAction(msg overlay.BulkActionMsg) (tea.Model, tea.Cmd) {
	count := len(msg.SelectedIDs)
	if count == 0 {
		return m, nil
	}

	switch msg.Action {
	case "h": // Move left (previous status)
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, -1)

	case "l": // Move right (next status)
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, 1)

	case "o": // Set to Open
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusOpen)

	case "i": // Set to In Progress
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusInProgress)

	case "b": // Set to Blocked
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusBlocked)

	case "D": // Set to Done
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusDone)

	case "d": // Delete selected
		return m, m.bulkDeleteCmd(msg.SelectedIDs)

	case "a":
		return m, m.bulkArchiveCmd(msg.SelectedIDs)

	case "x": // Clear selection
		m.editor.ClearSelection()
		m.editor.EnterNormal()
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Selection cleared",
			Expires: time.Now().Add(2 * time.Second),
		})
	}

	return m, nil
}

// handleSelection handles overlay selection messages
func (m Model) handleSelection(msg overlay.SelectionMsg) (tea.Model, tea.Cmd) {
	// Handle special overlay-specific messages first (before popping overlay)
	switch msg.Key {
	case "abort", "claude", "manual":
		// Conflict resolution messages - extract the value
		if resolution, ok := msg.Value.(overlay.ConflictResolutionMsg); ok {
			return m.handleConflictResolution(resolution)
		}
	case "merge":
		// Merge target selection message
		if mergeMsg, ok := msg.Value.(overlay.MergeTargetSelectedMsg); ok {
			return m.handleMergeTargetSelection(mergeMsg)
		}
	case "m":
		task, session := m.getCurrentTaskAndSession()
		return m, m.followOnMergeSelectionCmd(task, session)
	case "merge_preflight_abort":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.abortMergeCmd(worktree)
	case "merge_preflight_discard_source":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.discardChangesCmd("source", worktree)
	case "merge_preflight_discard_target":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.discardChangesCmd("target", worktree)
	case "merge_preflight_commit_source":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.commitChangesCmd("source", worktree)
	case "merge_preflight_commit_target":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.commitChangesCmd("target", worktree)
	case "merge_preflight_refresh":
		m.overlayStack.Pop()
		return m, m.loadIssuesCmd()
	case "projects":
		// Settings -> Manage projects
		m.overlayStack.Pop() // Close settings
		return m, m.openOverlay(overlay.NewProjectSelectorWithOptions(
			m.projectRegistry,
			overlay.WithInitialCursor(m.projectSelectorCursor()),
			overlay.WithCurrentProjectName(m.currentProject),
		))
	case "event-log-stream":
		switch value := msg.Value.(type) {
		case string:
			return m, m.openLogStreamCmd(value)
		case []string:
			return m, m.openLogStreamCmd(value...)
		}
		return m, nil
	case "event-log-editor":
		if path, ok := msg.Value.(string); ok {
			return m, m.openLogEditorCmd(path)
		}
		return m, nil
	case "event-log-error":
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Log action failed: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	case "event-log-opened":
		return m, tea.ClearScreen
	case "editor-error":
		// Editor open error
		m.overlayStack.Pop()
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Editor error: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	case "editor-closed":
		// Editor closed successfully
		m.overlayStack.Pop()
		return m, nil
	case "select_child":
		// Epic drill-down: child task selected
		m.overlayStack.Pop()
		if childID, ok := msg.Value.(string); ok {
			// Jump to the child task by ID
			columns := m.buildColumns()
			m.nav.JumpToTaskByID(columns, childID)
			m.ensureCursorVisible(columns)
		}
		return m, nil
	case "set-default-success", "remove-success", "detect-success":
		// Project registry actions succeeded - just show success toast
		if name, ok := msg.Value.(string); ok {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Project %s: %s", msg.Key[:len(msg.Key)-8], name), // Remove "-success"
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		return m, nil
	case "settings-save-error":
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to save settings: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	case "set-default-error", "remove-error", "add-error", "save-error", "detect-error":
		// Project registry actions failed
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Error: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	}

	keepWorkspaceOpen := false
	if msg.Key == "a" {
		_, keepWorkspaceOpen = m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	}

	// Close the overlay first unless task-workspace attach wants in-panel progress.
	if !keepWorkspaceOpen {
		m.overlayStack.Pop()
	}

	if msg.Key == "yes" && m.pendingCleanup != nil {
		pending := m.pendingCleanup
		m.pendingCleanup = nil
		return m, m.cleanupWorktreeCmd(pending.taskID, pending.deletedTask, true)
	}
	if msg.Key == "no" && m.pendingCleanup != nil {
		pending := m.pendingCleanup
		m.pendingCleanup = nil
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Cancelled forced cleanup for %s", pending.taskID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}

	task, session := m.getCurrentTaskAndSession()
	if task == nil {
		return m, nil
	}

	// Handle the selection based on key
	switch msg.Key {
	// Session actions
	case "s":
		// Start session
		return m, m.startSessionCmd(task.ID, m.resolveBaseBranch(), false)
	case "S":
		// Start session directly without origin/base selection prompt.
		return m, m.startSessionCmd(task.ID, m.resolveBaseBranch(), false)
	case "!":
		// Start session with dangerous skip-permissions mode.
		return m, m.startSessionCmd(task.ID, m.resolveBaseBranch(), true)
	case "session_origin":
		if originMsg, ok := msg.Value.(overlay.MergeTargetSelectedMsg); ok {
			return m, m.startSessionCmd(task.ID, m.originBranchForSelection(originMsg.SourceID), false)
		}
		return m, nil
	case "a":
		// Attach to session
		if session != nil {
			if keepWorkspaceOpen {
				m.markTaskOperationPending(task.ID, "session_attach", "session-attach", protocol.OperationStateRunning)
				m.syncTaskWorkspaceOverlay()
			}
			// Check if branch is behind main
			return m, m.checkBranchBehindCmd(session.Worktree, task.ID)
		} else if task.HasTmuxSession {
			if keepWorkspaceOpen {
				m.markTaskOperationPending(task.ID, "session_attach", "session-attach", protocol.OperationStateRunning)
				m.syncTaskWorkspaceOverlay()
			}
			// We still have tmux presence, so attempt direct attach even when
			// the session projection is stale or not yet hydrated.
			return m, m.attachSessionCmd(task.ID)
		} else {
			if keepWorkspaceOpen {
				m.clearPendingTaskStatus(task.ID)
				m.syncTaskWorkspaceOverlay()
			}
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No active session for this task",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
	case "p":
		// TODO: Pause session
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Pause session (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	case "x":
		// Stop session
		if session != nil {
			return m, m.stopSessionCmd(task.ID)
		} else {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No active session for this task",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
	case "R":
		// TODO: Resume session
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Resume session (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})

	// Git actions
	case "u":
		// Update from main
		if session == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		return m, m.fetchAndMergeCmd(session.Worktree, "main", task.ID, false)

	case "m":
		// Follow-on merge from dependency-aware context.
		return m, m.followOnMergeSelectionCmd(task, session)

	case "P":
		// Create PR (with overlay)
		if session == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		// Get current branch name and open PR creation overlay
		return m, m.openPROverlayCmd(session.Worktree, task.ID)
	case "O":
		// Open PR in browser for current branch
		if session == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		return m, m.openPRCmd(session.Worktree, task.ID)
	case "M":
		// Abort in-progress merge in worktree
		if session == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		return m, m.abortMergeCmd(session.Worktree)
	case "H":
		// Open Helix in the task worktree.
		if session == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		return m, m.openHelixCmd(session.Worktree, task.ID)

	case "f":
		// Show diff viewer
		diffWorktree := strings.TrimSpace(m.repoDir)
		if session != nil && strings.TrimSpace(session.Worktree) != "" {
			diffWorktree = strings.TrimSpace(session.Worktree)
		}
		if diffWorktree == "" {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No worktree available for diff",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		// Open diff viewer overlay
		openPopup := func(ctx context.Context, title, command string) error {
			if strings.TrimSpace(os.Getenv("TMUX")) == "" || m.tmuxClient == nil {
				return fmt.Errorf("diff popup unavailable outside tmux; run inside tmux and retry")
			}
			popupCommand := fmt.Sprintf("cd %s && %s", shellSingleQuote(diffWorktree), command)
			return m.tmuxClient.DisplayPopup(ctx, title, "95%", "95%", popupCommand)
		}
		viewer := diff.NewDiffViewer(diffWorktree, m.config.Git.BaseBranch, m.gitClient, openPopup)
		cmd := m.openOverlay(viewer)
		return m, cmd
	case "w":
		// Cleanup worktree and keep task.
		return m, m.cleanupWorktreeCmd(task.ID, false, false)
	case "W":
		// Delete task and cleanup worktree.
		return m, m.cleanupWorktreeCmd(task.ID, true, false)

	case "i":
		// Image attachments
		attachOverlay := overlay.NewImageAttachOverlay(task.ID, m.attachmentService)
		return m, m.openOverlay(attachOverlay)

	case "r":
		// Dev server menu
		servers := m.getDevServerInfo(task.ID)
		devOverlay := overlay.NewDevServerOverlay(
			servers,
			task.ID,
			func(serverID string) tea.Cmd { return m.toggleDevServer(serverID) },
			func(serverID string) tea.Cmd { return m.viewDevServer(serverID) },
			func(serverID string) tea.Cmd { return m.restartDevServer(serverID) },
			func() tea.Cmd { return func() tea.Msg { return overlay.CloseOverlayMsg{} } },
		)
		return m, m.openOverlay(devOverlay)

	case "b":
		return m, m.openMergeTargetSelection(task)

	// Task actions
	case "h":
		// Move task left (to previous status)
		if task.Status == domain.StatusOpen {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Task is already in Open status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		newStatus, ok := shiftedTaskStatus(task.Status, -1)
		if !ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: "Failed to compute previous status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		m.applyOptimisticTaskStatus(task.ID, newStatus)
		return m, m.moveTaskStatusCmd(task.ID, task.Status, newStatus)

	case "l":
		// Move task right (to next status)
		if task.Status == domain.StatusDone {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Task is already in Done status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		newStatus, ok := shiftedTaskStatus(task.Status, 1)
		if !ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: "Failed to compute next status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		m.applyOptimisticTaskStatus(task.ID, newStatus)
		return m, m.moveTaskStatusCmd(task.ID, task.Status, newStatus)
	case "e":
		return m, m.openOverlay(overlay.NewEditTaskOverlayWithImplOptionsAndAttachmentService(*task, m.availableTaskImplementations(), m.attachmentService))
	case "T":
		return m, m.deleteTaskCmd(task.ID)
	case "d":
		return m, m.deleteTaskCmd(task.ID)
	case "c":
		parentID := task.ID
		return m, m.openOverlay(overlay.NewCreateTaskOverlayWithParentAndImplOptions(&parentID, m.availableTaskImplementations()))
	}

	return m, nil
}

func (m Model) eventLogFilePath() string {
	if strings.TrimSpace(m.logFilePath) != "" {
		return m.logFilePath
	}
	return resolveTUILogFilePath(m.config)
}

func (m Model) daemonLogFilePath() string {
	repoDir := strings.TrimSpace(m.repoDir)
	if repoDir == "" {
		repoDir = "."
	}
	return filepath.Join(repoDir, ".azedarach", "daemon.log")
}

func resolveTUILogFilePath(cfg *config.Config) string {
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

func newTUILogger(logPath string) *slog.Logger {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	return func() tea.Msg {
		path := strings.TrimSpace(logPath)
		if path == "" {
			return overlay.SelectionMsg{Key: "event-log-error", Value: errors.New("log file path is empty")}
		}
		if _, err := os.Stat(path); err != nil {
			return overlay.SelectionMsg{
				Key:   "event-log-error",
				Value: fmt.Errorf("log file unavailable: %w", err),
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
		if err := cmd.Run(); err != nil {
			return overlay.SelectionMsg{
				Key:   "event-log-error",
				Value: fmt.Errorf("open log editor: %w", err),
			}
		}
		return overlay.SelectionMsg{Key: "event-log-opened", Value: path}
	}
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

type pendingWorktreeCleanupConfirmation struct {
	taskID      string
	deletedTask bool
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if m.daemonClient == nil {
			return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, err: fmt.Errorf("daemon client unavailable")}
		}

		if session := m.sessionForIssue(taskID); session != nil {
			m.sessionMonitor.Stop(taskID)
			if _, err := m.daemonClient.StopSession(ctx, taskID); err != nil {
				if force && isSessionAlreadyStoppedError(err) {
					// Force-retry path may re-enter before projections clear the stale session.
					// If daemon already stopped it, continue to worktree removal.
				} else {
					return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force, err: err}
				}
			}
		}

		if err := m.daemonClient.RemoveWorktreeWithOptions(ctx, taskID, force); err != nil {
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
			if err := m.daemonClient.DeleteTask(ctx, taskID); err != nil {
				return worktreeCleanupResultMsg{taskID: taskID, deletedTask: true, force: force, err: err}
			}
		}

		return worktreeCleanupResultMsg{taskID: taskID, deletedTask: deleteTask, force: force}
	}
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
		visible[task.ID] = struct{}{}
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
		return "S:session/asc"
	}

	field := strings.TrimSpace(string(sortState.Field))
	if field == "" {
		field = string(domain.SortBySession)
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
		ProjectID: m.daemonProjectID(),
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
		evt.ProjectID = m.daemonProjectID()
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
	case "task.created":
		return "Task created"
	case "task.updated":
		return "Task updated"
	case "task.deleted":
		return "Task deleted"
	case "task.archived":
		return "Task archived"
	case "session.started":
		return "Session started"
	case "session.stopped":
		return "Session stopped"
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
	result      *git.MergeResult
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
func (m Model) fetchAndMergeCmd(worktree, branch, issueID string, attachAfter bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()
		if branch == "" {
			branch = "main"
		}

		if m.daemonClient == nil {
			return fetchAndMergeResultMsg{
				worktree:    worktree,
				issueID:     issueID,
				attachAfter: attachAfter,
				err:         fmt.Errorf("daemon client unavailable"),
			}
		}

		// Fetch from origin through the daemon command surface.
		if _, err := m.daemonClient.GitFetch(ctx, worktree, "origin"); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return fetchAndMergeResultMsg{
					worktree:    worktree,
					issueID:     issueID,
					attachAfter: attachAfter,
					stage:       "fetch",
					operationID: pending.OperationID,
					state:       pending.State,
				}
			}
			return fetchAndMergeResultMsg{
				worktree:    worktree,
				issueID:     issueID,
				attachAfter: attachAfter,
				err:         fmt.Errorf("fetch failed: %w", err),
			}
		}

		// Merge origin/branch through the daemon command surface.
		result, err := m.daemonClient.GitMerge(ctx, worktree, "origin/"+branch)
		if pending, ok := pendingOperationDetails(err); ok {
			return fetchAndMergeResultMsg{
				worktree:    worktree,
				issueID:     issueID,
				attachAfter: attachAfter,
				stage:       "merge",
				operationID: pending.OperationID,
				state:       pending.State,
			}
		}
		return fetchAndMergeResultMsg{
			worktree:    worktree,
			issueID:     issueID,
			attachAfter: attachAfter,
			stage:       "merge",
			result:      &result.Result,
			err:         err,
		}
	}
}

type sessionAttachedMsg struct {
	issueID      string
	switchedTmux bool
}

func (m Model) attachSessionCmd(issueID string) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return sessionErrorMsg{issueID: issueID, err: fmt.Errorf("daemon client unavailable")}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if _, err := m.daemonClient.AttachSession(ctx, issueID); err != nil {
			return sessionErrorMsg{issueID: issueID, err: err}
		}

		if m.tmuxAvailable || strings.TrimSpace(os.Getenv("TMUX")) != "" {
			targets := []string{
				issueID,
				naming.CanonicalSessionID(m.daemonProjectID(), issueID),
			}
			seen := map[string]struct{}{}
			var lastErr error
			for _, target := range targets {
				target = strings.TrimSpace(target)
				if target == "" {
					continue
				}
				if _, exists := seen[target]; exists {
					continue
				}
				seen[target] = struct{}{}
				if m.tmuxClient == nil {
					break
				}
				if err := m.tmuxClient.SwitchClient(ctx, target); err == nil {
					return sessionAttachedMsg{issueID: issueID, switchedTmux: true}
				} else {
					lastErr = err
				}
			}
			if lastErr != nil {
				return sessionErrorMsg{
					issueID: issueID,
					err:     fmt.Errorf("attached in daemon but failed to switch tmux client: %w", lastErr),
				}
			}
		}

		return sessionAttachedMsg{issueID: issueID, switchedTmux: false}
	}
}

func (m Model) resolveConflictWithAICmd(issueID string) tea.Cmd {
	return func() tea.Msg {
		msg := m.attachSessionCmd(issueID)()
		if errMsg, ok := msg.(sessionErrorMsg); ok {
			return conflictResolveFallbackMsg{
				issueID: issueID,
				err:     errMsg.err,
			}
		}
		return msg
	}
}

// createPRCmd generates the gh pr create command
func (m Model) createPRCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		branch, err := m.resolveWorktreeBranch(ctx, worktree, issueID)
		if err != nil {
			return createPRResultMsg{
				issueID: issueID,
				err:     fmt.Errorf("failed to get current branch: %w", err),
			}
		}

		// Generate gh pr create command
		cmd := fmt.Sprintf("gh pr create --head %s --title \"[%s] ...\" --body \"...\"", branch, issueID)

		return createPRResultMsg{
			issueID: issueID,
			cmd:     cmd,
			err:     nil,
		}
	}
}

func (m Model) openPRCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		branch, err := m.resolveWorktreeBranch(ctx, worktree, issueID)
		if err != nil {
			return openPRResultMsg{issueID: issueID, err: fmt.Errorf("resolve branch: %w", err)}
		}
		cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--head", branch, "--web")
		cmd.Dir = worktree
		if err := cmd.Run(); err != nil {
			return openPRResultMsg{issueID: issueID, err: err}
		}
		return openPRResultMsg{issueID: issueID}
	}
}

func (m Model) openHelixCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(worktree) == "" {
			return helixOpenResultMsg{issueID: issueID, err: fmt.Errorf("worktree path is empty")}
		}
		if strings.TrimSpace(os.Getenv("TMUX")) != "" && m.tmuxClient != nil {
			popupCommand := fmt.Sprintf("cd %s && hx", shellSingleQuote(worktree))
			if err := m.tmuxClient.DisplayPopup(context.Background(), "hx-"+issueID, "90%", "90%", popupCommand); err != nil {
				return helixOpenResultMsg{issueID: issueID, err: err}
			}
			return helixOpenResultMsg{issueID: issueID, opened: true}
		}
		return helixOpenResultMsg{
			issueID:     issueID,
			commandHint: fmt.Sprintf("Run: cd %s && hx", worktree),
		}
	}
}

// handleConflictResolution handles conflict resolution choices
func (m Model) handleConflictResolution(resolution overlay.ConflictResolutionMsg) (tea.Model, tea.Cmd) {
	// Close the overlay
	m.overlayStack.Pop()

	task, session := m.getCurrentTaskAndSession()
	if task == nil || session == nil {
		return m, nil
	}

	switch {
	case resolution.Abort:
		// Abort the merge
		return m, m.abortMergeCmd(session.Worktree)

	case resolution.OpenManually:
		// Show instructions to open in editor
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Open conflicted files in your editor at: %s", session.Worktree),
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case resolution.ResolveWithClaude:
		// Attach to tmux session so AI can resolve merge conflicts in-session.
		if !m.tmuxAvailable {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("tmux attach-session -t %s is unavailable outside tmux; launch az inside tmux to use tmux actions", task.ID),
				Expires: time.Now().Add(8 * time.Second),
			})
			return m, nil
		}
		if m.daemonClient == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Daemon unavailable. Run: tmux attach-session -t %s (AI can help resolve)", task.ID),
				Expires: time.Now().Add(8 * time.Second),
			})
			return m, nil
		}
		return m, m.resolveConflictWithAICmd(task.ID)

	default:
		return m, nil
	}
}

// handleMergeTargetSelection handles merge target selection
func (m Model) handleMergeTargetSelection(msg overlay.MergeTargetSelectedMsg) (tea.Model, tea.Cmd) {
	m.overlayStack.Pop()
	targetState := domain.SessionIdle
	if targetSession := m.sessionForIssue(msg.TargetID); targetSession != nil {
		targetState = targetSession.State
	}
	return m, m.resolveMergeTargetSelectionCmd(msg.SourceID, msg.TargetID, targetState)
}

func (m Model) resolveMergeTargetSelectionCmd(sourceID, targetID string, targetState domain.SessionState) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		sourceWorktree, err := m.resolveIssueWorktreePath(ctx, sourceID)
		if err != nil || sourceWorktree == "" {
			return mergeTargetSelectionResolvedMsg{
				sourceID:    sourceID,
				targetID:    targetID,
				targetState: targetState,
				err:         fmt.Errorf("source session worktree not found"),
			}
		}

		if targetID == "main" {
			return mergeTargetSelectionResolvedMsg{
				sourceID:       sourceID,
				targetID:       targetID,
				sourceWorktree: sourceWorktree,
				targetWorktree: m.activeProjectPath(),
				targetState:    targetState,
			}
		}

		targetWorktree, err := m.resolveIssueWorktreePath(ctx, targetID)
		if err != nil || targetWorktree == "" {
			return mergeTargetSelectionResolvedMsg{
				sourceID:       sourceID,
				targetID:       targetID,
				sourceWorktree: sourceWorktree,
				targetState:    targetState,
				err:            fmt.Errorf("target session worktree not found"),
			}
		}
		return mergeTargetSelectionResolvedMsg{
			sourceID:       sourceID,
			targetID:       targetID,
			sourceWorktree: sourceWorktree,
			targetWorktree: targetWorktree,
			targetState:    targetState,
		}
	}
}

type mergeResultMsg struct {
	sourceID    string
	targetID    string
	result      *git.MergeResult
	stage       string
	state       protocol.OperationState
	operationID string
	err         error
}

type mergePreflightFailureMsg struct {
	sourceID       string
	sourceWorktree string
	targetID       string
	targetWorktree string
	reasons        []string
	sourceFiles    []string
	targetFiles    []string
}

type mergePreflightActionResultMsg struct {
	action   string
	side     string
	worktree string
	err      error
}

type mergeTargetSelectionResolvedMsg struct {
	sourceID       string
	targetID       string
	sourceWorktree string
	targetWorktree string
	targetState    domain.SessionState
	err            error
}

func (m Model) mergeToMainCmd(sourceWorktree, sourceID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		baseBranch := m.resolveBaseBranch()
		mainWorktree := m.activeProjectPath()
		if strings.TrimSpace(mainWorktree) == "" {
			mainWorktree = "."
		}

		branch, err := m.resolveWorktreeBranch(ctx, sourceWorktree, sourceID)
		if err != nil {
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: err}
		}

		m.logger.Info("merging upstream source into main",
			"sourceID", sourceID,
			"sourceBranch", branch,
			"targetBranch", baseBranch,
		)

		if m.daemonClient == nil {
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: fmt.Errorf("daemon client unavailable")}
		}

		if preflight := m.checkMergePreflight(ctx, sourceID, "main", sourceWorktree, mainWorktree); preflight != nil {
			return *preflight
		}

		if _, err := m.daemonClient.GitFetch(ctx, ".", "origin"); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return mergeResultMsg{
					sourceID:    sourceID,
					targetID:    "main",
					stage:       "fetch",
					state:       pending.State,
					operationID: pending.OperationID,
				}
			}
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: err}
		}

		if _, err := m.daemonClient.GitCheckout(ctx, ".", baseBranch); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return mergeResultMsg{
					sourceID:    sourceID,
					targetID:    "main",
					stage:       "checkout",
					state:       pending.State,
					operationID: pending.OperationID,
				}
			}
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: err}
		}

		result, err := m.daemonClient.GitMerge(ctx, ".", branch)
		if pending, ok := pendingOperationDetails(err); ok {
			return mergeResultMsg{
				sourceID:    sourceID,
				targetID:    "main",
				stage:       "merge",
				state:       pending.State,
				operationID: pending.OperationID,
			}
		}
		return mergeResultMsg{sourceID: sourceID, targetID: "main", result: &result.Result, err: err}
	}
}

func (m Model) mergeFeatureIntoFeatureCmd(sourceWorktree, targetWorktree, sourceID, targetID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		sourceBranch, err := m.resolveWorktreeBranch(ctx, sourceWorktree, sourceID)
		if err != nil {
			return mergeResultMsg{sourceID: sourceID, targetID: targetID, err: err}
		}

		m.logger.Info("merging upstream source into target issue branch",
			"sourceID", sourceID,
			"targetID", targetID,
			"sourceBranch", sourceBranch,
			"targetWorktree", targetWorktree,
		)

		if m.daemonClient == nil {
			return mergeResultMsg{sourceID: sourceID, targetID: targetID, err: fmt.Errorf("daemon client unavailable")}
		}

		if preflight := m.checkMergePreflight(ctx, sourceID, targetID, sourceWorktree, targetWorktree); preflight != nil {
			return *preflight
		}

		result, err := m.daemonClient.GitMerge(ctx, targetWorktree, sourceBranch)
		if pending, ok := pendingOperationDetails(err); ok {
			return mergeResultMsg{
				sourceID:    sourceID,
				targetID:    targetID,
				stage:       "merge",
				state:       pending.State,
				operationID: pending.OperationID,
			}
		}
		return mergeResultMsg{sourceID: sourceID, targetID: targetID, result: &result.Result, err: err}
	}
}

func shouldStopBeforeFollowOnMerge(state domain.SessionState) bool {
	return state == domain.SessionBusy || state == domain.SessionWaiting
}

func (m Model) followOnMergeIntoTargetCmd(sourceWorktree, targetWorktree, sourceID, targetID string, targetState domain.SessionState) tea.Cmd {
	return func() tea.Msg {
		if shouldStopBeforeFollowOnMerge(targetState) {
			ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
			defer cancel()
			if m.daemonClient == nil {
				return mergeResultMsg{sourceID: sourceID, targetID: targetID, err: fmt.Errorf("daemon client unavailable")}
			}
			m.sessionMonitor.Stop(targetID)
			if _, err := m.daemonClient.StopSession(ctx, targetID); err != nil {
				if pending, ok := pendingOperationDetails(err); ok {
					return mergeResultMsg{
						sourceID:    sourceID,
						targetID:    targetID,
						stage:       "stop_session",
						state:       pending.State,
						operationID: pending.OperationID,
					}
				}
				return mergeResultMsg{
					sourceID: sourceID,
					targetID: targetID,
					err:      fmt.Errorf("stop target session %s before merge: %w", targetID, err),
				}
			}
		}
		return m.mergeFeatureIntoFeatureCmd(sourceWorktree, targetWorktree, sourceID, targetID)()
	}
}

func (m Model) followOnMergeSelectionCmd(task *domain.Task, session *domain.Session) tea.Cmd {
	if task == nil {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No focused issue to merge",
			Expires: time.Now().Add(3 * time.Second),
		})
		return nil
	}
	targetState, targetStateKnown := projectedSessionState(session, m.sessionForIssue(task.ID))

	candidates := m.getFollowOnMergeCandidates(task)
	if len(candidates) == 0 {
		if task.ParentID == nil {
			return m.resolveMergeToMainCmd(task.ID)
		}
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No eligible upstream sources for follow-on merge; upstream sources must have an active session and be in progress or done",
			Expires: time.Now().Add(5 * time.Second),
		})
		return nil
	}

	if len(candidates) == 1 {
		m.logger.Info("follow-on merge selected",
			"sourceID", candidates[0].target.ID,
			"targetID", task.ID,
			"relation", candidates[0].relation,
		)
		return m.resolveFollowOnMergeCmd(candidates[0].target.ID, task.ID, targetState, targetStateKnown)
	}

	upstreamTargets := make([]overlay.MergeTarget, 0, len(candidates))
	for _, candidate := range candidates {
		upstreamTargets = append(upstreamTargets, candidate.target)
	}
	m.logger.Info("follow-on merge source picker opened", "targetID", task.ID, "candidateCount", len(upstreamTargets))
	return m.openOverlay(overlay.NewMergeSourceSelectOverlay(task, upstreamTargets, nil, nil))
}

func projectedSessionState(primary, fallback *domain.Session) (domain.SessionState, bool) {
	if primary != nil {
		return primary.State, true
	}
	if fallback != nil {
		return fallback.State, true
	}
	return domain.SessionIdle, false
}

func (m Model) resolveMergeToMainCmd(sourceID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		sourceWorktree, err := m.resolveIssueWorktreePath(ctx, sourceID)
		if err != nil || sourceWorktree == "" {
			return mergeResultMsg{
				sourceID: sourceID,
				targetID: "main",
				err:      fmt.Errorf("no active session/worktree - start session first"),
			}
		}
		return m.mergeToMainCmd(sourceWorktree, sourceID)()
	}
}

func (m Model) resolveFollowOnMergeCmd(sourceID, targetID string, targetState domain.SessionState, targetStateKnown bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		sourceWorktree, err := m.resolveIssueWorktreePath(ctx, sourceID)
		if err != nil || sourceWorktree == "" {
			return mergeResultMsg{
				sourceID: sourceID,
				targetID: targetID,
				err:      fmt.Errorf("selected upstream source has no active worktree"),
			}
		}

		targetWorktree, err := m.resolveIssueWorktreePath(ctx, targetID)
		if err != nil || targetWorktree == "" {
			return mergeResultMsg{
				sourceID: sourceID,
				targetID: targetID,
				err:      fmt.Errorf("no active session/worktree - start session first"),
			}
		}

		resolvedTargetState := targetState
		if !targetStateKnown {
			state, ok, err := m.resolveIssueSessionStateFromSnapshot(ctx, targetID)
			if err != nil {
				return mergeResultMsg{
					sourceID: sourceID,
					targetID: targetID,
					err:      fmt.Errorf("resolve target session state for %s: %w", targetID, err),
				}
			}
			if !ok {
				return mergeResultMsg{
					sourceID: sourceID,
					targetID: targetID,
					err:      fmt.Errorf("target session state unavailable for %s; refresh and retry", targetID),
				}
			}
			resolvedTargetState = state
		}

		return m.followOnMergeIntoTargetCmd(sourceWorktree, targetWorktree, sourceID, targetID, resolvedTargetState)()
	}
}

func (m Model) openMergeTargetSelection(task *domain.Task) tea.Cmd {
	if task == nil {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No focused issue to merge",
			Expires: time.Now().Add(3 * time.Second),
		})
		return nil
	}

	candidates := m.getMergeCandidates(task)
	mergeOverlay := overlay.NewMergeSelectOverlay(
		task,
		candidates,
		func(targetID string) tea.Cmd {
			return func() tea.Msg {
				return overlay.SelectionMsg{
					Key: "merge",
					Value: overlay.MergeTargetSelectedMsg{
						SourceID: task.ID,
						TargetID: targetID,
					},
				}
			}
		},
		func() tea.Cmd { return func() tea.Msg { return overlay.CloseOverlayMsg{} } },
	)
	return m.openOverlay(mergeOverlay)
}

func (m Model) sessionForIssue(issueID string) *domain.Session {
	if issueID == "" {
		return nil
	}
	for i := range m.tasks {
		if m.tasks[i].ID == issueID && m.tasks[i].Session != nil {
			return m.tasks[i].Session
		}
	}
	return nil
}

type followOnMergeCandidate struct {
	target   overlay.MergeTarget
	relation string
	order    int
}

func (m Model) getFollowOnMergeCandidates(target *domain.Task) []followOnMergeCandidate {
	if target == nil {
		return nil
	}

	candidates := make([]followOnMergeCandidate, 0, 4)
	seen := make(map[string]struct{}, 4)

	addCandidate := func(taskID, relation string, order int) {
		if taskID == "" {
			return
		}
		if _, ok := seen[taskID]; ok {
			return
		}
		for _, task := range m.tasks {
			if task.ID != taskID {
				continue
			}
			if !isEligibleUpstreamSource(task, relation) {
				return
			}
			hasWorktree := false
			if task.Session != nil && task.Session.Worktree != "" {
				hasWorktree = true
			} else if task.HasWorktree {
				hasWorktree = true
			}
			candidates = append(candidates, followOnMergeCandidate{
				target: overlay.MergeTarget{
					ID:          task.ID,
					Label:       task.Title,
					IsMain:      false,
					Status:      task.Status,
					HasWorktree: hasWorktree,
				},
				relation: relation,
				order:    order,
			})
			seen[taskID] = struct{}{}
			return
		}
	}

	if target.ParentID != nil {
		addCandidate(*target.ParentID, string(domain.DependencyParentChild), 0)
	}
	for _, dep := range target.Dependencies {
		switch dep.Type {
		case domain.DependencyBlocks, domain.DependencyBlockedBy:
			addCandidate(dep.ID, string(domain.DependencyBlocks), 1)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].order != candidates[j].order {
			return candidates[i].order < candidates[j].order
		}
		if statusPriority(candidates[i].target.Status) != statusPriority(candidates[j].target.Status) {
			return statusPriority(candidates[i].target.Status) < statusPriority(candidates[j].target.Status)
		}
		if candidates[i].target.Label != candidates[j].target.Label {
			return candidates[i].target.Label < candidates[j].target.Label
		}
		return candidates[i].target.ID < candidates[j].target.ID
	})

	return candidates
}

func isEligibleUpstreamSource(task domain.Task, relation string) bool {
	switch relation {
	case string(domain.DependencyParentChild), string(domain.DependencyBlocks):
		return task.Status == domain.StatusInProgress || task.Status == domain.StatusDone
	default:
		return false
	}
}

func statusPriority(status domain.Status) int {
	switch status {
	case domain.StatusInProgress:
		return 0
	case domain.StatusDone:
		return 1
	default:
		return 2
	}
}

type abortMergeResultMsg struct {
	worktree string
	err      error
}

// abortMergeCmd aborts an ongoing merge
func (m Model) abortMergeCmd(worktree string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		if m.daemonClient == nil {
			return abortMergeResultMsg{
				worktree: worktree,
				err:      fmt.Errorf("daemon client unavailable"),
			}
		}

		_, err := m.daemonClient.GitAbortMerge(ctx, worktree)
		return abortMergeResultMsg{
			worktree: worktree,
			err:      err,
		}
	}
}

// Bulk status commands

type bulkStatusResultMsg struct {
	updated int
	issues  []bulkTaskIssue
	failed  int
	err     error
}

type bulkTaskIssue struct {
	taskID string
	reason string
}

// bulkMoveStatusCmd moves tasks by delta (-1 = left, +1 = right)
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
				if m.tasks[i].ID == taskID {
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
		if m.tasks[i].ID == taskID {
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
			ParentID:        msg.ParentID,
		})
		return taskCreatedResultMsg{taskID: taskID, err: err, isUpdate: false}
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
		if m.tasks[i].ID == taskID {
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
	if len(m.pendingStatuses) == 0 && len(m.pendingOpsByTask) == 0 {
		return nil
	}
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
		if m.tasks[i].ID == taskID {
			task = &m.tasks[i]
			break
		}
	}
	if task == nil {
		return
	}

	workspace.SyncTask(*task, task.Session, m.tasks, m.pendingMutationForTask(taskID))
}

func (m *Model) applyPendingStatusOverlays() {
	if len(m.pendingStatuses) == 0 {
		return
	}
	for i := range m.tasks {
		key := taskIDKey(m.tasks[i].ID)
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
		taskByID[task.ID] = task
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
		if task.ID == parentID {
			continue
		}
		if task.ParentID != nil && *task.ParentID == parentID {
			children = append(children, task)
			continue
		}
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyParentChild && dep.ID == parentID {
				children = append(children, task)
				break
			}
		}
	}
	return children
}

type taskCreatedResultMsg struct {
	taskID   string
	err      error
	isUpdate bool
}

func (m Model) createTaskCmd(msg overlay.TaskCreatedMsg) tea.Cmd {
	return m.saveTaskCmd(msg)
}

// PR creation with overlay

type prCreatedResultMsg struct {
	url string
	err error
}

type openPROverlayResultMsg struct {
	branch  string
	issueID string
	err     error
}

// openPROverlayCmd gets the current branch and opens the PR creation overlay
func (m Model) openPROverlayCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		branch, err := m.resolveWorktreeBranch(ctx, worktree, issueID)
		if err != nil {
			return openPROverlayResultMsg{err: err}
		}
		return openPROverlayResultMsg{branch: branch, issueID: issueID}
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

func (m Model) listDaemonWorktrees(ctx context.Context) ([]git.Worktree, error) {
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

func (m Model) resolveIssueSessionStateFromSnapshot(ctx context.Context, issueID string) (domain.SessionState, bool, error) {
	if issueID == "" {
		return domain.SessionIdle, false, fmt.Errorf("issue ID is required")
	}
	if m.daemonClient == nil {
		return domain.SessionIdle, false, fmt.Errorf("daemon client unavailable")
	}

	snapshot, err := m.daemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return domain.SessionIdle, false, err
	}
	for _, task := range snapshot.Tasks {
		if task.ID != issueID {
			continue
		}
		if task.Session == nil {
			return domain.SessionIdle, false, nil
		}
		return task.Session.State, true, nil
	}
	return domain.SessionIdle, false, nil
}

func summarizeStatusChangeCounts(status git.GitStatus) string {
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
	if n := len(status.Untracked); n > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", n))
	}
	if len(parts) == 0 {
		return "working tree has changes"
	}
	return strings.Join(parts, ", ")
}

func dirtyFilesFromStatus(status git.GitStatus) []string {
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, len(status.Staged)+len(status.Modified)+len(status.Added)+len(status.Deleted)+len(status.Untracked))
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
	appendUnique(status.Untracked)
	sort.Strings(out)
	return out
}

func (m Model) checkMergePreflight(ctx context.Context, sourceID, targetID, sourceWorktree, targetWorktree string) *mergePreflightFailureMsg {
	if m.daemonClient == nil {
		return nil
	}

	reasons := make([]string, 0, 2)
	sourceFiles := make([]string, 0, 8)
	targetFiles := make([]string, 0, 8)
	sourceStatus, sourceErr := m.daemonClient.GitStatus(ctx, sourceWorktree)
	if sourceErr != nil {
		reasons = append(reasons, fmt.Sprintf("Could not read source status (%s): %v", sourceID, sourceErr))
	} else if sourceStatus.HasChanges {
		reasons = append(reasons, fmt.Sprintf("Source %s is not clean: %s", sourceID, summarizeStatusChangeCounts(sourceStatus)))
		sourceFiles = dirtyFilesFromStatus(sourceStatus)
	}

	targetStatus, targetErr := m.daemonClient.GitStatus(ctx, targetWorktree)
	if targetErr != nil {
		reasons = append(reasons, fmt.Sprintf("Could not read target status (%s): %v", targetID, targetErr))
	} else if targetStatus.HasChanges {
		reasons = append(reasons, fmt.Sprintf("Target %s is not clean: %s", targetID, summarizeStatusChangeCounts(targetStatus)))
		targetFiles = dirtyFilesFromStatus(targetStatus)
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

func runGitCommand(ctx context.Context, worktree string, args ...string) (string, error) {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return "", fmt.Errorf("worktree is required")
	}
	fullArgs := make([]string, 0, len(args)+2)
	fullArgs = append(fullArgs, "-C", worktree)
	fullArgs = append(fullArgs, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, trimmed)
	}
	return strings.TrimSpace(string(output)), nil
}

func (m Model) discardChangesCmd(side, worktree string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		_, err := runGitCommandFunc(ctx, worktree, "restore", "--staged", "--worktree", ".")
		if err == nil {
			_, err = runGitCommandFunc(ctx, worktree, "clean", "-fd")
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

		if m.daemonClient != nil {
			if status, err := m.daemonClient.GitStatus(ctx, worktree); err == nil && !status.HasChanges {
				return mergePreflightActionResultMsg{
					action:   "commit",
					side:     side,
					worktree: worktree,
					err:      fmt.Errorf("no changes to commit"),
				}
			}
		}

		if _, err := runGitCommandFunc(ctx, worktree, "add", "-A"); err != nil {
			return mergePreflightActionResultMsg{
				action:   "commit",
				side:     side,
				worktree: worktree,
				err:      err,
			}
		}
		if _, err := runGitCommandFunc(ctx, worktree, "commit", "-m", "chore: pre-merge checkpoint"); err != nil {
			return mergePreflightActionResultMsg{
				action:   "commit",
				side:     side,
				worktree: worktree,
				err:      err,
			}
		}
		return mergePreflightActionResultMsg{
			action:   "commit",
			side:     side,
			worktree: worktree,
		}
	}
}

func findDaemonWorktree(worktrees []git.Worktree, worktreePath, issueID string) (git.Worktree, bool) {
	for _, wt := range worktrees {
		if worktreePath != "" && wt.Path == worktreePath {
			return wt, true
		}
		if issueID != "" && strings.EqualFold(wt.IssueID, issueID) {
			return wt, true
		}
	}
	return git.Worktree{}, false
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
			ID:          task.ID,
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
func (m Model) renderBoardView() string {
	// Build columns for the board
	columns := m.buildColumns()
	if len(columns) == 0 {
		return ""
	}
	visibleStart, visibleEnd := m.boardVisibleColumnRange(columns)
	visibleColumns := columns[visibleStart:visibleEnd]

	// Create cursor for board package using computed position
	pos := m.nav.GetPosition(columns)
	localColumn := pos.Column - visibleStart
	cursor := board.Cursor{
		Column: localColumn,
		Task:   pos.Task,
	}
	if localColumn < 0 || localColumn >= len(visibleColumns) {
		cursor.Column = -1
	}
	activeViewportStart := 0
	if pos.Column >= 0 && pos.Column < len(m.viewportStarts) {
		activeViewportStart = m.viewportStarts[pos.Column]
	}

	// Compute phase data if showPhases is enabled
	phaseData := make(map[string]phases.TaskPhaseInfo)
	if m.editor.GetShowPhases() {
		phaseData = m.computePhases()
	}

	contentHeight := board.BoardContentHeight(m.height)
	toolbar := ""
	if m.isDrillDownActive() {
		toolbar = m.renderDrillDownToolbar()
		contentHeight -= lipgloss.Height(toolbar) + 1
	}
	if contentHeight < 6 {
		contentHeight = 6
	}

	boardView := board.Render(
		visibleColumns,
		cursor,
		m.editor.GetSelectedTasks(),
		m.runtimeSignalsForBoard(),
		board.BuildChildProgress(m.tasks),
		phaseData,
		m.editor.GetShowPhases(),
		activeViewportStart,
		m.styles,
		m.width,
		contentHeight,
	)
	if toolbar == "" {
		return boardView
	}
	parts := make([]string, 0, 2)
	if toolbar != "" {
		parts = append(parts, toolbar)
	}
	parts = append(parts, boardView)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) runtimeSignalsForBoard() map[string]board.RuntimeSignals {
	if len(m.pendingStatuses) == 0 && len(m.pendingOpsByTask) == 0 {
		return m.runtimeSignalsByTask
	}

	signalsByTask := make(map[string]board.RuntimeSignals, len(m.runtimeSignalsByTask)+len(m.pendingStatuses)+len(m.pendingOpsByTask))
	for taskID, signals := range m.runtimeSignalsByTask {
		signalsByTask[taskID] = signals
	}
	for _, task := range m.tasks {
		pending, ok := m.pendingStatuses[taskIDKey(task.ID)]
		if !ok {
			continue
		}
		signals := signalsByTask[task.ID]
		signals.PendingOperationState = string(pending.state)
		signals.PendingOperationID = pending.operationID
		signalsByTask[task.ID] = signals
	}
	for _, task := range m.tasks {
		pending, ok := m.pendingOpsByTask[taskIDKey(task.ID)]
		if !ok {
			continue
		}
		signals := signalsByTask[task.ID]
		signals.PendingOperationState = string(pending.state)
		signals.PendingOperationID = pending.operationID
		signals.PendingOperationPercent = pending.percent
		signalsByTask[task.ID] = signals
	}

	return signalsByTask
}

// renderCompactView renders the compact list view
func (m Model) renderCompactView() string {
	// Get all filtered and sorted tasks
	filteredTasks := m.boardVisibleTasks(m.tasks)
	sortedTasks := m.editor.ApplySort(filteredTasks)

	// Create compact view
	compactView := compact.NewCompactView(sortedTasks, m.width, board.BoardContentHeight(m.height))

	// Set cursor position based on current navigation
	// In compact mode, we use the flat task index
	columns := m.buildColumns()
	pos := m.nav.GetPosition(columns)
	flatIndex := m.getFlatIndexFromPosition(pos, columns)
	compactView.SetCursor(flatIndex)

	// Set selected tasks
	compactView.SetSelected(m.editor.GetSelectedTasks())

	return compactView.Render()
}

// getFlatIndexFromPosition converts a column/task position to a flat index
func (m Model) getFlatIndexFromPosition(pos navigation.Position, columns []board.Column) int {
	index := 0
	for i := 0; i < pos.Column && i < len(columns); i++ {
		index += len(columns[i].Tasks)
	}
	if pos.Column < len(columns) {
		index += pos.Task
	}
	return index
}

func (m Model) isDrillDownActive() bool {
	return strings.TrimSpace(m.drillDownParentID) != ""
}

func (m *Model) enterDrillDown(parentID, parentName string) {
	id := strings.TrimSpace(parentID)
	if id == "" {
		return
	}
	if m.isDrillDownActive() {
		m.drillDownTrail = append(m.drillDownTrail, drillDownContext{
			parentID:   strings.TrimSpace(m.drillDownParentID),
			parentName: strings.TrimSpace(m.drillDownParentName),
		})
	}
	m.drillDownParentID = id
	m.drillDownParentName = strings.TrimSpace(parentName)
}

func (m *Model) exitCurrentDrillDown() string {
	exitedParentID := strings.TrimSpace(m.drillDownParentID)
	if len(m.drillDownTrail) == 0 {
		m.clearDrillDown()
		return exitedParentID
	}

	prev := m.drillDownTrail[len(m.drillDownTrail)-1]
	m.drillDownTrail = m.drillDownTrail[:len(m.drillDownTrail)-1]
	m.drillDownParentID = prev.parentID
	m.drillDownParentName = prev.parentName
	return exitedParentID
}

func (m *Model) clearDrillDown() {
	m.drillDownParentID = ""
	m.drillDownParentName = ""
	m.drillDownTrail = nil
}

func (m Model) renderDrillDownToolbar() string {
	parentID := strings.TrimSpace(m.drillDownParentID)
	parentName := strings.TrimSpace(m.drillDownParentName)
	target := parentID
	if parentName != "" {
		target = fmt.Sprintf("%s %s", parentID, parentName)
	}
	left := m.styles.OverlayTitle.Render("Drill-down")
	body := m.styles.MenuItem.Render("Children of " + target)
	right := m.styles.StatusHint.Render("Esc: back to board  Space: details+actions")
	return lipgloss.JoinHorizontal(lipgloss.Left, left+"  ", body+"  ", right)
}

// openOrchestrationOverlay creates and opens the orchestration overlay
func (m Model) openOrchestrationOverlay() tea.Cmd {
	// Gather session information
	var sessions []overlay.SessionInfo
	for _, task := range m.tasks {
		if task.Session != nil {
			sessions = append(sessions, overlay.SessionInfo{
				IssueID:      task.ID,
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
					err := m.daemonClient.DeleteTask(ctx, task.ID)
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
					err := m.daemonClient.ArchiveTask(ctx, task.ID)
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
			removed, err := m.daemonClient.CleanupOrphanedWorktrees(ctx)
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
