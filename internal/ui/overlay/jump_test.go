package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGenerateLabelsWithChars(t *testing.T) {
	got := GenerateLabelsWithChars(7, "abc")
	want := []string{"a", "b", "c", "aa", "ab", "ac", "ba"}
	if len(got) != len(want) {
		t.Fatalf("label count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("label[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestJumpModeAcceptsConfiguredSecondCharacter(t *testing.T) {
	mode := NewJumpModeWithChars(6, "abc")

	model, cmd := mode.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	mode = model.(*JumpMode)
	if cmd != nil {
		t.Fatal("expected first jump key to wait for second key")
	}

	_, cmd = mode.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if cmd == nil {
		t.Fatal("expected second jump key to emit JumpSelectedMsg")
	}

	msg := cmd()
	selected, ok := msg.(JumpSelectedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want JumpSelectedMsg", msg)
	}
	if got, want := selected.TaskIndex, 4; got != want {
		t.Fatalf("selected TaskIndex = %d, want %d", got, want)
	}
}

func TestJumpModeIgnoresUnknownKeys(t *testing.T) {
	mode := NewJumpModeWithChars(5, "abc")

	model, cmd := mode.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	mode = model.(*JumpMode)
	if cmd != nil {
		t.Fatal("unexpected command for unknown key")
	}
	if mode.input != "" {
		t.Fatalf("input = %q, want empty", mode.input)
	}
}
