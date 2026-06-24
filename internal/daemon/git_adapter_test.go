package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
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

	dir, err := os.MkdirTemp("", "azedarach-git-adapter-*")
	if err != nil {
		t.Fatalf("create runtime state temp dir: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(dir, "projections.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
		_ = os.RemoveAll(dir)
	})

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
	worktree := t.TempDir()
	store := newGitAdapterStore(t, projectID, issueID, worktree, cleanGitStatus())

	var scratchWorktree string
	runner := &recordingGitRunner{
		runFn: func(args ...string) (string, error) {
			if len(args) == 0 {
				return "", nil
			}
			switch {
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "rev-parse" && args[3] == "--git-common-dir":
				return filepath.Join(worktree, ".git"), nil
			case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
				return "target-sha", nil
			case len(args) >= 7 && args[0] == "-C" && args[1] == worktree && args[2] == "worktree" && args[3] == "add":
				scratchWorktree = args[5]
				return "", nil
			case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
				return "Already up to date.", nil
			case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
				return "target-sha", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
				return "", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "status" && args[3] == "--porcelain":
				return "", nil
			case len(args) >= 6 && args[0] == "-C" && args[1] == worktree && args[2] == "worktree" && args[3] == "remove":
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

func TestGitServiceAdapterRefreshWriteThroughUsesRuntimeStatusWhenBaseBranchConfigured(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, cleanGitStatus())

	runner := &recordingGitRunner{
		runFn: func(args ...string) (string, error) {
			if len(args) < 3 || args[0] != "-C" || args[1] != worktree {
				t.Fatalf("unexpected git args: %v", args)
			}
			switch args[2] {
			case "status":
				if len(args) >= 4 && args[3] == "--porcelain" {
					return "", nil
				}
			case "symbolic-ref":
				// Keep candidate list deterministic in tests.
				return "", errors.New("origin HEAD unavailable")
			case "merge-base":
				return "abc123", nil
			case "diff":
				if len(args) >= 4 && args[3] == "--shortstat" {
					return " 3 files changed, 7 insertions(+), 3 deletions(-)", nil
				}
			case "rev-list":
				if len(args) >= 5 && args[3] == "--count" {
					switch args[4] {
					case "HEAD..main":
						return "1", nil
					case "main..HEAD":
						return "2", nil
					}
				}
			}
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		},
	}

	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
		baseBranch:        "main",
	}

	status, err := adapter.refreshGitStatusWriteThroughResult(ctx, projectID, worktree, true, false)
	if err != nil {
		t.Fatalf("refreshGitStatusWriteThroughResult: %v", err)
	}
	if status == nil {
		t.Fatal("expected status")
	}
	if status.GitAdditions != 7 || status.GitDeletions != 3 {
		t.Fatalf("diff totals = %d/%d, want 7/3", status.GitAdditions, status.GitDeletions)
	}
	if status.GitAheadCount != 2 || status.GitBehindCount != 1 {
		t.Fatalf("ahead/behind = %d/%d, want 2/1", status.GitAheadCount, status.GitBehindCount)
	}

	persisted, err := adapter.Status(ctx, projectID, worktree)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if persisted == nil {
		t.Fatal("expected persisted status")
	}
	if persisted.GitAdditions != 7 || persisted.GitDeletions != 3 {
		t.Fatalf("persisted diff totals = %d/%d, want 7/3", persisted.GitAdditions, persisted.GitDeletions)
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

func TestGitServiceAdapterMergePreflightCanIgnoreSourceDirty(t *testing.T) {
	t.Parallel()

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[1] == "/tmp/source" && args[2] == "status" && args[3] == "--porcelain":
			return " M source.txt", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == "/tmp/target" && args[2] == "status" && args[3] == "--porcelain":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == "/tmp/target" && args[2] == "merge-tree" && args[3] == "--write-tree" && args[4] == "main" && args[5] == "az/source":
			return "abc123", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	adapter := &gitServiceAdapter{client: git.NewClient(runner, slog.Default())}
	result, err := adapter.MergePreflight(context.Background(), "default", daemonhandlers.GitMergePreflightRequest{
		SourceID:          "az-source",
		SourceWorktree:    "/tmp/source",
		TargetID:          "main",
		TargetWorktree:    "/tmp/target",
		TargetRef:         "main",
		SourceBranch:      "az/source",
		IgnoreSourceDirty: true,
	})
	if err != nil {
		t.Fatalf("MergePreflight: %v", err)
	}
	if result == nil {
		t.Fatal("MergePreflight result is nil")
	}
	if !result.Clean {
		t.Fatalf("preflight clean = false; reasons=%v sourceFiles=%v", result.Reasons, result.SourceFiles)
	}
	if len(result.SourceFiles) != 0 {
		t.Fatalf("source files = %v, want none when source dirty is ignored", result.SourceFiles)
	}
}

func TestGitServiceAdapterMergePreflightDoesNotBlockUntrackedOnlyStatus(t *testing.T) {
	t.Parallel()

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[1] == "/tmp/source" && args[2] == "status" && args[3] == "--porcelain":
			return "?? scratch/", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == "/tmp/target" && args[2] == "status" && args[3] == "--porcelain":
			return "?? .azedarach/images/\n?? docs/", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == "/tmp/target" && args[2] == "merge-tree" && args[3] == "--write-tree" && args[4] == "main" && args[5] == "az/source":
			return "abc123", nil
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
	if !result.Clean {
		t.Fatalf("preflight clean = false; reasons=%v sourceFiles=%v targetFiles=%v", result.Reasons, result.SourceFiles, result.TargetFiles)
	}
	if len(result.SourceFiles) != 0 || len(result.TargetFiles) != 0 {
		t.Fatalf("dirty files = source %v target %v, want none", result.SourceFiles, result.TargetFiles)
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

func TestGitServiceAdapterQueueGitStatusRefreshDefersBackgroundDuringHeavySessionStart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, cleanGitStatus())

	var statusCalls atomic.Int32
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain" {
			statusCalls.Add(1)
			return "", nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}
	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "git_status_refresh_heavy_start_test",
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
		logger:             slog.Default(),
		heavySessionStartActive: func(ctx context.Context, _ string) bool {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("heavy session-start check context missing deadline")
			}
			return true
		},
	}

	background, err := adapter.queueGitStatusRefresh(projectID, worktree, reconcilePriorityBackground, "background")
	if err != nil {
		t.Fatalf("queue background refresh: %v", err)
	}
	backgroundResult, err := background.Wait(ctx)
	if err != nil {
		t.Fatalf("wait background refresh: %v", err)
	}
	if !backgroundResult.Deferred || backgroundResult.Skipped || backgroundResult.Reason != heavySessionStartBackgroundDeferReason {
		t.Fatalf("background refresh result = %+v, want deferred for heavy session start", backgroundResult)
	}
	if got := statusCalls.Load(); got != 0 {
		t.Fatalf("status calls after background refresh = %d, want 0", got)
	}

	visible, err := adapter.queueGitStatusRefresh(projectID, worktree, reconcilePriorityVisible, "visible")
	if err != nil {
		t.Fatalf("queue visible refresh: %v", err)
	}
	visibleResult, err := visible.Wait(ctx)
	if err != nil {
		t.Fatalf("wait visible refresh: %v", err)
	}
	if visibleResult.Err != nil {
		t.Fatalf("visible refresh error = %v, want nil", visibleResult.Err)
	}
	if got := statusCalls.Load(); got != 1 {
		t.Fatalf("status calls after visible refresh = %d, want 1", got)
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

func TestGitServiceAdapterHookRefreshCoalescesBurstForWorktree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"
	store := newGitAdapterStore(t, projectID, issueID, worktree, cleanGitStatus())

	var statusCalls atomic.Int32
	releaseStatus := make(chan struct{})
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain" {
			statusCalls.Add(1)
			<-releaseStatus
			return "", nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "git_status_hook_refresh_test",
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
		logger:             slog.Default(),
	}

	start := make(chan struct{})
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			<-start
			_, err := adapter.RefreshStatusForHook(ctx, projectID, worktree)
			errs <- err
		}()
	}
	close(start)
	for statusCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(releaseStatus)

	for i := 0; i < 5; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("RefreshStatusForHook error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for hook refresh burst")
		}
	}

	if got := statusCalls.Load(); got != 1 {
		t.Fatalf("status calls = %d, want 1 coalesced hook refresh", got)
	}
	counters := queue.snapshotCounters()
	if counters.Enqueued != 1 || counters.Deduped != 0 {
		t.Fatalf("queue counters = %+v, want one queued hook refresh", counters)
	}
}

func TestGitServiceAdapterStatusRefreshUsesWorktreeSpecificBaseBranch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	worktree := "/tmp/az-child"
	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
			mergeBaseRef = args[3]
			return "abc123\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "symbolic-ref":
			return "", errors.New("no remote head")
		case len(args) >= 8 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
			return " 2 files changed, 5 insertions(+), 1 deletion(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "rev-list" && args[3] == "--count":
			return "0\n", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}
	adapter := &gitServiceAdapter{
		client:     git.NewClient(runner, slog.Default()),
		baseBranch: "preview",
		baseBranchForWorktree: func(context.Context, string, string) string {
			return "az/parent"
		},
	}

	status, err := adapter.refreshGitStatusWriteThroughResult(ctx, projectID, worktree, false, false)
	if err != nil {
		t.Fatalf("refreshGitStatusWriteThroughResult: %v", err)
	}
	if status == nil || status.GitAdditions != 5 || status.GitDeletions != 1 {
		t.Fatalf("status = %+v, want runtime diff totals", status)
	}
	if mergeBaseRef != "az/parent" {
		t.Fatalf("merge-base ref = %q, want worktree-specific ancestor base", mergeBaseRef)
	}
}

func TestGitServiceAdapterDiffStatUsesWorktreeSpecificBaseBranch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	worktree := "/tmp/az-child"
	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
			mergeBaseRef = args[3]
			if args[3] != "az/parent" {
				return "", errors.New("preview should have been resolved to parent branch")
			}
			return "abc123\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "symbolic-ref":
			return "", errors.New("no remote head")
		case len(args) >= 8 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
			return " 2 files changed, 5 insertions(+), 1 deletion(-)\n", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}
	adapter := &gitServiceAdapter{
		client:     git.NewClient(runner, slog.Default()),
		baseBranch: "preview",
		baseBranchForWorktree: func(context.Context, string, string) string {
			return "az/parent"
		},
	}

	stat, err := adapter.DiffStat(ctx, projectID, worktree, "preview")
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if stat != "2 files changed, 5 insertions(+), 1 deletion(-)" {
		t.Fatalf("diff stat = %q, want parent-relative stat", stat)
	}
	if mergeBaseRef != "az/parent" {
		t.Fatalf("merge-base ref = %q, want worktree-specific ancestor base", mergeBaseRef)
	}
}

func TestGitServiceAdapterDiffStatPreservesExplicitBaseWithoutWorktreeSpecificBase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	worktree := "/tmp/az-child"
	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
			mergeBaseRef = args[3]
			if args[3] != "main" {
				return "", errors.New("explicit base should be preserved")
			}
			return "abc123\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "symbolic-ref":
			return "", errors.New("no remote head")
		case len(args) >= 8 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
			return " 1 file changed, 2 insertions(+)\n", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}
	adapter := &gitServiceAdapter{
		client:     git.NewClient(runner, slog.Default()),
		baseBranch: "preview",
		baseBranchForWorktree: func(context.Context, string, string) string {
			return ""
		},
	}

	stat, err := adapter.DiffStat(ctx, projectID, worktree, "main")
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if stat != "1 file changed, 2 insertions(+)" {
		t.Fatalf("diff stat = %q, want explicit-base stat", stat)
	}
	if mergeBaseRef != "main" {
		t.Fatalf("merge-base ref = %q, want explicit base", mergeBaseRef)
	}
}

func TestGitServiceAdapterRuntimeSignalsUsesProjectionOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-signal"
	worktree := "/tmp/az-signal"
	store := newGitAdapterStore(t, projectID, issueID, worktree, &git.GitStatus{
		HasChanges:     true,
		GitAdditions:   7,
		GitDeletions:   3,
		GitAheadCount:  2,
		GitBehindCount: 1,
	})

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		t.Fatalf("unexpected live git call for runtime signals: %v", args)
		return "", nil
	}}
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
	}

	signals, partialFailures, err := adapter.RuntimeSignals(ctx, projectID, []daemonhandlers.GitRuntimeSignalsTarget{
		{IssueID: issueID, Worktree: worktree},
	}, "main", true, "origin", false)
	if err != nil {
		t.Fatalf("RuntimeSignals: %v", err)
	}
	if partialFailures != 0 {
		t.Fatalf("partial failures = %d, want 0", partialFailures)
	}
	if len(signals) != 1 {
		t.Fatalf("signals len = %d, want 1", len(signals))
	}
	got := signals[0]
	if got.IssueID != issueID || got.Worktree != worktree {
		t.Fatalf("signal id/worktree = %+v", got)
	}
	if !got.HasUncommittedChanges || got.GitAdditions != 7 || got.GitDeletions != 3 || got.GitAheadCount != 2 || got.GitBehindCount != 1 {
		t.Fatalf("signal = %+v, want projected git metrics", got)
	}
}

func TestGitServiceAdapterRuntimeSignalsRefreshBypassesStaleCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-signal"
	worktree := "/tmp/az-signal"
	store := newGitAdapterStore(t, projectID, issueID, worktree, &git.GitStatus{
		HasChanges:   true,
		GitAdditions: 1,
		GitDeletions: 1,
	})

	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
			return " M changed.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
			mergeBaseRef = args[3]
			return "abc123\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "symbolic-ref":
			return "", errors.New("no remote head")
		case len(args) >= 8 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
			return " 2 files changed, 4 insertions(+), 2 deletions(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktree && args[2] == "rev-list" && args[3] == "--count":
			return "0\n", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
		baseBranch:        "preview",
		baseBranchForWorktree: func(context.Context, string, string) string {
			return "az/parent"
		},
	}
	adapter.storeRuntimeSignal(runtimeSignalCacheKey(projectID, issueID, worktree, "preview", true, "origin"), daemonhandlers.GitRuntimeSignalsResult{
		IssueID:      issueID,
		Worktree:     worktree,
		GitAdditions: 99,
		GitDeletions: 99,
	}, time.Now())

	signals, partialFailures, err := adapter.RuntimeSignals(ctx, projectID, []daemonhandlers.GitRuntimeSignalsTarget{
		{IssueID: issueID, Worktree: worktree},
	}, "preview", true, "origin", true)
	if err != nil {
		t.Fatalf("RuntimeSignals refresh: %v", err)
	}
	if partialFailures != 0 {
		t.Fatalf("partial failures = %d, want 0", partialFailures)
	}
	if len(signals) != 1 {
		t.Fatalf("signals len = %d, want 1", len(signals))
	}
	got := signals[0]
	if !got.HasUncommittedChanges || got.GitAdditions != 4 || got.GitDeletions != 2 {
		t.Fatalf("signal = %+v, want refreshed projection metrics", got)
	}
	if mergeBaseRef != "az/parent" {
		t.Fatalf("merge-base ref = %q, want worktree-specific ancestor base", mergeBaseRef)
	}
}

func TestGitServiceAdapterRuntimeSignalsMissingProjectionReturnsZeroSignal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-missing"
	worktree := "/tmp/az-missing"

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		t.Fatalf("unexpected live git call for runtime signals: %v", args)
		return "", nil
	}}
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
	}

	signals, partialFailures, err := adapter.RuntimeSignals(ctx, projectID, []daemonhandlers.GitRuntimeSignalsTarget{
		{IssueID: issueID, Worktree: worktree},
	}, "main", false, "origin", false)
	if err != nil {
		t.Fatalf("RuntimeSignals: %v", err)
	}
	if partialFailures != 0 {
		t.Fatalf("partial failures = %d, want 0", partialFailures)
	}
	if len(signals) != 1 {
		t.Fatalf("signals len = %d, want 1", len(signals))
	}
	got := signals[0]
	if got.IssueID != issueID || got.Worktree != worktree {
		t.Fatalf("signal id/worktree = %+v", got)
	}
	if got.HasUncommittedChanges || got.GitAdditions != 0 || got.GitDeletions != 0 || got.GitAheadCount != 0 || got.GitBehindCount != 0 {
		t.Fatalf("signal = %+v, want zero-value runtime signal for missing projection", got)
	}
}

func TestGitServiceAdapterBranchBehindUsesProjectionOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-behind"
	worktree := "/tmp/az-behind"
	store := newGitAdapterStore(t, projectID, issueID, worktree, &git.GitStatus{
		GitAheadCount:  4,
		GitBehindCount: 6,
	})

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		t.Fatalf("unexpected live git call for branch-behind: %v", args)
		return "", nil
	}}
	adapter := &gitServiceAdapter{
		client:            git.NewClient(runner, slog.Default()),
		runtimeStateStore: store,
	}

	ahead, behind, err := adapter.BranchBehind(ctx, projectID, worktree, "main", "origin")
	if err != nil {
		t.Fatalf("BranchBehind: %v", err)
	}
	if ahead != 4 || behind != 6 {
		t.Fatalf("ahead/behind = %d/%d, want 4/6", ahead, behind)
	}
}
