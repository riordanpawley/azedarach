package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type ancestorTaskLookup func(context.Context, string) (domain.Task, error)

type worktreeInitContext struct {
	ProjectID          string
	ProjectRoot        string
	IssueID            string
	WorktreePath       string
	ParentIssueID      string
	ParentWorktreePath string
}

type worktreeCreatedFunc func(context.Context, worktreeInitContext) error

type ensureAncestorWorktreesResult struct {
	BaseBranch           string
	AncestorIssueID      string
	AncestorWorktreePath string
	Created              []git.Worktree
}

func ensureAncestorWorktrees(
	ctx context.Context,
	projectID string,
	sourceTask domain.Task,
	baseBranch string,
	manager *git.WorktreeManager,
	lookup ancestorTaskLookup,
	projectionWriter runtimeProjectionWriter,
	onCreated worktreeCreatedFunc,
) (ensureAncestorWorktreesResult, error) {
	result := ensureAncestorWorktreesResult{BaseBranch: strings.TrimSpace(baseBranch)}
	if result.BaseBranch == "" {
		result.BaseBranch = "main"
	}
	if manager == nil || lookup == nil {
		return result, nil
	}

	chain, err := ancestorTaskChain(ctx, sourceTask, lookup)
	if err != nil {
		return ensureAncestorWorktreesResult{}, err
	}
	if len(chain) == 0 {
		return result, nil
	}

	currentBase := result.BaseBranch
	parentIssueID := ""
	parentWorktreePath := ""
	for i := len(chain) - 1; i >= 0; i-- {
		ancestor := chain[i]
		ancestorID := strings.TrimSpace(ancestor.ID.String())
		if ancestorID == "" {
			continue
		}
		worktree, err := manager.Get(ctx, ancestorID)
		if err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
			return ensureAncestorWorktreesResult{}, fmt.Errorf("load ancestor worktree %s: %w", ancestorID, err)
		}
		created := false
		if err != nil {
			worktree, created, err = createOrLoadIssueWorktree(ctx, manager, ancestorID, ancestor.Title, currentBase)
			if err != nil {
				return ensureAncestorWorktreesResult{}, fmt.Errorf("create ancestor worktree %s from %s: %w", ancestorID, currentBase, err)
			}
			if created && worktree != nil {
				result.Created = append(result.Created, *worktree)
			}
		}
		if worktree == nil || strings.TrimSpace(worktree.Branch) == "" {
			return ensureAncestorWorktreesResult{}, fmt.Errorf("ancestor worktree %s has no branch", ancestorID)
		}
		if created && onCreated != nil {
			initCtx := worktreeInitContext{
				ProjectID:          normalizedProjectID(projectID),
				IssueID:            ancestorID,
				WorktreePath:       worktree.Path,
				ParentIssueID:      parentIssueID,
				ParentWorktreePath: parentWorktreePath,
			}
			if err := onCreated(ctx, initCtx); err != nil {
				return ensureAncestorWorktreesResult{}, fmt.Errorf("initialize ancestor worktree %s: %w", ancestorID, err)
			}
		}
		currentBase = strings.TrimSpace(worktree.Branch)
		result.AncestorIssueID = ancestorID
		result.AncestorWorktreePath = worktree.Path
		parentIssueID = ancestorID
		parentWorktreePath = worktree.Path
		if projectionWriter != nil {
			if _, err := projectionWriter.PersistWorktreeProjectionAndPublish(ctx, normalizedProjectID(projectID), worktree.IssueID, worktree.Path, worktree.Branch); err != nil {
				return ensureAncestorWorktreesResult{}, fmt.Errorf("persist ancestor worktree projection %s: %w", ancestorID, err)
			}
		}
	}
	result.BaseBranch = currentBase
	return result, nil
}

func ancestorTaskChain(ctx context.Context, sourceTask domain.Task, lookup ancestorTaskLookup) ([]domain.Task, error) {
	chain := make([]domain.Task, 0, 4)
	seen := map[string]struct{}{}
	for parentID := domain.TaskParentIssueID(sourceTask); parentID != ""; {
		if _, ok := seen[strings.ToLower(parentID)]; ok {
			return nil, fmt.Errorf("cycle detected while resolving ancestor worktrees at %s", parentID)
		}
		seen[strings.ToLower(parentID)] = struct{}{}

		parentTask, err := lookup(ctx, parentID)
		if err != nil {
			return nil, fmt.Errorf("load parent issue %s: %w", parentID, err)
		}
		chain = append(chain, parentTask)
		parentID = domain.TaskParentIssueID(parentTask)
	}
	return chain, nil
}

func createOrLoadIssueWorktree(ctx context.Context, manager *git.WorktreeManager, issueID, title, baseBranch string) (*git.Worktree, bool, error) {
	worktree, err := manager.CreateWithTitle(ctx, issueID, title, baseBranch)
	if err == nil {
		return worktree, true, nil
	}
	if existing, getErr := manager.Get(ctx, issueID); getErr == nil {
		return existing, !errors.Is(err, git.ErrWorktreeAlreadyExists), nil
	}
	if errors.Is(err, git.ErrWorktreeAlreadyExists) {
		return nil, false, fmt.Errorf("worktree already exists for issue %s but could not be loaded", issueID)
	}
	return nil, false, err
}

func taskLookupFromMap(tasksByIssue map[string]domain.Task) ancestorTaskLookup {
	return func(_ context.Context, issueID string) (domain.Task, error) {
		if task, ok := taskByIssueIDLocal(tasksByIssue, issueID); ok {
			return task, nil
		}
		return domain.Task{}, domain.ErrNotFound
	}
}

func taskByIssueIDLocal(tasksByIssue map[string]domain.Task, issueID string) (domain.Task, bool) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return domain.Task{}, false
	}
	if task, ok := tasksByIssue[issueID]; ok {
		return task, true
	}
	for id, task := range tasksByIssue {
		if naming.IssueIDsEqual(id, issueID) {
			return task, true
		}
	}
	return domain.Task{}, false
}
