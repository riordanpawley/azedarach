package board

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

var update = flag.Bool("update", false, "update golden files")

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
			got := Render(columns, tt.cursor, tt.selectedTasks, nil, false, s, tt.width, tt.height)

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

			if got != string(want) {
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

			if got != string(want) {
				t.Errorf("RenderCard() output mismatch\nGot:\n%s\n\nWant:\n%s", got, string(want))
			}
		})
	}
}

func TestRenderEmptyBoard(t *testing.T) {
	s := styles.New()
	got := Render([]Column{}, Cursor{}, make(map[string]bool), nil, false, s, 120, 30)

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
			_ = Render(columns, tt.cursor, make(map[string]bool), nil, false, s, 120, 30)
		})
	}
}
