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
		hook     string
		expected []string
	}{
		{hook: "pre-commit", expected: nil},
		{hook: "post-merge", expected: []string{"az decision import"}},
		{hook: "post-checkout", expected: []string{"az decision import"}},
		{hook: "post-rewrite", expected: []string{"az decision import"}},
		{hook: "post-commit", expected: nil},
		{hook: "unknown-hook", expected: nil},
		{hook: "", expected: nil},
	}
	for _, tc := range cases {
		t.Run(tc.hook, func(t *testing.T) {
			got := builtInDecisionCommandsForHook(tc.hook, "/repo path")
			if tc.expected != nil {
				for i := range tc.expected {
					tc.expected[i] += " --project-dir '/repo path'"
				}
			}
			if !equalStringSlice(got, tc.expected) {
				t.Errorf("builtInDecisionCommandsForHook(%q) = %v, want %v", tc.hook, got, tc.expected)
			}
		})
	}
}

func TestGitHooksPreCommitDoesNotAlterConcurrentWorktreeCommit(t *testing.T) {
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
	decisionPath := filepath.Join(baseDir, "docs", "decisions", "dec-1-original.md")
	if err := os.MkdirAll(filepath.Dir(decisionPath), 0o755); err != nil {
		t.Fatalf("mkdir decisions: %v", err)
	}
	if err := os.WriteFile(decisionPath, []byte("# dec-1: Original\n"), 0o644); err != nil {
		t.Fatalf("write decision: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := runGitCommandIsolated(baseDir, "add", "."); err != nil {
		t.Fatalf("git add seed: %v", err)
	}
	if err := runGitCommandIsolated(baseDir, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit seed: %v", err)
	}

	worktreeA := filepath.Join(t.TempDir(), "worktree-a")
	worktreeB := filepath.Join(t.TempDir(), "worktree-b")
	if err := runGitCommandIsolated(baseDir, "worktree", "add", worktreeA, "-b", "decision-a"); err != nil {
		t.Fatalf("git worktree add A: %v", err)
	}
	if err := runGitCommandIsolated(baseDir, "worktree", "add", worktreeB, "-b", "commit-b"); err != nil {
		t.Fatalf("git worktree add B: %v", err)
	}

	// Model a decision mutation from worktree A in the shared store. If B's
	// pre-commit invokes decision sync, the fake command exposes the leak by
	// rewriting B's decision markdown and recording the invocation.
	sharedStore := filepath.Join(t.TempDir(), "shared-decision-store")
	if err := os.WriteFile(sharedStore, []byte("decision changed from worktree A\n"), 0o644); err != nil {
		t.Fatalf("write shared decision mutation: %v", err)
	}
	fakeBin := t.TempDir()
	fakeAz := filepath.Join(fakeBin, "az")
	callLog := filepath.Join(t.TempDir(), "az-calls")
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> \"$AZ_CALL_LOG\"\nif [ \"$1\" = decision ] && [ \"$2\" = sync ]; then cp \"$SHARED_STORE\" docs/decisions/dec-1-original.md; fi\n"
	if err := os.WriteFile(fakeAz, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake az: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AZ_CALL_LOG", callLog)
	t.Setenv("SHARED_STORE", sharedStore)

	if err := os.WriteFile(filepath.Join(worktreeB, "README.md"), []byte("worktree B change\n"), 0o644); err != nil {
		t.Fatalf("write B change: %v", err)
	}
	if err := runGitCommandIsolated(worktreeB, "add", "README.md"); err != nil {
		t.Fatalf("stage B change: %v", err)
	}
	before := gitCommandOutput(t, worktreeB, "diff", "--cached", "--binary")
	if err := GitHooksHookCommand(&Dependencies{RepoDir: worktreeB, Config: config.DefaultConfig()}, GitHooksHookOptions{ProjectDir: worktreeB, Hook: "pre-commit"}); err != nil {
		t.Fatalf("pre-commit hook: %v", err)
	}
	after := gitCommandOutput(t, worktreeB, "diff", "--cached", "--binary")
	if before != after {
		t.Fatalf("staged commit changed after concurrent decision mutation\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got := strings.TrimSpace(gitCommandOutput(t, worktreeB, "status", "--porcelain", "--", "docs/decisions")); got != "" {
		t.Fatalf("worktree B decision markdown became dirty: %q", got)
	}
	if _, err := os.Stat(callLog); !os.IsNotExist(err) {
		t.Fatalf("pre-commit invoked az decision command; call log err=%v", err)
	}
}

func TestGitHooksHookCommandRunsConflictSafeImportHooks(t *testing.T) {
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
	cfg := config.DefaultConfig()
	if err := GitHooksHookCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, GitHooksHookOptions{ProjectDir: projectDir, Hook: "post-merge"}); err != nil {
		t.Fatalf("GitHooksHookCommand error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".decision-import-ran")); err != nil {
		t.Fatalf("decision import hook did not run: %v", err)
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

func gitCommandOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitExecEnvWithoutRoutingVars()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
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
