package overlay

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CleanupCategory represents a type of cleanup operation
type CleanupCategory struct {
	ID           string
	Label        string
	Description  string
	Count        int
	SizeEstimate string
	Selected     bool
	Destructive  bool // Whether this requires confirmation
}

// BulkCleanupOverlay provides bulk cleanup operations
type BulkCleanupOverlay struct {
	categories      []CleanupCategory
	cursor          int
	confirmMode     bool
	confirmSelected bool // true = Yes, false = No
	styles          *Styles
	cleanupFunc     CleanupFunc
	error           string
}

// CleanupFunc is a function that performs cleanup operations
type CleanupFunc func(ctx context.Context, categoryIDs []string) (CleanupResult, error)

// CleanupResult contains the results of cleanup operations
type CleanupResult struct {
	Deleted          int
	Archived         int
	WorktreesRemoved int
	SessionsCleaned  int
}

// CleanupExecutedMsg is sent when cleanup is executed
type CleanupExecutedMsg struct {
	Result CleanupResult
	Error  error
}

// NewBulkCleanupOverlay creates a new bulk cleanup overlay
func NewBulkCleanupOverlay(cleanupFunc CleanupFunc, taskCount, worktreeCount, sessionCount int) *BulkCleanupOverlay {
	// Calculate estimates for completed tasks older than 30 days
	completedOldCount := taskCount / 10 // Estimate 10% of tasks

	categories := []CleanupCategory{
		{
			ID:           "delete_old_done",
			Label:        "Delete completed tasks (>30 days)",
			Description:  "Permanently remove done tasks older than 30 days",
			Count:        completedOldCount,
			SizeEstimate: fmt.Sprintf("~%d tasks", completedOldCount),
			Selected:     false,
			Destructive:  true,
		},
		{
			ID:           "archive_done",
			Label:        "Archive all done tasks",
			Description:  "Move all done tasks to archive",
			Count:        taskCount / 4, // Estimate 25% done
			SizeEstimate: fmt.Sprintf("~%d tasks", taskCount/4),
			Selected:     false,
			Destructive:  false,
		},
		{
			ID:           "remove_orphaned_worktrees",
			Label:        "Remove orphaned worktrees",
			Description:  "Delete worktrees with no active sessions",
			Count:        worktreeCount,
			SizeEstimate: fmt.Sprintf("~%d worktrees", worktreeCount),
			Selected:     false,
			Destructive:  true,
		},
		{
			ID:           "clean_stale_sessions",
			Label:        "Clean stale sessions",
			Description:  "Remove sessions inactive for >24 hours",
			Count:        sessionCount,
			SizeEstimate: fmt.Sprintf("~%d sessions", sessionCount),
			Selected:     false,
			Destructive:  false,
		},
	}

	return &BulkCleanupOverlay{
		categories:  categories,
		cursor:      0,
		confirmMode: false,
		styles:      New(),
		cleanupFunc: cleanupFunc,
	}
}

// Init initializes the overlay
func (c *BulkCleanupOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (c *BulkCleanupOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if c.confirmMode {
			return c.handleConfirmMode(msg)
		}
		return c.handleNormalMode(msg)
	}

	return c, nil
}

// handleNormalMode handles key presses in normal mode
func (c *BulkCleanupOverlay) handleNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return c, func() tea.Msg { return CloseOverlayMsg{} }

	case "j", "down":
		if c.cursor < len(c.categories)-1 {
			c.cursor++
		}
		return c, nil

	case "k", "up":
		if c.cursor > 0 {
			c.cursor--
		}
		return c, nil

	case " ":
		// Toggle selection
		if c.cursor >= 0 && c.cursor < len(c.categories) {
			c.categories[c.cursor].Selected = !c.categories[c.cursor].Selected
		}
		return c, nil

	case "a":
		// Select all
		for i := range c.categories {
			c.categories[i].Selected = true
		}
		return c, nil

	case "A":
		// Deselect all
		for i := range c.categories {
			c.categories[i].Selected = false
		}
		return c, nil

	case "enter":
		// Check if any destructive operations are selected
		hasDestructive := false
		hasSelected := false
		for _, cat := range c.categories {
			if cat.Selected {
				hasSelected = true
				if cat.Destructive {
					hasDestructive = true
					break
				}
			}
		}

		if !hasSelected {
			c.error = "No categories selected"
			return c, nil
		}

		if hasDestructive {
			// Show confirmation dialog
			c.confirmMode = true
			c.confirmSelected = false // Default to No
			return c, nil
		}

		// Execute cleanup directly for non-destructive operations
		return c, c.executeCleanup()
	}

	return c, nil
}

// handleConfirmMode handles key presses in confirmation mode
func (c *BulkCleanupOverlay) handleConfirmMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		// Yes - execute cleanup
		c.confirmMode = false
		return c, c.executeCleanup()

	case "n", "N", "esc":
		// No - cancel
		c.confirmMode = false
		return c, nil

	case "enter":
		// Confirm current selection
		if c.confirmSelected {
			c.confirmMode = false
			return c, c.executeCleanup()
		}
		c.confirmMode = false
		return c, nil

	case "left", "h":
		// Move to No
		c.confirmSelected = false
		return c, nil

	case "right", "l", "tab":
		// Move to Yes
		c.confirmSelected = true
		return c, nil
	}

	return c, nil
}

// executeCleanup executes the selected cleanup operations
func (c *BulkCleanupOverlay) executeCleanup() tea.Cmd {
	if c.cleanupFunc == nil {
		return func() tea.Msg {
			return CleanupExecutedMsg{
				Error: fmt.Errorf("cleanup function not configured"),
			}
		}
	}

	// Collect selected category IDs
	var selectedIDs []string
	for _, cat := range c.categories {
		if cat.Selected {
			selectedIDs = append(selectedIDs, cat.ID)
		}
	}

	return func() tea.Msg {
		ctx := context.Background()
		result, err := c.cleanupFunc(ctx, selectedIDs)
		return CleanupExecutedMsg{
			Result: result,
			Error:  err,
		}
	}
}

// View renders the overlay
func (c *BulkCleanupOverlay) View() string {
	if c.confirmMode {
		return c.renderConfirmDialog()
	}
	return c.renderCategoryList()
}

// renderCategoryList renders the category selection screen
func (c *BulkCleanupOverlay) renderCategoryList() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	b.WriteString(headerStyle.Render("Bulk Cleanup Operations"))
	b.WriteString("\n\n")

	// Category list
	for i, cat := range c.categories {
		var style lipgloss.Style
		indicator := "  "
		checkbox := "[ ]"

		if cat.Selected {
			checkbox = "[✓]"
		}

		if i == c.cursor {
			style = c.styles.MenuItemActive
			indicator = "▶ "
		} else {
			style = c.styles.MenuItem
		}

		// Format line: ▶ [✓] Label (count) - description
		line := fmt.Sprintf("%s%s %s (%s)",
			indicator,
			checkbox,
			cat.Label,
			cat.SizeEstimate,
		)

		b.WriteString(style.Render(line))
		b.WriteString("\n")

		// Show description in smaller text
		if i == c.cursor {
			descStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("#94e2d5")).
				Italic(true)
			b.WriteString("   ")
			b.WriteString(descStyle.Render(cat.Description))
			b.WriteString("\n")
		}
	}

	// Error display
	if c.error != "" {
		b.WriteString("\n")
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f38ba8")).
			Bold(true)
		b.WriteString(errorStyle.Render("Error: " + c.error))
	}

	// Separator
	b.WriteString("\n")
	b.WriteString(c.styles.Separator.Render(strings.Repeat("─", 70)))
	b.WriteString("\n\n")

	// Help text
	hints := []string{
		c.styles.MenuKey.Render("j/k") + " " + c.styles.Footer.Render("Navigate"),
		c.styles.MenuKey.Render("Space") + " " + c.styles.Footer.Render("Toggle"),
		c.styles.MenuKey.Render("a") + " " + c.styles.Footer.Render("Select all"),
		c.styles.MenuKey.Render("A") + " " + c.styles.Footer.Render("Deselect all"),
		c.styles.MenuKey.Render("Enter") + " " + c.styles.Footer.Render("Execute"),
		c.styles.MenuKey.Render("Esc") + " " + c.styles.Footer.Render("Cancel"),
	}

	b.WriteString(c.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderConfirmDialog renders the confirmation dialog
func (c *BulkCleanupOverlay) renderConfirmDialog() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f38ba8")).
		Bold(true)

	b.WriteString(headerStyle.Render("⚠ Confirm Destructive Operation"))
	b.WriteString("\n\n")

	// List selected operations
	b.WriteString(c.styles.MenuItem.Render("This will perform the following operations:"))
	b.WriteString("\n\n")

	for _, cat := range c.categories {
		if cat.Selected && cat.Destructive {
			b.WriteString(c.styles.MenuKey.Render("  • "))
			b.WriteString(c.styles.MenuItem.Render(cat.Label))
			b.WriteString(c.styles.Footer.Render(fmt.Sprintf(" (%s)", cat.SizeEstimate)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(c.styles.MenuItem.Render("Are you sure you want to continue?"))
	b.WriteString("\n\n")

	// Buttons
	yesStyle := c.styles.MenuItem
	noStyle := c.styles.MenuItem

	if c.confirmSelected {
		yesStyle = c.styles.MenuItemActive
	} else {
		noStyle = c.styles.MenuItemActive
	}

	yes := yesStyle.Render("[Y] Yes")
	no := noStyle.Render("[N] No")

	buttons := yes + "    " + no
	b.WriteString(buttons)
	b.WriteString("\n\n")

	// Footer hint
	footer := c.styles.Footer.Render("← → / Tab: Switch • Enter: Confirm • Esc: Cancel")
	b.WriteString(footer)

	return b.String()
}

// Title returns the overlay title
func (c *BulkCleanupOverlay) Title() string {
	return "Bulk Cleanup"
}

// Size returns the overlay dimensions
func (c *BulkCleanupOverlay) Size() (width, height int) {
	if c.confirmMode {
		return 70, 20
	}
	// Height: categories * 2 (label + description) + header + help + padding
	return 75, (len(c.categories) * 2) + 10
}
