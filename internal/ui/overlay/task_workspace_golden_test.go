package overlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestTaskWorkspaceOverlay_View_Golden(t *testing.T) {
	task := domain.Task{
		ID:          "bal",
		Title:       "add grom renderer for detail view maybe?",
		Status:      domain.StatusOpen,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
		Description: "review panel structure and preserve layout",
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	got := overlay.View()

	goldenPath := filepath.Join("testdata", "task_workspace_view.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("task workspace view changed; run UPDATE_GOLDEN=1 go test ./internal/ui/overlay to accept")
	}
}
