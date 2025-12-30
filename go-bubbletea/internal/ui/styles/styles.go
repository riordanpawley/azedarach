package styles

import "github.com/charmbracelet/lipgloss"

// Styles holds all the UI styles
type Styles struct {
	// Board
	Board              lipgloss.Style
	Column             lipgloss.Style
	ColumnHeader       lipgloss.Style
	ColumnHeaderActive lipgloss.Style

	// Cards
	Card         lipgloss.Style
	CardActive   lipgloss.Style
	CardSelected lipgloss.Style
	TaskID       lipgloss.Style
	TaskTitle    lipgloss.Style

	// Badges
	PriorityBadge func(priority int) lipgloss.Style
	TypeBadge     lipgloss.Style

	// Status bar
	StatusBar  lipgloss.Style
	StatusMode lipgloss.Style
	StatusHint lipgloss.Style
	StatusInfo lipgloss.Style

	// Overlays
	Overlay          lipgloss.Style
	OverlayTitle     lipgloss.Style
	MenuItem         lipgloss.Style
	MenuItemActive   lipgloss.Style
	MenuItemDisabled lipgloss.Style
	MenuKey          lipgloss.Style
	Separator        lipgloss.Style
}

// New creates a new Styles instance with Catppuccin Macchiato theme
func New() *Styles {
	return &Styles{
		Board: lipgloss.NewStyle().
			Background(Base),

		Column: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Surface1).
			Padding(0, 1),

		ColumnHeader: lipgloss.NewStyle().
			Foreground(Subtext0).
			Bold(true).
			Padding(0, 1).
			MarginBottom(1),

		ColumnHeaderActive: lipgloss.NewStyle().
			Foreground(Blue).
			Bold(true).
			Padding(0, 1).
			MarginBottom(1),

		Card: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Surface1).
			Padding(0, 1).
			MarginBottom(1),

		CardActive: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Blue).
			Padding(0, 1).
			MarginBottom(1),

		CardSelected: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Mauve).
			Padding(0, 1).
			MarginBottom(1),

		TaskID: lipgloss.NewStyle().
			Foreground(Overlay1).
			Bold(true),

		TaskTitle: lipgloss.NewStyle().
			Foreground(Text),

		PriorityBadge: func(priority int) lipgloss.Style {
			color := PriorityColors[min(priority, len(PriorityColors)-1)]
			return lipgloss.NewStyle().
				Foreground(Base).
				Background(color).
				Padding(0, 1).
				Bold(true)
		},

		TypeBadge: lipgloss.NewStyle().
			Foreground(Subtext0).
			Background(Surface1).
			Padding(0, 1),

		StatusBar: lipgloss.NewStyle().
			Background(Surface0).
			Foreground(Subtext0).
			Padding(0, 1),

		StatusMode: lipgloss.NewStyle().
			Background(Blue).
			Foreground(Base).
			Bold(true).
			Padding(0, 1),

		StatusHint: lipgloss.NewStyle().
			Foreground(Overlay1),

		StatusInfo: lipgloss.NewStyle().
			Foreground(Subtext0),

		Overlay: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Surface2).
			Background(Base).
			Padding(1, 2),

		OverlayTitle: lipgloss.NewStyle().
			Foreground(Text).
			Bold(true).
			MarginBottom(1),

		MenuItem: lipgloss.NewStyle().
			Foreground(Text),

		MenuItemActive: lipgloss.NewStyle().
			Foreground(Blue).
			Bold(true),

		MenuItemDisabled: lipgloss.NewStyle().
			Foreground(Overlay0),

		MenuKey: lipgloss.NewStyle().
			Foreground(Yellow).
			Bold(true),

		Separator: lipgloss.NewStyle().
			Foreground(Surface1),
	}
}
