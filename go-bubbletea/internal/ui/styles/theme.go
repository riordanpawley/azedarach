package styles

import "github.com/charmbracelet/lipgloss"

// Catppuccin Mocha palette (matching TypeScript version)
var (
	// Base colors
	Base   = lipgloss.Color("#1e1e2e")
	Mantle = lipgloss.Color("#181825")
	Crust  = lipgloss.Color("#11111b")

	// Text colors
	Text     = lipgloss.Color("#cdd6f4")
	Subtext0 = lipgloss.Color("#a6adc8")
	Subtext1 = lipgloss.Color("#bac2de")

	// Overlay colors
	Overlay0 = lipgloss.Color("#6c7086")
	Overlay1 = lipgloss.Color("#7f849c")
	Overlay2 = lipgloss.Color("#9399b2")

	// Surface colors
	Surface0 = lipgloss.Color("#313244")
	Surface1 = lipgloss.Color("#45475a")
	Surface2 = lipgloss.Color("#585b70")

	// Accent colors
	Red       = lipgloss.Color("#f38ba8")
	Green     = lipgloss.Color("#a6e3a1")
	Blue      = lipgloss.Color("#89b4fa")
	Yellow    = lipgloss.Color("#f9e2af")
	Peach     = lipgloss.Color("#fab387")
	Mauve     = lipgloss.Color("#cba6f7")
	Pink      = lipgloss.Color("#f5c2e7")
	Teal      = lipgloss.Color("#94e2d5")
	Sky       = lipgloss.Color("#89dceb")
	Sapphire  = lipgloss.Color("#74c7ec")
	Lavender  = lipgloss.Color("#b4befe")
	Flamingo  = lipgloss.Color("#f2cdcd")
	Rosewater = lipgloss.Color("#f5e0dc")
	Maroon    = lipgloss.Color("#eba0ac")
)

// PriorityColors maps priority levels to colors
var PriorityColors = []lipgloss.Color{
	Red,      // P0 - Critical
	Peach,    // P1 - High
	Yellow,   // P2 - Medium
	Green,    // P3 - Low
	Overlay0, // P4 - Backlog
}

// StatusColors maps status to colors
var StatusColors = map[string]lipgloss.Color{
	"open":        Blue,
	"in_progress": Mauve,
	"blocked":     Red,
	"closed":      Green,
}
