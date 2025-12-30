package statusbar

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// StatusBar represents the status bar at the bottom of the TUI
type StatusBar struct {
	mode   types.Mode
	width  int
	styles *styles.Styles
}

// New creates a new StatusBar with the given mode, width, and styles
func New(mode types.Mode, width int, styles *styles.Styles) StatusBar {
	return StatusBar{
		mode:   mode,
		width:  width,
		styles: styles,
	}
}

// Render renders the status bar as a string
func (sb StatusBar) Render() string {
	// Mode badge
	modeBadge := sb.styles.StatusMode.Render(" " + sb.mode.String() + " ")

	// Keybinding hints
	hints := GetHints(sb.mode)
	hintsRendered := sb.styles.StatusHint.Render(hints)

	// Combine mode badge and hints with separator
	var content string
	if hints != "" {
		separator := sb.styles.StatusHint.Render(" │ ")
		content = lipgloss.JoinHorizontal(lipgloss.Left, modeBadge, separator, hintsRendered)
	} else {
		content = modeBadge
	}

	// Apply status bar style and fill width
	return sb.styles.StatusBar.Width(sb.width).Render(content)
}
