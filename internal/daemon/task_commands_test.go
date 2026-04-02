package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

func TestBuildTaskSnapshotExportBodyIsDeterministic(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC)
	}
	wantCapturedAt := time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC).UnixMilli()

	parentID := "parent-1"
	body := buildTaskSnapshotExportBody(
		"proj",
		17,
		[]domain.Task{
			{
				ID:           "b-task",
				Title:        "Bravo",
				Status:       domain.StatusBlocked,
				Priority:     domain.P2,
				Type:         domain.TypeTask,
				UpdatedAt:    time.Now(),
				Dependencies: []domain.Dependency{{ID: "dep-1", Type: domain.DependencyBlocks}},
			},
			{
				ID:           "a-task",
				Title:        "Alpha",
				Status:       domain.StatusInProgress,
				Priority:     domain.P1,
				Type:         domain.TypeBug,
				ParentID:     &parentID,
				Dependencies: []domain.Dependency{{ID: "dep-2", Type: domain.DependencyRelatedTo}, {ID: "dep-3", Type: domain.DependencyBlocks}},
			},
		},
		[]string{"b-task", "a-task"},
		"/tmp/proj",
	)

	if got, want := body.SchemaVersion, uint16(1); got != want {
		t.Fatalf("SchemaVersion = %d, want %d", got, want)
	}
	if got, want := body.SnapshotRevision, uint64(17); got != want {
		t.Fatalf("SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := body.CapturedAtMs, wantCapturedAt; got != want {
		t.Fatalf("CapturedAtMs = %d, want %d", got, want)
	}
	if got, want := body.ProjectID, "proj"; got != want {
		t.Fatalf("ProjectID = %q, want %q", got, want)
	}
	if got, want := body.TaskCount, 2; got != want {
		t.Fatalf("TaskCount = %d, want %d", got, want)
	}
	if got, want := body.SessionCount, 2; got != want {
		t.Fatalf("SessionCount = %d, want %d", got, want)
	}

	if got, want := len(body.Tasks), 2; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].ID, "a-task"; got != want {
		t.Fatalf("Tasks[0].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[0].SessionAttached, true; got != want {
		t.Fatalf("Tasks[0].SessionAttached = %v, want %v", got, want)
	}
	if got, want := body.Tasks[0].DependencyCount, 2; got != want {
		t.Fatalf("Tasks[0].DependencyCount = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].Critical, false; got != want {
		t.Fatalf("Tasks[0].Critical = %v, want %v", got, want)
	}
	if body.Tasks[0].ParentID == nil || *body.Tasks[0].ParentID != parentID {
		t.Fatalf("Tasks[0].ParentID = %+v, want %q", body.Tasks[0].ParentID, parentID)
	}
	if got, want := body.Tasks[1].ID, "b-task"; got != want {
		t.Fatalf("Tasks[1].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[1].Critical, true; got != want {
		t.Fatalf("Tasks[1].Critical = %v, want %v", got, want)
	}

	if got, want := len(body.Sessions), 2; got != want {
		t.Fatalf("len(Sessions) = %d, want %d", got, want)
	}
	if got, want := body.Sessions[0].Name, "a-task"; got != want {
		t.Fatalf("Sessions[0].Name = %q, want %q", got, want)
	}
	if got, want := body.Sessions[1].Name, "b-task"; got != want {
		t.Fatalf("Sessions[1].Name = %q, want %q", got, want)
	}

	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal snapshot body: %v", err)
	}
}

func TestBuildTaskSnapshotExportBody_ProjectScopedSessionPrefixMatchesIssue(t *testing.T) {
	body := buildTaskSnapshotExportBody(
		"Chefy",
		1,
		[]domain.Task{
			{
				ID:       "em",
				Title:    "Issue with tmux session",
				Status:   domain.StatusInProgress,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		},
		[]string{"ch-em"},
		"Chefy",
	)

	if got, want := len(body.Tasks), 1; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if !body.Tasks[0].SessionAttached {
		t.Fatalf("SessionAttached = false, want true")
	}
}

func TestEnrichTasksWithRuntimeProjectionCache(t *testing.T) {
	ctx := context.Background()
	store := daemonstate.NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	const (
		projectID = "proj-runtime-first-render"
		issueID   = "az-123"
		worktree  = "/tmp/repo-az-123"
		branch    = "riordan/az-123/task"
	)

	if err := store.UpsertWorktree(ctx, daemonstate.WorktreeProjection{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	rawStatus, err := json.Marshal(git.GitStatus{
		Added:      []string{"new.go"},
		Modified:   []string{"changed.go"},
		Staged:     []string{"staged.go"},
		Deleted:    []string{"removed.go"},
		HasChanges: true,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	if err := store.UpsertWorktreeGitStatus(ctx, projectID, issueID, rawStatus, time.Date(2026, time.April, 2, 10, 1, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	d := &Daemon{
		cfg:             Config{Logger: slog.Default()},
		projectionStore: store,
	}

	now := time.Date(2026, time.April, 2, 10, 2, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID: issueID,
			Session: &domain.Session{
				IssueID:   issueID,
				State:     domain.SessionBusy,
				StartedAt: &now,
			},
		},
		{ID: "az-999"},
	}

	got := d.enrichTasksWithRuntimeProjectionCache(ctx, projectID, tasks)
	if len(got) != 2 {
		t.Fatalf("task count = %d, want 2", len(got))
	}
	if !got[0].HasWorktree {
		t.Fatal("task[0].HasWorktree = false, want true")
	}
	if got[0].Session == nil || got[0].Session.Worktree != worktree {
		t.Fatalf("task[0].Session = %+v, want worktree %s", got[0].Session, worktree)
	}
	if !got[0].HasUncommittedChanges {
		t.Fatal("task[0].HasUncommittedChanges = false, want true")
	}
	if got[0].GitAdditions != 3 || got[0].GitDeletions != 1 {
		t.Fatalf("task[0] git counters = +%d/-%d, want +3/-1", got[0].GitAdditions, got[0].GitDeletions)
	}
	if got[1].HasWorktree || got[1].HasUncommittedChanges || got[1].GitAdditions != 0 || got[1].GitDeletions != 0 {
		t.Fatalf("task[1] should remain unchanged, got %+v", got[1])
	}
}
