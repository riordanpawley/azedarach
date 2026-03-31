package board

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

func TestVisibleTaskRange(t *testing.T) {
	tests := []struct {
		name            string
		taskCount       int
		viewportStart   int
		availableHeight int
		wantStart       int
		wantEnd         int
	}{
		{
			name:            "empty_tasks",
			taskCount:       0,
			viewportStart:   0,
			availableHeight: 20,
			wantStart:       0,
			wantEnd:         0,
		},
		{
			name:            "fits_all_tasks",
			taskCount:       3,
			viewportStart:   2,
			availableHeight: 24,
			wantStart:       0,
			wantEnd:         3,
		},
		{
			name:            "viewport_start_at_top",
			taskCount:       10,
			viewportStart:   0,
			availableHeight: 12,
			wantStart:       0,
			wantEnd:         2,
		},
		{
			name:            "viewport_start_near_bottom",
			taskCount:       10,
			viewportStart:   8,
			availableHeight: 12,
			wantStart:       8,
			wantEnd:         10,
		},
		{
			name:            "viewport_start_clamped_high",
			taskCount:       10,
			viewportStart:   99,
			availableHeight: 12,
			wantStart:       8,
			wantEnd:         10,
		},
		{
			name:            "viewport_start_clamped_low",
			taskCount:       10,
			viewportStart:   -5,
			availableHeight: 12,
			wantStart:       0,
			wantEnd:         2,
		},
	}
	linesPerCard := CardLineFootprint(styles.New(), 30)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := visibleTaskRange(tt.taskCount, tt.viewportStart, tt.availableHeight, linesPerCard)
			if start != tt.wantStart || end != tt.wantEnd {
				t.Fatalf("visibleTaskRange() = (%d,%d), want (%d,%d)", start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}
