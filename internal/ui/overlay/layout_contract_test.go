package overlay

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

func TestProjectSelector_UsesActionsSectionLayout(t *testing.T) {
	selector := NewProjectSelector(&config.ProjectsRegistry{})
	view := selector.View()
	if !strings.Contains(view, "SCOPE SELECTOR") {
		t.Fatalf("expected selector title, got %q", view)
	}
	if !strings.Contains(view, "Actions") {
		t.Fatalf("expected actions section, got %q", view)
	}
}

func TestDevServerOverlay_UsesActionsSectionLayout(t *testing.T) {
	overlay := NewDevServerOverlay([]DevServerInfo{
		{ID: "one", Name: "web", Port: 3000, Status: "running"},
	}, "az-1", nil, nil, nil, nil)
	view := overlay.View()
	if !strings.Contains(view, "DEV SERVERS") {
		t.Fatalf("expected dev server title, got %q", view)
	}
	if !strings.Contains(view, "Actions") {
		t.Fatalf("expected actions section, got %q", view)
	}
}

func TestProjectSelector_SizeUsesModeHelpers(t *testing.T) {
	selector := NewProjectSelector(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "one", Path: "/tmp/one"},
			{Name: "two", Path: "/tmp/two"},
		},
	})

	selector.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	if width, height := selector.Size(); width != 96 || height != 27 {
		t.Fatalf("expected responsive list size 96x27, got %dx%d", width, height)
	}

	selector.mode = projectModeActions
	if width, height := selector.Size(); width != 50 || height != 10 {
		t.Fatalf("expected action size 50x10, got %dx%d", width, height)
	}
}

func TestProjectSelector_SizeResponsiveInListMode(t *testing.T) {
	selector := NewProjectSelector(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "one", Path: "/tmp/one"},
		},
	})

	selector.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	if width, height := selector.Size(); width != 70 || height != 20 {
		t.Fatalf("expected small list size to use viewport clamp 70x20, got %dx%d", width, height)
	}
	view := selector.View()
	for _, action := range []string{"D", "detect", "Esc", "close"} {
		if !strings.Contains(view, action) {
			t.Fatalf("small selector clipped %q action:\n%s", action, view)
		}
	}
	if lipgloss.Width(view) > 70 || lipgloss.Height(view) > 20 {
		t.Fatalf("small selector exceeds bounds: %dx%d", lipgloss.Width(view), lipgloss.Height(view))
	}
}

func TestDevServerOverlay_SizeUsesDialogHelper(t *testing.T) {
	overlay := NewDevServerOverlay([]DevServerInfo{
		{ID: "one", Name: "web", Port: 3000, Status: "running"},
		{ID: "two", Name: "api", Port: 3001, Status: "stopped"},
		{ID: "three", Name: "queue", Port: 3002, Status: "error"},
	}, "az-1", nil, nil, nil, nil)

	overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	if width, height := overlay.Size(); width != 96 || height != 27 {
		t.Fatalf("expected responsive devserver size 96x27, got %dx%d", width, height)
	}

	overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	if width, height := overlay.Size(); width != 70 || height != 20 {
		t.Fatalf("expected small devserver size to use viewport clamp 70x20, got %dx%d", width, height)
	}
}

func TestImageAttachOverlay_UsesActionsSectionLayout(t *testing.T) {
	overlay := NewImageAttachOverlay("az-1", &mockAttachService{})
	overlay.files = []attachment.Attachment{{ID: "a1", IssueID: "az-1", Filename: "pic.png"}}
	view := overlay.View()
	if !strings.Contains(view, "ATTACHMENTS FOR az-1") {
		t.Fatalf("expected attachment title, got %q", view)
	}
	if !strings.Contains(view, "Actions") {
		t.Fatalf("expected actions section, got %q", view)
	}
}

func TestImageAttachOverlay_DefaultViewportUsesContentHeight(t *testing.T) {
	overlay := NewImageAttachOverlay("az-1", &mockAttachService{})
	overlay.Update(tea.WindowSizeMsg{Width: 160, Height: 60})

	if width, height := overlay.Size(); width != 84 || height != 14 {
		t.Fatalf("expected content-sized attachment dialog 84x14, got %dx%d", width, height)
	}
}

func TestImageAttachOverlay_LongListKeepsSelectionAndPreviewInBounds(t *testing.T) {
	overlay := NewImageAttachOverlay("az-1", &mockAttachService{})
	for idx := 1; idx <= 20; idx++ {
		overlay.files = append(overlay.files, attachment.Attachment{
			ID:       fmt.Sprintf("a%d", idx),
			IssueID:  "az-1",
			Filename: fmt.Sprintf("capture-%02d-with-a-long-name.png", idx),
			MimeType: "image/png",
		})
	}
	overlay.cursor = len(overlay.files) - 1
	overlay.preview = attachmentPreviewState{
		attachmentID: overlay.files[overlay.cursor].ID,
		title:        "Image Preview",
		lines:        []string{"Selected preview"},
	}
	overlay.Update(tea.WindowSizeMsg{Width: 160, Height: 60})

	view := overlay.View()
	_, height := overlay.Size()
	if lipgloss.Height(view) > height {
		t.Fatalf("view height = %d, dialog height = %d", lipgloss.Height(view), height)
	}
	if !strings.Contains(view, "capture-20") || !strings.Contains(view, "Selected preview") {
		t.Fatalf("selected attachment and preview must remain visible:\n%s", view)
	}
}

func TestImageAttachOverlay_NarrowViewportKeepsCompactPreview(t *testing.T) {
	overlay := NewImageAttachOverlay("az-1", &mockAttachService{})
	overlay.files = []attachment.Attachment{{ID: "a1", Filename: "capture.png", MimeType: "image/png"}}
	overlay.preview = attachmentPreviewState{attachmentID: "a1", title: "Image Preview", lines: []string{"1200x800 PNG image with a deliberately long compact fallback description"}}
	overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})

	view := overlay.View()
	if !strings.Contains(view, "Image Preview") || !strings.Contains(view, "1200x800") {
		t.Fatalf("narrow dialog must retain compact selected preview:\n%s", view)
	}
}

func TestImagePreviewOverlay_UsesActionsSectionLayout(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()
	overlay := NewImagePreviewOverlay("az-1", service, 0)
	view := overlay.View()
	if !strings.Contains(view, "Attachment Preview") {
		t.Fatalf("expected attachment preview title, got %q", view)
	}
	if !strings.Contains(view, "Actions") {
		t.Fatalf("expected actions section, got %q", view)
	}
}

type mockAttachService struct{}

func (m *mockAttachService) List(_ context.Context, _ string) ([]attachment.Attachment, error) {
	return nil, nil
}

func (m *mockAttachService) AttachFromClipboard(_ context.Context, _ string) (*attachment.Attachment, error) {
	return nil, nil
}

func (m *mockAttachService) Attach(_ context.Context, _ string, _ string) (*attachment.Attachment, error) {
	return nil, nil
}

func (m *mockAttachService) Delete(_ context.Context, _ string, _ string) error { return nil }
