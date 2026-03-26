package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// KeyBinding represents a single keybinding entry
type KeyBinding struct {
	Key         string
	Description string
}

// KeyCategory represents a category of keybindings
type KeyCategory struct {
	Name     string
	Bindings []KeyBinding
}

// HelpOverlay displays keybinding reference
type HelpOverlay struct {
	styles     *Styles
	scroll     int
	maxScroll  int
	viewHeight int
}

// NewHelpOverlay creates a new help overlay
func NewHelpOverlay() *HelpOverlay {
	return &HelpOverlay{
		styles:     New(),
		scroll:     0,
		viewHeight: 20, // Default height, will be updated based on Size()
	}
}

// Init initializes the overlay
func (h *HelpOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (h *HelpOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "?":
			return h, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			if h.scroll < h.maxScroll {
				h.scroll++
			}
			return h, nil

		case "k", "up":
			if h.scroll > 0 {
				h.scroll--
			}
			return h, nil

		case "g":
			// Jump to top
			h.scroll = 0
			return h, nil

		case "G":
			// Jump to bottom
			h.scroll = h.maxScroll
			return h, nil
		}
	}

	return h, nil
}

// View renders the help overlay
func (h *HelpOverlay) View() string {
	categories := h.getCategories()
	keyWidth := h.keyColumnWidth(categories)

	var content strings.Builder
	for i, cat := range categories {
		if i > 0 {
			content.WriteString("\n")
		}

		categoryStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#89b4fa")).
			Bold(true)
		content.WriteString(categoryStyle.Render(cat.Name + ":"))
		content.WriteString("\n")
		content.WriteString(h.styles.Separator.Render(strings.Repeat("─", keyWidth+28)))
		content.WriteString("\n")

		for _, binding := range cat.Bindings {
			keyLabel := fmt.Sprintf("%-*s", keyWidth, binding.Key)
			line := "  " + h.styles.MenuKey.Render(keyLabel) + "  " + h.styles.MenuItem.Render(binding.Description)
			content.WriteString(line)
			content.WriteString("\n")
		}
	}

	// Calculate scroll limits
	lines := strings.Split(content.String(), "\n")
	totalLines := len(lines)
	h.maxScroll = max(0, totalLines-h.viewHeight)

	// Apply scroll offset
	start := h.scroll
	end := min(h.scroll+h.viewHeight, totalLines)

	visibleLines := lines[start:end]
	result := strings.Join(visibleLines, "\n")

	// Add scroll indicator if needed
	if h.maxScroll > 0 {
		scrollInfo := h.styles.Footer.Render(
			lipgloss.JoinHorizontal(
				lipgloss.Left,
				"[",
				lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Render("j/k"),
				" to scroll, ",
				lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Render("g/G"),
				" to jump]",
			),
		)
		result += "\n\n" + scrollInfo
	}

	return result
}

// Title returns the overlay title
func (h *HelpOverlay) Title() string {
	return "Help"
}

// Size returns the overlay dimensions
func (h *HelpOverlay) Size() (width, height int) {
	h.viewHeight = 20 // Content viewing area
	return 72, 24     // Total overlay size including padding and borders
}

// getCategories returns all keybinding categories
func (h *HelpOverlay) getCategories() []KeyCategory {
	return []KeyCategory{
		{
			Name: "Navigation",
			Bindings: []KeyBinding{
				{Key: "h/l", Description: "Move between columns"},
				{Key: "j/k", Description: "Move up/down in column"},
				{Key: "ctrl+u / ctrl+d", Description: "Half-page scroll"},
				{Key: "g then g/e/h/l", Description: "Jump in board"},
				{Key: "g then w/p/s", Description: "Jump / projects / spec"},
				{Key: "Enter", Description: "Drill into children"},
			},
		},
		{
			Name: "Actions",
			Bindings: []KeyBinding{
				{Key: "Space", Description: "Open task workspace"},
				{Key: "space then i", Description: "Image attachments"},
				{Key: "space then s/S/a/x", Description: "Session actions"},
				{Key: "space then u/m/P/f", Description: "Git actions"},
				{Key: "space then c/e/d/h/l", Description: "Task status/edit actions"},
			},
		},
		{
			Name: "Modes",
			Bindings: []KeyBinding{
				{Key: "/", Description: "Search"},
				{Key: "f", Description: "Filter menu"},
				{Key: ",", Description: "Sort menu"},
				{Key: "v", Description: "Select mode"},
				{Key: "?", Description: "Help (this screen)"},
			},
		},
		{
			Name: "Selection",
			Bindings: []KeyBinding{
				{Key: "space / a", Description: "Toggle selection"},
				{Key: "A", Description: "Select current column"},
				{Key: "%", Description: "Select all visible"},
				{Key: "*", Description: "Invert selection"},
				{Key: "x", Description: "Clear selection"},
				{Key: "Enter", Description: "Bulk action menu"},
			},
		},
		{
			Name: "Other",
			Bindings: []KeyBinding{
				{Key: "Tab", Description: "Toggle compact/kanban view"},
				{Key: "esc", Description: "Close overlay / exit mode"},
				{Key: "q", Description: "Quit"},
				{Key: "ctrl+l", Description: "Refresh screen"},
			},
		},
	}
}

func (h *HelpOverlay) keyColumnWidth(categories []KeyCategory) int {
	width := 8
	for _, category := range categories {
		for _, binding := range category.Bindings {
			if len(binding.Key) > width {
				width = len(binding.Key)
			}
		}
	}
	return width
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
