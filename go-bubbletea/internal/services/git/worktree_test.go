package git

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockRunner implements CommandRunner for testing.
type MockRunner struct {
	commands []string                                    // Record of commands run
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
	beadID := "bead-123"
	baseBranch := "main"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
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

	worktree, err := manager.Create(ctx, beadID, baseBranch)

	require.NoError(t, err)
	assert.NotNil(t, worktree)
	assert.Equal(t, beadID, worktree.BeadID)
	assert.Equal(t, "az/bead-123", worktree.Branch)
	assert.Equal(t, "/home/user/test-repo-bead-123", worktree.Path)

	// Verify the command was called correctly
	expectedCmd := "worktree add -b az/bead-123 /home/user/test-repo-bead-123 main"
	mock.AssertCommand(t, expectedCmd)
}

func TestWorktreeManager_Create_AlreadyExists(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	beadID := "bead-123"
	baseBranch := "main"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list' to return existing worktree
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	_, err := manager.Create(ctx, beadID, baseBranch)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestWorktreeManager_Delete(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	beadID := "bead-123"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list' to return existing worktree
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123
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

	err := manager.Delete(ctx, beadID)

	require.NoError(t, err)
	mock.AssertCommand(t, "worktree remove /home/user/test-repo-bead-123")
	mock.AssertCommand(t, "branch -D az/bead-123")
}

func TestWorktreeManager_Delete_NotFound(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	beadID := "nonexistent"

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

	err := manager.Delete(ctx, beadID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorktreeManager_Get(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	beadID := "bead-123"

	mock := NewMockRunner()
	mock.handler = func(ctx context.Context, args ...string) (string, error) {
		// Mock 'worktree list'
		if len(args) > 0 && args[0] == "worktree" && args[1] == "list" {
			return `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123

worktree /home/user/test-repo-bead-456
HEAD ghi789
branch refs/heads/az/bead-456
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	worktree, err := manager.Get(ctx, beadID)

	require.NoError(t, err)
	assert.NotNil(t, worktree)
	assert.Equal(t, beadID, worktree.BeadID)
	assert.Equal(t, "az/bead-123", worktree.Branch)
	assert.Equal(t, "/home/user/test-repo-bead-123", worktree.Path)
}

func TestWorktreeManager_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	repoDir := "/home/user/test-repo"
	beadID := "nonexistent"

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

	_, err := manager.Get(ctx, beadID)

	require.Error(t, err)
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

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123

worktree /home/user/test-repo-bead-456
HEAD ghi789
branch refs/heads/az/bead-456

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
	assert.Len(t, worktrees, 2) // Only az/ branches

	// Check first worktree
	assert.Equal(t, "bead-123", worktrees[0].BeadID)
	assert.Equal(t, "az/bead-123", worktrees[0].Branch)
	assert.Equal(t, "/home/user/test-repo-bead-123", worktrees[0].Path)

	// Check second worktree
	assert.Equal(t, "bead-456", worktrees[1].BeadID)
	assert.Equal(t, "az/bead-456", worktrees[1].Branch)
	assert.Equal(t, "/home/user/test-repo-bead-456", worktrees[1].Path)
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

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123
`, nil
		}
		return "", nil
	}

	logger := slog.Default()
	manager := NewWorktreeManager(mock, repoDir, logger)

	// Test existing worktree
	exists, err := manager.Exists(ctx, "bead-123")
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
	_, err = manager.Get(ctx, "bead-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list worktrees")

	// Test Exists error
	_, err = manager.Exists(ctx, "bead-123")
	require.Error(t, err)
}

func TestWorktreeManager_PathGeneration(t *testing.T) {
	ctx := context.Background()
	beadID := "bead-xyz"
	baseBranch := "main"

	testCases := []struct {
		name         string
		repoDir      string
		expectedPath string
	}{
		{
			name:         "simple path",
			repoDir:      "/home/user/my-repo",
			expectedPath: "/home/user/my-repo-bead-xyz",
		},
		{
			name:         "nested path",
			repoDir:      "/home/user/projects/awesome-app",
			expectedPath: "/home/user/projects/awesome-app-bead-xyz",
		},
		{
			name:         "path with spaces",
			repoDir:      "/home/user/my projects/test repo",
			expectedPath: "/home/user/my projects/test repo-bead-xyz",
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
				if len(args) > 0 && args[0] == "worktree" && args[1] == "add" {
					actualPath := args[3]
					if actualPath != tc.expectedPath {
						t.Errorf("expected path %q, got %q", tc.expectedPath, actualPath)
					}
				}
				return "", nil
			}

			logger := slog.Default()
			manager := NewWorktreeManager(mock, tc.repoDir, logger)

			worktree, err := manager.Create(ctx, beadID, baseBranch)

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

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123
`,
			expected: []Worktree{
				{
					Path:   "/home/user/test-repo-bead-123",
					Branch: "az/bead-123",
					BeadID: "bead-123",
				},
			},
		},
		{
			name: "multiple worktrees, only az included",
			output: `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123

worktree /home/user/test-repo-feature
HEAD ghi789
branch refs/heads/feature/test

worktree /home/user/test-repo-bead-456
HEAD jkl012
branch refs/heads/az/bead-456
`,
			expected: []Worktree{
				{
					Path:   "/home/user/test-repo-bead-123",
					Branch: "az/bead-123",
					BeadID: "bead-123",
				},
				{
					Path:   "/home/user/test-repo-bead-456",
					Branch: "az/bead-456",
					BeadID: "bead-456",
				},
			},
		},
		{
			name: "no trailing newline",
			output: `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123`,
			expected: []Worktree{
				{
					Path:   "/home/user/test-repo-bead-123",
					Branch: "az/bead-123",
					BeadID: "bead-123",
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

func BenchmarkParseWorktreeList(b *testing.B) {
	repoDir := "/home/user/test-repo"
	logger := slog.Default()
	manager := NewWorktreeManager(NewMockRunner(), repoDir, logger)

	output := `worktree /home/user/test-repo
HEAD abc123
branch refs/heads/main

worktree /home/user/test-repo-bead-123
HEAD def456
branch refs/heads/az/bead-123

worktree /home/user/test-repo-bead-456
HEAD ghi789
branch refs/heads/az/bead-456
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.parseWorktreeList(output)
	}
}
