package linearsync

import (
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestReconcileHydratedTasks_UsesHydratedRuntimeProjectionForMatchingTasks(t *testing.T) {
	startedAt := time.Date(2026, 4, 2, 3, 15, 0, 0, time.UTC)
	current := []domain.Task{
		{
			ID:                    "az-1",
			HasWorktree:           true,
			GitAheadCount:         1,
			GitBehindCount:        3,
			HasUncommittedChanges: true,
			GitAdditions:          8,
			GitDeletions:          2,
			Session: &domain.Session{
				IssueID:   "az-1",
				State:     domain.SessionBusy,
				StartedAt: &startedAt,
			},
		},
		{
			ID:             "az-stale",
			HasTmuxSession: false,
		},
	}
	hydrated := []domain.Task{
		{
			ID:                    "az-1",
			Title:                 "Refreshed",
			Status:                domain.StatusInProgress,
			HasWorktree:           true,
			GitAheadCount:         0,
			GitBehindCount:        1,
			HasUncommittedChanges: false,
			GitAdditions:          0,
			GitDeletions:          0,
			Session: &domain.Session{
				IssueID:   "az-1",
				State:     domain.SessionPaused,
				StartedAt: &startedAt,
			},
			HasTmuxSession: true,
		},
		{ID: "az-2", Title: "Fresh", Status: domain.StatusOpen},
	}

	got := ReconcileHydratedTasks(current, hydrated)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].ID != "az-1" || got[0].Title != "Refreshed" || got[0].Status != domain.StatusInProgress {
		t.Fatalf("merged task = %+v", got[0])
	}
	if got[0].Session == nil || got[0].Session.State != domain.SessionPaused {
		t.Fatalf("session projection = %+v, want hydrated paused session", got[0].Session)
	}
	if !got[0].HasTmuxSession || !got[0].HasWorktree || got[0].GitAheadCount != 0 || got[0].GitBehindCount != 1 || got[0].HasUncommittedChanges || got[0].GitAdditions != 0 || got[0].GitDeletions != 0 {
		t.Fatalf("runtime fields = %+v, want hydrated values", got[0])
	}
	if got[1].ID != "az-2" || got[1].HasTmuxSession || got[1].HasWorktree || got[1].GitBehindCount != 0 {
		t.Fatalf("unexpected overlay leakage into fresh task: %+v", got[1])
	}
}

func TestReconcileHydratedTasks_UsesHydratedDetailsWithoutPreservingLoadedDetails(t *testing.T) {
	estimate := 3
	current := []domain.Task{
		{
			ID:          "az-1",
			Title:       "Current full detail",
			Description: "Loaded description",
			Notes:       "Loaded notes",
			Design:      "Loaded design",
			Acceptance:  "Loaded acceptance",
			Estimate:    &estimate,
			Status:      domain.StatusInProgress,
			Type:        domain.TypeTask,
		},
	}
	hydrated := []domain.Task{
		{
			ID:          "az-1",
			Title:       "Summary title",
			Description: "Daemon detail",
			Design:      "Daemon design",
			Notes:       "Daemon notes",
			Acceptance:  "Daemon AC",
			Status:      domain.StatusInReview,
			Type:        domain.TypeTask,
			Priority:    domain.P1,
		},
	}

	got := ReconcileHydratedTasks(current, hydrated)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	task := got[0]
	if task.Title != "Summary title" || task.Status != domain.StatusInReview || task.Priority != domain.P1 {
		t.Fatalf("summary fields = %+v, want hydrated values", task)
	}
	if task.Description != "Daemon detail" || task.Notes != "Daemon notes" || task.Design != "Daemon design" || task.Acceptance != "Daemon AC" {
		t.Fatalf("detail fields = %+v, want hydrated details", task)
	}
	if task.Estimate != nil {
		t.Fatalf("estimate = %+v, want hydrated estimate", task.Estimate)
	}
}

func TestReconcileHydratedTasks_DropsStaleLocalTasks(t *testing.T) {
	current := []domain.Task{
		{ID: "az-local", HasTmuxSession: true, Session: &domain.Session{IssueID: "az-local", State: domain.SessionBusy}},
	}

	got := ReconcileHydratedTasks(current, nil)

	if got != nil {
		t.Fatalf("got = %+v, want nil", got)
	}
}
