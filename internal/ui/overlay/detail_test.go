package overlay

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDetailPanel(t *testing.T) {
	task := domain.Task{
		ID:          "test-123",
		Title:       "Test Task",
		Description: "Test description",
		Status:      domain.StatusOpen,
		Priority:    domain.P0,
		Type:        domain.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	panel := NewDetailPanel(task)
	require.NotNil(t, panel)
	assert.Equal(t, task.ID, panel.task.ID)
	assert.Equal(t, 0, panel.scrollY)
}

func TestDetailPanelTitle(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task)

	assert.Equal(t, "Task Details", panel.Title())
}

func TestDetailPanelSize(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task)

	width, height := panel.Size()
	assert.Equal(t, 70, width)
	assert.Equal(t, 30, height)
}

func TestDetailPanelView(t *testing.T) {
	task := domain.Task{
		ID:          "az-123",
		Title:       "Implement feature",
		Description: "This is a test description",
		Status:      domain.StatusInProgress,
		Priority:    domain.P1,
		Type:        domain.TypeFeature,
		CreatedAt:   time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2025, 1, 2, 14, 30, 0, 0, time.UTC),
	}

	panel := NewDetailPanel(task)
	view := panel.View()

	// Check that key information is present
	assert.Contains(t, view, "az-123")
	assert.Contains(t, view, "Implement feature")
	assert.Contains(t, view, "In Progress")
	assert.Contains(t, view, "P1")
	assert.Contains(t, view, "F")
	assert.Contains(t, view, "This is a test description")
}

func TestDetailPanelViewWithSession(t *testing.T) {
	startTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	task := domain.Task{
		ID:                    "az-456",
		Title:                 "Task with session",
		Status:                domain.StatusInProgress,
		HasUncommittedChanges: true,
		GitAdditions:          9,
		GitDeletions:          2,
		GitAheadCount:         1,
		GitBehindCount:        3,
	}

	session := &domain.Session{
		IssueID:   "az-456",
		State:     domain.SessionBusy,
		StartedAt: &startTime,
		Worktree:  "/path/to/worktree",
		DevServer: &domain.DevServer{
			Port:    3000,
			Command: "npm run dev",
			Running: true,
		},
	}

	task.Session = session

	panel := NewDetailPanel(task)
	view := panel.View()

	// Check runtime sections are present
	assert.Contains(t, view, "Runtime")
	assert.Contains(t, view, "Session:")
	assert.Contains(t, view, "Worktree:")
	assert.Contains(t, view, "busy")
	assert.Contains(t, view, "Age")
	assert.Contains(t, view, "dirty")
	assert.Contains(t, view, "+9/-2")
	assert.Contains(t, view, "↑1/↓3")
	assert.Contains(t, view, "|")
	assert.Contains(t, view, "Age")
	assert.NotContains(t, view, "available")
	assert.NotContains(t, view, "Attached:")
	assert.Contains(t, view, ":3000")
	assert.Contains(t, view, "Dev :3000")
}

func TestDetailPanelViewShowsBehindOnlyDirectionalStatus(t *testing.T) {
	task := domain.Task{
		ID:             "az-790",
		Title:          "Task with behind-only divergence",
		Status:         domain.StatusInProgress,
		HasWorktree:    true,
		GitBehindCount: 5,
	}

	panel := NewDetailPanel(task)
	view := panel.View()

	assert.Contains(t, view, "↓5")
	assert.NotContains(t, view, "↑0/↓5")
}

func TestDetailPanelViewBaseDiffWithoutUncommittedShowsCleanStatus(t *testing.T) {
	task := domain.Task{
		ID:            "az-792",
		Title:         "Task with committed divergence only",
		Status:        domain.StatusInProgress,
		HasWorktree:   true,
		GitAdditions:  163,
		GitDeletions:  1,
		GitAheadCount: 2,
	}

	panel := NewDetailPanel(task)
	view := panel.View()

	assert.Contains(t, view, "clean (+163/-1; ↑2)")
	assert.NotContains(t, view, "dirty (+163/-1; ↑2)")
}

func TestDetailPanelViewWithSessionShowsCleanGitStatusWithoutTelemetry(t *testing.T) {
	task := domain.Task{
		ID:          "az-789",
		Title:       "Task with unavailable git telemetry",
		Status:      domain.StatusInProgress,
		HasWorktree: true,
	}
	session := &domain.Session{
		IssueID:  "az-789",
		State:    domain.SessionIdle,
		Worktree: "/tmp/az-789",
	}

	task.Session = session

	panel := NewDetailPanel(task)
	view := panel.View()

	assert.Contains(t, view, "Worktree:")
	assert.Contains(t, view, "clean")
	assert.Contains(t, view, "/tmp/az-789")
}

func TestDetailPanelViewWithGitOnlyStillShowsSessionSection(t *testing.T) {
	task := domain.Task{
		ID:          "az-791",
		Title:       "Task with git data but no session",
		Status:      domain.StatusInProgress,
		HasWorktree: true,
	}

	panel := NewDetailPanel(task)
	view := panel.View()

	assert.Contains(t, view, "Runtime")
	assert.Contains(t, view, "none")
	assert.Contains(t, view, "clean | present")
}

func TestDetailPanelViewWithParent(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	task := domain.Task{
		ID:       "az-child",
		Title:    "Child task",
		ParentID: &parentID,
		Status:   domain.StatusOpen,
	}

	panel := NewDetailPanel(task)
	view := panel.View()

	assert.Contains(t, view, "Parent:")
	assert.Contains(t, view, "az-parent")
}

func TestDetailPanelViewShowsTypedDependencies(t *testing.T) {
	taskID := "az-current"
	task := domain.Task{
		ID:       naming.IssueID(taskID),
		Title:    "Current task",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Dependencies: []domain.Dependency{
			{ID: "az-downstream", Type: domain.DependencyBlocks},
		},
	}

	related := []domain.Task{
		task,
		{
			ID:       "az-upstream",
			Title:    "Upstream task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: naming.IssueID(taskID), Type: domain.DependencyRelatedTo},
			},
		},
		{
			ID:       "az-downstream",
			Title:    "Downstream task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		},
	}

	panel := NewDetailPanel(task).WithRelatedTasks(related)
	view := panel.View()

	assert.Contains(t, view, "Dependencies")
	assert.Contains(t, view, "Outgoing")
	assert.Contains(t, view, "blocks -> az-downstream")
	assert.Contains(t, view, "Incoming")
	assert.Contains(t, view, "related <- az-upstream")
}

func TestDetailPanelViewShowsLoadedIssueMetadata(t *testing.T) {
	estimate := 5
	task := domain.Task{
		ID:              "az-full",
		Title:           "Full task detail",
		Status:          domain.StatusInProgress,
		Priority:        domain.P1,
		Type:            domain.TypeFeature,
		Assignee:        "riordan",
		Labels:          []string{"tui", "detail"},
		Estimate:        &estimate,
		Implementations: []string{"go"},
		Design:          "Use the existing detail panel.",
		Acceptance:      "Every projected field is visible.",
		Notes:           "Keep this in the task workspace.",
	}

	panel := NewDetailPanel(task)
	view := panel.View()

	assert.Contains(t, view, "Assignee:")
	assert.Contains(t, view, "riordan")
	assert.Contains(t, view, "Estimate:")
	assert.Contains(t, view, "5")
	assert.Contains(t, view, "Labels:")
	assert.Contains(t, view, "tui, detail")
	assert.Contains(t, view, "Impls:")
	assert.Contains(t, view, "go")
	assert.Contains(t, view, "Design")
	assert.Contains(t, view, "Use the existing detail panel.")
	assert.Contains(t, view, "Acceptance")
	assert.Contains(t, view, "Every projected field is visible.")
	assert.Contains(t, view, "Notes")
	assert.Contains(t, view, "Keep this in the task workspace.")
}

func TestDetailPanelViewGroupsGraphRelationsByType(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	rootID := naming.IssueID("az-root")
	task := domain.Task{
		ID:       "az-current",
		Title:    "Current task",
		Status:   domain.StatusInProgress,
		Priority: domain.P2,
		Type:     domain.TypeTask,
		Dependencies: []domain.Dependency{
			{ID: "az-blocked-target", Type: domain.DependencyBlocks},
			{ID: "az-related-peer", Type: domain.DependencyRelatedTo},
			{ID: "az-discovered-source", Type: domain.DependencyDiscovered},
		},
		ParentID: &parentID,
	}
	related := []domain.Task{
		task,
		{ID: "az-parent", Title: "Parent task", Status: domain.StatusOpen, ParentID: &rootID},
		{ID: rootID, Title: "Root task", Status: domain.StatusOpen},
		{ID: "az-direct-child", Title: "Direct child", Status: domain.StatusOpen, ParentID: pointerToIssueID("az-current")},
		{ID: "az-blocked-target", Title: "Downstream blockee", Status: domain.StatusBlocked},
		{
			ID:           "az-blocker",
			Title:        "Upstream blocker",
			Status:       domain.StatusOpen,
			Dependencies: []domain.Dependency{{ID: "az-current", Type: domain.DependencyBlocks}},
		},
		{ID: "az-related-peer", Title: "Related peer", Status: domain.StatusOpen},
		{ID: "az-discovered-source", Title: "Discovered-from origin", Status: domain.StatusDone},
		{
			ID:           "az-spinoff",
			Title:        "Spinoff",
			Status:       domain.StatusOpen,
			Dependencies: []domain.Dependency{{ID: "az-current", Type: domain.DependencyDiscovered}},
		},
	}

	panel := NewDetailPanel(task).WithRelatedTasks(related)
	panel.viewHeight = 60
	view := panel.View()

	assert.Contains(t, view, "Graph")

	// Old flat labels are gone.
	assert.NotContains(t, view, "Ascendants")
	assert.NotContains(t, view, "Descendants")

	// Each relation has its own section with the expected row.
	// "Parent" is singular because a task has at most one parent;
	// the chain is not walked transitively here (use l/> to navigate up).
	assert.Contains(t, view, "Parent")
	assert.Contains(t, view, "< az-parent [Open] Parent task")
	assert.NotContains(t, view, "az-root [Open] Root task")

	assert.Contains(t, view, "Children")
	assert.Contains(t, view, "> az-direct-child [Open] Direct child")

	assert.Contains(t, view, "Blocks")
	assert.Contains(t, view, "> az-blocked-target [Blocked] Downstream blockee")

	assert.Contains(t, view, "Blocked by")
	assert.Contains(t, view, "< az-blocker [Open] Upstream blocker")

	assert.Contains(t, view, "Related")
	assert.Contains(t, view, "> az-related-peer [Open] Related peer")

	assert.Contains(t, view, "Discovered from")
	assert.Contains(t, view, "< az-discovered-source [Done] Discovered-from origin")

	assert.Contains(t, view, "Discovered")
	assert.Contains(t, view, "> az-spinoff [Open] Spinoff")
}

func TestDetailPanelGraphCursorWalksAllSections(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	task := domain.Task{
		ID:       "az-current",
		Title:    "Current task",
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
		Dependencies: []domain.Dependency{
			{ID: "az-blocked-target", Type: domain.DependencyBlocks},
		},
	}
	related := []domain.Task{
		task,
		{ID: "az-parent", Title: "Parent task", Status: domain.StatusOpen},
		{ID: "az-blocked-target", Title: "Downstream blockee", Status: domain.StatusBlocked},
	}

	panel := NewDetailPanel(task).WithRelatedTasks(related)
	require.Equal(t, 2, panel.GraphLinkCount())

	first, ok := panel.SelectedGraphTaskID()
	require.True(t, ok)
	assert.Equal(t, "az-parent", first, "cursor starts on the first parent")

	panel.MoveGraphCursor(1)
	second, ok := panel.SelectedGraphTaskID()
	require.True(t, ok)
	assert.Equal(t, "az-blocked-target", second, "cursor crosses into the Blocks section")
}

func TestDetailPanelGraphOmitsEmptySections(t *testing.T) {
	task := domain.Task{
		ID:    "az-loner",
		Title: "Lonely",
		Dependencies: []domain.Dependency{
			{ID: "az-related-peer", Type: domain.DependencyRelatedTo},
		},
	}
	related := []domain.Task{
		task,
		{ID: "az-related-peer", Title: "Related peer", Status: domain.StatusOpen},
	}
	panel := NewDetailPanel(task).WithRelatedTasks(related)
	view := panel.View()

	// Only the relation that actually has a row is rendered.
	assert.Contains(t, view, "Related")
	assert.NotContains(t, view, "Parent")
	assert.NotContains(t, view, "Children")
	assert.NotContains(t, view, "Blocked by")
	assert.NotContains(t, view, "Discovered from")
	assert.NotContains(t, view, "Discovered\n")
	// No "- none" placeholders either.
	assert.NotContains(t, view, "- none")
}

func TestDetailPanelGraphHeaderHiddenWhenNoLinks(t *testing.T) {
	task := domain.Task{ID: "az-solo", Title: "Truly alone"}
	panel := NewDetailPanel(task).WithRelatedTasks([]domain.Task{task})
	view := panel.View()

	assert.NotContains(t, view, "Graph")
	assert.NotContains(t, view, "Parent")
	assert.NotContains(t, view, "Children")
	assert.NotContains(t, view, "Blocks")
	assert.NotContains(t, view, "Related")
}

func TestDetailPanelDependenciesOmitsEmptySides(t *testing.T) {
	taskID := naming.IssueID("az-outgoing-only")
	task := domain.Task{
		ID:    taskID,
		Title: "Outgoing-only",
		Dependencies: []domain.Dependency{
			{ID: "az-downstream", Type: domain.DependencyBlocks},
		},
	}
	related := []domain.Task{
		task,
		{ID: "az-downstream", Title: "Downstream", Status: domain.StatusOpen},
	}
	panel := NewDetailPanel(task).WithRelatedTasks(related)
	view := panel.View()

	assert.Contains(t, view, "Outgoing")
	assert.Contains(t, view, "blocks -> az-downstream")
	// Incoming side has no rows, so the subheader is suppressed.
	assert.NotContains(t, view, "Incoming")
	assert.NotContains(t, view, "- none")
}

func pointerToIssueID(id naming.IssueID) *naming.IssueID {
	return &id
}

func TestDetailPanelScrolling(t *testing.T) {
	// Create a task with a long description
	lines := make([]string, 50)
	for i := 0; i < 50; i++ {
		lines[i] = "Line " + string(rune('A'+i%26))
	}
	description := strings.Join(lines, "\n")

	task := domain.Task{
		ID:          "test",
		Description: description,
	}

	panel := NewDetailPanel(task)

	// Initial scroll position should be 0
	assert.Equal(t, 0, panel.scrollY)

	// Scroll down
	m, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 1, panel.scrollY)

	// Scroll down multiple times
	for i := 0; i < 5; i++ {
		m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		panel = m.(*DetailPanel)
	}
	assert.Equal(t, 6, panel.scrollY)

	// Scroll up
	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 5, panel.scrollY)

	panel.descViewHeight = 8
	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	panel = m.(*DetailPanel)
	assert.Equal(t, 9, panel.scrollY)

	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	panel = m.(*DetailPanel)
	assert.Equal(t, 5, panel.scrollY)

	// Jump to top
	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 0, panel.scrollY)

	// Jump to bottom
	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	panel = m.(*DetailPanel)
	assert.Greater(t, panel.scrollY, 0)
}

func TestDetailPanelScrollsWhenGraphLinksExist(t *testing.T) {
	child := domain.Task{ID: "az-child", Title: "Child", Status: domain.StatusOpen}
	task := domain.Task{
		ID:          "az-parent",
		Title:       "Parent",
		Status:      domain.StatusOpen,
		Description: strings.Repeat("line\n", 80),
		Dependencies: []domain.Dependency{
			{ID: child.ID, Type: domain.DependencyBlocks},
		},
	}
	panel := NewDetailPanel(task).WithRelatedTasks([]domain.Task{task, child})

	m, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 1, panel.scrollY)
	assert.Equal(t, 0, panel.graphCursor)

	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 1, panel.scrollY)

	selected, ok := panel.SelectedGraphTaskID()
	require.True(t, ok)
	assert.Equal(t, child.ID.String(), selected)
}

func TestDetailPanelScrollLimits(t *testing.T) {
	task := domain.Task{
		ID:          "test",
		Description: "Short description",
	}

	panel := NewDetailPanel(task)

	// Should not scroll below 0
	m, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 0, panel.scrollY)

	// Should not scroll past maxScroll
	for i := 0; i < 100; i++ {
		m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		panel = m.(*DetailPanel)
	}
	assert.LessOrEqual(t, panel.scrollY, panel.maxScroll())
}

func TestDetailPanelScrollAccountsForWrappedLines(t *testing.T) {
	task := domain.Task{
		ID:          "wrap-test",
		Description: strings.Repeat("verylongtokenwithoutspaces", 20),
	}
	panel := NewDetailPanel(task)
	panel.viewHeight = 5
	panel.wrapWidth = 20
	_ = panel.View()

	assert.Greater(t, panel.maxScroll(), 0, "expected wrapped description to become scrollable")
}

func TestDetailPanelCompactModeScrollsEntirePanel(t *testing.T) {
	task := domain.Task{
		ID:          "az-compact",
		Title:       "Unique Compact Header",
		Status:      domain.StatusInProgress,
		Description: strings.Repeat("line\n", 120),
		CreatedAt:   time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC),
	}

	panel := NewDetailPanel(task)
	panel.viewHeight = 8
	panel.wrapWidth = 24

	initialView := panel.View()
	assert.Contains(t, initialView, "Unique Compact Header")
	assert.Greater(t, panel.maxScroll(), 0)

	m, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	panel = m.(*DetailPanel)
	scrolledView := panel.View()

	assert.NotContains(t, scrolledView, "Unique Compact Header")
}

func TestDetailPanelAutoScrollsToFocusedGraphRow(t *testing.T) {
	child := domain.Task{ID: "az-child", Title: "Child", Status: domain.StatusOpen}
	task := domain.Task{
		ID:    "az-parent",
		Title: "Parent",
		// Push the Graph section far below the viewport by adding long
		// Design/Notes content above it.
		Design: strings.Repeat("design line\n", 30),
		Notes:  strings.Repeat("notes line\n", 30),
		Status: domain.StatusOpen,
		Dependencies: []domain.Dependency{
			{ID: child.ID, Type: domain.DependencyBlocks},
		},
	}
	panel := NewDetailPanel(task).WithRelatedTasks([]domain.Task{task, child})
	panel.viewHeight = 10
	panel.wrapWidth = 40

	// Render once unfocused: graph is below the fold, scrollY stays put.
	_ = panel.View()
	assert.Equal(t, 0, panel.scrollY)

	// Focus the graph; the next render must scroll until the highlighted row is visible.
	panel.graphFocused = true
	view := panel.View()
	assert.Greater(t, panel.scrollY, 0, "expected scrollY to advance so the focused graph row enters the viewport")
	assert.Contains(t, view, "> az-child [Open] Child")
}

func TestDetailPanelEntirePanelScrollsWhenContentExceedsHeight(t *testing.T) {
	// The whole panel must be reachable via scroll, not just the description.
	task := domain.Task{
		ID:     "az-stack",
		Title:  "Stacked sections",
		Status: domain.StatusOpen,
		Design: strings.Repeat("design row\n", 20),
		Notes:  "FINAL_NOTES_MARKER",
	}
	panel := NewDetailPanel(task)
	panel.viewHeight = 8
	panel.wrapWidth = 60

	initial := panel.View()
	assert.NotContains(t, initial, "FINAL_NOTES_MARKER", "marker should start below the fold")

	panel.scrollY = panel.maxScroll()
	scrolled := panel.View()
	assert.Contains(t, scrolled, "FINAL_NOTES_MARKER", "scrolling to the bottom must reveal late sections")
}

func TestWrapDescriptionLines_HardWrapFallbackForLongToken(t *testing.T) {
	lines := wrapDescriptionLines(strings.Repeat("x", 100), 20)
	require.Greater(t, len(lines), 1)
	for _, line := range lines {
		assert.LessOrEqual(t, len(line), 20)
	}
}

func TestDetailPanelEscapeCloses(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task)

	// Test Esc key
	_, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	closeMsg, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
	assert.NotNil(t, closeMsg)

	// Test q key
	_, cmd = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)

	msg = cmd()
	closeMsg, ok = msg.(CloseOverlayMsg)
	assert.True(t, ok)
	assert.NotNil(t, closeMsg)
}

func TestDetailPanelFormatStatus(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task)

	tests := []struct {
		status   domain.Status
		expected string
	}{
		{domain.StatusOpen, "Open"},
		{domain.StatusInProgress, "In Progress"},
		{domain.StatusBlocked, "Blocked"},
		{domain.StatusDone, "Done"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := panel.formatStatus(tt.status)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetailPanelFormatDuration(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task)

	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{3665 * time.Second, "1h 1m 5s"},
		{7200 * time.Second, "2h 0m 0s"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := panel.formatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetailPanelRendersDecisionLinks(t *testing.T) {
	task := domain.Task{
		ID:        "az-42",
		Title:     "Implement decisions",
		Status:    domain.StatusInProgress,
		Priority:  domain.P1,
		Type:      domain.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	panel := NewDetailPanel(task).WithDecisionLinks([]DecisionLinkSummary{
		{
			DecisionID:    "dec-1",
			DecisionTitle: "Use SQLite for decision store",
			Relation:      "applies-to",
		},
		{
			DecisionID: "dec-2",
			Relation:   "informs",
			Note:       "discussed at sync",
		},
	})

	view := panel.View()

	assert.Contains(t, view, "Decisions")
	assert.Contains(t, view, "applies-to")
	assert.Contains(t, view, "dec-1")
	assert.Contains(t, view, "Use SQLite for decision store")
	assert.Contains(t, view, "informs")
	assert.Contains(t, view, "dec-2")
	assert.Contains(t, view, "discussed at sync")
	assert.NotContains(t, view, "[accepted]", "status should no longer appear in the Decisions row")
}

func TestDetailPanelOmitsDecisionsSectionWhenEmpty(t *testing.T) {
	task := domain.Task{
		ID:        "az-43",
		Title:     "No decisions linked",
		Status:    domain.StatusOpen,
		Priority:  domain.P2,
		Type:      domain.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	view := NewDetailPanel(task).View()
	assert.NotContains(t, view, "Decisions")
}

func TestDecisionLinksAccessorReturnsCopy(t *testing.T) {
	panel := NewDetailPanel(domain.Task{ID: "x"}).WithDecisionLinks([]DecisionLinkSummary{
		{DecisionID: "d-1", Relation: "relates"},
	})
	copy := panel.DecisionLinks()
	require.Len(t, copy, 1)
	copy[0].Relation = "mutated"
	again := panel.DecisionLinks()
	assert.Equal(t, "relates", again[0].Relation, "DecisionLinks() must return a defensive copy")
}
