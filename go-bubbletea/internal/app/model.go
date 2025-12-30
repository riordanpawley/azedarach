// Package app contains the main application model and TEA implementation.
package app

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
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

// Cursor tracks the current position in the Kanban board
type Cursor struct {
	Column int // 0=Open, 1=InProgress, 2=Blocked, 3=Done
	Task   int // Index within the column
}

// Model is the main application state
type Model struct {
	// Core data
	tasks    []domain.Task
	sessions map[string]*domain.Session

	// Navigation
	cursor Cursor
	mode   Mode

	// Selection (for multi-select mode)
	selectedTasks map[string]bool

	// UI state
	overlay    Overlay
	searchText string

	// Filters
	statusFilter   map[domain.Status]bool
	priorityFilter map[domain.Priority]bool
	typeFilter     map[domain.TaskType]bool
	sessionFilter  map[domain.SessionState]bool

	// Sorting
	sortBy SortField

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
	loading bool
	spinner spinner.Model

	// Use placeholder data in Phase 1
	usePlaceholder bool
}

// SortField defines how tasks are sorted
type SortField int

const (
	SortBySession SortField = iota
	SortByPriority
	SortByUpdated
)

// Toast represents a notification message
type Toast struct {
	Level   ToastLevel
	Message string
	Expires time.Time
}

// ToastLevel indicates the severity of a toast
type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastWarning
	ToastError
)

// Overlay represents the current modal overlay (if any)
type Overlay interface {
	Update(msg tea.Msg) (Overlay, tea.Cmd)
	View() string
}

// New creates a new application model with the given config
func New(cfg *config.Config) Model {
	// Initialize spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Blue)

	return Model{
		tasks:          []domain.Task{},
		sessions:       make(map[string]*domain.Session),
		cursor:         Cursor{Column: 0, Task: 0},
		mode:           ModeNormal,
		selectedTasks:  make(map[string]bool),
		statusFilter:   make(map[domain.Status]bool),
		priorityFilter: make(map[domain.Priority]bool),
		typeFilter:     make(map[domain.TaskType]bool),
		sessionFilter:  make(map[domain.SessionState]bool),
		sortBy:         SortBySession,
		toasts:         []Toast{},
		styles:         styles.New(),
		config:         cfg,
		loading:        false, // Start with placeholder data immediately
		spinner:        s,
		usePlaceholder: true, // Use placeholder data for Phase 1
	}
}

// Init returns the initial command for the application
func (m Model) Init() tea.Cmd {
	// For Phase 1, no async commands needed - we use placeholder data
	// Return spinner tick for future loading states
	return m.spinner.Tick
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
		// If overlay is open, route to overlay
		if m.overlay != nil {
			return m.updateOverlay(msg)
		}
		return m.handleKey(msg)

	case beadsLoadedMsg:
		m.tasks = msg.tasks
		m.loading = false
		return m, nil

	case beadsErrorMsg:
		m.toasts = append(m.toasts, Toast{
			Level:   ToastError,
			Message: msg.err.Error(),
			Expires: time.Now().Add(8 * time.Second),
		})
		m.loading = false
		return m, nil

	case tickMsg:
		// Periodic refresh
		return m, tea.Batch(
			loadBeads,
			tickEvery(2 * time.Second),
		)
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

	// Create cursor for board package
	cursor := board.Cursor{
		Column: m.cursor.Column,
		Task:   m.cursor.Task,
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

	// If overlay is open, render it on top (TODO: implement overlay rendering)
	if m.overlay != nil {
		// TODO: Center overlay on screen
		view = view + "\n" + m.overlay.View()
	}

	return view
}

// buildColumns converts tasks into board columns
func (m Model) buildColumns() []board.Column {
	// For Phase 1, use placeholder data
	if m.usePlaceholder {
		return board.CreatePlaceholderData()
	}

	// Build columns from actual tasks
	return []board.Column{
		{Title: "Open", Tasks: m.tasksInColumn(domain.StatusOpen)},
		{Title: "In Progress", Tasks: m.tasksInColumn(domain.StatusInProgress)},
		{Title: "Blocked", Tasks: m.tasksInColumn(domain.StatusBlocked)},
		{Title: "Done", Tasks: m.tasksInColumn(domain.StatusDone)},
	}
}

// handleKey processes keyboard input based on current mode
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys (work in any mode)
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "ctrl+l":
		// Force redraw
		return m, tea.ClearScreen
	}

	// Escape exits non-normal modes
	if msg.String() == "esc" && m.mode != ModeNormal {
		m.mode = ModeNormal
		return m, nil
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
		return m, tea.Quit

	// Vertical navigation
	case "j", "down":
		if m.cursor.Column < len(columns) {
			col := columns[m.cursor.Column]
			if len(col.Tasks) > 0 && m.cursor.Task < len(col.Tasks)-1 {
				m.cursor.Task++
			}
		}
		return m, nil

	case "k", "up":
		if m.cursor.Task > 0 {
			m.cursor.Task--
		}
		return m, nil

	// Horizontal navigation
	case "h", "left":
		if m.cursor.Column > 0 {
			m.cursor.Column--
			m.cursor.Task = m.clampTaskIndexForColumn(columns, m.cursor.Column)
		}
		return m, nil

	case "l", "right":
		if m.cursor.Column < len(columns)-1 {
			m.cursor.Column++
			m.cursor.Task = m.clampTaskIndexForColumn(columns, m.cursor.Column)
		}
		return m, nil

	// Half-page scroll
	case "ctrl+d":
		if m.cursor.Column < len(columns) {
			col := columns[m.cursor.Column]
			if len(col.Tasks) > 0 {
				m.cursor.Task += m.halfPage()
				if m.cursor.Task >= len(col.Tasks) {
					m.cursor.Task = len(col.Tasks) - 1
				}
			}
		}
		return m, nil

	case "ctrl+u":
		m.cursor.Task -= m.halfPage()
		if m.cursor.Task < 0 {
			m.cursor.Task = 0
		}
		return m, nil

	// Mode switches
	case "g":
		m.mode = ModeGoto
		return m, nil

	case " ": // Space - open action menu
		m.mode = ModeAction
		// TODO: Open action overlay
		return m, nil

	case "/": // Search
		m.mode = ModeSearch
		// TODO: Open search input
		return m, nil

	case "v": // Visual select
		m.mode = ModeSelect
		return m, nil

	case "?": // Help
		// TODO: Open help overlay
		return m, nil
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
		m.cursor.Task = 0
	case "e":
		// Go to end of column
		if m.cursor.Column < len(columns) {
			col := columns[m.cursor.Column]
			if len(col.Tasks) > 0 {
				m.cursor.Task = len(col.Tasks) - 1
			}
		}
	case "h":
		// Go to first column
		m.cursor.Column = 0
		m.cursor.Task = m.clampTaskIndexForColumn(columns, m.cursor.Column)
	case "l":
		// Go to last column
		m.cursor.Column = len(columns) - 1
		m.cursor.Task = m.clampTaskIndexForColumn(columns, m.cursor.Column)
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

// updateOverlay routes messages to the current overlay
func (m Model) updateOverlay(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "esc" {
		m.overlay = nil
		m.mode = ModeNormal
		return m, nil
	}

	var cmd tea.Cmd
	m.overlay, cmd = m.overlay.Update(msg)
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

// Commands

func loadBeads() tea.Msg {
	// TODO: Call beads.ListAll()
	// For now, return empty list
	return beadsLoadedMsg{tasks: []domain.Task{}}
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Helper methods

// currentColumn returns the tasks in the current column
func (m Model) currentColumn() []domain.Task {
	return m.tasksInColumn(m.columnStatus())
}

// columnStatus returns the status for the current column
func (m Model) columnStatus() domain.Status {
	statuses := []domain.Status{
		domain.StatusOpen,
		domain.StatusInProgress,
		domain.StatusBlocked,
		domain.StatusDone,
	}
	if m.cursor.Column < 0 || m.cursor.Column >= len(statuses) {
		return domain.StatusOpen
	}
	return statuses[m.cursor.Column]
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

// clampTaskIndex returns the task index clamped to the current column's bounds
func (m Model) clampTaskIndex() int {
	col := m.currentColumn()
	if len(col) == 0 {
		return 0
	}
	if m.cursor.Task < 0 {
		return 0
	}
	if m.cursor.Task >= len(col) {
		return len(col) - 1
	}
	return m.cursor.Task
}

// clampTaskIndexForColumn returns the task index clamped to a specific column's bounds
func (m Model) clampTaskIndexForColumn(columns []board.Column, colIndex int) int {
	if colIndex < 0 || colIndex >= len(columns) {
		return 0
	}
	col := columns[colIndex]
	if len(col.Tasks) == 0 {
		return 0
	}
	if m.cursor.Task < 0 {
		return 0
	}
	if m.cursor.Task >= len(col.Tasks) {
		return len(col.Tasks) - 1
	}
	return m.cursor.Task
}

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
