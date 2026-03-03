package gitpr

import (
	"context"
	"errors"
	"testing"

	adapterrors "github.com/riordanpawley/azedarach/internal/adapters/errors"
	"github.com/riordanpawley/azedarach/internal/adapters/gh"
	"github.com/riordanpawley/azedarach/internal/adapters/git"
	"github.com/riordanpawley/azedarach/internal/core/ops"
)

func TestProvisioningPath(t *testing.T) {
	t.Parallel()

	gitMock := &mockGit{}
	w := New(gitMock, &mockGH{}, ops.NewOrchestrator())

	result, err := w.ProvisionWorktree(context.Background(), ProvisionWorktreeInput{
		IssueKey:     "bd-101",
		RepoPath:     "/repo",
		WorktreePath: "/repo/.worktrees/bd-101",
		Branch:       "bd-101",
		BaseBranch:   "main",
	})
	if err != nil {
		t.Fatalf("provision worktree: %v", err)
	}
	if !gitMock.provisionCalled {
		t.Fatalf("expected ProvisionWorktree to be called")
	}
	if result.Operation.State != ops.StateSucceeded {
		t.Fatalf("expected succeeded state, got %s", result.Operation.State)
	}
}

func TestConflictAndAbortFlow(t *testing.T) {
	t.Parallel()

	gitMock := &mockGit{
		mergeResult: git.MergeResult{
			HasConflicts: true,
			Conflicts:    []string{"internal/app/model.go"},
		},
	}
	w := New(gitMock, &mockGH{}, ops.NewOrchestrator())

	result, err := w.UpdateFromBase(context.Background(), UpdateFromBaseInput{
		IssueKey:        "bd-102",
		WorktreePath:    "/repo/.worktrees/bd-102",
		BaseBranch:      "main",
		AbortOnConflict: true,
	})
	if err != nil {
		t.Fatalf("update from base: %v", err)
	}
	if result.Operation.State != ops.StateFailed {
		t.Fatalf("expected failed op state, got %s", result.Operation.State)
	}
	if !result.Conflict.Detected {
		t.Fatalf("expected conflict detected")
	}
	if result.Conflict.Fallback == nil {
		t.Fatalf("expected conflict fallback metadata")
	}
	if !result.Conflict.Fallback.AbortAttempted || !result.Conflict.Fallback.AbortSucceeded {
		t.Fatalf("expected merge abort attempt to succeed")
	}
	if result.Remediation == nil || result.Remediation.Code != RemediationConflict {
		t.Fatalf("expected conflict remediation")
	}
	if !gitMock.abortCalled {
		t.Fatalf("expected AbortMerge to be called")
	}
}

func TestMergeFlowStateUpdates(t *testing.T) {
	t.Parallel()

	gitMock := &mockGit{}
	w := New(gitMock, &mockGH{}, ops.NewOrchestrator())

	result, err := w.MergeToBase(context.Background(), MergeToBaseInput{
		IssueKey:   "bd-103",
		RepoPath:   "/repo",
		BaseBranch: "main",
		HeadBranch: "bd-103",
	})
	if err != nil {
		t.Fatalf("merge to base: %v", err)
	}
	if !result.Merged {
		t.Fatalf("expected merged=true")
	}
	if result.Operation.State != ops.StateSucceeded {
		t.Fatalf("expected succeeded op state, got %s", result.Operation.State)
	}
	if !gitMock.mergeCalled {
		t.Fatalf("expected git merge to be called")
	}
}

func TestPROfflineAuthHandling(t *testing.T) {
	t.Parallel()

	t.Run("offline on lookup", func(t *testing.T) {
		t.Parallel()
		ghMock := &mockGH{
			getErr: adapterrors.NewOffline("gh pr view", "offline", nil),
		}
		w := New(&mockGit{}, ghMock, ops.NewOrchestrator())

		result, err := w.CreateOrOpenPR(context.Background(), CreateOrOpenPRInput{
			IssueKey:   "bd-104",
			HeadBranch: "bd-104",
			BaseBranch: "main",
			Title:      "t",
			Body:       "b",
		})
		if err != nil {
			t.Fatalf("create/open pr: %v", err)
		}
		if result.Operation.State != ops.StateFailed {
			t.Fatalf("expected failed op state, got %s", result.Operation.State)
		}
		if result.Remediation == nil || result.Remediation.Code != RemediationOffline {
			t.Fatalf("expected offline remediation")
		}
	})

	t.Run("auth on create", func(t *testing.T) {
		t.Parallel()
		ghMock := &mockGH{
			getErr:    ErrPullRequestNotFound,
			createErr: adapterrors.NewAuth("gh pr create", "not logged in", nil),
		}
		w := New(&mockGit{}, ghMock, ops.NewOrchestrator())

		result, err := w.CreateOrOpenPR(context.Background(), CreateOrOpenPRInput{
			IssueKey:   "bd-105",
			HeadBranch: "bd-105",
			BaseBranch: "main",
			Title:      "t",
			Body:       "b",
		})
		if err != nil {
			t.Fatalf("create/open pr: %v", err)
		}
		if result.Operation.State != ops.StateFailed {
			t.Fatalf("expected failed op state, got %s", result.Operation.State)
		}
		if result.Remediation == nil || result.Remediation.Code != RemediationAuth {
			t.Fatalf("expected auth remediation")
		}
	})
}

func TestCleanupPreflightBlocksUnsafeOperations(t *testing.T) {
	t.Parallel()

	gitMock := &mockGit{
		safeDelete: false,
		status: git.Status{
			HasChanges: true,
			Untracked:  []string{"tmp.txt"},
		},
	}
	w := New(gitMock, &mockGH{}, ops.NewOrchestrator())

	result, err := w.Cleanup(context.Background(), CleanupInput{
		IssueKey:     "bd-106",
		RepoPath:     "/repo",
		WorktreePath: "/repo/.worktrees/bd-106",
		Branch:       "bd-106",
		DeleteBranch: true,
	})
	if err == nil {
		t.Fatalf("expected cleanup preflight failure")
	}
	var blockedErr *CleanupBlockedError
	if !errors.As(err, &blockedErr) {
		t.Fatalf("expected CleanupBlockedError, got %T", err)
	}
	if !result.PreflightBlocked {
		t.Fatalf("expected preflight blocked result")
	}
	if len(result.PreflightIssues) < 2 {
		t.Fatalf("expected multiple preflight issues, got %d", len(result.PreflightIssues))
	}
	if gitMock.removeCalled {
		t.Fatalf("remove worktree must not run on blocked preflight")
	}
	if result.Operation.State != ops.StateFailed {
		t.Fatalf("expected failed operation state, got %s", result.Operation.State)
	}
}

type mockGit struct {
	provisionCalled bool
	mergeCalled     bool
	abortCalled     bool
	removeCalled    bool
	deleteCalled    bool

	mergeResult git.MergeResult
	mergeErr    error

	fetchErr      error
	diverged      bool
	divergenceErr error

	status    git.Status
	statusErr error

	safeDelete    bool
	safeDeleteErr error
}

func (m *mockGit) ProvisionWorktree(_ context.Context, _ string, _ string, _ string, _ string) error {
	m.provisionCalled = true
	return nil
}

func (m *mockGit) FetchBranch(_ context.Context, _ string, _ string) error {
	return m.fetchErr
}

func (m *mockGit) CheckDivergence(_ context.Context, _ string, _ string) (bool, error) {
	return m.diverged, m.divergenceErr
}

func (m *mockGit) Merge(_ context.Context, _ string, _ string) (git.MergeResult, error) {
	m.mergeCalled = true
	return m.mergeResult, m.mergeErr
}

func (m *mockGit) AbortMerge(_ context.Context, _ string) error {
	m.abortCalled = true
	return nil
}

func (m *mockGit) OpenDiff(_ context.Context, _ string, _ string, _ string) (string, error) {
	return "https://example.test/compare", nil
}

func (m *mockGit) Status(_ context.Context, _ string) (git.Status, error) {
	return m.status, m.statusErr
}

func (m *mockGit) IsSafeToDeleteWorktree(_ context.Context, _ string, _ string) (bool, error) {
	return m.safeDelete, m.safeDeleteErr
}

func (m *mockGit) RemoveWorktree(_ context.Context, _ string, _ string, _ string) error {
	m.removeCalled = true
	return nil
}

func (m *mockGit) DeleteLocalBranch(_ context.Context, _ string, _ string) error {
	m.deleteCalled = true
	return nil
}

type mockGH struct {
	getResult    gh.PullRequest
	getErr       error
	createResult gh.PullRequest
	createErr    error
}

func (m *mockGH) CreatePullRequest(_ context.Context, _ gh.CreatePullRequestRequest) (gh.PullRequest, error) {
	if m.createResult.Number == 0 {
		m.createResult = gh.PullRequest{Number: 42, URL: "https://example.test/pull/42", State: "open"}
	}
	return m.createResult, m.createErr
}

func (m *mockGH) GetPullRequestByBranch(_ context.Context, _ string) (gh.PullRequest, error) {
	if m.getErr != nil {
		return gh.PullRequest{}, m.getErr
	}
	if m.getResult.Number == 0 {
		return gh.PullRequest{}, ErrPullRequestNotFound
	}
	return m.getResult, nil
}
