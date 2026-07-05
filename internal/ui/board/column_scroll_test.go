package board

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

func TestRenderColumn_DoesNotScrollImmediatelyOnNextTask(t *testing.T) {
	s := styles.New()
	tasks := []domain.Task{
		{ID: "az-1", Title: "Task One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-3", Title: "Task Three", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}

	// Height allows ~2 cards. Cursor on second task should stay in view without
	// snapping the viewport to the second card at the top.
	out := renderColumn("Open", tasks, 1, true, 0, map[string]bool{}, map[string]RuntimeSignals{}, nil, nil, false, nil, nil, 36, 16, s)

	if !strings.Contains(out, "Task One") {
		t.Fatalf("expected first task to remain visible when cursor moves to second task")
	}
	if !strings.Contains(out, "Task Two") {
		t.Fatalf("expected second task to be visible")
	}
}

func TestRenderColumn_ShowsTopAndBottomScrollIndicatorsConditionally(t *testing.T) {
	s := styles.New()
	tasks := []domain.Task{
		{ID: "az-1", Title: "Task One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-3", Title: "Task Three", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-4", Title: "Task Four", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-5", Title: "Task Five", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}

	topOut := renderColumn("Open", tasks, 0, true, 0, map[string]bool{}, map[string]RuntimeSignals{}, nil, nil, false, nil, nil, 36, 16, s)
	if strings.Contains(topOut, "more ^") {
		t.Fatalf("top indicator should not render at origin viewport")
	}
	if !strings.Contains(topOut, "more v") {
		t.Fatalf("bottom indicator should render when additional tasks exist below viewport")
	}

	midOut := renderColumn("Open", tasks, 2, true, 2, map[string]bool{}, map[string]RuntimeSignals{}, nil, nil, false, nil, nil, 36, 16, s)
	if !strings.Contains(midOut, "more ^") {
		t.Fatalf("top indicator should render when viewport is scrolled down")
	}
	if !strings.Contains(midOut, "more v") {
		t.Fatalf("bottom indicator should render when tasks remain below viewport")
	}
}

func TestRenderColumn_TinyViewportKeepsActiveCardInBounds(t *testing.T) {
	s := styles.New()
	tasks := []domain.Task{
		{ID: "az-1", Title: "Task One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-3", Title: "Task Three", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
	}

	width := 50
	linesPerCard := CardLineFootprint(s, CardContentWidth(width))
	height := ColumnHeaderLines + linesPerCard
	out := renderColumn("Open", tasks, 1, true, 1, map[string]bool{}, map[string]RuntimeSignals{}, nil, nil, false, nil, nil, width, height, s)

	if strings.Contains(out, "more ^") || strings.Contains(out, "more v") {
		t.Fatalf("tiny viewport should not render scroll indicators that push the card out of bounds:\n%s", out)
	}
	if !strings.Contains(out, "Task Two") {
		t.Fatalf("expected active task to remain visible in tiny viewport:\n%s", out)
	}
	if got := lipgloss.Height(out); got > height {
		t.Fatalf("column height = %d, want <= %d", got, height)
	}
}
