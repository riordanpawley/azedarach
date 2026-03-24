package board

import "testing"

func TestVisibleTaskRange(t *testing.T) {
	tests := []struct {
		name            string
		taskCount       int
		cursorTask      int
		availableHeight int
		wantStart       int
		wantEnd         int
	}{
		{
			name:            "empty_tasks",
			taskCount:       0,
			cursorTask:      0,
			availableHeight: 20,
			wantStart:       0,
			wantEnd:         0,
		},
		{
			name:            "fits_all_tasks",
			taskCount:       3,
			cursorTask:      2,
			availableHeight: 24,
			wantStart:       0,
			wantEnd:         3,
		},
		{
			name:            "cursor_near_top",
			taskCount:       10,
			cursorTask:      1,
			availableHeight: 12,
			wantStart:       0,
			wantEnd:         2,
		},
		{
			name:            "cursor_near_bottom",
			taskCount:       10,
			cursorTask:      9,
			availableHeight: 12,
			wantStart:       8,
			wantEnd:         10,
		},
		{
			name:            "invalid_cursor_defaults_top_window",
			taskCount:       10,
			cursorTask:      -1,
			availableHeight: 12,
			wantStart:       0,
			wantEnd:         2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := visibleTaskRange(tt.taskCount, tt.cursorTask, tt.availableHeight)
			if start != tt.wantStart || end != tt.wantEnd {
				t.Fatalf("visibleTaskRange() = (%d,%d), want (%d,%d)", start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

