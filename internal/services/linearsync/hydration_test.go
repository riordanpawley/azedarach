package linearsync

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestReconcileHydratedTasks_PreservesLocalRuntimeOverlayForMatchingTasks(t *testing.T) {
	current := []domain.Task{
		{
			ID:                    "az-1",
			HasTmuxSession:        true,
			HasWorktree:           true,
			GitAheadCount:         1,
			GitBehindCount:        3,
			HasUncommittedChanges: true,
			GitAdditions:          8,
			GitDeletions:          2,
		},
		{
			ID:             "az-stale",
			HasTmuxSession: true,
		},
	}
	hydrated := []domain.Task{
		{ID: "az-1", Title: "Refreshed", Status: domain.StatusInProgress},
		{ID: "az-2", Title: "Fresh", Status: domain.StatusOpen},
	}

	got := ReconcileHydratedTasks(current, hydrated)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].ID != "az-1" || got[0].Title != "Refreshed" || got[0].Status != domain.StatusInProgress {
		t.Fatalf("merged task = %+v", got[0])
	}
	if !got[0].HasTmuxSession || !got[0].HasWorktree || got[0].GitAheadCount != 1 || got[0].GitBehindCount != 3 || !got[0].HasUncommittedChanges || got[0].GitAdditions != 8 || got[0].GitDeletions != 2 {
		t.Fatalf("overlay fields were not preserved: %+v", got[0])
	}
	if got[1].ID != "az-2" || got[1].HasTmuxSession || got[1].HasWorktree || got[1].GitBehindCount != 0 {
		t.Fatalf("unexpected overlay leakage into fresh task: %+v", got[1])
	}
}

func TestReconcileHydratedTasks_DropsStaleLocalTasks(t *testing.T) {
	current := []domain.Task{
		{ID: "az-local", HasTmuxSession: true},
	}

	got := ReconcileHydratedTasks(current, nil)

	if got != nil {
		t.Fatalf("got = %+v, want nil", got)
	}
}
