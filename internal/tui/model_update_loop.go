package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/linearsync"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
	"github.com/riordanpawley/azedarach/internal/services/network"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.attachDaemonCmd(),
		m.attachLogStreamCmd(),
		m.gitSyncService.FetchAndCheck(),
	)
}

// Update handles incoming messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureCursorVisible(m.buildColumns())
		if !m.overlayStack.IsEmpty() {
			return m, m.overlayStack.Update(msg)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		// If overlay is open, route to overlay stack
		if !m.overlayStack.IsEmpty() {
			return m.handleOverlayKey(msg)
		}
		return m.handleKey(msg)

	// Overlay messages
	case overlay.CloseOverlayMsg:
		isSearchOverlay := false
		if current := m.overlayStack.Current(); current != nil {
			_, isSearchOverlay = current.(*overlay.SearchOverlay)
		}
		m.overlayStack.Pop()
		if isSearchOverlay {
			m.editor.EnterNormal()
		}
		return m, nil

	case overlay.SelectionMsg:
		if msg.Key == "git_pull" {
			m.overlayStack.Pop()
			return m, m.gitSyncService.Pull()
		}
		if msg.Key == "merge_attach" {
			m.overlayStack.Pop()
			issueID := msg.Value.(string)
			session := m.sessionForIssue(issueID)
			if session == nil {
				return m, nil
			}
			return m, m.fetchAndMergeCmd(session.Worktree, m.config.Git.BaseBranch, issueID, true)
		}
		if msg.Key == "skip_attach" {
			m.overlayStack.Pop()
			issueID := msg.Value.(string)
			return m, m.attachSessionCmd(issueID)
		}
		return m.handleSelection(msg)

	case overlay.BulkActionMsg:
		m.overlayStack.Pop()
		return m.handleBulkAction(msg)

	case overlay.SearchMsg:
		m.editor.SetSearchQuery(msg.Query)
		if current := m.overlayStack.Current(); current != nil {
			if searchOverlay, ok := current.(*overlay.SearchOverlay); ok {
				searchOverlay.SetMatchCount(len(m.editor.ApplyFilter(m.tasks)))
			}
		}
		return m, nil

	case issuesLoadedMsg:
		if msg.refreshSeq != 0 && msg.refreshSeq < m.issueRefreshSeq {
			return m, nil
		}
		if msg.projectID != "" && msg.projectID != m.daemonProjectID() {
			return m, nil
		}
		if msg.daemonClient != nil {
			m.daemonClient = msg.daemonClient
			if strings.TrimSpace(msg.daemonSocket) != "" {
				m.daemonSocketPath = msg.daemonSocket
			}
		}
		wasLoading := m.loading
		if msg.stale {
			m.loading = false
			m.boardRefreshing = false
			if wasLoading && msg.freshnessHint != "" {
				m.addToast(Toast{
					Level:   ToastWarning,
					Message: msg.freshnessHint,
					Expires: time.Now().Add(8 * time.Second),
				})
			}
			if msg.reconcileWarn != nil {
				m.addToast(Toast{
					Level:   ToastWarning,
					Message: fmt.Sprintf("Runtime reconcile warning: %v", msg.reconcileWarn),
					Expires: time.Now().Add(6 * time.Second),
				})
			}
			var cmds []tea.Cmd
			if !m.hasRefreshLoop {
				m.hasRefreshLoop = true
				cmds = append(cmds, tickEvery(2*time.Second))
			}
			if len(cmds) == 0 {
				return m, nil
			}
			return m, tea.Batch(cmds...)
		}
		tasks := m.filterSuppressedHydratedTasks(msg.tasks)
		m.tasks = linearsync.ReconcileHydratedTasks(m.tasks, tasks)
		for i := range m.tasks {
			m.tasks[i].Session = cloneSession(m.tasks[i].Session)
		}
		m.syncProjectionIndexesFromTasks()
		m.applyPendingStatusOverlays()
		m.reconcilePendingStatuses()
		m.reconcilePendingOperations()
		m.editor.ReconcileSelection(m.tasks)
		m.applyPendingCreatedTaskSelection()
		m.taskSnapshotCheckedAt = msg.lastCheckedAt
		m.taskSnapshotFreshness = msg.freshness
		m.reconcileCursorAfterIssuesRefresh()
		m.applyPendingCreatedWorkspaceTask()
		m.syncTaskWorkspaceOverlay()
		if msg.reconcileWarn != nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Runtime reconcile warning: %v", msg.reconcileWarn),
				Expires: time.Now().Add(6 * time.Second),
			})
		}
		if msg.revision > m.daemonRevision {
			m.daemonRevision = msg.revision
		}
		m.lastDaemonReattachAttempt = time.Time{}
		m.loading = false
		m.boardRefreshing = false
		m.lastRefresh = time.Now()
		// Show success toast on first load
		if wasLoading {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: "Issues loaded",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		var cmds []tea.Cmd
		if !m.hasRefreshLoop {
			m.hasRefreshLoop = true
			cmds = append(cmds, tickEvery(2*time.Second))
		}
		if msg.events != nil {
			m.daemonEvents = msg.events
			cmds = append(cmds, m.waitForDaemonEventCmd())
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case logStreamAttachedMsg:
		if msg.err != nil {
			if m.logger != nil {
				m.logger.Debug("log stream attach failed", "error", msg.err)
			}
			interval := reconnect.DefaultReconciliationPolicy().ReattachRetryInterval
			if interval <= 0 {
				interval = 5 * time.Second
			}
			now := time.Now()
			delay := time.Duration(0)
			if !m.lastLogStreamReattachAt.IsZero() {
				elapsed := now.Sub(m.lastLogStreamReattachAt)
				if elapsed < interval {
					delay = interval - elapsed
				}
			}
			if delay == 0 {
				m.lastLogStreamReattachAt = now
				m.logStreamReconnectQueued = false
				return m, m.attachLogStreamCmd()
			}
			if m.logStreamReconnectQueued {
				return m, nil
			}
			m.logStreamReconnectQueued = true
			return m, m.queueLogStreamReconnectCmd(delay)
		}
		m.logStreamEvents = msg.stream
		m.lastLogStreamReattachAt = time.Time{}
		m.logStreamReconnectQueued = false
		return m, m.waitForLogStreamEventCmd()

	case issuesErrorMsg:
		if msg.refreshSeq != 0 && msg.refreshSeq < m.issueRefreshSeq {
			return m, nil
		}
		if msg.projectID != "" && msg.projectID != m.daemonProjectID() {
			return m, nil
		}
		now := time.Now()
		m.addToast(Toast{
			Level:   ToastError,
			Message: msg.err.Error(),
			Expires: now.Add(8 * time.Second),
		})
		m.loading = false
		m.boardRefreshing = false
		cmds := []tea.Cmd{tickEvery(5 * time.Second)}
		if reconnect.DefaultReconciliationPolicy().ShouldQueueReattach(m.lastDaemonReattachAttempt, now, msg.err) {
			m.lastDaemonReattachAttempt = now
			cmds = append(cmds, m.attachDaemonCmd())
		}
		return m, tea.Batch(cmds...)

	case tickMsg:
		// Expire old toasts and refresh issues
		m.expireToasts()
		m.boardRefreshing = true
		m.issueRefreshSeq++
		return m, tea.Batch(
			m.loadIssuesCmd(),
			m.gitSyncService.FetchAndCheck(),
		)

	case monitor.SessionStateMsg:
		if session := m.sessionForIssue(msg.IssueID); session != nil {
			oldState := session.State
			m.logger.Debug("session state updated", "issueID", msg.IssueID, "state", msg.State)

			if oldState != msg.State && msg.State == domain.SessionWaiting {
				fmt.Print("\a")
				m.addToast(Toast{
					Level:   ToastWarning,
					Message: fmt.Sprintf("Session %s is waiting for input", msg.IssueID),
					Expires: time.Now().Add(10 * time.Second),
				})
			}
		}
		return m, nil

	case sessionStartedMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			m.markTaskOperationPending(msg.issueID, "session_start", msg.operationID, msg.state)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage("Session start", msg.issueID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		m.clearPendingTaskStatus(msg.issueID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Session started: %s", msg.issueID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case sessionStoppedMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			m.markTaskOperationPending(msg.issueID, "session_stop", msg.operationID, msg.state)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage("Session stop", msg.issueID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		m.clearPendingTaskStatus(msg.issueID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Session stopped: %s", msg.issueID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case sessionErrorMsg:
		m.clearPendingTaskStatus(msg.issueID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastError,
			Message: fmt.Sprintf("Session error: %s - %v", msg.issueID, msg.err),
			Expires: time.Now().Add(5 * time.Second),
		})
		return m, nil

	case conflictResolveFallbackMsg:
		m.addToast(Toast{
			Level: ToastWarning,
			Message: fmt.Sprintf(
				"Could not attach via daemon (%v). Run: tmux attach-session -t %s (agent can help resolve)",
				msg.err,
				msg.issueID,
			),
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case conflictResolveAgentResultMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			m.markTaskOperationPending(msg.issueID, protocol.CommandSessionResolveConflict, msg.operationID, msg.state)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage("Agent conflict resolution", msg.issueID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Agent conflict resolution failed: %v", msg.err),
				Expires: time.Now().Add(6 * time.Second),
			})
			return m, nil
		}
		target := strings.TrimSpace(msg.windowName)
		if target == "" {
			target = "resolve-conflict"
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Agent conflict resolution launched for %s in %s", msg.issueID, target),
			Expires: time.Now().Add(4 * time.Second),
		})
		return m, m.loadIssuesCmd()

	case daemonStreamEventMsg:
		if msg.stream != nil && msg.stream != m.daemonEvents {
			return m, nil
		}
		if projectID := strings.TrimSpace(msg.event.ProjectID.String()); projectID != "" && projectID != m.daemonProjectID() {
			return m, m.waitForDaemonEventCmd()
		}
		cursor := protocol.StreamCursor{Revision: m.daemonRevision}
		m.recordRuntimeEvent(msg.event)
		if current := m.overlayStack.Current(); current != nil {
			if logOverlay, ok := current.(*overlay.EventLogOverlay); ok {
				logOverlay.AddEvent(msg.event)
			}
		}
		if cmd, handled := m.handlePendingWorktreeCleanupOperationEvent(msg.event); handled {
			m.daemonRevision = cursor.Advance(msg.event).Revision
			if cmd != nil {
				return m, tea.Batch(cmd, m.waitForDaemonEventCmd())
			}
			return m, m.waitForDaemonEventCmd()
		}
		m.applyOperationProgressEvent(msg.event)
		if isTaskMutationEvent(msg.event.Event) && len(msg.event.Body) > 0 {
			switch cursor.Decide(msg.event) {
			case protocol.StreamProjectionDecisionIgnore:
				return m, m.waitForDaemonEventCmd()
			case protocol.StreamProjectionDecisionResync:
				m.daemonEvents = nil
				return m, m.attachDaemonCmd()
			}
			if m.applyTaskEvent(msg.event) {
				m.daemonRevision = cursor.Advance(msg.event).Revision
				return m, m.waitForDaemonEventCmd()
			}
		}
		if msg.event.Event == protocol.EventSessionUpdated {
			switch cursor.Decide(msg.event) {
			case protocol.StreamProjectionDecisionIgnore:
				return m, m.waitForDaemonEventCmd()
			case protocol.StreamProjectionDecisionResync:
				m.daemonEvents = nil
				return m, m.attachDaemonCmd()
			}
			m.applySessionProjectionEvent(msg.event)
			m.daemonRevision = cursor.Advance(msg.event).Revision
			return m, m.waitForDaemonEventCmd()
		}
		if msg.event.Event == protocol.EventWorktreeProjectionUpdated || msg.event.Event == protocol.EventGitStatusUpdated {
			switch cursor.Decide(msg.event) {
			case protocol.StreamProjectionDecisionIgnore:
				return m, m.waitForDaemonEventCmd()
			case protocol.StreamProjectionDecisionResync:
				m.daemonEvents = nil
				return m, m.attachDaemonCmd()
			}

			var body protocol.ProjectionUpdateEventBody
			if err := json.Unmarshal(msg.event.Body, &body); err == nil && body.Runtime != nil {
				m.applyRuntimeProjection(body.Runtime.Projection)
			}
			diffRefreshCmd := m.refreshOpenDiffOverlayFromProjectionBody(body)
			m.daemonRevision = cursor.Advance(msg.event).Revision
			if diffRefreshCmd != nil {
				return m, tea.Batch(diffRefreshCmd, m.waitForDaemonEventCmd())
			}
			return m, m.waitForDaemonEventCmd()
		}
		switch m.reduceDaemonEvent(msg.event) {
		case daemonEventIgnore:
			return m, m.waitForDaemonEventCmd()
		case daemonEventRefreshSnapshot:
			cursor := protocol.StreamCursor{Revision: m.daemonRevision}
			m.daemonRevision = cursor.Advance(msg.event).Revision
			return m, tea.Batch(m.loadIssuesCmd(), m.waitForDaemonEventCmd())
		case daemonEventRehydrate:
			m.daemonEvents = nil
			return m, m.attachDaemonCmd()
		default:
			return m, m.waitForDaemonEventCmd()
		}

	case daemonStreamClosedMsg:
		if msg.stream != nil && msg.stream != m.daemonEvents {
			return m, nil
		}
		m.daemonEvents = nil
		return m, m.attachDaemonCmd()

	case logStreamEventMsg:
		if msg.stream != nil && msg.stream != m.logStreamEvents {
			return m, nil
		}
		// Primary daemon stream already carries current-project events used for
		// projection/state updates. Keep this stream for cross-project logging.
		if strings.TrimSpace(msg.event.ProjectID.String()) == m.daemonProjectID() {
			return m, m.waitForLogStreamEventCmd()
		}
		m.recordRuntimeEvent(msg.event)
		if current := m.overlayStack.Current(); current != nil {
			if logOverlay, ok := current.(*overlay.EventLogOverlay); ok {
				logOverlay.AddEvent(msg.event)
			}
		}
		return m, m.waitForLogStreamEventCmd()

	case logStreamClosedMsg:
		if msg.stream != nil && msg.stream != m.logStreamEvents {
			return m, nil
		}
		m.logStreamEvents = nil
		interval := reconnect.DefaultReconciliationPolicy().ReattachRetryInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		now := time.Now()
		delay := time.Duration(0)
		if !m.lastLogStreamReattachAt.IsZero() {
			elapsed := now.Sub(m.lastLogStreamReattachAt)
			if elapsed < interval {
				delay = interval - elapsed
			}
		}
		if delay == 0 {
			m.lastLogStreamReattachAt = now
			m.logStreamReconnectQueued = false
			return m, m.attachLogStreamCmd()
		}
		if m.logStreamReconnectQueued {
			return m, nil
		}
		m.logStreamReconnectQueued = true
		return m, m.queueLogStreamReconnectCmd(delay)

	case logStreamReconnectMsg:
		m.logStreamReconnectQueued = false
		m.lastLogStreamReattachAt = time.Now()
		return m, m.attachLogStreamCmd()

	case hookLogLoadedMsg:
		if msg.err != nil {
			return m, nil
		}
		current := m.overlayStack.Current()
		logOverlay, ok := current.(*overlay.EventLogOverlay)
		if !ok {
			return m, nil
		}
		for _, hookEvt := range msg.events {
			body, err := json.Marshal(hookEvt)
			if err != nil {
				continue
			}
			evt := protocol.EventEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				ProjectID:       naming.ProjectID(m.daemonProjectID()),
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(m.daemonProjectID())},
				Event:           protocol.EventHookLogAppended,
				Kind:            protocol.EnvelopeKindEvent,
				EmittedAt:       hookEvt.CreatedAt.UTC(),
				Body:            body,
			}
			logOverlay.AddEvent(evt)
		}
		return m, nil

	case network.StatusMsg:
		// Update online status
		m.isOnline = msg.Online
		m.logger.Debug("network status updated", "online", msg.Online)
		return m, nil

	case daemonclient.GitSyncMsg:
		if msg.Err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Git sync failed: %v", msg.Err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		if m.gitSyncService.ShouldNotify(msg.CommitsBehind) {
			return m, m.openOverlay(overlay.NewGitPullOverlay(msg.CommitsBehind))
		}
		return m, nil

	case mergeResultMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			action := "Merge"
			if msg.stage == "stop_session" {
				action = "Stop session"
			}
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage(action, msg.sourceID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Merge failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		if msg.result.HasConflicts {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Merge conflicts: %s", strings.Join(msg.result.ConflictFiles, ", ")),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.openOverlay(overlay.NewConflictDialog(msg.result.ConflictFiles))
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Successfully merged %s into %s", msg.sourceID, msg.targetID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, m.loadIssuesCmd()

	case mergePreflightFailureMsg:
		return m, m.overlayStack.Push(overlay.NewMergePreflightOverlay(
			msg.sourceID,
			msg.targetID,
			msg.sourceWorktree,
			msg.targetWorktree,
			msg.reasons,
			msg.sourceFiles,
			msg.targetFiles,
			strings.TrimSpace(msg.targetWorktree) != "",
		))

	case mergePreflightActionResultMsg:
		sideTitle := strings.Title(msg.side)
		switch msg.action {
		case "discard":
			if msg.err != nil {
				m.addToast(Toast{
					Level:   ToastError,
					Message: fmt.Sprintf("Discard %s changes failed: %v", sideTitle, msg.err),
					Expires: time.Now().Add(5 * time.Second),
				})
				return m, nil
			}
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Discarded %s changes", strings.ToLower(sideTitle)),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.loadIssuesCmd()
		case "commit":
			if msg.err != nil {
				m.addToast(Toast{
					Level:   ToastError,
					Message: fmt.Sprintf("Commit %s changes failed: %v", strings.ToLower(sideTitle), msg.err),
					Expires: time.Now().Add(6 * time.Second),
				})
				return m, nil
			}
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Committed %s changes", strings.ToLower(sideTitle)),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.loadIssuesCmd()
		default:
			return m, nil
		}

	case mergePreflightRefreshResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Merge preflight refresh failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		if msg.cleared {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: "Merge preconditions now clean",
				Expires: time.Now().Add(3 * time.Second),
			})
		}
		return m, m.loadIssuesCmd()

	case mergeTargetSelectionResolvedMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: msg.err.Error(),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		if msg.targetID == mergeBaseTargetID {
			return m, m.mergeToBaseCmd(msg.sourceWorktree, msg.sourceID, msg.refreshStatus)
		}
		return m, m.followOnMergeIntoTargetCmd(msg.sourceWorktree, msg.targetWorktree, msg.sourceID, msg.targetID, msg.targetState, msg.refreshStatus)

	case refreshTaskWorkspaceResultMsg:
		if msg.reconcileErr != nil && m.logger != nil {
			m.logger.Warn("task workspace issue reconcile failed", "task_id", msg.taskID, "error", msg.reconcileErr)
		}
		if msg.snapshotErr != nil || !msg.hasTask {
			return m, nil
		}
		currentWorkspace, ok := m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
		if !ok || taskIDKey(currentWorkspace.TaskID()) != taskIDKey(msg.taskID) {
			return m, nil
		}
		task, ok := m.applySingleTaskWorkspaceRefresh(msg.taskID, msg.task)
		if !ok {
			return m, nil
		}

		m.overlayStack.Pop()
		workspace := overlay.NewTaskWorkspaceOverlay(task, m.tasks, m.pendingMutationForTask(msg.taskID), m.width, m.height)
		if !msg.lastCheckedAt.IsZero() && msg.freshness.Valid() {
			workspace.SyncSnapshotFreshness(msg.lastCheckedAt, msg.freshness)
		} else {
			workspace.SyncSnapshotFreshness(m.taskSnapshotCheckedAt, m.taskSnapshotFreshness)
		}
		return m, m.openOverlay(workspace)

	case fetchAndMergeResultMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			action := "Merge"
			if msg.stage == "fetch" {
				action = "Fetch"
			}
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage(action, msg.issueID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Merge failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		if msg.result.HasConflicts {
			// Show conflict dialog
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Merge conflicts in %d files", len(msg.result.ConflictFiles)),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.openOverlay(overlay.NewConflictDialogForIssue(msg.result.ConflictFiles, msg.issueID, msg.worktree))
		}

		// Successful merge
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: "Updated from main successfully",
			Expires: time.Now().Add(3 * time.Second),
		})
		if msg.attachAfter {
			return m, m.attachSessionCmd(msg.issueID)
		}
		return m, nil

	case createPRResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to get branch info: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		// Show PR command in toast
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Run: %s", msg.cmd),
			Expires: time.Now().Add(10 * time.Second),
		})
		return m, nil

	case abortMergeResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to abort merge: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: "Merge aborted successfully",
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	// Phase 6: Advanced features
	case overlay.JumpSelectedMsg:
		// Close overlay
		m.overlayStack.Pop()

		// Jump to selected task by flat index
		columns := m.buildColumns()
		m.nav.JumpToTaskByIndex(columns, msg.TaskIndex)
		m.ensureCursorVisible(columns)
		return m, nil

	case overlay.ProjectSelectedMsg:
		// Close overlay
		m.overlayStack.Pop()
		m.loading = false
		m.boardRefreshing = true
		m.projectSwitchInFlight = true
		m.issueRefreshSeq++
		m.projectSwitchSeq++

		// Switch project runtime context and reload issues.
		return m, m.switchProjectCmd(msg.Project)

	case projectSwitchResultMsg:
		if msg.switchSeq != 0 && msg.switchSeq != m.projectSwitchSeq {
			return m, nil
		}
		if msg.err != nil {
			m.loading = false
			m.boardRefreshing = false
			m.projectSwitchInFlight = false
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Project switch failed: %v", msg.err),
				Expires: time.Now().Add(6 * time.Second),
			})
			return m, nil
		}
		if msg.daemonClient != nil {
			m.daemonClient = msg.daemonClient
			if strings.TrimSpace(msg.daemonSocket) != "" {
				m.daemonSocketPath = msg.daemonSocket
			}
		}

		m.rebindProjectContext(msg.project, msg.projectConfig)

		// Reuse the normal loaded-state reducer path.
		tasks := m.filterSuppressedHydratedTasks(msg.tasks)
		m.tasks = linearsync.ReconcileHydratedTasks(m.tasks, tasks)
		for i := range m.tasks {
			m.tasks[i].Session = cloneSession(m.tasks[i].Session)
		}
		m.syncProjectionIndexesFromTasks()
		m.editor.ReconcileSelection(tasks)
		m.applyPendingCreatedTaskSelection()
		m.taskSnapshotCheckedAt = msg.lastCheckedAt
		m.taskSnapshotFreshness = msg.freshness
		m.reconcileCursorAfterIssuesRefresh()
		m.syncTaskWorkspaceOverlay()
		if msg.revision > m.daemonRevision {
			m.daemonRevision = msg.revision
		}
		m.loading = false
		m.boardRefreshing = false
		m.projectSwitchInFlight = false
		m.lastRefresh = time.Now()
		m.daemonEvents = msg.events

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Switched to project: %s", msg.project.Name),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, m.waitForDaemonEventCmd()

	case overlay.TaskCreatedMsg:
		m.overlayStack.Pop()
		if _, ok := m.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); ok && msg.ParentID != nil {
			m.openCreatedTaskInWorkspace = true
		}
		return m, m.saveTaskCmd(msg)

	case overlay.OpenTaskImageAttachMsg:
		issueID := strings.TrimSpace(msg.IssueID)
		if issueID == "" {
			return m, nil
		}
		attachOverlay := overlay.NewImageAttachOverlay(issueID, m.attachmentService)
		return m, m.openOverlay(attachOverlay)

	case taskCreatedResultMsg:
		if msg.err != nil {
			m.openCreatedTaskInWorkspace = false
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to create task: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		// Clear persisted create-draft state only after successful new-task creation.
		// Updates from edit overlays must not clear the "new task" draft cache.
		if !msg.isUpdate {
			m.createTaskOverlay = nil
			if taskID := strings.TrimSpace(msg.taskID); taskID != "" {
				m.pendingCreatedTaskID = taskID
				if m.openCreatedTaskInWorkspace {
					m.pendingCreatedWorkspaceTaskID = taskID
					m.openCreatedTaskInWorkspace = false
					m.applyPendingCreatedWorkspaceTask()
				}
				m.applyPendingCreatedTaskSelection()
			} else {
				m.openCreatedTaskInWorkspace = false
			}
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task created: %s", msg.taskID),
			Expires: time.Now().Add(3 * time.Second),
		})
		if strings.TrimSpace(msg.attachmentWarning) != "" {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: msg.attachmentWarning,
				Expires: time.Now().Add(6 * time.Second),
			})
		}

		// Reload issues to show new task
		return m, m.loadIssuesCmd()

	// PR creation overlay messages
	case overlay.PRCreatedMsg:
		m.overlayStack.Pop()
		return m, m.createPRWithOverlayCmd(msg)

	case prCreatedResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to create PR: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		message := fmt.Sprintf("PR created: %s", msg.url)
		if strings.TrimSpace(msg.title) != "" {
			message = fmt.Sprintf("PR created: %s (%s)", msg.title, msg.url)
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(5 * time.Second),
		})
		return m, nil

		// Image attachment messages
	case overlay.AttachmentActionMsg:
		if msg.Action == "attached" {
			filename := "image"
			if msg.Attachment != nil && strings.TrimSpace(msg.Attachment.Filename) != "" {
				filename = msg.Attachment.Filename
			}
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Image attached: %s", filename),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, m.appendAttachmentNoteCmd(msg.Attachment)
		} else if msg.Action == "staged" {
			filename := "image"
			if msg.Attachment != nil && strings.TrimSpace(msg.Attachment.Filename) != "" {
				filename = msg.Attachment.Filename
			}
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Image staged for new task: %s", filename),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		} else if msg.Action == "deleted" {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: "Image attachment deleted",
				Expires: time.Now().Add(3 * time.Second),
			})
		} else if msg.Action == "error" && msg.Error != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Image attachment failed: %s", compactErrorMessage(msg.Error)),
				Expires: time.Now().Add(5 * time.Second),
			})
		}
		return m, nil

	case overlay.OpenImagePreviewMsg:
		imageService, ok := m.attachmentService.(*attachment.Service)
		if !ok {
			m.addToast(Toast{
				Level:   ToastError,
				Message: "Image preview unavailable: unsupported attachment service",
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		preview := overlay.NewImagePreviewOverlay(msg.IssueID, imageService, msg.InitialIndex)
		return m, m.openOverlay(preview)

	// Cleanup executed result
	case overlay.CleanupExecutedMsg:
		m.overlayStack.Pop()
		if msg.Error != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Cleanup failed: %v", msg.Error),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		// Show success toast with results
		result := msg.Result
		var operations []string
		if result.Deleted > 0 {
			operations = append(operations, fmt.Sprintf("%d deleted", result.Deleted))
		}
		if result.Archived > 0 {
			operations = append(operations, fmt.Sprintf("%d archived", result.Archived))
		}
		if result.WorktreesRemoved > 0 {
			operations = append(operations, fmt.Sprintf("%d worktrees removed", result.WorktreesRemoved))
		}
		if result.SessionsCleaned > 0 {
			operations = append(operations, fmt.Sprintf("%d sessions cleaned", result.SessionsCleaned))
		}

		message := "Cleanup completed"
		if len(operations) > 0 {
			message = fmt.Sprintf("Cleanup: %s", strings.Join(operations, ", "))
		}

		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(5 * time.Second),
		})

		// Reload issues to reflect changes
		return m, m.loadIssuesCmd()

	case branchBehindMsg:
		if msg.err != nil {
			m.logger.Warn("failed to check branch distance", "issueID", msg.issueID, "error", msg.err)
			// Proceed to attach anyway if check fails.
			return m, m.attachSessionCmd(msg.issueID)
		}

		if msg.commitsBehind > 0 {
			// Show merge choice overlay
			m.openOverlay(overlay.NewMergeChoiceOverlay(msg.issueID, msg.commitsBehind, m.config.Git.BaseBranch))
			return m, nil
		}

		// Not behind, attach directly
		return m, m.attachSessionCmd(msg.issueID)

	case sessionAttachedMsg:
		message := fmt.Sprintf("Attached to session: %s", msg.issueID)
		if msg.switchedTmux {
			message = fmt.Sprintf("Switched to session: %s", msg.issueID)
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case devServerResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Dev server update failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		if devOverlay, ok := m.overlayStack.Current().(*overlay.DevServerOverlay); ok {
			devOverlay.SyncServer(msg.server)
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Dev server %s: %s", msg.server.Name, msg.server.Status),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case openPROverlayResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to get branch info: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: "Generating PR title/body with AI and creating PR...",
			Expires: time.Now().Add(4 * time.Second),
		})
		return m, m.createPRWithAICmd(msg)

	case openPRResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to open PR: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Opened PR for %s", msg.issueID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil

	case helixOpenResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to open Helix: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}
		if msg.opened {
			m.addToast(Toast{
				Level:   ToastSuccess,
				Message: fmt.Sprintf("Opened Helix for %s", msg.issueID),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: msg.commandHint,
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case taskDeletedResultMsg:
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to archive task: %v", msg.err),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.suppressTaskHydration(msg.taskID)
		m.tasks = removeTaskByID(m.tasks, msg.taskID)
		m.syncProjectionIndexesFromTasks()
		m.editor.ReconcileSelection(m.tasks)
		m.reconcileCursorAfterIssuesRefresh()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task %s archived", msg.taskID),
			Expires: time.Now().Add(2 * time.Second),
		})
		return m, m.loadIssuesCmd()

	case worktreeCleanupConfirmPromptMsg:
		if msg.hasTask {
			m.applySingleTaskWorkspaceRefresh(msg.taskID, msg.task)
		}
		m.pendingCleanup = &pendingWorktreeCleanupConfirmation{
			taskID:      msg.taskID,
			deletedTask: msg.deletedTask,
			force:       msg.force,
		}
		title := "Confirm worktree cleanup?"
		if msg.deletedTask {
			title = "Confirm delete + cleanup?"
		}
		if msg.force {
			title = "Force worktree cleanup?"
			if msg.deletedTask {
				title = "Force delete + cleanup?"
			}
		}
		confirm := overlay.NewConfirmDialogExplicitYN(title, formatWorktreeCleanupConfirmPrompt(msg))
		return m, m.openOverlay(confirm)

	case bulkCleanupPreflightMsg:
		m.applyTaskRefreshes(msg.refreshedTasks)
		if len(msg.risks) == 0 && msg.snapshotErr == nil {
			return m, m.bulkCleanupWorktreeCmd(msg.taskIDs, msg.deletedTasks)
		}
		m.pendingBulkCleanup = &pendingBulkCleanupConfirmation{
			taskIDs:      append([]string(nil), msg.taskIDs...),
			deletedTasks: msg.deletedTasks,
		}
		confirm := overlay.NewConfirmDialogExplicitYN("Bulk cleanup preflight", formatBulkCleanupPreflightPrompt(msg))
		return m, m.openOverlay(confirm)

	case worktreeCleanupResultMsg:
		if msg.operationID != "" && !operationStateTerminal(msg.state) {
			m.pendingCleanupOps[msg.operationID] = pendingWorktreeCleanupConfirmation{
				taskID:      msg.taskID,
				deletedTask: msg.deletedTask,
				force:       msg.force,
			}
			m.markTaskOperationPending(msg.taskID, "worktree_cleanup", msg.operationID, msg.state)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastInfo,
				Message: formatPendingOperationMessage("Worktree cleanup", msg.taskID, msg.operationID, msg.state),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, m.loadIssuesCmd()
		}
		if msg.needsForce {
			m.pendingCleanup = &pendingWorktreeCleanupConfirmation{
				taskID:      msg.taskID,
				deletedTask: msg.deletedTask,
				force:       true,
			}
			action := "cleanup worktree"
			if msg.deletedTask {
				action = "delete task and cleanup worktree"
			}
			confirm := overlay.NewConfirmDialogExplicitYN(
				"Force worktree cleanup?",
				fmt.Sprintf("Worktree has local changes.\n\nAction: %s\nTask: %s\n\nDetails: %s\n\nForce removal will discard modified/untracked files.\nProceed?", action, msg.taskID, msg.reason),
			)
			return m, m.openOverlay(confirm)
		}
		m.pendingCleanup = nil
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Worktree cleanup failed: %v", msg.err),
				Expires: time.Now().Add(4 * time.Second),
			})
			return m, nil
		}

		message := fmt.Sprintf("Worktree cleaned for %s", msg.taskID)
		if msg.deletedTask {
			message = fmt.Sprintf("Task %s deleted and worktree cleaned", msg.taskID)
		}
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: message,
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, m.loadIssuesCmd()

	case taskStatusResultMsg:
		if msg.err != nil {
			if pending, ok := pendingOperationDetails(msg.err); ok {
				m.markTaskStatusPending(msg.taskID, msg.previousStatus, msg.newStatus, pending.OperationID, pending.State)
				m.syncTaskWorkspaceOverlay()
				m.addToast(Toast{
					Level:   ToastInfo,
					Message: formatPendingOperationMessage("Task move", msg.taskID, pending.OperationID, pending.State),
					Expires: time.Now().Add(5 * time.Second),
				})
				return m, m.loadIssuesCmd()
			}
			m.rollbackTaskStatus(msg.taskID, msg.previousStatus)
			m.syncTaskWorkspaceOverlay()
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Failed to update task: %v", msg.err),
				Expires: time.Now().Add(3 * time.Second),
			})
			return m, nil
		}
		m.clearPendingTaskStatus(msg.taskID)
		m.syncTaskWorkspaceOverlay()
		m.addToast(Toast{
			Level:   ToastSuccess,
			Message: fmt.Sprintf("Task moved to %s", msg.newStatus),
			Expires: time.Now().Add(2 * time.Second),
		})

		if msg.newStatus == domain.StatusDone {
			if session := m.sessionForIssue(msg.taskID); session != nil {
				return m, tea.Batch(
					m.loadIssuesCmd(),
					m.openPROverlayCmd(session.Worktree, msg.taskID),
				)
			}
		}

		return m, m.loadIssuesCmd()

	case bulkStatusResultMsg:
		m.loading = false
		if msg.err != nil {
			m.addToast(Toast{
				Level:   ToastError,
				Message: fmt.Sprintf("Bulk action failed: %v", msg.err),
				Expires: time.Now().Add(5 * time.Second),
			})
			return m, nil
		}

		summary := fmt.Sprintf("Bulk action completed: %d updated", msg.updated)
		level := ToastSuccess
		if len(msg.issues) > 0 {
			level = ToastWarning
			summary = fmt.Sprintf("%s, %d reported issues (%s)", summary, len(msg.issues), summarizeBulkIssues(msg.issues))
		}
		if msg.failed > 0 {
			level = ToastWarning
			summary = fmt.Sprintf("%s, %d failed", summary, msg.failed)
		}
		m.addToast(Toast{
			Level:   level,
			Message: summary,
			Expires: time.Now().Add(3 * time.Second),
		})
		m.editor.ClearSelection()
		m.editor.EnterNormal()
		return m, m.loadIssuesCmd()
	}

	if !m.overlayStack.IsEmpty() {
		return m, m.overlayStack.Update(msg)
	}

	return m, nil
}

func (m Model) refreshOpenDiffOverlayFromProjectionBody(body protocol.ProjectionUpdateEventBody) tea.Cmd {
	current := m.overlayStack.Current()
	viewer, ok := current.(*diff.DiffViewer)
	if !ok || viewer == nil {
		return nil
	}

	eventWorktree := strings.TrimSpace(body.Worktree)
	if eventWorktree == "" && body.Runtime != nil {
		eventWorktree = strings.TrimSpace(body.Runtime.Projection.Worktree.Path)
	}
	if eventWorktree == "" || viewer.Worktree() != eventWorktree {
		return nil
	}

	_, cmd := viewer.Update(diff.ExternalRefreshMsg{})
	return cmd
}
