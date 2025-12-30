package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// filterMode represents the current selection mode
type filterMode string

const (
	filterModeNormal   filterMode = "normal"
	filterModeStatus   filterMode = "status"
	filterModePriority filterMode = "priority"
	filterModeType     filterMode = "type"
	filterModeSession  filterMode = "session"
)

// FilterMenu is a menu overlay for task filtering
type FilterMenu struct {
	filter *domain.Filter
	styles *Styles
	mode   filterMode
}

// NewFilterMenu creates a new filter menu for the given filter
func NewFilterMenu(filter *domain.Filter) *FilterMenu {
	return &FilterMenu{
		filter: filter,
		styles: New(),
		mode:   filterModeNormal,
	}
}

// Init initializes the menu
func (m *FilterMenu) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *FilterMenu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.mode {
		case filterModeNormal:
			return m.handleNormalMode(msg)
		case filterModeStatus:
			return m.handleStatusMode(msg)
		case filterModePriority:
			return m.handlePriorityMode(msg)
		case filterModeType:
			return m.handleTypeMode(msg)
		case filterModeSession:
			return m.handleSessionMode(msg)
		}
	}

	return m, nil
}

// handleNormalMode handles keys in normal mode
func (m *FilterMenu) handleNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return m, func() tea.Msg { return CloseOverlayMsg{} }

	case "s":
		m.mode = filterModeStatus
		return m, nil

	case "p":
		m.mode = filterModePriority
		return m, nil

	case "t":
		m.mode = filterModeType
		return m, nil

	case "S":
		m.mode = filterModeSession
		return m, nil

	case "e":
		m.filter.HideEpicChildren = !m.filter.HideEpicChildren
		return m, nil

	case "1":
		days := 1
		m.filter.AgeMaxDays = &days
		return m, nil

	case "7":
		days := 7
		m.filter.AgeMaxDays = &days
		return m, nil

	case "3":
		days := 30
		m.filter.AgeMaxDays = &days
		return m, nil

	case "0":
		m.filter.AgeMaxDays = nil
		return m, nil

	case "c":
		m.filter.Clear()
		return m, nil
	}

	return m, nil
}

// handleStatusMode handles keys in status selection mode
func (m *FilterMenu) handleStatusMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = filterModeNormal
		return m, nil

	case "o":
		m.filter.ToggleStatus(domain.StatusOpen)
		m.mode = filterModeNormal
		return m, nil

	case "i":
		m.filter.ToggleStatus(domain.StatusInProgress)
		m.mode = filterModeNormal
		return m, nil

	case "b":
		m.filter.ToggleStatus(domain.StatusBlocked)
		m.mode = filterModeNormal
		return m, nil

	case "d":
		m.filter.ToggleStatus(domain.StatusDone)
		m.mode = filterModeNormal
		return m, nil
	}

	return m, nil
}

// handlePriorityMode handles keys in priority selection mode
func (m *FilterMenu) handlePriorityMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = filterModeNormal
		return m, nil

	case "0":
		m.filter.TogglePriority(domain.P0)
		m.mode = filterModeNormal
		return m, nil

	case "1":
		m.filter.TogglePriority(domain.P1)
		m.mode = filterModeNormal
		return m, nil

	case "2":
		m.filter.TogglePriority(domain.P2)
		m.mode = filterModeNormal
		return m, nil

	case "3":
		m.filter.TogglePriority(domain.P3)
		m.mode = filterModeNormal
		return m, nil

	case "4":
		m.filter.TogglePriority(domain.P4)
		m.mode = filterModeNormal
		return m, nil
	}

	return m, nil
}

// handleTypeMode handles keys in type selection mode
func (m *FilterMenu) handleTypeMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = filterModeNormal
		return m, nil

	case "T":
		m.filter.ToggleType(domain.TypeTask)
		m.mode = filterModeNormal
		return m, nil

	case "B":
		m.filter.ToggleType(domain.TypeBug)
		m.mode = filterModeNormal
		return m, nil

	case "F":
		m.filter.ToggleType(domain.TypeFeature)
		m.mode = filterModeNormal
		return m, nil

	case "E":
		m.filter.ToggleType(domain.TypeEpic)
		m.mode = filterModeNormal
		return m, nil

	case "C":
		m.filter.ToggleType(domain.TypeChore)
		m.mode = filterModeNormal
		return m, nil
	}

	return m, nil
}

// handleSessionMode handles keys in session state selection mode
func (m *FilterMenu) handleSessionMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = filterModeNormal
		return m, nil

	case "I":
		m.filter.ToggleSessionState(domain.SessionIdle)
		m.mode = filterModeNormal
		return m, nil

	case "U":
		m.filter.ToggleSessionState(domain.SessionBusy)
		m.mode = filterModeNormal
		return m, nil

	case "W":
		m.filter.ToggleSessionState(domain.SessionWaiting)
		m.mode = filterModeNormal
		return m, nil

	case "D":
		m.filter.ToggleSessionState(domain.SessionDone)
		m.mode = filterModeNormal
		return m, nil

	case "X":
		m.filter.ToggleSessionState(domain.SessionError)
		m.mode = filterModeNormal
		return m, nil

	case "P":
		m.filter.ToggleSessionState(domain.SessionPaused)
		m.mode = filterModeNormal
		return m, nil
	}

	return m, nil
}

// View renders the menu
func (m *FilterMenu) View() string {
	var b strings.Builder

	// Status filter line
	b.WriteString(m.renderFilterLine("Status", "s", []filterOption{
		{key: "o", label: "Open", active: m.filter.Status[domain.StatusOpen]},
		{key: "i", label: "In Progress", active: m.filter.Status[domain.StatusInProgress]},
		{key: "b", label: "Blocked", active: m.filter.Status[domain.StatusBlocked]},
		{key: "d", label: "Done", active: m.filter.Status[domain.StatusDone]},
	}, m.mode == filterModeStatus))

	// Priority filter line
	b.WriteString(m.renderFilterLine("Priority", "p", []filterOption{
		{key: "0", label: "P0", active: m.filter.Priority[domain.P0]},
		{key: "1", label: "P1", active: m.filter.Priority[domain.P1]},
		{key: "2", label: "P2", active: m.filter.Priority[domain.P2]},
		{key: "3", label: "P3", active: m.filter.Priority[domain.P3]},
		{key: "4", label: "P4", active: m.filter.Priority[domain.P4]},
	}, m.mode == filterModePriority))

	// Type filter line
	b.WriteString(m.renderFilterLine("Type", "t", []filterOption{
		{key: "T", label: "Task", active: m.filter.Type[domain.TypeTask]},
		{key: "B", label: "Bug", active: m.filter.Type[domain.TypeBug]},
		{key: "F", label: "Feature", active: m.filter.Type[domain.TypeFeature]},
		{key: "E", label: "Epic", active: m.filter.Type[domain.TypeEpic]},
		{key: "C", label: "Chore", active: m.filter.Type[domain.TypeChore]},
	}, m.mode == filterModeType))

	// Session filter line
	b.WriteString(m.renderFilterLine("Session", "S", []filterOption{
		{key: "I", label: "Idle", active: m.filter.SessionState[domain.SessionIdle]},
		{key: "U", label: "Busy", active: m.filter.SessionState[domain.SessionBusy]},
		{key: "W", label: "Waiting", active: m.filter.SessionState[domain.SessionWaiting]},
		{key: "D", label: "Done", active: m.filter.SessionState[domain.SessionDone]},
		{key: "X", label: "Error", active: m.filter.SessionState[domain.SessionError]},
		{key: "P", label: "Paused", active: m.filter.SessionState[domain.SessionPaused]},
	}, m.mode == filterModeSession))

	// Separator
	b.WriteString(m.styles.Separator.Render("───────────────────────────────────────"))
	b.WriteString("\n")

	// Hide epic children checkbox
	checkbox := "[ ]"
	if m.filter.HideEpicChildren {
		checkbox = "[●]"
	}
	line := m.styles.MenuKey.Render("[e]") + " " +
		m.styles.MenuItem.Render(checkbox+" Hide epic children")
	b.WriteString(line)
	b.WriteString("\n")

	// Separator
	b.WriteString(m.styles.Separator.Render("───────────────────────────────────────"))
	b.WriteString("\n")

	// Age filter line
	b.WriteString(m.renderAgeFilter())

	// Separator
	b.WriteString(m.styles.Separator.Render("───────────────────────────────────────"))
	b.WriteString("\n")

	// Clear all
	line = m.styles.MenuKey.Render("[c]") + " " +
		m.styles.MenuItem.Render("Clear all filters")
	b.WriteString(line)
	b.WriteString("\n")

	// Footer hint based on mode
	if m.mode != filterModeNormal {
		hint := m.styles.Footer.Render("Press key to toggle filter, Esc to cancel")
		b.WriteString("\n")
		b.WriteString(hint)
	}

	return b.String()
}

// filterOption represents a single filter option
type filterOption struct {
	key    string
	label  string
	active bool
}

// renderFilterLine renders a filter category line
func (m *FilterMenu) renderFilterLine(category string, categoryKey string, options []filterOption, selecting bool) string {
	var b strings.Builder

	// Category with key hint
	keyStyle := m.styles.MenuKey
	if selecting {
		keyStyle = m.styles.MenuItemActive
	}
	b.WriteString(keyStyle.Render(fmt.Sprintf("[%s]", categoryKey)))
	b.WriteString(" ")
	b.WriteString(m.styles.MenuItem.Render(category + ":"))
	b.WriteString(" ")

	// Options
	for i, opt := range options {
		if i > 0 {
			b.WriteString(" ")
		}

		indicator := " "
		style := m.styles.MenuItem
		if opt.active {
			indicator = "●"
			style = m.styles.MenuItemActive
		}

		optStr := fmt.Sprintf("%s=%s", opt.key, opt.label)
		b.WriteString(style.Render(fmt.Sprintf("[%s%s]", indicator, optStr)))
	}

	b.WriteString("\n")
	return b.String()
}

// renderAgeFilter renders the age filter line
func (m *FilterMenu) renderAgeFilter() string {
	var b strings.Builder

	b.WriteString(m.styles.MenuItem.Render("Age:"))
	b.WriteString(" ")

	options := []struct {
		key   string
		label string
		days  *int
	}{
		{key: "1", label: "24h", days: intPtr(1)},
		{key: "7", label: "7d", days: intPtr(7)},
		{key: "3", label: "30d", days: intPtr(30)},
		{key: "0", label: "All", days: nil},
	}

	for i, opt := range options {
		if i > 0 {
			b.WriteString(" ")
		}

		active := false
		if opt.days == nil && m.filter.AgeMaxDays == nil {
			active = true
		} else if opt.days != nil && m.filter.AgeMaxDays != nil && *opt.days == *m.filter.AgeMaxDays {
			active = true
		}

		indicator := " "
		style := m.styles.MenuItem
		if active {
			indicator = "●"
			style = m.styles.MenuItemActive
		}

		optStr := fmt.Sprintf("%s=%s", opt.key, opt.label)
		b.WriteString(style.Render(fmt.Sprintf("[%s%s]", indicator, optStr)))
	}

	b.WriteString("\n")
	return b.String()
}

// Title returns the overlay title
func (m *FilterMenu) Title() string {
	return "Filter Tasks"
}

// Size returns the overlay dimensions
func (m *FilterMenu) Size() (width, height int) {
	// Width: enough for filter options
	// Height: 4 filter lines + 1 checkbox + 1 age + 1 clear + separators + padding
	return 56, 14
}

// intPtr returns a pointer to an int
func intPtr(i int) *int {
	return &i
}
