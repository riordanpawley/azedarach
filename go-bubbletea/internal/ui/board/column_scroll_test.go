package board

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

func TestRenderColumn_KeepsCursorVisibleWithVariableCardHeights(t *testing.T) {
	s := styles.New()
	now := time.Now().Add(-10 * time.Minute)

	tasks := make([]domain.Task, 0, 20)
	for i := 0; i < 20; i++ {
		task := domain.Task{
			ID:       "az-scroll-" + string(rune('a'+(i%26))),
			Title:    "Scroll Task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		}
		if i == 14 {
			task.Title = "CURSOR-ME"
		}
		if i%2 == 0 {
			task.Type = domain.TypeEpic // adds progress line
		}
		if i%3 == 0 {
			task.Session = &domain.Session{State: domain.SessionBusy, StartedAt: &now}
		}
		tasks = append(tasks, task)
	}

	// Deep cursor to force scrolling
	view := renderColumn(
		"Open",
		tasks,
		14,
		true,
		map[string]bool{},
		nil,
		false,
		38,
		24,
		s,
	)

	if !strings.Contains(view, "CURSOR-ME") {
		t.Fatalf("expected cursor task title to be visible in rendered viewport\n%s", view)
	}
}

func TestRenderColumn_UsesProvidedHeight(t *testing.T) {
	s := styles.New()
	tasks := make([]domain.Task, 0, 6)
	for i := 0; i < 6; i++ {
		tasks = append(tasks, domain.Task{
			ID:       "az-h-" + string(rune('a'+i)),
			Title:    "Height Fill",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}

	height := 36
	view := renderColumn("Open", tasks, 0, true, map[string]bool{}, nil, false, 38, height, s)
	lines := strings.Split(view, "\n")

	if got := len(lines); got < height {
		t.Fatalf("expected at least %d lines to fill terminal height, got %d", height, got)
	}
}

func TestRenderColumn_DoesNotDoubleSpaceCards(t *testing.T) {
	s := styles.New()
	tasks := make([]domain.Task, 0, 12)
	for i := 0; i < 12; i++ {
		tasks = append(tasks, domain.Task{
			ID:       fmt.Sprintf("az-dense-%02d", i),
			Title:    fmt.Sprintf("Dense Card %02d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}

	// height=62 => available body height is 60.
	// Simple cards with style margin should all fit when spacing is correct.
	view := renderColumn("Open", tasks, 0, true, map[string]bool{}, nil, false, 40, 62, s)

	for i := 0; i < 12; i++ {
		title := fmt.Sprintf("Dense Card %02d", i)
		if !strings.Contains(view, title) {
			t.Fatalf("expected %q to be visible; renderer is likely over-spacing cards", title)
		}
	}
}
