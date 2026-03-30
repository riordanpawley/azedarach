package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// ConfirmDialog is a confirmation dialog overlay with Yes/No options
type ConfirmDialog struct {
	twoPaneDialogChrome
	title          string
	message        string
	styles         *Styles
	selected       bool // true = Yes, false = No
	viewportWidth  int
	viewportHeight int
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

		case "n", "N", "esc":
			// No or Escape - cancel and close
			return c, func() tea.Msg {
				return SelectionMsg{
					Key:   "no",
					Value: ConfirmResult{Confirmed: false},
				}
			}

		case "enter":
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
		if msg.Width > 0 {
			c.viewportWidth = msg.Width
		}
		if msg.Height > 0 {
			c.viewportHeight = msg.Height
		}
	}

	return c, nil
}

// View renders the dialog
func (c *ConfirmDialog) View() string {
	width, height := clampDialogSize(60, 12, c.viewportWidth, c.viewportHeight)
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
			return renderDialogActions(c.styles, []keybinds.Binding{
				{Key: "←/→/Tab", Description: "switch"},
				{Key: "Enter", Description: "confirm"},
				{Key: "Esc", Description: "cancel"},
			})
		},
	})
}

// Title returns the dialog title
func (c *ConfirmDialog) Title() string {
	return c.title
}

// Size returns the dialog dimensions
func (c *ConfirmDialog) Size() (width, height int) {
	// Width: enough for message and buttons
	// Height: message + buttons + footer + padding
	messageLines := len(strings.Split(c.message, "\n"))
	return 60, messageLines + 6
}
