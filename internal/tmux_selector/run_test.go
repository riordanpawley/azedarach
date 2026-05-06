package tmuxselector

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunPlainPrintsSessionList(t *testing.T) {
	loader := fakeSnapshotLoader{snapshot: Snapshot{Entries: []InventoryEntry{
		{
			SessionID:   "az-one",
			IssueID:     "one",
			TaskTitle:   "One task",
			ProjectPath: "/tmp/project",
		},
		{
			SessionID: "plain-tmux",
			TaskTitle: "plain-tmux",
		},
	}}}
	var out bytes.Buffer

	if err := RunPlain(context.Background(), loader, &out); err != nil {
		t.Fatalf("RunPlain error: %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"2 tmux sessions",
		"- az-one (one): One task [/tmp/project]",
		"- plain-tmux: plain-tmux",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("plain output missing %q:\n%s", want, output)
		}
	}
}

func TestPlainSnapshotFallsBackToTasksAndEmptyState(t *testing.T) {
	output := PlainSnapshot(Snapshot{Tasks: []domain.Task{
		{
			ID:             "one",
			Title:          "One task",
			Status:         domain.StatusInProgress,
			Priority:       domain.P2,
			Type:           domain.TypeTask,
			HasTmuxSession: true,
		},
	}})
	if !strings.Contains(output, "1 tmux session") || !strings.Contains(output, "- az-one (one): One task") {
		t.Fatalf("plain task fallback output = %q", output)
	}

	output = PlainSnapshot(Snapshot{})
	if output != "No tmux sessions found.\n" {
		t.Fatalf("empty output = %q", output)
	}
}
