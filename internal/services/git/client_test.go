package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
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

func installPassingIntegrationGate(t *testing.T, repo string) {
	t.Helper()
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write integration gate: %v", err)
	}
	runClientTestGit(t, repo, "add", "scripts/git-merge-rebase-gate.sh")
	runClientTestGit(t, repo, "commit", "-q", "-m", "add integration gate")
}

func addOwnedIntegrationScratch(t *testing.T, client *Client, repo, head string) (string, integrationScratchOwnership) {
	t.Helper()
	scratch, err := os.MkdirTemp("", "azedarach-integration-")
	if err != nil {
		t.Fatalf("create scratch path: %v", err)
	}
	if err := os.Remove(scratch); err != nil {
		t.Fatalf("prepare scratch worktree: %v", err)
	}
	runClientTestGit(t, repo, "worktree", "add", "--detach", scratch, head)
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", scratch)
		cmd.Dir = repo
		_ = cmd.Run()
		_ = os.RemoveAll(scratch)
	})
	owner, err := client.createIntegrationScratchOwnership(context.Background(), repo, scratch)
	if err != nil {
		t.Fatalf("createIntegrationScratchOwnership() error = %v", err)
	}
	return scratch, owner
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

func TestParseGitStatusMarksEveryTrackedPorcelainStateDirty(t *testing.T) {
	for _, porcelain := range []string{
		" T type-changed.txt\n",
		"M  staged.txt\n",
		" m submodule-with-worktree-change\n",
		"UU conflicted.txt\n",
	} {
		status := parseGitStatus(porcelain)
		if !status.hasTrackedChanges || !gitStatusHasTrackedChanges(status) || !status.HasChanges {
			t.Fatalf("parseGitStatus(%q) = %+v, want tracked dirty state", porcelain, status)
		}
	}
	untracked := parseGitStatus("?? untracked.txt\n")
	if untracked.hasTrackedChanges || gitStatusHasTrackedChanges(untracked) {
		t.Fatalf("untracked status = %+v, want no tracked changes", untracked)
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

func TestRealProcessProfileMergePreservesCommitHooksAndAbortsIncompleteMerge(t *testing.T) {
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

func TestRealProcessProfileMergeReportsSlowHookDiagnostics(t *testing.T) {
	repo := initDivergedRepo(t)
	hooks := map[string]string{
		"commit-msg": "#!/bin/sh\nsleep 0.05\nexit 0\n",
		"post-merge": "#!/bin/sh\nsleep 0.04\nexit 0\n",
	}
	for hookName, body := range hooks {
		hookPath := filepath.Join(repo, ".git", "hooks", hookName)
		if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
			t.Fatalf("write hook %s: %v", hookName, err)
		}
	}

	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.Merge(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("Merge() result = %+v, want success", result)
	}
	if len(result.HookDiagnostics) != 2 {
		t.Fatalf("hook diagnostics = %+v, want two entries", result.HookDiagnostics)
	}
	byHook := map[string]GitHookDiagnostic{}
	for _, diag := range result.HookDiagnostics {
		byHook[diag.Hook] = diag
		if diag.Command != "git merge --..." {
			t.Fatalf("command = %q, want sanitized git merge command shape", diag.Command)
		}
		if strings.Contains(diag.Command, "feature") || strings.Contains(diag.Command, repo) {
			t.Fatalf("command = %q, must not contain branch or path", diag.Command)
		}
		if diag.ExitStatus != 0 || !diag.Blocking || diag.TimedOut {
			t.Fatalf("hook diagnostic = %+v, want exit 0 blocking without timeout", diag)
		}
	}
	if byHook["commit-msg"].ElapsedMS < 40 {
		t.Fatalf("commit-msg elapsed_ms = %d, want slow hook attribution", byHook["commit-msg"].ElapsedMS)
	}
	if byHook["post-merge"].ElapsedMS < 30 {
		t.Fatalf("post-merge elapsed_ms = %d, want slow hook attribution", byHook["post-merge"].ElapsedMS)
	}
}

func TestMergeCommandContextAddsDefaultTimeout(t *testing.T) {
	ctx, cancel := mergeCommandContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("merge command context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= domain.IntegrationMergeTimeout-time.Second || remaining > domain.IntegrationMergeTimeout {
		t.Fatalf("merge command timeout remaining = %v, want close to %v", remaining, domain.IntegrationMergeTimeout)
	}

	parent, parentCancel := context.WithTimeout(context.Background(), time.Hour)
	defer parentCancel()
	ctx, cancel = mergeCommandContext(parent)
	defer cancel()
	deadline, ok = ctx.Deadline()
	if !ok {
		t.Fatal("merge command context derived from long parent has no deadline")
	}
	remaining = time.Until(deadline)
	if remaining <= domain.IntegrationMergeTimeout-time.Second || remaining > domain.IntegrationMergeTimeout {
		t.Fatalf("merge command timeout with long parent = %v, want close to %v", remaining, domain.IntegrationMergeTimeout)
	}

	shortParent, shortCancel := context.WithTimeout(context.Background(), time.Minute)
	defer shortCancel()
	ctx, cancel = mergeCommandContext(shortParent)
	defer cancel()
	deadline, ok = ctx.Deadline()
	if !ok || time.Until(deadline) > time.Minute {
		t.Fatalf("merge command should preserve shorter parent deadline, deadline=%v ok=%t", deadline, ok)
	}
}

func TestMergeGateBudgetLeavesFinalizationReserve(t *testing.T) {
	if got := domain.IntegrationMergeTimeout - domain.IntegrationValidationTimeout; got < domain.IntegrationFinalizeReserve {
		t.Fatalf("merge finalization reserve = %v, want at least %v", got, domain.IntegrationFinalizeReserve)
	}
	scriptsDir := filepath.Join("..", "..", "..", "scripts")
	gate, err := os.ReadFile(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"))
	if err != nil {
		t.Fatalf("read merge gate: %v", err)
	}
	wantWall := fmt.Sprintf("AZEDARACH_MERGE_GATE_TIMEOUT:-%.0fm", domain.IntegrationValidationTimeout.Minutes())
	if !strings.Contains(string(gate), wantWall) {
		t.Fatalf("merge gate does not enforce shared wall validation budget %q", wantWall)
	}
	if strings.Contains(string(gate), "canonical=true status=passed") ||
		!strings.Contains(string(gate), "canonical=false status=passed awaiting_exact_apply=true") {
		t.Fatal("standalone gate must publish a passed candidate as noncanonical until exact target apply")
	}
	body, err := os.ReadFile(filepath.Join(scriptsDir, "git-merge-rebase-gate-body.sh"))
	if err != nil {
		t.Fatalf("read merge gate body: %v", err)
	}
	wantTest := fmt.Sprintf("go test -timeout %.0fm ./...", domain.IntegrationTestBinaryTimeout.Minutes())
	if !strings.Contains(string(body), wantTest) {
		t.Fatalf("merge gate body does not enforce test binary budget %q", wantTest)
	}
}

func TestMergeGateWallTimeoutRetainsChildOutput(t *testing.T) {
	// A direct synthetic body isolates wrapper timeout/output semantics from
	// task-runner and Go startup scheduling. Use a long safety budget, then
	// trigger GNU timeout only after the synthetic body confirms that it emitted
	// its diagnostic. The separate budget test inspects the production body, and
	// the merge hook executes it end to end.
	const timeoutBudget = "1h"

	timeoutPath, err := exec.LookPath("timeout")
	if err != nil {
		timeoutPath, err = exec.LookPath("gtimeout")
	}
	if err != nil {
		t.Skip("GNU timeout unavailable")
	}
	killPath, err := exec.LookPath("kill")
	if err != nil {
		t.Skip("POSIX kill unavailable")
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "status.showUntrackedFiles", "no")
	runGit(t, repo, "commit", "--allow-empty", "-m", "candidate")
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	sourceScriptsDir := filepath.Join("..", "..", "..", "scripts")
	for _, name := range []string{"git-merge-rebase-gate.sh"} {
		content, readErr := os.ReadFile(filepath.Join(sourceScriptsDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(scriptsDir, name), content, 0o755); writeErr != nil {
			t.Fatalf("write %s: %v", name, writeErr)
		}
	}
	fakeBin := filepath.Join(repo, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	timeoutPIDFile := filepath.Join(repo, "timeout.pid")
	timeoutWrapper := "#!/bin/sh\nprintf '%s\\n' \"$$\" >\"$AZEDARACH_TEST_TIMEOUT_PID_FILE\"\nexec \"$AZEDARACH_TEST_TIMEOUT_PATH\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "timeout"), []byte(timeoutWrapper), 0o755); err != nil {
		t.Fatalf("write timeout wrapper: %v", err)
	}
	childPIDFile := filepath.Join(repo, "timeout-child.pid")
	diagnosticEmittedFile := filepath.Join(repo, "diagnostic-emitted")
	fakeBody := "#!/bin/sh\nsleep 30 &\nchild_pid=$!\nprintf '%s\\n' \"$child_pid\" >\"$AZEDARACH_TEST_CHILD_PID_FILE\"\necho retained-timeout-marker\nprintf 'emitted\\n' >\"$AZEDARACH_TEST_DIAGNOSTIC_EMITTED_FILE\"\nwait \"$child_pid\"\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate-body.sh"), []byte(fakeBody), 0o755); err != nil {
		t.Fatalf("write fake gate body: %v", err)
	}

	cmd := exec.Command(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"))
	cmd.Dir = repo
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"AZEDARACH_MERGE_GATE_TIMEOUT="+timeoutBudget,
		"AZEDARACH_TEST_CHILD_PID_FILE="+childPIDFile,
		"AZEDARACH_TEST_DIAGNOSTIC_EMITTED_FILE="+diagnosticEmittedFile,
		"AZEDARACH_TEST_TIMEOUT_PATH="+timeoutPath,
		"AZEDARACH_TEST_TIMEOUT_PID_FILE="+timeoutPIDFile,
		"PATH="+fakeBin+string(os.PathListSeparator)+filepath.Dir(timeoutPath)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start merge gate: %v", err)
	}
	// Keep the entire gate process tree in a dedicated group.  Readiness and
	// assertion failures must not leave the one-hour timeout (or its children)
	// running after this test exits.
	var runErr error
	done := make(chan struct{})
	cleanup := func() {
		if cmd.Process == nil {
			return
		}
		if syscall.Kill(-cmd.Process.Pid, 0) != nil {
			return
		}
		killProcessGroupWithGrace(-cmd.Process.Pid)
		select {
		case <-done:
		default:
		}
		<-done
	}
	defer cleanup()
	go func() {
		runErr = cmd.Wait()
		close(done)
	}()

	waitForFile := func(path, description string) []byte {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			content, readErr := os.ReadFile(path)
			if readErr == nil && len(content) > 0 {
				return content
			}
			select {
			case <-done:
				t.Fatalf("merge gate exited before %s: %v\n%s", description, runErr, output.String())
			default:
			}
			if time.Now().After(deadline) {
				cleanup()
				<-done
				t.Fatalf("timed out waiting for %s\n%s", description, output.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	timeoutPIDBytes := waitForFile(timeoutPIDFile, "GNU timeout startup")
	childPIDBytes := waitForFile(childPIDFile, "fake Go child startup")
	waitForFile(diagnosticEmittedFile, "fake Go diagnostic emission")
	select {
	case <-done:
		t.Fatalf("merge gate exited before timeout was triggered: %v\n%s", runErr, output.String())
	default:
	}
	timeoutPID, err := strconv.Atoi(strings.TrimSpace(string(timeoutPIDBytes)))
	if err != nil {
		t.Fatalf("parse GNU timeout PID %q: %v", timeoutPIDBytes, err)
	}
	timeoutTriggeredAt := time.Now()
	if signalOutput, signalErr := exec.Command(killPath, "-ALRM", strconv.Itoa(timeoutPID)).CombinedOutput(); signalErr != nil {
		t.Fatalf("trigger GNU timeout process %d: %v: %s", timeoutPID, signalErr, signalOutput)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cleanup()
		<-done
		t.Fatalf("merge gate did not return within timeout kill-after reserve\n%s", output.String())
	}
	elapsedAfterTrigger := time.Since(timeoutTriggeredAt)
	if runErr == nil {
		t.Fatal("merge gate timeout error = nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 124 {
		t.Fatalf("merge gate timeout error = %v, output=%s", runErr, output.String())
	}
	if !strings.Contains(output.String(), "retained-timeout-marker") || !strings.Contains(output.String(), timeoutBudget+" wall-clock budget") {
		t.Fatalf("merge gate timeout output did not retain child diagnostics:\n%s", output.String())
	}
	if elapsedAfterTrigger > 20*time.Second {
		t.Fatalf("merge gate timeout return after trigger = %v, want within kill-after reserve", elapsedAfterTrigger)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(childPIDBytes)))
	if err != nil {
		t.Fatalf("parse timed-out child PID %q: %v", childPIDBytes, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		probeOutput, probeErr := exec.Command(killPath, "-0", strconv.Itoa(childPID)).CombinedOutput()
		if probeErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(probeErr, &exitErr) {
				t.Fatalf("probe timed-out child process %d: %v", childPID, probeErr)
			}
			if strings.Contains(strings.ToLower(string(probeOutput)), "not permitted") {
				t.Fatalf("probe timed-out child process %d: %s", childPID, probeOutput)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed-out child process %d still running after merge gate returned", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func killProcessGroupWithGrace(pgid int) {
	if syscall.Kill(pgid, 0) != nil {
		return
	}
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	if syscall.Kill(pgid, 0) == nil {
		_ = syscall.Kill(pgid, syscall.SIGKILL)
	}
}

func TestProcessGroupCleanupKillsTermIgnoringMemberAfterOuterDone(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30 & exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process group: %v", err)
	}
	defer killProcessGroupWithGrace(-cmd.Process.Pid)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("outer process: %v", err)
	}
	pgid := -cmd.Process.Pid
	if err := syscall.Kill(pgid, 0); err != nil {
		t.Fatalf("term-ignoring member did not keep process group alive: %v", err)
	}
	killProcessGroupWithGrace(pgid)
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pgid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(pgid, 0) == nil {
		t.Fatal("term-ignoring process-group member survived KILL")
	}
}

func TestRealProcessProfileMergeCleanlyDiscardsDirtyPostMergeHookAndReportsFailure(t *testing.T) {
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

func TestRealProcessProfileMergeCleanlyDiscardsDirtyCommitMsgHookAfterAbort(t *testing.T) {
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

func TestRealProcessProfileMergeCleanlyTransactionalAppliesScratchMergeToCleanTarget(t *testing.T) {
	repo := initDivergedRepo(t)
	gateEvidence := filepath.Join(t.TempDir(), "candidate-gate-evidence")
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	gate := "#!/bin/sh\nset -eu\nif [ \"${AZEDARACH_SKIP_MERGE_REBASE_GATE:-0}\" = 1 ]; then exit 0; fi\nprintf 'head=%s\\nstatus=%s\\nexpected=%s\\n' \"$(git rev-parse HEAD)\" \"$(git status --porcelain)\" \"${AZEDARACH_CANDIDATE_HEAD:-}\" >\"$AZEDARACH_TEST_GATE_EVIDENCE\"\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"), []byte(gate), 0o755); err != nil {
		t.Fatalf("write candidate gate: %v", err)
	}
	runClientTestGit(t, repo, "add", "scripts/git-merge-rebase-gate.sh")
	runClientTestGit(t, repo, "commit", "-q", "-m", "add candidate gate")
	originalHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	t.Setenv("AZEDARACH_TEST_GATE_EVIDENCE", gateEvidence)
	t.Setenv("AZEDARACH_SKIP_MERGE_REBASE_GATE", "1")

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
	resultHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	if resultHead == originalHead {
		t.Fatalf("HEAD = %s, want transactional merge to advance target", resultHead)
	}
	if len(result.ValidationAttempts) != 1 {
		t.Fatalf("validation attempts = %+v, want exactly one candidate attempt", result.ValidationAttempts)
	}
	attempt := result.ValidationAttempts[0]
	if attempt.CandidateHead != resultHead || attempt.Status != CandidateValidationPassed || !attempt.Canonical {
		t.Fatalf("validation attempt = %+v, want canonical passed candidate %s", attempt, resultHead)
	}
	evidence, err := os.ReadFile(gateEvidence)
	if err != nil {
		t.Fatalf("read candidate gate evidence: %v", err)
	}
	wantEvidence := "head=" + resultHead + "\nstatus=\nexpected=" + resultHead + "\n"
	if string(evidence) != wantEvidence {
		t.Fatalf("candidate gate evidence = %q, want %q", evidence, wantEvidence)
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

func TestRealProcessProfileMergeCleanlyTransactionalDoesNotGateConflictedCandidate(t *testing.T) {
	repo := t.TempDir()
	runClientTestGit(t, repo, "init", "-q", "-b", "main")
	runClientTestGit(t, repo, "config", "user.email", "test@example.com")
	runClientTestGit(t, repo, "config", "user.name", "Test User")
	conflictPath := filepath.Join(repo, "conflict.txt")
	if err := os.WriteFile(conflictPath, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base conflict file: %v", err)
	}
	runClientTestGit(t, repo, "add", "conflict.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "base")
	runClientTestGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(conflictPath, []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature conflict file: %v", err)
	}
	runClientTestGit(t, repo, "commit", "-q", "-am", "feature")
	runClientTestGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(conflictPath, []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main conflict file: %v", err)
	}
	runClientTestGit(t, repo, "commit", "-q", "-am", "main")
	gateMarker := filepath.Join(t.TempDir(), "gate-started")
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	gate := "#!/bin/sh\nprintf started >\"$AZEDARACH_TEST_GATE_MARKER\"\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"), []byte(gate), 0o755); err != nil {
		t.Fatalf("write candidate gate: %v", err)
	}
	runClientTestGit(t, repo, "add", "scripts/git-merge-rebase-gate.sh")
	runClientTestGit(t, repo, "commit", "-q", "-m", "add candidate gate")
	originalHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	t.Setenv("AZEDARACH_TEST_GATE_MARKER", gateMarker)

	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
	if err != nil {
		t.Fatalf("MergeCleanlyTransactional() error = %v", err)
	}
	if result == nil || result.Success || !result.HasConflicts {
		t.Fatalf("MergeCleanlyTransactional() result = %+v, want conflict failure", result)
	}
	if len(result.ValidationAttempts) != 0 {
		t.Fatalf("validation attempts = %+v, want none without a resolved candidate", result.ValidationAttempts)
	}
	if _, err := os.Stat(gateMarker); !os.IsNotExist(err) {
		t.Fatalf("gate marker stat error = %v, want gate never started", err)
	}
	if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != originalHead {
		t.Fatalf("target HEAD = %s, want unchanged %s", head, originalHead)
	}
}

func TestRealProcessProfileMergeCleanlyTransactionalUsesTargetGateAuthority(t *testing.T) {
	repo := t.TempDir()
	runClientTestGit(t, repo, "init", "-q", "-b", "main")
	runClientTestGit(t, repo, "config", "user.email", "test@example.com")
	runClientTestGit(t, repo, "config", "user.name", "Test User")
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	evidence := filepath.Join(t.TempDir(), "trusted-gate-evidence")
	trustedGate := "#!/bin/sh\nprintf '%s' \"$AZEDARACH_CANDIDATE_HEAD\" >\"$AZEDARACH_TEST_GATE_EVIDENCE\"\n"
	gatePath := filepath.Join(scriptsDir, "git-merge-rebase-gate.sh")
	if err := os.WriteFile(gatePath, []byte(trustedGate), 0o755); err != nil {
		t.Fatalf("write trusted gate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runClientTestGit(t, repo, "add", ".")
	runClientTestGit(t, repo, "commit", "-q", "-m", "base with trusted gate")
	runClientTestGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(gatePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("weaken candidate gate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	runClientTestGit(t, repo, "add", ".")
	runClientTestGit(t, repo, "commit", "-q", "-m", "feature weakens gate")
	runClientTestGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}
	runClientTestGit(t, repo, "add", "main.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "main")
	t.Setenv("AZEDARACH_TEST_GATE_EVIDENCE", evidence)

	client := NewClient(NewExecRunner(repo), slog.Default())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	relativeRepo, err := filepath.Rel(cwd, repo)
	if err != nil || filepath.IsAbs(relativeRepo) {
		t.Fatalf("relative target path = %q, %v", relativeRepo, err)
	}
	result, err := client.MergeCleanlyTransactional(context.Background(), relativeRepo, "feature")
	if err != nil {
		t.Fatalf("MergeCleanlyTransactional() error = %v", err)
	}
	if result == nil || !result.Success || len(result.ValidationAttempts) != 1 {
		t.Fatalf("MergeCleanlyTransactional() result = %+v, want one successful trusted validation", result)
	}
	content, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatalf("read trusted gate evidence: %v", err)
	}
	if got, want := strings.TrimSpace(string(content)), result.ValidationAttempts[0].CandidateHead; got != want {
		t.Fatalf("trusted gate candidate = %q, want %q", got, want)
	}
}

func TestRealProcessProfileMergeCleanlyTransactionalCompositionSkipsPublicationGate(t *testing.T) {
	repo := t.TempDir()
	runClientTestGit(t, repo, "init", "-q", "-b", "main")
	runClientTestGit(t, repo, "config", "user.email", "test@example.com")
	runClientTestGit(t, repo, "config", "user.name", "Test User")
	requireDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(requireDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	gateMarker := filepath.Join(t.TempDir(), "gate-ran")
	gate := "#!/bin/sh\nprintf ran >\"$AZEDARACH_TEST_GATE_MARKER\"\nexit 91\n"
	if err := os.WriteFile(filepath.Join(requireDir, "git-merge-rebase-gate.sh"), []byte(gate), 0o755); err != nil {
		t.Fatalf("write publication gate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runClientTestGit(t, repo, "add", ".")
	runClientTestGit(t, repo, "commit", "-q", "-m", "base")
	runClientTestGit(t, repo, "checkout", "-q", "-b", "child")
	if err := os.WriteFile(filepath.Join(repo, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}
	runClientTestGit(t, repo, "add", "child.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "child")
	runClientTestGit(t, repo, "checkout", "-q", "main")
	t.Setenv("AZEDARACH_TEST_GATE_MARKER", gateMarker)

	result, err := NewClient(NewExecRunner(repo), slog.Default()).MergeCleanlyTransactionalComposition(context.Background(), repo, "child")
	if err != nil || result == nil || !result.Success {
		t.Fatalf("MergeCleanlyTransactionalComposition() = (%+v, %v), want clean composition", result, err)
	}
	if len(result.ValidationAttempts) != 0 {
		t.Fatalf("composition validation attempts = %+v, want none", result.ValidationAttempts)
	}
	if _, err := os.Stat(gateMarker); !os.IsNotExist(err) {
		t.Fatalf("non-base composition invoked publication gate: %v", err)
	}
}

func TestRealProcessProfileMergeCleanlyTransactionalRunsScratchHooksAndKeepsTargetCleanWhenHookFails(t *testing.T) {
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

func TestRealProcessProfileRecoverIntegrationJournalCompletesInterruptedFinalReset(t *testing.T) {
	repo := initDivergedRepo(t)
	client := NewClient(NewExecRunner(repo), slog.Default())
	ctx := context.Background()
	targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
	desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "scratch merge")
	scratch, scratchOwner := addOwnedIntegrationScratch(t, client, repo, desiredHead)

	if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
		Version:         integrationJournalVersion,
		TargetWorktree:  repo,
		TargetHead:      targetHead,
		DesiredHead:     desiredHead,
		ScratchWorktree: scratch,
		ScratchOwner:    scratchOwner,
		Validation: CandidateValidationAttempt{
			CandidateHead: desiredHead,
			Status:        CandidateValidationPassed,
			Canonical:     false,
		},
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("writeIntegrationJournal() error = %v", err)
	}
	runClientTestGit(t, repo, "reset", "--hard", desiredHead)
	if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != desiredHead {
		t.Fatalf("HEAD before recovery = %s, want interrupted apply at %s", head, desiredHead)
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
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch stat err = %v, want proven scratch removed", err)
	}
	attempt, found, err := client.CanonicalIntegrationValidation(ctx, repo, desiredHead)
	if err != nil {
		t.Fatalf("CanonicalIntegrationValidation() error = %v", err)
	}
	if !found || attempt.CandidateHead != desiredHead || attempt.Status != CandidateValidationPassed || !attempt.Canonical {
		t.Fatalf("canonical validation = (%+v, %t), want exact recovered candidate %s", attempt, found, desiredHead)
	}
	if attempt, found, err := client.CanonicalIntegrationValidation(ctx, repo, targetHead); err != nil || found {
		t.Fatalf("validation for noncandidate target = (%+v, %t, %v), want no mismatched receipt", attempt, found, err)
	}
}

func TestRealProcessProfileMergeCleanlyTransactionalPreservesJournalScratchAcrossPostResetFailures(t *testing.T) {
	for _, failure := range []string{"status", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			repo := initDivergedRepo(t)
			installPassingIntegrationGate(t, repo)
			baseRunner := NewExecRunner(repo)
			targetStatusReads := 0
			runner := &rawMockRunner{runFunc: func(ctx context.Context, args ...string) (string, error) {
				if failure == "status" && clientTestArgsForWorktree(args, repo, "status", "--porcelain") {
					targetStatusReads++
					if targetStatusReads == 3 {
						return "", fmt.Errorf("injected post-reset status failure")
					}
				}
				return baseRunner.Run(ctx, args...)
			}}
			client := NewClient(runner, slog.Default())
			var receiptObstacle string
			if failure == "receipt" {
				receiptObstacle = filepath.Join(repo, ".git", "azedarach", integrationReceiptName(repo))
				if err := os.MkdirAll(receiptObstacle, 0o755); err != nil {
					t.Fatalf("create receipt failure obstacle: %v", err)
				}
			}

			result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
			if err == nil {
				t.Fatalf("MergeCleanlyTransactional() = (%+v, nil), want injected %s failure", result, failure)
			}
			if !strings.Contains(err.Error(), failure) {
				t.Fatalf("MergeCleanlyTransactional() error = %v, want %s detail", err, failure)
			}
			journal, journalPath, found, readErr := client.readIntegrationJournal(context.Background(), repo)
			if readErr != nil || !found {
				t.Fatalf("readIntegrationJournal() = (%+v, %q, %t, %v), want retained journal", journal, journalPath, found, readErr)
			}
			if _, statErr := os.Stat(journal.ScratchWorktree); statErr != nil {
				t.Fatalf("retained scratch stat error = %v", statErr)
			}
			if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != journal.DesiredHead {
				t.Fatalf("HEAD after injected failure = %s, want applied candidate %s", head, journal.DesiredHead)
			}
			if failure == "receipt" {
				if err := os.Remove(receiptObstacle); err != nil {
					t.Fatalf("remove receipt failure obstacle: %v", err)
				}
			}

			recoveryClient := NewClient(NewExecRunner(repo), slog.Default())
			if err := recoveryClient.RecoverIntegrationJournal(context.Background(), repo); err != nil {
				t.Fatalf("RecoverIntegrationJournal() retry error = %v", err)
			}
			if _, statErr := os.Stat(journalPath); !os.IsNotExist(statErr) {
				t.Fatalf("journal after retry stat error = %v, want removed", statErr)
			}
			if _, statErr := os.Stat(journal.ScratchWorktree); !os.IsNotExist(statErr) {
				t.Fatalf("scratch after retry stat error = %v, want removed", statErr)
			}
			for path, want := range map[string]string{"main.txt": "main\n", "feature.txt": "feature\n"} {
				got, readErr := os.ReadFile(filepath.Join(repo, path))
				if readErr != nil || string(got) != want {
					t.Fatalf("%s after retry = %q, %v; want %q", path, got, readErr, want)
				}
			}
			if status, statusErr := recoveryClient.Status(context.Background(), repo); statusErr != nil || status.HasChanges {
				t.Fatalf("target status after retry = (%+v, %v), want clean", status, statusErr)
			}
			attempt, canonical, receiptErr := recoveryClient.CanonicalIntegrationValidation(context.Background(), repo, journal.DesiredHead)
			if receiptErr != nil || !canonical || !attempt.Canonical {
				t.Fatalf("canonical receipt after retry = (%+v, %t, %v), want exact canonical proof", attempt, canonical, receiptErr)
			}
		})
	}
}

func TestRealProcessProfileMergeCleanlyTransactionalRetainsScratchUntilUnlinkIsDurable(t *testing.T) {
	repo := initDivergedRepo(t)
	installPassingIntegrationGate(t, repo)
	client := NewClient(NewExecRunner(repo), slog.Default())
	syncAttempts := 0
	client.syncJournalDir = func(string) error {
		syncAttempts++
		if syncAttempts == 1 {
			return fmt.Errorf("injected journal directory sync failure")
		}
		return nil
	}

	result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
	if err == nil || !strings.Contains(err.Error(), "injected journal directory sync failure") {
		t.Fatalf("MergeCleanlyTransactional() = (%+v, %v), want injected sync failure", result, err)
	}
	journal, journalPath, found, readErr := client.readIntegrationJournal(context.Background(), repo)
	if readErr != nil || !found {
		t.Fatalf("readIntegrationJournal() = (%+v, %q, %t, %v), want restored journal", journal, journalPath, found, readErr)
	}
	if _, statErr := os.Stat(journal.ScratchWorktree); statErr != nil {
		t.Fatalf("scratch after failed durable unlink stat error = %v, want retained", statErr)
	}
	if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != journal.DesiredHead {
		t.Fatalf("HEAD after failed durable unlink = %s, want applied candidate %s", head, journal.DesiredHead)
	}

	if err := client.RecoverIntegrationJournal(context.Background(), repo); err != nil {
		t.Fatalf("RecoverIntegrationJournal() retry error = %v", err)
	}
	if _, statErr := os.Stat(journalPath); !os.IsNotExist(statErr) {
		t.Fatalf("journal after durable retry stat error = %v, want removed", statErr)
	}
	if _, statErr := os.Stat(journal.ScratchWorktree); !os.IsNotExist(statErr) {
		t.Fatalf("scratch after durable retry stat error = %v, want removed", statErr)
	}
	if syncAttempts != 2 {
		t.Fatalf("journal directory sync attempts = %d, want failed apply and successful recovery", syncAttempts)
	}
}

func TestRealProcessProfileRecoverIntegrationJournalRetriesDeleteBeforeScratchCleanup(t *testing.T) {
	repo := initDivergedRepo(t)
	client := NewClient(NewExecRunner(repo), slog.Default())
	ctx := context.Background()
	targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
	desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "scratch merge")
	scratch, scratchOwner := addOwnedIntegrationScratch(t, client, repo, desiredHead)
	journal := integrationJournal{
		Version: integrationJournalVersion, TargetWorktree: repo, TargetHead: targetHead, DesiredHead: desiredHead,
		ScratchWorktree: scratch,
		ScratchOwner:    scratchOwner,
		Validation:      CandidateValidationAttempt{CandidateHead: desiredHead, Status: CandidateValidationPassed},
		StartedAt:       time.Now().UTC(),
	}
	if err := client.writeIntegrationJournal(ctx, repo, journal); err != nil {
		t.Fatalf("writeIntegrationJournal() error = %v", err)
	}
	runClientTestGit(t, repo, "reset", "--hard", desiredHead)
	journalPath, err := client.integrationJournalPath(ctx, repo)
	if err != nil {
		t.Fatalf("integrationJournalPath() error = %v", err)
	}
	deleteAttempts := 0
	client.removeJournal = func(path string) error {
		deleteAttempts++
		if deleteAttempts == 1 {
			return fmt.Errorf("injected journal delete failure")
		}
		return removeIntegrationJournalPath(path)
	}

	err = client.RecoverIntegrationJournal(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "injected journal delete failure") {
		t.Fatalf("first RecoverIntegrationJournal() error = %v, want injected delete failure", err)
	}
	if _, statErr := os.Stat(journalPath); statErr != nil {
		t.Fatalf("journal after failed delete stat error = %v, want retained", statErr)
	}
	if _, statErr := os.Stat(scratch); statErr != nil {
		t.Fatalf("scratch after failed journal delete stat error = %v, want retained", statErr)
	}
	if err := client.RecoverIntegrationJournal(ctx, repo); err != nil {
		t.Fatalf("second RecoverIntegrationJournal() error = %v", err)
	}
	if _, statErr := os.Stat(journalPath); !os.IsNotExist(statErr) {
		t.Fatalf("journal after retry stat error = %v, want removed", statErr)
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Fatalf("scratch after retry stat error = %v, want removed", statErr)
	}
	if deleteAttempts != 2 {
		t.Fatalf("journal delete attempts = %d, want one failed attempt and one retry", deleteAttempts)
	}
	for path, want := range map[string]string{"main.txt": "main\n", "feature.txt": "feature\n"} {
		got, readErr := os.ReadFile(filepath.Join(repo, path))
		if readErr != nil || string(got) != want {
			t.Fatalf("%s after delete retry = %q, %v; want %q", path, got, readErr, want)
		}
	}
	attempt, canonical, receiptErr := client.CanonicalIntegrationValidation(ctx, repo, desiredHead)
	if receiptErr != nil || !canonical || !attempt.Canonical {
		t.Fatalf("canonical receipt after delete retry = (%+v, %t, %v), want exact canonical proof", attempt, canonical, receiptErr)
	}
}

func TestRealProcessProfileRecoverIntegrationJournalRetainsScratchUntilUnlinkIsDurable(t *testing.T) {
	repo := initDivergedRepo(t)
	client := NewClient(NewExecRunner(repo), slog.Default())
	ctx := context.Background()
	targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
	desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "scratch merge")
	scratch, scratchOwner := addOwnedIntegrationScratch(t, client, repo, desiredHead)
	journal := integrationJournal{
		Version: integrationJournalVersion, TargetWorktree: repo, TargetHead: targetHead, DesiredHead: desiredHead,
		ScratchWorktree: scratch,
		ScratchOwner:    scratchOwner,
		Validation:      CandidateValidationAttempt{CandidateHead: desiredHead, Status: CandidateValidationPassed},
		StartedAt:       time.Now().UTC(),
	}
	if err := client.writeIntegrationJournal(ctx, repo, journal); err != nil {
		t.Fatalf("writeIntegrationJournal() error = %v", err)
	}
	runClientTestGit(t, repo, "reset", "--hard", desiredHead)
	journalPath, err := client.integrationJournalPath(ctx, repo)
	if err != nil {
		t.Fatalf("integrationJournalPath() error = %v", err)
	}
	wantJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal before recovery: %v", err)
	}

	syncAttempts := 0
	client.syncJournalDir = func(string) error {
		syncAttempts++
		if syncAttempts == 1 {
			return fmt.Errorf("injected journal directory sync failure")
		}
		return nil
	}
	err = client.RecoverIntegrationJournal(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "injected journal directory sync failure") {
		t.Fatalf("first RecoverIntegrationJournal() error = %v, want injected sync failure", err)
	}
	gotJournal, readErr := os.ReadFile(journalPath)
	if readErr != nil || !bytes.Equal(gotJournal, wantJournal) {
		t.Fatalf("journal after failed durable unlink = %q, %v; want restored bytes %q", gotJournal, readErr, wantJournal)
	}
	if _, statErr := os.Stat(scratch); statErr != nil {
		t.Fatalf("scratch after failed durable unlink stat error = %v, want retained", statErr)
	}

	if err := client.RecoverIntegrationJournal(ctx, repo); err != nil {
		t.Fatalf("second RecoverIntegrationJournal() error = %v", err)
	}
	if _, statErr := os.Stat(journalPath); !os.IsNotExist(statErr) {
		t.Fatalf("journal after durable retry stat error = %v, want removed", statErr)
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Fatalf("scratch after durable retry stat error = %v, want removed", statErr)
	}
	if syncAttempts != 2 {
		t.Fatalf("journal directory sync attempts = %d, want failed attempt and successful retry", syncAttempts)
	}
}

func TestRealProcessProfileRecoverIntegrationJournalReprovesOwnershipImmediatelyBeforeCleanup(t *testing.T) {
	repo := initDivergedRepo(t)
	setupClient := NewClient(NewExecRunner(repo), slog.Default())
	ctx := context.Background()
	targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
	desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "scratch merge")
	scratch, scratchOwner := addOwnedIntegrationScratch(t, setupClient, repo, desiredHead)
	journal := integrationJournal{
		Version: integrationJournalVersion, TargetWorktree: repo, TargetHead: targetHead, DesiredHead: desiredHead,
		ScratchWorktree: scratch,
		ScratchOwner:    scratchOwner,
		Validation:      CandidateValidationAttempt{CandidateHead: desiredHead, Status: CandidateValidationPassed},
		StartedAt:       time.Now().UTC(),
	}
	if err := setupClient.writeIntegrationJournal(ctx, repo, journal); err != nil {
		t.Fatalf("writeIntegrationJournal() error = %v", err)
	}
	runClientTestGit(t, repo, "reset", "--hard", desiredHead)
	commonDir, err := setupClient.gitCommonDir(ctx, repo)
	if err != nil {
		t.Fatalf("gitCommonDir() error = %v", err)
	}
	ownerPath, err := setupClient.integrationScratchOwnershipPath(ctx, scratch, commonDir)
	if err != nil {
		t.Fatalf("integrationScratchOwnershipPath() error = %v", err)
	}

	baseRunner := NewExecRunner(repo)
	markerReplaced := false
	runner := &rawMockRunner{runFunc: func(ctx context.Context, args ...string) (string, error) {
		output, runErr := baseRunner.Run(ctx, args...)
		if runErr == nil && !markerReplaced && clientTestArgsForWorktree(args, repo, "reset", "--hard", desiredHead) {
			markerReplaced = true
			replacement := scratchOwner
			replacement.AttemptID = strings.Repeat("f", len(scratchOwner.AttemptID))
			payload, marshalErr := json.MarshalIndent(replacement, "", "  ")
			if marshalErr != nil {
				return "", marshalErr
			}
			if writeErr := writeFileAtomic(ownerPath, append(payload, '\n'), 0o600); writeErr != nil {
				return "", writeErr
			}
		}
		return output, runErr
	}}
	recoveryClient := NewClient(runner, slog.Default())
	err = recoveryClient.RecoverIntegrationJournal(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "re-prove exact integration scratch ownership") {
		t.Fatalf("RecoverIntegrationJournal() error = %v, want cleanup-time ownership refusal", err)
	}
	journalPath, pathErr := setupClient.integrationJournalPath(ctx, repo)
	if pathErr != nil {
		t.Fatalf("integrationJournalPath() error = %v", pathErr)
	}
	if _, statErr := os.Stat(journalPath); statErr != nil {
		t.Fatalf("journal after cleanup-time replacement stat error = %v, want retained", statErr)
	}
	if _, statErr := os.Stat(scratch); statErr != nil {
		t.Fatalf("scratch after cleanup-time replacement stat error = %v, want retained", statErr)
	}

	originalPayload, err := json.MarshalIndent(scratchOwner, "", "  ")
	if err != nil {
		t.Fatalf("marshal original owner: %v", err)
	}
	if err := writeFileAtomic(ownerPath, append(originalPayload, '\n'), 0o600); err != nil {
		t.Fatalf("restore original owner: %v", err)
	}
	if err := setupClient.RecoverIntegrationJournal(ctx, repo); err != nil {
		t.Fatalf("RecoverIntegrationJournal() retry error = %v", err)
	}
	if _, statErr := os.Stat(journalPath); !os.IsNotExist(statErr) {
		t.Fatalf("journal after ownership retry stat error = %v, want removed", statErr)
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Fatalf("scratch after ownership retry stat error = %v, want removed", statErr)
	}
}

func TestRealProcessProfileMergeCleanlyTransactionalWithoutGateCreatesNoCanonicalReceipt(t *testing.T) {
	repo := initDivergedRepo(t)
	client := NewClient(NewExecRunner(repo), slog.Default())
	result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
	if err != nil || result == nil || !result.Success {
		t.Fatalf("MergeCleanlyTransactional() = (%+v, %v), want success", result, err)
	}
	candidateHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	if attempt, found, err := client.CanonicalIntegrationValidation(context.Background(), repo, candidateHead); err != nil || found {
		t.Fatalf("canonical validation = (%+v, %t, %v), want no receipt without a configured gate", attempt, found, err)
	}
}

func TestRealProcessProfileRecoverIntegrationJournalRollsBackDesiredHeadWithoutValidationProof(t *testing.T) {
	for _, version := range []int{integrationJournalVersionV1, integrationJournalVersion} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			repo := initDivergedRepo(t)
			client := NewClient(NewExecRunner(repo), slog.Default())
			ctx := context.Background()
			targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
			tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
			desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "unproved merge")
			scratch, scratchOwner := addOwnedIntegrationScratch(t, client, repo, desiredHead)
			if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
				Version: version, TargetWorktree: repo, TargetHead: targetHead, DesiredHead: desiredHead,
				ScratchWorktree: scratch, ScratchOwner: scratchOwner, StartedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatalf("writeIntegrationJournal() error = %v", err)
			}
			runClientTestGit(t, repo, "reset", "--hard", desiredHead)
			if err := client.RecoverIntegrationJournal(ctx, repo); err != nil {
				t.Fatalf("RecoverIntegrationJournal() error = %v", err)
			}
			if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != targetHead {
				t.Fatalf("HEAD = %s, want rollback to unvalidated target %s", head, targetHead)
			}
			if attempt, found, err := client.CanonicalIntegrationValidation(ctx, repo, desiredHead); err != nil || found {
				t.Fatalf("canonical validation = (%+v, %t, %v), want no proof", attempt, found, err)
			}
		})
	}
}

func TestRecoverIntegrationJournalRejectsUnknownVersionWithoutMutation(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch-must-remain")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	var commands []string
	runner := &rawMockRunner{runFunc: func(_ context.Context, args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		if clientTestArgsForWorktree(args, repo, "rev-parse", "--git-common-dir") {
			return gitDir, nil
		}
		return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
	}}
	client := NewClient(runner, slog.Default())
	ctx := context.Background()
	if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
		Version:         integrationJournalVersionV2 + 1,
		TargetWorktree:  repo,
		TargetHead:      "target-sha",
		DesiredHead:     "future-sha",
		ScratchWorktree: scratch,
		StartedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("writeIntegrationJournal() error = %v", err)
	}
	journalPath, err := client.integrationJournalPath(ctx, repo)
	if err != nil {
		t.Fatalf("integrationJournalPath() error = %v", err)
	}
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal before recovery: %v", err)
	}
	commands = nil

	err = client.RecoverIntegrationJournal(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "unsupported integration journal version 3") {
		t.Fatalf("RecoverIntegrationJournal() error = %v, want unsupported version", err)
	}
	if len(commands) != 0 {
		t.Fatalf("recovery commands = %v, want no Git mutation or inspection", commands)
	}
	journalAfter, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("journal was deleted: %v", err)
	}
	if !bytes.Equal(journalAfter, journalBefore) {
		t.Fatal("journal contents changed during rejected recovery")
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("scratch worktree was changed or deleted: %v", err)
	}
}

func TestRecoverIntegrationJournalRejectsRawLegacyV1WithoutScratchOwner(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	targetSentinel := filepath.Join(repo, "target-must-remain.txt")
	if err := os.WriteFile(targetSentinel, []byte("target bytes\n"), 0o644); err != nil {
		t.Fatalf("write target sentinel: %v", err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch-must-remain")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	scratchSentinel := filepath.Join(scratch, "scratch-must-remain.txt")
	if err := os.WriteFile(scratchSentinel, []byte("scratch bytes\n"), 0o644); err != nil {
		t.Fatalf("write scratch sentinel: %v", err)
	}

	var commands []string
	runner := &rawMockRunner{runFunc: func(_ context.Context, args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		if clientTestArgsForWorktree(args, repo, "rev-parse", "--git-common-dir") {
			return gitDir, nil
		}
		return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
	}}
	client := NewClient(runner, slog.Default())
	ctx := context.Background()
	journalPath, err := client.integrationJournalPath(ctx, repo)
	if err != nil {
		t.Fatalf("integrationJournalPath() error = %v", err)
	}
	rawJournal, err := json.Marshal(map[string]any{
		"version":          integrationJournalVersionV1,
		"target_worktree":  repo,
		"target_head":      "target-sha",
		"desired_head":     "desired-sha",
		"scratch_worktree": scratch,
		"started_at":       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal raw legacy journal: %v", err)
	}
	if bytes.Contains(rawJournal, []byte("scratch_owner")) {
		t.Fatalf("raw legacy journal unexpectedly contains scratch_owner: %s", rawJournal)
	}
	if err := writeFileAtomic(journalPath, append(rawJournal, '\n'), 0o600); err != nil {
		t.Fatalf("write raw legacy journal: %v", err)
	}
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal before recovery: %v", err)
	}
	commands = nil

	err = client.RecoverIntegrationJournal(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "legacy integration journal v1 has no scratch ownership proof") {
		t.Fatalf("RecoverIntegrationJournal() error = %v, want operator-recovery refusal", err)
	}
	if len(commands) != 0 {
		t.Fatalf("recovery commands = %v, want no Git mutation or inspection", commands)
	}
	journalAfter, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("journal was deleted: %v", err)
	}
	if !bytes.Equal(journalAfter, journalBefore) {
		t.Fatal("journal contents changed during ownerless-v1 refusal")
	}
	if got, err := os.ReadFile(targetSentinel); err != nil || string(got) != "target bytes\n" {
		t.Fatalf("target sentinel = %q, %v; want preserved", got, err)
	}
	if got, err := os.ReadFile(scratchSentinel); err != nil || string(got) != "scratch bytes\n" {
		t.Fatalf("scratch sentinel = %q, %v; want preserved", got, err)
	}
}

func TestRealProcessProfileRecoverIntegrationJournalRetainsTrackedEditsForV1AndV2(t *testing.T) {
	for _, version := range []int{integrationJournalVersionV1, integrationJournalVersionV2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			repo := initDivergedRepo(t)
			client := NewClient(NewExecRunner(repo), slog.Default())
			ctx := context.Background()
			targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
			tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
			desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "unapplied merge")
			scratch, scratchOwner := addOwnedIntegrationScratch(t, client, repo, desiredHead)
			scratchMarker := filepath.Join(scratch, "marker")
			if err := os.WriteFile(scratchMarker, []byte("retain scratch\n"), 0o644); err != nil {
				t.Fatalf("write scratch marker: %v", err)
			}
			if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
				Version:         version,
				TargetWorktree:  repo,
				TargetHead:      targetHead,
				DesiredHead:     desiredHead,
				ScratchWorktree: scratch,
				ScratchOwner:    scratchOwner,
				StartedAt:       time.Now().UTC(),
			}); err != nil {
				t.Fatalf("writeIntegrationJournal() error = %v", err)
			}
			journalPath, err := client.integrationJournalPath(ctx, repo)
			if err != nil {
				t.Fatalf("integrationJournalPath() error = %v", err)
			}
			journalBefore, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatalf("read journal before recovery: %v", err)
			}
			trackedPath := filepath.Join(repo, "main.txt")
			trackedBytes := []byte("user tracked edit\n")
			if err := os.WriteFile(trackedPath, trackedBytes, 0o644); err != nil {
				t.Fatalf("write tracked edit: %v", err)
			}

			err = client.RecoverIntegrationJournal(ctx, repo)
			if err == nil || !strings.Contains(err.Error(), "target is dirty before integration recovery") {
				t.Fatalf("RecoverIntegrationJournal() error = %v, want tracked-edit refusal", err)
			}
			if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != targetHead {
				t.Fatalf("HEAD = %s, want unchanged %s", head, targetHead)
			}
			if got, err := os.ReadFile(trackedPath); err != nil || !bytes.Equal(got, trackedBytes) {
				t.Fatalf("tracked bytes = %q, %v; want preserved %q", got, err, trackedBytes)
			}
			if got, err := os.ReadFile(journalPath); err != nil || !bytes.Equal(got, journalBefore) {
				t.Fatalf("journal bytes changed: %v", err)
			}
			if got, err := os.ReadFile(scratchMarker); err != nil || string(got) != "retain scratch\n" {
				t.Fatalf("scratch marker = %q, %v; want retained", got, err)
			}
		})
	}
}

func TestRealProcessProfileRecoverIntegrationJournalRetainsUntrackedResetCollisionsForV1AndV2(t *testing.T) {
	for _, version := range []int{integrationJournalVersionV1, integrationJournalVersionV2} {
		for _, collision := range []string{"file", "directory"} {
			t.Run(fmt.Sprintf("v%d/%s", version, collision), func(t *testing.T) {
				repo := initDivergedRepo(t)
				client := NewClient(NewExecRunner(repo), slog.Default())
				ctx := context.Background()
				collisionPath := filepath.Join(repo, "collision")
				if err := os.WriteFile(collisionPath, []byte("tracked target bytes\n"), 0o644); err != nil {
					t.Fatalf("write tracked collision source: %v", err)
				}
				runClientTestGit(t, repo, "add", "collision")
				runClientTestGit(t, repo, "commit", "-q", "-m", "track collision at rollback head")
				targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
				runClientTestGit(t, repo, "rm", "-q", "collision")
				runClientTestGit(t, repo, "commit", "-q", "-m", "remove collision at interrupted head")
				desiredHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
				scratch, scratchOwner := addOwnedIntegrationScratch(t, client, repo, desiredHead)

				var preservedPath string
				preservedBytes := []byte("user collision bytes\n")
				switch collision {
				case "file":
					preservedPath = collisionPath
					if err := os.WriteFile(preservedPath, preservedBytes, 0o644); err != nil {
						t.Fatalf("write untracked file collision: %v", err)
					}
				case "directory":
					preservedPath = filepath.Join(collisionPath, "nested")
					if err := os.Mkdir(collisionPath, 0o755); err != nil {
						t.Fatalf("mkdir untracked directory collision: %v", err)
					}
					if err := os.WriteFile(preservedPath, preservedBytes, 0o644); err != nil {
						t.Fatalf("write untracked directory bytes: %v", err)
					}
				}
				if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
					Version: version, TargetWorktree: repo, TargetHead: targetHead, DesiredHead: desiredHead,
					ScratchWorktree: scratch, ScratchOwner: scratchOwner, StartedAt: time.Now().UTC(),
				}); err != nil {
					t.Fatalf("writeIntegrationJournal() error = %v", err)
				}
				journalPath, err := client.integrationJournalPath(ctx, repo)
				if err != nil {
					t.Fatalf("integrationJournalPath() error = %v", err)
				}
				journalBefore, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatalf("read journal before recovery: %v", err)
				}

				err = client.RecoverIntegrationJournal(ctx, repo)
				if err == nil || !strings.Contains(err.Error(), "target is dirty before integration recovery") {
					t.Fatalf("RecoverIntegrationJournal() error = %v, want untracked collision refusal", err)
				}
				if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != desiredHead {
					t.Fatalf("HEAD = %s, want unchanged interrupted head %s", head, desiredHead)
				}
				if got, readErr := os.ReadFile(preservedPath); readErr != nil || !bytes.Equal(got, preservedBytes) {
					t.Fatalf("collision bytes = %q, %v; want preserved %q", got, readErr, preservedBytes)
				}
				if got, readErr := os.ReadFile(journalPath); readErr != nil || !bytes.Equal(got, journalBefore) {
					t.Fatalf("journal changed after collision refusal: %v", readErr)
				}
				if _, statErr := os.Stat(scratch); statErr != nil {
					t.Fatalf("scratch after collision refusal: %v", statErr)
				}
			})
		}
	}
}

func TestRealProcessProfileRecoverIntegrationJournalRejectsOtherOwnedScratchForV1AndV2(t *testing.T) {
	for _, version := range []int{integrationJournalVersionV1, integrationJournalVersionV2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			repo := initDivergedRepo(t)
			client := NewClient(NewExecRunner(repo), slog.Default())
			ctx := context.Background()
			targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
			tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
			desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "shared desired oid")
			intended, intendedOwner := addOwnedIntegrationScratch(t, client, repo, desiredHead)
			victim, _ := addOwnedIntegrationScratch(t, client, repo, desiredHead)
			victimMarker := filepath.Join(victim, "must-survive")
			victimBytes := []byte("valid unrelated scratch\n")
			if err := os.WriteFile(victimMarker, victimBytes, 0o644); err != nil {
				t.Fatalf("write victim marker: %v", err)
			}
			if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
				Version: version, TargetWorktree: repo, TargetHead: targetHead, DesiredHead: desiredHead,
				ScratchWorktree: victim, ScratchOwner: intendedOwner, StartedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatalf("writeIntegrationJournal() error = %v", err)
			}
			journalPath, err := client.integrationJournalPath(ctx, repo)
			if err != nil {
				t.Fatalf("integrationJournalPath() error = %v", err)
			}
			journalBefore, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatalf("read journal before recovery: %v", err)
			}

			err = client.RecoverIntegrationJournal(ctx, repo)
			if err == nil || !strings.Contains(err.Error(), "journal scratch ownership") {
				t.Fatalf("RecoverIntegrationJournal() error = %v, want attempt ownership refusal", err)
			}
			if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != targetHead {
				t.Fatalf("HEAD = %s, want unchanged %s", head, targetHead)
			}
			if got, readErr := os.ReadFile(victimMarker); readErr != nil || !bytes.Equal(got, victimBytes) {
				t.Fatalf("victim bytes = %q, %v; want preserved %q", got, readErr, victimBytes)
			}
			if got, readErr := os.ReadFile(journalPath); readErr != nil || !bytes.Equal(got, journalBefore) {
				t.Fatalf("journal changed after victim refusal: %v", readErr)
			}
			if _, statErr := os.Stat(intended); statErr != nil {
				t.Fatalf("intended scratch changed: %v", statErr)
			}
		})
	}
}

func TestRealProcessProfileRecoverIntegrationJournalValidatesTargetForV1AndV2(t *testing.T) {
	for _, version := range []int{integrationJournalVersionV1, integrationJournalVersionV2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			repo := initDivergedRepo(t)
			client := NewClient(NewExecRunner(repo), slog.Default())
			ctx := context.Background()
			targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
			scratch, scratchOwner := addOwnedIntegrationScratch(t, client, repo, targetHead)
			if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
				Version: version, TargetWorktree: repo + "-other", TargetHead: targetHead, DesiredHead: targetHead,
				ScratchWorktree: scratch, ScratchOwner: scratchOwner, StartedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatalf("writeIntegrationJournal() error = %v", err)
			}
			err := client.RecoverIntegrationJournal(ctx, repo)
			if err == nil || !strings.Contains(err.Error(), "does not match recovery target") {
				t.Fatalf("RecoverIntegrationJournal() error = %v, want target identity refusal", err)
			}
			if _, statErr := os.Stat(scratch); statErr != nil {
				t.Fatalf("scratch after target refusal: %v", statErr)
			}
		})
	}
}

func TestRealProcessProfileRecoverIntegrationJournalRejectsUnprovenScratchWithoutLoss(t *testing.T) {
	for _, version := range []int{integrationJournalVersionV1, integrationJournalVersionV2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			repo := initDivergedRepo(t)
			client := NewClient(NewExecRunner(repo), slog.Default())
			ctx := context.Background()
			targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
			tree := runClientTestGitOutput(t, repo, "merge-tree", "--write-tree", targetHead, "feature")
			desiredHead := runClientTestGitOutput(t, repo, "commit-tree", tree, "-p", targetHead, "-p", "feature", "-m", "unapplied merge")
			victim, err := os.MkdirTemp("", "azedarach-integration-corrupt-")
			if err != nil {
				t.Fatalf("create victim path: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(victim) })
			victimMarker := filepath.Join(victim, "must-survive")
			victimBytes := []byte("not a registered worktree\n")
			if err := os.WriteFile(victimMarker, victimBytes, 0o644); err != nil {
				t.Fatalf("write victim marker: %v", err)
			}
			if err := client.writeIntegrationJournal(ctx, repo, integrationJournal{
				Version:         version,
				TargetWorktree:  repo,
				TargetHead:      targetHead,
				DesiredHead:     desiredHead,
				ScratchWorktree: victim,
				StartedAt:       time.Now().UTC(),
			}); err != nil {
				t.Fatalf("writeIntegrationJournal() error = %v", err)
			}
			journalPath, err := client.integrationJournalPath(ctx, repo)
			if err != nil {
				t.Fatalf("integrationJournalPath() error = %v", err)
			}
			journalBefore, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatalf("read journal before recovery: %v", err)
			}

			err = client.RecoverIntegrationJournal(ctx, repo)
			wantError := "is not a registered worktree"
			if version == integrationJournalVersionV1 {
				wantError = "legacy integration journal v1 has no scratch ownership proof"
			}
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("RecoverIntegrationJournal() error = %v, want %q refusal", err, wantError)
			}
			if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != targetHead {
				t.Fatalf("HEAD = %s, want unchanged %s", head, targetHead)
			}
			if got, err := os.ReadFile(journalPath); err != nil || !bytes.Equal(got, journalBefore) {
				t.Fatalf("journal bytes changed: %v", err)
			}
			if got, err := os.ReadFile(victimMarker); err != nil || !bytes.Equal(got, victimBytes) {
				t.Fatalf("victim bytes = %q, %v; want preserved %q", got, err, victimBytes)
			}
		})
	}
}

func TestMergeCleanlyTransactionalRetainsJournalAndScratchWhenFinalApplyIsDirty(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir target scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write target gate: %v", err)
	}

	var scratchWorktree string
	targetStatusReads := 0
	scratchStatusReads := 0
	targetRollbackResets := 0
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
			if err := os.MkdirAll(scratchWorktree, 0o755); err != nil {
				t.Fatalf("mkdir scratch worktree: %v", err)
			}
			return "", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, scratchWorktree, "status", "--porcelain"):
			scratchStatusReads++
			return "", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, scratchWorktree, "merge", "--no-edit", "feature"):
			return "Merge made by the 'ort' strategy.", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, scratchWorktree, "rev-parse", "--verify", "HEAD"):
			return "desired-sha", nil
		case scratchWorktree != "" && len(args) == 4 && args[0] == "-C" &&
			normalizeWorktreeLockKey(args[1]) == normalizeWorktreeLockKey(scratchWorktree) &&
			args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(repo, ".git", "worktrees", "integration-scratch"), nil
		case clientTestArgsForWorktree(args, repo, "reset", "--hard", "desired-sha"):
			return "", nil
		case clientTestArgsForWorktree(args, repo, "reset", "--hard", "target-sha"):
			targetRollbackResets++
			return "", nil
		case scratchWorktree != "" && clientTestArgsForWorktree(args, repo, "worktree", "list", "--porcelain"):
			return fmt.Sprintf("worktree %s\nHEAD desired-sha\ndetached\n\nworktree %s\nHEAD desired-sha\ndetached\n", repo, scratchWorktree), nil
		case scratchWorktree != "" && len(args) == 6 && args[0] == "-C" && args[1] == repo &&
			args[2] == "worktree" && args[3] == "remove" && args[4] == "--force" &&
			normalizeWorktreeLockKey(args[5]) == normalizeWorktreeLockKey(scratchWorktree):
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
	}}

	client := NewClient(runner, slog.Default())
	result, err := client.MergeCleanlyTransactional(context.Background(), repo, "feature")
	if err == nil || !strings.Contains(err.Error(), "target is dirty before integration recovery") {
		t.Fatalf("MergeCleanlyTransactional() = (%+v, %v), want fail-closed dirty recovery", result, err)
	}
	journalPath, err := client.integrationJournalPath(context.Background(), repo)
	if err != nil {
		t.Fatalf("integrationJournalPath() error = %v", err)
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("journal stat err = %v, want retained", err)
	}
	if _, err := os.Stat(scratchWorktree); err != nil {
		t.Fatalf("scratch stat err = %v, want retained", err)
	}
	if targetRollbackResets != 0 {
		t.Fatalf("target rollback resets = %d, want none while porcelain is dirty", targetRollbackResets)
	}
}

func TestRealProcessProfileMergeCleanlyTransactionalSerializesTwoClientsAndPreservesUnrelatedScratch(t *testing.T) {
	repo := initDivergedRepo(t)
	baseHead := runClientTestGitOutput(t, repo, "rev-parse", "main~1")
	runClientTestGit(t, repo, "checkout", "-q", "-b", "feature-b", baseHead)
	if err := os.WriteFile(filepath.Join(repo, "feature-b.txt"), []byte("feature-b\n"), 0o644); err != nil {
		t.Fatalf("write feature-b file: %v", err)
	}
	runClientTestGit(t, repo, "add", "feature-b.txt")
	runClientTestGit(t, repo, "commit", "-q", "-m", "feature b")
	runClientTestGit(t, repo, "checkout", "-q", "main")

	gateSyncDir := t.TempDir()
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	gate := `#!/bin/sh
set -eu
marker="$AZEDARACH_TEST_GATE_SYNC/$AZEDARACH_CANDIDATE_HEAD"
: >"$marker"
while [ "$(find "$AZEDARACH_TEST_GATE_SYNC" -type f | wc -l | tr -d ' ')" -lt 2 ]; do
  sleep 0.01
done
`
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"), []byte(gate), 0o755); err != nil {
		t.Fatalf("write integration gate: %v", err)
	}
	runClientTestGit(t, repo, "add", "scripts/git-merge-rebase-gate.sh")
	runClientTestGit(t, repo, "commit", "-q", "-m", "add synchronized integration gate")
	targetHead := runClientTestGitOutput(t, repo, "rev-parse", "HEAD")
	t.Setenv("AZEDARACH_TEST_GATE_SYNC", gateSyncDir)

	ownerClient := NewClient(NewExecRunner(repo), slog.Default())
	victimScratch, _ := addOwnedIntegrationScratch(t, ownerClient, repo, targetHead)
	victimSentinel := filepath.Join(victimScratch, "victim-untracked.txt")
	if err := os.WriteFile(victimSentinel, []byte("unrelated scratch bytes\n"), 0o644); err != nil {
		t.Fatalf("write victim sentinel: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type mergeOutcome struct {
		branch string
		result *MergeResult
		err    error
	}
	outcomes := make(chan mergeOutcome, 2)
	for _, branch := range []string{"feature", "feature-b"} {
		branch := branch
		go func() {
			client := NewClient(NewExecRunner(repo), slog.Default())
			result, err := client.MergeCleanlyTransactional(ctx, repo, branch)
			outcomes <- mergeOutcome{branch: branch, result: result, err: err}
		}()
	}

	var got []mergeOutcome
	for len(got) < 2 {
		select {
		case outcome := <-outcomes:
			got = append(got, outcome)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for two-client integration outcomes: %v", ctx.Err())
		}
	}
	var winner, loser *mergeOutcome
	for i := range got {
		outcome := &got[i]
		if outcome.err != nil {
			t.Fatalf("%s integration error = %v", outcome.branch, outcome.err)
		}
		if outcome.result != nil && outcome.result.Success {
			if winner != nil {
				t.Fatalf("both integrations reported success: %+v", got)
			}
			winner = outcome
		} else {
			loser = outcome
		}
	}
	if winner == nil || loser == nil {
		t.Fatalf("integration outcomes = %+v, want exactly one winner and one stale loser", got)
	}
	if loser.result == nil || !IsTransactionalMergeStaleTarget(loser.result) {
		t.Fatalf("losing integration result = %+v, want stale-target rejection", loser.result)
	}
	if len(winner.result.ValidationAttempts) != 1 || !winner.result.ValidationAttempts[0].Canonical {
		t.Fatalf("winning validation attempts = %+v, want one canonical candidate", winner.result.ValidationAttempts)
	}
	winnerHead := winner.result.ValidationAttempts[0].CandidateHead
	if head := runClientTestGitOutput(t, repo, "rev-parse", "HEAD"); head != winnerHead {
		t.Fatalf("target HEAD = %s, want sole canonical candidate %s", head, winnerHead)
	}
	if len(loser.result.ValidationAttempts) != 1 || loser.result.ValidationAttempts[0].Canonical {
		t.Fatalf("losing validation attempts = %+v, want noncanonical candidate", loser.result.ValidationAttempts)
	}
	attempt, found, err := ownerClient.CanonicalIntegrationValidation(ctx, repo, winnerHead)
	if err != nil || !found || !attempt.Canonical || attempt.CandidateHead != winnerHead {
		t.Fatalf("canonical receipt = (%+v, %t, %v), want exact winner %s", attempt, found, err, winnerHead)
	}
	if got, err := os.ReadFile(victimSentinel); err != nil || string(got) != "unrelated scratch bytes\n" {
		t.Fatalf("unrelated scratch sentinel = %q, %v; want preserved bytes", got, err)
	}
	worktrees := runClientTestGitOutput(t, repo, "worktree", "list", "--porcelain")
	victimRegistered := false
	for _, entry := range parseWorktreeEntries(worktrees) {
		if normalizeWorktreeLockKey(entry.Path) == normalizeWorktreeLockKey(victimScratch) {
			victimRegistered = true
			break
		}
	}
	if !victimRegistered {
		t.Fatalf("worktree list removed unrelated scratch %s:\n%s", victimScratch, worktrees)
	}
	if strings.Count(worktrees, "azedarach-integration-") != 1 {
		t.Fatalf("worktree list contains unexpected transactional scratch after completion:\n%s", worktrees)
	}
	if _, _, found, err := ownerClient.readIntegrationJournal(ctx, repo); err != nil || found {
		t.Fatalf("integration journal after two-client completion = (found=%t, err=%v), want removed", found, err)
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
		case clientTestArgsForWorktree(args, repo, "worktree", "list", "--porcelain"):
			mu.Lock()
			defer mu.Unlock()
			var entries strings.Builder
			for scratch, branch := range scratchBranches {
				fmt.Fprintf(&entries, "worktree %s\nHEAD desired-%s\ndetached\n\n", scratch, branch)
			}
			return entries.String(), nil
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
		case len(args) == 4 && args[0] == "-C" && strings.HasPrefix(args[1], os.TempDir()) && args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(gitDir, "worktrees", filepath.Base(args[1])), nil
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

func TestRealProcessProfileRuntimeStatusUsesLocalBaseBeforeCurrentRemoteBase(t *testing.T) {
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

func TestRealProcessProfileRuntimeStatusWithRemoteBasePreferenceUsesCurrentRemoteBase(t *testing.T) {
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

func TestChangedFilesBetweenRefTreesDoesNotUseMergeBase(t *testing.T) {
	var gotArgs []string
	runner := &mockRunner{
		runFunc: func(ctx context.Context, args ...string) (string, error) {
			gotArgs = append([]string(nil), args...)
			if len(args) >= 6 && args[0] == "diff" && args[1] == "--name-only" && args[2] == "-z" && args[3] == "origin/preview" && args[4] == "feature" {
				return "portable/雪\nline.txt\x00 leading-space.txt\x00trailing-space.txt \x00", nil
			}
			return "", fmt.Errorf("unexpected command: %v", args)
		},
	}

	client := NewClient(runner, slog.Default())
	files, err := client.ChangedFilesBetweenRefTrees(context.Background(), "/fake/worktree", "origin/preview", "feature")
	if err != nil {
		t.Fatalf("ChangedFilesBetweenRefTrees() error = %v", err)
	}
	if !reflect.DeepEqual(files, []string{"portable/雪\nline.txt", " leading-space.txt", "trailing-space.txt "}) {
		t.Fatalf("ChangedFilesBetweenRefTrees() = %v", files)
	}
	joined := strings.Join(gotArgs, " ")
	if strings.Contains(joined, "...") || strings.Contains(joined, "merge-base") {
		t.Fatalf("ChangedFilesBetweenRefTrees() used ancestry comparison: %v", gotArgs)
	}
}

func TestParseNULTerminatedGitPathsRejectsAmbiguousOutput(t *testing.T) {
	for _, output := range []string{"newline-delimited\n", "path\x00\x00"} {
		if paths, err := parseNULTerminatedGitPaths(output); err == nil {
			t.Fatalf("parseNULTerminatedGitPaths(%q) = %q, want error", output, paths)
		}
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

func TestSnapshotRefGraphCapturesMultipleRefsWithOneCommand(t *testing.T) {
	calls := 0
	base := strings.Repeat("0", 39) + "1"
	root := strings.Repeat("0", 39) + "2"
	active := strings.Repeat("0", 39) + "3"
	runner := &mockRunner{runFunc: func(_ context.Context, args ...string) (string, error) {
		calls++
		return fmt.Sprintf("\x1e%s\x00%s\x00refs/heads/root\x00az-closed: integrated\n\nshared.go\n\x1e%s\x00%s\x00refs/heads/active\x00az-active: working\n\nshared.go\n\x1e%s\x00\x00\x00base\n", root, base, active, base, base), nil
	}}
	snapshot, err := NewClient(runner, slog.Default()).SnapshotRefGraph(context.Background(), "/repo", []string{"root", "active"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("git calls = %d, want one", calls)
	}
	if !snapshot.Contains("root", root) || snapshot.Contains("active", root) {
		t.Fatalf("containment = root:%t active:%t", snapshot.Contains("root", root), snapshot.Contains("active", root))
	}
	if evidence := snapshot.IssueEvidence("root", "az-closed"); len(evidence) != 1 || evidence[0].Hash != root {
		t.Fatalf("evidence = %+v", evidence)
	}
	if files := snapshot.ChangedFilesExclusive("root", "active"); len(files) != 1 || files[0] != "shared.go" {
		t.Fatalf("exclusive files = %v", files)
	}
}

func TestSnapshotRefGraphKeepsValidRefsWhenProjectionContainsMissingBranch(t *testing.T) {
	repoDir := t.TempDir()
	runClientTestGit(t, repoDir, "init", "-q", "-b", "main")
	runClientTestGit(t, repoDir, "config", "user.name", "Azedarach Test")
	runClientTestGit(t, repoDir, "config", "user.email", "azedarach@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runClientTestGit(t, repoDir, "add", "tracked.txt")
	runClientTestGit(t, repoDir, "commit", "-q", "-m", "seed")
	snapshot, err := NewClient(NewExecRunner(repoDir), slog.Default()).SnapshotRefGraph(context.Background(), repoDir, []string{"main", "missing-branch"})
	if err != nil {
		t.Fatal(err)
	}
	mainTip := runClientTestGitOutput(t, repoDir, "rev-parse", "main")
	if !snapshot.Contains("main", mainTip) || snapshot.Tips["missing-branch"] != "" {
		t.Fatalf("tips = %+v, want valid main and ignored missing branch", snapshot.Tips)
	}
}

func TestSnapshotRefGraphMarksUnevenRefsIncompleteAtGlobalBound(t *testing.T) {
	runner := &mockRunner{runFunc: func(_ context.Context, _ ...string) (string, error) {
		var out strings.Builder
		for i := range maxRefGraphSnapshotCommits {
			hash := fmt.Sprintf("%040x", maxRefGraphSnapshotCommits-i)
			parent := ""
			if i+1 < maxRefGraphSnapshotCommits {
				parent = fmt.Sprintf("%040x", maxRefGraphSnapshotCommits-i-1)
			} else {
				parent = strings.Repeat("f", 40)
			}
			decoration := ""
			if i == 0 {
				decoration = "refs/heads/root"
			}
			fmt.Fprintf(&out, "\x1e%s\x00%s\x00%s\x00root history %d\n\nroot.txt\n", hash, parent, decoration, i)
		}
		return out.String(), nil
	}}
	snapshot, err := NewClient(runner, slog.Default()).SnapshotRefGraph(context.Background(), "/repo", []string{"root", "short-active"})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Truncated {
		t.Fatal("snapshot must report the shared graph bound")
	}
	if snapshot.RefComplete("root") || snapshot.RefComplete("short-active") {
		t.Fatalf("completeness root=%t active=%t, want both incomplete", snapshot.RefComplete("root"), snapshot.RefComplete("short-active"))
	}
}

func TestRefGraphChangedFilesExclusiveConservativelyIncludesRevertedFiles(t *testing.T) {
	base, first, revert := strings.Repeat("0", 39)+"1", strings.Repeat("0", 39)+"2", strings.Repeat("0", 39)+"3"
	snapshot := RefGraphSnapshot{
		Tips: map[string]string{"root": base, "active": revert},
		Commits: map[string]RefGraphCommit{
			base:   {Hash: base},
			first:  {Hash: first, Parents: []string{base}, ChangedFiles: []string{"reverted.txt"}},
			revert: {Hash: revert, Parents: []string{first}, ChangedFiles: []string{"reverted.txt"}},
		},
		Order: []string{revert, first, base},
	}
	snapshot.reachable = map[string]map[string]struct{}{
		"root":   snapshot.reachableFrom(base),
		"active": snapshot.reachableFrom(revert),
	}
	if files := snapshot.ChangedFilesExclusive("root", "active"); len(files) != 1 || files[0] != "reverted.txt" {
		t.Fatalf("exclusive touched files = %v, want conservative reverted-file inclusion", files)
	}
}
