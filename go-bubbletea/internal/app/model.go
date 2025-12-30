// Package app contains the main application model and TEA implementation.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/beads"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
	"github.com/riordanpawley/azedarach/internal/services/navigation"
	"github.com/riordanpawley/azedarach/internal/services/network"
	"github.com/riordanpawley/azedarach/internal/services/pr"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
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

// Model is the main application state
type Model struct {
	// Core data
	tasks    []domain.Task
	sessions map[string]*domain.Session

	// Navigation (using NavigationService)
	nav  *navigation.Service
	mode Mode

	// Selection (for multi-select mode)
	selectedTasks map[string]bool

	// UI state
	overlayStack *overlay.Stack

	// Filtering and sorting
	filter *domain.Filter
	sort   *domain.Sort

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

	return Model{
		tasks:           []domain.Task{},
		sessions:        make(map[string]*domain.Session),
		nav:             navigation.NewService(),
		mode:            ModeNormal,
		selectedTasks:   make(map[string]bool),
		overlayStack:    overlay.NewStack(),
		filter:          domain.NewFilter(),
		sort:            &domain.Sort{Field: domain.SortBySession, Order: domain.SortAsc},
		toasts:          []Toast{},
		styles:          styles.New(),
		config:          cfg,
		loading:         true, // Start with loading state
		spinner:         s,
		beadsClient:     beadsClient,
		tmuxClient:      tmuxClient,
		worktreeManager: worktreeManager,
		sessionMonitor:  sessionMonitor,
		portAllocator:   portAllocator,
		gitClient:       gitClient,
		networkChecker:  networkChecker,
		projectRegistry:   registry,
		isOnline:          true, // Optimistically assume online
		attachmentService: attachmentSvc,
		prWorkflow:        prWorkflow,
		devServerManager:  devServerMgr,
		logger:            logger,
		usePlaceholder:    false, // Use real data from beads
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
		m.filter.SearchQuery = msg.Query
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
			tickEvery(2 * time.Second),
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

	// Build columns for the board
	columns := m.buildColumns()

	// Create cursor for board package using computed position
	pos := m.nav.GetPosition(columns)
	cursor := board.Cursor{
		Column: pos.Column,
		Task:   pos.Task,
	}

	// Render board (takes full height minus 1 for statusbar)
	boardView := board.Render(
		columns,
		cursor,
		m.selectedTasks,
		m.styles,
		m.width,
		m.height-1,
	)

	// Render status bar
	sb := statusbar.New(m.mode, m.width, m.styles)
	statusBarView := sb.Render()

	// Compose the layout
	view := lipgloss.JoinVertical(lipgloss.Left, boardView, statusBarView)

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
	filteredTasks := m.filter.Apply(m.tasks)

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
		if m.mode != ModeNormal {
			m.mode = ModeNormal
			return m, nil
		}
	}

	// Mode-specific handling
	switch m.mode {
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
		m.mode = ModeGoto
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
		return m, m.overlayStack.Push(overlay.NewFilterMenu(m.filter))

	case ",": // Sort menu
		return m, m.overlayStack.Push(overlay.NewSortMenu(m.sort))

	case "v": // Visual select
		m.mode = ModeSelect
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
		return m, m.overlayStack.Push(overlay.NewDefaultSettingsOverlay())
	}

	return m, nil
}

// handleGotoMode processes keyboard input in goto mode
func (m Model) handleGotoMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	columns := m.buildColumns()
	// Always return to normal mode after processing
	m.mode = ModeNormal

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
		// Jump mode - quick navigation with labels
		taskCount := 0
		for _, col := range columns {
			taskCount += len(col.Tasks)
		}
		return m, m.overlayStack.Push(overlay.NewJumpMode(taskCount))
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
	// TODO: Implement select mode handling
	// For now, just allow escape to exit
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
	return m.sort.Apply(inColumn)
}

// getCurrentTaskAndSession returns the currently selected task and its session
func (m Model) getCurrentTaskAndSession() (*domain.Task, *domain.Session) {
	columns := m.buildColumns()
	return m.nav.GetCurrentTask(columns)
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
		// TODO: Move task left
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Move task left (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	case "l":
		// TODO: Move task right
		m.toasts = append(m.toasts, Toast{
			Level:   ToastInfo,
			Message: "Move task right (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
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
