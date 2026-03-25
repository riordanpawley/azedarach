package statusbar

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/eventticker"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// StatusBar represents the status bar at the bottom of the TUI
type StatusBar struct {
	mode             types.Mode
	width            int
	selectionSummary string
	eventTicker      *eventticker.Ring
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

// SetEventTicker sets the ring buffer that provides the latest event message.
func (sb *StatusBar) SetEventTicker(ticker *eventticker.Ring) {
	sb.eventTicker = ticker
}

// Render renders the status bar as a string
func (sb StatusBar) Render() string {
	if sb.width < 1 {
		sb.width = 1
	}

	contentWidth := sb.width - sb.styles.StatusBar.GetHorizontalFrameSize()
	if contentWidth < 1 {
		contentWidth = 1
	}

	modeBadge, truncated := renderWithin(sb.styles.StatusMode, " "+sb.mode.String()+" ", contentWidth)
	if modeBadge == "" {
		return sb.styles.StatusBar.Width(sb.width).Render("")
	}
	if truncated {
		return sb.styles.StatusBar.Width(sb.width).Render(modeBadge)
	}

	separator := sb.styles.StatusHint.Render(" │ ")
	separatorWidth := lipgloss.Width(separator)

	parts := []string{modeBadge}
	visibleWidth := lipgloss.Width(modeBadge)

	slots := make([]statusSlot, 0, 3)
	if sb.eventTicker != nil {
		if latest := sb.eventTicker.Latest(); latest != "" {
			slots = append(slots, statusSlot{style: sb.styles.StatusInfo, text: latest})
		}
	}
	if sb.selectionSummary != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusInfo, text: sb.selectionSummary})
	}
	if hints := GetHints(sb.mode); hints != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusHint, text: hints})
	}

	for _, slot := range slots {
		if visibleWidth+separatorWidth >= contentWidth {
			break
		}

		availableWidth := contentWidth - visibleWidth - separatorWidth
		rendered, truncated := renderWithin(slot.style, slot.text, availableWidth)
		if rendered == "" {
			continue
		}

		parts = append(parts, separator, rendered)
		visibleWidth += separatorWidth + lipgloss.Width(rendered)
		if truncated {
			break
		}
	}

	content := strings.Join(parts, "")

	// Apply status bar style and fill width
	return sb.styles.StatusBar.Width(sb.width).Render(content)
}

type statusSlot struct {
	style lipgloss.Style
	text  string
}

func renderWithin(style lipgloss.Style, text string, width int) (string, bool) {
	if width < 1 {
		return "", false
	}

	cleaned := strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if cleaned == "" {
		return "", false
	}

	rendered := style.Render(cleaned)
	if lipgloss.Width(rendered) <= width {
		return rendered, false
	}

	return ansi.Truncate(rendered, width, "…"), true
}
