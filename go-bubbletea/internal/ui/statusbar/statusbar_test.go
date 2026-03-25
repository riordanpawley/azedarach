package statusbar

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/eventticker"
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

func TestStatusBar_RenderLatestEventTicker(t *testing.T) {
	style := styles.New()
	ring := eventticker.NewRing(4)
	ring.Push("daemon.event.publish")
	sb := New(types.ModeNormal, 80, style)
	sb.SetEventTicker(ring)

	result := sb.Render()

	if !strings.Contains(result, "daemon.event.publish") {
		t.Fatalf("Expected status bar to contain latest event ticker, got: %s", result)
	}
	if !strings.Contains(result, "NORMAL") {
		t.Fatalf("Expected status bar to keep mode badge, got: %s", result)
	}
}

func TestStatusBar_RenderFallsBackToSelectionSummary(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeNormal, 80, style)
	sb.SetSelectionSummary("Selected: 3")

	result := sb.Render()

	if !strings.Contains(result, "Selected: 3") {
		t.Fatalf("Expected status bar to contain selection summary, got: %s", result)
	}
	if strings.Contains(result, "daemon.event.") {
		t.Fatalf("Expected no event ticker content, got: %s", result)
	}
}

func TestStatusBar_RenderFallsBackAndTruncatesEventTicker(t *testing.T) {
	style := styles.New()
	ring := eventticker.NewRing(2)
	ring.Push("daemon.event.publish.with.an.extremely.long.event.name")
	sb := New(types.ModeNormal, 32, style)
	sb.SetEventTicker(ring)

	result := sb.Render()

	if strings.Contains(result, "\n") {
		t.Fatalf("Expected single-line status bar, got newline in: %q", result)
	}
	if !strings.Contains(result, "daemon.event.") {
		t.Fatalf("Expected truncated ticker to stay visible, got: %s", result)
	}
	if strings.Contains(result, "extremely.long.event.name") {
		t.Fatalf("Expected ticker to be truncated, got: %s", result)
	}
	if !strings.Contains(result, "NORMAL") {
		t.Fatalf("Expected status bar to keep mode badge, got: %s", result)
	}
	if got := lipgloss.Height(result); got != 1 {
		t.Fatalf("Expected single-line status bar, got height %d: %q", got, result)
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
