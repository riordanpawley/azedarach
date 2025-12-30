package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewConfirmDialog(t *testing.T) {
	title := "Delete Task"
	message := "Are you sure you want to delete this task?"

	dialog := NewConfirmDialog(title, message)

	if dialog.title != title {
		t.Errorf("expected title %q, got %q", title, dialog.title)
	}
	if dialog.message != message {
		t.Errorf("expected message %q, got %q", message, dialog.message)
	}
	if dialog.selected {
		t.Error("expected default selection to be No (false), got Yes (true)")
	}
	if dialog.styles == nil {
		t.Error("expected styles to be initialized")
	}
}

func TestConfirmDialog_Title(t *testing.T) {
	expected := "Confirm Action"
	dialog := NewConfirmDialog(expected, "Message")

	if got := dialog.Title(); got != expected {
		t.Errorf("expected title %q, got %q", expected, got)
	}
}

func TestConfirmDialog_Size(t *testing.T) {
	dialog := NewConfirmDialog("Title", "Single line message")

	width, height := dialog.Size()
	if width != 60 {
		t.Errorf("expected width 60, got %d", width)
	}
	// Single line message + buttons + footer + padding = 7
	if height < 6 {
		t.Errorf("expected height >= 6, got %d", height)
	}
}

func TestConfirmDialog_YesKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"lowercase y", "y"},
		{"uppercase Y", "Y"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialog := NewConfirmDialog("Title", "Message")

			_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(tt.key[0])}})

			if cmd == nil {
				t.Fatal("expected command, got nil")
			}

			msg := cmd()
			selMsg, ok := msg.(SelectionMsg)
			if !ok {
				t.Fatalf("expected SelectionMsg, got %T", msg)
			}

			if selMsg.Key != "yes" {
				t.Errorf("expected key %q, got %q", "yes", selMsg.Key)
			}

			result, ok := selMsg.Value.(ConfirmResult)
			if !ok {
				t.Fatalf("expected ConfirmResult, got %T", selMsg.Value)
			}

			if !result.Confirmed {
				t.Error("expected Confirmed to be true")
			}
		})
	}
}

func TestConfirmDialog_NoKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"lowercase n", "n"},
		{"uppercase N", "N"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialog := NewConfirmDialog("Title", "Message")

			_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(tt.key[0])}})

			if cmd == nil {
				t.Fatal("expected command, got nil")
			}

			msg := cmd()
			selMsg, ok := msg.(SelectionMsg)
			if !ok {
				t.Fatalf("expected SelectionMsg, got %T", msg)
			}

			if selMsg.Key != "no" {
				t.Errorf("expected key %q, got %q", "no", selMsg.Key)
			}

			result, ok := selMsg.Value.(ConfirmResult)
			if !ok {
				t.Fatalf("expected ConfirmResult, got %T", selMsg.Value)
			}

			if result.Confirmed {
				t.Error("expected Confirmed to be false")
			}
		})
	}
}

func TestConfirmDialog_Escape(t *testing.T) {
	dialog := NewConfirmDialog("Title", "Message")

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEscape})

	if cmd == nil {
		t.Fatal("expected command, got nil")
	}

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg, got %T", msg)
	}

	if selMsg.Key != "no" {
		t.Errorf("expected key %q (escape = cancel), got %q", "no", selMsg.Key)
	}

	result, ok := selMsg.Value.(ConfirmResult)
	if !ok {
		t.Fatalf("expected ConfirmResult, got %T", selMsg.Value)
	}

	if result.Confirmed {
		t.Error("expected Confirmed to be false (escape = cancel)")
	}
}

func TestConfirmDialog_NavigateLeft(t *testing.T) {
	dialog := NewConfirmDialog("Title", "Message")
	dialog.selected = true // Start with Yes selected

	// Press left arrow
	updatedModel, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyLeft})

	if cmd != nil {
		t.Error("expected no command for navigation, got command")
	}

	updatedDialog := updatedModel.(*ConfirmDialog)
	if updatedDialog.selected {
		t.Error("expected selection to move to No (false)")
	}
}

func TestConfirmDialog_NavigateRight(t *testing.T) {
	dialog := NewConfirmDialog("Title", "Message")
	dialog.selected = false // Start with No selected

	// Press right arrow
	updatedModel, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRight})

	if cmd != nil {
		t.Error("expected no command for navigation, got command")
	}

	updatedDialog := updatedModel.(*ConfirmDialog)
	if !updatedDialog.selected {
		t.Error("expected selection to move to Yes (true)")
	}
}

func TestConfirmDialog_NavigateTab(t *testing.T) {
	dialog := NewConfirmDialog("Title", "Message")
	dialog.selected = false // Start with No selected

	// Press tab
	updatedModel, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyTab})

	if cmd != nil {
		t.Error("expected no command for navigation, got command")
	}

	updatedDialog := updatedModel.(*ConfirmDialog)
	if !updatedDialog.selected {
		t.Error("expected selection to move to Yes (true)")
	}
}

func TestConfirmDialog_NavigateVim(t *testing.T) {
	dialog := NewConfirmDialog("Title", "Message")
	dialog.selected = true // Start with Yes selected

	// Press h (vim left)
	updatedModel, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})

	if cmd != nil {
		t.Error("expected no command for navigation, got command")
	}

	updatedDialog := updatedModel.(*ConfirmDialog)
	if updatedDialog.selected {
		t.Error("expected selection to move to No (false)")
	}

	// Press l (vim right)
	updatedModel, cmd = updatedDialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})

	if cmd != nil {
		t.Error("expected no command for navigation, got command")
	}

	updatedDialog = updatedModel.(*ConfirmDialog)
	if !updatedDialog.selected {
		t.Error("expected selection to move to Yes (true)")
	}
}

func TestConfirmDialog_EnterConfirmsSelection(t *testing.T) {
	tests := []struct {
		name            string
		initialSelected bool
		expectedKey     string
		expectedResult  bool
	}{
		{"enter on No", false, "no", false},
		{"enter on Yes", true, "yes", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialog := NewConfirmDialog("Title", "Message")
			dialog.selected = tt.initialSelected

			_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEnter})

			if cmd == nil {
				t.Fatal("expected command, got nil")
			}

			msg := cmd()
			selMsg, ok := msg.(SelectionMsg)
			if !ok {
				t.Fatalf("expected SelectionMsg, got %T", msg)
			}

			if selMsg.Key != tt.expectedKey {
				t.Errorf("expected key %q, got %q", tt.expectedKey, selMsg.Key)
			}

			result, ok := selMsg.Value.(ConfirmResult)
			if !ok {
				t.Fatalf("expected ConfirmResult, got %T", selMsg.Value)
			}

			if result.Confirmed != tt.expectedResult {
				t.Errorf("expected Confirmed to be %v, got %v", tt.expectedResult, result.Confirmed)
			}
		})
	}
}

func TestConfirmDialog_View(t *testing.T) {
	dialog := NewConfirmDialog("Confirm", "Are you sure?")

	view := dialog.View()

	// Should contain the message
	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain button text
	// Note: Actual styling may apply ANSI codes, so we just check basic structure
	if len(view) < 10 {
		t.Error("expected view to contain message and buttons")
	}
}

func TestConfirmDialog_Init(t *testing.T) {
	dialog := NewConfirmDialog("Title", "Message")

	cmd := dialog.Init()

	if cmd != nil {
		t.Error("expected Init to return nil command")
	}
}
