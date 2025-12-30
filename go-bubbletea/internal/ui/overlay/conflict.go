package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// ConflictOverlay displays merge conflicts and resolution options
type ConflictOverlay struct {
	files                []string
	cursor               int
	onResolveWithClaude  func() tea.Cmd
	onAbort              func() tea.Cmd
	overlayStyles        *Styles
}

// ConflictResolutionMsg is sent when the user chooses a resolution method
type ConflictResolutionMsg struct {
	ResolveWithClaude bool
	Abort             bool
	OpenManually      bool
}

// NewConflictOverlay creates a new conflict resolution overlay
func NewConflictOverlay(
	files []string,
	onResolveWithClaude func() tea.Cmd,
	onAbort func() tea.Cmd,
) *ConflictOverlay {
	return &ConflictOverlay{
		files:               files,
		cursor:              0,
		onResolveWithClaude: onResolveWithClaude,
		onAbort:             onAbort,
		overlayStyles:       New(),
	}
}

// NewConflictDialog creates a new conflict resolution dialog (deprecated, use NewConflictOverlay)
func NewConflictDialog(files []string) *ConflictOverlay {
	return NewConflictOverlay(files, nil, nil)
}

// Init initializes the overlay
func (c *ConflictOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (c *ConflictOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			// Escape closes without action
			return c, func() tea.Msg { return CloseOverlayMsg{} }

		case "c", "C":
			// Resolve with Claude
			if c.onResolveWithClaude != nil {
				return c, c.onResolveWithClaude()
			}
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
			if c.onAbort != nil {
				return c, c.onAbort()
			}
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

// View renders the overlay
func (c *ConflictOverlay) View() string {
	var b strings.Builder

	// Header message with warning color
	headerStyle := lipgloss.NewStyle().
		Foreground(styles.Red).
		Bold(true)
	header := headerStyle.Render("⚠ Merge conflicts detected!")
	b.WriteString(header)
	b.WriteString("\n\n")

	// Conflicted files list
	if len(c.files) > 0 {
		filesLabel := lipgloss.NewStyle().
			Foreground(styles.Yellow).
			Bold(true).
			Render("Conflicted files:")
		b.WriteString(filesLabel)
		b.WriteString("\n")

		for i, file := range c.files {
			prefix := "  "
			fileStyle := c.overlayStyles.MenuItem
			if i == c.cursor {
				prefix = lipgloss.NewStyle().Foreground(styles.Blue).Render("▸ ")
				fileStyle = c.overlayStyles.MenuItemActive
			}
			line := prefix + fileStyle.Render(file)
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Resolution options
	b.WriteString(c.overlayStyles.Separator.Render("───────────────────────────────"))
	b.WriteString("\n")

	options := []struct {
		key   string
		label string
		desc  string
		color lipgloss.Color
	}{
		{"c", "Resolve with Claude", "Use Claude Code to resolve conflicts", styles.Green},
		{"o", "Open manually", "Open files in your editor", styles.Blue},
		{"a", "Abort merge", "Cancel the merge operation", styles.Red},
	}

	for _, opt := range options {
		keyStyle := lipgloss.NewStyle().Foreground(opt.color).Bold(true)
		labelStyle := c.overlayStyles.MenuItem.Bold(true)
		descStyle := c.overlayStyles.Footer

		line := keyStyle.Render("["+opt.key+"]") + " " +
			labelStyle.Render(opt.label) + " " +
			descStyle.Render("- "+opt.desc)
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Footer hint
	b.WriteString("\n")
	footer := c.overlayStyles.Footer.Render("j/k: navigate • Esc: close")
	b.WriteString(footer)

	return b.String()
}

// Title returns the overlay title
func (c *ConflictOverlay) Title() string {
	return "Merge Conflicts"
}

// Size returns the overlay dimensions
func (c *ConflictOverlay) Size() (width, height int) {
	// Width: enough for file paths and options
	// Height: header + files + separator + options + footer + padding
	fileLines := len(c.files)
	if fileLines > 10 {
		fileLines = 10 // Cap at 10 visible files
	}
	return 70, 8 + fileLines // header(2) + files + separator(1) + options(3) + footer(2)
}
