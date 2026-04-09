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
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, -1)

	case "l": // Move right (next status)
		return m, m.bulkMoveStatusCmd(msg.SelectedIDs, 1)

	case "o": // Set to Open
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusOpen)

	case "i": // Set to In Progress
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusInProgress)

	case "b": // Set to Blocked
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusBlocked)

	case "D": // Set to Done
		return m, m.bulkSetStatusCmd(msg.SelectedIDs, domain.StatusDone)

	case "d": // Delete selected
		return m, m.bulkDeleteCmd(msg.SelectedIDs)

	case "a":
		return m, m.bulkArchiveCmd(msg.SelectedIDs)

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
	case "abort", "claude", "manual":
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
		return m, m.followOnMergeSelectionCmd(task, session)
	case "merge_preflight_abort":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.abortMergeCmd(worktree)
	case "merge_preflight_discard_source":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.discardChangesCmd("source", worktree)
	case "merge_preflight_discard_target":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.discardChangesCmd("target", worktree)
	case "merge_preflight_commit_source":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.commitChangesCmd("source", worktree)
	case "merge_preflight_commit_target":
		m.overlayStack.Pop()
		worktree, ok := msg.Value.(string)
		if !ok || strings.TrimSpace(worktree) == "" {
			return m, nil
		}
		return m, m.commitChangesCmd("target", worktree)
	case "merge_preflight_refresh":
		m.overlayStack.Pop()
		selection, ok := msg.Value.(overlay.MergePreflightRefreshSelection)
		if !ok {
			return m, m.loadIssuesAfterRuntimeReconcileCmd()
		}
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

	// Keep task workspace open when attaching so users can return to the
	// full detail/actions panel after attach without reopening it.
	if !(msg.Key == "a" && isTaskWorkspaceOverlay(m.overlayStack.Current())) {
		m.overlayStack.Pop()
	}

	if msg.Key == "yes" && m.pendingCleanup != nil {
		pending := m.pendingCleanup
		m.pendingCleanup = nil
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
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), false, false)
	case "S":
		// Start session directly without origin/base selection prompt.
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), false, true)
	case "!":
		// Start session with dangerous skip-permissions mode.
		return m, m.startSessionCmd(task.ID.String(), m.resolveBaseBranch(), true, true)
	case "session_origin":
		if originMsg, ok := msg.Value.(overlay.MergeTargetSelectedMsg); ok {
			return m, m.startSessionCmd(task.ID.String(), m.originBranchForSelection(originMsg.SourceID), false, true)
		}
		return m, nil
	case "a":
		// Delegate attach readiness to daemon authority; projection can be stale.
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		return m, m.checkBranchBehindCmd(worktreeHint, task.ID.String())
	case "p":
		// TODO: Pause session
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Pause session (TODO)",
			Expires: time.Now().Add(3 * time.Second),
		})
	case "x":
		// Delegate stop decision to daemon authority; projection can be stale.
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
		return m, m.updateFromBaseCmd(task.ID.String(), worktreeHint, false)

	case "m":
		// Follow-on merge from dependency-aware context.
		return m, m.followOnMergeSelectionCmd(task, session)

	case "P":
		// Resolve branch/worktree via daemon when local projection is stale.
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		return m, m.openPROverlayCmd(worktreeHint, task.ID.String())
	case "O":
		// Open PR in browser for current branch
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
		return m, m.openPRCmd(worktreeHint, task.ID.String())
	case "M":
		// Abort in-progress merge in worktree
		return m, m.abortMergeIssueCmd(task.ID.String())
	case "H":
		// Open Helix in the task worktree.
		worktreeHint := ""
		if session != nil {
			worktreeHint = session.Worktree
		}
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
		viewer := diff.NewDiffViewer(diffWorktree, m.config.Git.BaseBranch, m.gitClient, openPopup)
		cmd := m.openOverlay(viewer)
		return m, cmd
	case "w":
		// Cleanup worktree and keep task.
		return m, m.requestWorktreeCleanupConfirmationCmd(task.ID.String(), false)
	case "W":
		// Delete task and cleanup worktree.
		return m, m.requestWorktreeCleanupConfirmationCmd(task.ID.String(), true)

	case "i":
		// Image attachments
		attachOverlay := overlay.NewImageAttachOverlay(task.ID.String(), m.attachmentService)
		return m, m.openOverlay(attachOverlay)

	case "r":
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
		m.applyOptimisticTaskStatus(task.ID.String(), newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), task.Status, newStatus)

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
		m.applyOptimisticTaskStatus(task.ID.String(), newStatus)
		return m, m.moveTaskStatusCmd(task.ID.String(), task.Status, newStatus)
	case "e":
		return m, m.openOverlay(overlay.NewEditTaskOverlayWithImplOptionsAndAttachmentService(*task, m.availableTaskImplementations(), m.attachmentService))
	case "T":
		return m, m.deleteTaskCmd(task.ID.String())
	case "d":
		return m, m.deleteTaskCmd(task.ID.String())
	case "c":
		parentID := task.ID.String()
		return m, m.openOverlay(overlay.NewCreateTaskOverlayWithParentImplOptionsAndAttachmentService(&parentID, m.availableTaskImplementations(), m.attachmentService))
	}

	return m, nil
}
