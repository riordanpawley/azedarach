package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func (m Model) handleBulkAction(msg overlay.BulkActionMsg) (tea.Model, tea.Cmd) {
	count := len(msg.SelectedIDs)
	if count == 0 {
		return m, nil
	}

	switch msg.Action {
	case "h": // Previous lifecycle action
		m.beginMutationFeedback(fmt.Sprintf("Bulk lifecycle action queued for %d task(s)", count))
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, -1)

	case "l": // Next lifecycle action
		closeTaskIDs := m.bulkMoveCloseCleanupTaskIDs(msg.SelectedIDs, 1)
		if len(closeTaskIDs) > 0 {
			pending := pendingCloseCleanupConfirmation{
				taskIDs:      append([]string(nil), msg.SelectedIDs...),
				closeTaskIDs: append([]string(nil), closeTaskIDs...),
				bulkMode:     "move",
				delta:        1,
				summaries:    m.closeCleanupSummaries(closeTaskIDs),
			}
			pending = m.prepareCloseCleanupConfirmation(pending)
			m.pendingClose = &pending
			return m, m.confirmCloseCleanupCmd(pending)
		}
		m.beginMutationFeedback(fmt.Sprintf("Bulk lifecycle action queued for %d task(s)", count))
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, 1)

	case "0": // Move to Backlog
		m.beginMutationFeedback(fmt.Sprintf("Bulk backlog update queued for %d task(s)", count))
		return m, m.bulkSetLifecycleCmd(msg.SelectedIDs, domain.IssueWorkflowBacklog)

	case "1": // Move to Open
		m.beginMutationFeedback(fmt.Sprintf("Bulk open update queued for %d task(s)", count))
		return m, m.bulkSetLifecycleCmd(msg.SelectedIDs, domain.IssueWorkflowOpen)

	case "2": // Start active work
		m.beginMutationFeedback(fmt.Sprintf("Bulk active update queued for %d task(s)", count))
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusInProgress)

	case "3": // Request review
		m.beginMutationFeedback(fmt.Sprintf("Bulk review request queued for %d task(s)", count))
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusInReview)

	case "4": // Integrate and close
		pending := pendingCloseCleanupConfirmation{
			taskIDs:      append([]string(nil), msg.SelectedIDs...),
			closeTaskIDs: append([]string(nil), msg.SelectedIDs...),
			bulkMode:     "set",
			targetStatus: domain.StatusDone,
			summaries:    m.closeCleanupSummaries(msg.SelectedIDs),
		}
		pending = m.prepareCloseCleanupConfirmation(pending)
		m.pendingClose = &pending
		return m, m.confirmCloseCleanupCmd(pending)

	case "d": // Delete selected
		m.beginMutationFeedback(fmt.Sprintf("Bulk delete queued for %d task(s)", count))
		return m, m.bulkDeleteCmd(msg.SelectedIDs)

	case "a":
		m.beginMutationFeedback(fmt.Sprintf("Bulk archive queued for %d task(s)", count))
		return m, m.bulkArchiveCmd(msg.SelectedIDs)

	case "w": // Cleanup selected worktrees
		m.beginMutationFeedback(fmt.Sprintf("Bulk cleanup preflight queued for %d task(s)", count))
		return m, m.bulkCleanupPreflightCmd(msg.SelectedIDs, false)

	case "W": // Delete selected tasks and cleanup worktrees
		m.beginMutationFeedback(fmt.Sprintf("Bulk delete + cleanup preflight queued for %d task(s)", count))
		return m, m.bulkCleanupPreflightCmd(msg.SelectedIDs, true)

	case "x": // Clear selection
		m.editor.ClearSelection()
		m.editor.EnterNormal()
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Selection cleared",
			Expires: time.Now().Add(2 * time.Second),
		})
	}

	return m, nil
}

func (m Model) modelForCloseFailureAction(action overlay.CloseFailureActionMsg) Model {
	scoped := m.modelForProjectActionContext(overlay.ProjectActionContext{
		ProjectID:    action.ProjectID,
		ProjectName:  action.ProjectName,
		ProjectPath:  action.ProjectPath,
		DaemonSocket: action.DaemonSocket,
		BaseBranch:   action.BaseBranch,
	})
	if worktree := strings.TrimSpace(action.SourceWorktree); worktree != "" {
		sessions := make(map[string]*domain.Session, len(scoped.sessions)+1)
		for issueID, session := range scoped.sessions {
			sessions[issueID] = cloneSession(session)
		}
		scoped.sessions = sessions
		scoped.sessions[taskIDKey(action.TaskID)] = &domain.Session{
			IssueID:  naming.IssueID(strings.TrimSpace(action.TaskID)),
			State:    domain.SessionIdle,
			Worktree: worktree,
		}
	}
	return scoped
}

func (m Model) modelForProjectActionContext(context overlay.ProjectActionContext) Model {
	scoped := m
	if projectID := strings.TrimSpace(context.ProjectID); projectID != "" {
		scoped.daemonClient = m.scopedDaemonClientForSocket(strings.TrimSpace(context.DaemonSocket), projectID)
		scoped.daemonProjectRouteID = naming.ProjectID(protocol.NormalizeProjectID(projectID))
	}
	if projectName := strings.TrimSpace(context.ProjectName); projectName != "" {
		scoped.currentProject = projectName
	}
	if projectPath := strings.TrimSpace(context.ProjectPath); projectPath != "" {
		scoped.repoDir = projectPath
	}
	if baseBranch := strings.TrimSpace(context.BaseBranch); baseBranch != "" {
		cfg := config.DefaultConfig()
		if scoped.config != nil {
			clone := *scoped.config
			cfg = &clone
		}
		cfg.Git.BaseBranch = baseBranch
		scoped.config = cfg
	}
	return scoped
}

func taskFromCloseFailureAction(action overlay.CloseFailureActionMsg) *domain.Task {
	taskID := strings.TrimSpace(action.TaskID)
	if taskID == "" {
		return nil
	}
	task := &domain.Task{ID: naming.IssueID(taskID)}
	if parentID := strings.TrimSpace(action.ParentID); parentID != "" {
		parent := naming.IssueID(parentID)
		task.ParentID = &parent
	}
	if worktree := strings.TrimSpace(action.SourceWorktree); worktree != "" {
		task.Session = &domain.Session{
			IssueID:  naming.IssueID(taskID),
			State:    domain.SessionIdle,
			Worktree: worktree,
		}
		task.HasWorktree = true
	}
	return task
}

func mergePreflightWorktreeSelection(value any) (overlay.MergePreflightWorktreeSelection, bool) {
	switch selection := value.(type) {
	case overlay.MergePreflightWorktreeSelection:
		selection.Worktree = strings.TrimSpace(selection.Worktree)
		return selection, selection.Worktree != ""
	case string:
		worktree := strings.TrimSpace(selection)
		return overlay.MergePreflightWorktreeSelection{Worktree: worktree}, worktree != ""
	default:
		return overlay.MergePreflightWorktreeSelection{}, false
	}
}

// handleSelection handles overlay selection messages
func (m Model) handleSelection(msg overlay.SelectionMsg) (tea.Model, tea.Cmd) {
	selectionTaskID := ""
	if workspace, ok := m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); ok {
		selectionTaskID = strings.TrimSpace(workspace.TaskID())
	}

	// Handle special overlay-specific messages first (before popping overlay)
	switch msg.Key {
	case "abort", "agent", "manual":
		// Conflict resolution messages - extract the value
		if resolution, ok := msg.Value.(overlay.ConflictResolutionMsg); ok {
			return m.handleConflictResolution(resolution)
		}
	case "merge":
		// Merge target selection message
		if mergeMsg, ok := msg.Value.(overlay.MergeTargetSelectedMsg); ok {
			return m.handleMergeTargetSelection(mergeMsg)
		}
	case "m":
		task, _ := m.getCurrentTaskAndSession()
		if selectionTaskID != "" {
			if selectedTask, _, ok := m.taskAndSessionByID(selectionTaskID); ok {
				task = selectedTask
			}
		}
		if task != nil {
			m.beginMutationFeedback(fmt.Sprintf("Preparing merge for %s", task.ID))
		}
		return m.mergeCurrentIssueIntoDefaultTarget(task)
	case "merge_preflight_abort":
		m.overlayStack.Pop()
		selection, ok := mergePreflightWorktreeSelection(msg.Value)
		if !ok {
			return m, nil
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		m.beginMutationFeedback("Abort merge queued")
		return m, actionModel.abortMergeCmd(selection.Worktree)
	case "merge_preflight_discard_source":
		m.overlayStack.Pop()
		selection, ok := mergePreflightWorktreeSelection(msg.Value)
		if !ok {
			return m, nil
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		m.beginMutationFeedback("Discard source changes queued")
		return m, actionModel.discardChangesCmd("source", selection.Worktree)
	case "merge_preflight_discard_target":
		m.overlayStack.Pop()
		selection, ok := mergePreflightWorktreeSelection(msg.Value)
		if !ok {
			return m, nil
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		m.beginMutationFeedback("Discard target changes queued")
		return m, actionModel.discardChangesCmd("target", selection.Worktree)
	case "merge_preflight_commit_source":
		m.overlayStack.Pop()
		selection, ok := mergePreflightWorktreeSelection(msg.Value)
		if !ok {
			return m, nil
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		m.beginMutationFeedback("Commit source changes queued")
		return m, actionModel.commitChangesCmd("source", selection.Worktree)
	case "merge_preflight_commit_target":
		m.overlayStack.Pop()
		selection, ok := mergePreflightWorktreeSelection(msg.Value)
		if !ok {
			return m, nil
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		m.beginMutationFeedback("Commit target changes queued")
		return m, actionModel.commitChangesCmd("target", selection.Worktree)
	case "merge_preflight_agent":
		m.overlayStack.Pop()
		selection, ok := msg.Value.(overlay.MergePreflightAgentSelection)
		if !ok {
			return m, nil
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		m.beginMutationFeedback(fmt.Sprintf("Agent merge queued for %s -> %s", selection.SourceID, selection.TargetID))
		return m, actionModel.resolveMergePreflightWithAgentCmd(selection)
	case "merge_preflight_ignore_source_dirty":
		m.overlayStack.Pop()
		selection, ok := msg.Value.(overlay.MergePreflightRefreshSelection)
		if !ok {
			return m, nil
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		opts := mergePreflightOptions{
			ignoreSourceDirty:     true,
			stopTargetBeforeMerge: selection.StopTargetBeforeMerge,
			targetState:           domain.SessionBusy,
		}
		m.beginMutationFeedback(fmt.Sprintf("Merge queued for %s -> %s ignoring source dirty files", selection.SourceID, selection.TargetID))
		if strings.TrimSpace(selection.TargetID) == mergeBaseTargetID {
			return m, actionModel.mergeToBaseCmdWithOptions(strings.TrimSpace(selection.SourceWorktree), strings.TrimSpace(selection.SourceID), true, opts)
		}
		return m, actionModel.mergeFeatureIntoFeatureCmdWithOptions(
			strings.TrimSpace(selection.SourceWorktree),
			strings.TrimSpace(selection.TargetWorktree),
			strings.TrimSpace(selection.SourceID),
			strings.TrimSpace(selection.TargetID),
			true,
			opts,
		)
	case "merge_preflight_refresh":
		m.overlayStack.Pop()
		selection, ok := msg.Value.(overlay.MergePreflightRefreshSelection)
		if !ok {
			m.beginMutationFeedback("Refreshing merge preflight")
			return m, m.scheduleIssuesRefreshAfterRuntimeReconcileCmd()
		}
		actionModel := m.modelForProjectActionContext(selection.Context)
		m.beginMutationFeedback("Refreshing merge preflight")
		return m, actionModel.refreshMergePreflightCmd(selection)
	case "projects":
		// Settings -> Manage projects
		m.overlayStack.Pop() // Close settings
		return m, m.openOverlay(overlay.NewProjectSelectorWithOptions(
			m.projectRegistry,
			overlay.WithInitialCursor(m.projectSelectorCursor()),
			overlay.WithCurrentProjectName(m.currentProject),
			overlay.WithGlobalScope(m.scope.IsGlobal()),
		))
	case "event-log-stream":
		switch value := msg.Value.(type) {
		case string:
			return m, m.openLogStreamCmd(value)
		case []string:
			return m, m.openLogStreamCmd(value...)
		}
		return m, nil
	case "event-log-editor":
		if path, ok := msg.Value.(string); ok {
			return m, m.openLogEditorCmd(path)
		}
		return m, nil
	case "editor":
		if path, ok := msg.Value.(string); ok && strings.TrimSpace(path) != "" {
			return m, m.openSettingsEditorCmd(path)
		}
		return m, m.openSettingsEditorCmd()
	case "event-log-error":
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Log action failed: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	case "event-log-opened":
		return m, tea.ClearScreen
	case "close_failure_action":
		m.overlayStack.Pop()
		action, ok := msg.Value.(overlay.CloseFailureActionMsg)
		if !ok {
			return m, nil
		}
		if action.Action == overlay.CloseFailureActionCancel {
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: fmt.Sprintf("Cancelled close retry for %s", action.TaskID),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		actionModel := m.modelForCloseFailureAction(action)
		if action.Action == overlay.CloseFailureActionAIMerge {
			task, _, ok := actionModel.taskAndSessionByID(action.TaskID)
			if !ok {
				task = taskFromCloseFailureAction(action)
			}
			if task == nil {
				m.addToast(Toast{
					Level:   ToastWarning,
					Message: fmt.Sprintf("Issue %s is unavailable for AI merge", action.TaskID),
					Expires: time.Now().Add(5 * time.Second),
				})
				return m, nil
			}
			m.beginMutationFeedback(fmt.Sprintf("Checking AI merge readiness for %s", action.TaskID))
			_, cmd := actionModel.agentMergeCurrentIssueIntoDefaultTarget(task)
			return m, cmd
		}
		if action.Action == overlay.CloseFailureActionCreatePR {
			m.beginMutationFeedback(fmt.Sprintf("Preparing PR for %s", action.TaskID))
			return m, actionModel.openPROverlayCmd(action.SourceWorktree, action.TaskID)
		}
		if action.Action == overlay.CloseFailureActionCreateAncestor {
			m.beginMutationFeedback(fmt.Sprintf("Creating ancestor worktree %s for %s", action.ParentID, action.TaskID))
			return m, actionModel.createAncestorWorktreeAndRetryCloseCmd(action)
		}
		previousStatus := domain.Status(strings.TrimSpace(action.PreviousStatus))
		targetStatus := domain.Status(strings.TrimSpace(action.TargetStatus))
		if previousStatus == "" {
			previousStatus = domain.StatusInReview
		}
		if targetStatus == "" {
			targetStatus = domain.StatusDone
		}
		opts := daemonclient.TaskStatusOptions{
			ForceWorktree:      action.ForceWorktree,
			CloseCleanChildren: action.CloseCleanChildren,
			AllowActiveSession: action.AllowActiveSession,
		}
		m.beginTaskStatusMoveFeedback(action.TaskID, previousStatus, targetStatus)
		switch action.Action {
		case overlay.CloseFailureActionForceWorktree:
			m.addToast(Toast{Level: ToastWarning, Message: fmt.Sprintf("Retrying close for %s with forced worktree cleanup", action.TaskID), Expires: time.Now().Add(5 * time.Second)})
		case overlay.CloseFailureActionAllowActiveSession:
			m.addToast(Toast{Level: ToastWarning, Message: fmt.Sprintf("Retrying close for %s and allowing active session cleanup", action.TaskID), Expires: time.Now().Add(5 * time.Second)})
		case overlay.CloseFailureActionCloseCleanChildren:
			m.addToast(Toast{Level: ToastInfo, Message: fmt.Sprintf("Retrying close for %s and clean child issues", action.TaskID), Expires: time.Now().Add(5 * time.Second)})
		default:
			m.addToast(Toast{Level: ToastInfo, Message: fmt.Sprintf("Retrying close for %s", action.TaskID), Expires: time.Now().Add(3 * time.Second)})
		}
		return m, actionModel.moveTaskStatusCmdWithOptions(action.TaskID, previousStatus, targetStatus, opts)
	case "editor-error":
		// Editor open error
		m.overlayStack.Pop()
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Editor error: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	case "editor-closed":
		// Editor closed successfully
		m.overlayStack.Pop()
		return m, nil
	case "select_child":
		// Epic drill-down: child task selected
		m.overlayStack.Pop()
		if childID, ok := msg.Value.(string); ok {
			// Jump to the child task by ID
			columns := m.buildColumns()
			m.nav.JumpToTaskByID(columns, childID)
			m.ensureCursorVisible(columns)
		}
		return m, nil
	case "task_workspace_open_task":
		targetID, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(targetID) == "" {
			return m, nil
		}
		task, _, ok := m.taskAndSessionByID(targetID)
		if !ok || task == nil {
			if workspace, workspaceOK := m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); workspaceOK {
				if contextualTask, found := workspace.TaskByID(targetID); found {
					task = &contextualTask
					ok = true
				}
			}
			if !ok || task == nil {
				m.addToast(Toast{
					Level:   ToastWarning,
					Message: fmt.Sprintf("Related task %s is not loaded", strings.TrimSpace(targetID)),
					Expires: time.Now().Add(5 * time.Second),
				})
				return m, nil
			}
		}
		columns := m.buildColumns()
		m.nav.JumpToTaskByID(columns, task.ID.String())
		m.ensureCursorVisible(columns)
		if workspace, ok := m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); ok {
			workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
			relatedTasks := m.tasks
			if _, _, onBoard := m.taskAndSessionByID(task.ID.String()); !onBoard {
				// Keep the existing workspace snapshot until task.get refreshes the
				// newly selected off-board issue.
				relatedTasks = workspace.ContextTasks()
			}
			workspace.SyncTask(*task, relatedTasks, m.pendingMutationForTask(task.ID.String()))
		}
		if m.daemonClient == nil {
			return m, nil
		}
		return m, m.refreshTaskWorkspaceInBackgroundCmd(task.ID.String())
	case "task_workspace_drill_down":
		targetID, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(targetID) == "" {
			return m, nil
		}
		return m.enterDrillDownByID(targetID)
	case "set-default-success", "remove-success", "detect-success":
		// Project registry actions succeeded - just show success toast
		if name, ok := msg.Value.(string); ok {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Project %s: %s", msg.Key[:len(msg.Key)-8], name), // Remove "-success"
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		return m, nil
	case "settings-save-error":
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to save settings: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	case "set-default-error", "remove-error", "add-error", "save-error", "detect-error":
		// Project registry actions failed
		if err, ok := msg.Value.(error); ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Error: %v", err),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil
	}

	// Keep task workspace open for actions that should layer over it or return
	// to it without forcing users to reopen the details.
	keepWorkspaceOpen := isTaskWorkspaceOverlay(m.overlayStack.Current()) &&
		(msg.Key == "a" || msg.Key == "c" || msg.Key == "o" || msg.Key == "U" || msg.Key == "y" || msg.Key == "r" || msg.Key == "w" || msg.Key == "W" || msg.Key == "x")
	if !keepWorkspaceOpen {
		m.overlayStack.Pop()
	}

	if msg.Key == "yes" && m.pendingBulkCleanup != nil {
		pending := m.pendingBulkCleanup
		m.pendingBulkCleanup = nil
		m.beginMutationFeedback(fmt.Sprintf("Bulk cleanup queued for %d task(s)", len(pending.taskIDs)))
		return m, m.bulkCleanupWorktreeCmd(pending.taskIDs, pending.deletedTasks, pending.deleteBranch)
	}
	if msg.Key == "yes-delete-branch" && m.pendingBulkCleanup != nil {
		pending := m.pendingBulkCleanup
		pending.deleteBranch = true
		m.pendingBulkCleanup = nil
		m.beginMutationFeedback(fmt.Sprintf("Bulk cleanup + branch delete queued for %d task(s)", len(pending.taskIDs)))
		return m, m.bulkCleanupWorktreeCmd(pending.taskIDs, pending.deletedTasks, true)
	}
	if msg.Key == "no" && m.pendingBulkCleanup != nil {
		pending := m.pendingBulkCleanup
		m.pendingBulkCleanup = nil
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Cancelled bulk cleanup for %d task(s)", len(pending.taskIDs)),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}

	if msg.Key == "yes" && m.pendingCleanup != nil {
		pending := m.pendingCleanup
		m.pendingCleanup = nil
		m.beginMutationFeedback(fmt.Sprintf("Worktree cleanup queued for %s", pending.taskID))
		return m, m.cleanupWorktreeCmd(pending.taskID, pending.deletedTask, pending.force, pending.deleteBranch)
	}
	if msg.Key == "yes-delete-branch" && m.pendingCleanup != nil {
		pending := m.pendingCleanup
		pending.deleteBranch = true
		m.pendingCleanup = nil
		branch := cleanupBranchLabel(pending.branch)
		m.beginMutationFeedback(fmt.Sprintf("Worktree cleanup + branch delete queued for %s (%s)", pending.taskID, branch))
		return m, m.cleanupWorktreeCmd(pending.taskID, pending.deletedTask, pending.force, true)
	}
	if msg.Key == "no" && m.pendingCleanup != nil {
		pending := m.pendingCleanup
		m.pendingCleanup = nil
		cancelMessage := fmt.Sprintf("Cancelled cleanup for %s", pending.taskID)
		if pending.force {
			cancelMessage = fmt.Sprintf("Cancelled forced cleanup for %s", pending.taskID)
		}
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: cancelMessage,
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}

	if (msg.Key == "yes" || msg.Key == "close_clean_children") && m.pendingClose != nil {
		pending := *m.pendingClose
		m.pendingClose = nil
		if msg.Key == "yes" && pending.targetOnlyBlockedByChildren {
			m.pendingClose = &pending
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Target-only close is blocked by unresolved children; press C or cancel",
				Expires: time.Now().Add(4 * time.Second),
			})
			return m, m.confirmCloseCleanupCmd(pending)
		}
		if msg.Key == "close_clean_children" {
			pending.closeCleanChildren = true
		}
		if reason := pendingCloseCleanupBlockedReason(pending); reason != "" {
			m.overlayStack.Pop()
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Refreshing close preflight before blocking dirty worktree state",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.confirmCloseCleanupPreflightCmd(pending)
		}
		if len(pending.taskIDs) > 0 {
			m.beginMutationFeedback(fmt.Sprintf("Bulk close queued for %d task(s)", len(pending.taskIDs)))
			opts := daemonclient.TaskStatusOptions{CloseCleanChildren: pending.closeCleanChildren}
			if pending.bulkMode == "move" {
				return m, m.bulkMoveStatusCmdWithOptions(pending.taskIDs, pending.delta, opts)
			}
			return m, m.bulkSetStatusCmdWithOptions(pending.taskIDs, pending.targetStatus, opts)
		}
		m.beginTaskStatusMoveFeedback(pending.taskID, pending.previousStatus, pending.targetStatus)
		return m, m.moveTaskStatusCmdWithOptions(pending.taskID, pending.previousStatus, pending.targetStatus, daemonclient.TaskStatusOptions{CloseCleanChildren: pending.closeCleanChildren})
	}
	if msg.Key == "no" && m.pendingClose != nil {
		pending := *m.pendingClose
		m.pendingClose = nil
		target := pending.taskID
		if len(pending.taskIDs) > 1 {
			target = fmt.Sprintf("%d tasks", len(pending.taskIDs))
		} else if len(pending.taskIDs) == 1 {
			target = pending.taskIDs[0]
		}
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Cancelled integrate and close for %s", target),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}
	if msg.Key == "yes" && m.pendingReviewCascade != nil {
		pending := *m.pendingReviewCascade
		m.pendingReviewCascade = nil
		m.beginTaskStatusMoveFeedback(pending.taskID, pending.previousStatus, domain.StatusInReview)
		return m, m.moveTaskStatusCascadeChildrenCmd(pending.taskID, pending.previousStatus, domain.StatusInReview)
	}
	if msg.Key == "no" && m.pendingReviewCascade != nil {
		pending := *m.pendingReviewCascade
		m.pendingReviewCascade = nil
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Cancelled review cascade for %s", pending.taskID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}

	task, session := m.getCurrentTaskAndSession()
	if selectionTaskID != "" {
		if selectedTask, selectedSession, ok := m.taskAndSessionByID(selectionTaskID); ok {
			task, session = selectedTask, selectedSession
		}
	}
	if task == nil {
		return m, nil
	}

	// Handle the selection based on key
	switch msg.Key {
	// Session actions
	case "s":
		// Match CLI `az session start`: daemon defaults to launching AI work.
		m.beginTaskMutationFeedback(task.ID.String(), "session_start", sessionStartActionLabel(false, true))
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), false, true)
	case "t":
		// Explicit tmux-only session; do not launch work automatically.
		m.beginTaskMutationFeedback(task.ID.String(), "session_start", sessionStartActionLabel(false, false))
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), false, false)
	case "S":
		// Start session directly without origin/base selection prompt.
		m.beginTaskMutationFeedback(task.ID.String(), "session_start", sessionStartActionLabel(false, true))
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), false, true)
	case "!":
		// Start session with dangerous skip-permissions mode.
		m.beginTaskMutationFeedback(task.ID.String(), "session_start", sessionStartActionLabel(true, true))
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), true, true)
	case "session_origin":
		if originMsg, ok := msg.Value.(overlay.MergeTargetSelectedMsg); ok {
			m.beginTaskMutationFeedback(task.ID.String(), "session_start", sessionStartActionLabel(false, true))
			return m, m.startSessionCmd(task.ID.String(), m.originBranchForSelection(originMsg.SourceID), false, true)
		}
		return m, nil
	case "a":
		m.beginMutationFeedback(fmt.Sprintf("Attach queued for %s", task.ID))
		return m, m.attachSessionCmd(task.ID.String())
	case "p":
		// TODO: Pause session
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Pause session (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	case "x":
		if operationID, ok := m.activePendingOperationForTask(task.ID.String()); ok {
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: fmt.Sprintf("Cancelling operation %s for %s", operationID, task.ID),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.cancelTaskOperationCmd(task.ID.String(), operationID)
		}
		// Delegate stop decision to daemon authority; projection can be stale.
		m.beginTaskMutationFeedback(task.ID.String(), "session_stop", "Session stop")
		return m, m.stopSessionCmd(task.ID.String())
	case "R":
		// TODO: Resume session
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Resume session (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})

	// Git actions
	case "u":
		// Update from base branch using local worktree hint when available.
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		m.markMergeOperationPreparing(task.ID.String(), "", "preparing update")
		m.beginMutationFeedback(fmt.Sprintf("Update from base queued for %s", task.ID))
		return m, m.updateFromBaseCmd(task.ID.String(), worktreeHint, false)

	case "m":
		// Merge focused issue into its default target.
		m.beginMutationFeedback(fmt.Sprintf("Preparing merge for %s", task.ID))
		return m.mergeCurrentIssueIntoDefaultTarget(task)

	case "P":
		// Resolve branch/worktree via daemon when local projection is stale.
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		m.beginMutationFeedback(fmt.Sprintf("Preparing PR for %s", task.ID))
		return m, m.openPROverlayCmd(worktreeHint, task.ID.String())
	case "O":
		// Open PR in browser for current branch
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		m.beginMutationFeedback(fmt.Sprintf("Opening PR for %s", task.ID))
		return m, m.openPRCmd(worktreeHint, task.ID.String())
	case "M":
		// Abort in-progress merge in worktree
		m.beginMutationFeedback(fmt.Sprintf("Abort merge queued for %s", task.ID))
		return m, m.abortMergeIssueCmd(task.ID.String())
	case "H":
		// Open Helix in the task worktree.
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		m.beginMutationFeedback(fmt.Sprintf("Opening Helix for %s", task.ID))
		return m, m.openHelixCmd(worktreeHint, task.ID.String())

	case "f":
		// Show diff viewer
		diffWorktree := ""
		if session != nil && strings.TrimSpace(session.Worktree) != "" {
			diffWorktree = strings.TrimSpace(session.Worktree)
		}
		if diffWorktree == "" && task.Session != nil && strings.TrimSpace(task.Session.Worktree) != "" {
			diffWorktree = strings.TrimSpace(task.Session.Worktree)
		}
		if diffWorktree == "" {
			if runtimeWorktree := strings.TrimSpace(m.runtimeSignalWorktreeByTask[task.ID.String()]); runtimeWorktree != "" {
				diffWorktree = runtimeWorktree
			}
		}
		if diffWorktree == "" && !task.HasWorktree {
			diffWorktree = strings.TrimSpace(m.repoDir)
		}
		if diffWorktree == "" {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "No task worktree available for diff",
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		return m, m.resolveDiffViewerCmd(diffWorktree, task.ID.String())
	case "w":
		// Cleanup worktree and keep task.
		m.selectTaskByID(task.ID.String())
		m.beginMutationFeedback(fmt.Sprintf("Cleanup preflight queued for %s", task.ID))
		return m, m.requestWorktreeCleanupConfirmationCmd(task.ID.String(), false)
	case "W":
		// Delete task and cleanup worktree.
		m.selectTaskByID(task.ID.String())
		m.beginMutationFeedback(fmt.Sprintf("Delete + cleanup preflight queued for %s", task.ID))
		return m, m.requestWorktreeCleanupConfirmationCmd(task.ID.String(), true)

	case "i":
		// Attachments
		attachOverlay := overlay.NewImageAttachOverlay(task.ID.String(), m.attachmentService)
		return m, m.openOverlay(attachOverlay)

	case "r":
		m.beginMutationFeedback(fmt.Sprintf("Refreshing %s", task.ID))
		return m, m.refreshTaskWorkspaceInBackgroundCmd(task.ID.String())
	case "o":
		m.beginMutationFeedback(fmt.Sprintf("Claiming ownership for %s", task.ID))
		return m, m.issueOwnershipCmd(task.ID.String(), "claim", false)
	case "U":
		m.beginMutationFeedback(fmt.Sprintf("Taking ownership for %s", task.ID))
		return m, m.issueOwnershipCmd(task.ID.String(), "claim", true)
	case "y":
		m.beginMutationFeedback(fmt.Sprintf("Releasing ownership for %s", task.ID))
		return m, m.issueOwnershipCmd(task.ID.String(), "release", false)

	case "V":
		// Dev server menu
		servers := m.getDevServerInfo(task.ID.String())
		devOverlay := overlay.NewDevServerOverlay(
			servers,
			task.ID.String(),
			func(serverID string) tea.Cmd { return m.toggleDevServer(serverID) },
			func(serverID string) tea.Cmd { return m.viewDevServer(serverID) },
			func(serverID string) tea.Cmd { return m.restartDevServer(serverID) },
			func() tea.Cmd { return func() tea.Msg { return overlay.CloseOverlayMsg{} } },
		)
		return m, m.openOverlay(devOverlay)

	case "b":
		return m, m.openMergeTargetSelection(task)

	// Task actions
	case "h":
		// Move task to the previous durable lifecycle action.
		previousStatus := task.Status
		newAction, ok := shiftedTaskLifecycleAction(*task, -1)
		if !ok {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Task is already at the first lifecycle action",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		newStatus := newAction.LegacyStatus()
		if newAction.RequiresClose() {
			pending := pendingCloseCleanupConfirmation{
				taskID:         task.ID.String(),
				previousStatus: previousStatus,
				targetStatus:   newStatus,
				summaries:      []closeCleanupTaskSummary{closeCleanupSummary(*task)},
			}
			pending = m.prepareCloseCleanupConfirmation(pending)
			m.pendingClose = &pending
			return m, m.confirmCloseCleanupCmd(pending)
		}
		if newAction.IsLifecycleOnly() {
			previousTask := *task
			m.applyOptimisticTaskLifecycle(task.ID.String(), newAction.Lifecycle)
			return m, m.moveTaskLifecycleCmd(previousTask, newAction)
		}
		m.beginTaskStatusMoveFeedback(task.ID.String(), previousStatus, newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), previousStatus, newStatus)

	case "l":
		// Move task to the next durable lifecycle action.
		previousStatus := task.Status
		newAction, ok := shiftedTaskLifecycleAction(*task, 1)
		if !ok {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Task is already at the final lifecycle action",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		newStatus := newAction.LegacyStatus()
		if newAction.RequiresClose() {
			pending := pendingCloseCleanupConfirmation{
				taskID:         task.ID.String(),
				previousStatus: previousStatus,
				targetStatus:   newStatus,
				summaries:      []closeCleanupTaskSummary{closeCleanupSummary(*task)},
			}
			pending = m.prepareCloseCleanupConfirmation(pending)
			m.pendingClose = &pending
			return m, m.confirmCloseCleanupCmd(pending)
		}
		if newAction.IsLifecycleOnly() {
			previousTask := *task
			m.applyOptimisticTaskLifecycle(task.ID.String(), newAction.Lifecycle)
			return m, m.moveTaskLifecycleCmd(previousTask, newAction)
		}
		m.beginTaskStatusMoveFeedback(task.ID.String(), previousStatus, newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), previousStatus, newStatus)
	case "0", "1", "2", "3", "4", "5":
		newAction, ok := exactTaskActionForKey(msg.Key)
		if !ok {
			return m, nil
		}
		if taskActionPhase(*task) == newAction.Phase {
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: fmt.Sprintf("Task is already at %s", newAction.DisplayName()),
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		previousStatus := task.Status
		newStatus := newAction.LegacyStatus()
		if newAction.RequiresClose() {
			pending := pendingCloseCleanupConfirmation{
				taskID:         task.ID.String(),
				previousStatus: previousStatus,
				targetStatus:   newStatus,
				summaries:      []closeCleanupTaskSummary{closeCleanupSummary(*task)},
			}
			pending = m.prepareCloseCleanupConfirmation(pending)
			m.pendingClose = &pending
			return m, m.confirmCloseCleanupCmd(pending)
		}
		if newAction.IsLifecycleOnly() {
			previousTask := *task
			m.applyOptimisticTaskLifecycle(task.ID.String(), newAction.Lifecycle)
			return m, m.moveTaskLifecycleCmd(previousTask, newAction)
		}
		m.beginTaskStatusMoveFeedback(task.ID.String(), previousStatus, newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), previousStatus, newStatus)
	case "C":
		if task.Status == domain.StatusInReview {
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: "Review already requested",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		childIDs := m.reviewCascadeChildIDs(task.ID.String())
		if len(childIDs) == 0 {
			previousStatus := task.Status
			m.beginTaskStatusMoveFeedback(task.ID.String(), previousStatus, domain.StatusInReview)
			return m, m.moveTaskStatusCmd(task.ID.String(), previousStatus, domain.StatusInReview)
		}
		pending := pendingReviewCascadeConfirmation{
			taskID:         task.ID.String(),
			previousStatus: task.Status,
			childIDs:       childIDs,
		}
		m.pendingReviewCascade = &pending
		confirm := overlay.NewConfirmDialogExplicitYN("Move children to review?", formatReviewCascadeConfirmPrompt(pending))
		return m, m.openOverlay(confirm)
	case "e":
		return m.openEditTaskOverlay(*task)
	case "T":
		m.beginMutationFeedback(fmt.Sprintf("Archive queued for %s", task.ID))
		return m, m.deleteTaskCmd(task.ID.String())
	case "d":
		m.beginMutationFeedback(fmt.Sprintf("Archive queued for %s", task.ID))
		return m, m.deleteTaskCmd(task.ID.String())
	case "c":
		parentID := task.ID.String()
		return m, m.openOverlay(overlay.NewCreateTaskOverlayWithParentImplOptionsAndAttachmentService(&parentID, m.availableTaskImplementations(), m.attachmentService))
	}

	return m, nil
}

type diffViewerResolvedMsg struct {
	issueID    string
	worktree   string
	baseBranch string
	err        error
}

func (m Model) resolveDiffViewerCmd(worktree, issueID string) tea.Cmd {
	defaultBase := m.resolveBaseBranch()
	client := m.daemonClient
	return func() tea.Msg {
		if client == nil {
			return diffViewerResolvedMsg{
				issueID:  issueID,
				worktree: worktree,
				err:      fmt.Errorf("daemon client unavailable"),
			}
		}
		target, err := client.TaskMergeBaseTarget(context.Background(), issueID, defaultBase, true)
		if err != nil {
			return diffViewerResolvedMsg{
				issueID:  issueID,
				worktree: worktree,
				err:      err,
			}
		}
		baseBranch := strings.TrimSpace(target.Branch)
		if baseBranch == "" {
			baseBranch = defaultBase
		}
		return diffViewerResolvedMsg{
			issueID:    issueID,
			worktree:   worktree,
			baseBranch: baseBranch,
		}
	}
}

func (m Model) openDiffViewerFromResolved(msg diffViewerResolvedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.addToast(Toast{
			Level:   ToastError,
			Message: fmt.Sprintf("Resolve diff base failed: %v", msg.err),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}
	diffWorktree := strings.TrimSpace(msg.worktree)
	if diffWorktree == "" {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No task worktree available for diff",
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil
	}
	openPopup := func(ctx context.Context, title, command string) error {
		if strings.TrimSpace(os.Getenv("TMUX")) == "" || m.tmuxClient == nil {
			return fmt.Errorf("diff popup unavailable outside tmux; run inside tmux and retry")
		}
		popupCommand := fmt.Sprintf("cd %s && %s", shellSingleQuote(diffWorktree), command)
		return m.tmuxClient.DisplayPopup(ctx, title, "95%", "95%", popupCommand)
	}
	viewer := diff.NewDiffViewer(diffWorktree, msg.baseBranch, m.gitClient, openPopup).WithIssueID(msg.issueID)
	return m, m.openOverlay(viewer)
}
