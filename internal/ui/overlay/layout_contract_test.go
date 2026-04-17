package overlay

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

func TestProjectSelector_UsesActionsSectionLayout(t *testing.T) {
	selector := NewProjectSelector(&config.ProjectsRegistry{})
	view := selector.View()
	if !strings.Contains(view, "PROJECT SELECTOR") {
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
		t.Fatalf("expected image attachment title, got %q", view)
	}
	if !strings.Contains(view, "Actions") {
		t.Fatalf("expected actions section, got %q", view)
	}
}

func TestImagePreviewOverlay_UsesActionsSectionLayout(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()
	overlay := NewImagePreviewOverlay("az-1", service, 0)
	view := overlay.View()
	if !strings.Contains(view, "Image Preview") {
		t.Fatalf("expected image preview title, got %q", view)
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
