package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// SortOption represents a sort option with metadata
type SortOption struct {
	Key         string
	Label       string
	Field       domain.SortField
	Description string
}

// SortMenu is a menu overlay for sorting configuration
type SortMenu struct {
	twoPaneDialogChrome
	dialogViewportState
	sort    *domain.Sort
	options []SortOption
	styles  *Styles
}

// NewSortMenu creates a new sort menu for the given sort state
func NewSortMenu(sort *domain.Sort) *SortMenu {
	return &SortMenu{
		sort:   sort,
		styles: New(),
		options: []SortOption{
			{
				Key:         "d",
				Label:       "Git Diff",
				Field:       domain.SortByGitDiff,
				Description: "Sort by total diff (+/-), highest first",
			},
			{
				Key:         "s",
				Label:       "Session",
				Field:       domain.SortBySession,
				Description: "Sort by session state (waiting tasks first)",
			},
			{
				Key:         "p",
				Label:       "Priority",
				Field:       domain.SortByPriority,
				Description: "Sort by priority (P0 highest)",
			},
			{
				Key:         "u",
				Label:       "Updated",
				Field:       domain.SortByUpdated,
				Description: "Sort by last updated time",
			},
		},
	}
}

// Init initializes the menu
func (m *SortMenu) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *SortMenu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "d", "s", "p", "u":
			// Find the option for this key
			for _, opt := range m.options {
				if opt.Key == msg.String() {
					// Toggle will handle both field change and direction toggle
					m.sort.Toggle(opt.Field)
					return m, func() tea.Msg {
						return SelectionMsg{
							Key:   msg.String(),
							Value: m.sort,
						}
					}
				}
			}
		}
	}

	return m, nil
}

// View renders the menu
func (m *SortMenu) View() string {
	width, height := m.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             m.Title(),
		rightSectionTitle: "Actions",
		breakpoint:        80,
		gap:               3,
		minLeft:           44,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return m.renderContent()
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.styles, m.actionBindings(), width)
		},
	})
}

func (m *SortMenu) renderContent() string {
	var b strings.Builder

	for _, opt := range m.options {
		// Check if this is the current sort field
		isActive := m.sort.Field == opt.Field

		// Build the line: [key] Label (description) [indicator] [arrow]
		var line strings.Builder

		// Key
		keyStyle := m.styles.MenuKey
		if !isActive {
			keyStyle = m.styles.MenuItem
		}
		line.WriteString(keyStyle.Render("[" + opt.Key + "]"))
		line.WriteString(" ")

		// Label
		labelStyle := m.styles.MenuItem
		if isActive {
			labelStyle = m.styles.MenuItemActive
		}
		line.WriteString(labelStyle.Render(opt.Label))
		line.WriteString(" ")

		// Description
		descStyle := m.styles.Footer
		line.WriteString(descStyle.Render("(" + opt.Description + ")"))

		// Indicator and arrow (only for active field)
		if isActive {
			line.WriteString(" ")
			line.WriteString(m.styles.MenuItemActive.Render("●"))
			line.WriteString(" ")

			// Direction arrow
			arrow := "↑"
			if m.sort.Order == domain.SortDesc {
				arrow = "↓"
			}
			line.WriteString(m.styles.MenuItemActive.Render(arrow))
		}

		b.WriteString(line.String())
		b.WriteString("\n")
	}

	// Add footer hint
	b.WriteString("\n")
	footer := m.styles.Footer.Render("Press same key to toggle direction • Esc to close")
	b.WriteString(footer)

	return b.String()
}

func (m *SortMenu) actionBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "d", Description: "sort by git diff"},
		{Key: "s", Description: "sort by session"},
		{Key: "p", Description: "sort by priority"},
		{Key: "u", Description: "sort by updated"},
		{Key: "same key", Description: "toggle direction"},
		{Key: "Esc/q", Description: "close"},
	}
}

// Title returns the overlay title
func (m *SortMenu) Title() string {
	return "Sort"
}

// Size returns the overlay dimensions
func (m *SortMenu) Size() (width, height int) {
	// Width: enough for longest line with description + indicator + arrow
	// Height: number of options + footer + padding
	return m.Clamp(110, len(m.options)+10)
}
