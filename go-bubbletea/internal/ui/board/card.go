package board

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// renderCard renders a task card
func renderCard(task domain.Task, isCursor bool, isSelected bool, width int, s *styles.Styles) string {
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
	badgeLine := lipgloss.JoinHorizontal(lipgloss.Left, priorityBadge, " • ", typeBadge)

	content := lipgloss.JoinVertical(lipgloss.Left, titleLine, badgeLine)

	return cardStyle.Render(content)
}

// RenderCard is the exported version for testing
func RenderCard(task domain.Task, isCursor bool, isSelected bool, width int, s *styles.Styles) string {
	return renderCard(task, isCursor, isSelected, width, s)
}
