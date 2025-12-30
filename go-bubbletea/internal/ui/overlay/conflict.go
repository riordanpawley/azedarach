package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ConflictDialog displays merge conflicts and resolution options
type ConflictDialog struct {
	files  []string
	cursor int
	styles *Styles
}

// ConflictResolutionMsg is sent when the user chooses a resolution method
type ConflictResolutionMsg struct {
	ResolveWithClaude bool
	Abort             bool
	OpenManually      bool
}

// NewConflictDialog creates a new conflict resolution dialog
func NewConflictDialog(files []string) *ConflictDialog {
	return &ConflictDialog{
		files:  files,
		cursor: 0,
		styles: New(),
	}
}

// Init initializes the dialog
func (c *ConflictDialog) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (c *ConflictDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			// Escape closes without action
			return c, func() tea.Msg { return CloseOverlayMsg{} }

		case "c", "C":
			// Resolve with Claude
			return c, func() tea.Msg {
				return SelectionMsg{
					Key: "claude",
					Value: ConflictResolutionMsg{
						ResolveWithClaude: true,
					},
				}
			}

		case "a", "A":
			// Abort merge
			return c, func() tea.Msg {
				return SelectionMsg{
					Key: "abort",
					Value: ConflictResolutionMsg{
						Abort: true,
					},
				}
			}

		case "o", "O":
			// Open manually
			return c, func() tea.Msg {
				return SelectionMsg{
					Key: "manual",
					Value: ConflictResolutionMsg{
						OpenManually: true,
					},
				}
			}

		case "j", "down":
			if c.cursor < len(c.files)-1 {
				c.cursor++
			}
			return c, nil

		case "k", "up":
			if c.cursor > 0 {
				c.cursor--
			}
			return c, nil
		}
	}

	return c, nil
}

// View renders the dialog
func (c *ConflictDialog) View() string {
	var b strings.Builder

	// Header message
	header := c.styles.MenuItem.Bold(true).Render("Merge conflicts detected!")
	b.WriteString(header)
	b.WriteString("\n\n")

	// Conflicted files list
	if len(c.files) > 0 {
		filesLabel := c.styles.MenuItem.Foreground(c.styles.MenuKey.GetForeground()).Render("Conflicted files:")
		b.WriteString(filesLabel)
		b.WriteString("\n")

		for i, file := range c.files {
			prefix := "  "
			if i == c.cursor {
				prefix = "▸ "
			}
			line := prefix + c.styles.MenuItem.Render(file)
			if i == c.cursor {
				line = c.styles.MenuItemActive.Render(prefix + file)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Resolution options
	b.WriteString(c.styles.Separator.Render("───────────────────────────────"))
	b.WriteString("\n")

	options := []struct {
		key   string
		label string
		desc  string
	}{
		{"c", "Resolve with Claude", "Use Claude Code to resolve conflicts"},
		{"o", "Open manually", "Open files in your editor"},
		{"a", "Abort merge", "Cancel the merge operation"},
	}

	for _, opt := range options {
		line := c.styles.MenuKey.Render("["+opt.key+"]") + " " +
			c.styles.MenuItem.Bold(true).Render(opt.label) + " " +
			c.styles.Footer.Render("- "+opt.desc)
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Footer hint
	b.WriteString("\n")
	footer := c.styles.Footer.Render("j/k: Navigate • Esc: Close")
	b.WriteString(footer)

	return b.String()
}

// Title returns the dialog title
func (c *ConflictDialog) Title() string {
	return "Merge Conflicts"
}

// Size returns the dialog dimensions
func (c *ConflictDialog) Size() (width, height int) {
	// Width: enough for file paths and options
	// Height: header + files + options + footer + padding
	fileLines := len(c.files)
	if fileLines > 10 {
		fileLines = 10 // Cap at 10 visible files
	}
	return 70, 8 + fileLines + 5 // header(2) + files + separator(1) + options(3) + footer(2)
}
