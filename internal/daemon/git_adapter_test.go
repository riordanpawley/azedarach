package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type recordingGitRunner struct {
	runFn func(args ...string) (string, error)
}

func cleanGitStatus() *git.GitStatus {
	return &git.GitStatus{
		Modified:   []string{},
		Added:      []string{},
		Deleted:    []string{},
		Untracked:  []string{},
		Staged:     []string{},
		HasChanges: false,
	}
}

func (r *recordingGitRunner) Run(_ context.Context, args ...string) (string, error) {
	if r.runFn == nil {
		return "", nil
	}
	return r.runFn(args...)
}

func newGitAdapterStore(t *testing.T, projectID, issueID, worktree string, status *git.GitStatus) *daemonstate.RuntimeStateStore {
	t.Helper()

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    "az/" + issueID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	if status != nil {
		rawStatus, err := json.Marshal(status)
		if err != nil {
			t.Fatalf("marshal status: %v", err)
		}
		if err := store.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Now().UTC()); err != nil {
			t.Fatalf("UpsertWorktreeGitStatus: %v", err)
		}
	}

	return store
}

func TestGitServiceAdapterMergeForcesStatusUpdatePublish(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, cleanGitStatus())

	runner := &recordingGitRunner{
		runFn: func(args ...string) (string, error) {
			if len(args) == 0 {
				return "", nil
			}
			switch {
			case args[0] == "merge":
				return "Already up to date.", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
				return "", nil
			default:
				t.Fatalf("unexpected git args: %v", args)
				return "", nil
			}
		},
	}

	updates := 0
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
		onStatusUpdate: func(_ context.Context, gotProjectID, gotIssueID, gotWorktree string, status *git.GitStatus) {
			updates++
			if gotProjectID != projectID {
				t.Fatalf("project id = %q, want %q", gotProjectID, projectID)
			}
			if gotIssueID != issueID {
				t.Fatalf("issue id = %q, want %q", gotIssueID, issueID)
			}
			if gotWorktree != worktree {
				t.Fatalf("worktree = %q, want %q", gotWorktree, worktree)
			}
			if status == nil {
				t.Fatal("expected git status to be forwarded")
			}
		},
	}

	result, err := adapter.Merge(ctx, projectID, worktree, "az/az-source")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if result == nil {
		t.Fatal("Merge result is nil")
	}
	if updates != 1 {
		t.Fatalf("status update publishes = %d, want 1", updates)
	}
}

func TestGitServiceAdapterMergePreflightUsesWorktreeAwareClient(t *testing.T) {
	t.Parallel()

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[1] == "/tmp/source" && args[2] == "status" && args[3] == "--porcelain":
			return " M source.txt", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == "/tmp/target" && args[2] == "status" && args[3] == "--porcelain":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == "/tmp/target" && args[2] == "merge-tree" && args[3] == "--write-tree" && args[4] == "main" && args[5] == "az/source":
			return "CONFLICT (content): Merge conflict in cmd/az/main.go", errors.New("merge-tree conflict")
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	adapter := &gitServiceAdapter{client: git.NewClient(runner, slog.Default())}
	result, err := adapter.MergePreflight(context.Background(), "default", daemonhandlers.GitMergePreflightRequest{
		SourceID:       "az-source",
		SourceWorktree: "/tmp/source",
		TargetID:       "main",
		TargetWorktree: "/tmp/target",
		TargetRef:      "main",
		SourceBranch:   "az/source",
	})
	if err != nil {
		t.Fatalf("MergePreflight: %v", err)
	}
	if result == nil {
		t.Fatal("MergePreflight result is nil")
	}
	if result.Clean {
		t.Fatal("expected unclean merge preflight result")
	}
	if !reflect.DeepEqual(result.SourceFiles, []string{"source.txt"}) {
		t.Fatalf("source files = %v, want [source.txt]", result.SourceFiles)
	}
	if !reflect.DeepEqual(result.ConflictFiles, []string{"cmd/az/main.go"}) {
		t.Fatalf("conflict files = %v, want [cmd/az/main.go]", result.ConflictFiles)
	}
}

func TestGitServiceAdapterDiscardChangesPublishesOnStatusChange(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, &git.GitStatus{HasChanges: true, Modified: []string{"dirty.go"}})

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 6 && args[0] == "-C" && args[1] == worktree && args[2] == "restore" && args[3] == "--staged" && args[4] == "--worktree" && args[5] == ".":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "clean" && args[3] == "-fd":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
			return "", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	updates := 0
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
		onStatusUpdate: func(_ context.Context, _, gotIssueID, gotWorktree string, _ *git.GitStatus) {
			updates++
			if gotIssueID != issueID || gotWorktree != worktree {
				t.Fatalf("status update = (%s, %s), want (%s, %s)", gotIssueID, gotWorktree, issueID, worktree)
			}
		},
	}
	adapter.storeRuntimeSignal(runtimeSignalCacheKey(projectID, issueID, worktree, "main", false, "origin"), daemonhandlers.GitRuntimeSignalsResult{IssueID: issueID, Worktree: worktree}, time.Now())

	result, err := adapter.DiscardChanges(ctx, projectID, worktree)
	if err != nil {
		t.Fatalf("DiscardChanges: %v", err)
	}
	if result == nil || result.Worktree != worktree {
		t.Fatalf("discard result = %+v, want worktree %q", result, worktree)
	}
	if updates != 1 {
		t.Fatalf("status update publishes = %d, want 1", updates)
	}
	if _, ok := adapter.cachedRuntimeSignal(runtimeSignalCacheKey(projectID, issueID, worktree, "main", false, "origin"), time.Now()); ok {
		t.Fatal("runtime signal cache should be invalidated")
	}
}

func TestGitServiceAdapterDiscardChangesSkipsPublishWhenStatusUnchanged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, cleanGitStatus())

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 6 && args[0] == "-C" && args[1] == worktree && args[2] == "restore" && args[3] == "--staged" && args[4] == "--worktree" && args[5] == ".":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "clean" && args[3] == "-fd":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
			return "", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	updates := 0
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
		onStatusUpdate:    func(_ context.Context, _, _, _ string, _ *git.GitStatus) { updates++ },
	}
	cacheKey := runtimeSignalCacheKey(projectID, issueID, worktree, "main", false, "origin")
	adapter.storeRuntimeSignal(cacheKey, daemonhandlers.GitRuntimeSignalsResult{IssueID: issueID, Worktree: worktree}, time.Now())

	result, err := adapter.DiscardChanges(ctx, projectID, worktree)
	if err != nil {
		t.Fatalf("DiscardChanges: %v", err)
	}
	if result == nil || result.Worktree != worktree {
		t.Fatalf("discard result = %+v, want worktree %q", result, worktree)
	}
	if updates != 0 {
		t.Fatalf("status update publishes = %d, want 0", updates)
	}
	if _, ok := adapter.cachedRuntimeSignal(cacheKey, time.Now()); ok {
		t.Fatal("runtime signal cache should be invalidated even when status is unchanged")
	}
}

func TestGitServiceAdapterCreateCheckpointPublishesOnStatusChange(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, &git.GitStatus{HasChanges: true, Modified: []string{"dirty.go"}})

	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
			statusCalls++
			if statusCalls == 1 {
				return " M dirty.go", nil
			}
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "add" && args[3] == "-A":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "commit" && args[3] == "-m" && args[4] == git.DefaultCheckpointMessage:
			return "[branch abc123] checkpoint", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	updates := 0
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
		onStatusUpdate:    func(_ context.Context, _, _, _ string, _ *git.GitStatus) { updates++ },
	}

	result, err := adapter.Checkpoint(ctx, projectID, daemonhandlers.GitCheckpointRequest{
		Worktree: worktree,
		Message:  "",
	})
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if result == nil || result.Worktree != worktree || result.Message != git.DefaultCheckpointMessage {
		t.Fatalf("checkpoint result = %+v", result)
	}
	if updates != 1 {
		t.Fatalf("status update publishes = %d, want 1", updates)
	}
}

func TestGitServiceAdapterCreateCheckpointReturnsNoChangesWithoutPublishing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	worktree := "/tmp/az-target"

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain" {
			return "", nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	updates := 0
	adapter := &gitServiceAdapter{
		client:         git.NewClient(runner, slog.Default()),
		onStatusUpdate: func(_ context.Context, _, _, _ string, _ *git.GitStatus) { updates++ },
	}

	_, err := adapter.Checkpoint(ctx, projectID, daemonhandlers.GitCheckpointRequest{
		Worktree: worktree,
		Message:  "",
	})
	if !errors.Is(err, git.ErrNoChangesToCommit) {
		t.Fatalf("CreateCheckpoint error = %v, want ErrNoChangesToCommit", err)
	}
	if updates != 0 {
		t.Fatalf("status update publishes = %d, want 0", updates)
	}
}

func TestGitServiceAdapterStatusCachedPathBumpsVisibleRefreshPriority(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	busyWorktree := "/tmp/az-busy"
	targetWorktree := "/tmp/az-target"
	laterWorktree := "/tmp/az-later"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	seed := func(issueID, worktree string) {
		t.Helper()
		if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
			ProjectID: projectID,
			IssueID:   issueID,
			Path:      worktree,
			Branch:    "az/" + issueID,
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed worktree %s: %v", issueID, err)
		}
		rawStatus, err := json.Marshal(cleanGitStatus())
		if err != nil {
			t.Fatalf("marshal seed status %s: %v", issueID, err)
		}
		if err := store.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Now().UTC()); err != nil {
			t.Fatalf("seed git status %s: %v", issueID, err)
		}
	}
	seed("az-busy", busyWorktree)
	seed("az-target", targetWorktree)
	seed("az-later", laterWorktree)

	busyStarted := make(chan struct{}, 1)
	releaseBusy := make(chan struct{})
	targetRan := make(chan struct{}, 1)
	laterRan := make(chan struct{}, 1)
	var (
		orderMu sync.Mutex
		order   []string
	)
	recordOrder := func(worktree string) {
		orderMu.Lock()
		defer orderMu.Unlock()
		order = append(order, worktree)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) < 4 || args[0] != "-C" || args[2] != "status" || args[3] != "--porcelain" {
			t.Fatalf("unexpected git args: %v", args)
		}
		worktree := args[1]
		switch worktree {
		case busyWorktree:
			busyStarted <- struct{}{}
			<-releaseBusy
			recordOrder("busy")
			return " M busy.go", nil
		case targetWorktree:
			recordOrder("target")
			targetRan <- struct{}{}
			return " M target.go", nil
		case laterWorktree:
			recordOrder("later")
			laterRan <- struct{}{}
			return " M later.go", nil
		default:
			t.Fatalf("unexpected worktree: %s", worktree)
			return "", nil
		}
	}}

	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "git_status_refresh_test",
		Workers: 1,
		Logger:  slog.Default(),
	})
	t.Cleanup(func() {
		_ = queue.Close()
	})

	adapter := &gitServiceAdapter{
		client:             git.NewClient(runner, slog.Default()),
		runtimeStateStore:  store,
		statusRefreshQueue: queue,
	}

	adapter.refreshGitStatusAsync(projectID, busyWorktree)
	select {
	case <-busyStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for busy refresh to start")
	}

	adapter.refreshGitStatusAsync(projectID, targetWorktree)
	adapter.refreshGitStatusAsync(projectID, laterWorktree)

	status, err := adapter.Status(ctx, projectID, targetWorktree)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status == nil {
		t.Fatal("expected cached status")
	}
	if status.HasChanges {
		t.Fatal("cached status should remain clean before queued refresh runs")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := queue.snapshot()
		if len(snapshot.Pending) == 2 &&
			snapshot.Pending[0] == gitStatusRefreshQueueKey(projectID, targetWorktree) &&
			snapshot.Pending[1] == gitStatusRefreshQueueKey(projectID, laterWorktree) &&
			snapshot.Counters.Deduped == 1 &&
			snapshot.Counters.Reprioritized == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := queue.snapshot()
	if len(snapshot.Pending) != 2 ||
		snapshot.Pending[0] != gitStatusRefreshQueueKey(projectID, targetWorktree) ||
		snapshot.Pending[1] != gitStatusRefreshQueueKey(projectID, laterWorktree) {
		t.Fatalf("pending git refresh order = %v", snapshot.Pending)
	}
	if snapshot.Counters.Deduped != 1 || snapshot.Counters.Reprioritized != 1 {
		t.Fatalf("queue counters = %+v", snapshot.Counters)
	}

	close(releaseBusy)

	select {
	case <-targetRan:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for target refresh to run")
	}
	select {
	case <-laterRan:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for later refresh to run")
	}

	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	wantOrder := []string{"busy", "target", "later"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("git refresh order = %v, want %v", gotOrder, wantOrder)
	}
}

func TestGitServiceAdapterQueueGitStatusRefreshDefersWhenBudgetExhausted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	firstWorktree := "/tmp/az-first"
	secondWorktree := "/tmp/az-second"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	seed := func(issueID, worktree string) {
		t.Helper()
		if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
			ProjectID: projectID,
			IssueID:   issueID,
			Path:      worktree,
			Branch:    "az/" + issueID,
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed worktree %s: %v", issueID, err)
		}
		rawStatus, err := json.Marshal(cleanGitStatus())
		if err != nil {
			t.Fatalf("marshal seed status %s: %v", issueID, err)
		}
		if err := store.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Now().UTC()); err != nil {
			t.Fatalf("seed git status %s: %v", issueID, err)
		}
	}
	seed("az-first", firstWorktree)
	seed("az-second", secondWorktree)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls sync.Map
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) < 4 || args[0] != "-C" || args[2] != "status" || args[3] != "--porcelain" {
			t.Fatalf("unexpected git args: %v", args)
		}
		worktree := args[1]
		calls.Store(worktree, true)
		if worktree == firstWorktree {
			started <- struct{}{}
			<-release
		}
		return "", nil
	}}

	now := time.Date(2026, time.April, 3, 12, 0, 0, 0, time.UTC)
	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "git_status_refresh_budget_test",
		Workers: 1,
		Logger:  slog.Default(),
	})
	t.Cleanup(func() {
		_ = queue.Close()
	})
	throttle := newReconcileThrottle(reconcileThrottleConfig{
		Name:                 "git_status_refresh_budget_test",
		Budget:               1,
		Cadence:              time.Hour,
		UnchangedBackoffBase: time.Hour,
		UnchangedBackoffMax:  time.Hour,
		FailureBackoffBase:   time.Hour,
		FailureBackoffMax:    time.Hour,
		Now:                  func() time.Time { return now },
	})

	adapter := &gitServiceAdapter{
		client:                git.NewClient(runner, slog.Default()),
		runtimeStateStore:     store,
		statusRefreshQueue:    queue,
		statusRefreshThrottle: throttle,
		logger:                slog.Default(),
	}

	first, err := adapter.queueGitStatusRefresh(projectID, firstWorktree, reconcilePriorityBackground, "background")
	if err != nil {
		t.Fatalf("queue first refresh: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first refresh to start")
	}

	second, err := adapter.queueGitStatusRefresh(projectID, secondWorktree, reconcilePriorityBackground, "background")
	if err != nil {
		t.Fatalf("queue second refresh: %v", err)
	}
	secondResult, err := second.Wait(ctx)
	if err != nil {
		t.Fatalf("wait second refresh: %v", err)
	}
	if !secondResult.Deferred || secondResult.Skipped {
		t.Fatalf("second refresh result = %+v, want deferred", secondResult)
	}

	close(release)

	firstResult, err := first.Wait(ctx)
	if err != nil {
		t.Fatalf("wait first refresh: %v", err)
	}
	if firstResult.Err != nil {
		t.Fatalf("first refresh error = %v, want nil", firstResult.Err)
	}
	if _, ok := calls.Load(secondWorktree); ok {
		t.Fatal("second worktree should not have been probed while budget was exhausted")
	}
	counters := throttle.snapshotCounters()
	if counters.Processed != 1 || counters.Deferred != 1 || counters.Skipped != 0 {
		t.Fatalf("throttle counters = %+v, want processed=1 deferred=1 skipped=0", counters)
	}
}

func TestGitServiceAdapterQueueGitStatusRefreshBacksOffUnchangedTarget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, cleanGitStatus())

	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain" {
			statusCalls++
			return "", nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	now := time.Date(2026, time.April, 3, 13, 0, 0, 0, time.UTC)
	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "git_status_refresh_backoff_test",
		Workers: 1,
		Logger:  slog.Default(),
	})
	t.Cleanup(func() {
		_ = queue.Close()
	})
	throttle := newReconcileThrottle(reconcileThrottleConfig{
		Name:                 "git_status_refresh_backoff_test",
		Budget:               4,
		Cadence:              time.Second,
		UnchangedBackoffBase: time.Hour,
		UnchangedBackoffMax:  time.Hour,
		FailureBackoffBase:   time.Hour,
		FailureBackoffMax:    time.Hour,
		Now:                  func() time.Time { return now },
	})

	adapter := &gitServiceAdapter{
		client:                git.NewClient(runner, slog.Default()),
		runtimeStateStore:     store,
		statusRefreshQueue:    queue,
		statusRefreshThrottle: throttle,
		logger:                slog.Default(),
	}

	first, err := adapter.queueGitStatusRefresh(projectID, worktree, reconcilePriorityBackground, "background")
	if err != nil {
		t.Fatalf("queue first refresh: %v", err)
	}
	firstResult, err := first.Wait(ctx)
	if err != nil {
		t.Fatalf("wait first refresh: %v", err)
	}
	if firstResult.Err != nil {
		t.Fatalf("first refresh error = %v, want nil", firstResult.Err)
	}

	second, err := adapter.queueGitStatusRefresh(projectID, worktree, reconcilePriorityBackground, "background")
	if err != nil {
		t.Fatalf("queue second refresh: %v", err)
	}
	secondResult, err := second.Wait(ctx)
	if err != nil {
		t.Fatalf("wait second refresh: %v", err)
	}
	if !secondResult.Skipped || secondResult.Deferred {
		t.Fatalf("second refresh result = %+v, want skipped", secondResult)
	}
	if statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1", statusCalls)
	}
	counters := throttle.snapshotCounters()
	if counters.Processed != 1 || counters.Skipped != 1 || counters.Deferred != 0 {
		t.Fatalf("throttle counters = %+v, want processed=1 skipped=1 deferred=0", counters)
	}
}
