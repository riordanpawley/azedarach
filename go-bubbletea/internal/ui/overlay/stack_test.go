package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// mockOverlay is a simple overlay implementation for testing
type mockOverlay struct {
	title  string
	width  int
	height int
	value  string
}

func (m mockOverlay) Init() tea.Cmd {
	return nil
}

func (m mockOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "enter" {
			return m, func() tea.Msg {
				return SelectionMsg{Key: "test", Value: m.value}
			}
		}
		if msg.String() == "esc" {
			return m, func() tea.Msg {
				return CloseOverlayMsg{}
			}
		}
	}
	return m, nil
}

func (m mockOverlay) View() string {
	return m.title
}

func (m mockOverlay) Title() string {
	return m.title
}

func (m mockOverlay) Size() (width, height int) {
	return m.width, m.height
}

func TestNewStack(t *testing.T) {
	stack := NewStack()
	if stack == nil {
		t.Fatal("NewStack returned nil")
	}
	if !stack.IsEmpty() {
		t.Error("New stack should be empty")
	}
}

func TestStackPush(t *testing.T) {
	stack := NewStack()
	overlay := mockOverlay{title: "Test Overlay", width: 40, height: 10}

	cmd := stack.Push(overlay)
	if cmd != nil {
		t.Error("Push should return nil cmd for mockOverlay")
	}

	if stack.IsEmpty() {
		t.Error("Stack should not be empty after push")
	}

	if stack.Current() == nil {
		t.Fatal("Current should not be nil after push")
	}

	if stack.Current().Title() != "Test Overlay" {
		t.Errorf("Expected title 'Test Overlay', got '%s'", stack.Current().Title())
	}
}

func TestStackPop(t *testing.T) {
	stack := NewStack()
	overlay1 := mockOverlay{title: "Overlay 1", width: 40, height: 10}
	overlay2 := mockOverlay{title: "Overlay 2", width: 50, height: 15}

	stack.Push(overlay1)
	stack.Push(overlay2)

	// Pop should return overlay2
	popped := stack.Pop()
	if popped == nil {
		t.Fatal("Pop returned nil")
	}
	if popped.Title() != "Overlay 2" {
		t.Errorf("Expected popped title 'Overlay 2', got '%s'", popped.Title())
	}

	// Current should now be overlay1
	if stack.Current().Title() != "Overlay 1" {
		t.Errorf("Expected current title 'Overlay 1', got '%s'", stack.Current().Title())
	}

	// Pop again should return overlay1
	popped = stack.Pop()
	if popped.Title() != "Overlay 1" {
		t.Errorf("Expected popped title 'Overlay 1', got '%s'", popped.Title())
	}

	// Stack should be empty
	if !stack.IsEmpty() {
		t.Error("Stack should be empty after popping all overlays")
	}

	// Pop on empty stack should return nil
	popped = stack.Pop()
	if popped != nil {
		t.Error("Pop on empty stack should return nil")
	}
}

func TestStackCurrent(t *testing.T) {
	stack := NewStack()

	// Current on empty stack should return nil
	if stack.Current() != nil {
		t.Error("Current on empty stack should return nil")
	}

	overlay1 := mockOverlay{title: "Overlay 1", width: 40, height: 10}
	overlay2 := mockOverlay{title: "Overlay 2", width: 50, height: 15}

	stack.Push(overlay1)
	if stack.Current().Title() != "Overlay 1" {
		t.Errorf("Expected current title 'Overlay 1', got '%s'", stack.Current().Title())
	}

	stack.Push(overlay2)
	if stack.Current().Title() != "Overlay 2" {
		t.Errorf("Expected current title 'Overlay 2', got '%s'", stack.Current().Title())
	}

	// Current should not modify the stack
	stack.Current()
	if stack.Current().Title() != "Overlay 2" {
		t.Error("Current should not modify the stack")
	}
}

func TestStackIsEmpty(t *testing.T) {
	stack := NewStack()

	if !stack.IsEmpty() {
		t.Error("New stack should be empty")
	}

	overlay := mockOverlay{title: "Test", width: 40, height: 10}
	stack.Push(overlay)

	if stack.IsEmpty() {
		t.Error("Stack should not be empty after push")
	}

	stack.Pop()

	if !stack.IsEmpty() {
		t.Error("Stack should be empty after popping all overlays")
	}
}

func TestStackClear(t *testing.T) {
	stack := NewStack()

	overlay1 := mockOverlay{title: "Overlay 1", width: 40, height: 10}
	overlay2 := mockOverlay{title: "Overlay 2", width: 50, height: 15}
	overlay3 := mockOverlay{title: "Overlay 3", width: 60, height: 20}

	stack.Push(overlay1)
	stack.Push(overlay2)
	stack.Push(overlay3)

	if stack.IsEmpty() {
		t.Error("Stack should not be empty after pushes")
	}

	stack.Clear()

	if !stack.IsEmpty() {
		t.Error("Stack should be empty after clear")
	}

	if stack.Current() != nil {
		t.Error("Current should return nil after clear")
	}
}

func TestStackUpdate(t *testing.T) {
	stack := NewStack()

	// Update on empty stack should return nil
	cmd := stack.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("Update on empty stack should return nil")
	}

	overlay := mockOverlay{title: "Test", width: 40, height: 10, value: "selected"}
	stack.Push(overlay)

	// Send a regular message
	cmd = stack.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Error("Update with regular key should return nil for mockOverlay")
	}

	// Current should still be the same overlay
	if stack.Current().Title() != "Test" {
		t.Error("Current overlay should not change with regular update")
	}
}

func TestStackUpdateWithCloseMsg(t *testing.T) {
	stack := NewStack()

	overlay1 := mockOverlay{title: "Overlay 1", width: 40, height: 10}
	overlay2 := mockOverlay{title: "Overlay 2", width: 50, height: 15}

	stack.Push(overlay1)
	stack.Push(overlay2)

	// Send CloseOverlayMsg
	cmd := stack.Update(CloseOverlayMsg{})
	if cmd != nil {
		t.Error("Update with CloseOverlayMsg should return nil")
	}

	// Should have popped overlay2
	if stack.Current().Title() != "Overlay 1" {
		t.Errorf("Expected current title 'Overlay 1' after close, got '%s'", stack.Current().Title())
	}

	// Send another CloseOverlayMsg
	stack.Update(CloseOverlayMsg{})

	// Stack should be empty
	if !stack.IsEmpty() {
		t.Error("Stack should be empty after closing all overlays")
	}
}

func TestStackUpdateForwardsMessages(t *testing.T) {
	stack := NewStack()

	overlay := mockOverlay{title: "Test", width: 40, height: 10, value: "result"}
	stack.Push(overlay)

	// Send enter key which should trigger SelectionMsg
	cmd := stack.Update(tea.KeyMsg{Type: tea.KeyEnter, Runes: []rune{'\r'}})
	if cmd == nil {
		t.Fatal("Update should return cmd from overlay")
	}

	// Execute the command to get the message
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
}
