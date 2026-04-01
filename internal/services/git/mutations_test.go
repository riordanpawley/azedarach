package git

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
)

type mutationRunner struct {
	runFn func(args ...string) (string, error)
}

func (r *mutationRunner) Run(_ context.Context, args ...string) (string, error) {
	if r.runFn == nil {
		return "", nil
	}
	return r.runFn(args...)
}

func TestMergePreflightUsesWorktreeAwareStatusAndMergeTree(t *testing.T) {
	t.Parallel()

	var calls [][]string
	runner := &mutationRunner{runFn: func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
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

	client := NewClient(runner, slog.Default())
	result, err := client.MergePreflight(context.Background(), "/tmp/source", "/tmp/target", "main", "az/source")
	if err != nil {
		t.Fatalf("MergePreflight() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergePreflight() returned nil result")
	}
	if !result.SourceStatus.HasChanges {
		t.Fatal("source status should report changes")
	}
	if !result.HasConflicts {
		t.Fatal("expected merge preflight conflict")
	}
	if !reflect.DeepEqual(result.ConflictFiles, []string{"cmd/az/main.go"}) {
		t.Fatalf("conflict files = %v, want [cmd/az/main.go]", result.ConflictFiles)
	}
	if len(calls) != 3 {
		t.Fatalf("git calls = %v, want 3", calls)
	}
}

func TestDiscardChangesRunsRestoreThenCleanInWorktree(t *testing.T) {
	t.Parallel()

	var calls [][]string
	runner := &mutationRunner{runFn: func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}}

	client := NewClient(runner, slog.Default())
	if err := client.DiscardChanges(context.Background(), "/tmp/az-1"); err != nil {
		t.Fatalf("DiscardChanges() error = %v", err)
	}

	want := [][]string{
		{"-C", "/tmp/az-1", "restore", "--staged", "--worktree", "."},
		{"-C", "/tmp/az-1", "clean", "-fd"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("git calls = %v, want %v", calls, want)
	}
}

func TestCreateCheckpointReturnsErrNoChangesToCommitWhenClean(t *testing.T) {
	t.Parallel()

	var calls [][]string
	runner := &mutationRunner{runFn: func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain" {
			return "", nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	client := NewClient(runner, slog.Default())
	err := client.CreateCheckpoint(context.Background(), "/tmp/az-1", "")
	if !errors.Is(err, ErrNoChangesToCommit) {
		t.Fatalf("CreateCheckpoint() error = %v, want ErrNoChangesToCommit", err)
	}
	if len(calls) != 1 {
		t.Fatalf("git calls = %v, want status only", calls)
	}
}

func TestCreateCheckpointStagesAndCommitsWithDefaultMessage(t *testing.T) {
	t.Parallel()

	var calls [][]string
	runner := &mutationRunner{runFn: func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain":
			return " M daemon/git_adapter.go", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "add" && args[3] == "-A":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "commit" && args[3] == "-m" && args[4] == DefaultCheckpointMessage:
			return "[branch abc123] checkpoint", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	client := NewClient(runner, slog.Default())
	if err := client.CreateCheckpoint(context.Background(), "/tmp/az-1", ""); err != nil {
		t.Fatalf("CreateCheckpoint() error = %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("git calls = %v, want 3", calls)
	}
}
