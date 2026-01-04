package styles

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
)

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

	// Toasts
	ToastInfo    lipgloss.Style
	ToastSuccess lipgloss.Style
	ToastWarning lipgloss.Style
	ToastError   lipgloss.Style

	// Session states
	SessionBusy    lipgloss.Style
	SessionWaiting lipgloss.Style
	SessionDone    lipgloss.Style
	SessionError   lipgloss.Style
	SessionPaused  lipgloss.Style
	SessionIdle    lipgloss.Style

	// Epic progress
	EpicProgress lipgloss.Style
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
			BorderForeground(Lavender).
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

		ToastInfo: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Blue).
			Foreground(Blue).
			Padding(0, 1),

		ToastSuccess: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Green).
			Foreground(Green).
			Padding(0, 1),

		ToastWarning: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Yellow).
			Foreground(Yellow).
			Padding(0, 1),

		ToastError: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Red).
			Foreground(Red).
			Padding(0, 1),

		SessionBusy: lipgloss.NewStyle().
			Foreground(Blue),

		SessionWaiting: lipgloss.NewStyle().
			Foreground(Yellow),

		SessionDone: lipgloss.NewStyle().
			Foreground(Green),

		SessionError: lipgloss.NewStyle().
			Foreground(Red),

		SessionPaused: lipgloss.NewStyle().
			Foreground(Overlay0),

		SessionIdle: lipgloss.NewStyle().
			Foreground(Subtext0),

		EpicProgress: lipgloss.NewStyle().
			Foreground(Subtext0),
	}
}

// SessionState returns the appropriate style for a session state
func (s *Styles) SessionState(state domain.SessionState) lipgloss.Style {
	switch state {
	case domain.SessionBusy:
		return s.SessionBusy
	case domain.SessionWaiting:
		return s.SessionWaiting
	case domain.SessionDone:
		return s.SessionDone
	case domain.SessionError:
		return s.SessionError
	case domain.SessionPaused:
		return s.SessionPaused
	case domain.SessionIdle:
		return s.SessionIdle
	default:
		return s.SessionIdle
	}
}
