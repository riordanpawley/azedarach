package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGenerateLabels(t *testing.T) {
	tests := []struct {
		name          string
		count         int
		expectedFirst string
		expectedLast  string
		expectedLen   int
	}{
		{
			name:          "zero tasks",
			count:         0,
			expectedFirst: "",
			expectedLast:  "",
			expectedLen:   0,
		},
		{
			name:          "one task",
			count:         1,
			expectedFirst: "a",
			expectedLast:  "a",
			expectedLen:   1,
		},
		{
			name:          "five tasks",
			count:         5,
			expectedFirst: "a",
			expectedLast:  "g",
			expectedLen:   5,
		},
		{
			name:          "ten tasks (exactly home row)",
			count:         10,
			expectedFirst: "a",
			expectedLast:  ";",
			expectedLen:   10,
		},
		{
			name:          "fifteen tasks (need double chars)",
			count:         15,
			expectedFirst: "a",
			expectedLast:  "ag",
			expectedLen:   15,
		},
		{
			name:          "fifty tasks",
			count:         50,
			expectedFirst: "a",
			expectedLast:  "f;",
			expectedLen:   50,
		},
		{
			name:          "hundred tasks",
			count:         100,
			expectedFirst: "a",
			expectedLast:  "l;",
			expectedLen:   100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := GenerateLabels(tt.count)

			if len(labels) != tt.expectedLen {
				t.Errorf("GenerateLabels(%d) length = %d, want %d",
					tt.count, len(labels), tt.expectedLen)
			}

			if tt.expectedLen > 0 {
				if labels[0] != tt.expectedFirst {
					t.Errorf("First label = %s, want %s", labels[0], tt.expectedFirst)
				}

				if labels[len(labels)-1] != tt.expectedLast {
					t.Errorf("Last label = %s, want %s", labels[len(labels)-1], tt.expectedLast)
				}
			}
		})
	}
}

func TestGenerateLabels_SingleChar(t *testing.T) {
	// First 10 should be single character
	labels := GenerateLabels(10)

	for i, label := range labels {
		if len(label) != 1 {
			t.Errorf("Label at index %d = %s (len %d), want single character",
				i, label, len(label))
		}
	}

	expectedLabels := []string{"a", "s", "d", "f", "g", "h", "j", "k", "l", ";"}
	for i, expected := range expectedLabels {
		if labels[i] != expected {
			t.Errorf("Label at index %d = %s, want %s", i, labels[i], expected)
		}
	}
}

func TestGenerateLabels_DoubleChar(t *testing.T) {
	// After 10, should be double character
	labels := GenerateLabels(20)

	// First 10 should be single char
	for i := 0; i < 10; i++ {
		if len(labels[i]) != 1 {
			t.Errorf("Label at index %d should be single char, got %s", i, labels[i])
		}
	}

	// Next 10 should be double char
	for i := 10; i < 20; i++ {
		if len(labels[i]) != 2 {
			t.Errorf("Label at index %d should be double char, got %s", i, labels[i])
		}
	}

	// Check specific double char labels
	expectedDouble := []string{"aa", "as", "ad", "af", "ag", "ah", "aj", "ak", "al", "a;"}
	for i, expected := range expectedDouble {
		actual := labels[10+i]
		if actual != expected {
			t.Errorf("Double char label at index %d = %s, want %s", 10+i, actual, expected)
		}
	}
}

func TestGenerateLabels_Uniqueness(t *testing.T) {
	// Test that all labels are unique
	counts := []int{10, 50, 100}

	for _, count := range counts {
		t.Run("unique_for_"+string(rune('0'+count/10)), func(t *testing.T) {
			labels := GenerateLabels(count)
			seen := make(map[string]bool)

			for _, label := range labels {
				if seen[label] {
					t.Errorf("Duplicate label found: %s", label)
				}
				seen[label] = true
			}
		})
	}
}

func TestJumpMode_Init(t *testing.T) {
	jump := NewJumpMode(5)

	if cmd := jump.Init(); cmd != nil {
		t.Errorf("Init() should return nil, got %v", cmd)
	}
}

func TestJumpMode_InputAccumulation(t *testing.T) {
	jump := NewJumpMode(20)

	tests := []struct {
		name          string
		key           string
		expectedInput string
	}{
		{"first key", "a", "a"},
		{"second key", "s", "as"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			model, _ := jump.Update(msg)
			jump = model.(*JumpMode)

			if jump.input != tt.expectedInput {
				t.Errorf("input = %s, want %s", jump.input, tt.expectedInput)
			}
		})
	}
}

func TestJumpMode_Backspace(t *testing.T) {
	jump := NewJumpMode(20)

	// Add some input
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}
	model, _ := jump.Update(msg)
	jump = model.(*JumpMode)
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}
	model, _ = jump.Update(msg)
	jump = model.(*JumpMode)

	if jump.input != "as" {
		t.Fatalf("Setup failed: input = %s, want 'as'", jump.input)
	}

	// Backspace
	msg = tea.KeyMsg{Type: tea.KeyBackspace}
	model, _ = jump.Update(msg)
	jump = model.(*JumpMode)

	if jump.input != "a" {
		t.Errorf("After backspace, input = %s, want 'a'", jump.input)
	}

	// Backspace again
	model, _ = jump.Update(msg)
	jump = model.(*JumpMode)

	if jump.input != "" {
		t.Errorf("After second backspace, input = %s, want ''", jump.input)
	}

	// Backspace on empty (should not error)
	model, _ = jump.Update(msg)
	jump = model.(*JumpMode)

	if jump.input != "" {
		t.Errorf("After backspace on empty, input = %s, want ''", jump.input)
	}
}

func TestJumpMode_Selection(t *testing.T) {
	jump := NewJumpMode(5)

	// Type 's' which should match the second label
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}
	_, cmd := jump.Update(msg)

	if cmd == nil {
		t.Fatal("Expected selection command, got nil")
	}

	result := cmd()
	jumpMsg, ok := result.(JumpSelectedMsg)
	if !ok {
		t.Fatalf("Expected JumpSelectedMsg, got %T", result)
	}

	// 's' is the second label, should map to index 1
	if jumpMsg.TaskIndex != 1 {
		t.Errorf("TaskIndex = %d, want 1", jumpMsg.TaskIndex)
	}
}

func TestJumpMode_DoubleCharSelection(t *testing.T) {
	jump := NewJumpMode(15)

	// Type 'a' then 'a' which should match the 11th label (index 10)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}
	model, cmd := jump.Update(msg)
	jump = model.(*JumpMode)
	if cmd != nil {
		t.Error("Should not select after first character of double-char label")
	}

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}
	_, cmd = jump.Update(msg)

	if cmd == nil {
		t.Fatal("Expected selection command after second character, got nil")
	}

	result := cmd()
	jumpMsg, ok := result.(JumpSelectedMsg)
	if !ok {
		t.Fatalf("Expected JumpSelectedMsg, got %T", result)
	}

	// 'aa' is the 11th label, should map to index 10
	if jumpMsg.TaskIndex != 10 {
		t.Errorf("TaskIndex = %d, want 10", jumpMsg.TaskIndex)
	}
}

func TestJumpMode_InvalidKey(t *testing.T) {
	jump := NewJumpMode(10)

	// Type a non-home-row key
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")}
	_, cmd := jump.Update(msg)

	if cmd != nil {
		t.Error("Invalid key should not trigger any command")
	}

	if jump.input != "" {
		t.Errorf("Invalid key should not update input, got %s", jump.input)
	}
}

func TestJumpMode_NoMatch(t *testing.T) {
	jump := NewJumpMode(5) // Only has labels a, s, d, f, g

	// Try to type 'h' which doesn't exist
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")}
	_, cmd := jump.Update(msg)

	if cmd != nil {
		t.Error("Non-existent label should not trigger selection")
	}

	// Input should be cleared because 'h' alone doesn't match anything
	// and maxLen is 1 for 5 tasks
	if jump.input != "" {
		t.Errorf("Input should be cleared after no match, got %s", jump.input)
	}
}

func TestJumpMode_Close(t *testing.T) {
	jump := NewJumpMode(10)

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	_, cmd := jump.Update(msg)

	if cmd == nil {
		t.Fatal("Expected close command, got nil")
	}

	result := cmd()
	if _, ok := result.(CloseOverlayMsg); !ok {
		t.Errorf("Expected CloseOverlayMsg, got %T", result)
	}
}

func TestJumpMode_GetLabel(t *testing.T) {
	jump := NewJumpMode(15)

	tests := []struct {
		index int
		want  string
	}{
		{0, "a"},
		{1, "s"},
		{9, ";"},
		{10, "aa"},
		{11, "as"},
		{14, "ag"},
		{999, ""}, // Out of range
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := jump.GetLabel(tt.index)
			if got != tt.want {
				t.Errorf("GetLabel(%d) = %s, want %s", tt.index, got, tt.want)
			}
		})
	}
}

func TestJumpMode_View(t *testing.T) {
	jump := NewJumpMode(10)
	view := jump.View()

	// Check title
	if !strings.Contains(view, "Jump Mode") {
		t.Error("View should contain title 'Jump Mode'")
	}

	// Check hint for empty input
	if !strings.Contains(view, "Type a label") {
		t.Error("View should contain input hint")
	}

	// Check footer
	if !strings.Contains(view, "Esc: cancel") {
		t.Error("View should contain footer with keybindings")
	}

	// Add some input and check view updates
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}
	model, _ := jump.Update(msg)
	jump = model.(*JumpMode)
	view = jump.View()

	if !strings.Contains(view, "Input:") {
		t.Error("View should show input label when there's input")
	}
}

func TestJumpMode_Title(t *testing.T) {
	jump := NewJumpMode(10)
	title := jump.Title()

	if title != "Jump" {
		t.Errorf("Title() = %s, want 'Jump'", title)
	}
}

func TestJumpMode_Size(t *testing.T) {
	jump := NewJumpMode(10)
	width, height := jump.Size()

	if width != 50 {
		t.Errorf("Width = %d, want 50", width)
	}

	if height != 10 {
		t.Errorf("Height = %d, want 10", height)
	}
}

func TestIsHomeRowKey(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'a', true},
		{'s', true},
		{'d', true},
		{'f', true},
		{'g', true},
		{'h', true},
		{'j', true},
		{'k', true},
		{'l', true},
		{';', true},
		{'z', false},
		{'q', false},
		{'1', false},
		{' ', false},
	}

	for _, tt := range tests {
		t.Run(string(tt.r), func(t *testing.T) {
			got := isHomeRowKey(tt.r)
			if got != tt.want {
				t.Errorf("isHomeRowKey(%c) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

func TestRenderLabel(t *testing.T) {
	// Just test that it doesn't panic
	label := RenderLabel("a")
	if label == "" {
		t.Error("RenderLabel should return non-empty string")
	}

	label = RenderLabel("as")
	if label == "" {
		t.Error("RenderLabel should handle double char labels")
	}
}
