// Package app contains the main application model and TEA implementation.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/beads"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
	"github.com/riordanpawley/azedarach/internal/services/editor"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
	"github.com/riordanpawley/azedarach/internal/services/navigation"
	"github.com/riordanpawley/azedarach/internal/services/network"
	"github.com/riordanpawley/azedarach/internal/services/pr"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/compact"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/statusbar"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
	"github.com/riordanpawley/azedarach/internal/ui/toast"
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

// Re-export Toast type and constants for convenience
type Toast = types.Toast
type ToastLevel = types.ToastLevel

const (
	ToastInfo    = types.ToastInfo
	ToastSuccess = types.ToastSuccess
	ToastWarning = types.ToastWarning
	ToastError   = types.ToastError
)

// tmuxAdapter adapts tmux.Client to satisfy monitor.TmuxClient interface
type tmuxAdapter struct {
	client *tmux.Client
}

func (a *tmuxAdapter) CapturePane(ctx context.Context, sessionName string) (string, error) {
	// Capture last 100 lines by default
	return a.client.CapturePane(ctx, sessionName, 100)
}

// Re-export navigation types for compatibility
type Position = navigation.Position

// ViewMode represents the current view mode
type ViewMode int

const (
	ViewModeBoard ViewMode = iota
	ViewModeCompact
)

// Model is the main application state
type Model struct {
	// Core data
	tasks    []domain.Task
	sessions map[string]*domain.Session

	// Navigation (using NavigationService)
	nav *navigation.Service

	// Editor state (mode, filter, sort, selections)
	editor *editor.Service

	// UI state
	overlayStack *overlay.Stack
	viewMode     ViewMode

	// Project
	currentProject string
	projects       []domain.Project

	// Toasts
	toasts []Toast

	// Terminal size
	width  int
	height int

	// Styles
	styles *styles.Styles

	// Configuration
	config *config.Config

	// Loading state
	loading     bool
	spinner     spinner.Model
	lastRefresh time.Time

	// Beads client
	beadsClient *beads.Client

	// Session management services
	tmuxClient      *tmux.Client
	worktreeManager *git.WorktreeManager
	sessionMonitor  *monitor.SessionMonitor
	portAllocator   *devserver.PortAllocator

	// Git services
	gitClient      *git.Client
	networkChecker *network.StatusChecker
	isOnline       bool

	// Project registry
	projectRegistry *config.ProjectsRegistry

	// Image attachment service
	attachmentService *attachment.Service

	// PR workflow service
	prWorkflow *pr.PRWorkflow

	// Dev server manager
	devServerManager *devserver.Manager

	// Diagnostics service
	diagnosticsService *diagnostics.Service

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

	// Initialize logger
	logger := slog.Default()

	// Initialize beads client
	beadsRunner := &beads.ExecRunner{}
	beadsClient := beads.NewClient(beadsRunner, logger)

	// Initialize tmux client
	tmuxRunner := &tmux.ExecRunner{}
	tmuxClient := tmux.NewClient(tmuxRunner, logger)

	// Initialize git worktree manager
	// Get current working directory as repo directory
	repoDir, err := os.Getwd()
	if err != nil {
		logger.Error("failed to get current directory", "error", err)
		repoDir = "."
	}
	gitRunner := git.NewExecRunner(repoDir)
	worktreeManager := git.NewWorktreeManager(gitRunner, repoDir, logger)

	// Initialize session monitor with tmux adapter
	adapter := &tmuxAdapter{client: tmuxClient}
	sessionMonitor := monitor.NewSessionMonitor(adapter)

	// Initialize port allocator (base port 3000)
	portAllocator := devserver.NewPortAllocator(3000)

	// Initialize git client (uses same runner as worktree manager)
	gitClient := git.NewClient(gitRunner, logger)

	// Initialize network checker
	networkChecker := network.NewStatusChecker()

	// Load project registry
	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		logger.Error("failed to load project registry", "error", err)
		// Continue with empty registry
		registry = &config.ProjectsRegistry{
			Projects:       []config.Project{},
			DefaultProject: "",
		}
	}

	// Initialize attachment service
	beadsPath := filepath.Join(repoDir, ".beads")
	attachmentSvc := attachment.NewService(beadsPath, logger)

	// Initialize PR workflow
	prRunner := &pr.ExecRunner{}
	prWorkflow := pr.NewPRWorkflow(prRunner, logger)

	// Initialize dev server manager
	devServerMgr := devserver.NewManager(portAllocator, logger)

	// Initialize diagnostics service
	diagService := diagnostics.NewService(tmuxClient, portAllocator, networkChecker)

	return Model{
		tasks:              []domain.Task{},
		sessions:           make(map[string]*domain.Session),
		nav:                navigation.NewService(),
		editor:             editor.NewService(),
		overlayStack:       overlay.NewStack(),
		viewMode:           ViewModeBoard, // Start with board view
		toasts:             []Toast{},
		styles:             styles.New(),
		config:             cfg,
		loading:            true, // Start with loading state
		spinner:            s,
		beadsClient:        beadsClient,
		tmuxClient:         tmuxClient,
		worktreeManager:    worktreeManager,
		sessionMonitor:     sessionMonitor,
		portAllocator:      portAllocator,
		gitClient:          gitClient,
		networkChecker:     networkChecker,
		projectRegistry:    registry,
		isOnline:           true, // Optimistically assume online
		attachmentService:  attachmentSvc,
		prWorkflow:         prWorkflow,
		devServerManager:   devServerMgr,
		diagnosticsService: diagService,
		logger:             logger,
		usePlaceholder:     false, // Use real data from beads
	}
}

// Init returns the initial command for the application
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.loadBeadsCmd(),
	)
}

// Update handles incoming messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
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
		m.overlayStack.Pop()
		return m, nil

	case overlay.SelectionMsg:
		return m.handleSelection(msg)

	case overlay.SearchMsg:
		m.editor.SetSearchQuery(msg.Query)
		return m, nil

	case beadsLoadedMsg:
		wasLoading := m.loading
		m.tasks = msg.tasks
		m.loading = false
		m.lastRefresh = time.Now()
		// Show success toast on first load
		if wasLoading {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastSuccess,
				Message: "Beads loaded",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		// Start periodic refresh
		return m, tickEvery(2 * time.Second)

	case beadsErrorMsg:
		m.toasts = append(m.toasts, Toast{
			Level:   ToastError,
			Message: msg.err.Error(),
			Expires: time.Now().Add(8 * time.Second),
		})
		m.loading = false
		// Still schedule a refresh to retry
		return m, tickEvery(5 * time.Second)

	case tickMsg:
		// Expire old toasts and refresh beads
		m.expireToasts()
		return m, tea.Batch(
			m.loadBeadsCmd(),
			tickEvery(2*time.Second),
		)

	case monitor.SessionStateMsg:
		// Update session state from monitor
		if session, ok := m.sessions[msg.BeadID]; ok {
			session.State = msg.State
			m.logger.Debug("session state updated", "beadID", msg.BeadID, "state", msg.State)
		}
		return m, nil

	case sessionStartedMsg:
		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Session started: %s", msg.beadID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case sessionErrorMsg:
		m.toasts = append(m.toasts, Toast{
			Level:   ToastError,
			Message: fmt.Sprintf("Session error: %s - %v", msg.beadID, msg.err),
			Expires: time.Now().Add(5 * time.Second),
		})
		return m, nil

	case network.StatusMsg:
		// Update online status
		m.isOnline = msg.Online
		m.logger.Debug("network status updated", "online", msg.Online)
		return m, nil

	case fetchAndMergeResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Merge failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		if msg.result.HasConflicts {
			// Show conflict dialog
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Merge conflicts in %d files", len(msg.result.ConflictFiles)),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.overlayStack.Push(overlay.NewConflictDialog(msg.result.ConflictFiles))
		}

		// Successful merge
		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: "Updated from main successfully",
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case createPRResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to get branch info: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		// Show PR command in toast
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Run: %s", msg.cmd),
			Expires: time.Now().Add(10 * time.Second),
		})
		return m, nil

	case showDiffResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to get diff: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		// Show abbreviated diff in toast
		diff := msg.diff
		if len(diff) > 200 {
			diff = diff[:200] + "..."
		}
		if diff == "" {
			diff = "No changes"
		}

		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: diff,
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case abortMergeResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to abort merge: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		m.toasts = append(m.toasts, Toast{
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
		return m, nil

	case overlay.ProjectSelectedMsg:
		// Close overlay
		m.overlayStack.Pop()

		// Switch to selected project
		m.currentProject = msg.Project.Name
		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Switched to project: %s", msg.Project.Name),
			Expires: time.Now().Add(3 * time.Second),
		})

		// Reload beads for new project
		return m, m.loadBeadsCmd()

	case overlay.TaskCreatedMsg:
		// Close overlay
		m.overlayStack.Pop()

		// Create task via beads client
		return m, m.createTaskCmd(msg)

	case taskCreatedResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to create task: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task created: %s", msg.taskID),
			Expires: time.Now().Add(3 * time.Second),
		})

		// Reload beads to show new task
		return m, m.loadBeadsCmd()

	// PR creation overlay messages
	case overlay.PRCreatedMsg:
		m.overlayStack.Pop()
		return m, m.createPRWithOverlayCmd(msg)

	case prCreatedResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to create PR: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("PR created: %s", msg.url),
			Expires: time.Now().Add(5 * time.Second),
		})
		return m, nil

	// Diff viewer messages
	case diff.LoadDiffMsg:
		// Route to diff viewer overlay if open
		if !m.overlayStack.IsEmpty() {
			if viewer, ok := m.overlayStack.Current().(*diff.DiffViewer); ok {
				newModel, cmd := viewer.Update(msg)
				// Update the overlay in the stack
				if newViewer, ok := newModel.(*diff.DiffViewer); ok {
					m.overlayStack.Pop()
					m.overlayStack.Push(newViewer)
				}
				return m, cmd
			}
		}
		return m, nil

	// Image attachment messages
	case overlay.AttachmentActionMsg:
		if msg.Action == "attached" {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Image attached: %s", msg.Attachment.Filename),
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		return m, nil

	// Cleanup executed result
	case overlay.CleanupExecutedMsg:
		m.overlayStack.Pop()
		if msg.Error != nil {
			m.toasts = append(m.toasts, Toast{
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

		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(5 * time.Second),
		})

		// Reload beads to reflect changes
		return m, m.loadBeadsCmd()

	// Image deleted from preview
	case overlay.ImageDeletedMsg:
		if msg.Error != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to delete image: %v", msg.Error),
				Expires: time.Now().Add(3 * time.Second),
			})
		} else {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastSuccess,
				Message: "Image deleted",
				Expires: time.Now().Add(2 * time.Second),
			})
		}

	// Open image preview overlay
	case overlay.OpenImagePreviewMsg:
		previewOverlay := overlay.NewImagePreviewOverlay(msg.BeadID, m.attachmentService, msg.InitialIndex)
		return m, tea.Batch(m.overlayStack.Push(previewOverlay), previewOverlay.Init())
		return m, nil
	// PR overlay open result
	case openPROverlayResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to get branch: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		// Open the PR creation overlay with branch info
		prOverlay := overlay.NewPRCreateOverlay(msg.branch, "main", msg.beadID)
		return m, tea.Batch(m.overlayStack.Push(prOverlay), prOverlay.Init())

	// Bulk action messages
	case overlay.BulkActionMsg:
		m.overlayStack.Pop()
		return m.handleBulkAction(msg)

	// Bulk status result
	case bulkStatusResultMsg:
		if msg.updated > 0 {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Updated %d tasks", msg.updated),
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		if msg.failed > 0 {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("%d tasks failed to update", msg.failed),
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		// Clear selection and return to normal mode after bulk action
		m.editor.ClearSelection()
		m.editor.EnterNormal()
		// Reload beads to reflect changes
		return m, m.loadBeadsCmd()

	// Single task status result
	case taskStatusResultMsg:
		if msg.err != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to update task: %v", msg.err),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task moved to %s", msg.newStatus),
			Expires: time.Now().Add(2 * time.Second),
		})
		// Reload beads to reflect changes
		return m, m.loadBeadsCmd()
	}

	return m, nil
}

// View renders the current state as a string
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	// Show loading spinner if loading
	if m.loading {
		return m.renderLoading()
	}

	// Render main view based on view mode
	var mainView string
	if m.viewMode == ViewModeCompact {
		mainView = m.renderCompactView()
	} else {
		mainView = m.renderBoardView()
	}

	// Render status bar
	sb := statusbar.New(m.editor.GetMode(), m.width, m.styles)
	statusBarView := sb.Render()

	// Compose the layout
	view := lipgloss.JoinVertical(lipgloss.Left, mainView, statusBarView)

	// If overlay is open, render it on top (centered)
	if !m.overlayStack.IsEmpty() {
		current := m.overlayStack.Current()
		overlayView := current.View()

		// Get overlay size for proper centering
		overlayWidth, overlayHeight := current.Size()

		// If width is 0, it means full width (like search bar)
		if overlayWidth == 0 {
			// Full-width overlay (search bar at bottom)
			view = lipgloss.JoinVertical(lipgloss.Left, view, overlayView)
		} else {
			// Centered modal overlay with border and title
			title := current.Title()
			if title != "" {
				titleView := m.styles.OverlayTitle.Render(title)
				overlayView = lipgloss.JoinVertical(lipgloss.Left, titleView, overlayView)
			}
			overlayView = m.styles.Overlay.
				Width(overlayWidth).
				Height(overlayHeight).
				Render(overlayView)

			// Center the overlay on screen
			centeredOverlay := lipgloss.Place(
				m.width,
				m.height,
				lipgloss.Center,
				lipgloss.Center,
				overlayView,
			)

			// Overlay on top of main view
			view = lipgloss.Place(
				m.width,
				m.height,
				lipgloss.Left,
				lipgloss.Top,
				view,
			)

			// Combine base and overlay
			// Note: This is a simple overlay - for true transparency we'd need
			// more complex rendering, but this works for modal overlays
			view = lipgloss.JoinVertical(lipgloss.Left, view, centeredOverlay)
		}
	}

	// Render toasts in bottom-right corner
	if len(m.toasts) > 0 {
		toastRenderer := toast.New(m.styles)
		toastView := toastRenderer.Render(m.toasts, m.width)
		if toastView != "" {
			// Overlay toasts on top of the main view
			view = lipgloss.JoinVertical(lipgloss.Left, view, toastView)
		}
	}

	return view
}

// buildColumns converts tasks into board columns, applying filter and sort
func (m Model) buildColumns() []board.Column {
	// For Phase 1, use placeholder data
	if m.usePlaceholder {
		return board.CreatePlaceholderData()
	}

	// Apply filter to tasks
	filteredTasks := m.editor.ApplyFilter(m.tasks)

	// Build columns from filtered tasks
	return []board.Column{
		{Title: "Open", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusOpen)},
		{Title: "In Progress", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusInProgress)},
		{Title: "Blocked", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusBlocked)},
		{Title: "Done", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusDone)},
	}
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
	}

	// Escape closes overlay or exits non-normal modes
	if msg.String() == "esc" {
		if !m.overlayStack.IsEmpty() {
			m.overlayStack.Pop()
			return m, nil
		}
		if !m.editor.IsNormal() {
			m.editor.EnterNormal()
			return m, nil
		}
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
	case "q":
		// Cleanup before quitting
		m.sessionMonitor.StopAll()
		return m, tea.Quit

	// Vertical navigation
	case "j", "down":
		m.nav.MoveDown(columns)
		return m, nil

	case "k", "up":
		m.nav.MoveUp(columns)
		return m, nil

	// Horizontal navigation
	case "h", "left":
		m.nav.MoveLeft(columns)
		return m, nil

	case "l", "right":
		m.nav.MoveRight(columns)
		return m, nil

	// Half-page scroll
	case "ctrl+d":
		m.nav.HalfPageDown(columns, m.halfPage())
		return m, nil

	case "ctrl+u":
		m.nav.HalfPageUp(columns, m.halfPage())
		return m, nil

	// Mode switches
	case "g":
		m.editor.EnterGoto()
		return m, nil

	case " ": // Space - open action menu
		task, session := m.getCurrentTaskAndSession()
		if task != nil {
			return m, m.overlayStack.Push(overlay.NewActionMenu(*task, session))
		}
		return m, nil

	case "/": // Search
		return m, m.overlayStack.Push(overlay.NewSearchOverlay())

	case "f": // Filter menu
		return m, m.overlayStack.Push(overlay.NewFilterMenu(m.editor.GetFilter()))

	case ",": // Sort menu
		return m, m.overlayStack.Push(overlay.NewSortMenu(m.editor.GetSort()))

	case "v": // Visual select
		m.editor.EnterSelect()
		return m, nil

	case "?": // Help
		return m, m.overlayStack.Push(overlay.NewHelpOverlay())

	case "enter": // View task details or drill into epic
		task, session := m.getCurrentTaskAndSession()
		if task != nil {
			if m.isCurrentTaskEpic() {
				// Epic drill-down
				children := m.getEpicChildren(task.ID)
				return m, m.overlayStack.Push(overlay.NewEpicDrillDown(*task, children))
			} else {
				// Regular task detail panel
				return m, m.overlayStack.Push(overlay.NewDetailPanel(*task, session))
			}
		}
		return m, nil

	case "c": // Create task
		return m, m.overlayStack.Push(overlay.NewCreateTaskOverlay())

	case "s": // Settings
		return m, m.overlayStack.Push(overlay.NewSettingsOverlayWithEditor(m.editor))

	case "D": // Diagnostics (Shift+D)
		diagPanel := overlay.NewDiagnosticsPanel(m.diagnosticsService, m.sessions)
		return m, tea.Batch(m.overlayStack.Push(diagPanel), diagPanel.Init())

	case "tab": // Toggle view mode
		if m.viewMode == ViewModeBoard {
			m.viewMode = ViewModeCompact
			m.toasts = append(m.toasts, Toast{
				Level:   ToastInfo,
				Message: "Switched to compact view",
				Expires: time.Now().Add(2 * time.Second),
			})
		} else {
			m.viewMode = ViewModeBoard
			m.toasts = append(m.toasts, Toast{
				Level:   ToastInfo,
				Message: "Switched to board view",
				Expires: time.Now().Add(2 * time.Second),
			})
		}
		return m, nil

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
		return m, m.overlayStack.Push(cleanupOverlay)
	}

	return m, nil
}

// handleGotoMode processes keyboard input in goto mode
func (m Model) handleGotoMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	columns := m.buildColumns()
	// Always return to normal mode after processing
	m.editor.EnterNormal()

	switch msg.String() {
	case "g":
		// Go to top of column
		m.nav.GotoTop(columns)
	case "e":
		// Go to end of column
		m.nav.GotoBottom(columns)
	case "h":
		// Go to first column
		m.nav.GotoFirstColumn(columns)
	case "l":
		// Go to last column
		m.nav.GotoLastColumn(columns)
	case "w":
		// Jump mode - quick navigation with labels for VISIBLE tasks only
		// Calculate visible tasks per column based on screen height
		// Card height is 6 lines (border + content), minus header and status bar
		cardHeight := 6
		availableHeight := m.height - 2 // status bar + column header
		visiblePerColumn := availableHeight / cardHeight
		if visiblePerColumn < 1 {
			visiblePerColumn = 1
		}

		// Count visible tasks (capped by actual task count per column)
		visibleCount := 0
		for _, col := range columns {
			colVisible := len(col.Tasks)
			if colVisible > visiblePerColumn {
				colVisible = visiblePerColumn
			}
			visibleCount += colVisible
		}
		return m, m.overlayStack.Push(overlay.NewJumpMode(visibleCount))
	case "p":
		// Project selector
		return m, m.overlayStack.Push(overlay.NewProjectSelector(m.projectRegistry))
	}

	return m, nil
}

// handleSearchMode processes keyboard input in search mode
func (m Model) handleSearchMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// TODO: Implement search mode handling
	// For now, just allow escape to exit
	return m, nil
}

// handleActionMode processes keyboard input in action mode
func (m Model) handleActionMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// TODO: Implement action mode handling
	// For now, just allow escape to exit
	return m, nil
}

// handleSelectMode processes keyboard input in select mode
func (m Model) handleSelectMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	columns := m.buildColumns()
	task, _ := m.getCurrentTaskAndSession()

	switch msg.String() {
	// Navigation with selection toggle
	case "j", "down":
		// Toggle current task selection, then move down
		if task != nil {
			m.editor.ToggleSelection(task.ID)
		}
		m.nav.MoveDown(columns)
		return m, nil

	case "k", "up":
		// Toggle current task selection, then move up
		if task != nil {
			m.editor.ToggleSelection(task.ID)
		}
		m.nav.MoveUp(columns)
		return m, nil

	// Horizontal movement (no selection toggle)
	case "h", "left":
		m.nav.MoveLeft(columns)
		return m, nil

	case "l", "right":
		m.nav.MoveRight(columns)
		return m, nil

	// Toggle selection without moving
	case " ":
		if task != nil {
			m.editor.ToggleSelection(task.ID)
		}
		return m, nil

	// Select all in current column
	case "a":
		status := m.nav.GetCurrentStatus(columns)
		for _, t := range m.tasks {
			if t.Status == status {
				m.editor.Select(t.ID)
			}
		}
		return m, nil

	// Select all visible tasks
	case "A":
		filteredTasks := m.editor.ApplyFilter(m.tasks)
		m.editor.SelectAll(filteredTasks)
		return m, nil

	// Clear selection
	case "x":
		m.editor.ClearSelection()
		return m, nil

	// Bulk action menu for selected tasks
	case "enter":
		if m.editor.HasSelection() {
			selectedIDs := m.editor.GetSelectedTasksList()
			return m, m.overlayStack.Push(overlay.NewBulkActionMenu(selectedIDs, len(selectedIDs)))
		}
		return m, nil

	// Exit select mode
	case "esc":
		m.editor.EnterNormal()
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

type beadsLoadedMsg struct {
	tasks []domain.Task
}

type beadsErrorMsg struct {
	err error
}

type tickMsg time.Time

type sessionStartedMsg struct {
	beadID       string
	worktreePath string
}

type sessionErrorMsg struct {
	beadID string
	err    error
}

// Commands

// loadBeadsCmd returns a command that fetches beads from the CLI
func (m Model) loadBeadsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		tasks, err := m.beadsClient.List(ctx)
		if err != nil {
			return beadsErrorMsg{err: err}
		}
		return beadsLoadedMsg{tasks: tasks}
	}
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// startSessionCmd creates a worktree, tmux session, and starts monitoring
func (m Model) startSessionCmd(beadID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Create worktree for the task
		baseBranch := "main" // TODO: Make configurable
		worktree, err := m.worktreeManager.Create(ctx, beadID, baseBranch)
		if err != nil {
			return sessionErrorMsg{beadID: beadID, err: fmt.Errorf("failed to create worktree: %w", err)}
		}

		// Create tmux session
		err = m.tmuxClient.NewSession(ctx, beadID, worktree.Path)
		if err != nil {
			return sessionErrorMsg{beadID: beadID, err: fmt.Errorf("failed to create tmux session: %w", err)}
		}

		// Send Claude command to session
		claudeCmd := "claude" // TODO: Make configurable or add more context
		err = m.tmuxClient.SendKeys(ctx, beadID, claudeCmd)
		if err != nil {
			return sessionErrorMsg{beadID: beadID, err: fmt.Errorf("failed to send keys: %w", err)}
		}

		// Create session record
		now := time.Now()
		session := &domain.Session{
			BeadID:    beadID,
			State:     domain.SessionBusy,
			StartedAt: &now,
			Worktree:  worktree.Path,
		}
		m.sessions[beadID] = session

		// Start monitoring the session
		// Note: We need a way to pass the tea.Program to the monitor
		// For now, we'll skip this and implement it properly later
		// m.sessionMonitor.Start(ctx, beadID, program)

		return sessionStartedMsg{beadID: beadID, worktreePath: worktree.Path}
	}
}

// stopSessionCmd stops the tmux session, monitoring, and optionally cleans up worktree
func (m Model) stopSessionCmd(beadID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Stop monitoring
		m.sessionMonitor.Stop(beadID)

		// Kill tmux session
		err := m.tmuxClient.KillSession(ctx, beadID)
		if err != nil {
			return sessionErrorMsg{beadID: beadID, err: fmt.Errorf("failed to kill tmux session: %w", err)}
		}

		// Remove session record
		delete(m.sessions, beadID)

		// Release port if allocated
		m.portAllocator.Release(beadID)

		// TODO: Optionally delete worktree (should probably ask user first)
		// err = m.worktreeManager.Delete(ctx, beadID)

		m.toasts = append(m.toasts, Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Session stopped: %s", beadID),
			Expires: time.Now().Add(3 * time.Second),
		})

		return nil
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
	return m.nav.GetCurrentTask(columns)
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
		m.toasts = append(m.toasts, Toast{
			Level:   ToastWarning,
			Message: fmt.Sprintf("Delete %d tasks (TODO)", count),
			Expires: time.Now().Add(3 * time.Second),
		})

	case "x": // Clear selection
		m.editor.ClearSelection()
		m.editor.EnterNormal()
		m.toasts = append(m.toasts, Toast{
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
	case "projects":
		// Settings -> Manage projects
		m.overlayStack.Pop() // Close settings
		return m, m.overlayStack.Push(overlay.NewProjectSelector(m.projectRegistry))
	case "editor-error":
		// Editor open error
		m.overlayStack.Pop()
		if err, ok := msg.Value.(error); ok {
			m.toasts = append(m.toasts, Toast{
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
		}
		return m, nil
	case "set-default-success", "remove-success", "detect-success":
		// Project registry actions succeeded - just show success toast
		if name, ok := msg.Value.(string); ok {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Project %s: %s", msg.Key[:len(msg.Key)-8], name), // Remove "-success"
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		return m, nil
	case "set-default-error", "remove-error", "add-error", "save-error", "detect-error":
		// Project registry actions failed
		if err, ok := msg.Value.(error); ok {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Error: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	}

	// Close the overlay first
	m.overlayStack.Pop()

	task, session := m.getCurrentTaskAndSession()
	if task == nil {
		return m, nil
	}

	// Handle the selection based on key
	switch msg.Key {
	// Session actions
	case "s":
		// Start session
		return m, m.startSessionCmd(task.ID)
	case "S":
		// TODO: Start session + work
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Start session + work (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	case "a":
		// Attach to session
		if session != nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastInfo,
				Message: fmt.Sprintf("Run: tmux attach-session -t %s", task.ID),
				Expires: time.Now().Add(5 * time.Second),
			})
		} else {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: "No active session for this task",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
	case "p":
		// TODO: Pause session
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Pause session (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	case "x":
		// Stop session
		if session != nil {
			return m, m.stopSessionCmd(task.ID)
		} else {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: "No active session for this task",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
	case "R":
		// TODO: Resume session
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Resume session (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})

	// Git actions
	case "u":
		// Update from main
		if session == nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		return m, m.fetchAndMergeCmd(session.Worktree, "main")

	case "m":
		// TODO: Merge to main (Phase 6)
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Merge to main (TODO - Phase 6)",
			Expires: time.Now().Add(3 * time.Second),
		})

	case "P":
		// Create PR (with overlay)
		if session == nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		// Get current branch name and open PR creation overlay
		return m, m.openPROverlayCmd(session.Worktree, task.ID)

	case "f":
		// Show diff viewer
		if session == nil {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: "No active session - start session first",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		// Open diff viewer overlay
		viewer := diff.NewDiffViewer(session.Worktree)
		cmd := m.overlayStack.Push(viewer)
		return m, tea.Batch(cmd, viewer.LoadDiff(context.Background(), m.gitClient))

	case "i":
		// Image attachments
		attachOverlay := overlay.NewImageAttachOverlay(task.ID, m.attachmentService)
		return m, tea.Batch(m.overlayStack.Push(attachOverlay), attachOverlay.Init())

	case "r":
		// Dev server menu
		servers := m.getDevServerInfo()
		devOverlay := overlay.NewDevServerOverlay(
			servers,
			task.ID,
			func(serverID string) tea.Cmd { return m.toggleDevServer(serverID) },
			func(serverID string) tea.Cmd { return m.viewDevServer(serverID) },
			func(serverID string) tea.Cmd { return m.restartDevServer(serverID) },
			func() tea.Cmd { return func() tea.Msg { return overlay.CloseOverlayMsg{} } },
		)
		return m, m.overlayStack.Push(devOverlay)

	case "b":
		// Merge bead into... (merge select mode)
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
		return m, m.overlayStack.Push(mergeOverlay)

	// Task actions
	case "h":
		// Move task left (to previous status)
		if task.Status == domain.StatusOpen {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: "Task is already in Open status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		return m, m.moveTaskStatusCmd(task.ID, -1)

	case "l":
		// Move task right (to next status)
		if task.Status == domain.StatusDone {
			m.toasts = append(m.toasts, Toast{
				Level:   ToastWarning,
				Message: "Task is already in Done status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		return m, m.moveTaskStatusCmd(task.ID, 1)
	case "e":
		// TODO: Edit task
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Edit task (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	case "d":
		// TODO: Delete task
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Delete task (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	}

	return m, nil
}

// NOTE: clampTaskIndex and clampTaskIndexForColumn have been removed.
// The ID-based Cursor now handles bounds clamping internally via
// MoveVertical, MoveHorizontal, and FindPosition methods.

// halfPage calculates half-page scroll distance based on terminal height
func (m Model) halfPage() int {
	// Approximate: subtract status bar (1) and header (2), divide by card height (~4 lines)
	visibleRows := m.height - 3
	if visibleRows < 4 {
		return 1
	}
	cardsPerColumn := visibleRows / 4
	half := cardsPerColumn / 2
	if half < 1 {
		return 1
	}
	return half
}

// renderLoading renders a centered loading spinner with message
func (m Model) renderLoading() string {
	content := lipgloss.JoinVertical(
		lipgloss.Center,
		m.spinner.View(),
		"Loading beads...",
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
	worktree string
	result   *git.MergeResult
	err      error
}

type createPRResultMsg struct {
	beadID string
	cmd    string
	err    error
}

type showDiffResultMsg struct {
	diff string
	err  error
}

// fetchAndMergeCmd fetches and merges from the specified branch
func (m Model) fetchAndMergeCmd(worktree, branch string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Fetch from origin
		if err := m.gitClient.Fetch(ctx, worktree, "origin"); err != nil {
			return fetchAndMergeResultMsg{
				worktree: worktree,
				err:      fmt.Errorf("fetch failed: %w", err),
			}
		}

		// Merge origin/branch
		result, err := m.gitClient.Merge(ctx, worktree, "origin/"+branch)
		return fetchAndMergeResultMsg{
			worktree: worktree,
			result:   result,
			err:      err,
		}
	}
}

// createPRCmd generates the gh pr create command
func (m Model) createPRCmd(worktree, beadID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Get current branch name
		branch, err := m.gitClient.CurrentBranch(ctx, worktree)
		if err != nil {
			return createPRResultMsg{
				beadID: beadID,
				err:    fmt.Errorf("failed to get current branch: %w", err),
			}
		}

		// Generate gh pr create command
		cmd := fmt.Sprintf("gh pr create --head %s --title \"[%s] ...\" --body \"...\"", branch, beadID)

		return createPRResultMsg{
			beadID: beadID,
			cmd:    cmd,
			err:    nil,
		}
	}
}

// showDiffCmd gets the diff stat for the worktree
func (m Model) showDiffCmd(worktree string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		diff, err := m.gitClient.DiffStat(ctx, worktree)
		if err != nil {
			return showDiffResultMsg{
				err: fmt.Errorf("failed to get diff: %w", err),
			}
		}

		return showDiffResultMsg{
			diff: diff,
			err:  nil,
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
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Open conflicted files in your editor at: %s", session.Worktree),
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case resolution.ResolveWithClaude:
		// Attach to tmux session for Claude to resolve
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Run: tmux attach-session -t %s (Claude can help resolve)", task.ID),
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	default:
		return m, nil
	}
}

// handleMergeTargetSelection handles merge target selection
func (m Model) handleMergeTargetSelection(msg overlay.MergeTargetSelectedMsg) (tea.Model, tea.Cmd) {
	// Close the overlay
	m.overlayStack.Pop()

	// TODO: Implement merge workflow in Phase 6
	m.toasts = append(m.toasts, Toast{
		Level:   ToastInfo,
		Message: fmt.Sprintf("Merge %s -> %s (TODO - Phase 6)", msg.SourceID, msg.TargetID),
		Expires: time.Now().Add(3 * time.Second),
	})

	return m, nil
}

type abortMergeResultMsg struct {
	worktree string
	err      error
}

// abortMergeCmd aborts an ongoing merge
func (m Model) abortMergeCmd(worktree string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		err := m.gitClient.AbortMerge(ctx, worktree)
		return abortMergeResultMsg{
			worktree: worktree,
			err:      err,
		}
	}
}

// Bulk status commands

type bulkStatusResultMsg struct {
	updated int
	failed  int
	err     error
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
				failed++
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
				failed++
				continue
			}

			// Calculate new status
			newIdx := currentIdx + delta
			if newIdx < 0 || newIdx >= len(statusOrder) {
				// Can't move beyond bounds
				continue
			}

			newStatus := statusOrder[newIdx]

			// Update via beads client
			err := m.beadsClient.Update(ctx, taskID, newStatus)
			if err != nil {
				failed++
				continue
			}

			updated++
		}

		return bulkStatusResultMsg{updated: updated, failed: failed}
	}
}

// bulkSetStatusCmd sets all selected tasks to a specific status
func (m Model) bulkSetStatusCmd(taskIDs []string, status domain.Status) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		updated := 0
		failed := 0

		for _, taskID := range taskIDs {
			err := m.beadsClient.Update(ctx, taskID, status)
			if err != nil {
				failed++
				continue
			}
			updated++
		}

		return bulkStatusResultMsg{updated: updated, failed: failed}
	}
}

// Single task status result
type taskStatusResultMsg struct {
	taskID    string
	newStatus domain.Status
	err       error
}

// moveTaskStatusCmd moves a single task's status by delta
func (m Model) moveTaskStatusCmd(taskID string, delta int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		statusOrder := []domain.Status{
			domain.StatusOpen,
			domain.StatusInProgress,
			domain.StatusBlocked,
			domain.StatusDone,
		}

		// Find the task to get current status
		var currentTask *domain.Task
		for i := range m.tasks {
			if m.tasks[i].ID == taskID {
				currentTask = &m.tasks[i]
				break
			}
		}

		if currentTask == nil {
			return taskStatusResultMsg{taskID: taskID, err: fmt.Errorf("task not found")}
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
			return taskStatusResultMsg{taskID: taskID, err: fmt.Errorf("invalid status")}
		}

		// Calculate new status
		newIdx := currentIdx + delta
		if newIdx < 0 || newIdx >= len(statusOrder) {
			return taskStatusResultMsg{taskID: taskID, err: fmt.Errorf("cannot move beyond status bounds")}
		}

		newStatus := statusOrder[newIdx]

		// Update via beads client
		err := m.beadsClient.Update(ctx, taskID, newStatus)
		if err != nil {
			return taskStatusResultMsg{taskID: taskID, err: err}
		}

		return taskStatusResultMsg{taskID: taskID, newStatus: newStatus}
	}
}

// Phase 6 helper methods

// isCurrentTaskEpic returns true if the currently selected task is an epic
func (m Model) isCurrentTaskEpic() bool {
	task, _ := m.getCurrentTaskAndSession()
	if task == nil {
		return false
	}
	return task.Type == domain.TypeEpic
}

// getEpicChildren returns all tasks that are children of the given epic
func (m Model) getEpicChildren(epicID string) []domain.Task {
	var children []domain.Task
	for _, task := range m.tasks {
		if task.ParentID != nil && *task.ParentID == epicID {
			children = append(children, task)
		}
	}
	return children
}

type taskCreatedResultMsg struct {
	taskID string
	err    error
}

// createTaskCmd creates a new task via the beads client
func (m Model) createTaskCmd(msg overlay.TaskCreatedMsg) tea.Cmd {
	return func() tea.Msg {
		// TODO: Implement beads.Client.Create() method
		// For now, return a placeholder success message
		// This will need to be implemented when the Create method is added to beads client

		// Expected implementation:
		// ctx := context.Background()
		// taskID, err := m.beadsClient.Create(ctx, beads.CreateTaskParams{
		//     Title:       msg.Title,
		//     Description: msg.Description,
		//     Type:        msg.Type,
		//     Priority:    msg.Priority,
		// })
		// if err != nil {
		//     return taskCreatedResultMsg{err: err}
		// }
		// return taskCreatedResultMsg{taskID: taskID}

		return taskCreatedResultMsg{
			err: fmt.Errorf("task creation not yet implemented - need to add Create() method to beads.Client"),
		}
	}
}

// PR creation with overlay

type prCreatedResultMsg struct {
	url string
	err error
}

type openPROverlayResultMsg struct {
	branch string
	beadID string
	err    error
}

// openPROverlayCmd gets the current branch and opens the PR creation overlay
func (m Model) openPROverlayCmd(worktree, beadID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		branch, err := m.gitClient.CurrentBranch(ctx, worktree)
		if err != nil {
			return openPROverlayResultMsg{err: err}
		}
		return openPROverlayResultMsg{branch: branch, beadID: beadID}
	}
}

// createPRWithOverlayCmd creates a PR using the pr workflow service
func (m Model) createPRWithOverlayCmd(msg overlay.PRCreatedMsg) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		result, err := m.prWorkflow.Create(ctx, pr.CreatePRParams{
			Title:      msg.Title,
			Body:       msg.Body,
			Branch:     msg.Branch,
			BaseBranch: msg.BaseBranch,
			Draft:      msg.Draft,
			BeadID:     msg.BeadID,
		})
		if err != nil {
			return prCreatedResultMsg{err: err}
		}

		return prCreatedResultMsg{url: result.URL}
	}
}

// Dev server helpers

func (m Model) getDevServerInfo() []overlay.DevServerInfo {
	if m.devServerManager == nil {
		return nil
	}

	servers := m.devServerManager.List()
	info := make([]overlay.DevServerInfo, 0, len(servers))
	for _, srv := range servers {
		info = append(info, overlay.DevServerInfo{
			ID:     srv.ID,
			Name:   srv.Name,
			Port:   srv.Port,
			Status: srv.Status,
			Uptime: srv.Uptime,
		})
	}
	return info
}

func (m Model) toggleDevServer(serverID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.devServerManager.Toggle(ctx, serverID); err != nil {
			return sessionErrorMsg{beadID: serverID, err: err}
		}
		return nil
	}
}

func (m Model) viewDevServer(serverID string) tea.Cmd {
	return func() tea.Msg {
		// For now, show a toast with instructions
		return Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Run: tmux attach-session -t devserver-%s", serverID),
			Expires: time.Now().Add(5 * time.Second),
		}
	}
}

func (m Model) restartDevServer(serverID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.devServerManager.Restart(ctx, serverID); err != nil {
			return sessionErrorMsg{beadID: serverID, err: err}
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
		_, hasSession := m.sessions[task.ID]

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

	// Create cursor for board package using computed position
	pos := m.nav.GetPosition(columns)
	cursor := board.Cursor{
		Column: pos.Column,
		Task:   pos.Task,
	}

	// Compute phase data if showPhases is enabled
	phaseData := make(map[string]phases.TaskPhaseInfo)
	if m.editor.GetShowPhases() {
		phaseData = m.computePhases()
	}

	// Render board (takes full height minus 1 for statusbar)
	return board.Render(
		columns,
		cursor,
		m.editor.GetSelectedTasks(),
		phaseData,
		m.editor.GetShowPhases(),
		m.styles,
		m.width,
		m.height-1,
	)
}

// renderCompactView renders the compact list view
func (m Model) renderCompactView() string {
	// Get all filtered and sorted tasks
	filteredTasks := m.editor.ApplyFilter(m.tasks)
	sortedTasks := m.editor.ApplySort(filteredTasks)

	// Create compact view
	compactView := compact.NewCompactView(sortedTasks, m.width, m.height-1)

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

// openOrchestrationOverlay creates and opens the orchestration overlay
func (m Model) openOrchestrationOverlay() tea.Cmd {
	// Gather session information
	var sessions []overlay.SessionInfo
	for _, task := range m.tasks {
		if task.Session != nil {
			sessions = append(sessions, overlay.SessionInfo{
				BeadID:       task.ID,
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
		func(beadID string) tea.Cmd {
			return func() tea.Msg {
				// Show attach instructions
				return Toast{
					Level:   ToastInfo,
					Message: fmt.Sprintf("Run: tmux attach-session -t %s", beadID),
					Expires: time.Now().Add(5 * time.Second),
				}
			}
		},
		// onKill
		func(beadID string) tea.Cmd {
			return m.stopSessionCmd(beadID)
		},
		// onRefresh
		func() tea.Cmd {
			return m.loadBeadsCmd()
		},
	)

	return m.overlayStack.Push(orchOverlay)
}

// performCleanup executes cleanup operations for selected categories
func (m Model) performCleanup(ctx context.Context, categoryIDs []string) (overlay.CleanupResult, error) {
	result := overlay.CleanupResult{}

	for _, id := range categoryIDs {
		switch id {
		case "delete_old_done":
			// Delete completed tasks older than 30 days
			cutoff := time.Now().AddDate(0, 0, -30)
			deleted := 0
			for _, task := range m.tasks {
				if task.Status == domain.StatusDone && task.UpdatedAt.Before(cutoff) {
					// TODO: Implement task deletion via beads client
					// err := m.beadsClient.Delete(ctx, task.ID)
					// if err != nil {
					//     m.logger.Warn("failed to delete task", "id", task.ID, "error", err)
					//     continue
					// }
					deleted++
				}
			}
			result.Deleted = deleted

		case "archive_done":
			// Archive all done tasks
			archived := 0
			for _, task := range m.tasks {
				if task.Status == domain.StatusDone {
					// TODO: Implement task archival via beads client
					// err := m.beadsClient.Archive(ctx, task.ID)
					// if err != nil {
					//     m.logger.Warn("failed to archive task", "id", task.ID, "error", err)
					//     continue
					// }
					archived++
				}
			}
			result.Archived = archived

		case "remove_orphaned_worktrees":
			// Remove worktrees with no active sessions
			removed := 0
			// TODO: Implement worktree cleanup
			// List all worktrees, check if they have sessions, delete orphaned ones
			// worktrees, err := m.worktreeManager.List(ctx)
			// for _, wt := range worktrees {
			//     if _, hasSession := m.sessions[wt.BeadID]; !hasSession {
			//         err := m.worktreeManager.Delete(ctx, wt.BeadID)
			//         if err == nil {
			//             removed++
			//         }
			//     }
			// }
			result.WorktreesRemoved = removed

		case "clean_stale_sessions":
			// Clean sessions inactive for >24 hours
			cleaned := 0
			cutoff := time.Now().Add(-24 * time.Hour)
			for beadID, session := range m.sessions {
				if session.StartedAt != nil && session.StartedAt.Before(cutoff) {
					if session.State == domain.SessionIdle || session.State == domain.SessionPaused {
						// Stop and clean up stale session
						m.sessionMonitor.Stop(beadID)
						_ = m.tmuxClient.KillSession(ctx, beadID)
						delete(m.sessions, beadID)
						m.portAllocator.Release(beadID)
						cleaned++
					}
				}
			}
			result.SessionsCleaned = cleaned
		}
	}

	return result, nil
}
