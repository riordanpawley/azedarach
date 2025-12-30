package board

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// renderCard renders a task card
func renderCard(task domain.Task, isCursor bool, isSelected bool, width int, phaseInfo *phases.TaskPhaseInfo, showPhases bool, s *styles.Styles) string {
	// Choose card style based on state
	cardStyle := s.Card
	if isSelected {
		cardStyle = s.CardSelected
	} else if isCursor {
		cardStyle = s.CardActive
	}

	// Apply width
	cardStyle = cardStyle.Width(width)

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
	// Account for padding (2), border (2), and some space for badges
	maxTitleLen := width - 4
	title := task.Title
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen-1] + "…"
	}

	// Cursor indicator (▶ symbol when cursor is on this card)
	cursor := ""
	if isCursor {
		cursor = "▶"
	}

	// Build the card content
	titleLine := cursor + title

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

	// Epic progress (if epic type)
	var epicProgress string
	if task.Type == domain.TypeEpic {
		epicProgress = renderEpicProgress(task, width, s)
	}

	// Compose card content
	content := lipgloss.JoinVertical(lipgloss.Left, titleLine, badgeLine)

	if sessionRow != "" {
		content = lipgloss.JoinVertical(lipgloss.Left, content, sessionRow)
	}
	if epicProgress != "" {
		content = lipgloss.JoinVertical(lipgloss.Left, content, epicProgress)
	}

	return cardStyle.Render(content)
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

// renderEpicProgress renders the epic progress bar with completion ratio
func renderEpicProgress(task domain.Task, width int, s *styles.Styles) string {
	// TODO: Get child counts from task metadata
	// For now, use placeholder values
	completed := 3
	total := 5

	if total == 0 {
		return ""
	}

	percent := float64(completed) / float64(total)
	barWidth := 6
	filled := int(percent * float64(barWidth))
	empty := barWidth - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	return s.EpicProgress.Render(fmt.Sprintf("[%d/%d] %s", completed, total, bar))
}

// RenderCard is the exported version for testing
func RenderCard(task domain.Task, isCursor bool, isSelected bool, width int, s *styles.Styles) string {
	return renderCard(task, isCursor, isSelected, width, nil, false, s)
}
