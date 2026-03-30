package statusbar

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/eventticker"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// StatusBar represents the status bar at the bottom of the TUI
type StatusBar struct {
	mode             types.Mode
	width            int
	hintBindings     []keybinds.Binding
	currentProject   string
	selectionSummary string
	loadingIndicator string
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

// SetLoadingIndicator sets the loading label rendered before other status hints.
func (sb *StatusBar) SetLoadingIndicator(indicator string) {
	sb.loadingIndicator = indicator
}

// SetCurrentProject sets the project name rendered at the left of the status bar.
func (sb *StatusBar) SetCurrentProject(project string) {
	sb.currentProject = project
}

// SetEventTicker sets the ring buffer that provides the latest event message.
func (sb *StatusBar) SetEventTicker(ticker *eventticker.Ring) {
	sb.eventTicker = ticker
}

func (sb *StatusBar) SetHintBindings(bindings []keybinds.Binding) {
	sb.hintBindings = append([]keybinds.Binding(nil), bindings...)
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

	separator := sb.styles.StatusHint.Render(" │ ")
	separatorWidth := lipgloss.Width(separator)

	parts := make([]string, 0, 8)
	visibleWidth := 0

	appendPart := func(style lipgloss.Style, text string) bool {
		availableWidth := contentWidth - visibleWidth
		if availableWidth < 1 {
			return false
		}
		rendered, truncated := renderWithin(style, text, availableWidth)
		if rendered == "" {
			return false
		}
		parts = append(parts, rendered)
		visibleWidth += lipgloss.Width(rendered)
		return !truncated
	}

	appendSlot := func(style lipgloss.Style, text string) bool {
		if len(parts) > 0 {
			if visibleWidth+separatorWidth >= contentWidth {
				return false
			}
			parts = append(parts, separator)
			visibleWidth += separatorWidth
		}
		return appendPart(style, text)
	}

	modeLabel := " " + sb.mode.String() + " "
	modeLabelWidth := lipgloss.Width(sb.styles.StatusMode.Render(modeLabel))

	if sb.currentProject != "" {
		projectStyle := sb.styles.StatusInfo.Copy().Bold(true)
		reservedForMode := modeLabelWidth + separatorWidth
		projectWidth := contentWidth - reservedForMode
		if projectWidth > 0 {
			renderedProject, _ := renderWithin(projectStyle, sb.currentProject, projectWidth)
			if renderedProject != "" {
				parts = append(parts, renderedProject)
				visibleWidth += lipgloss.Width(renderedProject)
			}
		}
	}

	if !appendSlot(sb.styles.StatusMode, modeLabel) {
		return sb.styles.StatusBar.Width(sb.width).Render(strings.Join(parts, ""))
	}

	slots := make([]statusSlot, 0, 4)
	if sb.loadingIndicator != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusHint, text: sb.loadingIndicator})
	}
	if sb.eventTicker != nil {
		if latest := sb.eventTicker.Latest(); latest != "" {
			slots = append(slots, statusSlot{style: sb.styles.StatusInfo, text: latest})
		}
	}
	if sb.selectionSummary != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusInfo, text: sb.selectionSummary})
	}
	if hints := sb.inlineHints(); hints != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusHint, text: hints})
	}

	for _, slot := range slots {
		if !appendSlot(slot.style, slot.text) {
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

func (sb StatusBar) inlineHints() string {
	bindings := sb.hintBindings
	if len(bindings) == 0 {
		bindings = GetHintBindings(sb.mode)
	}
	if len(bindings) == 0 {
		return ""
	}
	bindings = truncateHintBindings(sb.mode, bindings)
	inline := make([]keybinds.Binding, 0, len(bindings))
	for _, binding := range bindings {
		key := strings.TrimSpace(binding.Key)
		desc := strings.TrimSpace(binding.Description)
		if key == "" || desc == "" {
			continue
		}
		inline = append(inline, keybinds.Binding{
			Key:         key + ":",
			Description: desc,
		})
	}
	if len(inline) == 0 {
		return ""
	}
	return keybinds.RenderInline(inline, "  ", keybinds.Theme{
		KeyStyle:         sb.styles.StatusInfo,
		DescriptionStyle: sb.styles.StatusHint,
		FooterStyle:      sb.styles.StatusHint,
	})
}

func truncateHintBindings(mode types.Mode, bindings []keybinds.Binding) []keybinds.Binding {
	maxHints := len(bindings)
	if mode == types.ModeAction {
		maxHints = 10
	}
	if maxHints >= len(bindings) {
		return bindings
	}
	truncated := append([]keybinds.Binding{}, bindings[:maxHints]...)
	truncated = append(truncated, keybinds.Binding{
		Key:         "…",
		Description: fmt.Sprintf("+%d more", len(bindings)-maxHints),
	})
	return truncated
}
