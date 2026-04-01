package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type recordingGitRunner struct {
	runFn func(args ...string) (string, error)
}

func (r *recordingGitRunner) Run(_ context.Context, args ...string) (string, error) {
	if r.runFn == nil {
		return "", nil
	}
	return r.runFn(args...)
}

func TestGitServiceAdapterMergeForcesStatusUpdatePublish(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectID := "default"
	issueID := "az-target"
	worktree := "/tmp/az-target"

	store := daemonstate.NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	if err := store.UpsertWorktree(ctx, daemonstate.WorktreeProjection{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    "az/az-target",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	status := &git.GitStatus{HasChanges: false}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if err := store.UpsertWorktreeGitStatus(ctx, projectID, issueID, rawStatus, time.Now().UTC()); err != nil {
		t.Fatalf("UpsertWorktreeGitStatus: %v", err)
	}

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
