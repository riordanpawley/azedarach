package overlay

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// TaskCreatedMsg is emitted when a new task is created
type TaskCreatedMsg struct {
	Title       string
	Description string
	Type        domain.TaskType
	Priority    domain.Priority
}

// CreateTaskOverlay provides a form to create a new task
type CreateTaskOverlay struct {
	title       textinput.Model
	description textarea.Model
	taskType    domain.TaskType
	priority    domain.Priority
	focusIndex  int
	styles      *Styles
}

const (
	focusTitle = iota
	focusDescription
	focusType
	focusPriority
	focusSubmit
)

// NewCreateTaskOverlay creates a new task creation overlay
func NewCreateTaskOverlay() *CreateTaskOverlay {
	// Initialize title input
	ti := textinput.New()
	ti.Placeholder = "Task title..."
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 60

	// Initialize description textarea
	ta := textarea.New()
	ta.Placeholder = "Task description (optional)..."
	ta.CharLimit = 2000
	ta.SetWidth(60)
	ta.SetHeight(5)

	return &CreateTaskOverlay{
		title:       ti,
		description: ta,
		taskType:    domain.TypeTask,
		priority:    domain.P2,
		focusIndex:  focusTitle,
		styles:      New(),
	}
}

// Init initializes the overlay
func (c *CreateTaskOverlay) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages
func (c *CreateTaskOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return c, func() tea.Msg { return CloseOverlayMsg{} }

		case "ctrl+s":
			// Submit the form
			return c, c.submit()

		case "tab", "shift+tab":
			// Tab through fields
			if msg.String() == "tab" {
				c.focusIndex = (c.focusIndex + 1) % 5
			} else {
				c.focusIndex = (c.focusIndex - 1 + 5) % 5
			}

			// Update focus
			if c.focusIndex == focusTitle {
				c.title.Focus()
				c.description.Blur()
			} else if c.focusIndex == focusDescription {
				c.title.Blur()
				c.description.Focus()
			} else {
				c.title.Blur()
				c.description.Blur()
			}

			return c, nil

		case "enter":
			// Submit if on submit button, otherwise handle in active field
			if c.focusIndex == focusSubmit {
				return c, c.submit()
			}
			// Let the active field handle enter
		}

		// Handle type selection when focused
		if c.focusIndex == focusType {
			switch msg.String() {
			case "T":
				c.taskType = domain.TypeTask
				return c, nil
			case "B":
				c.taskType = domain.TypeBug
				return c, nil
			case "F":
				c.taskType = domain.TypeFeature
				return c, nil
			case "E":
				c.taskType = domain.TypeEpic
				return c, nil
			case "C":
				c.taskType = domain.TypeChore
				return c, nil
			}
		}

		// Handle priority selection when focused
		if c.focusIndex == focusPriority {
			switch msg.String() {
			case "0":
				c.priority = domain.P0
				return c, nil
			case "1":
				c.priority = domain.P1
				return c, nil
			case "2":
				c.priority = domain.P2
				return c, nil
			case "3":
				c.priority = domain.P3
				return c, nil
			case "4":
				c.priority = domain.P4
				return c, nil
			}
		}
	}

	// Update active field
	var cmd tea.Cmd
	if c.focusIndex == focusTitle {
		c.title, cmd = c.title.Update(msg)
		cmds = append(cmds, cmd)
	} else if c.focusIndex == focusDescription {
		c.description, cmd = c.description.Update(msg)
		cmds = append(cmds, cmd)
	}

	return c, tea.Batch(cmds...)
}

// View renders the form
func (c *CreateTaskOverlay) View() string {
	var b strings.Builder

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(12).
		Align(lipgloss.Right)

	focusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	// Title field
	if c.focusIndex == focusTitle {
		b.WriteString(focusStyle.Render("Title:"))
	} else {
		b.WriteString(labelStyle.Render("Title:"))
	}
	b.WriteString("  ")
	b.WriteString(c.title.View())
	b.WriteString("\n\n")

	// Description field
	if c.focusIndex == focusDescription {
		b.WriteString(focusStyle.Render("Description:"))
	} else {
		b.WriteString(labelStyle.Render("Description:"))
	}
	b.WriteString("\n")
	b.WriteString(c.description.View())
	b.WriteString("\n\n")

	// Type selector
	if c.focusIndex == focusType {
		b.WriteString(focusStyle.Render("Type:"))
	} else {
		b.WriteString(labelStyle.Render("Type:"))
	}
	b.WriteString("  ")
	b.WriteString(c.renderTypeSelector())
	b.WriteString("\n\n")

	// Priority selector
	if c.focusIndex == focusPriority {
		b.WriteString(focusStyle.Render("Priority:"))
	} else {
		b.WriteString(labelStyle.Render("Priority:"))
	}
	b.WriteString("  ")
	b.WriteString(c.renderPrioritySelector())
	b.WriteString("\n\n")

	// Separator
	b.WriteString(c.styles.Separator.Render(strings.Repeat("─", 60)))
	b.WriteString("\n\n")

	// Submit button
	submitStyle := c.styles.MenuItem
	if c.focusIndex == focusSubmit {
		submitStyle = c.styles.MenuItemActive
	}
	b.WriteString(submitStyle.Render("[ Create Task ]"))
	b.WriteString("\n\n")

	// Footer hints
	hints := []string{
		c.styles.MenuKey.Render("Tab") + " " + c.styles.Footer.Render("Switch fields"),
		c.styles.MenuKey.Render("Ctrl+S") + " " + c.styles.Footer.Render("Submit"),
		c.styles.MenuKey.Render("Esc") + " " + c.styles.Footer.Render("Cancel"),
	}
	b.WriteString(c.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderTypeSelector renders the type selector with current selection
func (c *CreateTaskOverlay) renderTypeSelector() string {
	types := []struct {
		key  string
		typ  domain.TaskType
		name string
	}{
		{"T", domain.TypeTask, "Task"},
		{"B", domain.TypeBug, "Bug"},
		{"F", domain.TypeFeature, "Feature"},
		{"E", domain.TypeEpic, "Epic"},
		{"C", domain.TypeChore, "Chore"},
	}

	var parts []string
	for _, t := range types {
		style := c.styles.MenuItem
		indicator := " "
		if t.typ == c.taskType {
			style = c.styles.MenuItemActive
			indicator = "●"
		}

		parts = append(parts, style.Render(fmt.Sprintf("[%s%s]", indicator, t.key)))
	}

	return strings.Join(parts, " ")
}

// renderPrioritySelector renders the priority selector with current selection
func (c *CreateTaskOverlay) renderPrioritySelector() string {
	priorities := []struct {
		key string
		pri domain.Priority
	}{
		{"0", domain.P0},
		{"1", domain.P1},
		{"2", domain.P2},
		{"3", domain.P3},
		{"4", domain.P4},
	}

	var parts []string
	for _, p := range priorities {
		style := c.styles.MenuItem
		indicator := " "
		if p.pri == c.priority {
			style = c.styles.MenuItemActive
			indicator = "●"
		}

		parts = append(parts, style.Render(fmt.Sprintf("[%s%s]", indicator, p.key)))
	}

	return strings.Join(parts, " ")
}

// submit creates a TaskCreatedMsg and closes the overlay
func (c *CreateTaskOverlay) submit() tea.Cmd {
	// Validate title is not empty
	title := strings.TrimSpace(c.title.Value())
	if title == "" {
		return nil // Don't submit if title is empty
	}

	return tea.Batch(
		func() tea.Msg {
			return TaskCreatedMsg{
				Title:       title,
				Description: strings.TrimSpace(c.description.Value()),
				Type:        c.taskType,
				Priority:    c.priority,
			}
		},
		func() tea.Msg { return CloseOverlayMsg{} },
	)
}

// Title returns the overlay title
func (c *CreateTaskOverlay) Title() string {
	return "Create New Task"
}

// Size returns the overlay dimensions
func (c *CreateTaskOverlay) Size() (width, height int) {
	return 70, 25
}
