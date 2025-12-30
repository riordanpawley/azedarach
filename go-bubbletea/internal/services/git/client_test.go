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
			name:      "untracked files only",
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
			gitOutput:      "az/bead-123",
			expectedBranch: "az/bead-123",
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
	expectedStat := " file.txt | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)"

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "diff" && args[1] == "--stat" {
				return expectedStat, nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	stat, err := client.DiffStat(context.Background(), "/fake/worktree")

	if err != nil {
		t.Fatalf("DiffStat() error = %v", err)
	}

	if stat != expectedStat {
		t.Errorf("DiffStat() = %v, want %v", stat, expectedStat)
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
