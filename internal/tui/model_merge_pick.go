package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

// mergePickState tracks the in-progress board-driven merge target selection.
// It mirrors jumpMode in spirit: a short-lived overlay-free mode that
// intercepts key handling while active and lets the user pick a target by
// navigating the existing kanban board.
type mergePickState struct {
	sourceID   string
	candidates map[string]struct{} // task IDs eligible as merge targets
	hasBase    bool                // whether the configured base branch is selectable
}

func (s *mergePickState) isCandidate(taskID string) bool {
	if s == nil {
		return false
	}
	_, ok := s.candidates[strings.TrimSpace(taskID)]
	return ok
}

// openMergeTargetSelection starts the in-board merge target picker. It mirrors
// the older overlay-based flow (still used elsewhere for the upstream-source
// picker) but keeps the kanban visible so the user can navigate to a target
// card directly.
func (m *Model) openMergeTargetSelection(task *domain.Task) tea.Cmd {
	if task == nil {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No focused issue to merge",
			Expires: time.Now().Add(3 * time.Second),
		})
		return nil
	}

	// Exit action mode so the board responds to plain navigation keys while
	// the user picks a target. The picker has its own dedicated key handler.
	if m.editor != nil && m.editor.IsAction() {
		m.editor.EnterNormal()
	}

	candidates := m.getMergeCandidates(task)
	state := &mergePickState{
		sourceID:   task.ID.String(),
		candidates: make(map[string]struct{}, len(candidates)),
	}
	for _, c := range candidates {
		if c.IsMain {
			state.hasBase = true
			continue
		}
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		state.candidates[id] = struct{}{}
	}
	m.mergePickMode = state
	return nil
}

func (m *Model) clearMergePickMode() {
	m.mergePickMode = nil
}

// handleMergePickMode routes key input while the board-driven merge picker is
// active. The board itself keeps rendering normally; only navigation, enter,
// the base-branch hotkey, and cancel are interpreted here.
func (m Model) handleMergePickMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.mergePickMode
	if state == nil {
		return m, nil
	}

	switch msg.String() {
	case "esc", "q":
		m.clearMergePickMode()
		return m, nil
	case "enter":
		return m.confirmMergePickAtCursor()
	case "B", "0":
		if !state.hasBase {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Base branch is not an eligible merge target",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		return m.confirmMergePick(mergeBaseTargetID)
	}

	m.applyBoardNavigationKey(msg)
	return m, nil
}

// applyBoardNavigationKey applies one of the configured Normal-mode navigation
// actions (cursor moves, half-page scroll) if the key maps to one. Used by
// both Normal mode and transient overlay-free modes (e.g. merge pick) that
// share the same board navigation. Returns true when the key was handled.
func (m *Model) applyBoardNavigationKey(msg tea.KeyMsg) bool {
	action, ok := keybinds.LookupAction(types.ModeNormal, msg.String())
	if !ok {
		return false
	}
	columns := m.buildColumns()
	return m.applyBoardNavigationAction(action, columns)
}

func (m *Model) applyBoardNavigationAction(action keybinds.ActionID, columns []board.Column) bool {
	switch action {
	case keybinds.ActionMoveDown:
		m.nav.MoveDown(columns)
	case keybinds.ActionMoveUp:
		m.nav.MoveUp(columns)
	case keybinds.ActionMoveLeft:
		m.nav.MoveLeft(columns)
	case keybinds.ActionMoveRight:
		m.nav.MoveRight(columns)
	case keybinds.ActionHalfPageDown:
		m.nav.HalfPageDown(columns, m.halfPage())
	case keybinds.ActionHalfPageUp:
		m.nav.HalfPageUp(columns, m.halfPage())
	default:
		return false
	}
	m.ensureCursorVisible(columns)
	return true
}

func (m Model) confirmMergePickAtCursor() (tea.Model, tea.Cmd) {
	state := m.mergePickMode
	if state == nil {
		return m, nil
	}
	columns := m.buildColumns()
	task, _ := m.nav.GetCurrentTask(columns)
	if task == nil {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "Move the cursor to an eligible card and press Enter",
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}
	if !state.isCandidate(task.ID.String()) {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: fmt.Sprintf("%s is not an eligible merge target", task.ID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}
	return m.confirmMergePick(task.ID.String())
}

func (m Model) confirmMergePick(targetID string) (tea.Model, tea.Cmd) {
	state := m.mergePickMode
	if state == nil {
		return m, nil
	}
	sourceID := state.sourceID
	m.clearMergePickMode()
	return m.handleMergeTargetSelection(overlay.MergeTargetSelectedMsg{
		SourceID: sourceID,
		TargetID: targetID,
	})
}

// renderMergePickToolbar paints a slim banner above the board when the picker
// is active so users know why navigation no longer behaves like normal mode.
func (m Model) renderMergePickToolbar() string {
	state := m.mergePickMode
	if state == nil {
		return ""
	}
	left := m.styles.OverlayTitle.Render("Merge pick")
	body := m.styles.MenuItem.Render(fmt.Sprintf("Pick target for %s — navigate cards and press Enter", state.sourceID))
	hint := "Enter: confirm  Esc: cancel"
	if state.hasBase {
		hint = "Enter: confirm  B: base branch  Esc: cancel"
	}
	right := m.styles.StatusHint.Render(hint)
	return lipgloss.JoinHorizontal(lipgloss.Left, left+"  ", body+"  ", right)
}

// mergePickCandidatesByTask exposes the active candidate set as a renderable
// map for the board layer. Returns nil when the picker is inactive so the
// renderer can skip the candidate accent entirely.
func (m Model) mergePickCandidatesByTask() map[string]bool {
	state := m.mergePickMode
	if state == nil || len(state.candidates) == 0 {
		return nil
	}
	out := make(map[string]bool, len(state.candidates))
	for id := range state.candidates {
		out[id] = true
	}
	return out
}
