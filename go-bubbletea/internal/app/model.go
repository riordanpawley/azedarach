// Package app contains the main application model and TEA implementation.
package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui"
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

// Cursor tracks the current position in the Kanban board
type Cursor struct {
	Column int // 0=Open, 1=InProgress, 2=Blocked, 3=Done
	Task   int // Index within the column
}

// Model is the main application state
type Model struct {
	// Core data
	tasks    []domain.Task
	sessions map[string]domain.SessionState

	// Navigation
	cursor Cursor
	mode   Mode

	// UI state
	overlay    Overlay
	searchText string

	// Filters
	statusFilter   map[domain.Status]bool
	priorityFilter map[domain.Priority]bool
	typeFilter     map[domain.IssueType]bool
	sessionFilter  map[domain.State]bool

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
	styles ui.Styles

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

// NewModel creates a new application model with default state
func NewModel() Model {
	return Model{
		tasks:          []domain.Task{},
		sessions:       make(map[string]domain.SessionState),
		cursor:         Cursor{Column: 0, Task: 0},
		mode:           ModeNormal,
		statusFilter:   make(map[domain.Status]bool),
		priorityFilter: make(map[domain.Priority]bool),
		typeFilter:     make(map[domain.IssueType]bool),
		sessionFilter:  make(map[domain.State]bool),
		sortBy:         SortBySession,
		toasts:         []Toast{},
		styles:         ui.NewStyles(),
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

// handleKey processes keyboard input in normal mode
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "j", "down":
		m.cursor.Task++
		return m, nil

	case "k", "up":
		if m.cursor.Task > 0 {
			m.cursor.Task--
		}
		return m, nil

	case "h", "left":
		if m.cursor.Column > 0 {
			m.cursor.Column--
			m.cursor.Task = 0
		}
		return m, nil

	case "l", "right":
		if m.cursor.Column < 3 {
			m.cursor.Column++
			m.cursor.Task = 0
		}
		return m, nil

	case " ": // Space - open action menu
		m.mode = ModeAction
		// TODO: Open action overlay
		return m, nil

	case "/": // Search
		m.mode = ModeSearch
		// TODO: Open search input
		return m, nil

	case "?": // Help
		// TODO: Open help overlay
		return m, nil
	}

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
