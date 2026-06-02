package domain

import (
	"strings"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// IssueWorktreeRef is the branch/worktree projection for an issue.
type IssueWorktreeRef struct {
	Branch string
	Path   string
}

// AncestorWorktreeTarget is the closest ancestor that has a worktree branch.
type AncestorWorktreeTarget struct {
	IssueID       string
	Branch        string
	WorktreePath  string
	AncestorChain []string
}

// TaskParentIssueID returns the parent issue ID from the task's canonical
// parent field, falling back to legacy parent-child dependency records.
func TaskParentIssueID(task Task) string {
	if task.ParentID != nil {
		return strings.TrimSpace(task.ParentID.String())
	}
	for _, dep := range task.Dependencies {
		if dep.Type == DependencyParentChild || string(dep.Type) == "parent_child" {
			if parentID := strings.TrimSpace(dep.ID.String()); parentID != "" {
				return parentID
			}
		}
	}
	return ""
}

// ClosestAncestorWithWorktree resolves the nearest ancestor in the issue tree
// that has a known worktree branch. Ancestors without worktrees are skipped.
func ClosestAncestorWithWorktree(issueID string, tasksByID map[string]Task, worktreesByIssue map[string]IssueWorktreeRef) (AncestorWorktreeTarget, bool) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" || len(tasksByID) == 0 {
		return AncestorWorktreeTarget{}, false
	}
	sourceTask, ok := taskByIssueID(tasksByID, issueID)
	if !ok {
		return AncestorWorktreeTarget{}, false
	}

	target := AncestorWorktreeTarget{AncestorChain: make([]string, 0, 6)}
	seen := map[string]struct{}{}
	for parentID := TaskParentIssueID(sourceTask); parentID != ""; {
		target.AncestorChain = append(target.AncestorChain, parentID)
		if _, ok := seen[parentID]; ok {
			return target, false
		}
		seen[parentID] = struct{}{}

		if ref, ok := worktreeByIssueID(worktreesByIssue, parentID); ok && strings.TrimSpace(ref.Branch) != "" {
			target.IssueID = parentID
			target.Branch = strings.TrimSpace(ref.Branch)
			target.WorktreePath = strings.TrimSpace(ref.Path)
			return target, true
		}

		parentTask, ok := taskByIssueID(tasksByID, parentID)
		if !ok {
			return target, false
		}
		parentID = TaskParentIssueID(parentTask)
	}
	return target, false
}

func taskByIssueID(tasksByID map[string]Task, issueID string) (Task, bool) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return Task{}, false
	}
	if task, ok := tasksByID[issueID]; ok {
		return task, true
	}
	for id, task := range tasksByID {
		if naming.IssueIDsEqual(id, issueID) {
			return task, true
		}
	}
	return Task{}, false
}

func worktreeByIssueID(worktreesByIssue map[string]IssueWorktreeRef, issueID string) (IssueWorktreeRef, bool) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return IssueWorktreeRef{}, false
	}
	if ref, ok := worktreesByIssue[issueID]; ok {
		return ref, true
	}
	for id, ref := range worktreesByIssue {
		if naming.IssueIDsEqual(id, issueID) {
			return ref, true
		}
	}
	return IssueWorktreeRef{}, false
}
