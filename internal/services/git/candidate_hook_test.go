package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealProcessProfileRebaseGateRunsOnlyAfterResolvedCandidate(t *testing.T) {
	t.Run("clean rebase", func(t *testing.T) {
		repo, evidence := initCandidateHookRebaseRepo(t, false)
		runCandidateHookGit(t, repo, nil, "checkout", "-q", "feature")
		runCandidateHookGit(t, repo, candidateHookPathEnv(), "rebase", "main")
		assertCandidateHookNotStarted(t, evidence)
		runCandidateGate(t, repo)
		assertCandidateHookEvidence(t, repo, evidence)
	})

	t.Run("conflicting rebase", func(t *testing.T) {
		repo, evidence := initCandidateHookRebaseRepo(t, true)
		runCandidateHookGit(t, repo, nil, "checkout", "-q", "feature")
		cmd := exec.Command("git", "rebase", "main")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), candidateHookPathEnv()...)
		if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "CONFLICT") {
			t.Fatalf("git rebase main error = %v, output=%s, want conflict", err, output)
		}
		if _, err := os.Stat(evidence); !os.IsNotExist(err) {
			t.Fatalf("gate evidence stat error = %v, want no pre-reconciliation gate", err)
		}
		if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
			t.Fatalf("write conflict resolution: %v", err)
		}
		runCandidateHookGit(t, repo, nil, "add", "conflict.txt")
		runCandidateHookGit(t, repo, append(candidateHookPathEnv(), "GIT_EDITOR=true"), "rebase", "--continue")
		assertCandidateHookNotStarted(t, evidence)
		runCandidateGate(t, repo)
		assertCandidateHookEvidence(t, repo, evidence)
	})
}

func TestRepositoryMergeRebaseHooksDoNotInvokeAggregateGate(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range []string{"pre-rebase", "pre-merge-commit", "pre-commit", "post-rewrite", "post-merge"} {
		content, err := os.ReadFile(filepath.Join(root, ".githooks", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(content), "git-merge-rebase-gate.sh") {
			t.Fatalf("%s invokes aggregate gate during reconciliation", name)
		}
	}
}

func initCandidateHookRebaseRepo(t *testing.T, conflict bool) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runCandidateHookGit(t, repo, nil, "init", "-q", "-b", "main")
	runCandidateHookGit(t, repo, nil, "config", "user.email", "test@example.com")
	runCandidateHookGit(t, repo, nil, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runCandidateHookGit(t, repo, nil, "add", "conflict.txt")
	runCandidateHookGit(t, repo, nil, "commit", "-q", "-m", "base")
	runCandidateHookGit(t, repo, nil, "checkout", "-q", "-b", "feature")
	featurePath := filepath.Join(repo, "feature.txt")
	if conflict {
		featurePath = filepath.Join(repo, "conflict.txt")
	}
	if err := os.WriteFile(featurePath, []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	runCandidateHookGit(t, repo, nil, "add", filepath.Base(featurePath))
	runCandidateHookGit(t, repo, nil, "commit", "-q", "-m", "feature")
	runCandidateHookGit(t, repo, nil, "checkout", "-q", "main")
	mainPath := filepath.Join(repo, "main.txt")
	if conflict {
		mainPath = filepath.Join(repo, "conflict.txt")
	}
	if err := os.WriteFile(mainPath, []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}
	runCandidateHookGit(t, repo, nil, "add", filepath.Base(mainPath))
	runCandidateHookGit(t, repo, nil, "commit", "-q", "-m", "main")

	hooksDir := filepath.Join(repo, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	sourceRoot := filepath.Join("..", "..", "..")
	for _, name := range []string{"pre-rebase", "post-rewrite"} {
		content, err := os.ReadFile(filepath.Join(sourceRoot, ".githooks", name))
		if err != nil {
			t.Fatalf("read %s hook: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(hooksDir, name), content, 0o755); err != nil {
			t.Fatalf("write %s hook: %v", name, err)
		}
	}
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	evidence := filepath.Join(t.TempDir(), "candidate-head")
	gate := "#!/bin/sh\nset -eu\nprintf '%s|%s\\n' \"$(git rev-parse HEAD)\" \"$(git status --porcelain)\" >\"$AZEDARACH_TEST_GATE_EVIDENCE\"\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate.sh"), []byte(gate), 0o755); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(".githooks/\nscripts/\n"), 0o644); err != nil {
		t.Fatalf("exclude test hooks and gate: %v", err)
	}
	runCandidateHookGit(t, repo, nil, "config", "core.hooksPath", ".githooks")
	t.Setenv("AZEDARACH_TEST_GATE_EVIDENCE", evidence)
	return repo, evidence
}

func assertCandidateHookEvidence(t *testing.T, repo, evidence string) {
	t.Helper()
	head := strings.TrimSpace(runCandidateHookGit(t, repo, nil, "rev-parse", "HEAD"))
	content, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatalf("read candidate gate evidence: %v", err)
	}
	if got, want := string(content), head+"|\n"; got != want {
		t.Fatalf("candidate gate evidence = %q, want %q", got, want)
	}
}

func assertCandidateHookNotStarted(t *testing.T, evidence string) {
	t.Helper()
	if _, err := os.Stat(evidence); !os.IsNotExist(err) {
		t.Fatalf("gate evidence stat error = %v, want no hook-started aggregate", err)
	}
}

func runCandidateGate(t *testing.T, repo string) {
	t.Helper()
	head := strings.TrimSpace(runCandidateHookGit(t, repo, nil, "rev-parse", "HEAD"))
	cmd := exec.Command(filepath.Join(repo, "scripts", "git-merge-rebase-gate.sh"))
	cmd.Dir = repo
	cmd.Env = gitEnvWithOverrides(sanitizedGitEnv(os.Environ()), []string{"AZEDARACH_CANDIDATE_HEAD=" + head})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run explicit candidate gate: %v\n%s", err, output)
	}
}

func runCandidateHookGit(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func candidateHookPathEnv() []string {
	return []string{"PATH=/usr/bin:/bin"}
}
