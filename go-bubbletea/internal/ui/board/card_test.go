package board

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// stripANSI removes ANSI escape codes from a string for testing
func stripANSI(s string) string {
	return ansi.Strip(s)
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "less than one hour",
			duration: 45 * time.Minute,
			want:     "45m",
		},
		{
			name:     "exactly one hour",
			duration: 1 * time.Hour,
			want:     "1h 0m",
		},
		{
			name:     "hours and minutes",
			duration: 2*time.Hour + 34*time.Minute,
			want:     "2h 34m",
		},
		{
			name:     "multiple hours",
			duration: 5*time.Hour + 15*time.Minute,
			want:     "5h 15m",
		},
		{
			name:     "less than one minute",
			duration: 30 * time.Second,
			want:     "0m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRenderCard_Basic(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-123",
		Title:    "Test task",
		Status:   domain.StatusOpen,
		Priority: domain.P1,
		Type:     domain.TypeTask,
	}

	result := RenderCard(task, false, false, 30, s)
	stripped := stripANSI(result)

	// Should contain title
	if !strings.Contains(stripped, "Test task") {
		t.Errorf("Card should contain task title, got: %s", stripped)
	}

	// Should contain priority badge
	if !strings.Contains(stripped, "P1") {
		t.Errorf("Card should contain priority badge, got: %s", stripped)
	}

	// Should contain type badge
	if !strings.Contains(stripped, "T") {
		t.Errorf("Card should contain type badge, got: %s", stripped)
	}
}

func TestRenderCard_WithSession(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-2*time.Hour - 30*time.Minute)

	task := domain.Task{
		ID:       "az-456",
		Title:    "Task with session",
		Status:   domain.StatusInProgress,
		Priority: domain.P0,
		Type:     domain.TypeFeature,
		Session: &domain.Session{
			BeadID:    "az-456",
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
			Worktree:  "/tmp/az-456",
		},
	}

	result := RenderCard(task, false, false, 30, s)
	stripped := stripANSI(result)

	// Should contain session icon
	if !strings.Contains(stripped, domain.SessionBusy.Icon()) {
		t.Errorf("Card should contain session icon, got: %s", stripped)
	}

	// Should contain elapsed time (approximately 2h 30m)
	// Note: exact time will vary slightly, so we just check for "h" and "m"
	if !strings.Contains(stripped, "h") || !strings.Contains(stripped, "m") {
		t.Errorf("Card should contain elapsed time for busy session, got: %s", stripped)
	}
}

func TestRenderCard_WithSessionNoElapsed(t *testing.T) {
	s := styles.New()

	tests := []struct {
		name  string
		state domain.SessionState
		icon  string
	}{
		{
			name:  "done session",
			state: domain.SessionDone,
			icon:  domain.SessionDone.Icon(),
		},
		{
			name:  "error session",
			state: domain.SessionError,
			icon:  domain.SessionError.Icon(),
		},
		{
			name:  "paused session",
			state: domain.SessionPaused,
			icon:  domain.SessionPaused.Icon(),
		},
		{
			name:  "waiting session",
			state: domain.SessionWaiting,
			icon:  domain.SessionWaiting.Icon(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := domain.Task{
				ID:       "az-789",
				Title:    "Task with " + tt.name,
				Status:   domain.StatusInProgress,
				Priority: domain.P2,
				Type:     domain.TypeBug,
				Session: &domain.Session{
					BeadID: "az-789",
					State:  tt.state,
					// No StartedAt for these states
				},
			}

			result := RenderCard(task, false, false, 30, s)
			stripped := stripANSI(result)

			// Should contain session icon
			if !strings.Contains(stripped, tt.icon) {
				t.Errorf("Card should contain session icon %s, got: %s", tt.icon, stripped)
			}

			// Should NOT contain elapsed time (no StartedAt)
			if strings.Contains(stripped, "h ") && strings.Contains(stripped, "m") {
				t.Errorf("Card should not contain elapsed time for non-busy session, got: %s", stripped)
			}
		})
	}
}

func TestRenderCard_Epic(t *testing.T) {
	s := styles.New()

	task := domain.Task{
		ID:       "az-epic-1",
		Title:    "Epic task",
		Status:   domain.StatusInProgress,
		Priority: domain.P1,
		Type:     domain.TypeEpic,
	}

	result := RenderCard(task, false, false, 30, s)
	stripped := stripANSI(result)

	// Should contain epic progress bar
	if !strings.Contains(stripped, "[") || !strings.Contains(stripped, "]") {
		t.Errorf("Epic card should contain progress brackets, got: %s", stripped)
	}

	// Should contain progress bar characters
	if !strings.Contains(stripped, "█") && !strings.Contains(stripped, "░") {
		t.Errorf("Epic card should contain progress bar, got: %s", stripped)
	}

	// Should contain ratio (from placeholder values)
	if !strings.Contains(stripped, "/") {
		t.Errorf("Epic card should contain completion ratio, got: %s", stripped)
	}
}

func TestRenderCard_EpicWithSession(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-1 * time.Hour)

	task := domain.Task{
		ID:       "az-epic-2",
		Title:    "Epic with session",
		Status:   domain.StatusInProgress,
		Priority: domain.P0,
		Type:     domain.TypeEpic,
		Session: &domain.Session{
			BeadID:    "az-epic-2",
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
		},
	}

	result := RenderCard(task, false, false, 35, s)
	stripped := stripANSI(result)

	// Should contain both session status and epic progress
	if !strings.Contains(stripped, domain.SessionBusy.Icon()) {
		t.Errorf("Epic card with session should contain session icon, got: %s", stripped)
	}

	if !strings.Contains(stripped, "[") || !strings.Contains(stripped, "]") {
		t.Errorf("Epic card with session should contain progress, got: %s", stripped)
	}
}

func TestRenderCard_Cursor(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-111",
		Title:    "Cursor task",
		Status:   domain.StatusOpen,
		Priority: domain.P3,
		Type:     domain.TypeChore,
	}

	result := RenderCard(task, true, false, 30, s)
	stripped := stripANSI(result)

	// Should contain cursor indicator
	if !strings.Contains(stripped, "▶") {
		t.Errorf("Card with cursor should contain cursor indicator, got: %s", stripped)
	}
}

func TestRenderCard_Selected(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-222",
		Title:    "Selected task",
		Status:   domain.StatusBlocked,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}

	// Card can be both cursor and selected
	resultBoth := RenderCard(task, true, true, 30, s)
	resultSelected := RenderCard(task, false, true, 30, s)
	resultNormal := RenderCard(task, false, false, 30, s)

	// All should render, but with different styles (we can't easily test
	// styling differences without parsing ANSI codes, so just ensure no crashes)
	if resultBoth == "" || resultSelected == "" || resultNormal == "" {
		t.Error("All card state combinations should render")
	}
}

func TestRenderCard_TitleTruncation(t *testing.T) {
	s := styles.New()
	longTitle := "This is a very long task title that should be truncated to fit within the card width"

	task := domain.Task{
		ID:       "az-333",
		Title:    longTitle,
		Status:   domain.StatusOpen,
		Priority: domain.P1,
		Type:     domain.TypeTask,
	}

	result := RenderCard(task, false, false, 30, s)
	stripped := stripANSI(result)

	// Should contain ellipsis for truncated title
	if !strings.Contains(stripped, "…") {
		t.Errorf("Long title should be truncated with ellipsis, got: %s", stripped)
	}

	// Should not contain the full original title
	if strings.Contains(stripped, longTitle) {
		t.Errorf("Long title should be truncated, got: %s", stripped)
	}
}

func TestRenderEpicProgress(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-epic-test",
		Title:    "Test epic",
		Status:   domain.StatusInProgress,
		Priority: domain.P1,
		Type:     domain.TypeEpic,
	}

	result := renderEpicProgress(task, 40, s)

	// Should contain progress ratio (placeholder: 3/5)
	if !strings.Contains(result, "3") || !strings.Contains(result, "5") {
		t.Error("Epic progress should contain completion counts")
	}

	// Should contain filled blocks
	if !strings.Contains(result, "█") {
		t.Error("Epic progress should contain filled blocks")
	}

	// Should contain empty blocks
	if !strings.Contains(result, "░") {
		t.Error("Epic progress should contain empty blocks")
	}
}

func TestRenderSessionStatus(t *testing.T) {
	s := styles.New()

	t.Run("busy with elapsed time", func(t *testing.T) {
		startedAt := time.Now().Add(-1*time.Hour - 30*time.Minute)
		session := &domain.Session{
			BeadID:    "test",
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
		}

		result := renderSessionStatus(session, s)
		stripped := stripANSI(result)

		if !strings.Contains(stripped, "●") {
			t.Errorf("Busy session should contain busy icon, got: %s", stripped)
		}

		if !strings.Contains(stripped, "h") || !strings.Contains(stripped, "m") {
			t.Errorf("Busy session should show elapsed time, got: %s", stripped)
		}
	})

	t.Run("done without elapsed time", func(t *testing.T) {
		session := &domain.Session{
			BeadID: "test",
			State:  domain.SessionDone,
		}

		result := renderSessionStatus(session, s)
		stripped := stripANSI(result)

		if !strings.Contains(stripped, "✓") {
			t.Errorf("Done session should contain done icon, got: %s", stripped)
		}

		// Should NOT contain time format
		if strings.Contains(stripped, "h ") || strings.Contains(stripped, "m") {
			t.Errorf("Done session should not show elapsed time, got: %s", stripped)
		}
	})

	t.Run("error session", func(t *testing.T) {
		session := &domain.Session{
			BeadID: "test",
			State:  domain.SessionError,
		}

		result := renderSessionStatus(session, s)
		stripped := stripANSI(result)

		if !strings.Contains(stripped, "✗") {
			t.Errorf("Error session should contain error icon, got: %s", stripped)
		}
	})
}
