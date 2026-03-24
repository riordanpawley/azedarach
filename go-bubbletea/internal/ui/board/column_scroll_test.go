package board

import (
	"strings"
	"testing"

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
	out := renderColumn("Open", tasks, 1, true, map[string]bool{}, nil, false, 36, 16, s)

	if !strings.Contains(out, "Task One") {
		t.Fatalf("expected first task to remain visible when cursor moves to second task")
	}
	if !strings.Contains(out, "Task Two") {
		t.Fatalf("expected second task to be visible")
	}
}

