package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

// sendE2EKey exercises the same key-routing boundary as a running Bubble Tea
// program. Immediate command messages are fed back through Update so overlay
// close commands and resize initialization complete as they do at runtime.
func sendE2EKey(t *testing.T, model Model, key tea.KeyMsg) Model {
	t.Helper()

	next, cmd := model.Update(key)
	model = next.(Model)
	commands := []tea.Cmd{cmd}
	for len(commands) > 0 {
		cmd = commands[0]
		commands = commands[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if msg == nil {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			commands = append(commands, batch...)
			continue
		}
		next, cmd = model.Update(msg)
		model = next.(Model)
		commands = append(commands, cmd)
	}
	return model
}

func e2eStatusLine(model Model) string {
	view := ansi.Strip(model.View())
	lines := strings.Split(view, "\n")
	return lines[len(lines)-1]
}

func TestKeyboardE2EBoardAndCompactNavigation(t *testing.T) {
	tests := []struct {
		name     string
		view     domain.BoardView
		startID  string
		startCol int
		keys     []tea.KeyMsg
		wantID   string
		wantMode types.Mode
	}{
		{
			name:     "column board crosses tasks and columns",
			view:     domain.DefaultBoardView(),
			startID:  "az-1",
			startCol: 0,
			keys: []tea.KeyMsg{
				{Type: tea.KeyDown},
				{Type: tea.KeyRight},
			},
			wantID:   "az-3",
			wantMode: types.ModeNormal,
		},
		{
			name:     "compact tree moves across flattened tasks",
			view:     domain.TreeBoardView(),
			startID:  "az-1",
			startCol: 0,
			keys: []tea.KeyMsg{
				{Type: tea.KeyDown},
				{Type: tea.KeyDown},
			},
			wantID:   "az-3",
			wantMode: types.ModeNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newTestModel()
			model.loading = false
			model.boardView = tt.view
			projection, err := domain.ProjectTasksByBoardView(tt.view, model.tasks)
			if err != nil {
				t.Fatalf("project test tasks: %v", err)
			}
			model.boardProjection = projection
			model.boardOrdered = projection.OrderedTasks()
			startID, wantID := tt.startID, tt.wantID
			if tt.view.Layout == domain.BoardViewLayoutTreeList {
				if len(model.boardOrdered) < 3 {
					t.Fatalf("compact projection has %d tasks, want at least 3", len(model.boardOrdered))
				}
				startID = model.boardOrdered[0].ID.String()
				wantID = model.boardOrdered[2].ID.String()
			}
			model.nav.SelectTask(startID, tt.startCol)

			for _, key := range tt.keys {
				model = sendE2EKey(t, model, key)
			}

			if got := model.nav.GetCursor().TaskID; got != wantID {
				t.Fatalf("cursor task after real key sequence = %q, want %q", got, wantID)
			}
			if got := model.statusBarMode(); got != tt.wantMode {
				t.Fatalf("status mode = %v, want %v", got, tt.wantMode)
			}
			if status := e2eStatusLine(model); !strings.Contains(status, tt.wantMode.String()) {
				t.Fatalf("rendered status line %q does not contain mode %q", status, tt.wantMode.String())
			}
		})
	}
}

func TestKeyboardE2EOverlayOpenCloseAndModeTransitions(t *testing.T) {
	model := newTestModel()
	model.loading = false
	model.nav.SelectTask("az-1", 0)

	model = sendE2EKey(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if _, ok := model.overlayStack.Current().(*overlay.HelpOverlay); !ok {
		t.Fatalf("? opened %T, want HelpOverlay", model.overlayStack.Current())
	}
	if status := e2eStatusLine(model); !strings.Contains(status, types.ModeNormal.String()) || !strings.Contains(status, "close") {
		t.Fatalf("help status line = %q, want normal mode and close hint", status)
	}

	model = sendE2EKey(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if !model.overlayStack.IsEmpty() {
		t.Fatalf("esc left overlay open: %T", model.overlayStack.Current())
	}

	model = sendE2EKey(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if _, ok := model.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); !ok {
		t.Fatalf("space opened %T, want TaskWorkspaceOverlay", model.overlayStack.Current())
	}
	if status := e2eStatusLine(model); !strings.Contains(status, types.ModeAction.String()) {
		t.Fatalf("workspace status line = %q, want action mode", status)
	}

	model = sendE2EKey(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if !model.overlayStack.IsEmpty() {
		t.Fatalf("esc left workspace open: %T", model.overlayStack.Current())
	}

	model = sendE2EKey(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if !model.editor.IsSelect() {
		t.Fatalf("v mode = %v, want select", model.editor.GetMode())
	}
	if status := e2eStatusLine(model); !strings.Contains(status, types.ModeSelect.String()) {
		t.Fatalf("select status line %q does not contain %q", status, types.ModeSelect.String())
	}

	model = sendE2EKey(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if !model.editor.IsNormal() {
		t.Fatalf("esc mode = %v, want normal", model.editor.GetMode())
	}
	if status := e2eStatusLine(model); !strings.Contains(status, types.ModeNormal.String()) {
		t.Fatalf("normal status line %q does not contain %q", status, types.ModeNormal.String())
	}
}
