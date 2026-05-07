package board

import (
	"fmt"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

func TestVisibleTaskRange_ClampsTinyViewportAndInvalidFootprint(t *testing.T) {
	tests := []struct {
		name            string
		taskCount       int
		viewportStart   int
		availableHeight int
		linesPerCard    int
		wantStart       int
		wantEnd         int
	}{
		{
			name:            "invalid_footprint_still_shows_one_card",
			taskCount:       7,
			viewportStart:   99,
			availableHeight: 0,
			linesPerCard:    0,
			wantStart:       6,
			wantEnd:         7,
		},
		{
			name:            "negative_viewport_clamps_to_first_card",
			taskCount:       7,
			viewportStart:   -3,
			availableHeight: 0,
			linesPerCard:    0,
			wantStart:       0,
			wantEnd:         1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd := visibleTaskRange(tt.taskCount, tt.viewportStart, tt.availableHeight, tt.linesPerCard)
			if gotStart != tt.wantStart || gotEnd != tt.wantEnd {
				t.Fatalf("visibleTaskRange() = (%d,%d), want (%d,%d)", gotStart, gotEnd, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestCardLineFootprint_ClampsWidthBelowMinimum(t *testing.T) {
	s := styles.New()

	gotZero := CardLineFootprint(s, 0)
	gotOne := CardLineFootprint(s, 1)

	if gotZero < 1 {
		t.Fatalf("CardLineFootprint(..., 0) = %d, want at least 1", gotZero)
	}
	if gotZero != gotOne {
		t.Fatalf("CardLineFootprint(..., 0) = %d, want same footprint as width 1 (%d)", gotZero, gotOne)
	}
}

func TestRender_UsesViewportOnlyOnActiveColumn(t *testing.T) {
	s := styles.New()

	makeTasks := func(prefix string, count int) []domain.Task {
		tasks := make([]domain.Task, 0, count)
		for i := 1; i <= count; i++ {
			tasks = append(tasks, domain.Task{
				ID:       naming.IssueID(fmt.Sprintf("%s-%02d", prefix, i)),
				Title:    fmt.Sprintf("%s-task-%02d", prefix, i),
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			})
		}
		return tasks
	}

	columns := []Column{
		{Title: "Open", Tasks: makeTasks("open", 6)},
		{Title: "Doing", Tasks: makeTasks("doing", 6)},
	}

	width := 80
	height := 12
	columnWidth := width / len(columns)
	linesPerCard := CardLineFootprint(s, CardContentWidth(columnWidth))
	availableHeight := ColumnBodyHeight(height)

	inactiveStart, inactiveEnd := visibleTaskRange(len(columns[0].Tasks), 0, availableHeight, linesPerCard)
	activeStart, activeEnd := visibleTaskRange(len(columns[1].Tasks), 4, availableHeight, linesPerCard)

	out := normalizeBoardOutput(Render(columns, Cursor{Column: 1, Task: 4}, map[string]bool{}, map[string]RuntimeSignals{}, BuildChildProgress(columnsToTasks(columns)), nil, false, nil, 4, s, width, height))

	for i, task := range columns[0].Tasks {
		wantVisible := i >= inactiveStart && i < inactiveEnd
		contains := strings.Contains(out, task.Title)
		if wantVisible && !contains {
			t.Fatalf("inactive column should show %q in viewport [%d,%d), output:\n%s", task.Title, inactiveStart, inactiveEnd, out)
		}
		if !wantVisible && contains {
			t.Fatalf("inactive column should not show off-screen task %q, output:\n%s", task.Title, out)
		}
	}

	for i, task := range columns[1].Tasks {
		wantVisible := i >= activeStart && i < activeEnd
		contains := strings.Contains(out, task.Title)
		if wantVisible && !contains {
			t.Fatalf("active column should show %q in viewport [%d,%d), output:\n%s", task.Title, activeStart, activeEnd, out)
		}
		if !wantVisible && contains {
			t.Fatalf("active column should not show off-screen task %q, output:\n%s", task.Title, out)
		}
	}
}
