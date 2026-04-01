package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
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

func newGitAdapterStore(t *testing.T, projectID, issueID, worktree string, status *git.GitStatus) *daemonstate.ProjectionStore {
	t.Helper()

	store := daemonstate.NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.UpsertWorktree(ctx, daemonstate.WorktreeProjection{
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
		if err := store.UpsertWorktreeGitStatus(ctx, projectID, issueID, rawStatus, time.Now().UTC()); err != nil {
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
		client:          git.NewClient(runner, slog.Default()),
		projectionStore: store,
		onStatusUpdate: func(gotProjectID, gotIssueID, gotWorktree string) {
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
		client:          git.NewClient(runner, slog.Default()),
		projectionStore: store,
		onStatusUpdate: func(_, gotIssueID, gotWorktree string) {
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
		client:          git.NewClient(runner, slog.Default()),
		projectionStore: store,
		onStatusUpdate:  func(_, _, _ string) { updates++ },
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
		client:          git.NewClient(runner, slog.Default()),
		projectionStore: store,
		onStatusUpdate:  func(_, _, _ string) { updates++ },
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
		onStatusUpdate: func(_, _, _ string) { updates++ },
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
