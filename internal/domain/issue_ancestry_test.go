package domain

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestClosestAncestorWithWorktreeSkipsAncestorsWithoutWorktrees(t *testing.T) {
	childID := naming.IssueID("az-child")
	parentID := naming.IssueID("az-parent")
	rootID := naming.IssueID("az-root")

	tasksByID := map[string]Task{
		childID.String(): {
			ID:       childID,
			ParentID: &parentID,
		},
		parentID.String(): {
			ID:       parentID,
			ParentID: &rootID,
		},
		rootID.String(): {
			ID: rootID,
		},
	}
	worktreesByIssue := map[string]IssueWorktreeRef{
		rootID.String(): {
			Branch: "riordan/az-root/root-branch",
			Path:   "/repo-root",
		},
	}

	target, ok := ClosestAncestorWithWorktree(childID.String(), tasksByID, worktreesByIssue)
	if !ok {
		t.Fatal("expected ancestor worktree target")
	}
	if target.IssueID != rootID.String() || target.Branch != "riordan/az-root/root-branch" || target.WorktreePath != "/repo-root" {
		t.Fatalf("target = %+v, want root worktree target", target)
	}
	if len(target.AncestorChain) != 2 || target.AncestorChain[0] != parentID.String() || target.AncestorChain[1] != rootID.String() {
		t.Fatalf("ancestor chain = %v, want parent then root", target.AncestorChain)
	}
}

func TestClosestAncestorWithWorktreePrefersClosestWorktreeRegardlessOfStatus(t *testing.T) {
	childID := naming.IssueID("az-child")
	parentID := naming.IssueID("az-parent")
	rootID := naming.IssueID("az-root")

	tasksByID := map[string]Task{
		childID.String(): {
			ID:       childID,
			ParentID: &parentID,
		},
		parentID.String(): {
			ID:       parentID,
			Status:   StatusDone,
			ParentID: &rootID,
		},
		rootID.String(): {
			ID: rootID,
		},
	}
	worktreesByIssue := map[string]IssueWorktreeRef{
		parentID.String(): {Branch: "riordan/az-parent/parent-branch"},
		rootID.String():   {Branch: "riordan/az-root/root-branch"},
	}

	target, ok := ClosestAncestorWithWorktree(childID.String(), tasksByID, worktreesByIssue)
	if !ok {
		t.Fatal("expected ancestor worktree target")
	}
	if target.IssueID != parentID.String() || target.Branch != "riordan/az-parent/parent-branch" {
		t.Fatalf("target = %+v, want closest parent worktree target", target)
	}
}
