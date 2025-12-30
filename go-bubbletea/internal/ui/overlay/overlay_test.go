package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestOverlayInterface verifies that mockOverlay implements Overlay
func TestOverlayInterface(t *testing.T) {
	var _ Overlay = mockOverlay{}
}

// TestOverlayTitle tests that Title() returns the expected value
func TestOverlayTitle(t *testing.T) {
	overlay := mockOverlay{title: "Test Title", width: 40, height: 10}

	if overlay.Title() != "Test Title" {
		t.Errorf("Expected title 'Test Title', got '%s'", overlay.Title())
	}
}

// TestOverlaySize tests that Size() returns the expected dimensions
func TestOverlaySize(t *testing.T) {
	overlay := mockOverlay{title: "Test", width: 60, height: 25}

	w, h := overlay.Size()
	if w != 60 {
		t.Errorf("Expected width 60, got %d", w)
	}
	if h != 25 {
		t.Errorf("Expected height 25, got %d", h)
	}
}

// TestOverlayInit tests that Init() returns the expected command
func TestOverlayInit(t *testing.T) {
	overlay := mockOverlay{title: "Test", width: 40, height: 10}

	cmd := overlay.Init()
	if cmd != nil {
		t.Error("mockOverlay Init should return nil")
	}
}

// TestOverlayView tests that View() returns the expected string
func TestOverlayView(t *testing.T) {
	overlay := mockOverlay{title: "Test View", width: 40, height: 10}

	view := overlay.View()
	if view != "Test View" {
		t.Errorf("Expected view 'Test View', got '%s'", view)
	}
}

// TestOverlayUpdate tests that Update() handles messages correctly
func TestOverlayUpdate(t *testing.T) {
	overlay := mockOverlay{title: "Test", width: 40, height: 10, value: "test-value"}

	// Test with regular key
	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Error("Regular key should return nil cmd")
	}

	newOverlay, ok := model.(mockOverlay)
	if !ok {
		t.Fatal("Update should return mockOverlay")
	}
	if newOverlay.title != "Test" {
		t.Error("Update should preserve overlay state")
	}
}

// TestCloseOverlayMsg tests that CloseOverlayMsg can be created
func TestCloseOverlayMsg(t *testing.T) {
	msg := CloseOverlayMsg{}

	// Just verify it can be created and used as tea.Msg
	var _ tea.Msg = msg
}

// TestSelectionMsg tests that SelectionMsg can be created with different types
func TestSelectionMsg(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value any
	}{
		{"string value", "key1", "string-value"},
		{"int value", "key2", 42},
		{"bool value", "key3", true},
		{"struct value", "key4", mockOverlay{title: "nested"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := SelectionMsg{
				Key:   tt.key,
				Value: tt.value,
			}

			if msg.Key != tt.key {
				t.Errorf("Expected key '%s', got '%s'", tt.key, msg.Key)
			}

			// Value comparison depends on type
			switch expected := tt.value.(type) {
			case string:
				if msg.Value != expected {
					t.Errorf("Expected value '%s', got '%v'", expected, msg.Value)
				}
			case int:
				if msg.Value != expected {
					t.Errorf("Expected value %d, got %v", expected, msg.Value)
				}
			case bool:
				if msg.Value != expected {
					t.Errorf("Expected value %t, got %v", expected, msg.Value)
				}
			}
		})
	}
}

// TestOverlayKeyHandling tests the mock overlay's key handling
func TestOverlayKeyHandling(t *testing.T) {
	overlay := mockOverlay{title: "Test", width: 40, height: 10, value: "result"}

	// Test enter key
	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter, Runes: []rune{'\r'}})
	if cmd == nil {
		t.Error("Enter key should return a command")
	}

	msg := cmd()
	selectionMsg, ok := msg.(SelectionMsg)
	if !ok {
		t.Fatalf("Expected SelectionMsg, got %T", msg)
	}

	if selectionMsg.Key != "test" {
		t.Errorf("Expected key 'test', got '%s'", selectionMsg.Key)
	}
	if selectionMsg.Value != "result" {
		t.Errorf("Expected value 'result', got '%v'", selectionMsg.Value)
	}

	// Verify model unchanged
	newOverlay, ok := model.(mockOverlay)
	if !ok {
		t.Fatal("Update should return mockOverlay")
	}
	if newOverlay.title != "Test" {
		t.Error("Update should preserve overlay state")
	}

	// Test escape key
	model, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Error("Escape key should return a command")
	}

	msg = cmd()
	_, ok = msg.(CloseOverlayMsg)
	if !ok {
		t.Fatalf("Expected CloseOverlayMsg, got %T", msg)
	}
}
