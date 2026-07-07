package git

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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

type rawMockRunner struct {
	runFunc func(ctx context.Context, args ...string) (string, error)
}

func (m *rawMockRunner) Run(ctx context.Context, args ...string) (string, error) {
	if m.runFunc != nil {
		return m.runFunc(ctx, args...)
	}
	return "", nil
}

func clientTestArgsForWorktree(args []string, worktree string, want ...string) bool {
	if len(args) != len(want)+2 || args[0] != "-C" || args[1] != worktree {
		return false
	}
	for i, part := range want {
		if args[i+2] != part {
			return false
		}
	}
	return true
}

func initDivergedRepo(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	runClientTestGit(t, repo, "init", "-q", "-b", "main")
	runClientTestGit(t, repo, "config", "user.email", "test@example.com")
	runClientTestGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runClientTestGit(t, repo, "add", "base.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "base")

	runClientTestGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	runClientTestGit(t, repo, "add", "feature.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "feature")

	runClientTestGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main file: %v", err)
	}
	runClientTestGit(t, repo, "add", "main.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "main")
	return repo
}

func runClientTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func runClientTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
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
		{
			name: "unmerged conflict files",
			gitOutput: `UU conflicted.txt
AA both-added.txt
DU deleted-by-us.txt`,
			expectedStatus: &GitStatus{
				Modified:     []string{},
				Added:        []string{},
				Deleted:      []string{},
				Untracked:    []string{},
				Staged:       []string{"conflicted.txt", "both-added.txt", "deleted-by-us.txt"},
				Conflicted:   []string{"conflicted.txt", "both-added.txt", "deleted-by-us.txt"},
				HasChanges:   true,
				HasConflicts: true,
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
			if status.HasConflicts != tt.expectedStatus.HasConflicts {
				t.Errorf("HasConflicts = %v, want %v", status.HasConflicts, tt.expectedStatus.HasConflicts)
			}

			compareStringSlices(t, "Modified", status.Modified, tt.expectedStatus.Modified)
			compareStringSlices(t, "Added", status.Added, tt.expectedStatus.Added)
			compareStringSlices(t, "Deleted", status.Deleted, tt.expectedStatus.Deleted)
			compareStringSlices(t, "Untracked", status.Untracked, tt.expectedStatus.Untracked)
			compareStringSlices(t, "Staged", status.Staged, tt.expectedStatus.Staged)
			compareStringSlices(t, "Conflicted", status.Conflicted, tt.expectedStatus.Conflicted)
		})
	}
}

func TestWorktreePathForBranch(t *testing.T) {
	client := NewClient(&mockRunner{runFunc: func(_ context.Context, args ...string) (string, error) {
		if strings.Join(args, " ") != "worktree list --porcelain" {
			t.Fatalf("args = %q, want worktree list --porcelain", strings.Join(args, " "))
		}
		return `worktree /tmp/repo
HEAD abc123
branch refs/heads/main

worktree /tmp/repo-parent
HEAD def456
branch refs/heads/riordan/parent/work

worktree /tmp/repo-child
HEAD fedcba
branch refs/heads/riordan/child/work
`, nil
	}}, slog.Default())

	path, found, err := client.WorktreePathForBranch(context.Background(), "riordan/parent/work")
	if err != nil {
		t.Fatalf("WorktreePathForBranch error: %v", err)
	}
	if !found || path != "/tmp/repo-parent" {
		t.Fatalf("path=%q found=%v, want /tmp/repo-parent true", path, found)
	}

	path, found, err = client.WorktreePathForBranch(context.Background(), "missing")
	if err != nil {
		t.Fatalf("WorktreePathForBranch missing error: %v", err)
	}
	if found || path != "" {
		t.Fatalf("path=%q found=%v, want empty false", path, found)
	}
}

func TestMergeSuccess(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge" && args[1] == "--no-edit" && args[2] == "feature-branch" {
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

	var calls []string
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			if len(args) >= 3 && args[0] == "merge" && args[1] == "--no-edit" && args[2] == "feature-branch" {
				return conflictOutput, fmt.Errorf("merge conflict")
			}
			if len(args) >= 2 && args[0] == "merge" && args[1] == "--abort" {
				return "", nil
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
	compareStringSlices(t, "calls", calls, []string{"merge --no-edit feature-branch", "merge --abort"})
}

func TestMergeWithConflictsReturnsAbortError(t *testing.T) {
	conflictOutput := `CONFLICT (content): Merge conflict in file1.txt
Automatic merge failed; fix conflicts and then commit the result.`

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge" && args[1] == "--no-edit" && args[2] == "feature-branch" {
				return conflictOutput, fmt.Errorf("merge conflict")
			}
			if len(args) >= 2 && args[0] == "merge" && args[1] == "--abort" {
				return "", fmt.Errorf("abort failed")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	result, err := client.Merge(context.Background(), "/fake/worktree", "feature-branch")

	if err == nil {
		t.Fatal("Merge() error = nil, want abort error")
	}
	if result != nil {
		t.Fatalf("Merge() result = %+v, want nil on abort error", result)
	}
	if !strings.Contains(err.Error(), "failed to abort conflicted merge") {
		t.Fatalf("Merge() error = %v, want abort context", err)
	}
}

func TestMergeAbortsIncompleteNonConflictMerge(t *testing.T) {
	var calls []string
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			switch {
			case len(args) >= 3 && args[0] == "merge" && args[1] == "--no-edit" && args[2] == "feature-branch":
				return "", fmt.Errorf("commit-msg hook failed")
			case len(args) >= 4 && args[0] == "rev-parse" && args[1] == "-q" && args[2] == "--verify" && args[3] == "MERGE_HEAD":
				return "abc123", nil
			case len(args) >= 2 && args[0] == "merge" && args[1] == "--abort":
				return "", nil
			default:
				return "", fmt.Errorf("unexpected command: %v", args)
			}
		},
	}

	client := NewClient(runner, slog.Default())
	result, err := client.Merge(context.Background(), "/fake/worktree", "feature-branch")

	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if result == nil || result.Success || result.HasConflicts {
		t.Fatalf("Merge() result = %+v, want non-conflict failure result", result)
	}
	if !strings.Contains(result.Message, "commit-msg hook failed") {
		t.Fatalf("Merge() message = %q, want hook failure", result.Message)
	}
	compareStringSlices(t, "calls", calls, []string{
		"merge --no-edit feature-branch",
		"rev-parse -q --verify MERGE_HEAD",
		"merge --abort",
	})
}

func TestMergePreservesCommitHooksAndAbortsIncompleteMerge(t *testing.T) {
	repo := initDivergedRepo(t)
	hookPath := filepath.Join(repo, ".git", "hooks", "commit-msg")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho hook failed >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.Merge(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if result == nil {
		t.Fatal("Merge() result = nil")
	}
	if result.Success {
		t.Fatalf("Merge() result = %+v, want failed merge result", result)
	}
	if result.HasConflicts {
		t.Fatalf("Merge() result = %+v, want non-conflict hook failure", result)
	}
	if !strings.Contains(result.Message, "hook failed") {
		t.Fatalf("Merge() message = %q, want hook output", result.Message)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("MERGE_HEAD stat err = %v, want absent", err)
	}

	status, err := client.Status(context.Background(), repo)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("status = %+v, want clean after aborted incomplete merge", status)
	}
}

func TestMergeCleanlyDiscardsDirtyPostMergeHookAndReportsFailure(t *testing.T) {
	repo := initDivergedRepo(t)
	hookPath := filepath.Join(repo, ".git", "hooks", "post-merge")
	hook := "#!/bin/sh\nprintf hook-dirty\\n > hook-created.txt\ngit add hook-created.txt\necho post-merge hook dirtied target >&2\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.MergeCleanly(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("MergeCleanly() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergeCleanly() result = nil")
	}
	if result.Success {
		t.Fatalf("MergeCleanly() result = %+v, want post-merge dirty failure", result)
	}
	if result.HasConflicts {
		t.Fatalf("MergeCleanly() result = %+v, want non-conflict failure", result)
	}
	if !strings.Contains(result.Message, "post-merge hooks") || !strings.Contains(result.Message, "hook-created.txt") {
		t.Fatalf("MergeCleanly() message = %q, want post-merge dirty detail", result.Message)
	}

	status, err := client.Status(context.Background(), repo)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("status = %+v, want clean after discarded post-merge hook changes", status)
	}
	if _, err := os.Stat(filepath.Join(repo, "hook-created.txt")); !os.IsNotExist(err) {
		t.Fatalf("hook-created.txt stat err = %v, want removed", err)
	}
}

func TestMergeCleanlyDiscardsDirtyCommitMsgHookAfterAbort(t *testing.T) {
	repo := initDivergedRepo(t)
	hookPath := filepath.Join(repo, ".git", "hooks", "commit-msg")
	hook := "#!/bin/sh\nprintf hook-dirty\\n > hook-created.txt\necho commit-msg hook dirtied target >&2\nexit 1\n"
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.MergeCleanly(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("MergeCleanly() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergeCleanly() result = nil")
	}
	if result.Success {
		t.Fatalf("MergeCleanly() result = %+v, want commit-msg dirty failure", result)
	}
	if !strings.Contains(result.Message, "commit-msg hook dirtied target") ||
		!strings.Contains(result.Message, "discarded partial merge changes") ||
		!strings.Contains(result.Message, "hook-created.txt") {
		t.Fatalf("MergeCleanly() message = %q, want commit-msg cleanup detail", result.Message)
	}

	status, err := client.Status(context.Background(), repo)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("status = %+v, want clean after discarded commit-msg hook changes", status)
	}
	if _, err := os.Stat(filepath.Join(repo, "hook-created.txt")); !os.IsNotExist(err) {
		t.Fatalf("hook-created.txt stat err = %v, want removed", err)
	}
}

func TestMergeCleanlyTransactionalAppliesScratchMergeToCleanTarget(t *testing.T) {
	repo := initDivergedRepo(t)
	originalHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")

	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("MergeCleanlyTransactional() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergeCleanlyTransactional() result = nil")
	}
	if !result.Success {
		t.Fatalf("MergeCleanlyTransactional() result = %+v, want success", result)
	}
	if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head == originalHead {
		t.Fatalf("HEAD = %s, want transactional merge to advance target", head)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatalf("feature.txt stat err = %v, want merged file in target", err)
	}

	status, err := client.Status(context.Background(), repo)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("status = %+v, want clean target after transactional merge", status)
	}
	if worktrees := runClientTestGitOutput(t, repo, "worktree", "list", "--porcelain"); strings.Contains(worktrees, "azedarach-integration-") {
		t.Fatalf("worktree list contains scratch integration worktree after cleanup:\n%s", worktrees)
	}
}

func TestMergeCleanlyTransactionalRunsScratchHooksAndKeepsTargetCleanWhenHookFails(t *testing.T) {
	repo := initDivergedRepo(t)
	originalHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	hookPath := filepath.Join(repo, ".git", "hooks", "commit-msg")
	hook := "#!/bin/sh\nprintf commit-msg-dirty\\n > commit-msg-created.txt\necho commit-msg hook dirtied scratch >&2\nexit 1\n"
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("write commit-msg hook: %v", err)
	}

	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("MergeCleanlyTransactional() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergeCleanlyTransactional() result = nil")
	}
	if result.Success {
		t.Fatalf("MergeCleanlyTransactional() result = %+v, want scratch hook failure", result)
	}
	if !strings.Contains(result.Message, "commit-msg hook dirtied scratch") ||
		!strings.Contains(result.Message, "discarded partial merge changes") {
		t.Fatalf("MergeCleanlyTransactional() message = %q, want scratch hook cleanup detail", result.Message)
	}
	if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != originalHead {
		t.Fatalf("HEAD = %s, want target unchanged at %s", head, originalHead)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("feature.txt stat err = %v, want target untouched", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "commit-msg-created.txt")); !os.IsNotExist(err) {
		t.Fatalf("commit-msg-created.txt stat err = %v, want scratch hook output isolated from target", err)
	}

	status, err := client.Status(context.Background(), repo)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("status = %+v, want clean target after scratch merge failure", status)
	}
}

func TestRecoverIntegrationJournalCompletesInterruptedFinalReset(t *testing.T) {
	repo := initDivergedRepo(t)
	client := NewClient(NewExecRunner(repo), slog.Default())
	ctx := context.Background()
	targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
	desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "scratch merge")

	if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
		Version:        integrationJournalVersion,
		TargetWorktree: repo,
		TargetHead:     targetHead,
		DesiredHead:    desiredHead,
		StartedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("writeIntegrationJournal() error = %v", err)
	}
	runClientTestGit(t, repo, "reset", "--soft", desiredHead)
	if status, err := client.Status(ctx, repo); err != nil {
		t.Fatalf("Status() before recovery error = %v", err)
	} else if !status.HasChanges {
		t.Fatalf("status before recovery = %+v, want simulated interrupted reset to look dirty", status)
	}

	if err := client.RecoverIntegrationJournal(ctx, repo); err != nil {
		t.Fatalf("RecoverIntegrationJournal() error = %v", err)
	}
	if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != desiredHead {
		t.Fatalf("HEAD = %s, want recovered desired head %s", head, desiredHead)
	}
	status, err := client.Status(ctx, repo)
	if err != nil {
		t.Fatalf("Status() after recovery error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("status after recovery = %+v, want clean target", status)
	}
	journalPath, err := client.integrationJournalPath(ctx, repo)
	if err != nil {
		t.Fatalf("integrationJournalPath() error = %v", err)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("journal stat err = %v, want removed", err)
	}
}

func TestMergeCleanlyTransactionalRecoversDirtyFinalApplyAndRemovesJournal(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	var scratchWorktree string
	targetStatusReads := 0
	scratchStatusReads := 0
	runner := &rawMockRunner{runFunc: func(ctx context.Context, args ...string) (string, error) {
		switch {
		case clientTestArgsForWorktree(args, repo, "status", "--porcelain"):
			targetStatusReads++
			if targetStatusReads >= 3 {
				return "?? user-created.txt\n", nil
			}
			return "", nil
		case clientTestArgsForWorktree(args, repo, "rev-parse", "--verify", "HEAD"):
			if targetStatusReads >= 3 {
				return "desired-sha", nil
			}
			return "target-sha", nil
		case clientTestArgsForWorktree(args, repo, "rev-parse", "--git-common-dir"):
			return filepath.Join(repo, ".git"), nil
		case len(args) >= 7 &&
			args[0] == "-C" &&
			args[1] == repo &&
			args[2] == "worktree" &&
			args[3] == "add" &&
			args[4] == "--detach":
			scratchWorktree = args[5]
			if args[6] != "target-sha" {
				t.Fatalf("worktree add ref = %q, want target-sha", args[6])
			}
			return "", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, scratchWorktree, "status", "--porcelain"):
			scratchStatusReads++
			return "", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, scratchWorktree, "merge", "--no-edit", "feature"):
			return "Merge made by the 'ort' strategy.", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, scratchWorktree, "rev-parse", "--verify", "HEAD"):
			return "desired-sha", nil
		case clientTestArgsForWorktree(args, repo, "reset", "--hard", "desired-sha"):
			return "", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, repo, "worktree", "remove", "--force", scratchWorktree):
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
	}}

	client := NewClient(runner, slog.Default())
	result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("MergeCleanlyTransactional() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergeCleanlyTransactional() result = nil")
	}
	if result.Success {
		t.Fatalf("MergeCleanlyTransactional() result = %+v, want dirty final apply failure", result)
	}
	if !strings.Contains(result.Message, "left target dirty after recovery") ||
		!strings.Contains(result.Message, "user-created.txt") {
		t.Fatalf("MergeCleanlyTransactional() message = %q, want recovery dirty detail", result.Message)
	}
	if scratchStatusReads != 3 {
		t.Fatalf("scratch status reads = %d, want 3", scratchStatusReads)
	}
	journalPath, err := client.integrationJournalPath(context.Background(), repo)
	if err != nil {
		t.Fatalf("integrationJournalPath() error = %v", err)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("journal stat err = %v, want removed", err)
	}
}

func TestMergeCleanlyTransactionalAllowsConcurrentScratchValidationAndRejectsStaleFinalApply(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	ctx := context.Background()
	var (
		mu               sync.Mutex
		currentHead      = "target-sha"
		scratchBranches  = map[string]string{}
		resetRefs        []string
		scratchEntered   = make(chan string, 2)
		releaseScratch   = make(chan struct{})
		firstApplyDone   = make(chan struct{})
		firstApplyDoneMu sync.Once
	)
	runner := &rawMockRunner{runFunc: func(ctx context.Context, args ...string) (string, error) {
		switch {
		case clientTestArgsForWorktree(args, repo, "rev-parse", "--git-common-dir"):
			return gitDir, nil
		case clientTestArgsForWorktree(args, repo, "status", "--porcelain"):
			return "", nil
		case clientTestArgsForWorktree(args, repo, "rev-parse", "--verify", "HEAD"):
			mu.Lock()
			defer mu.Unlock()
			return currentHead, nil
		case len(args) >= 7 &&
			args[0] == "-C" &&
			args[1] == repo &&
			args[2] == "worktree" &&
			args[3] == "add" &&
			args[4] == "--detach":
			if args[6] != "target-sha" {
				t.Fatalf("worktree add ref = %q, want target-sha", args[6])
			}
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && strings.HasPrefix(args[1], os.TempDir()) && args[2] == "status" && args[3] == "--porcelain":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && strings.HasPrefix(args[1], os.TempDir()) && args[2] == "merge" && args[3] == "--no-edit":
			scratch := args[1]
			branch := args[4]
			mu.Lock()
			scratchBranches[scratch] = branch
			mu.Unlock()
			scratchEntered <- branch
			<-releaseScratch
			if branch == "feature-b" {
				<-firstApplyDone
			}
			return "Merge made by the 'ort' strategy.", nil
		case len(args) >= 5 && args[0] == "-C" && strings.HasPrefix(args[1], os.TempDir()) && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			mu.Lock()
			branch := scratchBranches[args[1]]
			mu.Unlock()
			if branch == "" {
				t.Fatalf("unknown scratch worktree %q", args[1])
			}
			return "desired-" + branch, nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repo && args[2] == "reset" && args[3] == "--hard":
			mu.Lock()
			currentHead = args[4]
			resetRefs = append(resetRefs, args[4])
			mu.Unlock()
			firstApplyDoneMu.Do(func() { close(firstApplyDone) })
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repo && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
	}}

	client := NewClient(runner, slog.Default())
	type mergeOutcome struct {
		branch string
		result *MergeResult
		err    error
	}
	outcomes := make(chan mergeOutcome, 2)
	for _, branch := range []string{"feature-a", "feature-b"} {
		branch := branch
		go func() {
			result, err := client.MergeCleanlyTransactional(ctx, repo, branch)
			outcomes <- mergeOutcome{branch: branch, result: result, err: err}
		}()
	}

	entered := map[string]bool{}
	for len(entered) < 2 {
		select {
		case branch := <-scratchEntered:
			entered[branch] = true
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for both scratch validations; entered=%v", entered)
		}
	}
	close(releaseScratch)

	got := map[string]mergeOutcome{}
	for len(got) < 2 {
		select {
		case outcome := <-outcomes:
			got[outcome.branch] = outcome
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for merge outcomes; got=%v", got)
		}
	}

	first := got["feature-a"]
	if first.err != nil {
		t.Fatalf("feature-a error = %v", first.err)
	}
	if first.result == nil || !first.result.Success {
		t.Fatalf("feature-a result = %+v, want success", first.result)
	}
	second := got["feature-b"]
	if second.err != nil {
		t.Fatalf("feature-b error = %v", second.err)
	}
	if second.result == nil || second.result.Success {
		t.Fatalf("feature-b result = %+v, want stale-base failure", second.result)
	}
	if !strings.Contains(second.result.Message, "target HEAD moved from target-sha to desired-feature-a") ||
		!strings.Contains(second.result.Message, "retry integration") {
		t.Fatalf("feature-b message = %q, want stale-base retry detail", second.result.Message)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(resetRefs, []string{"desired-feature-a"}) {
		t.Fatalf("reset refs = %v, want only first final apply", resetRefs)
	}
}

func TestClientWorktreeLockSerializesSameWorktree(t *testing.T) {
	client := NewClient(&mockRunner{}, slog.Default())
	ctx := context.Background()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.WithWorktreeLock(ctx, "/tmp/same-worktree", func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- client.WithWorktreeLock(ctx, "/tmp/same-worktree", func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("second lock entered while first lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock error = %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second lock did not enter after first lock released")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second lock error = %v", err)
	}
}

func TestWorktreeLockReleasesWaitersFIFO(t *testing.T) {
	lock := newWorktreeLock()
	ctx := context.Background()
	unlockFirst, err := lock.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}

	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		unlock, err := lock.acquire(ctx)
		if err != nil {
			secondDone <- err
			return
		}
		close(secondEntered)
		<-releaseSecond
		unlock()
		secondDone <- nil
	}()
	waitForWorktreeLockWaiters(t, lock, 1)

	thirdEntered := make(chan struct{})
	thirdDone := make(chan error, 1)
	go func() {
		unlock, err := lock.acquire(ctx)
		if err != nil {
			thirdDone <- err
			return
		}
		close(thirdEntered)
		unlock()
		thirdDone <- nil
	}()
	waitForWorktreeLockWaiters(t, lock, 2)

	unlockFirst()
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second waiter did not enter after first release")
	}
	select {
	case <-thirdEntered:
		t.Fatal("third waiter entered before second released")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSecond)
	if err := <-secondDone; err != nil {
		t.Fatalf("second waiter error = %v", err)
	}
	select {
	case <-thirdEntered:
	case <-time.After(time.Second):
		t.Fatal("third waiter did not enter after second release")
	}
	if err := <-thirdDone; err != nil {
		t.Fatalf("third waiter error = %v", err)
	}
}

func TestWorktreeLockRemovesCanceledWaiter(t *testing.T) {
	lock := newWorktreeLock()
	ctx := context.Background()
	unlockFirst, err := lock.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}

	waitCtx, cancelWait := context.WithCancel(ctx)
	waiterDone := make(chan error, 1)
	go func() {
		unlock, err := lock.acquire(waitCtx)
		if unlock != nil {
			unlock()
		}
		waiterDone <- err
	}()
	waitForWorktreeLockWaiters(t, lock, 1)
	cancelWait()
	if err := <-waiterDone; err == nil {
		t.Fatal("canceled waiter error = nil, want context cancellation")
	}
	waitForWorktreeLockWaiters(t, lock, 0)

	nextEntered := make(chan struct{})
	nextDone := make(chan error, 1)
	go func() {
		unlock, err := lock.acquire(ctx)
		if err != nil {
			nextDone <- err
			return
		}
		close(nextEntered)
		unlock()
		nextDone <- nil
	}()
	waitForWorktreeLockWaiters(t, lock, 1)
	unlockFirst()
	select {
	case <-nextEntered:
	case <-time.After(time.Second):
		t.Fatal("next waiter did not enter after canceled waiter was removed")
	}
	if err := <-nextDone; err != nil {
		t.Fatalf("next waiter error = %v", err)
	}
}

func TestWorktreeLockRejectsAlreadyCanceledContext(t *testing.T) {
	lock := newWorktreeLock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unlock, err := lock.acquire(ctx)
	if err == nil {
		if unlock != nil {
			unlock()
		}
		t.Fatal("acquire with canceled context error = nil")
	}
	lock.mu.Lock()
	held := lock.held
	waiters := len(lock.waiters)
	lock.mu.Unlock()
	if held || waiters != 0 {
		t.Fatalf("lock state after canceled acquire: held=%t waiters=%d, want free", held, waiters)
	}
}

func waitForWorktreeLockWaiters(t *testing.T, lock *worktreeLock, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		lock.mu.Lock()
		got := len(lock.waiters)
		lock.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	lock.mu.Lock()
	got := len(lock.waiters)
	lock.mu.Unlock()
	t.Fatalf("worktree lock waiters = %d, want %d", got, want)
}

func TestNormalizeWorktreeLockKeyCanonicalizesSymlink(t *testing.T) {
	root := t.TempDir()
	realPath := filepath.Join(root, "real")
	if err := os.Mkdir(realPath, 0o755); err != nil {
		t.Fatalf("mkdir real path: %v", err)
	}
	linkPath := filepath.Join(root, "link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if got, want := normalizeWorktreeLockKey(linkPath), normalizeWorktreeLockKey(realPath); got != want {
		t.Fatalf("normalizeWorktreeLockKey(link) = %q, want %q", got, want)
	}
}

func TestMergeCleanlyDiscardsDirtyTargetAfterKilledMergeCommand(t *testing.T) {
	var calls []string
	statusCalls := 0
	ctx, cancel := context.WithCancel(context.Background())
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			switch {
			case len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain":
				statusCalls++
				if statusCalls == 1 {
					return "", nil
				}
				if ctx.Err() != nil {
					return "", fmt.Errorf("cleanup status used canceled context: %w", ctx.Err())
				}
				return "M  internal/tui/model.go\nM  internal/tui/model_daemonclient_test.go", nil
			case len(args) >= 3 && args[0] == "merge" && args[1] == "--no-edit" && args[2] == "feature-branch":
				cancel()
				return "[gate] build+tests passed", fmt.Errorf("signal: killed")
			case len(args) >= 4 && args[0] == "rev-parse" && args[1] == "-q" && args[2] == "--verify" && args[3] == "MERGE_HEAD":
				return "", fmt.Errorf("exit status 1")
			case len(args) >= 4 && args[0] == "restore" && args[1] == "--staged" && args[2] == "--worktree" && args[3] == ".":
				if ctx.Err() != nil {
					return "", fmt.Errorf("cleanup restore used canceled context: %w", ctx.Err())
				}
				return "", nil
			case len(args) >= 2 && args[0] == "clean" && args[1] == "-fd":
				if ctx.Err() != nil {
					return "", fmt.Errorf("cleanup clean used canceled context: %w", ctx.Err())
				}
				return "", nil
			default:
				return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
			}
		},
	}

	client := NewClient(runner, slog.Default())
	result, err := client.MergeCleanly(ctx, "/fake/worktree", "feature-branch")
	if err != nil {
		t.Fatalf("MergeCleanly() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergeCleanly() result = nil")
	}
	if result.Success {
		t.Fatalf("MergeCleanly() result = %+v, want failed merge result", result)
	}
	if result.HasConflicts {
		t.Fatalf("MergeCleanly() result = %+v, want non-conflict failure", result)
	}
	if !strings.Contains(result.Message, "signal: killed") ||
		!strings.Contains(result.Message, "discarded partial merge changes") ||
		!strings.Contains(result.Message, "internal/tui/model.go") ||
		!strings.Contains(result.Message, "internal/tui/model_daemonclient_test.go") {
		t.Fatalf("MergeCleanly() message = %q, want killed merge cleanup detail", result.Message)
	}
	compareStringSlices(t, "calls", calls, []string{
		"status --porcelain",
		"merge --no-edit feature-branch",
		"rev-parse -q --verify MERGE_HEAD",
		"rev-parse -q --verify MERGE_HEAD",
		"status --porcelain",
		"restore --staged --worktree .",
		"clean -fd",
	})
}

func TestMergeCleanlyAbortsIncompleteMergeWithDetachedCleanupContext(t *testing.T) {
	var calls []string
	ctx, cancel := context.WithCancel(context.Background())
	mergeHeadChecks := 0
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			switch {
			case len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain":
				if ctx.Err() != nil {
					return "", fmt.Errorf("cleanup status used canceled context: %w", ctx.Err())
				}
				return "", nil
			case len(args) >= 3 && args[0] == "merge" && args[1] == "--no-edit" && args[2] == "feature-branch":
				cancel()
				return "[gate] running test check", fmt.Errorf("signal: killed")
			case len(args) >= 4 && args[0] == "rev-parse" && args[1] == "-q" && args[2] == "--verify" && args[3] == "MERGE_HEAD":
				mergeHeadChecks++
				if mergeHeadChecks == 1 {
					if ctx.Err() == nil {
						return "", fmt.Errorf("initial merge probe should use canceled context")
					}
					return "", ctx.Err()
				}
				if ctx.Err() != nil {
					return "", fmt.Errorf("cleanup merge probe used canceled context: %w", ctx.Err())
				}
				return "abcdef", nil
			case len(args) >= 2 && args[0] == "merge" && args[1] == "--abort":
				if ctx.Err() != nil {
					return "", fmt.Errorf("cleanup abort used canceled context: %w", ctx.Err())
				}
				return "", nil
			default:
				return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
			}
		},
	}

	client := NewClient(runner, slog.Default())
	result, err := client.MergeCleanly(ctx, "/fake/worktree", "feature-branch")
	if err != nil {
		t.Fatalf("MergeCleanly() error = %v", err)
	}
	if result == nil {
		t.Fatal("MergeCleanly() result = nil")
	}
	if result.Success || result.HasConflicts {
		t.Fatalf("MergeCleanly() result = %+v, want non-conflict failure", result)
	}
	if !strings.Contains(result.Message, "signal: killed") ||
		!strings.Contains(result.Message, "aborted incomplete merge during cleanup") {
		t.Fatalf("MergeCleanly() message = %q, want detached abort cleanup detail", result.Message)
	}
	compareStringSlices(t, "calls", calls, []string{
		"status --porcelain",
		"merge --no-edit feature-branch",
		"rev-parse -q --verify MERGE_HEAD",
		"rev-parse -q --verify MERGE_HEAD",
		"merge --abort",
		"status --porcelain",
	})
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

func TestDiffStatAgainstBaseBranchBacksOffMissingMergeBaseAndSkipsFallback(t *testing.T) {
	now := time.Date(2026, time.July, 7, 12, 0, 0, 0, time.UTC)
	mergeBaseCalls := 0
	localDiffCalls := 0
	unstagedStat := "1 file changed, 3 insertions(+)"
	stagedStat := "1 file changed, 1 deletion(-)"
	expectedStat := unstagedStat + "\n" + stagedStat

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			switch {
			case len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" && args[2] == "refs/remotes/origin/HEAD":
				return "", fmt.Errorf("origin HEAD unavailable")
			case len(args) >= 3 && args[0] == "merge-base" && args[2] == "HEAD":
				mergeBaseCalls++
				return "", fmt.Errorf("unknown revision %s", args[1])
			case len(args) >= 3 && args[0] == "diff" && args[1] == "--cached" && args[2] == "--shortstat":
				localDiffCalls++
				return stagedStat, nil
			case len(args) >= 2 && args[0] == "diff" && args[1] == "--shortstat":
				localDiffCalls++
				return unstagedStat, nil
			default:
				return "", fmt.Errorf("unexpected command: %v", args)
			}
		},
	}

	client := NewClient(runner, slog.Default())
	client.now = func() time.Time { return now }

	stat, err := client.DiffStat(context.Background(), "/fake/worktree", "preview")
	if err != nil {
		t.Fatalf("first DiffStat() error = %v", err)
	}
	if stat != expectedStat {
		t.Fatalf("first DiffStat() = %v, want %v", stat, expectedStat)
	}
	if mergeBaseCalls != 2 {
		t.Fatalf("merge-base calls after first DiffStat = %d, want 2", mergeBaseCalls)
	}
	if localDiffCalls != 2 {
		t.Fatalf("local diff calls after first DiffStat = %d, want 2", localDiffCalls)
	}

	_, err = client.DiffStat(context.Background(), "/fake/worktree", "preview")
	if err == nil {
		t.Fatal("second DiffStat() error = nil, want backoff error")
	}
	if _, ok := diffStatBackoffErr(err); !ok {
		t.Fatalf("second DiffStat() error = %T %[1]v, want diffStatBackoffError", err)
	}
	if mergeBaseCalls != 2 {
		t.Fatalf("merge-base calls after backoff DiffStat = %d, want 2", mergeBaseCalls)
	}
	if localDiffCalls != 2 {
		t.Fatalf("local diff calls after backoff DiffStat = %d, want 2", localDiffCalls)
	}

	now = now.Add(diffStatFailureBackoff + time.Second)
	stat, err = client.DiffStat(context.Background(), "/fake/worktree", "preview")
	if err != nil {
		t.Fatalf("third DiffStat() after backoff error = %v", err)
	}
	if stat != expectedStat {
		t.Fatalf("third DiffStat() = %v, want %v", stat, expectedStat)
	}
	if mergeBaseCalls != 4 {
		t.Fatalf("merge-base calls after expired backoff = %d, want 4", mergeBaseCalls)
	}
	if localDiffCalls != 4 {
		t.Fatalf("local diff calls after expired backoff = %d, want 4", localDiffCalls)
	}
}

func TestDiffStatAgainstBaseBranchBacksOffKilledBaseShortstatAndSkipsFallback(t *testing.T) {
	now := time.Date(2026, time.July, 7, 12, 0, 0, 0, time.UTC)
	baseDiffCalls := 0
	localDiffCalls := 0
	sawBaseDeadline := false
	unstagedStat := "1 file changed, 2 insertions(+)"
	stagedStat := "1 file changed, 1 deletion(-)"
	expectedStat := unstagedStat + "\n" + stagedStat

	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			switch {
			case len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD":
				return "abc123\n", nil
			case len(args) >= 6 && args[0] == "diff" && args[1] == "--shortstat" && args[2] == "abc123" && args[3] == "HEAD":
				baseDiffCalls++
				if _, ok := ctx.Deadline(); ok {
					sawBaseDeadline = true
				}
				return "", fmt.Errorf("signal: killed")
			case len(args) >= 3 && args[0] == "diff" && args[1] == "--cached" && args[2] == "--shortstat":
				localDiffCalls++
				return stagedStat, nil
			case len(args) >= 2 && args[0] == "diff" && args[1] == "--shortstat":
				localDiffCalls++
				return unstagedStat, nil
			default:
				return "", fmt.Errorf("unexpected command: %v", args)
			}
		},
	}

	client := NewClient(runner, slog.Default())
	client.now = func() time.Time { return now }

	stat, err := client.DiffStat(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("first DiffStat() error = %v", err)
	}
	if stat != expectedStat {
		t.Fatalf("first DiffStat() = %v, want %v", stat, expectedStat)
	}
	if baseDiffCalls != 1 {
		t.Fatalf("base diff calls after first DiffStat = %d, want 1", baseDiffCalls)
	}
	if !sawBaseDeadline {
		t.Fatal("base diff did not run with a context deadline")
	}
	if localDiffCalls != 2 {
		t.Fatalf("local diff calls after first DiffStat = %d, want 2", localDiffCalls)
	}

	_, err = client.DiffStat(context.Background(), "/fake/worktree", "main")
	if err == nil {
		t.Fatal("second DiffStat() error = nil, want shortstat backoff error")
	}
	if _, ok := diffStatBackoffErr(err); !ok {
		t.Fatalf("second DiffStat() error = %T %[1]v, want diffStatBackoffError", err)
	}
	if baseDiffCalls != 1 {
		t.Fatalf("base diff calls after backoff DiffStat = %d, want 1", baseDiffCalls)
	}
	if localDiffCalls != 2 {
		t.Fatalf("local diff calls after backoff DiffStat = %d, want 2", localDiffCalls)
	}
}

func TestLocalDiffStatFallbackUsesTimeoutAndBacksOffFailure(t *testing.T) {
	now := time.Date(2026, time.July, 7, 12, 0, 0, 0, time.UTC)
	fallbackCalls := 0
	sawDeadline := false
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "diff" && args[1] == "--shortstat" {
				fallbackCalls++
				if _, ok := ctx.Deadline(); ok {
					sawDeadline = true
				}
				return "", context.DeadlineExceeded
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	client.now = func() time.Time { return now }

	_, err := client.localDiffStat(context.Background(), "/fake/worktree", "preview", true)
	if err == nil {
		t.Fatal("localDiffStat() error = nil, want timeout failure")
	}
	if !sawDeadline {
		t.Fatal("fallback diff did not run with a context deadline")
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls after first localDiffStat = %d, want 1", fallbackCalls)
	}

	_, err = client.localDiffStat(context.Background(), "/fake/worktree", "preview", true)
	if err == nil {
		t.Fatal("second localDiffStat() error = nil, want backoff error")
	}
	if _, ok := diffStatBackoffErr(err); !ok {
		t.Fatalf("second localDiffStat() error = %T %[1]v, want diffStatBackoffError", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls after backoff localDiffStat = %d, want 1", fallbackCalls)
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

func TestBranchAheadBehindPrefersLocalBaseWhenOriginHeadDiffers(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" && args[2] == "refs/remotes/origin/HEAD" {
				return "origin/preview\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "HEAD..main" {
				return "5\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "main..HEAD" {
				return "2\n", nil
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "HEAD..origin/preview" {
				return "", fmt.Errorf("should not use origin/HEAD before local main")
			}
			if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "origin/preview..HEAD" {
				return "", fmt.Errorf("should not use origin/HEAD before local main")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	ahead, behind, err := client.BranchAheadBehind(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("BranchAheadBehind() error = %v", err)
	}
	if ahead != 2 || behind != 5 {
		t.Fatalf("BranchAheadBehind() = %d/%d, want 2/5", ahead, behind)
	}
}

func TestBranchAheadBehindWithRemoteBasePreferencePrefersOriginHeadWhenBaseIsGeneric(t *testing.T) {
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
				return "", fmt.Errorf("should not use local main before origin/HEAD")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	ahead, behind, err := client.BranchAheadBehindWithBasePreference(context.Background(), "/fake/worktree", "main", true)
	if err != nil {
		t.Fatalf("BranchAheadBehindWithBasePreference() error = %v", err)
	}
	if ahead != 3 || behind != 7 {
		t.Fatalf("BranchAheadBehindWithBasePreference() = %d/%d, want 3/7", ahead, behind)
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

func TestRuntimeStatusUsesLocalBaseBeforeCurrentRemoteBase(t *testing.T) {
	repo := t.TempDir()
	runClientTestGit(t, repo, "init", "-q", "-b", "preview")
	runClientTestGit(t, repo, "config", "user.email", "test@example.com")
	runClientTestGit(t, repo, "config", "user.name", "Test User")
	runClientTestGit(t, repo, "config", "remote.origin.url", "https://example.invalid/repo.git")
	runClientTestGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/preview")

	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runClientTestGit(t, repo, "add", "base.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "base")
	runClientTestGit(t, repo, "update-ref", "refs/remotes/origin/preview", "HEAD")

	runClientTestGit(t, repo, "checkout", "-q", "-b", "issue")
	if err := os.WriteFile(filepath.Join(repo, "issue.txt"), []byte("issue\n"), 0o644); err != nil {
		t.Fatalf("write issue file: %v", err)
	}
	runClientTestGit(t, repo, "add", "issue.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "issue")
	issueHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	remoteBaseHead := runClientTestGitOutput(t, repo, "commit-tree", "HEAD^{tree}", "-p", issueHead, "-m", "base contains issue")

	runClientTestGit(t, repo, "update-ref", "refs/remotes/origin/preview", remoteBaseHead)

	client := NewClient(NewExecRunner(repo), slog.Default())
	status, err := client.RuntimeStatus(context.Background(), repo, "preview")
	if err != nil {
		t.Fatalf("RuntimeStatus() error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("RuntimeStatus() dirty = true, want clean: %+v", status)
	}
	if status.GitAdditions != 1 || status.GitDeletions != 0 {
		t.Fatalf("RuntimeStatus() diff totals = %d/%d, want 1/0 from local base", status.GitAdditions, status.GitDeletions)
	}
	if status.GitAheadCount != 1 || status.GitBehindCount != 0 {
		t.Fatalf("RuntimeStatus() ahead/behind = %d/%d, want 1/0 from local base", status.GitAheadCount, status.GitBehindCount)
	}
}

func TestRuntimeStatusWithRemoteBasePreferenceUsesCurrentRemoteBase(t *testing.T) {
	repo := t.TempDir()
	runClientTestGit(t, repo, "init", "-q", "-b", "preview")
	runClientTestGit(t, repo, "config", "user.email", "test@example.com")
	runClientTestGit(t, repo, "config", "user.name", "Test User")
	runClientTestGit(t, repo, "config", "remote.origin.url", "https://example.invalid/repo.git")
	runClientTestGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/preview")

	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runClientTestGit(t, repo, "add", "base.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "base")
	runClientTestGit(t, repo, "update-ref", "refs/remotes/origin/preview", "HEAD")

	runClientTestGit(t, repo, "checkout", "-q", "-b", "issue")
	if err := os.WriteFile(filepath.Join(repo, "issue.txt"), []byte("issue\n"), 0o644); err != nil {
		t.Fatalf("write issue file: %v", err)
	}
	runClientTestGit(t, repo, "add", "issue.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "issue")
	issueHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	remoteBaseHead := runClientTestGitOutput(t, repo, "commit-tree", "HEAD^{tree}", "-p", issueHead, "-m", "base contains issue")

	runClientTestGit(t, repo, "update-ref", "refs/remotes/origin/preview", remoteBaseHead)

	client := NewClient(NewExecRunner(repo), slog.Default())
	status, err := client.RuntimeStatusWithBasePreference(context.Background(), repo, "preview", true)
	if err != nil {
		t.Fatalf("RuntimeStatusWithBasePreference() error = %v", err)
	}
	if status.HasChanges {
		t.Fatalf("RuntimeStatusWithBasePreference() dirty = true, want clean: %+v", status)
	}
	if status.GitAdditions != 0 || status.GitDeletions != 0 {
		t.Fatalf("RuntimeStatusWithBasePreference() diff totals = %d/%d, want 0/0 from remote base", status.GitAdditions, status.GitDeletions)
	}
	if status.GitAheadCount != 0 || status.GitBehindCount != 1 {
		t.Fatalf("RuntimeStatusWithBasePreference() ahead/behind = %d/%d, want 0/1 from remote base", status.GitAheadCount, status.GitBehindCount)
	}
}

func TestMergeBaseFallsBackToOriginRef(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "origin/main" && args[2] == "HEAD" {
				return "def456\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "", fmt.Errorf("unknown revision")
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

func TestMergeBasePrefersLocalBaseWhenOriginHeadDiffers(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" && args[2] == "refs/remotes/origin/HEAD" {
				return "origin/preview\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "abc123\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "origin/preview" && args[2] == "HEAD" {
				return "", fmt.Errorf("should not use origin/HEAD before local main")
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

func TestMergeBaseWithRemoteBasePreferencePrefersOriginHeadWhenBaseIsGeneric(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" && args[2] == "refs/remotes/origin/HEAD" {
				return "origin/preview\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "origin/preview" && args[2] == "HEAD" {
				return "fedcba\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "", fmt.Errorf("should not use local main before origin/HEAD")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	mergeBase, err := client.mergeBase(context.Background(), "/fake/worktree", "main", true)
	if err != nil {
		t.Fatalf("mergeBase() error = %v", err)
	}
	if mergeBase != "fedcba" {
		t.Fatalf("mergeBase() = %q, want fedcba", mergeBase)
	}
}

func TestMergeBaseNonGenericBaseDoesNotFallbackToMainOrMaster(t *testing.T) {
	var attempts []string
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" && args[2] == "refs/remotes/origin/HEAD" {
				return "origin/main\n", nil
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[2] == "HEAD" {
				attempts = append(attempts, args[1])
				switch args[1] {
				case "preview", "origin/preview":
					return "", fmt.Errorf("unknown revision %s", args[1])
				case "main", "origin/main", "master", "origin/master":
					return "", fmt.Errorf("should not fallback to %s for explicit preview base", args[1])
				default:
					return "", fmt.Errorf("unexpected merge-base ref %s", args[1])
				}
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	_, err := client.MergeBase(context.Background(), "/fake/worktree", "preview")
	if err == nil {
		t.Fatal("MergeBase() expected error")
	}
	if strings.Contains(err.Error(), "should not fallback") {
		t.Fatalf("MergeBase() used unrelated fallback refs: %v; attempts=%v", err, attempts)
	}
	wantAttempts := []string{"preview", "origin/preview"}
	if !reflect.DeepEqual(attempts, wantAttempts) {
		t.Fatalf("merge-base attempts = %v, want %v", attempts, wantAttempts)
	}
	if !strings.Contains(err.Error(), "preview") || !strings.Contains(err.Error(), "origin/preview") {
		t.Fatalf("MergeBase() error = %v, want attempted preview refs", err)
	}
	if strings.Contains(err.Error(), "origin/master") {
		t.Fatalf("MergeBase() error = %v, should not mention origin/master", err)
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
				args[3] == "HEAD" &&
				args[4] == "--" {
				return "M\tinternal/tui/model.go\nM\t.azedarach/config.json\nA\tnew.go\nD\told.go\nR100\tfrom.go\tto.go", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	files, err := client.ChangedFiles(context.Background(), "/fake/worktree", "main")
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	if len(files) != 5 {
		t.Fatalf("ChangedFiles() len = %d, want 5", len(files))
	}

	if files[0].Path != "internal/tui/model.go" || files[0].Status != DiffFileModified {
		t.Fatalf("first file = %+v", files[0])
	}
	if files[1].Path != ".azedarach/config.json" || files[1].Status != DiffFileModified {
		t.Fatalf("second file = %+v", files[1])
	}
	if files[2].Path != "new.go" || files[2].Status != DiffFileAdded {
		t.Fatalf("third file = %+v", files[2])
	}
	if files[3].Path != "old.go" || files[3].Status != DiffFileDeleted {
		t.Fatalf("fourth file = %+v", files[3])
	}
	if files[4].OldPath != "from.go" || files[4].Path != "to.go" || files[4].Status != DiffFileRenamed {
		t.Fatalf("fifth file = %+v", files[4])
	}
}

func TestChangedFilesLocalBaseDoesNotFallBackToRemote(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "main" && args[2] == "HEAD" {
				return "", fmt.Errorf("unknown revision")
			}
			if len(args) >= 3 && args[0] == "merge-base" && args[1] == "origin/main" && args[2] == "HEAD" {
				return "", fmt.Errorf("should not use remote base branch")
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	_, err := client.ChangedFilesLocalBase(context.Background(), "/fake/worktree", "main")
	if err == nil {
		t.Fatal("ChangedFilesLocalBase() expected error")
	}
	if strings.Contains(err.Error(), "should not use remote base branch") {
		t.Fatalf("ChangedFilesLocalBase() attempted remote fallback: %v", err)
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
