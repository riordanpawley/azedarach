// Package app contains the main application model and TEA implementation.
package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// Mode represents the current editing mode (Helix-style modal editing)
type Mode int

const (
	ModeNormal Mode = iota
	ModeSelect
	ModeSearch
	ModeGoto
	ModeAction
)

// String returns the string representation of the mode
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "NORMAL"
	case ModeSelect:
		return "SELECT"
	case ModeSearch:
		return "SEARCH"
	case ModeGoto:
		return "GOTO"
	case ModeAction:
		return "ACTION"
	default:
		return "UNKNOWN"
	}
}

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
	return Model{
		tasks:          []domain.Task{},
		sessions:       make(map[string]*domain.Session),
		cursor:         Cursor{Column: 0, Task: 0},
		mode:           ModeNormal,
		statusFilter:   make(map[domain.Status]bool),
		priorityFilter: make(map[domain.Priority]bool),
		typeFilter:     make(map[domain.TaskType]bool),
		sessionFilter:  make(map[domain.SessionState]bool),
		sortBy:         SortBySession,
		toasts:         []Toast{},
		styles:         styles.New(),
		config:         cfg,
		loading:        true,
	}
}

// Init returns the initial command for the application
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		loadBeads,
		tickEvery(2 * time.Second),
	)
}

// Update handles incoming messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

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
	// TODO: Implement full view rendering
	// This is a placeholder that will be replaced with proper board rendering
	if m.loading {
		return "Loading..."
	}

	// Render board + status bar
	// If overlay open, render overlay on top
	return "Azedarach Go/Bubbletea - Press q to quit\n\n" +
		"Tasks loaded: " + string(rune('0'+len(m.tasks))) + "\n" +
		"Mode: Normal\n"
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
	switch msg.String() {
	case "q":
		return m, tea.Quit

	// Vertical navigation
	case "j", "down":
		col := m.currentColumn()
		if len(col) > 0 && m.cursor.Task < len(col)-1 {
			m.cursor.Task++
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
			m.cursor.Task = m.clampTaskIndex()
		}
		return m, nil

	case "l", "right":
		if m.cursor.Column < 3 {
			m.cursor.Column++
			m.cursor.Task = m.clampTaskIndex()
		}
		return m, nil

	// Half-page scroll
	case "ctrl+d":
		col := m.currentColumn()
		if len(col) > 0 {
			m.cursor.Task += m.halfPage()
			if m.cursor.Task >= len(col) {
				m.cursor.Task = len(col) - 1
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
	// Always return to normal mode after processing
	m.mode = ModeNormal

	switch msg.String() {
	case "g":
		// Go to top of column
		m.cursor.Task = 0
	case "e":
		// Go to end of column
		col := m.currentColumn()
		if len(col) > 0 {
			m.cursor.Task = len(col) - 1
		}
	case "h":
		// Go to first column
		m.cursor.Column = 0
		m.cursor.Task = m.clampTaskIndex()
	case "l":
		// Go to last column
		m.cursor.Column = 3
		m.cursor.Task = m.clampTaskIndex()
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
