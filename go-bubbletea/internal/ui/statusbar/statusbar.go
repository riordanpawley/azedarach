package statusbar

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// StatusBar represents the status bar at the bottom of the TUI
type StatusBar struct {
	mode             types.Mode
	width            int
	selectionSummary string
	styles           *styles.Styles
}

// New creates a new StatusBar with the given mode, width, and styles
func New(mode types.Mode, width int, styles *styles.Styles) StatusBar {
	return StatusBar{
		mode:   mode,
		width:  width,
		styles: styles,
	}
}

// SetSelectionSummary sets the optional selection summary rendered in the status bar.
func (sb *StatusBar) SetSelectionSummary(summary string) {
	sb.selectionSummary = summary
}

// Render renders the status bar as a string
func (sb StatusBar) Render() string {
	if sb.width < 1 {
		sb.width = 1
	}

	modeBadge := sb.styles.StatusMode.Render(" " + sb.mode.String() + " ")
	info := ""
	if sb.selectionSummary != "" {
		info = sb.styles.StatusInfo.Render(" " + sb.selectionSummary + " ")
	}

	// Keybinding hints
	hints := GetHints(sb.mode)
	hintsRendered := sb.styles.StatusHint.Render(hints)

	// Combine mode badge and hints with separator
	var content string
	if info != "" && hints != "" {
		separator := sb.styles.StatusHint.Render(" │ ")
		fullContent := lipgloss.JoinHorizontal(lipgloss.Left, modeBadge, separator, info, separator, hintsRendered)
		if lipgloss.Width(fullContent) <= sb.width {
			content = fullContent
		} else {
			infoContent := lipgloss.JoinHorizontal(lipgloss.Left, modeBadge, separator, info)
			if lipgloss.Width(infoContent) <= sb.width {
				content = infoContent
			} else {
				content = modeBadge
			}
		}
	} else if info != "" {
		separator := sb.styles.StatusHint.Render(" │ ")
		infoContent := lipgloss.JoinHorizontal(lipgloss.Left, modeBadge, separator, info)
		if lipgloss.Width(infoContent) <= sb.width {
			content = infoContent
		} else {
			content = modeBadge
		}
	} else if hints != "" {
		separator := sb.styles.StatusHint.Render(" │ ")
		fullContent := lipgloss.JoinHorizontal(lipgloss.Left, modeBadge, separator, hintsRendered)
		if lipgloss.Width(fullContent) <= sb.width {
			content = fullContent
		} else {
			content = modeBadge
		}
	} else {
		content = modeBadge
	}

	// Apply status bar style and fill width
	return sb.styles.StatusBar.Width(sb.width).Render(content)
}
