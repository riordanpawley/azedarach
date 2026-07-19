package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestObserveGitProjectSchedulesOnlyLiveEligibleWorktrees(t *testing.T) {
	ctx := context.Background()
	const projectID = "portable-rust-consumer"
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, "issues.db")
	client := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	activeID, err := client.Create(ctx, issues.CreateTaskParams{Title: "active", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	stoppedID, err := client.Create(ctx, issues.CreateTaskParams{Title: "stopped", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	activePath := t.TempDir()
	stoppedPath := t.TempDir()
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: activeID, Path: activePath, Branch: "worker/active", UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: stoppedID, Path: stoppedPath, Branch: "worker/stopped", UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID: "active-session", IssueID: activeID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID: "stopped-session", IssueID: stoppedID, State: daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return "", nil
	}}
	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{Name: "git_observer_test", Workers: 1, Logger: slog.Default()})
	t.Cleanup(func() { _ = queue.Close() })
	adapter := &gitServiceAdapter{
		client: git.NewClient(runner, slog.Default()), statusRefreshQueue: queue, logger: slog.Default(), baseBranch: "main",
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore { return store },
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, gitStatusAdapter: adapter,
		issueClientsByProject:  map[string]*issues.Client{projectID: client},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
	}
	scheduled, err := d.observeGitProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if scheduled != 1 {
		t.Fatalf("scheduled = %d, want only active worktree", scheduled)
	}
	<-entered
	close(release)
	result, err := queue.Enqueue(reconcileQueueRequest[*git.GitStatus]{Key: "barrier", Priority: reconcilePriorityBackground, Work: func(context.Context) (*git.GitStatus, error) { return &git.GitStatus{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := result.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if counters := queue.snapshotCounters(); counters.Enqueued != 2 {
		t.Fatalf("queue counters = %+v, want one observation plus barrier", counters)
	}
}

func TestRotateGitObservationRowsAdvancesAcrossBoundedBatches(t *testing.T) {
	const (
		projectID = "portable-python-consumer"
		rowCount  = 70
		limit     = 64
	)
	rows := make([]daemonstate.WorktreeState, 0, rowCount)
	for index := 0; index < rowCount; index++ {
		rows = append(rows, daemonstate.WorktreeState{IssueID: fmt.Sprintf("issue-%02d", index)})
	}
	d := &Daemon{}

	first := d.rotateGitObservationRows(projectID, rows, limit)
	second := d.rotateGitObservationRows(projectID, rows, limit)

	if len(first) != limit || first[0].IssueID != "issue-00" || first[limit-1].IssueID != "issue-63" {
		t.Fatalf("first batch = %d rows [%s..%s]", len(first), first[0].IssueID, first[len(first)-1].IssueID)
	}
	if len(second) != limit || second[0].IssueID != "issue-64" || second[5].IssueID != "issue-69" || second[6].IssueID != "issue-00" {
		t.Fatalf("second batch did not wrap fairly: %d rows [%s..%s]", len(second), second[0].IssueID, second[len(second)-1].IssueID)
	}
}
