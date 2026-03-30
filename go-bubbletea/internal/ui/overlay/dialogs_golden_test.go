package overlay

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

func TestDialogs_View_Golden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		view func(t *testing.T) string
	}{
		{name: "confirm", view: goldenConfirmView},
		{name: "gitpull", view: goldenGitPullView},
		{name: "mergechoice", view: goldenMergeChoiceView},
		{name: "devserver", view: goldenDevServerView},
		{name: "project_selector", view: goldenProjectSelectorView},
		{name: "imageattach_list", view: goldenImageAttachListView},
		{name: "imageattach_preview", view: goldenImageAttachPreviewView},
		{name: "imagepreview_default", view: goldenImagePreviewView},
		{name: "imagepreview_confirm_delete", view: goldenImagePreviewConfirmDeleteView},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.view(t)
			assertGolden(t, filepath.Join("testdata", "dialog_"+tc.name+".golden"), got)
		})
	}
}

func TestDialogs_View_Golden_SmallViewport(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		view func(t *testing.T) string
	}{
		{name: "imageattach_list_small", view: goldenImageAttachListSmallView},
		{name: "imagepreview_default_small", view: goldenImagePreviewSmallView},
		{name: "project_selector_small", view: goldenProjectSelectorSmallView},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.view(t)
			assertGolden(t, filepath.Join("testdata", "dialog_"+tc.name+".golden"), got)
		})
	}
}

func assertGolden(t *testing.T, path string, got string) {
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

func goldenConfirmView(t *testing.T) string {
	t.Helper()
	dialog := NewConfirmDialog("Delete task", "Delete task az-321?")
	model, _ := dialog.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ConfirmDialog).View()
}

func goldenGitPullView(t *testing.T) string {
	t.Helper()
	overlay := NewGitPullOverlay(7)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*GitPullOverlay).View()
}

func goldenMergeChoiceView(t *testing.T) string {
	t.Helper()
	overlay := NewMergeChoiceOverlay("az-123", 3, "main")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*MergeChoiceOverlay).View()
}

func goldenDevServerView(t *testing.T) string {
	t.Helper()
	overlay := NewDevServerOverlay(
		[]DevServerInfo{
			{ID: "api", Name: "api", Port: 8080, Status: "running", Uptime: 92 * time.Minute},
			{ID: "web", Name: "web", Port: 5173, Status: "stopped"},
			{ID: "queue", Name: "queue", Port: 8081, Status: "error"},
		},
		"az-321",
		nil, nil, nil, nil,
	)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*DevServerOverlay).View()
}

func goldenProjectSelectorView(t *testing.T) string {
	t.Helper()
	registry := &config.ProjectsRegistry{
		DefaultProject: "chefy",
		Projects: []config.Project{
			{Name: "chefy", Path: "/Users/riordan/prog/Chefy"},
			{Name: "azedarach", Path: "/Users/riordan/prog/azedarach"},
			{Name: "otel-tui", Path: "/Users/riordan/prog/otel-tui"},
		},
	}
	selector := NewProjectSelectorWithOptions(registry, WithCurrentProjectName("azedarach"), WithInitialCursor(1))
	model, _ := selector.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ProjectSelector).View()
}

func goldenImageAttachListView(t *testing.T) string {
	t.Helper()
	overlay := NewImageAttachOverlay("az-321", &mockClipboardAttachService{})
	overlay.files = []attachment.Attachment{
		{ID: "a1", IssueID: "az-321", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
		{ID: "a2", IssueID: "az-321", Filename: "mobile-panel.png", MimeType: "image/png", Size: 99872},
	}
	overlay.cursor = 1
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ImageAttachOverlay).View()
}

func goldenImageAttachPreviewView(t *testing.T) string {
	t.Helper()
	overlay := NewImageAttachOverlay("az-321", &mockClipboardAttachService{})
	overlay.mode = imageAttachModePreview
	overlay.files = []attachment.Attachment{
		{
			ID:       "a2",
			IssueID:  "az-321",
			Filename: "mobile-panel.png",
			MimeType: "image/png",
			Size:     99872,
			Path:     "/tmp/mobile-panel.png",
			Created:  time.Date(2026, time.March, 30, 8, 10, 0, 0, time.UTC),
		},
	}
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ImageAttachOverlay).View()
}

func goldenImagePreviewView(t *testing.T) string {
	t.Helper()
	overlay := NewImagePreviewOverlay("az-321", nil, 0)
	overlay.images = []attachment.Attachment{
		{ID: "a1", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
		{ID: "a2", Filename: "mobile-panel.png", MimeType: "image/png", Size: 99872},
	}
	overlay.currentIndex = 0
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ImagePreviewOverlay).View()
}

func goldenImagePreviewConfirmDeleteView(t *testing.T) string {
	t.Helper()
	overlay := NewImagePreviewOverlay("az-321", nil, 1)
	overlay.images = []attachment.Attachment{
		{ID: "a1", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
		{ID: "a2", Filename: "mobile-panel.png", MimeType: "image/png", Size: 99872},
	}
	overlay.currentIndex = 1
	overlay.confirmDelete = true
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ImagePreviewOverlay).View()
}

func goldenImageAttachListSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewImageAttachOverlay("az-321", &mockClipboardAttachService{})
	overlay.files = []attachment.Attachment{
		{ID: "a1", IssueID: "az-321", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
		{ID: "a2", IssueID: "az-321", Filename: "mobile-panel.png", MimeType: "image/png", Size: 99872},
	}
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*ImageAttachOverlay).View()
}

func goldenImagePreviewSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewImagePreviewOverlay("az-321", nil, 0)
	overlay.images = []attachment.Attachment{
		{ID: "a1", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
	}
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*ImagePreviewOverlay).View()
}

func goldenProjectSelectorSmallView(t *testing.T) string {
	t.Helper()
	registry := &config.ProjectsRegistry{
		DefaultProject: "chefy",
		Projects: []config.Project{
			{Name: "chefy", Path: "/Users/riordan/prog/Chefy"},
			{Name: "azedarach", Path: "/Users/riordan/prog/azedarach"},
		},
	}
	selector := NewProjectSelectorWithOptions(registry, WithCurrentProjectName("azedarach"), WithInitialCursor(1))
	model, _ := selector.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*ProjectSelector).View()
}
