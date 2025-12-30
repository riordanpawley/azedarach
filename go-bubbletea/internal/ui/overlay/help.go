package overlay

import (
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

	// Build full content
	var content strings.Builder
	for i, cat := range categories {
		if i > 0 {
			content.WriteString("\n")
		}

		// Category header
		categoryStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#89b4fa")).
			Bold(true)
		content.WriteString(categoryStyle.Render(cat.Name + ":"))
		content.WriteString("\n")

		// Bindings in this category
		for _, binding := range cat.Bindings {
			keyStyle := h.styles.MenuKey
			descStyle := h.styles.MenuItem

			line := "  " + keyStyle.Render(binding.Key) + "  " + descStyle.Render(binding.Description)
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
	return 50, 24     // Total overlay size including padding and borders
}

// getCategories returns all keybinding categories
func (h *HelpOverlay) getCategories() []KeyCategory {
	return []KeyCategory{
		{
			Name: "Navigation",
			Bindings: []KeyBinding{
				{Key: "h/l", Description: "Move between columns"},
				{Key: "j/k", Description: "Move up/down in column"},
				{Key: "gg", Description: "Jump to top of column"},
				{Key: "ge", Description: "Jump to bottom of column"},
				{Key: "gh", Description: "Jump to first column"},
				{Key: "gl", Description: "Jump to last column"},
			},
		},
		{
			Name: "Actions",
			Bindings: []KeyBinding{
				{Key: "Space", Description: "Open action menu"},
				{Key: "Enter", Description: "Show task details"},
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
				{Key: "v", Description: "Toggle selection on current task"},
				{Key: "%", Description: "Select all"},
				{Key: "A", Description: "Clear selection"},
			},
		},
		{
			Name: "Other",
			Bindings: []KeyBinding{
				{Key: "Tab", Description: "Toggle compact/kanban view"},
				{Key: "q", Description: "Quit"},
				{Key: "Ctrl+L", Description: "Refresh screen"},
			},
		},
	}
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
