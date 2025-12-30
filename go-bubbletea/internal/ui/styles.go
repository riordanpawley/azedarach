// Package ui contains UI components and styling for the TUI.
package ui

import "github.com/charmbracelet/lipgloss"

// Catppuccin Macchiato color palette
var (
	// Base colors
	Base     = lipgloss.Color("#24273a")
	Mantle   = lipgloss.Color("#1e2030")
	Crust    = lipgloss.Color("#181926")
	Surface0 = lipgloss.Color("#363a4f")
	Surface1 = lipgloss.Color("#494d64")
	Surface2 = lipgloss.Color("#5b6078")

	// Text colors
	Text     = lipgloss.Color("#cad3f5")
	Subtext1 = lipgloss.Color("#b8c0e0")
	Subtext0 = lipgloss.Color("#a5adcb")
	Overlay2 = lipgloss.Color("#939ab7")
	Overlay1 = lipgloss.Color("#8087a2")
	Overlay0 = lipgloss.Color("#6e738d")

	// Accent colors
	Blue    = lipgloss.Color("#8aadf4")
	Lavender= lipgloss.Color("#b7bdf8")
	Sapphire= lipgloss.Color("#7dc4e4")
	Sky     = lipgloss.Color("#91d7e3")
	Teal    = lipgloss.Color("#8bd5ca")
	Green   = lipgloss.Color("#a6da95")
	Yellow  = lipgloss.Color("#eed49f")
	Peach   = lipgloss.Color("#f5a97f")
	Maroon  = lipgloss.Color("#ee99a0")
	Red     = lipgloss.Color("#ed8796")
	Mauve   = lipgloss.Color("#c6a0f6")
	Pink    = lipgloss.Color("#f5bde6")
	Flamingo= lipgloss.Color("#f0c6c6")
	Rosewater= lipgloss.Color("#f4dbd6")
)

// Styles contains all the lipgloss styles used in the application
type Styles struct {
	// Board
	Board  lipgloss.Style
	Column lipgloss.Style
	Header lipgloss.Style

	// Cards
	Card       lipgloss.Style
	CardActive lipgloss.Style
	CardTitle  lipgloss.Style

	// Status bar
	StatusBar lipgloss.Style
	ModeTag   lipgloss.Style

	// Session state indicators
	StateBusy    lipgloss.Style
	StateWaiting lipgloss.Style
	StateDone    lipgloss.Style
	StateError   lipgloss.Style
	StateIdle    lipgloss.Style

	// Priority indicators
	PriorityCritical lipgloss.Style
	PriorityHigh     lipgloss.Style
	PriorityMedium   lipgloss.Style
	PriorityLow      lipgloss.Style

	// Overlays
	Overlay     lipgloss.Style
	OverlayItem lipgloss.Style
	OverlayKey  lipgloss.Style

	// Toasts
	ToastInfo    lipgloss.Style
	ToastSuccess lipgloss.Style
	ToastWarning lipgloss.Style
	ToastError   lipgloss.Style
}

// NewStyles creates a new Styles with Catppuccin Macchiato theme
func NewStyles() Styles {
	return Styles{
		Board: lipgloss.NewStyle().
			Background(Base),

		Column: lipgloss.NewStyle().
			Width(25).
			Padding(0, 1).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Surface0),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(Text).
			Padding(0, 1).
			MarginBottom(1),

		Card: lipgloss.NewStyle().
			Padding(0, 1).
			MarginBottom(1).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Surface0),

		CardActive: lipgloss.NewStyle().
			Padding(0, 1).
			MarginBottom(1).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Blue),

		CardTitle: lipgloss.NewStyle().
			Foreground(Text),

		StatusBar: lipgloss.NewStyle().
			Background(Surface0).
			Foreground(Subtext0).
			Padding(0, 1),

		ModeTag: lipgloss.NewStyle().
			Bold(true).
			Foreground(Base).
			Background(Blue).
			Padding(0, 1),

		// Session states
		StateBusy:    lipgloss.NewStyle().Foreground(Blue).Bold(true),
		StateWaiting: lipgloss.NewStyle().Foreground(Yellow).Bold(true),
		StateDone:    lipgloss.NewStyle().Foreground(Green).Bold(true),
		StateError:   lipgloss.NewStyle().Foreground(Red).Bold(true),
		StateIdle:    lipgloss.NewStyle().Foreground(Overlay0),

		// Priorities
		PriorityCritical: lipgloss.NewStyle().Foreground(Red).Bold(true),
		PriorityHigh:     lipgloss.NewStyle().Foreground(Peach),
		PriorityMedium:   lipgloss.NewStyle().Foreground(Yellow),
		PriorityLow:      lipgloss.NewStyle().Foreground(Subtext0),

		// Overlay
		Overlay: lipgloss.NewStyle().
			Background(Surface0).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(Surface1).
			Padding(1, 2),

		OverlayItem: lipgloss.NewStyle().
			Foreground(Text),

		OverlayKey: lipgloss.NewStyle().
			Foreground(Blue).
			Bold(true),

		// Toasts
		ToastInfo: lipgloss.NewStyle().
			Background(Surface1).
			Foreground(Text).
			Padding(0, 1),

		ToastSuccess: lipgloss.NewStyle().
			Background(Green).
			Foreground(Base).
			Padding(0, 1),

		ToastWarning: lipgloss.NewStyle().
			Background(Yellow).
			Foreground(Base).
			Padding(0, 1),

		ToastError: lipgloss.NewStyle().
			Background(Red).
			Foreground(Base).
			Padding(0, 1),
	}
}
