package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// HelpOverlay displays keybinding reference
type HelpOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
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
		case "ctrl+d":
			h.scroll = min(h.maxScroll, h.scroll+h.halfPageStep())
			return h, nil
		case "ctrl+u":
			h.scroll = max(0, h.scroll-h.halfPageStep())
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
	case tea.WindowSizeMsg:
		h.ApplyWindowSize(msg)
	}

	return h, nil
}

// View renders the help overlay
func (h *HelpOverlay) View() string {
	width, height := h.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:      h.styles,
		width:       width,
		height:      height,
		title:       "Help",
		breakpoint:  84,
		gap:         2,
		minLeft:     40,
		leftFocused: true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return h.renderScrollableContent(max(3, height))
		},
	})
}

func (h *HelpOverlay) renderScrollableContent(contentHeight int) string {
	categories := h.getCategories()
	keyWidth := keybinds.KeyColumnWidth(categories, 8)
	content := keybinds.RenderCategories(categories, keyWidth, keybinds.Theme{
		HeaderStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#89b4fa")).
			Bold(true),
		SeparatorStyle:   h.styles.Separator,
		KeyStyle:         h.styles.MenuKey,
		DescriptionStyle: h.styles.MenuItem,
	})

	// Calculate scroll limits
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	h.viewHeight = max(1, contentHeight)
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
					"/",
					lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Render("ctrl+u/d"),
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
	return h.ClampResponsive(72, 24)
}

// getCategories returns all keybinding categories
func (h *HelpOverlay) getCategories() []keybinds.Category {
	categories := make([]keybinds.Category, 0, 1+len(keybinds.HelpCategories()))
	categories = append(categories, cardHeaderLegendCategory())
	categories = append(categories, keybinds.HelpCategories()...)
	return categories
}

func cardHeaderLegendCategory() keybinds.Category {
	return keybinds.Category{
		Name: "Card Header Legend",
		Bindings: []keybinds.Binding{
			{Key: "P0/P1/P2/...", Description: "Issue priority badge"},
			{Key: "T/F/B/E/C", Description: "Type badge letter (Task/Feature/Bug/Epic/Chore)"},
			{Key: "● B 23h / ◐ W 5m", Description: "Session icon + state code + elapsed"},
			{Key: "T / Td / ✓", Description: "Runtime assets; T/Td are hidden when session badge is shown"},
			{Key: "↑2 / ↓1", Description: "Git ahead / behind upstream"},
			{Key: "✎", Description: "Uncommitted file changes present"},
			{Key: "+171/-65", Description: "Added/removed line counts"},
			{Key: "M:running(40%)", Description: "Pending operation progress"},
			{Key: "[1/3]", Description: "Completed child issues / total children"},
		},
	}
}

func (h *HelpOverlay) halfPageStep() int {
	step := h.viewHeight / 2
	if step < 1 {
		return 1
	}
	return step
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
