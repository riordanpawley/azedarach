package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// ConfirmDialog is a confirmation dialog overlay with Yes/No options
type ConfirmDialog struct {
	twoPaneDialogChrome
	dialogViewportState
	title                string
	message              string
	styles               *Styles
	selected             bool // true = Yes, false = No
	requireExplicitYNKey bool
}

// ConfirmResult represents the result of a confirmation dialog
type ConfirmResult struct {
	Confirmed bool
}

// NewConfirmDialog creates a new confirmation dialog with the given title and message
func NewConfirmDialog(title, message string) *ConfirmDialog {
	return &ConfirmDialog{
		title:    title,
		message:  message,
		styles:   New(),
		selected: false, // Default to No
	}
}

// NewConfirmDialogExplicitYN creates a confirmation dialog that only accepts Y/N.
func NewConfirmDialogExplicitYN(title, message string) *ConfirmDialog {
	dialog := NewConfirmDialog(title, message)
	dialog.requireExplicitYNKey = true
	return dialog
}

// Init initializes the dialog
func (c *ConfirmDialog) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (c *ConfirmDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "Y":
			// Yes - confirm and close
			return c, func() tea.Msg {
				return SelectionMsg{
					Key:   "yes",
					Value: ConfirmResult{Confirmed: true},
				}
			}

		case "n", "N":
			// No - cancel and close
			return c, func() tea.Msg {
				return SelectionMsg{
					Key:   "no",
					Value: ConfirmResult{Confirmed: false},
				}
			}

		case "esc":
			if c.requireExplicitYNKey {
				return c, nil
			}
			// Escape - cancel and close
			return c, func() tea.Msg {
				return SelectionMsg{
					Key:   "no",
					Value: ConfirmResult{Confirmed: false},
				}
			}

		case "enter":
			if c.requireExplicitYNKey {
				return c, nil
			}
			// Confirm current selection
			return c, func() tea.Msg {
				return SelectionMsg{
					Key:   map[bool]string{true: "yes", false: "no"}[c.selected],
					Value: ConfirmResult{Confirmed: c.selected},
				}
			}

		case "left", "h":
			// Move to No
			c.selected = false
			return c, nil

		case "right", "l", "tab":
			// Move to Yes
			c.selected = true
			return c, nil
		}
	case tea.WindowSizeMsg:
		c.ApplyWindowSize(msg)
	}

	return c, nil
}

// View renders the dialog
func (c *ConfirmDialog) View() string {
	width, height := c.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            c.styles,
		width:             width,
		height:            height,
		title:             strings.ToUpper(c.title),
		rightSectionTitle: "Actions",
		breakpoint:        70,
		gap:               3,
		minLeft:           28,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			var b strings.Builder
			if c.message != "" {
				b.WriteString(c.styles.MenuItem.Render(c.message))
				b.WriteString("\n\n")
			}
			yesStyle := c.styles.MenuItem
			noStyle := c.styles.MenuItem
			if c.selected {
				yesStyle = c.styles.MenuItemActive
			} else {
				noStyle = c.styles.MenuItemActive
			}
			b.WriteString(yesStyle.Render("[Y] Yes"))
			b.WriteString("\n")
			b.WriteString(noStyle.Render("[N] No"))
			return strings.TrimRight(b.String(), "\n")
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			bindings := []keybinds.Binding{
				{Key: "←/→/Tab", Description: "switch"},
				{Key: "Y", Description: "confirm"},
				{Key: "N", Description: "cancel"},
			}
			if !c.requireExplicitYNKey {
				bindings = append(bindings,
					keybinds.Binding{Key: "Enter", Description: "confirm"},
					keybinds.Binding{Key: "Esc", Description: "cancel"},
				)
			}
			return renderDialogActions(c.styles, bindings)
		},
	})
}

// Title returns the dialog title
func (c *ConfirmDialog) Title() string {
	return c.title
}

// Size returns the dialog dimensions
func (c *ConfirmDialog) Size() (width, height int) {
	messageLines := len(strings.Split(c.message, "\n"))
	return c.Clamp(60, max(12, messageLines+11))
}
