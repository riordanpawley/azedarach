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

const tmuxSessionToken = "T:Y"
const worktreeToken = "W:Y"

// RuntimeSignals represents runtime metadata rendered for a task card.
// These values are computed independently from session state.
type RuntimeSignals struct {
	HasTmuxSession        bool
	HasWorktree           bool
	GitAheadCount         int
	GitBehindCount        int
	HasUncommittedChanges bool
	GitAdditions          int
	GitDeletions          int
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

	// Cursor indicator (▶ symbol when cursor is on this card)
	cursor := ""
	if isCursor {
		cursor = "▶"
	}

	issueToken := task.ID
	if cursor != "" {
		issueToken = cursor + task.ID
	}
	headerParts := []string{
		priorityBadge,
		issueToken,
		fmt.Sprintf("[%s]", task.Type.String()),
	}
	if phaseBadge != "" {
		headerParts = append(headerParts, phaseBadge)
	}

	// Session status row (if session exists)
	var sessionRow string
	if task.Session != nil {
		sessionRow = renderSessionStatus(task.Session, s)
	}
	runtimeRow := renderRuntimeSignals(runtimeSignals)

	auxParts := make([]string, 0, 3)
	if sessionRow != "" {
		auxParts = append(auxParts, sessionRow)
	}
	if runtimeRow != "" {
		auxParts = append(auxParts, runtimeRow)
	}
	if childProgress != nil && childProgress.Total > 0 {
		auxParts = append(auxParts, renderChildProgress(*childProgress, s))
	}
	headerLine := strings.Join(headerParts, " ")
	if len(auxParts) > 0 {
		headerLine = headerLine + " " + strings.Join(auxParts, " ")
	}

	titleLines := renderTitleBodyLines(task.Title, maxLineLen, CardContentHeight-1)

	// Compose fixed content rows to guarantee stable card height.
	lines := make([]string, 0, CardContentHeight)
	lines = append(lines, ansi.Truncate(headerLine, maxLineLen, "…"))
	for _, line := range titleLines {
		lines = append(lines, ansi.Truncate(line, maxLineLen, "…"))
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)

	return cardStyle.Render(content)
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
	label := strings.ToUpper(session.State.String())
	if session.State == domain.SessionWaiting {
		label = "WAIT"
	}
	var value string
	if elapsed != "" {
		value = fmt.Sprintf("%s %s %s", icon, label, elapsed)
	} else {
		value = fmt.Sprintf("%s %s", icon, label)
	}
	return stateStyle.Render(value)
}

func renderRuntimeSignals(signals *RuntimeSignals) string {
	if signals == nil {
		return ""
	}
	parts := make([]string, 0, 6)
	if signals.HasTmuxSession {
		parts = append(parts, tmuxSessionToken)
	}
	if signals.HasWorktree {
		parts = append(parts, worktreeToken)
	}
	if signals.GitAheadCount > 0 {
		parts = append(parts, fmt.Sprintf("G:↑%d", signals.GitAheadCount))
	}
	if signals.GitBehindCount > 0 {
		parts = append(parts, fmt.Sprintf("G:↓%d", signals.GitBehindCount))
	}
	if signals.HasUncommittedChanges {
		parts = append(parts, "G:✎")
	}
	if signals.GitAdditions > 0 || signals.GitDeletions > 0 {
		parts = append(parts, fmt.Sprintf("+%d/-%d", signals.GitAdditions, signals.GitDeletions))
	}

	return strings.Join(parts, " ")
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

// renderChildProgress renders child completion progress with completion ratio.
func renderChildProgress(progress ChildProgress, s *styles.Styles) string {
	if progress.Total <= 0 {
		return ""
	}
	return s.EpicProgress.Render(fmt.Sprintf("[%d/%d]", progress.Done, progress.Total))
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
		if task.ParentID != nil && *task.ParentID != "" {
			parentID = *task.ParentID
		} else {
			for _, dep := range task.Dependencies {
				if dep.Type == domain.DependencyParentChild && dep.ID != "" {
					parentID = dep.ID
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
