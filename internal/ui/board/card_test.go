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
			name:     "days and hours",
			duration: 50 * time.Hour,
			want:     "2d 2h",
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

	result := RenderCard(task, false, false, 56, s)
	stripped := stripANSI(result)

	// Should contain title
	if !strings.Contains(stripped, "Test task") {
		t.Errorf("Card should contain task title, got: %s", stripped)
	}
	if !strings.Contains(stripped, "az-123") {
		t.Errorf("Card should contain issue ID, got: %s", stripped)
	}

	// Should contain priority badge
	if !strings.Contains(stripped, "P1") {
		t.Errorf("Card should contain priority badge, got: %s", stripped)
	}

	// Should contain compact type token
	if !strings.Contains(stripped, " T ") {
		t.Errorf("Card should contain compact type token, got: %s", stripped)
	}
}

func TestRenderTaskTypeBadge_UsesSingleLetter(t *testing.T) {
	s := styles.New()
	tests := []struct {
		name     string
		taskType domain.TaskType
		want     string
	}{
		{name: "task", taskType: domain.TypeTask, want: " T "},
		{name: "bug", taskType: domain.TypeBug, want: " B "},
		{name: "feature", taskType: domain.TypeFeature, want: " F "},
		{name: "epic", taskType: domain.TypeEpic, want: " E "},
		{name: "chore", taskType: domain.TypeChore, want: " C "},
		{name: "unknown", taskType: domain.TaskType("other"), want: " ? "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(renderTaskTypeBadge(tt.taskType, s))
			if got != tt.want {
				t.Fatalf("renderTaskTypeBadge(%q) = %q, want %q", tt.taskType, got, tt.want)
			}
		})
	}
}

func TestRenderCard_TitleAlwaysStartsWithIssueID(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-long-id-999",
		Title:    "This title is intentionally long so truncation happens early",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
	stripped := stripANSI(RenderCard(task, false, false, 20, s))
	if !strings.Contains(stripped, "az-long-id-999") && !strings.Contains(stripped, "az-long-id") {
		t.Fatalf("expected truncated title line to keep issue ID prefix, got: %s", stripped)
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
			IssueID:   "az-456",
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
			Worktree:  "/tmp/az-456",
		},
	}

	result := RenderCard(task, false, false, 64, s)
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

func TestRenderCard_FixedHeightAcrossTypes(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-1 * time.Hour)

	normal := domain.Task{
		ID:       "az-normal",
		Title:    "Normal",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
	epic := domain.Task{
		ID:       "az-epic",
		Title:    "Epic",
		Status:   domain.StatusInProgress,
		Priority: domain.P1,
		Type:     domain.TypeEpic,
	}
	epicWithSession := domain.Task{
		ID:       "az-epic-session",
		Title:    "Epic Session",
		Status:   domain.StatusInProgress,
		Priority: domain.P0,
		Type:     domain.TypeEpic,
		Session: &domain.Session{
			IssueID:   "az-epic-session",
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
		},
	}

	h := func(rendered string) int { return len(strings.Split(rendered, "\n")) }
	h1 := h(RenderCard(normal, false, false, 35, s))
	h2 := h(RenderCard(epic, false, false, 35, s))
	h3 := h(RenderCard(epicWithSession, false, false, 35, s))

	if h1 != h2 || h2 != h3 {
		t.Fatalf("card heights must match: normal=%d epic=%d epic+session=%d", h1, h2, h3)
	}
}

func TestCardLineFootprintMatchesRenderedCard(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-footprint",
		Title:    "Footprint",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
	rendered := RenderCard(task, false, false, 35, s)
	want := len(strings.Split(rendered, "\n"))
	got := CardLineFootprint(s, 35)
	if got != want {
		t.Fatalf("CardLineFootprint()=%d want=%d", got, want)
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
					IssueID: "az-789",
					State:   tt.state,
					// No StartedAt for these states
				},
			}

			result := RenderCard(task, false, false, 56, s)
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

func TestRenderCard_WithChildProgress(t *testing.T) {
	s := styles.New()

	task := domain.Task{
		ID:       "az-parent-1",
		Title:    "Parent task",
		Status:   domain.StatusInProgress,
		Priority: domain.P1,
		Type:     domain.TypeTask,
	}

	result := renderCard(task, nil, false, false, 56, &ChildProgress{Total: 5, Done: 3}, nil, false, s)
	stripped := stripANSI(result)

	// Should contain epic progress bar
	if !strings.Contains(stripped, "[") || !strings.Contains(stripped, "]") {
		t.Errorf("Card should contain progress brackets, got: %s", stripped)
	}

	// Should contain ratio (from placeholder values)
	if !strings.Contains(stripped, "/") {
		t.Errorf("Card should contain completion ratio, got: %s", stripped)
	}
	if strings.Contains(stripped, "█") || strings.Contains(stripped, "░") {
		t.Errorf("Card should not contain bar glyphs, got: %s", stripped)
	}
}

func TestRenderCard_WithChildProgressAndSession(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-1 * time.Hour)

	task := domain.Task{
		ID:       "az-parent-2",
		Title:    "Parent with session",
		Status:   domain.StatusInProgress,
		Priority: domain.P0,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:   "az-parent-2",
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
		},
	}

	result := renderCard(task, nil, false, false, 46, &ChildProgress{Total: 2, Done: 1}, nil, false, s)
	stripped := stripANSI(result)

	// Should contain both session status and child progress
	if !strings.Contains(stripped, domain.SessionBusy.Icon()) {
		t.Errorf("Card with session should contain session icon, got: %s", stripped)
	}

	if !strings.Contains(stripped, "[") || !strings.Contains(stripped, "]") {
		t.Errorf("Card with session should contain progress, got: %s", stripped)
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

func TestRenderTitleBodyLines_UsesRemainingRows(t *testing.T) {
	lines := renderTitleBodyLines("abcdefghijABCDEFGHIJklmnop", 14, 3)
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	if lines[0] != "abcdefghijABCD" {
		t.Fatalf("line[0] = %q", lines[0])
	}
	if lines[1] != "EFGHIJklmnop" {
		t.Fatalf("line[1] = %q", lines[1])
	}
	if lines[2] != "" {
		t.Fatalf("line[2] = %q, want empty", lines[2])
	}
}

func TestRenderChildProgress(t *testing.T) {
	s := styles.New()
	result := renderChildProgress(ChildProgress{Total: 5, Done: 3}, s)

	// Should contain progress ratio (3/5).
	if !strings.Contains(result, "3") || !strings.Contains(result, "5") {
		t.Error("Child progress should contain completion counts")
	}

	if strings.Contains(result, "█") || strings.Contains(result, "░") {
		t.Error("Child progress should not contain bar glyphs")
	}
}

func TestRenderSessionStatus(t *testing.T) {
	s := styles.New()

	t.Run("busy with elapsed time", func(t *testing.T) {
		startedAt := time.Now().Add(-1*time.Hour - 30*time.Minute)
		session := &domain.Session{
			IssueID:   "test",
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
		}

		result := renderSessionStatus(session, s)
		stripped := stripANSI(result)

		if !strings.Contains(stripped, "●") {
			t.Errorf("Busy session should contain busy icon, got: %s", stripped)
		}
		if !strings.Contains(stripped, " B ") {
			t.Errorf("Busy session should contain compact B label, got: %s", stripped)
		}

		if !strings.Contains(stripped, "h") || !strings.Contains(stripped, "m") {
			t.Errorf("Busy session should show elapsed time, got: %s", stripped)
		}
	})

	t.Run("done without elapsed time", func(t *testing.T) {
		session := &domain.Session{
			IssueID: "test",
			State:   domain.SessionDone,
		}

		result := renderSessionStatus(session, s)
		stripped := stripANSI(result)

		if !strings.Contains(stripped, "✓") {
			t.Errorf("Done session should contain done icon, got: %s", stripped)
		}
		if !strings.Contains(stripped, " D") {
			t.Errorf("Done session should contain compact D label, got: %s", stripped)
		}

		// Should NOT contain time format
		if strings.Contains(stripped, "h ") || strings.Contains(stripped, "m") {
			t.Errorf("Done session should not show elapsed time, got: %s", stripped)
		}
	})

	t.Run("error session", func(t *testing.T) {
		session := &domain.Session{
			IssueID: "test",
			State:   domain.SessionError,
		}

		result := renderSessionStatus(session, s)
		stripped := stripANSI(result)

		if !strings.Contains(stripped, "✗") {
			t.Errorf("Error session should contain error icon, got: %s", stripped)
		}
	})
}

func TestRenderCard_WithRuntimeSignals(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-runtime-1",
		Title:    "Runtime signal task",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
	signals := &RuntimeSignals{
		HasTmuxSession:        true,
		HasWorktree:           true,
		GitAheadCount:         2,
		GitBehindCount:        4,
		HasUncommittedChanges: true,
		GitAdditions:          8,
		GitDeletions:          2,
	}

	result := renderCard(task, signals, false, false, 72, nil, nil, false, s)
	stripped := stripANSI(result)
	var headerLine string
	for _, line := range strings.Split(stripped, "\n") {
		if strings.Contains(line, "az-runtime-1") {
			headerLine = line
			break
		}
	}
	if headerLine == "" {
		t.Fatalf("expected header line in card, got: %s", stripped)
	}

	for _, token := range []string{tmuxSessionToken, worktreeToken, "↓4", "✎", "+8/-2"} {
		if !strings.Contains(headerLine, token) {
			t.Fatalf("header should contain %q, got: %s", token, headerLine)
		}
	}
	if strings.Contains(headerLine, "↑2") {
		t.Fatalf("header should hide ahead token when line changes exist, got: %s", headerLine)
	}
}

func TestRenderCard_MetadataOnFirstLine(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-50 * time.Hour)
	task := domain.Task{
		ID:       "CHE-3002",
		Title:    "migrate prep lists to db",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:   "CHE-3002",
			State:     domain.SessionWaiting,
			StartedAt: &startedAt,
		},
	}
	signals := &RuntimeSignals{
		HasTmuxSession:        true,
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          12,
		GitDeletions:          3,
	}

	result := stripANSI(renderCard(task, signals, false, false, 90, &ChildProgress{Total: 7, Done: 1}, nil, false, s))
	lines := strings.Split(result, "\n")
	if len(lines) == 0 {
		t.Fatalf("expected rendered lines, got %q", result)
	}

	first := ""
	for _, line := range lines {
		if strings.Contains(line, "CHE-3002") {
			first = line
			break
		}
	}
	if first == "" {
		t.Fatalf("expected metadata line containing issue id, got: %s", result)
	}
	for _, token := range []string{"P2", "CHE-3002", " T ", " W ", "2d 2h", tmuxSessionToken, worktreeToken, "✎", "+12/-3", "[1/7]"} {
		if !strings.Contains(first, token) {
			t.Fatalf("first line should contain %q, got: %s", token, first)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "migrate prep lists to db") && strings.Contains(line, "CHE-3002") {
			t.Fatalf("title rows should not include issue id, line=%s", line)
		}
	}
}

func TestRenderCard_MetadataCompactsOnNarrowWidth(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-90 * time.Minute)
	task := domain.Task{
		ID:       "CHE-3010",
		Title:    "narrow header compaction",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:   "CHE-3010",
			State:     domain.SessionWaiting,
			StartedAt: &startedAt,
		},
	}
	signals := &RuntimeSignals{
		HasTmuxSession:        true,
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          12,
		GitDeletions:          3,
	}

	result := stripANSI(renderCard(task, signals, false, false, 40, &ChildProgress{Total: 7, Done: 1}, nil, false, s))
	lines := strings.Split(result, "\n")
	first := ""
	for _, line := range lines {
		if strings.Contains(line, "CHE-3010") {
			first = line
			break
		}
	}
	if first == "" {
		t.Fatalf("expected metadata line containing issue id, got: %s", result)
	}
	if !strings.Contains(first, "P2") || !strings.Contains(first, "CHE-3010") || !strings.Contains(first, " T ") {
		t.Fatalf("first line must preserve core tokens, got: %s", first)
	}
	if strings.Contains(first, " W ") || strings.Contains(first, "+12/-3") {
		t.Fatalf("first line should compact verbose metadata at narrow width, got: %s", first)
	}
}

func TestRenderRuntimeSignals(t *testing.T) {
	t.Run("nil runtime signals", func(t *testing.T) {
		if got := renderRuntimeSignals(nil, styles.New()); got != "" {
			t.Fatalf("renderRuntimeSignals(nil) = %q, want empty", got)
		}
	})

	t.Run("session and worktree signals", func(t *testing.T) {
		signals := &RuntimeSignals{
			HasTmuxSession:           true,
			HasDescendantTmuxSession: true,
			HasWorktree:              true,
			GitAheadCount:            1,
			GitBehindCount:           2,
			HasUncommittedChanges:    true,
			GitAdditions:             10,
			GitDeletions:             3,
			PendingOperationState:    "queued",
			PendingOperationPercent:  25,
		}
		got := stripANSI(renderRuntimeSignals(signals, styles.New()))
		if !strings.Contains(got, tmuxSessionToken) ||
			!strings.Contains(got, descendantTmuxSessionToken) ||
			!strings.Contains(got, worktreeToken) ||
			!strings.Contains(got, "M:queued(25%)") ||
			!strings.Contains(got, "↓2") ||
			!strings.Contains(got, "✎") ||
			!strings.Contains(got, "+10/-3") {
			t.Fatalf("renderRuntimeSignals(...) = %q, missing expected token(s)", got)
		}
		if strings.Contains(got, "↑1") {
			t.Fatalf("renderRuntimeSignals(...) = %q, should hide ahead token when line changes exist", got)
		}
	})

	t.Run("ahead shown when no line changes", func(t *testing.T) {
		signals := &RuntimeSignals{
			HasTmuxSession: true,
			HasWorktree:    true,
			GitAheadCount:  3,
		}
		got := stripANSI(renderRuntimeSignals(signals, styles.New()))
		if !strings.Contains(got, "↑3") {
			t.Fatalf("renderRuntimeSignals(...) = %q, missing ahead token", got)
		}
	})
}

func TestRenderRuntimeSignalsCompact(t *testing.T) {
	t.Run("nil runtime signals", func(t *testing.T) {
		if got := renderRuntimeSignalsCompact(nil, styles.New()); got != "" {
			t.Fatalf("renderRuntimeSignalsCompact(nil) = %q, want empty", got)
		}
	})

	t.Run("compacts to narrow token set", func(t *testing.T) {
		signals := &RuntimeSignals{
			HasTmuxSession:           true,
			HasDescendantTmuxSession: true,
			HasWorktree:              true,
			HasUncommittedChanges:    true,
			GitAdditions:             10,
			GitDeletions:             3,
			GitBehindCount:           4,
			PendingOperationState:    "running",
			PendingOperationPercent:  50,
		}
		got := stripANSI(renderRuntimeSignalsCompact(signals, styles.New()))
		if !strings.Contains(got, "T") || !strings.Contains(got, "Td") || !strings.Contains(got, worktreeToken) || !strings.Contains(got, "M:R50") || !strings.Contains(got, "G*↓4") {
			t.Fatalf("renderRuntimeSignalsCompact(...) = %q, missing expected compact token(s)", got)
		}
		if strings.Contains(got, "+10/-3") {
			t.Fatalf("renderRuntimeSignalsCompact(...) = %q, should omit verbose line-change token", got)
		}
	})

	t.Run("shows directional pairing without line changes", func(t *testing.T) {
		signals := &RuntimeSignals{
			HasWorktree:    true,
			GitAheadCount:  2,
			GitBehindCount: 3,
		}
		got := stripANSI(renderRuntimeSignalsCompact(signals, styles.New()))
		if !strings.Contains(got, "G↑2/↓3") {
			t.Fatalf("renderRuntimeSignalsCompact(...) = %q, missing directional ahead/behind pairing", got)
		}
	})
}

func TestBuildChildProgress(t *testing.T) {
	parentID := "az-parent"
	tasks := []domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusOpen},
		{ID: "az-c1", Title: "Child 1", Status: domain.StatusDone, ParentID: &parentID},
		{ID: "az-c2", Title: "Child 2", Status: domain.StatusInProgress, ParentID: &parentID},
	}

	progress := BuildChildProgress(tasks)
	got, ok := progress[parentID]
	if !ok {
		t.Fatalf("expected child progress for %s", parentID)
	}
	if got.Total != 2 || got.Done != 1 {
		t.Fatalf("progress = %+v, want total=2 done=1", got)
	}
}
