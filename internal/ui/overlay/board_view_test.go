package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestBoardViewOverlaySelectsCurrentView(t *testing.T) {
	records := []domain.BoardViewRecord{
		{ProjectID: "proj", View: domain.DefaultBoardView(), BuiltIn: true},
		{ProjectID: "proj", View: domain.ActivityBoardView(), BuiltIn: true},
	}
	o := NewBoardViewOverlay(records, "activity")

	view := o.View()
	if !strings.Contains(view, "Activity") || !strings.Contains(view, "built-in") {
		t.Fatalf("overlay view missing board view rows:\n%s", view)
	}

	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter command = nil")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("message = %T, want SelectionMsg", cmd())
	}
	if msg.Key != BoardViewSelectKey {
		t.Fatalf("selection key = %q, want %q", msg.Key, BoardViewSelectKey)
	}
	selected, ok := msg.Value.(BoardViewSelectMsg)
	if !ok {
		t.Fatalf("selection value = %T, want BoardViewSelectMsg", msg.Value)
	}
	if selected.ViewID != "activity" {
		t.Fatalf("selected view = %q, want activity", selected.ViewID)
	}
}

func TestBoardViewOverlayStatusBindingsDescribeWorkingActions(t *testing.T) {
	o := NewBoardViewOverlay(nil, "")
	bindings := o.StatusBindings()
	got := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		got[binding.Key] = binding.Description
	}
	for key, description := range map[string]string{
		"j/k":   "view",
		"Enter": "select",
		"c":     "create",
		"d":     "duplicate",
		"e":     "edit",
		"x":     "delete",
		"h":     "hide empty",
		"Esc":   "close",
	} {
		if got[key] != description {
			t.Fatalf("status binding %q = %q, want %q (all=%+v)", key, got[key], description, bindings)
		}
	}
}

func TestBoardViewOverlayDuplicateValidatesAndSaves(t *testing.T) {
	existing := domain.DefaultBoardView()
	existing.ID = "current-copy"
	existing.Title = "Existing"
	o := NewBoardViewOverlay([]domain.BoardViewRecord{{View: domain.DefaultBoardView(), BuiltIn: true}, {View: existing}}, "current")
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if o.mode != boardViewEdit || !strings.Contains(o.editor.Value(), `"id": "current-copy-2"`) {
		t.Fatalf("duplicate did not open populated editor: mode=%v value=%s", o.mode, o.editor.Value())
	}
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("save command = nil")
	}
	msg := cmd().(SelectionMsg)
	saved := msg.Value.(BoardViewSaveMsg)
	if saved.View.ID != "current-copy-2" || len(saved.View.Columns) == 0 {
		t.Fatalf("saved view = %+v", saved.View)
	}
}

func TestBoardViewOverlayInvalidEditStaysOpenAndCancelIsSafe(t *testing.T) {
	o := NewBoardViewOverlay(nil, "")
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	o.editor.SetValue(`{"id":"bad","title":"Bad","columns":[]}`)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil || o.errorText == "" || o.mode != boardViewEdit {
		t.Fatalf("invalid save cmd=%v error=%q mode=%v", cmd, o.errorText, o.mode)
	}
	_, cmd = o.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || o.mode != boardViewBrowse {
		t.Fatal("escape should cancel without mutation")
	}
}

func TestBoardViewOverlayEditLocksRecordIDButAllowsTitleRename(t *testing.T) {
	custom := domain.DefaultBoardView()
	custom.ID = "custom"
	custom.Title = "Custom"
	o := NewBoardViewOverlay([]domain.BoardViewRecord{{View: custom}}, "custom")
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	o.editor.SetValue(strings.Replace(o.editor.Value(), `"id": "custom"`, `"id": "renamed-id"`, 1))
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil || !strings.Contains(o.errorText, "id is fixed") {
		t.Fatalf("id mutation cmd=%v error=%q", cmd, o.errorText)
	}
	o.editor.SetValue(strings.Replace(o.editor.Value(), `"id": "renamed-id"`, `"id": "custom"`, 1))
	o.editor.SetValue(strings.Replace(o.editor.Value(), `"title": "Custom"`, `"title": "Renamed"`, 1))
	_, cmd = o.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if got := cmd().(SelectionMsg).Value.(BoardViewSaveMsg).View.Title; got != "Renamed" {
		t.Fatalf("title=%q", got)
	}
}

func TestBoardViewOverlayProtectsBuiltInAndConfirmsCustomDelete(t *testing.T) {
	custom := domain.DefaultBoardView()
	custom.ID = "custom"
	custom.Title = "Custom"
	o := NewBoardViewOverlay([]domain.BoardViewRecord{{View: domain.DefaultBoardView(), BuiltIn: true}, {View: custom}}, "current")
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if o.mode != boardViewBrowse || !strings.Contains(o.errorText, "Built-in") {
		t.Fatal("built-in delete was not protected")
	}
	if !strings.Contains(o.renderDetails(100), "Built-in views cannot be deleted") {
		t.Fatal("protection error is not visible")
	}
	o.moveCursor(1)
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if o.mode != boardViewConfirmDelete {
		t.Fatal("custom delete did not request confirmation")
	}
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd != nil || o.mode != boardViewBrowse {
		t.Fatal("delete cancel should not mutate")
	}
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	_, cmd = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if got := cmd().(SelectionMsg).Value.(BoardViewDeleteMsg).ViewID; got != "custom" {
		t.Fatalf("delete id=%q", got)
	}
}

func TestBoardViewOverlayTogglesHideEmptyForCustomView(t *testing.T) {
	custom := domain.DefaultBoardView()
	custom.ID = "custom"
	custom.Title = "Custom"
	o := NewBoardViewOverlay([]domain.BoardViewRecord{{View: custom}}, "custom")
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if cmd == nil {
		t.Fatal("toggle command = nil")
	}
	saved := cmd().(SelectionMsg).Value.(BoardViewSaveMsg).View
	if !saved.Options.HideEmptyColumns {
		t.Fatal("hide-empty option was not toggled")
	}
}

func TestBoardViewOverlayResizeAppliesInModalModes(t *testing.T) {
	o := NewBoardViewOverlay(nil, "")
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	_, _ = o.Update(tea.WindowSizeMsg{Width: 48, Height: 16})
	width, height := o.Size()
	if width > 46 || height > 14 {
		t.Fatalf("edit size=%dx%d exceeds viewport", width, height)
	}
}
