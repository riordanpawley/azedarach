package statusbar

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// StatusBar represents the status bar at the bottom of the TUI
type StatusBar struct {
	mode   types.Mode
	width  int
	styles *styles.Styles
	view   string
}

// New creates a new StatusBar with the given mode, width, and styles
func New(mode types.Mode, width int, styles *styles.Styles) StatusBar {
	return StatusBar{
		mode:   mode,
		width:  width,
		styles: styles,
	}
}

// WithView sets an optional current view indicator (e.g. KAN/LST).
func (sb StatusBar) WithView(view string) StatusBar {
	sb.view = strings.ToUpper(strings.TrimSpace(view))
	return sb
}

// Render renders the status bar as a string
func (sb StatusBar) Render() string {
	modeBadge := sb.styles.StatusMode.Render(" " + sb.mode.String() + " ")

	// Keybinding hints
	hints := GetHints(sb.mode)
	hintParts := []string{}
	if sb.view != "" {
		hintParts = append(hintParts, "VIEW:"+sb.view)
	}
	if hints != "" {
		hintParts = append(hintParts, hints)
	}
	hintsRendered := sb.styles.StatusHint.Render(strings.Join(hintParts, "  "))

	// Combine mode badge and hints with separator
	var content string
	if len(hintParts) > 0 {
		separator := sb.styles.StatusHint.Render(" │ ")
		content = lipgloss.JoinHorizontal(lipgloss.Left, modeBadge, separator, hintsRendered)
	} else {
		content = modeBadge
	}

	// Keep status bar to a single terminal row.
	return sb.styles.StatusBar.Width(sb.width).Height(1).MaxHeight(1).Render(content)
}
