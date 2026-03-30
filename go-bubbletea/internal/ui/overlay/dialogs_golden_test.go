package overlay

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
)

func TestDialogs_View_Golden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		view func(t *testing.T) string
	}{
		{name: "conflict", view: goldenConflictView},
		{name: "cleanup", view: goldenCleanupView},
		{name: "cleanup_confirm", view: goldenCleanupConfirmView},
		{name: "confirm", view: goldenConfirmView},
		{name: "gitpull", view: goldenGitPullView},
		{name: "mergechoice", view: goldenMergeChoiceView},
		{name: "merge_select", view: goldenMergeSelectView},
		{name: "merge_upstream", view: goldenMergeUpstreamView},
		{name: "devserver", view: goldenDevServerView},
		{name: "project_selector", view: goldenProjectSelectorView},
		{name: "settings_default", view: goldenSettingsDefaultView},
		{name: "diagnostics_overview", view: goldenDiagnosticsOverviewView},
		{name: "event_log_default", view: goldenEventLogView},
		{name: "help_default", view: goldenHelpView},
		{name: "spec_workspace_default", view: goldenSpecWorkspaceView},
		{name: "imageattach_list", view: goldenImageAttachListView},
		{name: "imageattach_preview", view: goldenImageAttachPreviewView},
		{name: "imagepreview_default", view: goldenImagePreviewView},
		{name: "imagepreview_confirm_delete", view: goldenImagePreviewConfirmDeleteView},
		{name: "orchestration", view: goldenOrchestrationView},
		{name: "orchestration_empty", view: goldenOrchestrationEmptyView},
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
		{name: "conflict_small", view: goldenConflictSmallView},
		{name: "cleanup_small", view: goldenCleanupSmallView},
		{name: "imageattach_list_small", view: goldenImageAttachListSmallView},
		{name: "merge_select_small", view: goldenMergeSelectSmallView},
		{name: "imagepreview_default_small", view: goldenImagePreviewSmallView},
		{name: "project_selector_small", view: goldenProjectSelectorSmallView},
		{name: "settings_default_small", view: goldenSettingsDefaultSmallView},
		{name: "diagnostics_overview_small", view: goldenDiagnosticsOverviewSmallView},
		{name: "event_log_small", view: goldenEventLogSmallView},
		{name: "help_small", view: goldenHelpSmallView},
		{name: "spec_workspace_small", view: goldenSpecWorkspaceSmallView},
		{name: "orchestration_small", view: goldenOrchestrationSmallView},
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

func goldenConflictView(t *testing.T) string {
	t.Helper()
	dialog := NewConflictDialog([]string{
		"internal/ui/app.go",
		"internal/domain/task.go",
		"internal/services/git.go",
	})
	model, _ := dialog.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ConflictOverlay).View()
}

func goldenConflictSmallView(t *testing.T) string {
	t.Helper()
	dialog := NewConflictDialog([]string{
		"internal/ui/app.go",
		"internal/domain/task.go",
		"internal/services/git.go",
	})
	model, _ := dialog.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*ConflictOverlay).View()
}

func goldenCleanupView(t *testing.T) string {
	t.Helper()
	overlay := NewBulkCleanupOverlay(func(context.Context, []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}, 100, 5, 2)
	overlay.categories[0].Selected = true
	overlay.categories[2].Selected = true
	overlay.cursor = 2
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*BulkCleanupOverlay).View()
}

func goldenCleanupConfirmView(t *testing.T) string {
	t.Helper()
	overlay := NewBulkCleanupOverlay(func(context.Context, []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}, 100, 5, 2)
	overlay.categories[0].Selected = true
	overlay.categories[2].Selected = true
	overlay.confirmMode = true
	overlay.confirmSelected = true
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*BulkCleanupOverlay).View()
}

func goldenCleanupSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewBulkCleanupOverlay(func(context.Context, []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}, 100, 5, 2)
	overlay.categories[0].Selected = true
	overlay.categories[2].Selected = true
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*BulkCleanupOverlay).View()
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

func goldenMergeSelectView(t *testing.T) string {
	t.Helper()
	source := &domain.Task{ID: "az-123", Title: "Implement feature X"}
	overlay := NewMergeSelectOverlay(source, []MergeTarget{
		{ID: "az-456", Label: "Related task 1", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-789", Label: "Related task 2", Status: domain.StatusDone, HasWorktree: false},
	}, nil, nil)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*MergeSelectOverlay).View()
}

func goldenMergeUpstreamView(t *testing.T) string {
	t.Helper()
	source := &domain.Task{ID: "az-123", Title: "Implement feature X"}
	overlay := NewMergeSourceSelectOverlay(source, []MergeTarget{
		{ID: "az-456", Label: "Related task 1", Status: domain.StatusOpen, HasWorktree: true},
	}, nil, nil)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*MergeSelectOverlay).View()
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

func goldenSettingsDefaultView(t *testing.T) string {
	t.Helper()
	menu := NewDefaultSettingsOverlay()
	model, _ := menu.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*SettingsOverlay).View()
}

func goldenSettingsDefaultSmallView(t *testing.T) string {
	t.Helper()
	menu := NewDefaultSettingsOverlay()
	model, _ := menu.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*SettingsOverlay).View()
}

func goldenDiagnosticsOverviewView(t *testing.T) string {
	t.Helper()
	panel := newGoldenDiagnosticsPanel()
	model, _ := panel.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*DiagnosticsPanel).View()
}

func goldenDiagnosticsOverviewSmallView(t *testing.T) string {
	t.Helper()
	panel := newGoldenDiagnosticsPanel()
	model, _ := panel.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*DiagnosticsPanel).View()
}

func goldenEventLogView(t *testing.T) string {
	t.Helper()
	overlay := NewEventLogOverlayWithLogFile([]protocol.EventEnvelope{
		{
			ProtocolVersion: protocol.CurrentVersion,
			ProjectID:       "azedarach",
			Revision:        41,
			Event:           "daemon.event.old",
			Kind:            protocol.EnvelopeKindEvent,
			EmittedAt:       time.Date(2026, time.March, 30, 8, 20, 0, 0, time.UTC),
			Meta: protocol.Metadata{
				SessionID:     "sess-1",
				CorrelationID: "corr-old",
			},
			Body: []byte("old payload line"),
		},
		{
			ProtocolVersion: protocol.CurrentVersion,
			ProjectID:       "azedarach",
			Revision:        42,
			Event:           "daemon.event.new",
			Kind:            protocol.EnvelopeKindEvent,
			EmittedAt:       time.Date(2026, time.March, 30, 8, 21, 0, 0, time.UTC),
			Meta: protocol.Metadata{
				SessionID:     "sess-1",
				CorrelationID: "corr-new",
			},
			Body: []byte("new payload line"),
		},
	}, "/tmp/az.log")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*EventLogOverlay).View()
}

func goldenEventLogSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewEventLogOverlay([]protocol.EventEnvelope{
		{
			ProtocolVersion: protocol.CurrentVersion,
			ProjectID:       "azedarach",
			Revision:        42,
			Event:           "daemon.event.new",
			Kind:            protocol.EnvelopeKindEvent,
			EmittedAt:       time.Date(2026, time.March, 30, 8, 21, 0, 0, time.UTC),
			Body:            []byte("new payload line"),
		},
	})
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*EventLogOverlay).View()
}

func goldenHelpView(t *testing.T) string {
	t.Helper()
	overlay := NewHelpOverlay()
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*HelpOverlay).View()
}

func goldenHelpSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewHelpOverlay()
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*HelpOverlay).View()
}

func goldenSpecWorkspaceView(t *testing.T) string {
	t.Helper()
	overlay := NewSpecWorkspaceOverlay("azedarach")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*SpecWorkspaceOverlay).View()
}

func goldenSpecWorkspaceSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewSpecWorkspaceOverlay("azedarach")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*SpecWorkspaceOverlay).View()
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

func goldenMergeSelectSmallView(t *testing.T) string {
	t.Helper()
	source := &domain.Task{ID: "az-123", Title: "Implement feature X"}
	overlay := NewMergeSelectOverlay(source, []MergeTarget{
		{ID: "az-456", Label: "Related task 1", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-789", Label: "Related task 2", Status: domain.StatusDone, HasWorktree: false},
	}, nil, nil)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*MergeSelectOverlay).View()
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

func goldenOrchestrationView(t *testing.T) string {
	t.Helper()
	overlay := NewOrchestrationOverlay(
		[]SessionInfo{
			{
				IssueID:      "az-123",
				TaskTitle:    "Implement feature X with careful layout handling",
				State:        domain.SessionBusy,
				StartedAt:    nil,
				Worktree:     "/Users/riordan/prog/azedarach",
				RecentOutput: "build finished\nview rendered\nrenderDialogTwoPane ok",
			},
			{
				IssueID:   "az-456",
				TaskTitle: "Fix selector overflow on mobile",
				State:     domain.SessionWaiting,
				Worktree:  "/Users/riordan/prog/Chefy",
			},
		},
		nil, nil, nil,
	)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*OrchestrationOverlay).View()
}

func goldenOrchestrationEmptyView(t *testing.T) string {
	t.Helper()
	overlay := NewOrchestrationOverlay(nil, nil, nil, nil)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*OrchestrationOverlay).View()
}

func goldenOrchestrationSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewOrchestrationOverlay(
		[]SessionInfo{
			{
				IssueID:      "az-123",
				TaskTitle:    "Implement feature X with careful layout handling",
				State:        domain.SessionBusy,
				StartedAt:    nil,
				Worktree:     "/Users/riordan/prog/azedarach",
				RecentOutput: "build finished\nview rendered\nrenderDialogTwoPane ok",
			},
		},
		nil, nil, nil,
	)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*OrchestrationOverlay).View()
}

func newGoldenDiagnosticsPanel() *DiagnosticsPanel {
	now := time.Date(2026, time.March, 30, 8, 30, 0, 0, time.UTC)
	mockService := &mockDiagnosticsService{
		diagnostics: &diagnostics.SystemDiagnostics{
			Timestamp:    now,
			OverallState: diagnostics.HealthHealthy,
			Sessions: []diagnostics.SessionInfo{
				{
					IssueID:  "az-123",
					State:    domain.SessionBusy,
					Uptime:   5 * time.Minute,
					Worktree: "/Users/riordan/prog/azedarach",
				},
			},
			Ports: []diagnostics.PortInfo{
				{Port: 3000, IssueID: "az-123", InUse: true, Available: true},
			},
			Worktrees: []diagnostics.WorktreeInfo{
				{IssueID: "az-123", Path: "/Users/riordan/prog/azedarach", IsHealthy: true},
			},
			Network: diagnostics.NetworkInfo{
				IsOnline:  true,
				LastCheck: now,
				Latency:   21 * time.Millisecond,
			},
			System: diagnostics.SystemInfo{
				GoVersion:    "go1.21.5",
				OS:           "darwin",
				Arch:         "arm64",
				NumGoroutine: 42,
				MemoryUsage:  64 * 1024 * 1024,
			},
			Operations: diagnostics.OperationInfo{
				Total:      3,
				Busy:       1,
				Waiting:    1,
				Done:       1,
				Cancelable: 2,
			},
		},
	}
	panel := NewDiagnosticsPanel(mockService, map[string]*domain.Session{})
	panel.currentDiagnostics = mockService.diagnostics
	return panel
}
