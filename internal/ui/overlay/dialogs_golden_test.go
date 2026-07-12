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
	"github.com/riordanpawley/azedarach/internal/services/git"
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
		{name: "close_failure", view: goldenCloseFailureView},
		{name: "confirm", view: goldenConfirmView},
		{name: "gitpull", view: goldenGitPullView},
		{name: "git_pane", view: goldenGitPaneView},
		{name: "mergechoice", view: goldenMergeChoiceView},
		{name: "merge_upstream", view: goldenMergeUpstreamView},
		{name: "devserver", view: goldenDevServerView},
		{name: "project_selector", view: goldenProjectSelectorView},
		{name: "settings_default", view: goldenSettingsDefaultView},
		{name: "diagnostics_overview", view: goldenDiagnosticsOverviewView},
		{name: "event_log_default", view: goldenEventLogView},
		{name: "operation_queue_default", view: goldenOperationQueueView},
		{name: "help_default", view: goldenHelpView},
		{name: "spec_workspace_default", view: goldenSpecWorkspaceView},
		{name: "create_task_attachments", view: goldenCreateTaskAttachmentsView},
		{name: "imageattach_list", view: goldenImageAttachListView},
		{name: "imageattach_preview", view: goldenImageAttachPreviewView},
		{name: "imagepreview_default", view: goldenImagePreviewView},
		{name: "imagepreview_confirm_delete", view: goldenImagePreviewConfirmDeleteView},
		{name: "orchestration", view: goldenOrchestrationView},
		{name: "orchestration_empty", view: goldenOrchestrationEmptyView},
		{name: "board_view", view: goldenBoardView},
		{name: "view_configurator", view: goldenViewConfigurator},
		{name: "interaction", view: goldenInteractionView},
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
		{name: "confirm_small", view: goldenConfirmSmallView},
		{name: "gitpull_small", view: goldenGitPullSmallView},
		{name: "git_pane_small", view: goldenGitPaneSmallView},
		{name: "conflict_small", view: goldenConflictSmallView},
		{name: "cleanup_small", view: goldenCleanupSmallView},
		{name: "close_failure_small", view: goldenCloseFailureSmallView},
		{name: "mergechoice_small", view: goldenMergeChoiceSmallView},
		{name: "imageattach_list_small", view: goldenImageAttachListSmallView},
		{name: "merge_upstream_small", view: goldenMergeUpstreamSmallView},
		{name: "imagepreview_default_small", view: goldenImagePreviewSmallView},
		{name: "imageattach_preview_small", view: goldenImageAttachPreviewSmallView},
		{name: "imagepreview_confirm_delete_small", view: goldenImagePreviewConfirmDeleteSmallView},
		{name: "project_selector_small", view: goldenProjectSelectorSmallView},
		{name: "settings_default_small", view: goldenSettingsDefaultSmallView},
		{name: "diagnostics_overview_small", view: goldenDiagnosticsOverviewSmallView},
		{name: "event_log_small", view: goldenEventLogSmallView},
		{name: "operation_queue_small", view: goldenOperationQueueSmallView},
		{name: "help_small", view: goldenHelpSmallView},
		{name: "spec_workspace_small", view: goldenSpecWorkspaceSmallView},
		{name: "create_task_attachments_small", view: goldenCreateTaskAttachmentsSmallView},
		{name: "orchestration_small", view: goldenOrchestrationSmallView},
		{name: "board_view_small", view: goldenBoardViewSmall},
		{name: "view_configurator_small", view: goldenViewConfiguratorSmall},
		{name: "interaction_small", view: goldenInteractionSmallView},
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

func goldenInteractionRequest() (domain.InteractionRequest, domain.InteractionAgeView) {
	now := time.Date(2026, time.July, 11, 2, 0, 0, 0, time.UTC)
	return domain.InteractionRequest{ID: "int-7", IssueID: "dcj", DecisionKey: "rollout", OrchestrationScope: "project", Question: "Which rollout path should we use?", Why: "The choice changes migration risk and user-visible availability.", Options: []domain.InteractionOption{{Key: "gradual", Label: "Gradual", Description: "Canary the change and expand after validation"}, {Key: "direct", Label: "Direct", Description: "Ship to everyone in one release"}}, Context: "docs/24-issue-state-model-v2-rollout.md", Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose rollout", Recommendation: "Use the gradual rollout."}, State: domain.InteractionOpen, Revision: 3, CreatedAt: now, UpdatedAt: now}, domain.InteractionAgeView{AgeSeconds: 3720, Stale: true}
}

func goldenInteractionView(t *testing.T) string {
	r, age := goldenInteractionRequest()
	o := NewInteractionOverlay(r, age)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*InteractionOverlay).View()
}

func goldenInteractionSmallView(t *testing.T) string {
	r, age := goldenInteractionRequest()
	o := NewInteractionOverlay(r, age)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*InteractionOverlay).View()
}

func goldenBoardView(t *testing.T) string {
	o := NewBoardViewOverlay(goldenBuiltInBoardViews(), domain.DefaultBoardViewID)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*BoardViewOverlay).View()
}

func goldenGitPaneView(t *testing.T) string {
	o := NewGitPaneOverlay("main")
	o.SetStatus(git.GitStatus{HasChanges: true, Modified: []string{"README.md"}, Untracked: []string{"notes.txt"}, GitAheadCount: 2, GitBehindCount: 1}, nil)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*GitPaneOverlay).View()
}

func goldenGitPaneSmallView(t *testing.T) string {
	o := NewGitPaneOverlay("main")
	o.SetStatus(git.GitStatus{GitBehindCount: 1}, nil)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 54, Height: 18})
	return model.(*GitPaneOverlay).View()
}

func goldenBoardViewSmall(t *testing.T) string {
	o := NewBoardViewOverlay(goldenBuiltInBoardViews(), domain.DefaultBoardViewID)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 54, Height: 18})
	return model.(*BoardViewOverlay).View()
}

func goldenViewConfigurator(t *testing.T) string {
	o := NewBoardViewOverlay(goldenBuiltInBoardViews(), domain.DefaultBoardViewID)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	o = model.(*BoardViewOverlay)
	model, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	return model.(*BoardViewOverlay).View()
}

func goldenViewConfiguratorSmall(t *testing.T) string {
	o := NewBoardViewOverlay(goldenBuiltInBoardViews(), domain.DefaultBoardViewID)
	model, _ := o.Update(tea.WindowSizeMsg{Width: 54, Height: 18})
	o = model.(*BoardViewOverlay)
	model, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	return model.(*BoardViewOverlay).View()
}

func goldenBuiltInBoardViews() []domain.BoardViewRecord {
	return []domain.BoardViewRecord{
		{View: domain.DefaultBoardView(), BuiltIn: true},
		{View: domain.PlanningBoardView(), BuiltIn: true},
		{View: domain.OrchestrationBoardView(), BuiltIn: true},
		{View: domain.CloseoutBoardView(), BuiltIn: true},
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

func goldenConfirmSmallView(t *testing.T) string {
	t.Helper()
	dialog := NewConfirmDialog("Delete task", "Delete task az-321?")
	model, _ := dialog.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
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

func goldenCloseFailureView(t *testing.T) string {
	t.Helper()
	dialog := NewCloseFailureDialog(
		"gav",
		"refusing to merge child issue gav directly into base: no active ancestor worktree branch was found; run `az worktree create gat`, then close the child into that target",
		CloseFailureDialogOptions{
			ParentID:            "gat",
			PreviousStatus:      "in_review",
			TargetStatus:        "closed",
			AllowAIMerge:        true,
			AllowCreateAncestor: true,
		},
	)
	model, _ := dialog.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*CloseFailureDialog).View()
}

func goldenCloseFailureSmallView(t *testing.T) string {
	t.Helper()
	dialog := NewCloseFailureDialog(
		"gav",
		"refusing to merge child issue gav directly into base: no active ancestor worktree branch was found; run `az worktree create gat`, then close the child into that target",
		CloseFailureDialogOptions{
			ParentID:            "gat",
			PreviousStatus:      "in_review",
			TargetStatus:        "closed",
			AllowAIMerge:        true,
			AllowCreateAncestor: true,
		},
	)
	model, _ := dialog.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*CloseFailureDialog).View()
}

func goldenGitPullView(t *testing.T) string {
	t.Helper()
	overlay := NewGitPullOverlay(7)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*GitPullOverlay).View()
}

func goldenGitPullSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewGitPullOverlay(7)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*GitPullOverlay).View()
}

func goldenMergeChoiceView(t *testing.T) string {
	t.Helper()
	overlay := NewMergeChoiceOverlay("az-123", 3, "main")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*MergeChoiceOverlay).View()
}

func goldenMergeChoiceSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewMergeChoiceOverlay("az-123", 6, "main")
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*MergeChoiceOverlay).View()
}

func goldenMergeUpstreamView(t *testing.T) string {
	t.Helper()
	target := &domain.Task{ID: "az-123", Title: "Implement feature X"}
	overlay := NewMergeSourceSelectOverlay(target, []MergeTarget{
		{ID: "az-456", Label: "Related task 1", Status: domain.StatusInProgress, HasWorktree: true},
	}, nil, nil)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*MergeSourceSelectOverlay).View()
}

func goldenMergeUpstreamSmallView(t *testing.T) string {
	t.Helper()
	target := &domain.Task{ID: "az-123", Title: "Implement feature X"}
	overlay := NewMergeSourceSelectOverlay(target, []MergeTarget{
		{ID: "az-456", Label: "Related task 1", Status: domain.StatusInProgress, HasWorktree: true},
	}, nil, nil)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*MergeSourceSelectOverlay).View()
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
	}, "/tmp/az-tui.log")
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

func goldenOperationQueueView(t *testing.T) string {
	t.Helper()
	overlay := NewOperationQueueOverlay(testOperationQueueSnapshot())
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*OperationQueueOverlay).View()
}

func goldenOperationQueueSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewOperationQueueOverlay(testOperationQueueSnapshot())
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*OperationQueueOverlay).View()
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

func goldenCreateTaskAttachmentsView(t *testing.T) string {
	t.Helper()
	overlay := NewCreateTaskOverlayWithParentImplOptionsAndAttachmentService(nil, nil, &mockClipboardAttachService{})
	overlay.attachments = []attachment.Attachment{{
		ID: "a1", IssueID: "draft-1", Filename: "task-context.md", MimeType: "text/markdown", Size: 1536,
	}}
	overlay.attachmentPreview = attachmentPreviewState{
		attachmentID: "a1",
		title:        "Markdown Preview",
		lines:        []string{"## Context", "Keep attachment controls visible."},
	}
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(*CreateTaskOverlay).View()
}

func goldenCreateTaskAttachmentsSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewCreateTaskOverlayWithParentImplOptionsAndAttachmentService(nil, nil, &mockClipboardAttachService{})
	overlay.attachments = []attachment.Attachment{{
		ID: "a1", IssueID: "draft-1", Filename: "task-context.md", MimeType: "text/markdown", Size: 1536,
	}}
	overlay.attachmentPreview = attachmentPreviewState{
		attachmentID: "a1",
		title:        "Markdown Preview",
		lines:        []string{"## Context", "Keep attachment controls visible."},
	}
	overlay.focusIndex = focusAttachments
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*CreateTaskOverlay).View()
}

func goldenImageAttachListView(t *testing.T) string {
	t.Helper()
	overlay := NewImageAttachOverlay("az-321", &mockClipboardAttachService{})
	overlay.files = []attachment.Attachment{
		{ID: "a1", IssueID: "az-321", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
		{ID: "a2", IssueID: "az-321", Filename: "mobile-panel.png", MimeType: "image/png", Size: 99872},
	}
	overlay.cursor = 1
	overlay.preview = attachmentPreviewState{
		attachmentID: "a2",
		title:        "Image Preview",
		lines:        []string{"Inline image rendering is not available in this terminal.", "Press Enter/v for full preview or o to open externally."},
	}
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
	overlay.preview = attachmentPreviewState{
		attachmentID: "a2",
		title:        "Image Preview",
		lines:        []string{"Inline image rendering is not available in this terminal.", "Press Enter/v for full preview or o to open externally."},
	}
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*ImageAttachOverlay).View()
}

func goldenImageAttachPreviewSmallView(t *testing.T) string {
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
	overlay.preview = attachmentPreviewState{
		attachmentID: "a2",
		title:        "Image Preview",
		lines:        []string{"Inline image rendering is not available in this terminal.", "Press Enter/v for full preview or o to open externally."},
	}
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
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

func goldenImagePreviewConfirmDeleteSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewImagePreviewOverlay("az-321", nil, 1)
	overlay.images = []attachment.Attachment{
		{ID: "a1", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
		{ID: "a2", Filename: "mobile-panel.png", MimeType: "image/png", Size: 99872},
	}
	overlay.currentIndex = 1
	overlay.confirmDelete = true
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	return model.(*ImagePreviewOverlay).View()
}

func goldenImageAttachListSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewImageAttachOverlay("az-321", &mockClipboardAttachService{})
	overlay.files = []attachment.Attachment{
		{ID: "a1", IssueID: "az-321", Filename: "board-screen.png", MimeType: "image/png", Size: 340223},
		{ID: "a2", IssueID: "az-321", Filename: "mobile-panel.png", MimeType: "image/png", Size: 99872},
	}
	overlay.preview = attachmentPreviewState{
		attachmentID: "a1",
		title:        "Image Preview",
		lines:        []string{"Inline image rendering is not available in this terminal.", "Press Enter/v for full preview or o to open externally."},
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

func goldenOrchestrationView(t *testing.T) string {
	t.Helper()
	overlay := NewOrchestrationOverlay(
		[]SessionInfo{
			{
				IssueID:               "az-123",
				TaskTitle:             "Implement feature X with careful layout handling",
				IssueStatus:           domain.StatusInProgress,
				State:                 domain.SessionBusy,
				StartedAt:             nil,
				Worktree:              "/Users/riordan/prog/azedarach",
				HasTmuxSession:        true,
				HasWorktree:           true,
				GitAheadCount:         2,
				HasUncommittedChanges: true,
				GitAdditions:          8,
				GitDeletions:          2,
				RecentOutput:          "build finished\nview rendered\nrenderDialogTwoPane ok",
			},
			{
				IssueID:        "az-456",
				TaskTitle:      "Fix selector overflow on mobile",
				IssueStatus:    domain.StatusInReview,
				State:          domain.SessionWaiting,
				Worktree:       "/Users/riordan/prog/Chefy",
				HasTmuxSession: true,
				HasWorktree:    true,
			},
		},
		nil, nil, nil, nil, nil,
	)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*OrchestrationOverlay).View()
}

func goldenOrchestrationEmptyView(t *testing.T) string {
	t.Helper()
	overlay := NewOrchestrationOverlay(nil, nil, nil, nil, nil, nil)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*OrchestrationOverlay).View()
}

func goldenOrchestrationSmallView(t *testing.T) string {
	t.Helper()
	overlay := NewOrchestrationOverlay(
		[]SessionInfo{
			{
				IssueID:               "az-123",
				TaskTitle:             "Implement feature X with careful layout handling",
				IssueStatus:           domain.StatusInProgress,
				State:                 domain.SessionBusy,
				StartedAt:             nil,
				Worktree:              "/Users/riordan/prog/azedarach",
				HasTmuxSession:        true,
				HasWorktree:           true,
				GitAheadCount:         2,
				HasUncommittedChanges: true,
				GitAdditions:          8,
				GitDeletions:          2,
				RecentOutput:          "build finished\nview rendered\nrenderDialogTwoPane ok",
			},
		},
		nil, nil, nil, nil, nil,
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
