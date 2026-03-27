package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
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

	overlayWidth := max(84, viewportWidth)
	overlayHeight := max(16, viewportHeight-1)
	if viewportWidth > 0 {
		overlayWidth = min(overlayWidth, viewportWidth)
	}
	if viewportHeight > 0 {
		overlayHeight = min(overlayHeight, viewportHeight-1)
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
			if w.detail.scrollY < w.detail.maxScroll() {
				w.detail.scrollY++
			}
			return w, nil
		case "k", "up":
			if w.detail.scrollY > 0 {
				w.detail.scrollY--
			}
			return w, nil
		case "n":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorDown()
			}
			return w, nil
		case "p":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorUp()
			}
			return w, nil
		case "ctrl+d":
			w.detail.scrollY = min(w.detail.maxScroll(), w.detail.scrollY+w.halfPageStep())
			return w, nil
		case "ctrl+u":
			w.detail.scrollY = max(0, w.detail.scrollY-w.halfPageStep())
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
	contentWidth := max(24, w.overlayWidth-2)
	contentHeight := max(8, w.overlayHeight-2)
	titleLine := w.styles.MenuItemActive.Render("Task Workspace")
	separator := w.styles.Separator.Render(strings.Repeat("─", max(6, contentWidth)))

	bodyHeight := max(6, contentHeight-2)
	gap := 1
	minLeft := 28
	minRight := 20
	usableWidth := max(16, contentWidth-gap)
	leftWidth := (usableWidth * 2) / 3
	maxLeft := max(minLeft, usableWidth-minRight)
	if leftWidth > maxLeft {
		leftWidth = maxLeft
	}
	if leftWidth < minLeft {
		leftWidth = minLeft
	}
	rightWidth := usableWidth - leftWidth
	if rightWidth < minRight {
		rightWidth = minRight
		leftWidth = max(minLeft, usableWidth-rightWidth)
	}
	w.detail.viewHeight = max(6, bodyHeight)
	w.detail.wrapWidth = max(20, leftWidth-2)

	detailStyle := lipgloss.NewStyle()
	if w.focus == taskWorkspaceFocusDetail {
		detailStyle = detailStyle.BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(styles.Blue).PaddingLeft(1)
	}
	detailView := detailStyle.
		Width(leftWidth).
		MaxWidth(leftWidth).
		Height(bodyHeight).
		MaxHeight(bodyHeight).
		Render(w.detail.View())

	actionsHeader := w.styles.MenuItemActive.Render("Actions")
	actionsBody := lipgloss.JoinVertical(
		lipgloss.Left,
		actionsHeader,
		w.styles.Separator.Render(strings.Repeat("─", max(6, rightWidth))),
		w.actions.viewActionsOnly(),
	)
	actionStyle := lipgloss.NewStyle()
	if w.focus == taskWorkspaceFocusActions {
		actionStyle = actionStyle.BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(styles.Blue).PaddingLeft(1)
	}
	actionsView := actionStyle.
		Width(rightWidth).
		MaxWidth(rightWidth).
		Height(bodyHeight).
		MaxHeight(bodyHeight).
		Render(actionsBody)

	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		detailView,
		lipgloss.NewStyle().Width(gap).Render(""),
		actionsView,
	)
	content := lipgloss.JoinVertical(lipgloss.Left, titleLine, separator, body)
	return lipgloss.NewStyle().
		Width(contentWidth).
		Height(contentHeight).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Surface2).
		Render(content)
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

func (w *TaskWorkspaceOverlay) UsesFullScreen() bool {
	return true
}

func (w *TaskWorkspaceOverlay) UsesInternalTitle() bool {
	return true
}

func (w *TaskWorkspaceOverlay) StatusBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "j/k/↑/↓", Description: "scroll"},
		{Key: "ctrl+u/d", Description: "half-page"},
		{Key: "g/G", Description: "top/bottom"},
		{Key: "Tab/h/l", Description: "focus"},
		{Key: "Enter", Description: "run action"},
		{Key: "n/p", Description: "action up/down"},
		{Key: "Esc/q", Description: "close"},
	}
}

func (w *TaskWorkspaceOverlay) toggleFocus() {
	if w.focus == taskWorkspaceFocusDetail {
		w.focus = taskWorkspaceFocusActions
		return
	}
	w.focus = taskWorkspaceFocusDetail
}

func (w *TaskWorkspaceOverlay) halfPageStep() int {
	step := w.detail.viewHeight / 2
	if step < 1 {
		return 1
	}
	return step
}
