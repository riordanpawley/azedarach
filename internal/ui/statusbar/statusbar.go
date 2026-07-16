package statusbar

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// StatusBar represents the status bar at the bottom of the TUI
type StatusBar struct {
	mode             types.Mode
	modeSuffix       string
	alertIndicator   string
	width            int
	hintBindings     []keybinds.Binding
	currentProject   string
	selectionSummary string
	filterSummary    string
	sortSummary      string
	loadingIndicator string
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

// SetFilterSummary sets the current filter summary rendered in the status bar.
func (sb *StatusBar) SetFilterSummary(summary string) {
	sb.filterSummary = summary
}

// SetSortSummary sets the current sort summary rendered in the status bar.
func (sb *StatusBar) SetSortSummary(summary string) {
	sb.sortSummary = summary
}

// SetLoadingIndicator sets the loading label rendered before other status hints.
func (sb *StatusBar) SetLoadingIndicator(indicator string) {
	sb.loadingIndicator = indicator
}

// SetCurrentProject sets the project name rendered at the left of the status bar.
func (sb *StatusBar) SetCurrentProject(project string) {
	sb.currentProject = project
}

func (sb *StatusBar) SetHintBindings(bindings []keybinds.Binding) {
	sb.hintBindings = append([]keybinds.Binding(nil), bindings...)
}

// SetModeSuffix sets optional text rendered inside the mode badge after mode label.
func (sb *StatusBar) SetModeSuffix(suffix string) {
	sb.modeSuffix = strings.TrimSpace(suffix)
}

// SetAlertIndicator sets an optional alert label rendered near the mode badge.
func (sb *StatusBar) SetAlertIndicator(indicator string) {
	sb.alertIndicator = strings.TrimSpace(indicator)
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

	mandatorySlots := make([]statusSlot, 0, 2)
	if strings.TrimSpace(sb.alertIndicator) != "" {
		mandatorySlots = append(mandatorySlots, statusSlot{style: sb.styles.StatusHint.Bold(true), text: sb.alertIndicator})
	}
	if strings.TrimSpace(sb.filterSummary) != "" {
		mandatorySlots = append(mandatorySlots, statusSlot{style: sb.styles.StatusInfo, text: sb.filterSummary})
	}
	if strings.TrimSpace(sb.sortSummary) != "" {
		mandatorySlots = append(mandatorySlots, statusSlot{style: sb.styles.StatusInfo, text: sb.sortSummary})
	}
	if len(mandatorySlots) > 0 && contentWidth <= 18 {
		return sb.styles.StatusBar.Width(sb.width).Render(sb.compactMandatoryStatus())
	}
	modeLabel := " " + sb.mode.String() + " "
	if sb.modeSuffix != "" {
		modeLabel = " " + sb.mode.String() + " " + sb.modeSuffix + " "
	}
	priorityTail, priorityTailStyle := sb.priorityTailReservation()
	prioritySlots := make([]statusSlot, 0, 1)
	if priorityTail != "" {
		prioritySlots = append(prioritySlots, statusSlot{style: priorityTailStyle, text: priorityTail})
	}
	modeLabel, plannedMandatorySlots := sb.planCoreLayout(contentWidth, separatorWidth, modeLabel, mandatorySlots, prioritySlots)
	modeLabelWidth := lipgloss.Width(sb.styles.StatusMode.Render(modeLabel))
	plannedLoading := ""
	if sb.loadingIndicator != "" {
		loadingSlot := statusSlot{style: sb.styles.StatusHint, text: sb.loadingIndicator}
		if statusLayoutWidth(modeLabelWidth, plannedMandatorySlots, append([]statusSlot{loadingSlot}, prioritySlots...), separatorWidth) <= contentWidth {
			plannedLoading = sb.loadingIndicator
		}
	}
	reservedTrailing := make([]statusSlot, 0, 2)
	if plannedLoading != "" {
		reservedTrailing = append(reservedTrailing, statusSlot{style: sb.styles.StatusHint, text: plannedLoading})
	}
	reservedTrailing = append(reservedTrailing, prioritySlots...)

	if sb.currentProject != "" {
		projectStyle := sb.styles.StatusInfo.Bold(true)
		reservedForMode := statusLayoutWidth(modeLabelWidth, plannedMandatorySlots, reservedTrailing, separatorWidth)
		reservedForMode += separatorWidth // separator between project and mode
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
	for _, slot := range plannedMandatorySlots {
		if !appendSlot(slot.style, slot.text) {
			return sb.styles.StatusBar.Width(sb.width).Render(strings.Join(parts, ""))
		}
	}

	slots := make([]statusSlot, 0, 4)
	if plannedLoading != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusHint, text: plannedLoading})
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

func statusLayoutWidth(modeWidth int, mandatory, trailing []statusSlot, separatorWidth int) int {
	width := modeWidth
	for _, slot := range mandatory {
		width += separatorWidth + lipgloss.Width(slot.style.Render(slot.text))
	}
	for _, slot := range trailing {
		width += separatorWidth + lipgloss.Width(slot.style.Render(slot.text))
	}
	return width
}

func (sb StatusBar) planCoreLayout(contentWidth, separatorWidth int, preferredMode string, mandatory, priority []statusSlot) (string, []statusSlot) {
	if len(mandatory) == 0 {
		return preferredMode, mandatory
	}

	mandatoryPlans := [][]statusSlot{mandatory}
	if fallback := sb.filterSortFallbackSlots(); len(fallback) > 0 {
		mandatoryPlans = append(mandatoryPlans, fallback)
	}
	mandatoryPlans = append(mandatoryPlans, []statusSlot{{style: sb.styles.StatusInfo, text: sb.mandatoryFallbackToken()}})

	modePlans := []string{preferredMode, sb.mode.String(), shortModeLabel(sb.mode)}
	planningPriority := priority
	shortModeWidth := lipgloss.Width(sb.styles.StatusMode.Render(shortModeLabel(sb.mode)))
	lastMandatory := mandatoryPlans[len(mandatoryPlans)-1]
	if statusLayoutWidth(shortModeWidth, lastMandatory, planningPriority, separatorWidth) > contentWidth {
		// An oversized priority label cannot be preserved in full. Do not let it
		// demote mandatory alerts that still provide the actionable route.
		planningPriority = nil
	}
	for _, candidateMode := range modePlans {
		modeWidth := lipgloss.Width(sb.styles.StatusMode.Render(candidateMode))
		for _, candidateMandatory := range mandatoryPlans {
			if statusLayoutWidth(modeWidth, candidateMandatory, planningPriority, separatorWidth) <= contentWidth {
				return candidateMode, candidateMandatory
			}
		}
	}
	return shortModeLabel(sb.mode), lastMandatory
}

func (sb StatusBar) filterSortFallbackSlots() []statusSlot {
	slots := make([]statusSlot, 0, 2)
	if strings.TrimSpace(sb.alertIndicator) != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusHint.Bold(true), text: sb.alertIndicator})
	}
	filterSort := ""
	switch {
	case strings.TrimSpace(sb.filterSummary) != "" && strings.TrimSpace(sb.sortSummary) != "":
		filterSort = "F/S"
	case strings.TrimSpace(sb.filterSummary) != "":
		filterSort = "F"
	case strings.TrimSpace(sb.sortSummary) != "":
		filterSort = "S"
	}
	if filterSort != "" {
		slots = append(slots, statusSlot{style: sb.styles.StatusInfo, text: filterSort})
	}
	return slots
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
	bindings := sb.effectiveHintBindings()
	if len(bindings) == 0 {
		return ""
	}
	bindings = truncateHintBindings(sb.mode, bindings)
	return sb.renderHintBindings(bindings)
}

func (sb StatusBar) priorityTailReservation() (string, lipgloss.Style) {
	if sb.selectionSummary != "" {
		return sb.selectionSummary, sb.styles.StatusInfo
	}
	bindings := truncateHintBindings(sb.mode, sb.effectiveHintBindings())
	if len(bindings) == 0 {
		return "", sb.styles.StatusHint
	}
	limit := 1
	if len(sb.hintBindings) == 0 && sb.mode == types.ModeNormal {
		limit = 2
	}
	limit = min(limit, len(bindings))
	reservation := sb.renderHintBindings(bindings[:limit])
	if len(bindings) > limit {
		reservation += "…"
	}
	return reservation, sb.styles.StatusHint
}

func (sb StatusBar) effectiveHintBindings() []keybinds.Binding {
	if len(sb.hintBindings) > 0 {
		return sb.hintBindings
	}
	return GetHintBindings(sb.mode)
}

func (sb StatusBar) renderHintBindings(bindings []keybinds.Binding) string {
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
		maxHints = 12
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

func shortModeLabel(mode types.Mode) string {
	switch mode {
	case types.ModeNormal:
		return "N"
	case types.ModeSelect:
		return "V"
	case types.ModeSearch:
		return "/"
	case types.ModeGoto:
		return "G"
	case types.ModeAction:
		return "A"
	default:
		return "?"
	}
}

func (sb StatusBar) compactMandatoryStatus() string {
	parts := []string{shortModeLabel(sb.mode)}
	if token := compactAlertToken(sb.alertIndicator); token != "" {
		parts = append(parts, token)
	}
	if strings.TrimSpace(sb.filterSummary) != "" {
		parts = append(parts, compactFilterToken(sb.filterSummary))
	}
	if strings.TrimSpace(sb.sortSummary) != "" {
		parts = append(parts, compactSortToken(sb.sortSummary))
	}
	return strings.Join(parts, " ")
}

func (sb StatusBar) mandatoryFallbackToken() string {
	alertToken := compactAlertToken(sb.alertIndicator)
	hasAlert := alertToken != ""
	hasFilter := strings.TrimSpace(sb.filterSummary) != ""
	hasSort := strings.TrimSpace(sb.sortSummary) != ""
	switch {
	case hasAlert && (hasFilter || hasSort):
		return alertToken + "/F/S"
	case hasAlert:
		return alertToken
	case hasFilter && hasSort:
		return "F/S"
	case hasFilter:
		return "F"
	case hasSort:
		return "S"
	default:
		return ""
	}
}

func compactFilterToken(summary string) string {
	if strings.EqualFold(strings.TrimSpace(summary), "F:none") {
		return "F:0"
	}
	return "F:1"
}

func compactSortToken(summary string) string {
	trimmed := strings.TrimSpace(summary)
	field := "?"
	order := "a"
	if strings.HasPrefix(trimmed, "S:") {
		payload := strings.TrimPrefix(trimmed, "S:")
		fieldPart, orderPart, found := strings.Cut(payload, "/")
		if f := strings.TrimSpace(fieldPart); f != "" {
			field = strings.ToLower(string(f[0]))
		}
		if found && strings.EqualFold(strings.TrimSpace(orderPart), "desc") {
			order = "d"
		}
	}
	return "S:" + field + order
}

func compactAlertToken(indicator string) string {
	normalized := strings.ToLower(strings.TrimSpace(indicator))
	if normalized == "" {
		return ""
	}

	tokens := make([]string, 0, 2)
	if strings.Contains(normalized, "recover") {
		tokens = append(tokens, "R!")
	}
	if strings.Contains(normalized, "notice") || strings.Contains(normalized, "error") {
		tokens = append(tokens, "N!")
	}
	if len(tokens) == 0 {
		return "!"
	}
	return strings.Join(tokens, "/")
}
