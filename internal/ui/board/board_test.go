package board

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

var update = flag.Bool("update", false, "update golden files")

func columnsToTasks(columns []Column) []domain.Task {
	tasks := make([]domain.Task, 0)
	for _, col := range columns {
		tasks = append(tasks, col.Tasks...)
	}
	return tasks
}

func normalizeBoardOutput(s string) string {
	s = ansi.Strip(s)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.Join(lines, "\n")
}

func TestRender(t *testing.T) {
	tests := []struct {
		name          string
		cursor        Cursor
		selectedTasks map[string]bool
		width         int
		height        int
	}{
		{
			name:          "default_cursor_at_origin",
			cursor:        Cursor{Column: 0, Task: 0},
			selectedTasks: make(map[string]bool),
			width:         120,
			height:        30,
		},
		{
			name:          "cursor_in_progress_column",
			cursor:        Cursor{Column: 1, Task: 0},
			selectedTasks: make(map[string]bool),
			width:         120,
			height:        30,
		},
		{
			name:          "cursor_on_second_task",
			cursor:        Cursor{Column: 0, Task: 1},
			selectedTasks: make(map[string]bool),
			width:         120,
			height:        30,
		},
		{
			name:   "with_selected_tasks",
			cursor: Cursor{Column: 0, Task: 0},
			selectedTasks: map[string]bool{
				"az-2": true,
				"az-4": true,
			},
			width:  120,
			height: 30,
		},
		{
			name:          "narrow_terminal",
			cursor:        Cursor{Column: 0, Task: 0},
			selectedTasks: make(map[string]bool),
			width:         80,
			height:        24,
		},
		{
			name:          "wide_terminal",
			cursor:        Cursor{Column: 3, Task: 1},
			selectedTasks: make(map[string]bool),
			width:         160,
			height:        40,
		},
	}

	s := styles.New()
	columns := CreatePlaceholderData()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Render(columns, tt.cursor, tt.selectedTasks, map[string]RuntimeSignals{}, BuildChildProgress(columnsToTasks(columns)), nil, false, nil, 0, s, tt.width, tt.height)

			goldenFile := filepath.Join("testdata", tt.name+".golden")

			if *update {
				// Update golden file
				dir := filepath.Dir(goldenFile)
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("failed to create testdata dir: %v", err)
				}
				if err := os.WriteFile(goldenFile, []byte(got), 0644); err != nil {
					t.Fatalf("failed to update golden file: %v", err)
				}
			}

			// Read golden file
			want, err := os.ReadFile(goldenFile)
			if err != nil {
				t.Fatalf("failed to read golden file: %v\nRun with -update flag to create it", err)
			}

			if normalizeBoardOutput(got) != normalizeBoardOutput(string(want)) {
				t.Errorf("Render() output mismatch\nGot:\n%s\n\nWant:\n%s", got, string(want))
			}
		})
	}
}

func TestRenderCard(t *testing.T) {
	tests := []struct {
		name       string
		taskIndex  int // Index into placeholder data
		isCursor   bool
		isSelected bool
		width      int
	}{
		{
			name:       "normal_card",
			taskIndex:  0,
			isCursor:   false,
			isSelected: false,
			width:      25,
		},
		{
			name:       "cursor_card",
			taskIndex:  0,
			isCursor:   true,
			isSelected: false,
			width:      25,
		},
		{
			name:       "selected_card",
			taskIndex:  0,
			isCursor:   false,
			isSelected: true,
			width:      25,
		},
		{
			name:       "truncated_title",
			taskIndex:  0, // "Implement user authentication" should truncate
			isCursor:   false,
			isSelected: false,
			width:      20,
		},
	}

	s := styles.New()
	placeholderData := CreatePlaceholderData()
	task := placeholderData[0].Tasks[0] // Use first task from Open column

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderCard(task, tt.isCursor, tt.isSelected, tt.width, s)

			goldenFile := filepath.Join("testdata", "card_"+tt.name+".golden")

			if *update {
				dir := filepath.Dir(goldenFile)
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("failed to create testdata dir: %v", err)
				}
				if err := os.WriteFile(goldenFile, []byte(got), 0644); err != nil {
					t.Fatalf("failed to update golden file: %v", err)
				}
			}

			want, err := os.ReadFile(goldenFile)
			if err != nil {
				t.Fatalf("failed to read golden file: %v\nRun with -update flag to create it", err)
			}

			if normalizeBoardOutput(got) != normalizeBoardOutput(string(want)) {
				t.Errorf("RenderCard() output mismatch\nGot:\n%s\n\nWant:\n%s", got, string(want))
			}
		})
	}
}

func TestRenderCard_ShowsBlockedPhaseChip(t *testing.T) {
	s := styles.New()

	blocker := domain.Task{
		ID:       "az-blocker",
		Title:    "Blocker",
		Status:   domain.StatusOpen,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
	blocked := domain.Task{
		ID:       "az-blocked",
		Title:    "Blocked",
		Status:   domain.StatusBlocked,
		Priority: domain.P1,
		Type:     domain.TypeTask,
		Dependencies: []domain.Dependency{
			{ID: blocker.ID, Type: domain.DependencyBlocks},
		},
	}

	tasks := map[string]domain.Task{
		blocker.ID.String(): blocker,
		blocked.ID.String(): blocked,
	}
	taskIDs := map[string]bool{
		blocker.ID.String(): true,
		blocked.ID.String(): true,
	}
	phaseInfo := phases.ComputeDependencyPhases(taskIDs, tasks)

	blockerPhase := phaseInfo.Phases[blocker.ID.String()]
	blockedPhase := phaseInfo.Phases[blocked.ID.String()]

	blockerView := normalizeBoardOutput(renderCard(blocker, nil, false, false, 80, nil, &blockerPhase, true, "", s))
	blockedView := normalizeBoardOutput(renderCard(blocked, nil, false, false, 80, nil, &blockedPhase, true, "", s))

	if !strings.Contains(blockerView, "Φ0") {
		t.Fatalf("expected ready blocker chip in view, got %q", blockerView)
	}
	if !strings.Contains(blockedView, "Φ1") {
		t.Fatalf("expected blocked chip in view, got %q", blockedView)
	}
}

func TestRenderCard_OriginBadgeBottomRight(t *testing.T) {
	s := styles.New()

	cases := []struct {
		name      string
		origin    string
		want      string
		expectAny bool
	}{
		{name: "linear", origin: "linear", want: "lin", expectAny: true},
		{name: "local_explicit", origin: "local", want: "loc", expectAny: true},
		{name: "github", origin: "github", want: "gh", expectAny: true},
		{name: "empty_renders_no_badge", origin: "", want: "", expectAny: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := domain.Task{
				ID:       "az-1",
				Title:    "Origin test",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
				Origin:   tc.origin,
			}
			view := normalizeBoardOutput(renderCard(task, nil, false, false, 30, nil, nil, false, "", s))
			lines := strings.Split(view, "\n")
			if len(lines) < 2 {
				t.Fatalf("unexpected card with %d lines: %q", len(lines), view)
			}
			lastContent := lines[len(lines)-2]
			if !tc.expectAny {
				for _, marker := range []string{"lin", "loc", "gh"} {
					if strings.Contains(lastContent, marker) {
						t.Fatalf("expected no badge for empty origin, got %q in %q", marker, lastContent)
					}
				}
				return
			}
			if !strings.Contains(lastContent, tc.want) {
				t.Fatalf("expected last content line to contain %q (origin=%q), got %q\nfull view:\n%s", tc.want, tc.origin, lastContent, view)
			}
			trimmed := strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(lastContent, "│"), "│"), " ")
			if !strings.HasSuffix(trimmed, tc.want) {
				t.Fatalf("expected badge %q to be right-aligned on last content line, got %q", tc.want, lastContent)
			}
		})
	}
}

func TestRenderEmptyBoard(t *testing.T) {
	s := styles.New()
	got := Render([]Column{}, Cursor{}, make(map[string]bool), map[string]RuntimeSignals{}, nil, nil, false, nil, 0, s, 120, 30)

	if got != "" {
		t.Errorf("Render() with empty columns should return empty string, got: %q", got)
	}
}

func TestCursorBounds(t *testing.T) {
	// Test that rendering doesn't panic with out-of-bounds cursor
	s := styles.New()
	columns := CreatePlaceholderData()

	tests := []struct {
		name   string
		cursor Cursor
	}{
		{
			name:   "cursor_column_out_of_bounds",
			cursor: Cursor{Column: 99, Task: 0},
		},
		{
			name:   "cursor_task_out_of_bounds",
			cursor: Cursor{Column: 0, Task: 99},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			_ = Render(columns, tt.cursor, make(map[string]bool), map[string]RuntimeSignals{}, BuildChildProgress(columnsToTasks(columns)), nil, false, nil, 0, s, 120, 30)
		})
	}
}
