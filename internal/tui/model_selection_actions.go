package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func (m Model) handleBulkAction(msg overlay.BulkActionMsg) (tea.Model, tea.Cmd) {
	count := len(msg.SelectedIDs)
	if count == 0 {
		return m, nil
	}

	switch msg.Action {
	case "h": // Move left (previous status)
		m.beginMutationFeedback(fmt.Sprintf("Bulk move queued for %d task(s)", count))
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, -1)

	case "l": // Move right (next status)
		m.beginMutationFeedback(fmt.Sprintf("Bulk move queued for %d task(s)", count))
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, 1)

	case "o": // Set to Open
		m.beginMutationFeedback(fmt.Sprintf("Bulk status update queued for %d task(s)", count))
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusOpen)

	case "i": // Set to In Progress
		m.beginMutationFeedback(fmt.Sprintf("Bulk status update queued for %d task(s)", count))
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusInProgress)

	case "b": // Set to Blocked
		m.beginMutationFeedback(fmt.Sprintf("Bulk status update queued for %d task(s)", count))
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusBlocked)

	case "D": // Set to Done
		m.beginMutationFeedback(fmt.Sprintf("Bulk status update queued for %d task(s)", count))
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusDone)

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
		task, session := m.getCurrentTaskAndSession()
		if task != nil {
			m.beginMutationFeedback(fmt.Sprintf("Preparing merge for %s", task.ID))
		}
		return m, m.followOnMergeSelectionCmd(task, session)
	case "merge_preflight_abort":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		m.beginMutationFeedback("Abort merge queued")
		return m, m.abortMergeCmd(worktree)
	case "merge_preflight_discard_source":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		m.beginMutationFeedback("Discard source changes queued")
		return m, m.discardChangesCmd("source", worktree)
	case "merge_preflight_discard_target":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		m.beginMutationFeedback("Discard target changes queued")
		return m, m.discardChangesCmd("target", worktree)
	case "merge_preflight_commit_source":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		m.beginMutationFeedback("Commit source changes queued")
		return m, m.commitChangesCmd("source", worktree)
	case "merge_preflight_commit_target":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		m.beginMutationFeedback("Commit target changes queued")
		return m, m.commitChangesCmd("target", worktree)
	case "merge_preflight_agent":
		m.overlayStack.Pop()
		selection, ok := msg.Value.(overlay.MergePreflightAgentSelection)
		if !ok {
			return m, nil
		}
		m.beginMutationFeedback(fmt.Sprintf("Agent merge queued for %s -> %s", selection.SourceID, selection.TargetID))
		return m, m.resolveMergePreflightWithAgentCmd(selection)
	case "merge_preflight_refresh":
		m.overlayStack.Pop()
		selection, ok := msg.Value.(overlay.MergePreflightRefreshSelection)
		if !ok {
			m.beginMutationFeedback("Refreshing merge preflight")
			return m, m.loadIssuesAfterRuntimeReconcileCmd()
		}
		m.beginMutationFeedback("Refreshing merge preflight")
		return m, m.refreshMergePreflightCmd(selection)
	case "projects":
		// Settings -> Manage projects
		m.overlayStack.Pop() // Close settings
		return m, m.openOverlay(overlay.NewProjectSelectorWithOptions(
			m.projectRegistry,
			overlay.WithInitialCursor(m.projectSelectorCursor()),
			overlay.WithCurrentProjectName(m.currentProject),
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
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Related task %s is not loaded", strings.TrimSpace(targetID)),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		columns := m.buildColumns()
		m.nav.JumpToTaskByID(columns, task.ID.String())
		m.ensureCursorVisible(columns)
		if workspace, ok := m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); ok {
			workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
			workspace.SyncTask(*task, m.tasks, m.pendingMutationForTask(task.ID.String()))
		}
		if m.daemonClient == nil {
			return m, nil
		}
		return m, m.refreshTaskWorkspaceInBackgroundCmd(task.ID.String())
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
		(msg.Key == "a" || msg.Key == "c" || msg.Key == "r" || msg.Key == "x")
	if !keepWorkspaceOpen {
		m.overlayStack.Pop()
	}

	if msg.Key == "yes" && m.pendingBulkCleanup != nil {
		pending := m.pendingBulkCleanup
		m.pendingBulkCleanup = nil
		m.beginMutationFeedback(fmt.Sprintf("Bulk cleanup queued for %d task(s)", len(pending.taskIDs)))
		return m, m.bulkCleanupWorktreeCmd(pending.taskIDs, pending.deletedTasks)
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
		return m, m.cleanupWorktreeCmd(pending.taskID, pending.deletedTask, pending.force)
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
		// Start tmux session only; do not launch work automatically.
		m.beginTaskMutationFeedback(task.ID.String(), "session_start", "Session start")
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), false, false)
	case "S":
		// Start session directly without origin/base selection prompt.
		m.beginTaskMutationFeedback(task.ID.String(), "session_start", "Session start")
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), false, true)
	case "!":
		// Start session with dangerous skip-permissions mode.
		m.beginTaskMutationFeedback(task.ID.String(), "session_start", "Session start")
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), true, true)
	case "session_origin":
		if originMsg, ok := msg.Value.(overlay.MergeTargetSelectedMsg); ok {
			m.beginTaskMutationFeedback(task.ID.String(), "session_start", "Session start")
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
		// Follow-on merge from dependency-aware context.
		m.beginMutationFeedback(fmt.Sprintf("Preparing merge for %s", task.ID))
		return m, m.followOnMergeSelectionCmd(task, session)

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
		// Open diff viewer overlay
		openPopup := func(ctx context.Context, title, command string) error {
			if strings.TrimSpace(os.Getenv("TMUX")) == "" || m.tmuxClient == nil {
				return fmt.Errorf("diff popup unavailable outside tmux; run inside tmux and retry")
			}
			popupCommand := fmt.Sprintf("cd %s && %s", shellSingleQuote(diffWorktree), command)
			return m.tmuxClient.DisplayPopup(ctx, title, "95%", "95%", popupCommand)
		}
		viewer := diff.NewDiffViewer(diffWorktree, m.config.Git.BaseBranch, m.gitClient, openPopup).WithIssueID(task.ID.String())
		cmd := m.openOverlay(viewer)
		return m, cmd
	case "w":
		// Cleanup worktree and keep task.
		m.beginMutationFeedback(fmt.Sprintf("Cleanup preflight queued for %s", task.ID))
		return m, m.requestWorktreeCleanupConfirmationCmd(task.ID.String(), false)
	case "W":
		// Delete task and cleanup worktree.
		m.beginMutationFeedback(fmt.Sprintf("Delete + cleanup preflight queued for %s", task.ID))
		return m, m.requestWorktreeCleanupConfirmationCmd(task.ID.String(), true)

	case "i":
		// Image attachments
		attachOverlay := overlay.NewImageAttachOverlay(task.ID.String(), m.attachmentService)
		return m, m.openOverlay(attachOverlay)

	case "r":
		m.beginMutationFeedback(fmt.Sprintf("Refreshing %s", task.ID))
		return m, m.refreshTaskWorkspaceInBackgroundCmd(task.ID.String())

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
		// Move task left (to previous status)
		if task.Status == domain.StatusOpen {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Task is already in Open status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		newStatus, ok := shiftedTaskStatus(task.Status, -1)
		if !ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: "Failed to compute previous status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		previousStatus := task.Status
		m.applyOptimisticTaskStatus(task.ID.String(), newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), previousStatus, newStatus)

	case "l":
		// Move task right (to next status)
		if task.Status == domain.StatusDone {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: "Task is already in Done status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		newStatus, ok := shiftedTaskStatus(task.Status, 1)
		if !ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: "Failed to compute next status",
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		previousStatus := task.Status
		m.applyOptimisticTaskStatus(task.ID.String(), newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), previousStatus, newStatus)
	case "1", "2", "3", "4":
		newStatus, ok := exactTaskStatusForKey(msg.Key)
		if !ok {
			return m, nil
		}
		if task.Status == newStatus {
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: fmt.Sprintf("Task is already in %s status", statusDisplayName(newStatus)),
				Expires: time.Now().Add(2 * time.Second),
			})
			return m, nil
		}
		previousStatus := task.Status
		m.applyOptimisticTaskStatus(task.ID.String(), newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), previousStatus, newStatus)
	case "e":
		return m, m.openOverlay(overlay.NewEditTaskOverlayWithImplOptionsAndAttachmentService(*task, m.availableTaskImplementations(), m.attachmentService))
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
