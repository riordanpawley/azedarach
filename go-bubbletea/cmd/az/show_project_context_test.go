package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunCLIShowJSONProjectContextResolvesRegisteredProjectFromNestedPath(t *testing.T) {
	workspaceRoot := setupProjectCommandSandbox(t)
	projectPath := createProjectGitRepoAt(t, workspaceRoot, "azedarach-main")
	writeProjectsRegistryForTest(t, []config.Project{
		{Name: "azedarach-main", Path: projectPath},
	}, "azedarach-main")

	nestedPath := filepath.Join(projectPath, "pkg", "feature")
	if err := os.MkdirAll(nestedPath, 0o755); err != nil {
		t.Fatalf("failed to create nested project path %q: %v", nestedPath, err)
	}
	if err := os.Chdir(nestedPath); err != nil {
		t.Fatalf("failed to change directory to nested path %q: %v", nestedPath, err)
	}

	stubShowDependencies(t, func(requestedID string) ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       requestedID,
				Title:    "Nested project context",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"show", "AZE-100", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Project struct {
			ID              string `json:"id"`
			Path            string `json:"path"`
			CanonicalDBPath string `json:"canonicalDbPath"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	expectedCanonicalDBPath := filepath.Join(projectPath, ".azedarach", "azedarach.db")
	if !samePathForTest(envelope.Project.Path, projectPath) {
		t.Fatalf("expected project.path=%q, got %q", projectPath, envelope.Project.Path)
	}
	if envelope.Project.ID != "azedarach-main" {
		t.Fatalf("expected project.id=azedarach-main, got %q", envelope.Project.ID)
	}
	if !samePathForTest(envelope.Project.CanonicalDBPath, expectedCanonicalDBPath) {
		t.Fatalf(
			"expected project.canonicalDbPath=%q, got %q",
			expectedCanonicalDBPath,
			envelope.Project.CanonicalDBPath,
		)
	}
}

func TestRunCLIShowJSONProjectContextResolvesSiblingWorktreeToRegisteredBaseProject(t *testing.T) {
	workspaceRoot := setupProjectCommandSandbox(t)
	baseProjectPath := createProjectGitRepoAt(t, workspaceRoot, "azedarach-main")
	writeProjectsRegistryForTest(t, []config.Project{
		{Name: "azedarach-main", Path: baseProjectPath},
	}, "azedarach-main")

	worktreeName := "feature-branch"
	worktreeGitDir := filepath.Join(baseProjectPath, ".git", "worktrees", worktreeName)
	if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
		t.Fatalf("failed to create worktree gitdir %q: %v", worktreeGitDir, err)
	}

	siblingWorktreePath := filepath.Join(workspaceRoot, "azedarach-main-feature")
	if err := os.MkdirAll(siblingWorktreePath, 0o755); err != nil {
		t.Fatalf("failed to create sibling worktree root %q: %v", siblingWorktreePath, err)
	}

	gitPointer := "gitdir: " + worktreeGitDir + "\n"
	if err := os.WriteFile(filepath.Join(siblingWorktreePath, ".git"), []byte(gitPointer), 0o644); err != nil {
		t.Fatalf("failed to write worktree .git pointer: %v", err)
	}

	nestedWorktreePath := filepath.Join(siblingWorktreePath, "internal", "service")
	if err := os.MkdirAll(nestedWorktreePath, 0o755); err != nil {
		t.Fatalf("failed to create nested sibling worktree path %q: %v", nestedWorktreePath, err)
	}
	if err := os.Chdir(nestedWorktreePath); err != nil {
		t.Fatalf("failed to change directory to nested sibling worktree path %q: %v", nestedWorktreePath, err)
	}

	stubShowDependencies(t, func(requestedID string) ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       requestedID,
				Title:    "Sibling worktree project context",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"show", "AZE-101", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Project struct {
			ID              string `json:"id"`
			Path            string `json:"path"`
			CanonicalDBPath string `json:"canonicalDbPath"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	expectedCanonicalDBPath := filepath.Join(baseProjectPath, ".azedarach", "azedarach.db")
	if !samePathForTest(envelope.Project.Path, baseProjectPath) {
		t.Fatalf("expected project.path=%q, got %q", baseProjectPath, envelope.Project.Path)
	}
	if envelope.Project.ID != "azedarach-main" {
		t.Fatalf("expected project.id=azedarach-main, got %q", envelope.Project.ID)
	}
	if !samePathForTest(envelope.Project.CanonicalDBPath, expectedCanonicalDBPath) {
		t.Fatalf(
			"expected project.canonicalDbPath=%q, got %q",
			expectedCanonicalDBPath,
			envelope.Project.CanonicalDBPath,
		)
	}
}

func TestRunCLIShowJSONProjectContextFallsBackToDefaultRegisteredProject(t *testing.T) {
	workspaceRoot := setupProjectCommandSandbox(t)
	defaultProjectPath := createProjectGitRepoAt(t, workspaceRoot, "azedarach-main")
	writeProjectsRegistryForTest(t, []config.Project{
		{Name: "azedarach-main", Path: defaultProjectPath},
	}, "azedarach-main")

	unregisteredPath := filepath.Join(workspaceRoot, "outside-registered-project")
	if err := os.MkdirAll(unregisteredPath, 0o755); err != nil {
		t.Fatalf("failed to create unregistered path %q: %v", unregisteredPath, err)
	}
	if err := os.Chdir(unregisteredPath); err != nil {
		t.Fatalf("failed to change directory to unregistered path %q: %v", unregisteredPath, err)
	}

	stubShowDependencies(t, func(requestedID string) ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       requestedID,
				Title:    "Default project fallback context",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"show", "AZE-102", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Project struct {
			ID              string `json:"id"`
			Path            string `json:"path"`
			CanonicalDBPath string `json:"canonicalDbPath"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	expectedCanonicalDBPath := filepath.Join(defaultProjectPath, ".azedarach", "azedarach.db")
	if !samePathForTest(envelope.Project.Path, defaultProjectPath) {
		t.Fatalf("expected project.path=%q, got %q", defaultProjectPath, envelope.Project.Path)
	}
	if envelope.Project.ID != "azedarach-main" {
		t.Fatalf("expected project.id=azedarach-main, got %q", envelope.Project.ID)
	}
	if !samePathForTest(envelope.Project.CanonicalDBPath, expectedCanonicalDBPath) {
		t.Fatalf(
			"expected project.canonicalDbPath=%q, got %q",
			expectedCanonicalDBPath,
			envelope.Project.CanonicalDBPath,
		)
	}
}
