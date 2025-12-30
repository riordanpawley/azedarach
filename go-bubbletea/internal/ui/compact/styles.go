package compact

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// Styles holds the styling for the compact list view
type Styles struct {
	// Table structure
	HeaderCell lipgloss.Style
	Separator  lipgloss.Style

	// Row styles
	Row         lipgloss.Style
	RowActive   lipgloss.Style
	RowSelected lipgloss.Style

	// Column styles
	ColNumber  lipgloss.Style
	ColID      lipgloss.Style
	ColTitle   lipgloss.Style
	ColStatus  lipgloss.Style
	ColPri     lipgloss.Style
	ColSession lipgloss.Style

	// Status abbreviations
	StatusOpen       lipgloss.Style
	StatusInProgress lipgloss.Style
	StatusBlocked    lipgloss.Style
	StatusDone       lipgloss.Style

	// Priority colors
	PriorityP0 lipgloss.Style
	PriorityP1 lipgloss.Style
	PriorityP2 lipgloss.Style
	PriorityP3 lipgloss.Style
	PriorityP4 lipgloss.Style

	// Task type colors
	TypeEpic    lipgloss.Style
	TypeFeature lipgloss.Style
	TypeBug     lipgloss.Style
	TypeTask    lipgloss.Style
	TypeChore   lipgloss.Style

	// Indicators
	Cursor   lipgloss.Style
	Selected lipgloss.Style
}

// NewStyles creates a new Styles instance with Catppuccin Macchiato theme
func NewStyles() *Styles {
	return &Styles{
		// Table structure
		HeaderCell: lipgloss.NewStyle().
			Foreground(styles.Text).
			Bold(true),

		Separator: lipgloss.NewStyle().
			Foreground(styles.Surface1),

		// Row styles
		Row: lipgloss.NewStyle().
			Foreground(styles.Text),

		RowActive: lipgloss.NewStyle().
			Foreground(styles.Text).
			Background(styles.Surface0),

		RowSelected: lipgloss.NewStyle().
			Foreground(styles.Text).
			Background(styles.Surface1),

		// Column styles
		ColNumber: lipgloss.NewStyle().
			Foreground(styles.Overlay1).
			Width(5).
			Align(lipgloss.Right),

		ColID: lipgloss.NewStyle().
			Foreground(styles.Overlay1).
			Bold(true).
			Width(10).
			Align(lipgloss.Left),

		ColTitle: lipgloss.NewStyle().
			Foreground(styles.Text).
			Align(lipgloss.Left),

		ColStatus: lipgloss.NewStyle().
			Width(7).
			Align(lipgloss.Center),

		ColPri: lipgloss.NewStyle().
			Width(4).
			Align(lipgloss.Center),

		ColSession: lipgloss.NewStyle().
			Width(8).
			Align(lipgloss.Center),

		// Status abbreviations with colors
		StatusOpen: lipgloss.NewStyle().
			Foreground(styles.Blue),

		StatusInProgress: lipgloss.NewStyle().
			Foreground(styles.Yellow),

		StatusBlocked: lipgloss.NewStyle().
			Foreground(styles.Red),

		StatusDone: lipgloss.NewStyle().
			Foreground(styles.Green),

		// Priority colors
		PriorityP0: lipgloss.NewStyle().
			Foreground(styles.Red).
			Bold(true),

		PriorityP1: lipgloss.NewStyle().
			Foreground(styles.Peach).
			Bold(true),

		PriorityP2: lipgloss.NewStyle().
			Foreground(styles.Yellow),

		PriorityP3: lipgloss.NewStyle().
			Foreground(styles.Green),

		PriorityP4: lipgloss.NewStyle().
			Foreground(styles.Overlay0),

		// Task type colors
		TypeEpic: lipgloss.NewStyle().
			Foreground(styles.Mauve).
			Bold(true),

		TypeFeature: lipgloss.NewStyle().
			Foreground(styles.Green),

		TypeBug: lipgloss.NewStyle().
			Foreground(styles.Red),

		TypeTask: lipgloss.NewStyle().
			Foreground(styles.Blue),

		TypeChore: lipgloss.NewStyle().
			Foreground(styles.Yellow),

		// Indicators
		Cursor: lipgloss.NewStyle().
			Foreground(styles.Blue).
			Bold(true),

		Selected: lipgloss.NewStyle().
			Foreground(styles.Mauve).
			Bold(true),
	}
}
