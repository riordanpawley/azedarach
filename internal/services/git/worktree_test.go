package git

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockRunner implements CommandRunner for testing.
type MockRunner struct {
	commands []string                                                  // Record of commands run
	handler  func(ctx context.Context, args ...string) (string, error) // Custom handler
}

func NewMockRunner() *MockRunner {
	return &MockRunner{
		commands: []string{},
	}
}

func (m *MockRunner) Run(ctx context.Context, args ...string) (string, error) {
	m.commands = append(m.commands, strings.Join(args, " "))
	if m.handler != nil {
		return m.handler(ctx, args...)
	}
	return "", nil
}

// AssertCommand checks if a command was run.
func (m *MockRunner) AssertCommand(t *testing.T, expected string) {
	for _, cmd := range m.commands {
		if cmd == expected {
			return
		}
	}
	t.Errorf("expected command %q not found in %v", expected, m.commands)
}

// Reset clears recorded commands.
func (m *MockRunner) Reset() {
	m.commands = []string{}
}

func TestWorktreeManager_Create(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "issue-123"
	baseBranch := "main"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock git user config for deterministic branch author
		if len(args) > 1 && args[0] == "config" && args[1] == "user.name" {
			return "Riordan Pawley\n", nil
		}
		// Mock 'worktree list' to return empty (no existing worktrees)
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return "", nil
		}
		// Mock 'worktree add' to succeed
		if len(args) > 0 && args[0] == "worktree" && args[1] == "add" {
			return "", nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	worktree, err := manager.Create(ctx, issueID, baseBranch)

	require.NoError(t, err)
	assert.NotNil(t, worktree)
	assert.Equal(t, issueID, worktree.IssueID)
	assert.Equal(t, "riordanpawley/issue-123/issue-123", worktree.Branch)
	assert.Equal(t, "/home/user/test-repo-issue-123", worktree.Path)

	// Verify the command was called correctly
	expectedCmd := "worktree add -b riordanpawley/issue-123/issue-123 /home/user/test-repo-issue-123 main"
	mock.AssertCommand(t, expectedCmd)
}

func TestWorktreeManager_Create_AlreadyExists(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "issue-123"
	baseBranch := "main"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "config" && args[1] == "user.name" {
			return "Riordan Pawley\n", nil
		}
		// Mock 'worktree list' to return existing worktree
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	_, err := manager.Create(ctx, issueID, baseBranch)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeAlreadyExists)
	assert.Contains(t, err.Error(), "already exists")
}

func TestWorktreeManager_CreateWithTitle_UsesDeterministicBranchName(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "CHE-3002"
	baseBranch := "main"
	issueTitle := "Migrate prep lists to db with this title being way too long"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "config" && args[1] == "user.name" {
			return "Riordan Pawley\n", nil
		}
		if len(args) > 1 && args[0] == "worktree" && args[1] == "list" {
			return "", nil
		}
		return "", nil
	}

	manager := NewWorktreeManager(mock, repoDir, slog.Default())
	worktree, err := manager.CreateWithTitle(ctx, issueID, issueTitle, baseBranch)
	require.NoError(t, err)

	assert.Equal(t, "riordanpawley/che-3002/migrate-prep-lists-to-db", worktree.Branch)
	mock.AssertCommand(t, "worktree add -b riordanpawley/che-3002/migrate-prep-lists-to-db /home/user/test-repo-CHE-3002 main")
}

func TestWorktreeManager_CreateWithTitle_ReusesExistingBranchOnRetry(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "bhh"
	baseBranch := "main"
	issueTitle := "massive issue with cli"
	branchName := "testuser/bhh/massive-issue-with-cli"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "config" && args[1] == "user.name" {
			return "testuser\n", nil
		}
		if len(args) > 1 && args[0] == "worktree" && args[1] == "list" {
			return "", nil
		}
		if len(args) >= 6 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
			return "", fmt.Errorf("git worktree add -b %s /home/user/test-repo-bhh main failed: exit status 255: Preparing worktree (new branch '%s')\nfatal: a branch named '%s' already exists", branchName, branchName, branchName)
		}
		if len(args) == 4 && args[0] == "worktree" && args[1] == "add" && args[2] == "/home/user/test-repo-bhh" && args[3] == branchName {
			return "", nil
		}
		return "", nil
	}

	manager := NewWorktreeManager(mock, repoDir, slog.Default())
	worktree, err := manager.CreateWithTitle(ctx, issueID, issueTitle, baseBranch)
	require.NoError(t, err)
	require.NotNil(t, worktree)
	assert.Equal(t, branchName, worktree.Branch)

	mock.AssertCommand(t, "worktree add -b "+branchName+" /home/user/test-repo-bhh main")
	mock.AssertCommand(t, "worktree add /home/user/test-repo-bhh "+branchName)
}

func TestWorktreeManager_Delete(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "issue-123"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list' to return existing worktree
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123
`, nil
		}
		// Mock 'worktree remove' to succeed
		if len(args) > 0 && args[0] == "worktree" && args[1] == "remove" {
			return "", nil
		}
		// Mock 'branch -D' to succeed
		if len(args) > 0 && args[0] == "branch" && args[1] == "-D" {
			return "", nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	err := manager.Delete(ctx, issueID)

	require.NoError(t, err)
	mock.AssertCommand(t, "worktree remove /home/user/test-repo-issue-123")
	mock.AssertCommand(t, "branch -D az/issue-123")
}

func TestWorktreeManager_DeleteWithOptions_UsesForceWhenRequested(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "issue-123"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123
`, nil
		}
		if len(args) > 1 && args[0] == "worktree" && args[1] == "remove" {
			return "", nil
		}
		if len(args) > 1 && args[0] == "branch" && args[1] == "-D" {
			return "", nil
		}
		return "", nil
	}

	manager := NewWorktreeManager(mock, repoDir, slog.Default())

	err := manager.DeleteWithOptions(ctx, issueID, true)

	require.NoError(t, err)
	mock.AssertCommand(t, "worktree remove --force /home/user/test-repo-issue-123")
	mock.AssertCommand(t, "branch -D az/issue-123")
}

func TestWorktreeManager_Delete_NotFound(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "nonexistent"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list' to return empty
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	err := manager.Delete(ctx, issueID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorktreeManager_Get(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "issue-123"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list'
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123

worktree /home/user/test-repo-issue-456
HEAD ghi789
branch refs/heads/az/issue-456
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	worktree, err := manager.Get(ctx, issueID)

	require.NoError(t, err)
	assert.NotNil(t, worktree)
	assert.Equal(t, issueID, worktree.IssueID)
	assert.Equal(t, "az/issue-123", worktree.Branch)
	assert.Equal(t, "/home/user/test-repo-issue-123", worktree.Path)
}

func TestWorktreeManager_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	issueID := "nonexistent"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list' with no matching worktree
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	_, err := manager.Get(ctx, issueID)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeNotFound)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorktreeManager_List(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list'
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123

worktree /home/user/test-repo-issue-456
HEAD ghi789
branch refs/heads/az/issue-456

worktree /home/user/test-repo-feature
HEAD jkl012
branch refs/heads/feature/something
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	worktrees, err := manager.List(ctx)

	require.NoError(t, err)
	assert.Len(t, worktrees, 2) // Only issue-linked branch naming formats

	// Check first worktree
	assert.Equal(t, "issue-123", worktrees[0].IssueID)
	assert.Equal(t, "az/issue-123", worktrees[0].Branch)
	assert.Equal(t, "/home/user/test-repo-issue-123", worktrees[0].Path)

	// Check second worktree
	assert.Equal(t, "issue-456", worktrees[1].IssueID)
	assert.Equal(t, "az/issue-456", worktrees[1].Branch)
	assert.Equal(t, "/home/user/test-repo-issue-456", worktrees[1].Path)
}

func TestWorktreeManager_List_Empty(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list' with only main worktree
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	worktrees, err := manager.List(ctx)

	require.NoError(t, err)
	assert.Len(t, worktrees, 0)
}

func TestWorktreeManager_Exists(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list'
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	// Test existing worktree
	exists, err := manager.Exists(ctx, "issue-123")
	require.NoError(t, err)
	assert.True(t, exists)

	// Test non-existing worktree
	exists, err = manager.Exists(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestWorktreeManager_ErrorHandling(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Simulate git command failure
		return "", fmt.Errorf("git command failed")
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	// Test List error
	_, err := manager.List(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list worktrees")

	// Test Get error
	_, err = manager.Get(ctx, "issue-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list worktrees")

	// Test Exists error
	_, err = manager.Exists(ctx, "issue-123")
	require.Error(t, err)
}

func TestWorktreeManager_PathGeneration(t *testing.T) {
	ctx := context.Background()
	issueID := "issue-xyz"
	baseBranch := "main"

	testCases := []struct {
		name         string
		repoDir      string
		expectedPath string
	}{
		{
			name:         "simple path",
			repoDir:      "/home/user/my-repo",
			expectedPath: "/home/user/my-repo-issue-xyz",
		},
		{
			name:         "nested path",
			repoDir:      "/home/user/projects/awesome-app",
			expectedPath: "/home/user/projects/awesome-app-issue-xyz",
		},
		{
			name:         "path with spaces",
			repoDir:      "/home/user/my projects/test repo",
			expectedPath: "/home/user/my projects/test repo-issue-xyz",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mock := NewMockRunner()
			mock.handler = func(ctx context.Context, args ...string) (string, error) {
				// Mock 'worktree list' to return empty
				if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
					return "", nil
				}
				// Verify the path in 'worktree add' command
				// Command: git worktree add -b <branch> <path> <base>
				// args[0]=worktree, args[1]=add, args[2]=-b, args[3]=branch, args[4]=path, args[5]=base
				if len(args) > 4 && args[0] == "worktree" && args[1] == "add" {
					actualPath := args[4]
					if actualPath != tc.expectedPath {
						t.Errorf("expected path %q, got %q", tc.expectedPath, actualPath)
					}
				}
				return "", nil
			}

			logger := slog.Default()
			manager := NewWorktreeManager(mock, tc.repoDir, logger)

			worktree, err := manager.Create(ctx, issueID, baseBranch)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedPath, worktree.Path)
		})
	}
}

func TestParseWorktreeList(t *testing.T) {
	repoDir := "/home/user/test-repo"
	logger := slog.Default()
	manager := NewWorktreeManager(NewMockRunner(), repoDir, logger)

	testCases := []struct {
		name     string
		output   string
		expected []Worktree
	}{
		{
			name:     "empty output",
			output:   "",
			expected: []Worktree{},
		},
		{
			name: "single az worktree",
			output: `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123
`,
			expected: []Worktree{
				{
					Path:    "/home/user/test-repo-issue-123",
					Branch:  "az/issue-123",
					IssueID: "issue-123",
				},
			},
		},
		{
			name: "multiple worktrees, only az included",
			output: `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123

worktree /home/user/test-repo-feature
HEAD ghi789
branch refs/heads/feature/test

worktree /home/user/test-repo-issue-456
HEAD jkl012
branch refs/heads/az/issue-456
`,
			expected: []Worktree{
				{
					Path:    "/home/user/test-repo-issue-123",
					Branch:  "az/issue-123",
					IssueID: "issue-123",
				},
				{
					Path:    "/home/user/test-repo-issue-456",
					Branch:  "az/issue-456",
					IssueID: "issue-456",
				},
			},
		},
		{
			name: "no trailing newline",
			output: `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123`,
			expected: []Worktree{
				{
					Path:    "/home/user/test-repo-issue-123",
					Branch:  "az/issue-123",
					IssueID: "issue-123",
				},
			},
		},
		{
			name: "deterministic author issue slug branch",
			output: `worktree /home/user/test-repo-che-3002
HEAD abc123
branch refs/heads/riordanpawley/che-3002/migrate-prep-lists-to-db
`,
			expected: []Worktree{
				{
					Path:    "/home/user/test-repo-che-3002",
					Branch:  "riordanpawley/che-3002/migrate-prep-lists-to-db",
					IssueID: "che-3002",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := manager.parseWorktreeList(tc.output)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestNewWorktreeManager_DefaultLogger(t *testing.T) {
	mock := NewMockRunner()
	repoDir := "/home/user/test-repo"

	// Test with nil logger - should use default
	manager := NewWorktreeManager(mock, repoDir, nil)
	assert.NotNil(t, manager.logger)
}

func TestExecRunner(t *testing.T) {
	// This test requires actual git installation
	// Skip if git is not available
	ctx := context.Background()

	// Create a temporary directory for testing
	// Note: This is a basic test that just verifies the runner can execute git
	runner := NewExecRunner("/tmp")

	// Simple command that should work: git --version
	output, err := runner.Run(ctx, "--version")

	// We expect either success or a specific error
	// This test mainly validates the runner structure
	if err != nil {
		t.Logf("git command failed (this is OK if git is not installed): %v", err)
	} else {
		assert.Contains(t, output, "git version")
	}
}

func TestExecRunner_WorkDir(t *testing.T) {
	workDir := "/custom/work/dir"
	runner := NewExecRunner(workDir)

	assert.Equal(t, workDir, runner.workDir)
}

func TestExecRunner_IgnoresConflictingGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	repoDir := t.TempDir()
	ctx := context.Background()

	initCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "init")
	initCmd.Env = sanitizedGitEnv(os.Environ())
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, string(out))
	}

	badGitDir := filepath.Join(repoDir, "bad-git-dir-marker")
	require.NoError(t, os.WriteFile(badGitDir, []byte("not-a-dir"), 0o644))

	t.Setenv("GIT_DIR", badGitDir)
	t.Setenv("GIT_WORK_TREE", filepath.Join(repoDir, "bad-worktree"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(repoDir, "bad-index"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(repoDir, "bad-common-dir"))

	runner := NewExecRunner(repoDir)
	output, err := runner.Run(ctx, "rev-parse", "--git-dir")
	require.NoError(t, err)
	assert.Equal(t, ".git", strings.TrimSpace(output))
}

func BenchmarkParseWorktreeList(b *testing.B) {
	repoDir := "/home/user/test-repo"
	logger := slog.Default()
	manager := NewWorktreeManager(NewMockRunner(), repoDir, logger)

	output := `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-issue-123
HEAD def456
branch refs/heads/az/issue-123

worktree /home/user/test-repo-issue-456
HEAD ghi789
branch refs/heads/az/issue-456
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.parseWorktreeList(output)
	}
}
