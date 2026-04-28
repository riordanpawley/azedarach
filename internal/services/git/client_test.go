package git

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// mockRunner is a test implementation of CommandRunner.
type mockRunner struct {
	runFunc func(ctx context.Context, args ...string) (string, error)
}

func (m *mockRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "-C" {
		args = args[2:]
	}
	if m.runFunc != nil {
		return m.runFunc(ctx, args...)
	}
	return "", nil
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name           string
		gitOutput      string
		expectedStatus *GitStatus
	}{
		{
			name:      "clean repository",
			gitOutput: "",
			expectedStatus: &GitStatus{
				Modified:   []string{},
				Added:      []string{},
				Deleted:    []string{},
				Untracked:  []string{},
				Staged:     []string{},
				HasChanges: false,
			},
		},
		{
			name: "modified files",
			gitOutput: ` M file1.txt
 M file2.txt`,
			expectedStatus: &GitStatus{
				Modified:   []string{"file1.txt", "file2.txt"},
				Added:      []string{},
				Deleted:    []string{},
				Untracked:  []string{},
				Staged:     []string{},
				HasChanges: true,
			},
		},
		{
			name: "staged and unstaged changes",
			gitOutput: `M  staged.txt
 M unstaged.txt
A  added.txt
 D deleted.txt
?? untracked.txt`,
			expectedStatus: &GitStatus{
				Modified:   []string{"staged.txt", "unstaged.txt"},
				Added:      []string{"added.txt"},
				Deleted:    []string{"deleted.txt"},
				Untracked:  []string{"untracked.txt"},
				Staged:     []string{"staged.txt", "added.txt"},
				HasChanges: true,
			},
		},
		{
			name: "untracked files only",
			gitOutput: `?? file1.txt
?? file2.txt`,
			expectedStatus: &GitStatus{
				Modified:   []string{},
				Added:      []string{},
				Deleted:    []string{},
				Untracked:  []string{"file1.txt", "file2.txt"},
				Staged:     []string{},
				HasChanges: true,
			},
		},
		{
			name: "mixed changes",
			gitOutput: `MM both-modified.txt
A  staged-added.txt
 M unstaged-modified.txt
 D unstaged-deleted.txt
?? untracked.txt`,
			expectedStatus: &GitStatus{
				Modified:   []string{"both-modified.txt", "unstaged-modified.txt"},
				Added:      []string{"staged-added.txt"},
				Deleted:    []string{"unstaged-deleted.txt"},
				Untracked:  []string{"untracked.txt"},
				Staged:     []string{"both-modified.txt", "staged-added.txt"},
				HasChanges: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				runFunc: func(ctx context.Context, args ...string) (string, error) {
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return tt.gitOutput, nil
					}
					return "", fmt.Errorf("unexpected command: %v", args)
				},
			}

			client := NewClient(runner, slog.Default())
			status, err := client.Status(context.Background(), "/fake/worktree")

			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}

			// Compare status
			if status.HasChanges != tt.expectedStatus.HasChanges {
				t.Errorf("HasChanges = %v, want %v", status.HasChanges, tt.expectedStatus.HasChanges)
			}

			compareStringSlices(t, "Modified", status.Modified, tt.expectedStatus.Modified)
			compareStringSlices(t, "Added", status.Added, tt.expectedStatus.Added)
			compareStringSlices(t, "Deleted", status.Deleted, tt.expectedStatus.Deleted)
			compareStringSlices(t, "Untracked", status.Untracked, tt.expectedStatus.Untracked)
			compareStringSlices(t, "Staged", status.Staged, tt.expectedStatus.Staged)
		})
	}
}

func TestMergeSuccess(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "merge" {
				return "Merge made by the 'recursive' strategy.", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	result, err := client.Merge(context.Background(), "/fake/worktree", "feature-branch")

	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}

	if !result.Success {
		t.Error("Merge should be successful")
	}

	if result.HasConflicts {
		t.Error("Merge should not have conflicts")
	}

	if len(result.ConflictFiles) != 0 {
		t.Errorf("ConflictFiles should be empty, got %v", result.ConflictFiles)
	}
}

func TestMergeWithConflicts(t *testing.T) {
	conflictOutput := `Auto-merging file1.txt
CONFLICT (content): Merge conflict in file1.txt
Auto-merging file2.txt
CONFLICT (content): Merge conflict in file2.txt
Automatic merge failed; fix conflicts and then commit the result.`

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "merge" {
				return conflictOutput, fmt.Errorf("merge conflict")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	result, err := client.Merge(context.Background(), "/fake/worktree", "feature-branch")

	if err != nil {
		t.Fatalf("Merge() with conflicts should not return error, got %v", err)
	}

	if result.Success {
		t.Error("Merge should not be successful")
	}

	if !result.HasConflicts {
		t.Error("Merge should have conflicts")
	}

	expectedConflicts := []string{"file1.txt", "file2.txt"}
	compareStringSlices(t, "ConflictFiles", result.ConflictFiles, expectedConflicts)
}

func TestAbortMerge(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "merge" && args[1] == "--abort" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	err := client.AbortMerge(context.Background(), "/fake/worktree")

	if err != nil {
		t.Fatalf("AbortMerge() error = %v", err)
	}
}

func TestCurrentBranch(t *testing.T) {
	tests := []struct {
		name           string
		gitOutput      string
		expectedBranch string
	}{
		{
			name:           "main branch",
			gitOutput:      "main",
			expectedBranch: "main",
		},
		{
			name:           "feature branch",
			gitOutput:      "az/issue-123",
			expectedBranch: "az/issue-123",
		},
		{
			name:           "branch with trailing newline",
			gitOutput:      "feature\n",
			expectedBranch: "feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{
				runFunc: func(ctx context.Context, args ...string) (string, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return tt.gitOutput, nil
					}
					return "", fmt.Errorf("unexpected command: %v", args)
				},
			}

			client := NewClient(runner, slog.Default())
			branch, err := client.CurrentBranch(context.Background(), "/fake/worktree")

			if err != nil {
				t.Fatalf("CurrentBranch() error = %v", err)
			}

			if branch != tt.expectedBranch {
				t.Errorf("CurrentBranch() = %v, want %v", branch, tt.expectedBranch)
			}
		})
	}
}

func TestFetch(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "fetch" && args[1] == "origin" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	err := client.Fetch(context.Background(), "/fake/worktree", "origin")

	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
}

func TestPush(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "push" && args[1] == "origin" && args[2] == "main" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	err := client.Push(context.Background(), "/fake/worktree", "origin", "main")

	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
}

func TestCheckout(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "checkout" && args[1] == "main" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	err := client.Checkout(context.Background(), "/fake/worktree", "main")

	if err != nil {
		t.Fatalf("Checkout() error = %v", err)
	}
}

func TestDiff(t *testing.T) {
	expectedDiff := `diff --git a/file.txt b/file.txt
index 1234567..abcdefg 100644
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-old content
+new content`

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 1 && args[0] == "diff" {
				return expectedDiff, nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	diff, err := client.Diff(context.Background(), "/fake/worktree")

	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if diff != expectedDiff {
		t.Errorf("Diff() = %v, want %v", diff, expectedDiff)
	}
}

func TestDiffStat(t *testing.T) {
	unstagedStat := "1 file changed, 1 insertion(+)"
	stagedStat := "1 file changed, 2 deletions(-)"
	expectedStat := unstagedStat + "\n" + stagedStat

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "diff" && args[1] == "--shortstat" {
				return unstagedStat, nil
			}
			if len(args) >= 3 && args[0] == "diff" && args[1] == "--cached" && args[2] == "--shortstat" {
				return stagedStat, nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	stat, err := client.DiffStat(context.Background(), "/fake/worktree", "")

	if err != nil {
		t.Fatalf("DiffStat() error = %v", err)
	}

	if stat != expectedStat {
		t.Errorf("DiffStat() = %v, want %v", stat, expectedStat)
	}
}

func TestDiffStatAgainstBaseBranch(t *testing.T) {
	expectedStat := "2 files changed, 10 insertions(+), 4 deletions(-)"

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "abc123\n", nil
			}
			if len(args) >= 6 &&
				args[0] == "diff" &&
				args[1] == "--shortstat" &&
				args[2] == "abc123" &&
				args[3] == "HEAD" &&
				args[4] == "--" &&
				args[5] == ":^.azedarach" {
				return expectedStat, nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	stat, err := client.DiffStat(context.Background(), "/fake/worktree", "main")

	if err != nil {
		t.Fatalf("DiffStat() error = %v", err)
	}

	if stat != expectedStat {
		t.Errorf("DiffStat() = %v, want %v", stat, expectedStat)
	}
}

func TestDiffStatAgainstBaseBranchFallsBackToLocalChangesWhenMergeBaseFails(t *testing.T) {
	unstagedStat := "1 file changed, 3 insertions(+)"
	stagedStat := "1 file changed, 1 deletion(-)"
	expectedStat := unstagedStat + "\n" + stagedStat

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "", fmt.Errorf("unknown revision")
			}
			if len(args) >= 2 && args[0] == "diff" && args[1] == "--shortstat" {
				return unstagedStat, nil
			}
			if len(args) >= 3 && args[0] == "diff" && args[1] == "--cached" && args[2] == "--shortstat" {
				return stagedStat, nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	stat, err := client.DiffStat(context.Background(), "/fake/worktree", "main")

	if err != nil {
		t.Fatalf("DiffStat() error = %v", err)
	}
	if stat != expectedStat {
		t.Errorf("DiffStat() = %v, want %v", stat, expectedStat)
	}
}

func TestDiffStatTotals(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "diff" && args[1] == "--shortstat" {
				return "2 files changed, 10 insertions(+), 4 deletions(-)", nil
			}
			if len(args) >= 3 && args[0] == "diff" && args[1] == "--cached" && args[2] == "--shortstat" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	additions, deletions, err := client.DiffStatTotals(context.Background(), "/fake/worktree", "")
	if err != nil {
		t.Fatalf("DiffStatTotals() error = %v", err)
	}
	if additions != 10 || deletions != 4 {
		t.Fatalf("DiffStatTotals() = %d/%d, want 10/4", additions, deletions)
	}
}

func TestBranchAheadBehind(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "HEAD..main" {
				return "3\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "main..HEAD" {
				return "2\n", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	ahead, behind, err := client.BranchAheadBehind(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("BranchAheadBehind() error = %v", err)
	}
	if ahead != 2 || behind != 3 {
		t.Fatalf("BranchAheadBehind() = %d/%d, want 2/3", ahead, behind)
	}
}

func TestBranchAheadBehindFallsBackToOriginRef(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "HEAD..main" {
				return "", fmt.Errorf("unknown revision")
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "HEAD..origin/main" {
				return "1\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "origin/main..HEAD" {
				return "4\n", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	ahead, behind, err := client.BranchAheadBehind(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("BranchAheadBehind() error = %v", err)
	}
	if ahead != 4 || behind != 1 {
		t.Fatalf("BranchAheadBehind() = %d/%d, want 4/1", ahead, behind)
	}
}

func TestBranchAheadBehindPrefersOriginHeadWhenBaseIsGeneric(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" && args[2] == "refs/remotes/origin/HEAD" {
				return "origin/preview\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "HEAD..origin/preview" {
				return "7\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "origin/preview..HEAD" {
				return "3\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && (args[2] == "HEAD..main" || args[2] == "main..HEAD") {
				return "", fmt.Errorf("should not use main when origin/HEAD differs")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	ahead, behind, err := client.BranchAheadBehind(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("BranchAheadBehind() error = %v", err)
	}
	if ahead != 3 || behind != 7 {
		t.Fatalf("BranchAheadBehind() = %d/%d, want 3/7", ahead, behind)
	}
}

func TestRuntimeStatus(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			switch {
			case len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain":
				return " M changed.go\n?? new.go", nil
			case len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD":
				return "abc123\n", nil
			case len(args) >= 6 &&
				args[0] == "diff" &&
				args[1] == "--shortstat" &&
				args[2] == "abc123" &&
				args[3] == "HEAD":
				return " 2 files changed, 7 insertions(+), 3 deletions(-)\n", nil
			case len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "HEAD..main":
				return "5\n", nil
			case len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "main..HEAD":
				return "2\n", nil
			default:
				return "", fmt.Errorf("unexpected command: %v", args)
			}
		},
	}

	client := NewClient(runner, slog.Default())
	status, err := client.RuntimeStatus(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("RuntimeStatus() error = %v", err)
	}
	if !status.HasChanges {
		t.Fatal("RuntimeStatus() should mark repository dirty")
	}
	if status.GitAdditions != 7 || status.GitDeletions != 3 {
		t.Fatalf("RuntimeStatus() diff totals = %d/%d, want 7/3", status.GitAdditions, status.GitDeletions)
	}
	if status.GitAheadCount != 2 || status.GitBehindCount != 5 {
		t.Fatalf("RuntimeStatus() ahead/behind = %d/%d, want 2/5", status.GitAheadCount, status.GitBehindCount)
	}
}

func TestMergeBaseFallsBackToOriginRef(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "", fmt.Errorf("unknown revision")
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "origin/main" && args[2] == "HEAD" {
				return "def456\n", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	mergeBase, err := client.MergeBase(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("MergeBase() error = %v", err)
	}
	if mergeBase != "def456" {
		t.Fatalf("MergeBase() = %q, want def456", mergeBase)
	}
}

func TestMergeBasePrefersOriginHeadWhenBaseIsGeneric(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" && args[2] == "refs/remotes/origin/HEAD" {
				return "origin/preview\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "origin/preview" && args[2] == "HEAD" {
				return "fedcba\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "", fmt.Errorf("should not use main when origin/HEAD differs")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	mergeBase, err := client.MergeBase(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("MergeBase() error = %v", err)
	}
	if mergeBase != "fedcba" {
		t.Fatalf("MergeBase() = %q, want fedcba", mergeBase)
	}
}

func TestMergeBase(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "abc123\n", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	mergeBase, err := client.MergeBase(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("MergeBase() error = %v", err)
	}
	if mergeBase != "abc123" {
		t.Fatalf("MergeBase() = %q, want abc123", mergeBase)
	}
}

func TestChangedFiles(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "abc123\n", nil
			}
			if len(args) >= 5 &&
				args[0] == "diff" &&
				args[1] == "--name-status" &&
				args[2] == "abc123" &&
				args[3] == "--" &&
				args[4] == ":^.azedarach" {
				return "M\tinternal/tui/model.go\nA\tnew.go\nD\told.go\nR100\tfrom.go\tto.go", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	files, err := client.ChangedFiles(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("ChangedFiles() len = %d, want 4", len(files))
	}

	if files[0].Path != "internal/tui/model.go" || files[0].Status != DiffFileModified {
		t.Fatalf("first file = %+v", files[0])
	}
	if files[1].Path != "new.go" || files[1].Status != DiffFileAdded {
		t.Fatalf("second file = %+v", files[1])
	}
	if files[2].Path != "old.go" || files[2].Status != DiffFileDeleted {
		t.Fatalf("third file = %+v", files[2])
	}
	if files[3].OldPath != "from.go" || files[3].Path != "to.go" || files[3].Status != DiffFileRenamed {
		t.Fatalf("fourth file = %+v", files[3])
	}
}

func TestParseConflicts(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		conflicts []string
	}{
		{
			name:      "no conflicts",
			output:    "Merge made by the 'recursive' strategy.",
			conflicts: []string{},
		},
		{
			name: "single conflict",
			output: `Auto-merging file1.txt
CONFLICT (content): Merge conflict in file1.txt
Automatic merge failed; fix conflicts and then commit the result.`,
			conflicts: []string{"file1.txt"},
		},
		{
			name: "multiple conflicts",
			output: `Auto-merging file1.txt
CONFLICT (content): Merge conflict in file1.txt
Auto-merging file2.txt
CONFLICT (content): Merge conflict in file2.txt
CONFLICT (modify/delete): file3.txt deleted in HEAD and modified in feature-branch. Version feature-branch of file3.txt left in tree.
Automatic merge failed; fix conflicts and then commit the result.`,
			conflicts: []string{"file1.txt", "file2.txt", "file3.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflicts := parseConflicts(tt.output)
			compareStringSlices(t, "conflicts", conflicts, tt.conflicts)
		})
	}
}

// Helper function to compare string slices
func compareStringSlices(t *testing.T, name string, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("%s length = %d, want %d\nGot: %v\nWant: %v", name, len(got), len(want), got, want)
		return
	}

	// Create maps for easy comparison (order-independent)
	gotMap := make(map[string]bool)
	for _, s := range got {
		gotMap[s] = true
	}

	wantMap := make(map[string]bool)
	for _, s := range want {
		wantMap[s] = true
	}

	for s := range wantMap {
		if !gotMap[s] {
			t.Errorf("%s missing %q\nGot: %v\nWant: %v", name, s, got, want)
		}
	}

	for s := range gotMap {
		if !wantMap[s] {
			t.Errorf("%s has unexpected %q\nGot: %v\nWant: %v", name, s, got, want)
		}
	}
}

func TestStatusError(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			return "", fmt.Errorf("git command failed")
		},
	}

	client := NewClient(runner, slog.Default())
	_, err := client.Status(context.Background(), "/fake/worktree")

	if err == nil {
		t.Error("Status() should return error when git command fails")
	}

	if !strings.Contains(err.Error(), "failed to get git status") {
		t.Errorf("Error message should mention status failure, got: %v", err)
	}
}

func TestMergeError(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			return "", fmt.Errorf("fatal: not a git repository")
		},
	}

	client := NewClient(runner, slog.Default())
	_, err := client.Merge(context.Background(), "/fake/worktree", "branch")

	if err == nil {
		t.Error("Merge() should return error when git command fails without conflict")
	}

	if !strings.Contains(err.Error(), "failed to merge branch") {
		t.Errorf("Error message should mention merge failure, got: %v", err)
	}
}
