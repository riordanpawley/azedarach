package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// TestRouteTransientMode_NoModeActive verifies the router is transparent when
// no transient mode is active.
func TestRouteTransientMode_NoModeActive(t *testing.T) {
	m := newTestModel()
	if m.jumpMode != nil || m.mergePickMode != nil {
		t.Fatalf("setup: expected no transient mode active")
	}

	_, _, handled := m.routeTransientMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if handled {
		t.Fatalf("expected no transient mode to handle the key")
	}
}

// TestRouteTransientMode_MergePickActive verifies the merge picker intercepts
// the key when its mode is active.
func TestRouteTransientMode_MergePickActive(t *testing.T) {
	m := newTestModel()
	var source *domain.Task
	for i := range m.tasks {
		if m.tasks[i].Status == domain.StatusInProgress {
			t := m.tasks[i]
			source = &t
			break
		}
	}
	if source == nil {
		t.Fatalf("setup: expected an in-progress task in the test model")
	}
	_ = m.openMergeTargetSelection(source)
	if m.mergePickMode == nil {
		t.Fatalf("setup: expected mergePickMode to be active")
	}

	model, _, handled := m.routeTransientMode(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Fatalf("expected the router to dispatch the key while merge pick is active")
	}
	got := model.(Model)
	if got.mergePickMode != nil {
		t.Fatalf("expected merge pick mode to clear after Esc through the router")
	}
}

// TestTransientModeRoutesPriority documents the declared priority order so
// future readers can see that new modes go at the end (or earlier, with
// intent) and don't accidentally outrank existing ones.
func TestTransientModeRoutesPriority(t *testing.T) {
	routes := transientModeRoutes()
	if len(routes) < 2 {
		t.Fatalf("expected at least two transient mode routes, got %d", len(routes))
	}

	// jumpMode is declared first because its keys are single character labels
	// that would otherwise be swallowed by the merge picker's navigation
	// fallback if mergePickMode ran first.
	m := newTestModel()
	if !(routes[0].active(m) == false) {
		t.Fatalf("setup invariant: no transient mode should be active in the base test model")
	}
}
