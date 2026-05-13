package board

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const statusBarHeight = 1

// Render renders the visible kanban board columns.
func Render(
	columns []Column,
	cursor Cursor,
	selectedTasks map[string]bool,
	runtimeSignalsByTask map[string]RuntimeSignals,
	childProgressByTask map[string]ChildProgress,
	phaseData map[string]phases.TaskPhaseInfo,
	showPhases bool,
	jumpLabelsByTask map[string]string,
	activeViewportStart int,
	s *styles.Styles,
	width int,
	height int,
	opts ...RenderOption,
) string {
	if len(columns) == 0 {
		return ""
	}

	cfg := renderConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	columnWidth := width / len(columns)

	columnStrings := make([]string, len(columns))
	for i, col := range columns {
		isActive := i == cursor.Column
		cursorTask := -1
		viewportStart := 0
		if isActive {
			cursorTask = cursor.Task
			viewportStart = activeViewportStart
		}

		columnStrings[i] = renderColumn(
			col.Title,
			col.Tasks,
			cursorTask,
			isActive,
			viewportStart,
			selectedTasks,
			runtimeSignalsByTask,
			childProgressByTask,
			phaseData,
			showPhases,
			jumpLabelsByTask,
			cfg.mergeCandidates,
			columnWidth,
			height,
			s,
		)
	}

	// Join columns horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top, columnStrings...)
}

// RenderOption configures optional render behavior without expanding the
// positional argument list of Render.
type RenderOption func(*renderConfig)

type renderConfig struct {
	mergeCandidates map[string]bool
}

// WithMergeCandidates marks the supplied task IDs as eligible merge targets so
// the board can paint a candidate badge while the merge-pick mode is active.
func WithMergeCandidates(candidates map[string]bool) RenderOption {
	return func(cfg *renderConfig) {
		cfg.mergeCandidates = candidates
	}
}
