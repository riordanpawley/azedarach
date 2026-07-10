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
	isolateNestedGitEnvironment(t)
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

func TestGitHooksRunCommandSkipsBuiltInDecisionSyncAndRestageWhenEnvSet(t *testing.T) {
	isolateNestedGitEnvironment(t)
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
	t.Setenv("AZEDARACH_SKIP_DECISION_SYNC", "1")

	decisionPath := filepath.Join(projectDir, "docs", "decisions", "existing.md")
	if err := os.MkdirAll(filepath.Dir(decisionPath), 0o755); err != nil {
		t.Fatalf("mkdir decisions: %v", err)
	}
	if err := os.WriteFile(decisionPath, []byte("existing\n"), 0o644); err != nil {
		t.Fatalf("write existing decision: %v", err)
	}

	cfg := config.DefaultConfig()
	if err := GitHooksRunCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, GitHooksRunOptions{ProjectDir: projectDir}); err != nil {
		t.Fatalf("GitHooksRunCommand error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, "docs", "decisions", "generated.md")); !os.IsNotExist(err) {
		t.Fatalf("generated decision err = %v, want not exist", err)
	}

	statusCmd := exec.Command("git", "-C", projectDir, "status", "--porcelain", "--", "docs/decisions/existing.md")
	statusCmd.Env = gitExecEnvWithoutRoutingVars()
	out, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status existing decision: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "?? docs/decisions/existing.md" {
		t.Fatalf("existing decision git status = %q, want untracked", got)
	}
}

func TestGitHooksHookCommandDecisionSyncSkipDoesNotDisableImportHooks(t *testing.T) {
	isolateNestedGitEnvironment(t)
	projectDir := t.TempDir()
	if err := runGitCommandIsolated(projectDir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}

	fakeBin := t.TempDir()
	fakeAz := filepath.Join(fakeBin, "az")
	if err := os.WriteFile(fakeAz, []byte("#!/bin/sh\nif [ \"$1\" = decision ] && [ \"$2\" = import ]; then printf imported > .decision-import-ran; fi\n"), 0o755); err != nil {
		t.Fatalf("write fake az: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AZEDARACH_SKIP_DECISION_SYNC", "1")

	cfg := config.DefaultConfig()
	if err := GitHooksHookCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, GitHooksHookOptions{ProjectDir: projectDir, Hook: "post-merge"}); err != nil {
		t.Fatalf("GitHooksHookCommand error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".decision-import-ran")); err != nil {
		t.Fatalf("decision import hook did not run: %v", err)
	}
}

func TestGitHooksRunCommandRestagesDecisionsIntoHookIndex(t *testing.T) {
	isolateNestedGitEnvironment(t)
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

func TestGitHooksRunCommandUsesCurrentWorktreeForImplicitProjectDir(t *testing.T) {
	isolateNestedGitEnvironment(t)
	baseDir := t.TempDir()
	if err := runGitCommandIsolated(baseDir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := runGitCommandIsolated(baseDir, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}
	if err := runGitCommandIsolated(baseDir, "config", "user.email", "test@example.com"); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := runGitCommandIsolated(baseDir, "add", "README.md"); err != nil {
		t.Fatalf("git add seed: %v", err)
	}
	if err := runGitCommandIsolated(baseDir, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit seed: %v", err)
	}

	worktreeDir := filepath.Join(t.TempDir(), "linked-worktree")
	if err := runGitCommandIsolated(baseDir, "worktree", "add", worktreeDir, "-b", "hook-worktree"); err != nil {
		t.Fatalf("git worktree add: %v", err)
	}

	hookIndex := filepath.Join(t.TempDir(), "hook-index")
	readTree := exec.Command("git", "-C", worktreeDir, "read-tree", "HEAD")
	readTree.Env = append(gitExecEnvWithoutRoutingVars(), "GIT_INDEX_FILE="+hookIndex)
	if err := readTree.Run(); err != nil {
		t.Fatalf("seed hook index: %v", err)
	}

	fakeBin := t.TempDir()
	fakeAz := filepath.Join(fakeBin, "az")
	fakeAzScript := `#!/bin/sh
set -eu
if [ "$1" = decision ] && [ "$2" = sync ]; then
	project=""
	shift 2
	while [ "$#" -gt 0 ]; do
		if [ "$1" = "--project-dir" ]; then
			shift
			project="$1"
		fi
		shift || true
	done
	if [ -z "$project" ]; then
		project="$(pwd)"
	fi
	mkdir -p "$project/docs/decisions"
	printf '%s\n' "$project" > "$project/docs/decisions/generated.md"
fi
`
	if err := os.WriteFile(fakeAz, []byte(fakeAzScript), 0o755); err != nil {
		t.Fatalf("write fake az: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_INDEX_FILE", hookIndex)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("chdir worktree: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(wd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()

	cfg := config.DefaultConfig()
	if err := GitHooksRunCommand(&Dependencies{RepoDir: baseDir, Config: cfg}, GitHooksRunOptions{}); err != nil {
		t.Fatalf("GitHooksRunCommand error: %v", err)
	}

	worktreeDecisionPath := filepath.Join(worktreeDir, "docs", "decisions", "generated.md")
	data, err := os.ReadFile(worktreeDecisionPath)
	if err != nil {
		t.Fatalf("read worktree generated decision: %v", err)
	}
	wantWorktree, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		wantWorktree = worktreeDir
	}
	if got := strings.TrimSpace(string(data)); got != wantWorktree {
		t.Fatalf("generated decision content = %q, want worktree project dir %q", got, wantWorktree)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "docs", "decisions", "generated.md")); !os.IsNotExist(err) {
		t.Fatalf("base checkout generated decision err = %v, want not exist", err)
	}

	hookIndexBlob := exec.Command("git", "-C", worktreeDir, "show", ":docs/decisions/generated.md")
	hookIndexBlob.Env = append(gitExecEnvWithoutRoutingVars(), "GIT_INDEX_FILE="+hookIndex)
	out, err := hookIndexBlob.Output()
	if err != nil {
		t.Fatalf("git show generated decision from hook index: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != wantWorktree {
		t.Fatalf("hook index generated decision = %q, want %q", got, wantWorktree)
	}
}

func TestGitHooksRunCommandSkipsBuiltInDecisionSyncAndRestageDuringMerge(t *testing.T) {
	isolateNestedGitEnvironment(t)
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

func TestGitHooksRunCommandNestedRepositoriesIgnoreFullOuterGitEnvironment(t *testing.T) {
	localVars := gitLocalEnvironmentVariableNames(t)
	poisoned := make(map[string]string, len(localVars)+4)
	for _, key := range localVars {
		poisoned[key] = "outer-hook-value"
	}
	poisoned["GIT_CONFIG_KEY_0"] = "core.worktree"
	poisoned["GIT_CONFIG_VALUE_0"] = "/outer/worktree"
	poisoned["GIT_QUARANTINE_PATH"] = "/outer/quarantine"
	poisoned["GIT_REFLOG_ACTION"] = "outer merge hook"

	cmd := exec.Command(os.Args[0],
		"-test.run", "^TestGitHooksRunCommand(RunsBuiltInDecisionSyncAndRestageOutsideMerge|RestagesDecisionsIntoHookIndex|UsesCurrentWorktreeForImplicitProjectDir)$",
		"-test.count=1",
	)
	cmd.Env = gitEnvironmentWithOverrides(poisoned)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("decision hook tests under full outer Git environment: %v\n%s", err, output)
	}
}

func isolateNestedGitEnvironment(t *testing.T) {
	t.Helper()
	keys := make(map[string]struct{})
	for _, key := range gitLocalEnvironmentVariableNames(t) {
		keys[key] = struct{}{}
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			keys[key] = struct{}{}
		}
	}
	for key := range keys {
		value, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if present {
				if err := os.Setenv(key, value); err != nil {
					t.Errorf("restore %s: %v", key, err)
				}
				return
			}
			if err := os.Unsetenv(key); err != nil {
				t.Errorf("clear %s: %v", key, err)
			}
		})
	}
}

func gitLocalEnvironmentVariableNames(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--local-env-vars")
	cmd.Env = gitEnvironmentWithOverrides(nil)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("list Git local environment variables: %v", err)
	}
	return strings.Fields(string(output))
}

func gitEnvironmentWithOverrides(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
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
