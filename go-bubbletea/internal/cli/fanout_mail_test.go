package cli

import (
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestFlattenFanoutAndPlan(t *testing.T) {
	spec := fanoutSpec{
		ParentIssue: "az-root",
		Nodes: []fanoutNode{
			{
				Key:   "group-a",
				Kind:  "group",
				Title: "Group A",
				Children: []fanoutNode{
					{
						Key:        "leaf-a1",
						Kind:       "work",
						Title:      "Leaf A1",
						DependsOn:  []string{"leaf-a2"},
						FileBudget: []string{"go-bubbletea/internal/cli/**"},
					},
					{
						Key:   "leaf-a2",
						Kind:  "work",
						Title: "Leaf A2",
					},
				},
			},
		},
	}

	flat, warnings, err := flattenFanout(spec)
	if err != nil {
		t.Fatalf("flattenFanout error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want empty", warnings)
	}
	if len(flat) != 3 {
		t.Fatalf("flat len = %d, want 3", len(flat))
	}
	plan := buildFanoutPlan(spec.ParentIssue, flat, warnings)
	if plan.NodeCount != 3 {
		t.Fatalf("plan node_count = %d, want 3", plan.NodeCount)
	}
	if len(plan.Blocks) != 1 {
		t.Fatalf("plan blocks len = %d, want 1", len(plan.Blocks))
	}
	if plan.Blocks[0].IssueKey != "leaf-a1" || plan.Blocks[0].DependsOnKey != "leaf-a2" {
		t.Fatalf("blocks[0] = %+v", plan.Blocks[0])
	}
}

func TestComputeRunnableLeaves(t *testing.T) {
	root := "az-root"
	group := "az-group"
	leafA := "az-a"
	leafB := "az-b"
	groupParent := root
	leafAParent := group
	leafBParent := group

	tasks := []domain.Task{
		{ID: root, Type: domain.TypeFeature, Status: domain.StatusInProgress},
		{ID: group, Type: domain.TypeEpic, Status: domain.StatusOpen, ParentID: &groupParent},
		{ID: leafA, Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &leafAParent},
		{
			ID:       leafB,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &leafBParent,
			Dependencies: []domain.Dependency{
				{ID: leafA, Type: domain.DependencyBlocks},
			},
		},
	}

	result, err := computeRunnableLeaves(root, tasks)
	if err != nil {
		t.Fatalf("computeRunnableLeaves error: %v", err)
	}
	if len(result.Runnable) != 1 || result.Runnable[0] != leafB {
		t.Fatalf("runnable = %v, want [%s]", result.Runnable, leafB)
	}
}

func TestMailboxRoundTrip(t *testing.T) {
	repoDir := t.TempDir()
	parent := "az-parent"
	first := mailEvent{
		Seq:         1,
		ParentIssue: parent,
		Type:        "dependency-ready",
		Body:        "ready",
	}
	second := mailEvent{
		Seq:         2,
		ParentIssue: parent,
		Type:        "handoff",
		Body:        "handoff",
	}
	if err := appendMailboxEvent(repoDir, first); err != nil {
		t.Fatalf("appendMailboxEvent first: %v", err)
	}
	if err := appendMailboxEvent(repoDir, second); err != nil {
		t.Fatalf("appendMailboxEvent second: %v", err)
	}

	events, err := readMailboxEvents(repoDir, parent)
	if err != nil {
		t.Fatalf("readMailboxEvents error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seqs = [%d,%d], want [1,2]", events[0].Seq, events[1].Seq)
	}
	path := mailboxPath(repoDir, parent)
	if filepath.Ext(path) != ".jsonl" {
		t.Fatalf("mailbox path %q missing .jsonl suffix", path)
	}
}
