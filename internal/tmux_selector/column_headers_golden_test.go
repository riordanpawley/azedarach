package tmuxselector

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
)

var updateSelectorGoldens = flag.Bool("update-selector-goldens", false, "update tmux selector golden files")

func TestColumnBoardHeaderGoldens(t *testing.T) {
	view := domain.OrchestrationBoardView()
	entries := []InventoryEntry{
		{SessionID: "az-review", IssueID: "review", TaskTitle: "Review me", HasTmuxSession: true, ViewProjected: true, ViewGroupID: view.Columns[0].ID},
		{SessionID: "az-active", IssueID: "active", TaskTitle: "Active work", HasTmuxSession: true, ViewProjected: true, ViewGroupID: view.Columns[2].ID},
		{SessionID: "plain", TaskTitle: "Plain tmux", HasTmuxSession: true},
	}
	projectedGroups := []domain.BoardColumnID{view.Columns[0].ID, view.Columns[2].ID}

	for _, tc := range []struct {
		name   string
		width  int
		cursor int
	}{
		{name: "column_headers_default", width: 160, cursor: 0},
		{name: "column_headers_narrow", width: 60, cursor: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := New(SnapshotLoaderFunc(func(context.Context) (Snapshot, error) { return Snapshot{}, nil }))
			model.loading, model.width, model.height, model.cursor = false, tc.width, 20, tc.cursor
			model.snapshot = Snapshot{View: view, ProjectedGroups: projectedGroups, Entries: entries}

			got := normalizeSelectorGolden(model.View())
			goldenFile := filepath.Join("testdata", tc.name+".golden")
			if *updateSelectorGoldens {
				if err := os.MkdirAll(filepath.Dir(goldenFile), 0o755); err != nil {
					t.Fatalf("create golden directory: %v", err)
				}
				if err := os.WriteFile(goldenFile, []byte(got), 0o644); err != nil {
					t.Fatalf("update golden: %v", err)
				}
			}
			want, err := os.ReadFile(goldenFile)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update-selector-goldens)", err)
			}
			if got != string(want) {
				t.Fatalf("selector output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func normalizeSelectorGolden(value string) string {
	lines := strings.Split(ansi.Strip(value), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}
