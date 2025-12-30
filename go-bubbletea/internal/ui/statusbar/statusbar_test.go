package statusbar

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

func TestStatusBar_RenderNormalMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeNormal, 80, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "NORMAL") {
		t.Errorf("Expected status bar to contain 'NORMAL', got: %s", result)
	}

	// Should contain normal mode hints
	if !strings.Contains(result, "h/l: columns") {
		t.Errorf("Expected status bar to contain navigation hints, got: %s", result)
	}
	if !strings.Contains(result, "j/k: tasks") {
		t.Errorf("Expected status bar to contain task navigation hints, got: %s", result)
	}
	if !strings.Contains(result, "Space: action") {
		t.Errorf("Expected status bar to contain action hint, got: %s", result)
	}
}

func TestStatusBar_RenderSelectMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeSelect, 80, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "SELECT") {
		t.Errorf("Expected status bar to contain 'SELECT', got: %s", result)
	}

	// Should contain select mode hints
	if !strings.Contains(result, "Space: toggle") {
		t.Errorf("Expected status bar to contain toggle hint, got: %s", result)
	}
	if !strings.Contains(result, "a: all") {
		t.Errorf("Expected status bar to contain select all hint, got: %s", result)
	}
}

func TestStatusBar_RenderSearchMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeSearch, 80, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "SEARCH") {
		t.Errorf("Expected status bar to contain 'SEARCH', got: %s", result)
	}

	// Should contain search mode hints
	if !strings.Contains(result, "Type to search") {
		t.Errorf("Expected status bar to contain search hint, got: %s", result)
	}
}

func TestStatusBar_RenderGotoMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeGoto, 80, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "GOTO") {
		t.Errorf("Expected status bar to contain 'GOTO', got: %s", result)
	}

	// Should contain goto mode hints
	if !strings.Contains(result, "g: top") {
		t.Errorf("Expected status bar to contain goto top hint, got: %s", result)
	}
	if !strings.Contains(result, "e: end") {
		t.Errorf("Expected status bar to contain goto end hint, got: %s", result)
	}
}

func TestStatusBar_RenderActionMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeAction, 80, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "ACTION") {
		t.Errorf("Expected status bar to contain 'ACTION', got: %s", result)
	}
}

func TestStatusBar_FillsWidth(t *testing.T) {
	style := styles.New()
	width := 100
	sb := New(types.ModeNormal, width, style)

	result := sb.Render()

	// The rendered output should fill the terminal width
	// Note: This is a basic check - lipgloss rendering may add ANSI codes
	if result == "" {
		t.Error("Expected non-empty status bar")
	}
}

func TestGetHints_AllModes(t *testing.T) {
	tests := []struct {
		mode     types.Mode
		expected string
	}{
		{types.ModeNormal, "h/l: columns  j/k: tasks  Space: action  ?: help  q: quit"},
		{types.ModeSelect, "Space: toggle  a: all  n: none  Esc: cancel"},
		{types.ModeSearch, "Type to search  Enter: confirm  Esc: cancel"},
		{types.ModeGoto, "g: top  e: end  h: first col  l: last col  Esc: cancel"},
		{types.ModeAction, ""},
	}

	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			result := GetHints(tt.mode)
			if result != tt.expected {
				t.Errorf("GetHints(%v) = %q, want %q", tt.mode, result, tt.expected)
			}
		})
	}
}
