package board

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
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
		{name: "investigation", taskType: domain.TypeInvestigation, want: " I "},
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

func TestRenderCard_WithPullRequestSummary(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-pr",
		Title:    "Task with PR",
		Status:   domain.StatusInReview,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		PullRequest: &domain.PullRequest{
			Number:       42,
			DisplayKey:   "#42",
			State:        "open",
			ChecksStatus: "pending",
		},
	}

	stripped := stripANSI(RenderCard(task, false, false, 72, s))
	if !strings.Contains(stripped, "PR#42/pend") || strings.Contains(stripped, "open/pending") {
		t.Fatalf("card should contain PR summary, got: %s", stripped)
	}
}

func TestRenderCard_CompactPullRequestSummary(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "az-pr",
		Title:    "Task with PR",
		Status:   domain.StatusInReview,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		PullRequest: &domain.PullRequest{
			Number:       42,
			DisplayKey:   "#42",
			State:        "open",
			ChecksStatus: "pending",
		},
	}

	stripped := stripANSI(RenderCard(task, false, false, 20, s))
	if !strings.Contains(stripped, "PR…") {
		t.Fatalf("compact card should contain PR status icon, got: %s", stripped)
	}
	if strings.Contains(stripped, "PR#42") || strings.Contains(stripped, "pend") {
		t.Fatalf("compact card should omit PR number and text status, got: %s", stripped)
	}
}

func TestRenderPullRequestBadgesAreSingleLineHeaderTokens(t *testing.T) {
	s := styles.New()
	tests := []struct {
		name   string
		render func(*domain.PullRequest, *styles.Styles) string
		pr     domain.PullRequest
	}{
		{
			name:   "full badge with unicode display key",
			render: renderPullRequestBadge,
			pr: domain.PullRequest{
				DisplayKey:   "#12345✓",
				State:        "open",
				ChecksStatus: "pending",
			},
		},
		{
			name:   "compact draft badge",
			render: renderPullRequestBadgeCompact,
			pr: domain.PullRequest{
				Number:       12345,
				Draft:        true,
				ChecksStatus: "passing",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			badge := tt.render(&tt.pr, s)
			if got := lipgloss.Height(badge); got != 1 {
				t.Fatalf("badge height = %d, want 1; rendered badge:\n%s", got, stripANSI(badge))
			}
			if strings.ContainsAny(stripANSI(badge), "\r\n") {
				t.Fatalf("badge must be an atomic header token, got %q", stripANSI(badge))
			}
		})
	}
}

func TestRenderCard_PullRequestDoesNotMoveTitleBaseline(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "gdd",
		Title:    "Make projections complete reliably",
		Status:   domain.StatusInReview,
		Priority: domain.P2,
		Type:     domain.TypeBug,
		Origin:   "github",
	}
	withPullRequest := task
	withPullRequest.PullRequest = &domain.PullRequest{
		Number:       523,
		DisplayKey:   "#523",
		State:        "open",
		ChecksStatus: "pending",
	}

	titleLine := func(rendered string) int {
		t.Helper()
		for i, line := range strings.Split(stripANSI(rendered), "\n") {
			if strings.Contains(line, "Make") {
				return i
			}
		}
		return -1
	}

	for _, width := range []int{72, 45, 38, 30, 20, 19} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			withoutPR := RenderCard(task, false, false, width, s)
			withPR := RenderCard(withPullRequest, false, false, width, s)
			withoutLine := titleLine(withoutPR)
			withLine := titleLine(withPR)
			if withoutLine < 0 || withLine < 0 {
				t.Fatalf("title missing at width %d: without PR=%d with PR=%d", width, withoutLine, withLine)
			}
			if withLine != withoutLine {
				t.Fatalf("title baseline at width %d moved from line %d to %d when PR metadata was added\nwithout PR:\n%s\nwith PR:\n%s", width, withoutLine, withLine, stripANSI(withoutPR), stripANSI(withPR))
			}
		})
	}
}

func TestRenderCard_EssentialRuntimeStatePrecedesPullRequestAtMinimumWidth(t *testing.T) {
	s := styles.New()
	task := domain.Task{
		ID:       "gdd",
		Title:    "Runtime priority",
		Status:   domain.StatusInReview,
		Priority: domain.P2,
		Type:     domain.TypeBug,
		PullRequest: &domain.PullRequest{
			Number:       523,
			ChecksStatus: "pending",
		},
	}
	rendered := stripANSI(RenderCardWithRuntimeSignals(task, &RuntimeSignals{HasTmuxSession: true}, false, false, 17, s))
	header := ""
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "gdd") {
			header = line
			break
		}
	}
	if !strings.Contains(header, " T") {
		t.Fatalf("minimum-width header must preserve essential runtime state, got %q\n%s", header, rendered)
	}
	if strings.Contains(header, "PR") {
		t.Fatalf("minimum-width header must omit PR before essential runtime state, got %q\n%s", header, rendered)
	}
}

func TestRenderPullRequestBadgeCompactStatusIcons(t *testing.T) {
	s := styles.New()
	tests := []struct {
		name string
		pr   domain.PullRequest
		want string
	}{
		{name: "passing checks", pr: domain.PullRequest{ChecksStatus: "passing"}, want: "PR✓"},
		{name: "failing checks", pr: domain.PullRequest{ChecksStatus: "failing"}, want: "PR✗"},
		{name: "pending checks", pr: domain.PullRequest{ChecksStatus: "pending"}, want: "PR…"},
		{name: "draft", pr: domain.PullRequest{Draft: true, ChecksStatus: "passing"}, want: "PRD"},
		{name: "merged without checks", pr: domain.PullRequest{State: "merged"}, want: "PRM"},
		{name: "merged with passing checks", pr: domain.PullRequest{State: "merged", ChecksStatus: "passing"}, want: "PRM"},
		{name: "open without checks", pr: domain.PullRequest{State: "open"}, want: "PR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripANSI(renderPullRequestBadgeCompact(&tt.pr, s)); !strings.Contains(got, tt.want) {
				t.Fatalf("compact badge = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderPullRequestBadgeFullPrefersMergedOverPassingChecks(t *testing.T) {
	s := styles.New()
	pr := domain.PullRequest{
		Number:       42,
		DisplayKey:   "#42",
		State:        "merged",
		ChecksStatus: "passing",
	}
	got := stripANSI(renderPullRequestBadge(&pr, s))
	if !strings.Contains(got, "PR#42/mrg") {
		t.Fatalf("full badge = %q, want merged state", got)
	}
	if strings.Contains(got, "PR#42/ok") {
		t.Fatalf("full badge should not collapse merged into passing checks: %q", got)
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

	// Should contain compact elapsed time without minute detail once the
	// session has crossed an hour.
	if !strings.Contains(stripped, "2h") || strings.Contains(stripped, "30m") {
		t.Errorf("Card should contain compact elapsed time for busy session, got: %s", stripped)
	}

}

func TestRenderCard_WithPartialAggregateSession(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-15 * time.Minute)
	task := domain.Task{
		ID:       "az-partial",
		Title:    "Mixed session",
		Status:   domain.StatusInProgress,
		Priority: domain.P1,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:     "az-partial",
			State:       domain.SessionBusy,
			TotalCount:  2,
			ActiveCount: 1,
			PausedCount: 1,
			StartedAt:   &startedAt,
		},
	}

	result := RenderCard(task, false, false, 64, s)
	stripped := stripANSI(result)
	if !strings.Contains(stripped, "◒") || strings.Contains(stripped, "mix") {
		t.Fatalf("partial aggregate session should render mixed signage, got: %s", stripped)
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

	result := renderCard(task, CardState{}, nil, &ChildProgress{Total: 5, Done: 3}, nil, 56, s)
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

	result := renderCard(task, CardState{}, nil, &ChildProgress{Total: 2, Done: 1}, nil, 46, s)
	stripped := stripANSI(result)

	// Should contain both session status and child progress
	if !strings.Contains(stripped, domain.SessionBusy.Icon()) {
		t.Errorf("Card with session should contain session icon, got: %s", stripped)
	}

	if !strings.Contains(stripped, "[") || !strings.Contains(stripped, "]") {
		t.Errorf("Card with session should contain progress, got: %s", stripped)
	}
}

func TestRenderCard_DenseHeaderPrefersCompactSingleLine(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-2 * time.Hour)
	task := domain.Task{
		ID:       "cyk",
		Title:    "Redesign issue lifecycle and derived review state",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeEpic,
		Session: &domain.Session{
			IssueID:   naming.IssueID("cyk"),
			State:     domain.SessionBusy,
			StartedAt: &startedAt,
		},
	}
	runtimeSignals := &RuntimeSignals{
		HasWorktree:    true,
		GitAheadCount:  12,
		GitBehindCount: 14,
		GitAdditions:   4000,
		GitDeletions:   285,
	}

	rendered := renderCard(task, CardState{}, runtimeSignals, &ChildProgress{Total: 10, Done: 8}, nil, 45, s)
	stripped := stripANSI(rendered)
	lines := strings.Split(stripped, "\n")
	if len(lines) < 4 {
		t.Fatalf("rendered lines = %d, want card body:\n%s", len(lines), stripped)
	}

	header := strings.TrimSpace(strings.Trim(lines[1], "│"))
	secondBodyLine := strings.TrimSpace(strings.Trim(lines[2], "│"))
	for _, want := range []string{"P2", "cyk", "●", "2h", "[8/10]", "E", "G*↑12/↓14"} {
		if !strings.Contains(header, want) {
			t.Fatalf("dense compact header missing %q in %q\ncard:\n%s", want, header, stripped)
		}
	}
	if secondBodyLine == "E" {
		t.Fatalf("dense header stranded type badge on its own line:\n%s", stripped)
	}
	if !strings.Contains(secondBodyLine, "Redesign") {
		t.Fatalf("dense card should start title on second content line, got %q\ncard:\n%s", secondBodyLine, stripped)
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
		Status:   domain.StatusInReview,
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
	longTitle := strings.Repeat("This is a very long task title that should be truncated to fit within the card width. ", 3)

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
		if !strings.Contains(stripped, "busy") {
			t.Errorf("Busy session should contain readable busy label, got: %s", stripped)
		}

		if !strings.Contains(stripped, "1h") || strings.Contains(stripped, "30m") {
			t.Errorf("Busy session should show compact elapsed time, got: %s", stripped)
		}
	})

	t.Run("hook idle live session renders idle label and session age", func(t *testing.T) {
		startedAt := time.Now().Add(-1*time.Hour - 30*time.Minute)
		session := &domain.Session{
			IssueID:        "test",
			State:          domain.SessionBusy,
			Activity:       "idle",
			ActivitySource: "hooks",
			StartedAt:      &startedAt,
		}

		result := renderSessionStatus(session, s)
		stripped := stripANSI(result)

		if !strings.Contains(stripped, "○") {
			t.Errorf("Hook-idle session should contain idle icon, got: %s", stripped)
		}
		if !strings.Contains(stripped, "idle") {
			t.Errorf("Hook-idle session should contain readable idle label, got: %s", stripped)
		}
		if strings.Contains(stripped, "busy") {
			t.Errorf("Hook-idle session should not render busy label, got: %s", stripped)
		}
		if !strings.Contains(stripped, "1h") || strings.Contains(stripped, "30m") {
			t.Errorf("Hook-idle session should show compact session age, got: %s", stripped)
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
		if !strings.Contains(stripped, "done") {
			t.Errorf("Done session should contain readable done label, got: %s", stripped)
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

	t.Run("icon-only active sessions render age", func(t *testing.T) {
		startedAt := time.Now().Add(-2*time.Hour - 15*time.Minute)
		tests := []struct {
			name     string
			session  *domain.Session
			wantIcon string
		}{
			{
				name: "paused",
				session: &domain.Session{
					IssueID:   "test",
					State:     domain.SessionPaused,
					StartedAt: &startedAt,
				},
				wantIcon: domain.SessionPaused.Icon(),
			},
			{
				name: "no-agent",
				session: &domain.Session{
					IssueID:   "test",
					State:     domain.SessionBusy,
					Activity:  "no-agent",
					StartedAt: &startedAt,
				},
				wantIcon: domain.SessionState("no-agent").Icon(),
			},
			{
				name: "unknown",
				session: &domain.Session{
					IssueID:   "test",
					State:     domain.SessionBusy,
					Activity:  "unknown",
					StartedAt: &startedAt,
				},
				wantIcon: domain.SessionState("unknown").Icon(),
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := stripANSI(renderSessionStatus(tt.session, s))
				if !strings.Contains(result, tt.wantIcon) {
					t.Fatalf("session should show icon %q, got: %s", tt.wantIcon, result)
				}
				if !strings.Contains(result, "2h") || strings.Contains(result, "15m") {
					t.Fatalf("session should show compact age, got: %s", result)
				}
			})
		}
	})
}

func TestRenderSessionStatusLabelUsesReadableLabelsOrIconOnly(t *testing.T) {
	tests := []struct {
		name    string
		session *domain.Session
		want    string
	}{
		{name: "nil", session: nil, want: "idle"},
		{name: "busy", session: &domain.Session{State: domain.SessionBusy}, want: "busy"},
		{name: "idle", session: &domain.Session{State: domain.SessionIdle}, want: "idle"},
		{name: "waiting", session: &domain.Session{State: domain.SessionWaiting}, want: "wait"},
		{name: "done", session: &domain.Session{State: domain.SessionDone}, want: "done"},
		{name: "error", session: &domain.Session{State: domain.SessionError}, want: ""},
		{name: "paused", session: &domain.Session{State: domain.SessionPaused}, want: ""},
		{name: "partial", session: &domain.Session{State: domain.SessionBusy, ActiveCount: 1, PausedCount: 1}, want: ""},
		{name: "no agent", session: &domain.Session{State: domain.SessionBusy, Activity: "no-agent"}, want: ""},
		{name: "unknown", session: &domain.Session{State: domain.SessionBusy, Activity: "unknown"}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderSessionStatusLabel(tt.session)
			if got != tt.want {
				t.Fatalf("renderSessionStatusLabel() = %q, want %q", got, tt.want)
			}
			if len(got) > 4 {
				t.Fatalf("renderSessionStatusLabel() = %q, want at most 4 chars", got)
			}
		})
	}
}

func TestRenderSessionStatusCompactUsesIconAndAge(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-90 * time.Minute)
	tests := []struct {
		name    string
		session *domain.Session
		want    string
	}{
		{name: "busy", session: &domain.Session{State: domain.SessionBusy, StartedAt: &startedAt}, want: "● 1h"},
		{name: "idle", session: &domain.Session{State: domain.SessionBusy, Activity: "idle", StartedAt: &startedAt}, want: "○ 1h"},
		{name: "waiting", session: &domain.Session{State: domain.SessionWaiting, StartedAt: &startedAt}, want: "◐ 1h"},
		{name: "paused", session: &domain.Session{State: domain.SessionPaused}, want: "⏸"},
		{name: "nil", session: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(renderSessionStatusCompact(tt.session, s))
			if got != tt.want {
				t.Fatalf("renderSessionStatusCompact() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderCard_NestedIssueShowsTreeContext(t *testing.T) {
	s := styles.New()
	parentID := naming.IssueID("az-parent")
	task := domain.Task{
		ID:       "az-child",
		Title:    "Nested task",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		ParentID: &parentID,
	}

	result := stripANSI(RenderCard(task, false, false, 56, s))
	if !strings.Contains(result, nestedIssuePrefix+"az-child") {
		t.Fatalf("nested task should show tree prefix before issue id, got: %s", result)
	}
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
		TmuxAttached:          true,
		HasWorktree:           true,
		GitAheadCount:         2,
		GitBehindCount:        4,
		HasUncommittedChanges: true,
		GitAdditions:          8,
		GitDeletions:          2,
	}

	result := renderCard(task, CardState{}, signals, nil, nil, 72, s)
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

	for _, token := range []string{tmuxSessionToken, tmuxAttachedToken, worktreeToken, "↑2", "↓4", "✎", "+8/-2"} {
		if !strings.Contains(headerLine, token) {
			t.Fatalf("header should contain %q, got: %s", token, headerLine)
		}
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

	result := stripANSI(renderCard(task, CardState{}, signals, &ChildProgress{Total: 7, Done: 1}, nil, 90, s))
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
	for _, token := range []string{"P2", "CHE-3002", " T ", "wait", "2d", worktreeToken, "✎", "+12/-3", "[1/7]"} {
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

func TestRenderCard_NarrowWidthCompactsHeaderOnSingleRow(t *testing.T) {
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

	result := stripANSI(renderCard(task, CardState{}, signals, &ChildProgress{Total: 7, Done: 1}, nil, 40, s))
	lines := strings.Split(result, "\n")
	firstIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "CHE-3010") {
			firstIdx = i
			break
		}
	}
	if firstIdx < 0 {
		t.Fatalf("expected metadata line containing issue id, got: %q", result)
	}
	first := lines[firstIdx]
	if first == "" {
		t.Fatalf("expected metadata line containing issue id, got: %s", result)
	}
	for _, token := range []string{"P2", "CHE-3010", "◐", "1h", "[1/7]", " T ", "✓G*"} {
		if !strings.Contains(first, token) {
			t.Fatalf("expected compact token %q in first header line, got: %s", token, first)
		}
	}
	if strings.Contains(first, "wait") || strings.Contains(first, "+12/-3") {
		t.Fatalf("first line must preserve issue identity and session state, got: %s", first)
	}
	if firstIdx+1 >= len(lines) || !strings.Contains(lines[firstIdx+1], "narrow header") {
		t.Fatalf("expected title to start after compact header, got: %q", result)
	}
}

func TestRenderCard_SessionActivityPrecedesTaskType(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-90 * time.Minute)
	task := domain.Task{
		ID:       "CHE-3011",
		Title:    "activity before type",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeBug,
		Session: &domain.Session{
			IssueID:        "CHE-3011",
			State:          domain.SessionBusy,
			Activity:       "idle",
			ActivitySource: "hooks",
			StartedAt:      &startedAt,
		},
	}

	result := stripANSI(renderCard(task, CardState{}, nil, nil, nil, 64, s))
	lines := strings.Split(result, "\n")
	var header string
	for _, line := range lines {
		if strings.Contains(line, "CHE-3011") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("expected header line containing issue id, got: %s", result)
	}
	activityIdx := strings.Index(header, "idle")
	typeIdx := strings.LastIndex(header, " B ")
	if activityIdx < 0 {
		t.Fatalf("expected idle activity token in header, got: %s", header)
	}
	if typeIdx < 0 {
		t.Fatalf("expected task type token in header, got: %s", header)
	}
	if activityIdx > typeIdx {
		t.Fatalf("activity token should precede type token, got: %s", header)
	}
}

func TestRenderCard_InReviewSuppressesBusySessionStatus(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-90 * time.Minute)
	task := domain.Task{
		ID:       "CHE-3012",
		Title:    "ready for review",
		Status:   domain.StatusInReview,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:        "CHE-3012",
			State:          domain.SessionBusy,
			Activity:       "working",
			ActivitySource: "hooks",
			StartedAt:      &startedAt,
		},
	}

	result := stripANSI(renderCard(task, CardState{}, &RuntimeSignals{HasTmuxSession: true}, nil, nil, 64, s))
	if strings.Contains(result, "busy") || strings.Contains(result, "working") || strings.Contains(result, domain.SessionBusy.Icon()) {
		t.Fatalf("in_review card should suppress busy session status, got: %s", result)
	}
	if !strings.Contains(result, tmuxSessionToken+" 1h") || strings.Contains(result, "30m") {
		t.Fatalf("in_review card should keep compact session age when busy session is suppressed, got: %s", result)
	}
	if !strings.Contains(result, " T ") {
		t.Fatalf("in_review card should keep type metadata when busy session is suppressed, got: %s", result)
	}
}

func TestRenderCard_InReviewIdleSessionShowsAge(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-2*time.Hour - 15*time.Minute)
	task := domain.Task{
		ID:       "CHE-3013",
		Title:    "ready but idle",
		Status:   domain.StatusInReview,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:        "CHE-3013",
			State:          domain.SessionBusy,
			Activity:       "idle",
			ActivitySource: "hooks",
			StartedAt:      &startedAt,
		},
	}

	result := stripANSI(renderCard(task, CardState{}, nil, nil, nil, 64, s))
	if !strings.Contains(result, "idle") {
		t.Fatalf("in_review idle card should show idle activity, got: %s", result)
	}
	if !strings.Contains(result, "2h") || strings.Contains(result, "15m") {
		t.Fatalf("in_review idle card should show compact session age, got: %s", result)
	}
}

func TestRenderCard_NarrowWidthAddsBodyCapacityWithoutMovingTitleBaseline(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-80 * time.Minute)
	task := domain.Task{
		ID:       "CHE-4010",
		Title:    "narrow-height-check",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:   "CHE-4010",
			State:     domain.SessionWaiting,
			StartedAt: &startedAt,
		},
	}
	signals := &RuntimeSignals{
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          9,
		GitDeletions:          3,
	}
	progress := &ChildProgress{Total: 9, Done: 2}

	wide := stripANSI(renderCard(task, CardState{}, signals, progress, nil, 80, s))
	narrow := stripANSI(renderCard(task, CardState{}, signals, progress, nil, 22, s))

	wideLines := strings.Split(wide, "\n")
	narrowLines := strings.Split(narrow, "\n")
	if len(narrowLines) != len(wideLines)+1 {
		t.Fatalf("narrow card height = %d, want wide+1 (%d)", len(narrowLines), len(wideLines)+1)
	}

	wideHeader, wideTitle := wideLines[1], wideLines[2]
	narrowHeader, narrowTitle := narrowLines[1], narrowLines[2]
	if strings.TrimSpace(narrowHeader) == "" {
		t.Fatalf("expected non-empty narrow header row, got: %q", narrowHeader)
	}
	if strings.TrimSpace(narrowTitle) == "" || strings.TrimSpace(wideTitle) == "" {
		t.Fatalf("expected non-empty title rows, got narrow=%q wide=%q", narrowTitle, wideTitle)
	}
	if !strings.Contains(narrowTitle, "narrow-height") {
		t.Fatalf("narrow title must immediately follow its single header row, got header=%q title=%q", narrowHeader, narrowTitle)
	}
	if strings.TrimSpace(wideHeader) == "" {
		t.Fatalf("expected non-empty wide header row, got %q", wideHeader)
	}
}

func TestRenderCard_NarrowWidthKeepsCoreTokensOnSingleHeaderRow(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-75 * time.Minute)
	task := domain.Task{
		ID:       "CHE-4012",
		Title:    "narrow-verbose-header-check",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:   "CHE-4012",
			State:     domain.SessionWaiting,
			StartedAt: &startedAt,
		},
	}
	signals := &RuntimeSignals{
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          12,
		GitDeletions:          3,
	}

	narrow := stripANSI(renderCard(task, CardState{}, signals, &ChildProgress{Total: 7, Done: 1}, nil, 25, s))
	lines := strings.Split(narrow, "\n")
	firstIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "CHE-4012") {
			firstIdx = i
			break
		}
	}
	if firstIdx < 0 || firstIdx+1 >= len(lines) {
		t.Fatalf("expected header row and title row, got: %q", narrow)
	}
	header := lines[firstIdx]
	for _, token := range []string{"P2", "CHE-4012", "◐", "1h"} {
		if !strings.Contains(header, token) {
			t.Fatalf("expected core token %q to be preserved in narrow header, got: %q", token, header)
		}
	}
	if strings.Contains(header, "wait") || strings.Contains(header, "+12/-3") || strings.Contains(header, "✓G*") {
		t.Fatalf("narrow compact header should omit supplemental session/git text, got: %q", header)
	}
	if !strings.Contains(lines[firstIdx+1], "narrow-verbose") {
		t.Fatalf("title must immediately follow the single header row, got: %q", narrow)
	}
}

func TestRenderCard_HeaderOverflowOmitsSupplementalTokens(t *testing.T) {
	s := styles.New()
	startedAt := time.Now().Add(-70 * time.Minute)
	task := domain.Task{
		ID:       "CHE-4011",
		Title:    "header-overflow-check",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Session: &domain.Session{
			IssueID:   "CHE-4011",
			State:     domain.SessionWaiting,
			StartedAt: &startedAt,
		},
	}
	signals := &RuntimeSignals{
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          12,
		GitDeletions:          4,
	}
	progress := &ChildProgress{Total: 8, Done: 1}

	narrow := stripANSI(renderCard(task, CardState{}, signals, progress, nil, 25, s))
	wide := stripANSI(renderCard(task, CardState{}, signals, progress, nil, 80, s))

	narrowLines := strings.Split(narrow, "\n")
	wideLines := strings.Split(wide, "\n")
	if len(narrowLines) != len(wideLines)+1 {
		t.Fatalf("narrow card height = %d, want wide+1 (%d)", len(narrowLines), len(wideLines)+1)
	}
	if !strings.Contains(narrowLines[2], "header-overflow") {
		t.Fatalf("title must immediately follow the single header row, got %q", narrow)
	}
	if strings.Contains(narrowLines[1], "+12/-4") || strings.Contains(narrowLines[1], "[1/8]") {
		t.Fatalf("overflowing supplemental tokens should be omitted from the header, got %q", narrowLines[1])
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
			TmuxAttachedCount:        2,
			HasWorktree:              true,
			GitAheadCount:            1,
			GitBehindCount:           2,
			HasUncommittedChanges:    true,
			HasConflicts:             true,
			GitAdditions:             10,
			GitDeletions:             3,
			PendingOperationState:    "queued",
			PendingOperationPercent:  25,
		}
		got := stripANSI(renderRuntimeSignals(signals, styles.New()))
		if !strings.Contains(got, tmuxSessionToken) ||
			!strings.Contains(got, descendantTmuxSessionToken) ||
			!strings.Contains(got, "A2") ||
			!strings.Contains(got, worktreeToken) ||
			!strings.Contains(got, "M:queued(25%)") ||
			!strings.Contains(got, "↑1") ||
			!strings.Contains(got, "↓2") ||
			!strings.Contains(got, "conflict") ||
			!strings.Contains(got, "✎") ||
			!strings.Contains(got, "+10/-3") {
			t.Fatalf("renderRuntimeSignals(...) = %q, missing expected token(s)", got)
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

	t.Run("large git counts use whole suffixes", func(t *testing.T) {
		signals := &RuntimeSignals{
			GitAheadCount:         3333,
			GitBehindCount:        4,
			HasUncommittedChanges: true,
			GitAdditions:          3335,
			GitDeletions:          8827,
		}
		got := stripANSI(renderRuntimeSignals(signals, styles.New()))
		for _, want := range []string{"↑3k", "↓4", "+3k/-9k"} {
			if !strings.Contains(got, want) {
				t.Fatalf("renderRuntimeSignals(...) = %q, missing %q", got, want)
			}
		}
		for _, notWant := range []string{"↑3333", "+3335", "-8827"} {
			if strings.Contains(got, notWant) {
				t.Fatalf("renderRuntimeSignals(...) = %q, should compact %q", got, notWant)
			}
		}
	})

	t.Run("local preparing operation", func(t *testing.T) {
		signals := &RuntimeSignals{
			PendingOperationState: "preparing",
		}
		got := stripANSI(renderRuntimeSignals(signals, styles.New()))
		if !strings.Contains(got, "M:preparing") {
			t.Fatalf("renderRuntimeSignals(...) = %q, missing preparing token", got)
		}
	})

	t.Run("failed operation uses compact error marker", func(t *testing.T) {
		signals := &RuntimeSignals{
			PendingOperationState:   "failed",
			PendingOperationPercent: 75,
		}
		got := stripANSI(renderRuntimeSignals(signals, styles.New()))
		if got != "M:!" {
			t.Fatalf("renderRuntimeSignals(...) = %q, want M:!", got)
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
			TmuxAttached:             true,
			HasWorktree:              true,
			HasUncommittedChanges:    true,
			HasConflicts:             true,
			GitAdditions:             10,
			GitDeletions:             3,
			GitBehindCount:           4,
			PendingOperationState:    "running",
			PendingOperationPercent:  50,
		}
		got := stripANSI(renderRuntimeSignalsCompact(signals, styles.New()))
		if !strings.Contains(got, "T") || !strings.Contains(got, "Td") || !strings.Contains(got, tmuxAttachedToken) || !strings.Contains(got, worktreeToken) || !strings.Contains(got, "M:R50") || !strings.Contains(got, "C!") || !strings.Contains(got, "G*↓4") {
			t.Fatalf("renderRuntimeSignalsCompact(...) = %q, missing expected compact token(s)", got)
		}
		if strings.Contains(got, "+10/-3") {
			t.Fatalf("renderRuntimeSignalsCompact(...) = %q, should omit verbose line-change token", got)
		}
	})

	t.Run("shows directional pairing without line changes", func(t *testing.T) {
		signals := &RuntimeSignals{
			HasWorktree:    true,
			GitAheadCount:  1237,
			GitBehindCount: 210,
		}
		got := stripANSI(renderRuntimeSignalsCompact(signals, styles.New()))
		if !strings.Contains(got, "G↑1k/↓210") {
			t.Fatalf("renderRuntimeSignalsCompact(...) = %q, missing directional ahead/behind pairing", got)
		}
	})

	t.Run("failed operation stays compact", func(t *testing.T) {
		signals := &RuntimeSignals{
			PendingOperationState:   "failed",
			PendingOperationPercent: 50,
		}
		got := stripANSI(renderRuntimeSignalsCompact(signals, styles.New()))
		if got != "M:!" {
			t.Fatalf("renderRuntimeSignalsCompact(...) = %q, want M:!", got)
		}
	})
}

func TestFormatCompactCount(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want string
	}{
		{name: "small", n: 999, want: "999"},
		{name: "thousands round down", n: 3333, want: "3k"},
		{name: "thousands round up", n: 8827, want: "9k"},
		{name: "rounded thousands roll to million", n: 999500, want: "1M"},
		{name: "millions", n: 2500000, want: "3M"},
		{name: "negative", n: -3333, want: "-3k"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatCompactCount(tt.n); got != tt.want {
				t.Fatalf("formatCompactCount(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestRuntimeSignalsForHeader_SuppressesTmuxMarkersWithSession(t *testing.T) {
	startedAt := time.Now().Add(-10 * time.Minute)
	session := &domain.Session{
		IssueID:   "az-1",
		State:     domain.SessionBusy,
		StartedAt: &startedAt,
	}
	signals := &RuntimeSignals{
		HasTmuxSession:           true,
		HasDescendantTmuxSession: true,
		TmuxAttached:             true,
		TmuxAttachedCount:        1,
		HasWorktree:              true,
		GitAheadCount:            1,
	}

	got := runtimeSignalsForHeader(session, signals)
	if got == nil {
		t.Fatalf("runtimeSignalsForHeader(...) returned nil")
	}
	if got.HasTmuxSession || got.HasDescendantTmuxSession {
		t.Fatalf("runtimeSignalsForHeader(...) should hide tmux markers when session exists: %+v", got)
	}
	if !got.TmuxAttached || got.TmuxAttachedCount != 1 {
		t.Fatalf("runtimeSignalsForHeader(...) should preserve tmux attachment metadata: %+v", got)
	}
	if !got.HasWorktree || got.GitAheadCount != 1 {
		t.Fatalf("runtimeSignalsForHeader(...) should preserve non-tmux runtime signals: %+v", got)
	}
	if !signals.HasTmuxSession || !signals.HasDescendantTmuxSession {
		t.Fatalf("runtimeSignalsForHeader(...) must not mutate original signals: %+v", signals)
	}
}

func TestBuildChildProgress(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	tasks := []domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusOpen},
		{ID: "az-c1", Title: "Child 1", Status: domain.StatusDone, ParentID: &parentID},
		{ID: "az-c2", Title: "Child 2", Status: domain.StatusInProgress, ParentID: &parentID},
	}

	progress := BuildChildProgress(tasks)
	got, ok := progress[parentID.String()]
	if !ok {
		t.Fatalf("expected child progress for %s", parentID.String())
	}
	if got.Total != 2 || got.Done != 1 {
		t.Fatalf("progress = %+v, want total=2 done=1", got)
	}
}
