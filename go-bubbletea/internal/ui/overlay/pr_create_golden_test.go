package overlay

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/testprofile"
)

func TestPRCreateOverlay_View_Golden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		view func(t *testing.T) string
	}{
		{name: "default", view: goldenPRCreateDefaultView},
		{name: "small", view: goldenPRCreateSmallView},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertOverlayGolden(t, filepath.Join("testdata", "pr_create_"+tc.name+".golden"), tc.view(t))
		})
	}
}

func TestCreateTaskOverlay_View_Golden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		view func(t *testing.T) string
	}{
		{name: "create_default", view: goldenCreateTaskDefaultView},
		{name: "create_small", view: goldenCreateTaskSmallView},
		{name: "edit_default", view: goldenCreateTaskEditView},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertOverlayGolden(t, filepath.Join("testdata", tc.name+".golden"), tc.view(t))
		})
	}
}

func assertOverlayGolden(t *testing.T, path string, got string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("golden changed for %s; run UPDATE_GOLDEN=1 go test ./internal/ui/overlay to accept", filepath.Base(path))
	}
}

func goldenPRCreateDefaultView(t *testing.T) string {
	t.Helper()
	overlay := NewPRCreateOverlay("feature/auth", testprofile.Smoke.BaseBranch, "az-42")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*PRCreateOverlay).View()
}

func goldenPRCreateSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewPRCreateOverlay("feature/auth", testprofile.Smoke.BaseBranch, "az-42")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 76, Height: 22})
	return model.(*PRCreateOverlay).View()
}

func goldenCreateTaskDefaultView(t *testing.T) string {
	t.Helper()
	overlay := NewCreateTaskOverlay()
	overlay.title.SetValue("Add grom renderer for detail view maybe?")
	overlay.description.SetValue("review panel structure and preserve layout")
	overlay.taskType = domain.TypeTask
	overlay.priority = domain.P2
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*CreateTaskOverlay).View()
}

func goldenCreateTaskSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewCreateTaskOverlay()
	overlay.title.SetValue("Add grom renderer for detail view maybe?")
	overlay.description.SetValue("review panel structure and preserve layout")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*CreateTaskOverlay).View()
}

func goldenCreateTaskEditView(t *testing.T) string {
	t.Helper()
	overlay := NewEditTaskOverlay(domain.Task{
		ID:              "az-123",
		Title:           "Replace top level functions",
		Description:     "Review docs/src code of emdash.sh for improvements",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Status:          domain.StatusInProgress,
		Implementations: []string{"go-bubbletea"},
	})
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*CreateTaskOverlay).View()
}
