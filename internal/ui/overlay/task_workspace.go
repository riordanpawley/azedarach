package overlay

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

type taskWorkspaceFocus int

const (
	taskWorkspaceFocusDetail taskWorkspaceFocus = iota
	taskWorkspaceFocusGraph
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
	relatedTasks []domain.Task,
	mutation *TaskMutationProgress,
	viewportWidth int,
	viewportHeight int,
) *TaskWorkspaceOverlay {
	detail := NewDetailPanel(task).WithRelatedTasks(relatedTasks).WithMutationProgress(mutation)
	actions := NewActionMenu(task, task.Session).WithRelatedTasks(relatedTasks).WithoutStatusMoveActions()

	overlayWidth := viewportWidth
	overlayHeight := viewportHeight - 1
	if overlayWidth < 1 {
		overlayWidth = 84
	}
	if overlayHeight < 1 {
		overlayHeight = 24
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
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			w.overlayWidth = msg.Width
		}
		if msg.Height > 1 {
			w.overlayHeight = msg.Height - 1
		}
		return w, nil
	case tea.KeyMsg:
		w.normalizeFocus()
		switch msg.String() {
		case "esc", "q":
			return w, func() tea.Msg { return CloseOverlayMsg{} }
		case "tab":
			w.moveFocus(1)
			return w, nil
		case "shift+tab":
			w.moveFocus(-1)
			return w, nil
		case "[":
			if w.detail.GraphLinkCount() > 0 {
				w.focus = taskWorkspaceFocusGraph
				w.detail.MoveGraphCursor(-1)
			}
			return w, nil
		case "]":
			if w.detail.GraphLinkCount() > 0 {
				w.focus = taskWorkspaceFocusGraph
				w.detail.MoveGraphCursor(1)
			}
			return w, nil
		case "enter":
			if w.focus == taskWorkspaceFocusGraph {
				if taskID, ok := w.detail.SelectedGraphTaskID(); ok {
					return w, func() tea.Msg {
						return SelectionMsg{Key: "task_workspace_open_task", Value: taskID}
					}
				}
				return w, nil
			}
			if w.focus == taskWorkspaceFocusDetail {
				return w, nil
			}
			return w, w.actions.selectCurrentAction()
		case "h", "left", "<":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorUp()
			} else if w.focus == taskWorkspaceFocusGraph {
				taskID, ok := w.detail.SelectedGraphTaskIDForDirection("ascendant")
				if !ok {
					return w, nil
				}
				return w, func() tea.Msg {
					return SelectionMsg{Key: "task_workspace_open_task", Value: taskID}
				}
			}
			return w, nil
		case "l", "right", ">":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorDown()
			} else if w.focus == taskWorkspaceFocusGraph {
				taskID, ok := w.detail.SelectedGraphTaskIDForDirection("descendant")
				if !ok {
					return w, nil
				}
				return w, func() tea.Msg {
					return SelectionMsg{Key: "task_workspace_open_task", Value: taskID}
				}
			}
			return w, nil
		case "j", "down":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorDown()
			} else if w.focus == taskWorkspaceFocusGraph && w.detail.GraphLinkCount() > 0 {
				w.detail.MoveGraphCursor(1)
			} else if w.detail.scrollY < w.detail.maxScroll() {
				w.detail.scrollY++
			}
			return w, nil
		case "k", "up":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.moveCursorUp()
			} else if w.focus == taskWorkspaceFocusGraph && w.detail.GraphLinkCount() > 0 {
				w.detail.MoveGraphCursor(-1)
			} else if w.detail.scrollY > 0 {
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
				w.actions.ensureCursorVisible()
				return w, nil
			}
			w.detail.scrollY = 0
			return w, nil
		case "G", "end":
			if w.focus == taskWorkspaceFocusActions {
				w.actions.cursor = len(w.actions.actions) - 1
				w.actions.moveCursorUp()
				w.actions.ensureCursorVisible()
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
	w.normalizeFocus()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            w.styles,
		width:             w.overlayWidth,
		height:            w.overlayHeight,
		title:             "Task Workspace",
		rightSectionTitle: "Actions",
		breakpoint:        76,
		gap:               1,
		minLeft:           28,
		minRight:          20,
		leftFocused:       w.focus == taskWorkspaceFocusDetail || w.focus == taskWorkspaceFocusGraph,
		rightFocused:      w.focus == taskWorkspaceFocusActions,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			w.detail.viewHeight = max(4, height)
			w.detail.graphFocused = w.focus == taskWorkspaceFocusGraph
			if mode == dialogLayoutStacked {
				w.detail.wrapWidth = max(4, width-4)
			} else {
				w.detail.wrapWidth = max(20, width-2)
			}
			return w.detail.View()
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			w.actions.setViewportRows(max(1, height-2))
			return w.actions.viewActionsOnlyWidth(max(8, width-2))
		},
	})
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
	w.normalizeFocus()
	switch w.focus {
	case taskWorkspaceFocusGraph:
		return []keybinds.Binding{
			{Key: "j/k/↑/↓", Description: "select relation"},
			{Key: "h/l/←/→/Enter", Description: "open relation"},
			{Key: "[/]", Description: "select relation"},
			{Key: "Tab", Description: "focus"},
			{Key: "r", Description: "refresh issue"},
			{Key: "V", Description: "dev servers"},
			{Key: "1/2/3/4", Description: "set status"},
			{Key: "Esc/q", Description: "close"},
		}
	case taskWorkspaceFocusActions:
		return []keybinds.Binding{
			{Key: "j/k/↑/↓", Description: "select action"},
			{Key: "Enter", Description: "run action"},
			{Key: "1/2/3/4", Description: "set status"},
			{Key: "n/p", Description: "action select"},
			{Key: "r", Description: "refresh issue"},
			{Key: "V", Description: "dev servers"},
			{Key: "Tab", Description: "focus"},
			{Key: "Esc/q", Description: "close"},
		}
	default:
		return []keybinds.Binding{
			{Key: "j/k/↑/↓", Description: "scroll"},
			{Key: "ctrl+u/d", Description: "half-page"},
			{Key: "g/G", Description: "top/bottom"},
			{Key: "Tab", Description: "focus"},
			{Key: "r", Description: "refresh issue"},
			{Key: "V", Description: "dev servers"},
			{Key: "1/2/3/4", Description: "set status"},
			{Key: "Esc/q", Description: "close"},
		}
	}
}

func (w *TaskWorkspaceOverlay) moveFocus(delta int) {
	foci := []taskWorkspaceFocus{taskWorkspaceFocusDetail}
	if w.detail.GraphLinkCount() > 0 {
		foci = append(foci, taskWorkspaceFocusGraph)
	}
	foci = append(foci, taskWorkspaceFocusActions)

	index := 0
	for i, focus := range foci {
		if focus == w.focus {
			index = i
			break
		}
	}
	next := (index + delta) % len(foci)
	if next < 0 {
		next += len(foci)
	}
	w.focus = foci[next]
}

func (w *TaskWorkspaceOverlay) normalizeFocus() {
	if w.focus == taskWorkspaceFocusGraph && w.detail.GraphLinkCount() == 0 {
		w.focus = taskWorkspaceFocusDetail
	}
}

func (w *TaskWorkspaceOverlay) halfPageStep() int {
	step := w.detail.viewHeight / 2
	if step < 1 {
		return 1
	}
	return step
}

// TaskID returns the selected task ID shown in the workspace.
func (w *TaskWorkspaceOverlay) TaskID() string {
	return w.detail.task.ID.String()
}

// SyncSnapshotFreshness updates the detail panel freshness metadata from the latest daemon snapshot.
func (w *TaskWorkspaceOverlay) SyncSnapshotFreshness(checkedAt time.Time, freshness protocol.TaskListFreshness) {
	w.detail.checkedAt = checkedAt
	w.detail.freshness = freshness
}

// SyncTask updates workspace detail/actions from refreshed task projection data.
func (w *TaskWorkspaceOverlay) SyncTask(task domain.Task, relatedTasks []domain.Task, mutation *TaskMutationProgress) {
	w.detail.task = task
	w.detail.relatedTasks = append([]domain.Task(nil), relatedTasks...)
	w.detail.mutation = cloneTaskMutationProgress(mutation)

	w.actions.task = task
	w.actions.session = task.Session
	w.actions.relatedTasks = append([]domain.Task(nil), relatedTasks...)
	w.actions.hideStatusMoveActions = true
	w.actions.actions = w.actions.buildActions()
	if len(w.actions.actions) == 0 {
		w.actions.cursor = 0
		w.actions.scrollOffset = 0
		w.normalizeFocus()
		return
	}
	if w.actions.cursor < 0 {
		w.actions.cursor = 0
	}
	if w.actions.cursor >= len(w.actions.actions) {
		w.actions.cursor = len(w.actions.actions) - 1
	}
	w.actions.ensureCursorVisible()
	w.normalizeFocus()
}
