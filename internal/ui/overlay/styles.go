package overlay

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// Styles holds all overlay-specific styles
type Styles struct {
	// Overlay is the base overlay container style
	Overlay lipgloss.Style
	// Title is the overlay title style
	Title lipgloss.Style
	// MenuItem is the default menu item style
	MenuItem lipgloss.Style
	// MenuItemActive is the highlighted/selected menu item style
	MenuItemActive lipgloss.Style
	// MenuItemDisabled is the disabled menu item style
	MenuItemDisabled lipgloss.Style
	// MenuKey is the style for keybinding hints
	MenuKey lipgloss.Style
	// MenuKeyDisabled is the style for disabled keybinding hints
	MenuKeyDisabled lipgloss.Style
	// Separator is the style for divider lines
	Separator lipgloss.Style
	// Footer is the style for overlay footer text
	Footer lipgloss.Style
	// MenuHeader is the style for menu section headers
	MenuHeader lipgloss.Style
	// MenuCount is the style for count indicators
	MenuCount lipgloss.Style
}

// New creates a new Styles instance using the Catppuccin Macchiato theme
func New() *Styles {
	return &Styles{
		Overlay: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(styles.Surface2).
			Background(styles.Base).
			Padding(1, 2),

		Title: lipgloss.NewStyle().
			Foreground(styles.Text).
			Bold(true).
			MarginBottom(1),

		MenuItem: lipgloss.NewStyle().
			Foreground(styles.Text),

		MenuItemActive: lipgloss.NewStyle().
			Foreground(styles.Blue).
			Bold(true),

		MenuItemDisabled: lipgloss.NewStyle().
			Foreground(styles.Overlay0),

		MenuKey: lipgloss.NewStyle().
			Foreground(styles.Yellow).
			Bold(true),

		MenuKeyDisabled: lipgloss.NewStyle().
			Foreground(styles.Surface2).
			Bold(true),

		Separator: lipgloss.NewStyle().
			Foreground(styles.Surface1),

		Footer: lipgloss.NewStyle().
			Foreground(styles.Subtext0).
			MarginTop(1),

		MenuHeader: lipgloss.NewStyle().
			Foreground(styles.Subtext1).
			Bold(true),

		MenuCount: lipgloss.NewStyle().
			Foreground(styles.Green),
	}
}
