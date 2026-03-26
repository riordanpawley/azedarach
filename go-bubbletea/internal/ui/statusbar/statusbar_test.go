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
	sb := New(types.ModeNormal, 160, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "NORMAL") {
		t.Errorf("Expected status bar to contain 'NORMAL', got: %s", result)
	}

	// Should contain normal mode hints
	if !strings.Contains(result, "Space: task workspace") {
		t.Errorf("Expected status bar to contain workspace hint, got: %s", result)
	}
	if !strings.Contains(result, "g: goto") {
		t.Errorf("Expected status bar to contain goto hint, got: %s", result)
	}
	if !strings.Contains(result, "r: refresh") {
		t.Errorf("Expected status bar to contain refresh hint, got: %s", result)
	}
	if !strings.Contains(result, "Enter: drill") {
		t.Errorf("Expected status bar to contain drill hint, got: %s", result)
	}
	if !strings.Contains(result, "Tab: view") {
		t.Errorf("Expected status bar to contain view hint, got: %s", result)
	}
}

func TestStatusBar_RenderSelectMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeSelect, 160, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "SELECT") {
		t.Errorf("Expected status bar to contain 'SELECT', got: %s", result)
	}

	// Should contain select mode hints
	if !strings.Contains(result, "a/5: toggle") {
		t.Errorf("Expected status bar to contain toggle hint, got: %s", result)
	}
	if !strings.Contains(result, "A: column") {
		t.Errorf("Expected status bar to contain column select hint, got: %s", result)
	}
}

func TestStatusBar_RenderSearchMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeSearch, 120, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "SEARCH") {
		t.Errorf("Expected status bar to contain 'SEARCH', got: %s", result)
	}

	// Should contain search mode hints
	if !strings.Contains(result, "Type: search") {
		t.Errorf("Expected status bar to contain search hint, got: %s", result)
	}
}

func TestStatusBar_RenderGotoMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeGoto, 160, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "GOTO") {
		t.Errorf("Expected status bar to contain 'GOTO', got: %s", result)
	}

	// Should contain goto mode hints
	if !strings.Contains(result, "g g: top") {
		t.Errorf("Expected status bar to contain goto top hint, got: %s", result)
	}
	if !strings.Contains(result, "g e: bottom") {
		t.Errorf("Expected status bar to contain goto bottom hint, got: %s", result)
	}
}

func TestStatusBar_RenderActionMode(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeAction, 160, style)

	result := sb.Render()

	// Should contain mode badge
	if !strings.Contains(result, "ACTION") {
		t.Errorf("Expected status bar to contain 'ACTION', got: %s", result)
	}
	if !strings.Contains(result, "h/l: move") {
		t.Errorf("Expected status bar to contain move hint, got: %s", result)
	}
	if !strings.Contains(result, "m/b: merge") {
		t.Errorf("Expected status bar to contain merge hint, got: %s", result)
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

func TestStatusBar_RenderShowsCurrentProjectOnLeft(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeNormal, 120, style)
	sb.SetCurrentProject("azedarach")

	result := sb.Render()
	projectIdx := strings.Index(result, "azedarach")
	modeIdx := strings.Index(result, "NORMAL")
	if projectIdx < 0 {
		t.Fatalf("expected status bar to contain current project, got: %s", result)
	}
	if modeIdx < 0 {
		t.Fatalf("expected status bar to contain mode badge, got: %s", result)
	}
	if projectIdx > modeIdx {
		t.Fatalf("expected current project to appear before mode badge, got: %s", result)
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

func TestStatusBar_RenderShowsLoadingIndicator(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeNormal, 80, style)
	sb.SetLoadingIndicator("Loading runtime status...")

	result := sb.Render()

	if !strings.Contains(result, "Loading runtime status...") {
		t.Fatalf("Expected status bar to contain loading indicator, got: %s", result)
	}
	if !strings.Contains(result, "NORMAL") {
		t.Fatalf("Expected status bar to keep mode badge, got: %s", result)
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

func TestStatusBar_RenderTruncatesRichHints(t *testing.T) {
	style := styles.New()
	sb := New(types.ModeAction, 54, style)

	result := sb.Render()

	if strings.Contains(result, "\n") {
		t.Fatalf("Expected single-line status bar, got newline in: %q", result)
	}
	if !strings.Contains(result, "ACTION") {
		t.Fatalf("Expected action mode badge, got: %s", result)
	}
	if !strings.Contains(result, "h/l: move") {
		t.Fatalf("Expected rich action hints to start with move shortcut, got: %s", result)
	}
	if !strings.Contains(result, "…") {
		t.Fatalf("Expected narrow status bar to truncate rich hints, got: %q", result)
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
		{types.ModeNormal, "Space: task workspace  g: goto  /: search  f: filter  ,: sort  v: select  Enter: drill  c: create  s: settings  r: refresh  Tab: view  ?: help  q: quit"},
		{types.ModeSelect, "a/5: toggle  A: column  %: all  *: invert  x: clear  Space/Enter: bulk  v/Esc: exit"},
		{types.ModeSearch, "Type: search  Enter: confirm  Esc: cancel"},
		{types.ModeGoto, "g g: top  g e: bottom  g h: first col  g l: last col  g w: labels  g p: projects  g s: spec  Esc: cancel"},
		{types.ModeAction, "h/l: move  s/S/!: start  a: attach  p: pause  R: resume  r: dev  x: stop  u: update  m/b: merge  P/O: PR  M: abort  H: helix  i: attachments  f: diff  w/W: cleanup  e: edit  c: child  T/d: tombstone/delete  Esc/q: cancel"},
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
