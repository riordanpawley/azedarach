package board

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// CardContentHeight is the single source of truth for rendered card content height.
const CardContentHeight = 4
const narrowHeaderExpansionThreshold = 21
const narrowCardExtraLines = 1
const narrowHeaderExtraLines = 1

const tmuxSessionToken = "T"
const descendantTmuxSessionToken = "Td"
const worktreeToken = "✓"

// RuntimeSignals represents runtime metadata rendered for a task card.
// These values are computed independently from session state.
type RuntimeSignals struct {
	HasTmuxSession           bool
	HasDescendantTmuxSession bool
	HasWorktree              bool
	GitAheadCount            int
	GitBehindCount           int
	HasUncommittedChanges    bool
	GitAdditions             int
	GitDeletions             int
	PendingOperationState    string
	PendingOperationID       string
	PendingOperationPercent  int
}

// ChildProgress summarizes completion progress for a parent task's children.
type ChildProgress struct {
	Total int
	Done  int
}

// renderCard renders a task card
func renderCard(task domain.Task, runtimeSignals *RuntimeSignals, isCursor bool, isSelected bool, width int, childProgress *ChildProgress, phaseInfo *phases.TaskPhaseInfo, showPhases bool, s *styles.Styles) string {
	if width < 1 {
		width = 1
	}

	// Choose card style based on state
	cardStyle := s.Card
	if isSelected {
		cardStyle = s.CardSelected
	} else if isCursor {
		cardStyle = s.CardActive
	}

	// Apply fixed size to keep all cards same height regardless of content.
	cardStyle = cardStyle.Width(width).Height(CardContentHeight)

	// Priority badge (e.g., "P0", "P1", etc.)
	priorityText := task.Priority.String()
	priorityBadge := s.PriorityBadge(int(task.Priority)).Render(priorityText)

	// Phase badge (if enabled and phase info available)
	var phaseBadge string
	if showPhases && phaseInfo != nil {
		phaseStyle := s.Card.Copy().
			Foreground(styles.Blue).
			Bold(true)
		if phaseInfo.Phase == 0 {
			// Phase 0 is ready (green)
			phaseStyle = phaseStyle.Foreground(styles.Green)
		} else if phaseInfo.Phase > 0 {
			// Phase > 0 is blocked (yellow/orange)
			phaseStyle = phaseStyle.Foreground(styles.Yellow)
		}
		phaseBadge = phaseStyle.Render(fmt.Sprintf("Φ%d", phaseInfo.Phase))
	}

	// Title - truncate if needed
	// Account for border + padding.
	maxLineLen := width - 4
	if maxLineLen < 1 {
		maxLineLen = 1
	}
	cardHeight, headerLineCount := cardLayoutHeights(maxLineLen)

	// Cursor indicator (▶ symbol when cursor is on this card)
	cursor := ""
	if isCursor {
		cursor = "▶"
	}

	issueToken := task.ID.String()
	if cursor != "" {
		issueToken = cursor + task.ID.String()
	}
	headerParts := []string{
		priorityBadge,
		issueToken,
		renderTaskTypeBadge(task.Type, s),
	}
	if phaseBadge != "" {
		headerParts = append(headerParts, phaseBadge)
	}

	// Session status row (if session exists)
	var sessionRow string
	if task.Session != nil {
		sessionRow = renderSessionStatus(task.Session, s)
	}
	sessionCompact := renderSessionStatusCompact(task.Session)
	visibleRuntimeSignals := runtimeSignalsForHeader(task.Session, runtimeSignals)
	runtimeRow := renderRuntimeSignals(visibleRuntimeSignals, s)
	runtimeCompact := renderRuntimeSignalsCompact(visibleRuntimeSignals, s)

	headerLines := make([]string, max(1, headerLineCount))
	headerLines[0] = strings.Join(headerParts, " ")
	preferCompact := maxLineLen < 44
	for _, token := range []struct {
		full    string
		compact string
	}{
		{full: sessionRow, compact: sessionCompact},
		{full: runtimeRow, compact: runtimeCompact},
		{full: renderChildProgressValue(childProgress, s), compact: renderChildProgressValue(childProgress, s)},
	} {
		appendHeaderToken(headerLines, maxLineLen, token.full, token.compact, preferCompact)
	}

	titleLines := renderTitleBodyLines(task.Title, maxLineLen, cardHeight-headerLineCount)

	// Compose fixed content rows to guarantee stable card height.
	lines := make([]string, 0, cardHeight)
	for _, headerLine := range headerLines {
		lines = append(lines, ansi.Truncate(headerLine, maxLineLen, "…"))
	}
	for _, line := range titleLines {
		lines = append(lines, ansi.Truncate(line, maxLineLen, "…"))
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)

	return cardStyle.Height(cardHeight).Render(content)
}

func cardLayoutHeights(maxLineLen int) (cardHeight int, headerLines int) {
	cardHeight = CardContentHeight
	headerLines = 1
	if maxLineLen < narrowHeaderExpansionThreshold {
		cardHeight += narrowCardExtraLines
		headerLines += narrowHeaderExtraLines
	}
	return cardHeight, headerLines
}

func appendHeaderToken(headerLines []string, maxLineLen int, full string, compact string, preferCompact bool) {
	if full == "" && compact == "" {
		return
	}

	try := func(lineIdx int, token string) bool {
		if token == "" {
			return false
		}
		if lineIdx < 0 || lineIdx >= len(headerLines) {
			return false
		}
		candidate := strings.TrimSpace(headerLines[lineIdx])
		if candidate != "" {
			candidate += " "
		}
		candidate += token
		if ansi.StringWidth(candidate) <= maxLineLen {
			headerLines[lineIdx] = candidate
			return true
		}
		return false
	}

	appendToAnyLine := func(token string) bool {
		for i := range headerLines {
			if try(i, token) {
				return true
			}
		}
		return false
	}

	if preferCompact {
		if appendToAnyLine(compact) {
			return
		}
		if appendToAnyLine(full) {
			return
		}
		return
	}

	if appendToAnyLine(full) {
		return
	}
	if appendToAnyLine(compact) {
		return
	}
}

func runtimeSignalsForHeader(session *domain.Session, signals *RuntimeSignals) *RuntimeSignals {
	if signals == nil {
		return nil
	}
	normalized := *signals
	if session != nil {
		// Session badge already communicates active session state; omit duplicate tmux tokens.
		normalized.HasTmuxSession = false
		normalized.HasDescendantTmuxSession = false
	}
	return &normalized
}

func renderTaskTypeBadge(taskType domain.TaskType, s *styles.Styles) string {
	letter := taskType.Short()
	background := styles.Surface1

	switch taskType {
	case domain.TypeEpic:
		background = styles.Mauve
	case domain.TypeFeature:
		background = styles.Green
	case domain.TypeBug:
		background = styles.Red
	case domain.TypeTask:
		background = styles.Blue
	case domain.TypeChore:
		background = styles.Yellow
	}

	badgeStyle := lipgloss.NewStyle().
		Foreground(styles.Base).
		Background(background).
		Bold(true).
		Padding(0, 1)

	if s != nil {
		badgeStyle = s.TypeBadge.Copy().
			Foreground(styles.Base).
			Background(background).
			Bold(true)
	}

	return badgeStyle.Render(letter)
}

func renderTitleBodyLines(title string, maxLineLen int, maxLines int) []string {
	if maxLines <= 0 {
		return []string{}
	}
	lines := make([]string, 0, maxLines)
	if maxLineLen < 1 {
		for len(lines) < maxLines {
			lines = append(lines, "")
		}
		return lines
	}

	runes := []rune(title)
	for len(lines) < maxLines {
		if len(runes) == 0 {
			lines = append(lines, "")
			continue
		}
		if len(runes) <= maxLineLen {
			lines = append(lines, string(runes))
			runes = nil
			continue
		}
		if len(lines) == maxLines-1 {
			if maxLineLen == 1 {
				lines = append(lines, "…")
				break
			}
			lines = append(lines, string(runes[:maxLineLen-1])+"…")
			break
		}
		lines = append(lines, string(runes[:maxLineLen]))
		runes = runes[maxLineLen:]
	}
	return lines
}

// renderSessionStatus renders the session status line with icon and elapsed time
func renderSessionStatus(session *domain.Session, s *styles.Styles) string {
	icon := session.State.Icon()

	// Elapsed time if active/waiting and started.
	var elapsed string
	if session.StartedAt != nil && (session.State == domain.SessionBusy || session.State == domain.SessionWaiting) {
		d := time.Since(*session.StartedAt)
		elapsed = formatDuration(d)
	}

	stateStyle := s.SessionState(session.State)
	label := map[domain.SessionState]string{
		domain.SessionBusy:    "B",
		domain.SessionWaiting: "W",
		domain.SessionDone:    "D",
		domain.SessionError:   "E",
		domain.SessionPaused:  "P",
		domain.SessionIdle:    "I",
	}[session.State]
	if label == "" {
		label = "?"
	}
	var value string
	if elapsed != "" {
		value = fmt.Sprintf("%s %s %s", icon, label, elapsed)
	} else {
		value = fmt.Sprintf("%s %s", icon, label)
	}
	return stateStyle.Render(value)
}

func renderSessionStatusCompact(session *domain.Session) string {
	if session == nil {
		return ""
	}

	icon := session.State.Icon()
	if session.StartedAt != nil && (session.State == domain.SessionBusy || session.State == domain.SessionWaiting) {
		return fmt.Sprintf("%s%s", icon, formatCompactDuration(time.Since(*session.StartedAt)))
	}

	stateCode := map[domain.SessionState]string{
		domain.SessionBusy:    "B",
		domain.SessionWaiting: "W",
		domain.SessionDone:    "D",
		domain.SessionError:   "E",
		domain.SessionPaused:  "P",
		domain.SessionIdle:    "I",
	}
	code, ok := stateCode[session.State]
	if !ok {
		code = "?"
	}
	return icon + code
}

func renderRuntimeSignals(signals *RuntimeSignals, s *styles.Styles) string {
	if signals == nil {
		return ""
	}

	hasLineChanges := signals.GitAdditions > 0 || signals.GitDeletions > 0
	parts := make([]string, 0, 6)
	if signals.HasTmuxSession {
		parts = append(parts, renderRuntimeSignalToken(tmuxSessionToken, styles.Sky, s))
	}
	if signals.HasDescendantTmuxSession {
		parts = append(parts, renderRuntimeSignalToken(descendantTmuxSessionToken, styles.Sky, s))
	}
	if signals.HasWorktree {
		parts = append(parts, renderRuntimeSignalToken(worktreeToken, styles.Teal, s))
	}
	if pendingToken := renderPendingOperationToken(signals.PendingOperationState, signals.PendingOperationPercent, false); pendingToken != "" {
		parts = append(parts, renderRuntimeSignalToken(pendingToken, styles.Mauve, s))
	}
	if signals.GitAheadCount > 0 {
		parts = append(parts, renderRuntimeSignalToken(fmt.Sprintf("↑%d", signals.GitAheadCount), styles.Green, s))
	}
	if signals.GitBehindCount > 0 {
		parts = append(parts, renderRuntimeSignalToken(fmt.Sprintf("↓%d", signals.GitBehindCount), styles.Yellow, s))
	}
	if signals.HasUncommittedChanges {
		parts = append(parts, renderRuntimeSignalToken("✎", styles.Peach, s))
	}
	if hasLineChanges {
		if s == nil {
			parts = append(parts, fmt.Sprintf("+%d/-%d", signals.GitAdditions, signals.GitDeletions))
		} else {
			add := renderRuntimeSignalToken(fmt.Sprintf("+%d", signals.GitAdditions), styles.Green, s)
			del := renderRuntimeSignalToken(fmt.Sprintf("-%d", signals.GitDeletions), styles.Red, s)
			sep := lipgloss.NewStyle().Foreground(styles.Overlay0).Render("/")
			parts = append(parts, add+sep+del)
		}
	}

	return strings.Join(parts, " ")
}

func renderRuntimeSignalsCompact(signals *RuntimeSignals, s *styles.Styles) string {
	if signals == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if signals.HasTmuxSession {
		parts = append(parts, renderRuntimeSignalToken("T", styles.Sky, s))
	}
	if signals.HasDescendantTmuxSession {
		parts = append(parts, renderRuntimeSignalToken("Td", styles.Sky, s))
	}
	if signals.HasWorktree {
		parts = append(parts, renderRuntimeSignalToken(worktreeToken, styles.Teal, s))
	}
	if pendingToken := renderPendingOperationToken(signals.PendingOperationState, signals.PendingOperationPercent, true); pendingToken != "" {
		parts = append(parts, renderRuntimeSignalToken(pendingToken, styles.Mauve, s))
	}

	hasChanges := signals.HasUncommittedChanges || signals.GitAdditions > 0 || signals.GitDeletions > 0
	if hasChanges {
		token := "G*"
		if divergence := formatAheadBehindToken(signals.GitAheadCount, signals.GitBehindCount); divergence != "" {
			token += divergence
		}
		parts = append(parts, renderRuntimeSignalToken(token, styles.Peach, s))
	} else if signals.GitAheadCount > 0 || signals.GitBehindCount > 0 {
		parts = append(parts, renderRuntimeSignalToken("G"+formatAheadBehindToken(signals.GitAheadCount, signals.GitBehindCount), styles.Yellow, s))
	}

	return strings.Join(parts, "")
}

func formatAheadBehindToken(ahead, behind int) string {
	if ahead > 0 && behind > 0 {
		return fmt.Sprintf("↑%d/↓%d", ahead, behind)
	}
	if behind > 0 {
		return fmt.Sprintf("↓%d", behind)
	}
	if ahead > 0 {
		return fmt.Sprintf("↑%d", ahead)
	}
	return ""
}

func renderPendingOperationToken(state string, percent int, compact bool) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return ""
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	if compact {
		if percent > 0 {
			return fmt.Sprintf("M:%s%d", strings.ToUpper(string(state[0])), percent)
		}
		return "M:" + strings.ToUpper(string(state[0]))
	}
	if percent > 0 {
		return fmt.Sprintf("M:%s(%d%%)", state, percent)
	}
	return "M:" + state
}

func renderRuntimeSignalToken(token string, color lipgloss.Color, s *styles.Styles) string {
	if s == nil {
		return token
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(token)
}

// formatDuration formats a duration as "2d 2h", "2h 34m", or "45m".
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalHours := int(d.Hours())
	days := totalHours / 24
	h := totalHours % 24
	m := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func formatCompactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalHours := int(d.Hours())
	days := totalHours / 24
	h := totalHours % 24
	m := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	if h > 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}

// renderChildProgress renders child completion progress with completion ratio.
func renderChildProgress(progress ChildProgress, s *styles.Styles) string {
	if progress.Total <= 0 {
		return ""
	}
	return s.EpicProgress.Render(fmt.Sprintf("[%d/%d]", progress.Done, progress.Total))
}

func renderChildProgressValue(progress *ChildProgress, s *styles.Styles) string {
	if progress == nil {
		return ""
	}
	return renderChildProgress(*progress, s)
}

// RenderCard is the exported version for testing
func RenderCard(task domain.Task, isCursor bool, isSelected bool, width int, s *styles.Styles) string {
	return renderCard(task, nil, isCursor, isSelected, width, nil, nil, false, s)
}

// CardLineFootprint returns the number of terminal lines consumed by one card.
// Column rendering stacks cards with newline separators and uses this value as
// the row stride for cursor/viewport math.
func CardLineFootprint(s *styles.Styles, width int) int {
	if width < 1 {
		width = 1
	}
	sample := domain.Task{
		ID:       "sample",
		Title:    "sample",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
	cardLines := lipgloss.Height(renderCard(sample, nil, false, false, width, nil, nil, false, s))
	if cardLines < 1 {
		cardLines = 1
	}
	return cardLines
}

// BuildChildProgress computes done/total child counts keyed by parent task ID.
func BuildChildProgress(tasks []domain.Task) map[string]ChildProgress {
	progressByParent := make(map[string]ChildProgress)
	for _, task := range tasks {
		parentID := ""
		if task.ParentID != nil && task.ParentID.String() != "" {
			parentID = task.ParentID.String()
		} else {
			for _, dep := range task.Dependencies {
				if dep.Type == domain.DependencyParentChild && dep.ID.String() != "" {
					parentID = dep.ID.String()
					break
				}
			}
		}
		if parentID == "" {
			continue
		}
		progress := progressByParent[parentID]
		progress.Total++
		if task.Status == domain.StatusDone {
			progress.Done++
		}
		progressByParent[parentID] = progress
	}
	return progressByParent
}
