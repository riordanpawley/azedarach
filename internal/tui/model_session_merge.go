package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func (m Model) fetchAndMergeCmd(worktree, branch, issueID string, attachAfter bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()
		if branch == "" {
			branch = "main"
		}

		if m.daemonClient == nil {
			return fetchAndMergeResultMsg{
				worktree:    worktree,
				issueID:     issueID,
				attachAfter: attachAfter,
				err:         fmt.Errorf("daemon client unavailable"),
			}
		}

		// Fetch from origin through the daemon command surface.
		if _, err := m.daemonClient.GitFetch(ctx, worktree, "origin"); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return fetchAndMergeResultMsg{
					worktree:    worktree,
					issueID:     issueID,
					attachAfter: attachAfter,
					stage:       "fetch",
					operationID: pending.OperationID,
					state:       pending.State,
				}
			}
			return fetchAndMergeResultMsg{
				worktree:    worktree,
				issueID:     issueID,
				attachAfter: attachAfter,
				err:         fmt.Errorf("fetch failed: %w", err),
			}
		}

		mergeRef := branch
		if !strings.EqualFold(strings.TrimSpace(m.config.Git.WorkflowMode), "local") {
			mergeRef = "origin/" + branch
		}

		// Merge configured base branch reference through the daemon command
		// surface; in non-local modes this uses origin/<base>.
		result, err := m.daemonClient.GitMerge(ctx, worktree, mergeRef)
		if pending, ok := pendingOperationDetails(err); ok {
			return fetchAndMergeResultMsg{
				worktree:    worktree,
				issueID:     issueID,
				attachAfter: attachAfter,
				stage:       "merge",
				operationID: pending.OperationID,
				state:       pending.State,
			}
		}
		return fetchAndMergeResultMsg{
			worktree:    worktree,
			issueID:     issueID,
			attachAfter: attachAfter,
			stage:       "merge",
			result:      &result.Result,
			err:         err,
		}
	}
}

type sessionAttachedMsg struct {
	issueID      string
	switchedTmux bool
}

func (m Model) attachSessionCmd(issueID string) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return sessionErrorMsg{issueID: issueID, err: fmt.Errorf("daemon client unavailable")}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if _, err := m.daemonClient.AttachSession(ctx, issueID); err != nil {
			return sessionErrorMsg{issueID: issueID, err: err}
		}

		if m.tmuxAvailable || strings.TrimSpace(os.Getenv("TMUX")) != "" {
			targets := []string{
				issueID,
				naming.CanonicalSessionID(m.daemonProjectID(), issueID),
			}
			seen := map[string]struct{}{}
			var lastErr error
			for _, target := range targets {
				target = strings.TrimSpace(target)
				if target == "" {
					continue
				}
				if _, exists := seen[target]; exists {
					continue
				}
				seen[target] = struct{}{}
				if m.tmuxClient == nil {
					break
				}
				if err := m.tmuxClient.SwitchClient(ctx, target); err == nil {
					return sessionAttachedMsg{issueID: issueID, switchedTmux: true}
				} else {
					lastErr = err
				}
			}
			if lastErr != nil {
				return sessionErrorMsg{
					issueID: issueID,
					err:     fmt.Errorf("attached in daemon but failed to switch tmux client: %w", lastErr),
				}
			}
		}

		return sessionAttachedMsg{issueID: issueID, switchedTmux: false}
	}
}

func (m Model) resolveConflictWithAICmd(issueID string) tea.Cmd {
	return func() tea.Msg {
		msg := m.attachSessionCmd(issueID)()
		errMsg, ok := msg.(sessionErrorMsg)
		if !ok {
			return msg
		}

		if isSessionNotFoundError(errMsg.err) {
			startMsg := m.startSessionCmd(issueID, m.resolveBaseBranch(), false, true)()
			if startErr, ok := startMsg.(sessionErrorMsg); ok {
				return conflictResolveFallbackMsg{
					issueID: issueID,
					err:     fmt.Errorf("start AI session: %w", startErr.err),
				}
			}

			if started, ok := startMsg.(sessionStartedMsg); ok {
				if started.operationID != "" && !operationStateTerminal(started.state) {
					return started
				}
				reattachMsg := m.attachSessionCmd(issueID)()
				if reattachErr, ok := reattachMsg.(sessionErrorMsg); ok {
					return conflictResolveFallbackMsg{
						issueID: issueID,
						err:     fmt.Errorf("attach started AI session: %w", reattachErr.err),
					}
				}
				return reattachMsg
			}
			return startMsg
		}

		return conflictResolveFallbackMsg{
			issueID: issueID,
			err:     errMsg.err,
		}
	}
}

func isSessionNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var cmdErr *daemonclient.CommandError
	if errors.As(err, &cmdErr) && cmdErr.Code == protocol.ErrorCodeInvalidRequest {
		message := strings.ToLower(strings.TrimSpace(cmdErr.Message))
		return strings.Contains(message, "session not found")
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "session not found")
}

// createPRCmd generates the gh pr create command
func (m Model) createPRCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		branch, err := m.resolveWorktreeBranch(ctx, worktree, issueID)
		if err != nil {
			return createPRResultMsg{
				issueID: issueID,
				err:     fmt.Errorf("failed to get current branch: %w", err),
			}
		}

		// Generate gh pr create command
		cmd := fmt.Sprintf("gh pr create --head %s --title \"[%s] ...\" --body \"...\"", branch, issueID)

		return createPRResultMsg{
			issueID: issueID,
			cmd:     cmd,
			err:     nil,
		}
	}
}

func (m Model) openPRCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resolvedWorktree := strings.TrimSpace(worktree)
		if resolvedWorktree == "" {
			if fallback, resolveErr := m.resolveIssueWorktreePath(ctx, issueID); resolveErr == nil {
				resolvedWorktree = strings.TrimSpace(fallback)
			}
		}
		branch, err := m.resolveWorktreeBranch(ctx, resolvedWorktree, issueID)
		if err != nil {
			return openPRResultMsg{issueID: issueID, err: fmt.Errorf("resolve branch: %w", err)}
		}
		cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--head", branch, "--web")
		cmd.Dir = resolvedWorktree
		if err := cmd.Run(); err != nil {
			return openPRResultMsg{issueID: issueID, err: err}
		}
		return openPRResultMsg{issueID: issueID}
	}
}

func (m Model) openHelixCmd(worktree, issueID string) tea.Cmd {
	return func() tea.Msg {
		resolvedWorktree := strings.TrimSpace(worktree)
		if resolvedWorktree == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if fallback, err := m.resolveIssueWorktreePath(ctx, issueID); err == nil {
				resolvedWorktree = strings.TrimSpace(fallback)
			}
		}
		if strings.TrimSpace(resolvedWorktree) == "" {
			return helixOpenResultMsg{issueID: issueID, err: fmt.Errorf("worktree path is empty")}
		}
		if strings.TrimSpace(os.Getenv("TMUX")) != "" && m.tmuxClient != nil {
			popupCommand := fmt.Sprintf("cd %s && hx", shellSingleQuote(resolvedWorktree))
			if err := m.tmuxClient.DisplayPopup(context.Background(), "hx-"+issueID, "90%", "90%", popupCommand); err != nil {
				return helixOpenResultMsg{issueID: issueID, err: err}
			}
			return helixOpenResultMsg{issueID: issueID, opened: true}
		}
		return helixOpenResultMsg{
			issueID:     issueID,
			commandHint: fmt.Sprintf("Run: cd %s && hx", resolvedWorktree),
		}
	}
}

// handleConflictResolution handles conflict resolution choices
func (m Model) handleConflictResolution(resolution overlay.ConflictResolutionMsg) (tea.Model, tea.Cmd) {
	// Close the overlay
	m.overlayStack.Pop()

	task, session := m.getCurrentTaskAndSession()
	if task == nil || session == nil {
		return m, nil
	}

	switch {
	case resolution.Abort:
		// Abort the merge
		return m, m.abortMergeCmd(session.Worktree)

	case resolution.OpenManually:
		// Show instructions to open in editor
		m.addToast(Toast{
			Level:   ToastInfo,
			Message: fmt.Sprintf("Open conflicted files in your editor at: %s", session.Worktree),
			Expires: time.Now().Add(8 * time.Second),
		})
		return m, nil

	case resolution.ResolveWithClaude:
		// Attach to tmux session so AI can resolve merge conflicts in-session.
		if !m.tmuxAvailable {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("tmux attach-session -t %s is unavailable outside tmux; launch az inside tmux to use tmux actions", task.ID),
				Expires: time.Now().Add(8 * time.Second),
			})
			return m, nil
		}
		if m.daemonClient == nil {
			m.addToast(Toast{
				Level:   ToastWarning,
				Message: fmt.Sprintf("Daemon unavailable. Run: tmux attach-session -t %s (AI can help resolve)", task.ID),
				Expires: time.Now().Add(8 * time.Second),
			})
			return m, nil
		}
		return m, m.resolveConflictWithAICmd(task.ID)

	default:
		return m, nil
	}
}

// handleMergeTargetSelection handles merge target selection
func (m Model) handleMergeTargetSelection(msg overlay.MergeTargetSelectedMsg) (tea.Model, tea.Cmd) {
	m.overlayStack.Pop()
	targetState := domain.SessionIdle
	if targetSession := m.sessionForIssue(msg.TargetID); targetSession != nil {
		targetState = targetSession.State
	}
	return m, m.resolveMergeTargetSelectionCmd(msg.SourceID, msg.TargetID, targetState, !msg.SkipPreflightStatusRefresh)
}

func (m Model) resolveMergeTargetSelectionCmd(sourceID, targetID string, targetState domain.SessionState, refreshStatus bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		sourceWorktree, err := m.resolveIssueWorktreePath(ctx, sourceID)
		if err != nil || sourceWorktree == "" {
			return mergeTargetSelectionResolvedMsg{
				sourceID:      sourceID,
				targetID:      targetID,
				targetState:   targetState,
				refreshStatus: refreshStatus,
				err:           fmt.Errorf("source session worktree not found"),
			}
		}

		if targetID == "main" {
			return mergeTargetSelectionResolvedMsg{
				sourceID:       sourceID,
				targetID:       targetID,
				sourceWorktree: sourceWorktree,
				targetWorktree: m.activeProjectPath(),
				targetState:    targetState,
				refreshStatus:  refreshStatus,
			}
		}

		targetWorktree, err := m.resolveIssueWorktreePath(ctx, targetID)
		if err != nil || targetWorktree == "" {
			return mergeTargetSelectionResolvedMsg{
				sourceID:       sourceID,
				targetID:       targetID,
				sourceWorktree: sourceWorktree,
				targetState:    targetState,
				refreshStatus:  refreshStatus,
				err:            fmt.Errorf("target session worktree not found"),
			}
		}
		return mergeTargetSelectionResolvedMsg{
			sourceID:       sourceID,
			targetID:       targetID,
			sourceWorktree: sourceWorktree,
			targetWorktree: targetWorktree,
			targetState:    targetState,
			refreshStatus:  refreshStatus,
		}
	}
}

type mergeResultMsg struct {
	sourceID    string
	targetID    string
	result      *daemonclient.MergeResult
	stage       string
	state       protocol.OperationState
	operationID string
	err         error
}

type mergePreflightFailureMsg struct {
	sourceID       string
	sourceWorktree string
	targetID       string
	targetWorktree string
	reasons        []string
	sourceFiles    []string
	targetFiles    []string
}

type mergePreflightActionResultMsg struct {
	action   string
	side     string
	worktree string
	err      error
}

type mergeTargetSelectionResolvedMsg struct {
	sourceID       string
	targetID       string
	sourceWorktree string
	targetWorktree string
	targetState    domain.SessionState
	refreshStatus  bool
	err            error
}

func (m Model) mergeToMainCmd(sourceWorktree, sourceID string, refreshStatus bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		baseBranch := m.resolveBaseBranch()
		mainWorktree := m.activeProjectPath()
		if strings.TrimSpace(mainWorktree) == "" {
			mainWorktree = "."
		}

		branch, err := m.resolveWorktreeBranch(ctx, sourceWorktree, sourceID)
		if err != nil {
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: err}
		}

		m.logger.Info("merging upstream source into main",
			"sourceID", sourceID,
			"sourceBranch", branch,
			"targetBranch", baseBranch,
		)

		if m.daemonClient == nil {
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: fmt.Errorf("daemon client unavailable")}
		}

		if preflight := m.checkMergePreflight(ctx, sourceID, "main", sourceWorktree, mainWorktree, baseBranch, branch, refreshStatus); preflight != nil {
			return *preflight
		}

		if _, err := m.daemonClient.GitFetch(ctx, mainWorktree, "origin"); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return mergeResultMsg{
					sourceID:    sourceID,
					targetID:    "main",
					stage:       "fetch",
					state:       pending.State,
					operationID: pending.OperationID,
				}
			}
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: err}
		}

		if _, err := m.daemonClient.GitCheckout(ctx, mainWorktree, baseBranch); err != nil {
			if pending, ok := pendingOperationDetails(err); ok {
				return mergeResultMsg{
					sourceID:    sourceID,
					targetID:    "main",
					stage:       "checkout",
					state:       pending.State,
					operationID: pending.OperationID,
				}
			}
			return mergeResultMsg{sourceID: sourceID, targetID: "main", err: err}
		}

		result, err := m.daemonClient.GitMerge(ctx, mainWorktree, branch)
		if pending, ok := pendingOperationDetails(err); ok {
			return mergeResultMsg{
				sourceID:    sourceID,
				targetID:    "main",
				stage:       "merge",
				state:       pending.State,
				operationID: pending.OperationID,
			}
		}
		return mergeResultMsg{sourceID: sourceID, targetID: "main", result: &result.Result, err: err}
	}
}

func (m Model) mergeFeatureIntoFeatureCmd(sourceWorktree, targetWorktree, sourceID, targetID string, refreshStatus bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		sourceBranch, err := m.resolveWorktreeBranch(ctx, sourceWorktree, sourceID)
		if err != nil {
			return mergeResultMsg{sourceID: sourceID, targetID: targetID, err: err}
		}

		m.logger.Info("merging upstream source into target issue branch",
			"sourceID", sourceID,
			"targetID", targetID,
			"sourceBranch", sourceBranch,
			"targetWorktree", targetWorktree,
		)

		if m.daemonClient == nil {
			return mergeResultMsg{sourceID: sourceID, targetID: targetID, err: fmt.Errorf("daemon client unavailable")}
		}

		if preflight := m.checkMergePreflight(ctx, sourceID, targetID, sourceWorktree, targetWorktree, "HEAD", sourceBranch, refreshStatus); preflight != nil {
			return *preflight
		}

		result, err := m.daemonClient.GitMerge(ctx, targetWorktree, sourceBranch)
		if pending, ok := pendingOperationDetails(err); ok {
			return mergeResultMsg{
				sourceID:    sourceID,
				targetID:    targetID,
				stage:       "merge",
				state:       pending.State,
				operationID: pending.OperationID,
			}
		}
		return mergeResultMsg{sourceID: sourceID, targetID: targetID, result: &result.Result, err: err}
	}
}

func shouldStopBeforeFollowOnMerge(state domain.SessionState) bool {
	return state == domain.SessionBusy || state == domain.SessionWaiting
}

func (m Model) followOnMergeIntoTargetCmd(sourceWorktree, targetWorktree, sourceID, targetID string, targetState domain.SessionState, refreshStatus bool) tea.Cmd {
	return func() tea.Msg {
		if shouldStopBeforeFollowOnMerge(targetState) {
			ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
			defer cancel()
			if m.daemonClient == nil {
				return mergeResultMsg{sourceID: sourceID, targetID: targetID, err: fmt.Errorf("daemon client unavailable")}
			}
			m.sessionMonitor.Stop(targetID)
			if _, err := m.daemonClient.StopSession(ctx, targetID); err != nil {
				if pending, ok := pendingOperationDetails(err); ok {
					return mergeResultMsg{
						sourceID:    sourceID,
						targetID:    targetID,
						stage:       "stop_session",
						state:       pending.State,
						operationID: pending.OperationID,
					}
				}
				return mergeResultMsg{
					sourceID: sourceID,
					targetID: targetID,
					err:      fmt.Errorf("stop target session %s before merge: %w", targetID, err),
				}
			}
		}
		return m.mergeFeatureIntoFeatureCmd(sourceWorktree, targetWorktree, sourceID, targetID, refreshStatus)()
	}
}

func (m *Model) followOnMergeSelectionCmd(task *domain.Task, session *domain.Session) tea.Cmd {
	if task == nil {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No focused issue to merge",
			Expires: time.Now().Add(3 * time.Second),
		})
		return nil
	}
	targetState, targetStateKnown := projectedSessionState(session, m.sessionForIssue(task.ID))

	candidates := m.getFollowOnMergeCandidates(task)
	if len(candidates) == 0 {
		if task.ParentID == nil {
			return m.resolveMergeToMainCmd(task.ID, true)
		}
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No eligible upstream sources for follow-on merge; upstream sources must have an active session and be in progress or done",
			Expires: time.Now().Add(5 * time.Second),
		})
		return nil
	}

	if len(candidates) == 1 {
		m.logger.Info("follow-on merge selected",
			"sourceID", candidates[0].target.ID,
			"targetID", task.ID,
			"relation", candidates[0].relation,
		)
		return m.resolveFollowOnMergeCmd(candidates[0].target.ID, task.ID, targetState, targetStateKnown, true)
	}

	upstreamTargets := make([]overlay.MergeTarget, 0, len(candidates))
	for _, candidate := range candidates {
		upstreamTargets = append(upstreamTargets, candidate.target)
	}
	m.logger.Info("follow-on merge source picker opened", "targetID", task.ID, "candidateCount", len(upstreamTargets))
	return m.openOverlay(overlay.NewMergeSourceSelectOverlay(task, upstreamTargets, nil, nil))
}

func projectedSessionState(primary, fallback *domain.Session) (domain.SessionState, bool) {
	if primary != nil {
		return primary.State, true
	}
	if fallback != nil {
		return fallback.State, true
	}
	return domain.SessionIdle, false
}

func (m Model) resolveMergeToMainCmd(sourceID string, refreshStatus bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		sourceWorktree, err := m.resolveIssueWorktreePath(ctx, sourceID)
		if err != nil || sourceWorktree == "" {
			return mergeResultMsg{
				sourceID: sourceID,
				targetID: "main",
				err:      fmt.Errorf("no active session/worktree - start session first"),
			}
		}
		return m.mergeToMainCmd(sourceWorktree, sourceID, refreshStatus)()
	}
}

func (m Model) resolveFollowOnMergeCmd(sourceID, targetID string, targetState domain.SessionState, targetStateKnown bool, refreshStatus bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		sourceWorktree, err := m.resolveIssueWorktreePath(ctx, sourceID)
		if err != nil || sourceWorktree == "" {
			return mergeResultMsg{
				sourceID: sourceID,
				targetID: targetID,
				err:      fmt.Errorf("selected upstream source has no active worktree"),
			}
		}

		targetWorktree, err := m.resolveIssueWorktreePath(ctx, targetID)
		if err != nil || targetWorktree == "" {
			return mergeResultMsg{
				sourceID: sourceID,
				targetID: targetID,
				err:      fmt.Errorf("no active session/worktree - start session first"),
			}
		}

		resolvedTargetState := targetState
		if !targetStateKnown {
			state, ok, err := m.resolveIssueSessionStateFromSnapshot(ctx, targetID)
			if err != nil {
				return mergeResultMsg{
					sourceID: sourceID,
					targetID: targetID,
					err:      fmt.Errorf("resolve target session state for %s: %w", targetID, err),
				}
			}
			if !ok {
				return mergeResultMsg{
					sourceID: sourceID,
					targetID: targetID,
					err:      fmt.Errorf("target session state unavailable for %s; refresh and retry", targetID),
				}
			}
			resolvedTargetState = state
		}

		return m.followOnMergeIntoTargetCmd(sourceWorktree, targetWorktree, sourceID, targetID, resolvedTargetState, refreshStatus)()
	}
}

func (m Model) openMergeTargetSelection(task *domain.Task) tea.Cmd {
	if task == nil {
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: "No focused issue to merge",
			Expires: time.Now().Add(3 * time.Second),
		})
		return nil
	}

	candidates := m.getMergeCandidates(task)
	mergeOverlay := overlay.NewMergeSelectOverlay(
		task,
		candidates,
		func(targetID string) tea.Cmd {
			return func() tea.Msg {
				return overlay.SelectionMsg{
					Key: "merge",
					Value: overlay.MergeTargetSelectedMsg{
						SourceID: task.ID,
						TargetID: targetID,
					},
				}
			}
		},
		func() tea.Cmd { return func() tea.Msg { return overlay.CloseOverlayMsg{} } },
	)
	return m.openOverlay(mergeOverlay)
}

func (m Model) sessionForIssue(issueID string) *domain.Session {
	if issueID == "" {
		return nil
	}
	for i := range m.tasks {
		if m.tasks[i].ID == issueID && m.tasks[i].Session != nil {
			return m.tasks[i].Session
		}
	}
	return nil
}

type followOnMergeCandidate struct {
	target   overlay.MergeTarget
	relation string
	order    int
}

func (m Model) getFollowOnMergeCandidates(target *domain.Task) []followOnMergeCandidate {
	if target == nil {
		return nil
	}

	candidates := make([]followOnMergeCandidate, 0, 4)
	seen := make(map[string]struct{}, 4)

	addCandidate := func(taskID, relation string, order int) {
		if taskID == "" {
			return
		}
		if _, ok := seen[taskID]; ok {
			return
		}
		for _, task := range m.tasks {
			if task.ID != taskID {
				continue
			}
			hasWorktree := false
			if task.Session != nil && task.Session.Worktree != "" {
				hasWorktree = true
			} else if task.HasWorktree {
				hasWorktree = true
			}
			if !isEligibleUpstreamSource(task, relation, hasWorktree) {
				return
			}
			candidates = append(candidates, followOnMergeCandidate{
				target: overlay.MergeTarget{
					ID:          task.ID,
					Label:       task.Title,
					IsMain:      false,
					Status:      task.Status,
					HasWorktree: hasWorktree,
				},
				relation: relation,
				order:    order,
			})
			seen[taskID] = struct{}{}
			return
		}
	}

	if target.ParentID != nil {
		addCandidate(*target.ParentID, string(domain.DependencyParentChild), 0)
	}
	for _, dep := range target.Dependencies {
		switch dep.Type {
		case domain.DependencyBlocks, domain.DependencyBlockedBy:
			addCandidate(dep.ID, string(domain.DependencyBlocks), 1)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].order != candidates[j].order {
			return candidates[i].order < candidates[j].order
		}
		if statusPriority(candidates[i].target.Status) != statusPriority(candidates[j].target.Status) {
			return statusPriority(candidates[i].target.Status) < statusPriority(candidates[j].target.Status)
		}
		if candidates[i].target.Label != candidates[j].target.Label {
			return candidates[i].target.Label < candidates[j].target.Label
		}
		return candidates[i].target.ID < candidates[j].target.ID
	})

	return candidates
}

func isEligibleUpstreamSource(task domain.Task, relation string, hasWorktree bool) bool {
	switch relation {
	case string(domain.DependencyParentChild), string(domain.DependencyBlocks):
		return hasWorktree && (task.Status == domain.StatusInProgress || task.Status == domain.StatusDone)
	default:
		return false
	}
}

func statusPriority(status domain.Status) int {
	switch status {
	case domain.StatusInProgress:
		return 0
	case domain.StatusDone:
		return 1
	default:
		return 2
	}
}

type abortMergeResultMsg struct {
	worktree string
	err      error
}

// abortMergeCmd aborts an ongoing merge
func (m Model) abortMergeCmd(worktree string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.daemonCommandTimeout())
		defer cancel()

		if m.daemonClient == nil {
			return abortMergeResultMsg{
				worktree: worktree,
				err:      fmt.Errorf("daemon client unavailable"),
			}
		}

		_, err := m.daemonClient.GitAbortMerge(ctx, worktree)
		return abortMergeResultMsg{
			worktree: worktree,
			err:      err,
		}
	}
}

func (m Model) abortMergeIssueCmd(issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		worktree, err := m.resolveIssueWorktreePath(ctx, issueID)
		if err != nil {
			return abortMergeResultMsg{
				worktree: "",
				err:      err,
			}
		}
		return m.abortMergeCmd(worktree)()
	}
}

// Bulk status commands

type bulkStatusResultMsg struct {
	updated int
	issues  []bulkTaskIssue
	failed  int
	err     error
}

type bulkTaskIssue struct {
	taskID string
	reason string
}

// bulkMoveStatusCmd moves tasks by delta (-1 = left, +1 = right)
