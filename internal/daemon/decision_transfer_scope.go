package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

type decisionMDTransferTarget struct {
	RepoDir     string
	Revision    string
	IssueID     string
	FullProject bool
}

func (s issueDecisionService) withDecisionMDTransferTarget(ctx context.Context, requestRepoDir string, fullProject bool, fn func(context.Context, decisionMDTransferTarget) error) (decisionMDTransferTarget, error) {
	target, err := s.resolveDecisionMDTransferTarget(ctx, requestRepoDir, fullProject)
	if err != nil {
		return decisionMDTransferTarget{}, err
	}
	if s.daemon == nil || s.daemon.git == nil {
		if err := fn(ctx, target); err != nil {
			return target, err
		}
		return target, nil
	}
	err = s.daemon.git.WithWorktreeLock(ctx, target.RepoDir, func(lockCtx context.Context) error {
		return fn(lockCtx, target)
	})
	return target, err
}

func (s issueDecisionService) revalidateDecisionMDTransferTarget(ctx context.Context, expected decisionMDTransferTarget) error {
	actual, err := s.resolveDecisionMDTransferTarget(ctx, expected.RepoDir, expected.FullProject)
	if err != nil {
		return err
	}
	if actual.RepoDir != expected.RepoDir || actual.Revision != expected.Revision || actual.IssueID != expected.IssueID || actual.FullProject != expected.FullProject {
		return fmt.Errorf("decision transfer target changed: expected repo=%s revision=%s issue=%s full_project=%t; got repo=%s revision=%s issue=%s full_project=%t",
			expected.RepoDir, expected.Revision, expected.IssueID, expected.FullProject,
			actual.RepoDir, actual.Revision, actual.IssueID, actual.FullProject)
	}
	return nil
}

func (s issueDecisionService) beforeDecisionMDTransferRevalidation(operation string, target decisionMDTransferTarget) {
	if s.daemon != nil && s.daemon.decisionTransferBeforeRevalidation != nil {
		s.daemon.decisionTransferBeforeRevalidation(operation, target)
	}
}

func (s issueDecisionService) resolveDecisionMDTransferTarget(ctx context.Context, requestRepoDir string, fullProject bool) (decisionMDTransferTarget, error) {
	projectID := daemonProjectIDFromContext(ctx)
	repoDir, err := decisionMDRepoDir(s.daemon.resolveRepoDirForProject(projectID), requestRepoDir)
	if err != nil {
		return decisionMDTransferTarget{}, err
	}
	repoDir = canonicalDecisionMDPath(repoDir)
	rootDir := canonicalDecisionMDPath(s.daemon.resolveRepoDirForProject(projectID))

	var revision string
	if s.daemon.git != nil {
		revision, err = s.daemon.git.HeadRevision(ctx, repoDir)
		if err != nil {
			return decisionMDTransferTarget{}, fmt.Errorf("resolve decision transfer revision for %s: %w", repoDir, err)
		}
	}

	if fullProject {
		if rootDir == "" || repoDir != rootDir {
			return decisionMDTransferTarget{}, fmt.Errorf("full-project decision transfer requires the registered root checkout %s; target was %s", rootDir, repoDir)
		}
		return decisionMDTransferTarget{RepoDir: repoDir, Revision: revision, FullProject: true}, nil
	}

	if s.daemon.worktreeAdapter != nil {
		s.daemon.worktreeAdapter.pollAndPersistWorktrees(ctx, projectID)
	}
	manager := s.daemon.worktreeManagerForProject(projectID)
	if manager == nil {
		return decisionMDTransferTarget{}, fmt.Errorf("decision transfer target %s has no live worktree authority; use --all only from the registered root checkout", repoDir)
	}
	worktrees, err := manager.List(ctx)
	if err != nil {
		return decisionMDTransferTarget{}, fmt.Errorf("list live worktrees for decision transfer: %w", err)
	}
	issueID := ""
	for _, worktree := range worktrees {
		if canonicalDecisionMDPath(worktree.Path) == repoDir {
			issueID = strings.TrimSpace(worktree.IssueID)
			break
		}
	}
	if issueID == "" {
		return decisionMDTransferTarget{}, fmt.Errorf("decision transfer target %s is not an issue worktree; use --all explicitly from the registered root checkout", repoDir)
	}
	if store := s.daemon.worktreeRuntimeStateStoreIfConfigured(projectID); store != nil {
		projection, found, projectionErr := store.GetWorktreeStateByPath(ctx, projectID, repoDir)
		if projectionErr != nil {
			return decisionMDTransferTarget{}, fmt.Errorf("read refreshed decision transfer worktree projection: %w", projectionErr)
		}
		if !found || strings.TrimSpace(projection.IssueID) != issueID {
			return decisionMDTransferTarget{}, fmt.Errorf("decision transfer target projection does not match live worktree %s for issue %s", repoDir, issueID)
		}
	}
	return decisionMDTransferTarget{RepoDir: repoDir, Revision: revision, IssueID: issueID}, nil
}

func canonicalDecisionMDPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		path = evaluated
	}
	return filepath.Clean(path)
}
