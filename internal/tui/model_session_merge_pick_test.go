package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

// pickSourceTask returns the in-progress task from the default test model so
// the merge-pick tests have a stable source.
func pickSourceTask(m Model) *domain.Task {
	for i := range m.tasks {
		if m.tasks[i].Status == domain.StatusInProgress {
			t := m.tasks[i]
			return &t
		}
	}
	return nil
}

func TestOpenMergeTargetSelectionStartsBoardPickMode(t *testing.T) {
	m := newTestModel()
	source := pickSourceTask(m)
	if source == nil {
		t.Fatalf("expected an in-progress task in the test model")
	}

	cmd := m.openMergeTargetSelection(source)
	if cmd != nil {
		t.Fatalf("expected no command, got %T (board pick should not push an overlay)", cmd)
	}
	if m.mergePickMode == nil {
		t.Fatalf("expected mergePickMode to be active after openMergeTargetSelection")
	}
	if m.mergePickMode.sourceID != source.ID.String() {
		t.Fatalf("mergePickMode.sourceID = %q, want %q", m.mergePickMode.sourceID, source.ID.String())
	}
	if !m.mergePickMode.hasBase {
		t.Fatalf("expected base branch to be marked as eligible")
	}
	if !m.overlayStack.IsEmpty() {
		t.Fatalf("expected no overlay to be pushed; got %d", 1)
	}

	// Every other task should appear as a candidate (source excluded).
	for _, task := range m.tasks {
		if task.ID == source.ID {
			continue
		}
		if !m.mergePickMode.isCandidate(task.ID.String()) {
			t.Fatalf("expected %s to be an eligible candidate", task.ID)
		}
	}
}

func TestOpenMergeTargetSelectionExitsActionMode(t *testing.T) {
	m := newTestModel()
	source := pickSourceTask(m)
	if source == nil {
		t.Fatalf("expected an in-progress task in the test model")
	}
	m.editor.EnterAction()
	if !m.editor.IsAction() {
		t.Fatalf("setup: expected action mode")
	}

	_ = m.openMergeTargetSelection(source)
	if !m.editor.IsNormal() {
		t.Fatalf("expected editor to return to normal mode after entering merge pick mode")
	}
}

func TestHandleMergePickModeEscClears(t *testing.T) {
	m := newTestModel()
	source := pickSourceTask(m)
	_ = m.openMergeTargetSelection(source)
	if m.mergePickMode == nil {
		t.Fatalf("setup: expected merge pick mode to be active")
	}

	model, _ := m.handleMergePickMode(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mergePickMode != nil {
		t.Fatalf("expected merge pick mode to be cleared after Esc")
	}
}

func TestHandleMergePickModeBaseHotkeySelectsBase(t *testing.T) {
	m := newTestModel()
	source := pickSourceTask(m)
	_ = m.openMergeTargetSelection(source)

	model, cmd := m.handleMergePickMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}})
	m = model.(Model)
	if m.mergePickMode != nil {
		t.Fatalf("expected mergePickMode to be cleared after confirming base branch")
	}
	if cmd == nil {
		t.Fatalf("expected a command to be returned by confirming base branch")
	}
}

func TestHandleMergePickModeEnterOnCandidate(t *testing.T) {
	m := newTestModel()
	source := pickSourceTask(m)
	_ = m.openMergeTargetSelection(source)

	// Move the cursor to az-1 (an eligible candidate in the test model).
	m.nav.SelectTask("az-1", 0)

	model, cmd := m.handleMergePickMode(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if cmd == nil {
		t.Fatalf("expected a command after confirming a candidate target")
	}
	if m.mergePickMode != nil {
		t.Fatalf("expected mergePickMode to be cleared after confirming target")
	}
}

func TestHandleMergePickModeEnterOnNonCandidateShowsToast(t *testing.T) {
	m := newTestModel()
	source := pickSourceTask(m)
	_ = m.openMergeTargetSelection(source)

	// Cursor on the source task itself, which is never an eligible target.
	m.nav.SelectTask(source.ID.String(), 1)

	model, _ := m.handleMergePickMode(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mergePickMode == nil {
		t.Fatalf("expected mergePickMode to remain active after invalid Enter")
	}
	if len(m.toasts) == 0 {
		t.Fatalf("expected a toast to surface explaining the invalid selection")
	}
	if !strings.Contains(m.toasts[len(m.toasts)-1].Message, "not an eligible merge target") {
		t.Fatalf("toast message = %q, want eligibility hint", m.toasts[len(m.toasts)-1].Message)
	}
}

func TestMergePickCandidatesByTaskMatchesState(t *testing.T) {
	m := newTestModel()
	source := pickSourceTask(m)
	_ = m.openMergeTargetSelection(source)

	got := m.mergePickCandidatesByTask()
	if len(got) != len(m.mergePickMode.candidates) {
		t.Fatalf("candidates map size = %d, want %d", len(got), len(m.mergePickMode.candidates))
	}
	for id := range m.mergePickMode.candidates {
		if !got[id] {
			t.Fatalf("expected %s in candidate map", id)
		}
	}
}

func TestRenderMergePickToolbarOnlyWhenActive(t *testing.T) {
	m := newTestModel()
	if got := m.renderMergePickToolbar(); got != "" {
		t.Fatalf("expected empty toolbar before pick mode, got %q", got)
	}
	source := pickSourceTask(m)
	_ = m.openMergeTargetSelection(source)
	toolbar := m.renderMergePickToolbar()
	if toolbar == "" {
		t.Fatalf("expected toolbar after entering merge pick mode")
	}
	if !strings.Contains(toolbar, "Merge pick") {
		t.Fatalf("toolbar = %q, want label 'Merge pick'", toolbar)
	}
	if !strings.Contains(toolbar, source.ID.String()) {
		t.Fatalf("toolbar = %q, want source ID %s", toolbar, source.ID)
	}
}

// Sanity check: handleMergeTargetSelection should still work even when there
// is no overlay on the stack (the board picker calls it directly).
func TestHandleMergeTargetSelectionWithoutOverlay(t *testing.T) {
	m := newTestModel()
	if !m.overlayStack.IsEmpty() {
		t.Fatalf("setup: expected empty overlay stack")
	}
	model, _ := m.handleMergeTargetSelection(overlay.MergeTargetSelectedMsg{
		SourceID: "az-3",
		TargetID: "az-1",
	})
	if model == nil {
		t.Fatalf("expected non-nil model after merge target selection without overlay")
	}
}
