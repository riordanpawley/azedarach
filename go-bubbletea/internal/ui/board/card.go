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

// ChildProgress summarizes completion progress for a parent task's children.
type ChildProgress struct {
	Total int
	Done  int
}

// renderCard renders a task card
func renderCard(task domain.Task, isCursor bool, isSelected bool, width int, childProgress *ChildProgress, phaseInfo *phases.TaskPhaseInfo, showPhases bool, s *styles.Styles) string {
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

	// Type badge (first letter: T, B, F, E, C)
	typeBadge := s.TypeBadge.Render(task.Type.Short())

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

	// Build the card content. Keep issue ID at the start so it is always visible.
	titleLine1, titleLine2 := renderTitleLines(cursor, task.ID, task.Title, maxLineLen)

	// Badge line: priority • type [• phase]
	badgeLine := lipgloss.JoinHorizontal(lipgloss.Left, priorityBadge, " • ", typeBadge)
	if phaseBadge != "" {
		badgeLine = lipgloss.JoinHorizontal(lipgloss.Left, badgeLine, " • ", phaseBadge)
	}

	// Session status row (if session exists)
	var sessionRow string
	if task.Session != nil {
		sessionRow = renderSessionStatus(task.Session, s)
	}

	auxParts := make([]string, 0, 2)
	if sessionRow != "" {
		auxParts = append(auxParts, sessionRow)
	}
	if childProgress != nil && childProgress.Total > 0 {
		auxParts = append(auxParts, renderChildProgress(*childProgress, s))
	}
	auxLine := strings.Join(auxParts, " • ")

	// Compose fixed content rows to guarantee stable card height.
	lines := []string{
		ansi.Truncate(titleLine1, maxLineLen, "…"),
		ansi.Truncate(titleLine2, maxLineLen, "…"),
		ansi.Truncate(badgeLine, maxLineLen, "…"),
		ansi.Truncate(auxLine, maxLineLen, "…"),
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)

	return cardStyle.Render(content)
}

func renderTitleLines(cursor string, issueID string, title string, maxLineLen int) (string, string) {
	if maxLineLen < 1 {
		return "", ""
	}

	prefix := cursor + issueID + " "
	prefixRunes := []rune(prefix)
	titleRunes := []rune(title)

	firstLineCapacity := maxLineLen - len(prefixRunes)
	if firstLineCapacity <= 0 {
		return string(prefixRunes[:maxLineLen]), ""
	}

	if len(titleRunes) <= firstLineCapacity {
		return prefix + title, ""
	}

	firstLine := prefix + string(titleRunes[:firstLineCapacity])
	remaining := titleRunes[firstLineCapacity:]
	if len(remaining) <= maxLineLen {
		return firstLine, string(remaining)
	}
	if maxLineLen == 1 {
		return firstLine, "…"
	}
	return firstLine, string(remaining[:maxLineLen-1]) + "…"
}

// renderSessionStatus renders the session status line with icon and elapsed time
func renderSessionStatus(session *domain.Session, s *styles.Styles) string {
	icon := session.State.Icon()

	// Elapsed time if active and started
	var elapsed string
	if session.StartedAt != nil && session.State == domain.SessionBusy {
		d := time.Since(*session.StartedAt)
		elapsed = formatDuration(d)
	}

	stateStyle := s.SessionState(session.State)
	if elapsed != "" {
		return stateStyle.Render(fmt.Sprintf("%s %s", icon, elapsed))
	}
	return stateStyle.Render(icon)
}

// formatDuration formats a duration as "2h 34m" or "45m"
func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60

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
	percent := float64(progress.Done) / float64(progress.Total)
	barWidth := 6
	filled := int(percent * float64(barWidth))
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	empty := barWidth - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	return s.EpicProgress.Render(fmt.Sprintf("[%d/%d] %s", progress.Done, progress.Total, bar))
}

// RenderCard is the exported version for testing
func RenderCard(task domain.Task, isCursor bool, isSelected bool, width int, s *styles.Styles) string {
	return renderCard(task, isCursor, isSelected, width, nil, nil, false, s)
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
	cardLines := lipgloss.Height(renderCard(sample, false, false, width, nil, nil, false, s))
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
