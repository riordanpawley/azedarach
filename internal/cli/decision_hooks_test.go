package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestBuiltInDecisionCommandsForHook(t *testing.T) {
	cases := []struct {
		hook                     string
		preCommitMergeInProgress bool
		expected                 []string
	}{
		{hook: "pre-commit", expected: []string{"az decision sync"}},
		{hook: "pre-commit", preCommitMergeInProgress: true, expected: nil},
		{hook: "post-merge", expected: []string{"az decision import"}},
		{hook: "post-checkout", expected: []string{"az decision import"}},
		{hook: "post-rewrite", expected: []string{"az decision import"}},
		{hook: "post-commit", expected: nil},
		{hook: "unknown-hook", expected: nil},
		{hook: "", expected: nil},
	}
	for _, tc := range cases {
		t.Run(tc.hook, func(t *testing.T) {
			got := builtInDecisionCommandsForHook(tc.hook, "/repo path", tc.preCommitMergeInProgress)
			if tc.expected != nil {
				for i := range tc.expected {
					tc.expected[i] += " --project-dir '/repo path'"
				}
			}
			if !equalStringSlice(got, tc.expected) {
				t.Errorf("builtInDecisionCommandsForHook(%q, %v) = %v, want %v", tc.hook, tc.preCommitMergeInProgress, got, tc.expected)
			}
		})
	}
}

func TestGitHooksRunCommandRunsBuiltInDecisionSyncAndRestageOutsideMerge(t *testing.T) {
	projectDir := t.TempDir()
	if err := runGitCommandIsolated(projectDir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}

	fakeBin := t.TempDir()
	fakeAz := filepath.Join(fakeBin, "az")
	if err := os.WriteFile(fakeAz, []byte("#!/bin/sh\nif [ \"$1\" = decision ] && [ \"$2\" = sync ]; then mkdir -p docs/decisions && printf synced > docs/decisions/generated.md; fi\n"), 0o755); err != nil {
		t.Fatalf("write fake az: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config.DefaultConfig()
	if err := GitHooksRunCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, GitHooksRunOptions{ProjectDir: projectDir}); err != nil {
		t.Fatalf("GitHooksRunCommand error: %v", err)
	}

	statusCmd := exec.Command("git", "-C", projectDir, "status", "--porcelain", "--", "docs/decisions/generated.md")
	statusCmd.Env = gitExecEnvWithoutRoutingVars()
	out, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status generated decision: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "A  docs/decisions/generated.md" {
		t.Fatalf("generated decision git status = %q, want staged add", got)
	}
}

func TestGitHooksRunCommandRestagesDecisionsIntoHookIndex(t *testing.T) {
	projectDir := t.TempDir()
	if err := runGitCommandIsolated(projectDir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := runGitCommandIsolated(projectDir, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}
	if err := runGitCommandIsolated(projectDir, "config", "user.email", "test@example.com"); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := runGitCommandIsolated(projectDir, "add", "README.md"); err != nil {
		t.Fatalf("git add seed: %v", err)
	}
	if err := runGitCommandIsolated(projectDir, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit seed: %v", err)
	}

	hookIndex := filepath.Join(t.TempDir(), "hook-index")
	readTree := exec.Command("git", "-C", projectDir, "read-tree", "HEAD")
	readTree.Env = append(gitExecEnvWithoutRoutingVars(), "GIT_INDEX_FILE="+hookIndex)
	if err := readTree.Run(); err != nil {
		t.Fatalf("seed hook index: %v", err)
	}

	fakeBin := t.TempDir()
	fakeAz := filepath.Join(fakeBin, "az")
	if err := os.WriteFile(fakeAz, []byte("#!/bin/sh\nif [ \"$1\" = decision ] && [ \"$2\" = sync ]; then mkdir -p docs/decisions && printf synced > docs/decisions/generated.md; fi\n"), 0o755); err != nil {
		t.Fatalf("write fake az: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_INDEX_FILE", hookIndex)

	cfg := config.DefaultConfig()
	if err := GitHooksRunCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, GitHooksRunOptions{ProjectDir: projectDir}); err != nil {
		t.Fatalf("GitHooksRunCommand error: %v", err)
	}

	hookIndexFiles := exec.Command("git", "-C", projectDir, "ls-files", "--stage", "--", "docs/decisions/generated.md")
	hookIndexFiles.Env = append(gitExecEnvWithoutRoutingVars(), "GIT_INDEX_FILE="+hookIndex)
	out, err := hookIndexFiles.Output()
	if err != nil {
		t.Fatalf("git ls-files hook index: %v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatal("generated decision was not staged in hook index")
	}

	realIndexFiles := exec.Command("git", "-C", projectDir, "ls-files", "--stage", "--", "docs/decisions/generated.md")
	realIndexFiles.Env = gitExecEnvWithoutRoutingVars()
	out, err = realIndexFiles.Output()
	if err != nil {
		t.Fatalf("git ls-files real index: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("generated decision staged in real index = %q, want hook index only", strings.TrimSpace(string(out)))
	}
}

func TestGitHooksRunCommandSkipsBuiltInDecisionSyncAndRestageDuringMerge(t *testing.T) {
	projectDir := t.TempDir()
	if err := runGitCommandIsolated(projectDir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := runGitCommandIsolated(projectDir, "add", "README.md"); err != nil {
		t.Fatalf("git add seed: %v", err)
	}
	if err := runGitCommandIsolated(projectDir, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git", "MERGE_HEAD"), []byte(strings.Repeat("0", 40)+"\n"), 0o644); err != nil {
		t.Fatalf("write MERGE_HEAD: %v", err)
	}

	fakeBin := t.TempDir()
	fakeAz := filepath.Join(fakeBin, "az")
	if err := os.WriteFile(fakeAz, []byte("#!/bin/sh\nif [ \"$1\" = decision ] && [ \"$2\" = sync ]; then printf ran > .decision-sync-ran; fi\n"), 0o755); err != nil {
		t.Fatalf("write fake az: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	decisionPath := filepath.Join(projectDir, "docs", "decisions", "generated.md")
	if err := os.MkdirAll(filepath.Dir(decisionPath), 0o755); err != nil {
		t.Fatalf("mkdir decisions: %v", err)
	}
	if err := os.WriteFile(decisionPath, []byte("generated\n"), 0o644); err != nil {
		t.Fatalf("write generated decision: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.GitHooks.Commands["pre-commit"] = []string{"mkdir -p docs/spec && printf ok > docs/spec/.configured-ran"}
	if err := GitHooksRunCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, GitHooksRunOptions{ProjectDir: projectDir}); err != nil {
		t.Fatalf("GitHooksRunCommand error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".decision-sync-ran")); !os.IsNotExist(err) {
		t.Fatalf("built-in decision sync marker err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "docs", "spec", ".configured-ran")); err != nil {
		t.Fatalf("configured pre-commit command did not run: %v", err)
	}

	statusCmd := exec.Command("git", "-C", projectDir, "status", "--porcelain", "--", "docs/decisions/generated.md")
	statusCmd.Env = gitExecEnvWithoutRoutingVars()
	out, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status generated decision: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "?? docs/decisions/generated.md" {
		t.Fatalf("generated decision git status = %q, want untracked", got)
	}
}

func TestGitMergeInProgressReadsWorktreeGitDirPointer(t *testing.T) {
	projectDir := t.TempDir()
	gitDir := filepath.Join(t.TempDir(), "worktrees", "feature")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatalf("write .git pointer: %v", err)
	}

	merge, err := gitMergeInProgress(projectDir)
	if err != nil {
		t.Fatalf("gitMergeInProgress without MERGE_HEAD error: %v", err)
	}
	if merge {
		t.Fatal("gitMergeInProgress without MERGE_HEAD = true, want false")
	}

	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(strings.Repeat("0", 40)+"\n"), 0o644); err != nil {
		t.Fatalf("write MERGE_HEAD: %v", err)
	}
	merge, err = gitMergeInProgress(projectDir)
	if err != nil {
		t.Fatalf("gitMergeInProgress with MERGE_HEAD error: %v", err)
	}
	if !merge {
		t.Fatal("gitMergeInProgress with MERGE_HEAD = false, want true")
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
