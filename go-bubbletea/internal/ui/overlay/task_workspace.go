package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

type taskWorkspaceFocus int

const (
	taskWorkspaceFocusDetail taskWorkspaceFocus = iota
	taskWorkspaceFocusActions
)

// TaskWorkspaceOverlay renders full task details and actions together.
type TaskWorkspaceOverlay struct {
	detail        *DetailPanel
	actions       *ActionMenu
	focus         taskWorkspaceFocus
	overlayWidth  int
	overlayHeight int
	styles        *Styles
}

// NewTaskWorkspaceOverlay creates a large overlay with details + action panel.
func NewTaskWorkspaceOverlay(
	task domain.Task,
	session *domain.Session,
	relatedTasks []domain.Task,
	viewportWidth int,
	viewportHeight int,
) *TaskWorkspaceOverlay {
	detail := NewDetailPanel(task, session).WithRelatedTasks(relatedTasks)
	actions := NewActionMenu(task, session).WithRelatedTasks(relatedTasks)

	overlayWidth := max(84, viewportWidth-6)
	overlayHeight := max(20, viewportHeight-8)
	if viewportWidth > 0 {
		overlayWidth = min(overlayWidth, viewportWidth-8)
	}
	if viewportHeight > 0 {
		overlayHeight = min(overlayHeight, viewportHeight-6)
	}

	detail.viewHeight = max(8, overlayHeight-8)

	return &TaskWorkspaceOverlay{
		detail:        detail,
		actions:       actions,
		focus:         taskWorkspaceFocusDetail,
		overlayWidth:  overlayWidth,
		overlayHeight: overlayHeight,
		styles:        New(),
	}
}

func (w *TaskWorkspaceOverlay) Init() tea.Cmd {
	return nil
}

func (w *TaskWorkspaceOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return w, func() tea.Msg { return CloseOverlayMsg{} }
		case "tab", "h", "l":
			w.toggleFocus()
			return w, nil
		case "enter":
			if w.focus == taskWorkspaceFocusDetail {
				w.focus = taskWorkspaceFocusActions
				return w, nil
			}
			return w, w.actions.selectCurrentAction()
		case "j", "down":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorDown()
			} else if w.detail.scrollY < w.detail.maxScroll() {
				w.detail.scrollY++
			}
			return w, nil
		case "k", "up":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorUp()
			} else if w.detail.scrollY > 0 {
				w.detail.scrollY--
			}
			return w, nil
		case "g", "home":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.cursor = 0
				return w, nil
			}
			w.detail.scrollY = 0
			return w, nil
		case "G", "end":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.cursor = len(w.actions.actions) - 1
				w.actions.moveCursorUp()
				return w, nil
			}
			w.detail.scrollY = w.detail.maxScroll()
			return w, nil
		}

		if cmd := w.actions.selectByKey(msg.String()); cmd != nil {
			return w, cmd
		}
	}
	return w, nil
}

func (w *TaskWorkspaceOverlay) View() string {
	innerWidth := max(32, w.overlayWidth-2)
	bodyHeight := max(8, w.overlayHeight-4)
	gap := 1
	minLeft := 32
	minRight := 22
	leftWidth := (innerWidth * 2) / 3
	maxLeft := max(minLeft, innerWidth-gap-minRight)
	if leftWidth > maxLeft {
		leftWidth = maxLeft
	}
	if leftWidth < minLeft {
		leftWidth = minLeft
	}
	rightWidth := innerWidth - gap - leftWidth
	if rightWidth < minRight {
		rightWidth = minRight
		leftWidth = max(minLeft, innerWidth-gap-rightWidth)
	}

	leftBorder := styles.Overlay0
	rightBorder := styles.Overlay0
	if w.focus == taskWorkspaceFocusDetail {
		leftBorder = styles.Blue
	} else {
		rightBorder = styles.Blue
	}

	detailView := lipgloss.NewStyle().
		Width(leftWidth).
		MaxWidth(leftWidth).
		Height(bodyHeight).
		MaxHeight(bodyHeight).
		Border(lipgloss.NormalBorder()).
		BorderForeground(leftBorder).
		Padding(0, 1).
		Render(w.detail.View())

	actionsHeader := w.styles.MenuItemActive.Render("Actions")
	actionsBody := lipgloss.JoinVertical(
		lipgloss.Left,
		actionsHeader,
		w.styles.Separator.Render(strings.Repeat("─", max(6, rightWidth-6))),
		w.actions.viewActionsOnly(),
	)
	actionsView := lipgloss.NewStyle().
		Width(rightWidth).
		MaxWidth(rightWidth).
		Height(bodyHeight).
		MaxHeight(bodyHeight).
		Border(lipgloss.NormalBorder()).
		BorderForeground(rightBorder).
		Padding(0, 1).
		Render(actionsBody)

	body := lipgloss.JoinHorizontal(lipgloss.Top, detailView, lipgloss.NewStyle().Width(gap).Render(""), actionsView)
	return lipgloss.NewStyle().
		Width(w.overlayWidth).
		Height(w.overlayHeight).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Surface2).
		Render(body)
}

func (w *TaskWorkspaceOverlay) Title() string {
	return "Task Workspace"
}

func (w *TaskWorkspaceOverlay) Size() (width, height int) {
	return w.overlayWidth, w.overlayHeight
}

func (w *TaskWorkspaceOverlay) StatusMode() types.Mode {
	return types.ModeAction
}

func (w *TaskWorkspaceOverlay) UsesAppFrame() bool {
	return false
}

func (w *TaskWorkspaceOverlay) toggleFocus() {
	if w.focus == taskWorkspaceFocusDetail {
		w.focus = taskWorkspaceFocusActions
		return
	}
	w.focus = taskWorkspaceFocusDetail
}
