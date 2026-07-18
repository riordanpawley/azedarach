package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	sessionRestartPreflightTimeout   = 2 * time.Second
	sessionRestartPrepareTimeout     = 3 * time.Second
	sessionRestartReplaceTimeout     = 3 * time.Second
	sessionRestartObservationTimeout = 8 * time.Second

	sessionRestartPromptHandoffTypeNone              = "none"
	sessionRestartPromptHandoffTypeOwnerOnlyArtifact = "owner_only_launch_artifact"
	sessionRestartStageRootedInvalidateReady         = "rooted_invalidate_ready"
	sessionRestartBatchPlanVersion                   = 1

	sessionRestartLifecycleRequested   = protocol.SessionRestartStageRequested
	sessionRestartLifecycleTerminating = protocol.SessionRestartStageTerminating
	sessionRestartLifecycleLaunching   = protocol.SessionRestartStageLaunching
	sessionRestartLifecycleVerifying   = protocol.SessionRestartStageVerifying
	sessionRestartLifecycleCompleted   = protocol.SessionRestartStageCompleted
	sessionRestartLifecycleFailed      = protocol.SessionRestartStageFailed
	sessionRestartLifecycleCompensated = protocol.SessionRestartStageCompensated
)

type sessionRestartExecution struct {
	done chan struct{}
	item protocol.SessionRestartAllItem
}

type sessionRestartRecoveryPlan struct {
	ProjectID             string                           `json:"project_id"`
	SessionID             string                           `json:"session_id"`
	IssueID               string                           `json:"issue_id,omitempty"`
	Activity              string                           `json:"activity"`
	Old                   daemonstate.ManagedAgentIdentity `json:"old"`
	PlannedIncarnation    string                           `json:"planned_incarnation"`
	PromptHandoffRequired bool                             `json:"prompt_handoff_required"`
	PromptHandoffType     string                           `json:"prompt_handoff_type"`
	PromptPath            string                           `json:"prompt_path,omitempty"`
	RootedIdentity        *domain.OrchestratorIdentity     `json:"rooted_identity,omitempty"`
	Stage                 string                           `json:"stage"`
}

// sessionRestartBatchPlan is the complete crash-recovery authority for one
// restart-all operation. Targets are immutable once requested is persisted;
// Cursor, Results, and Current advance together through durable progress
// checkpoints.
type sessionRestartBatchPlan struct {
	Version     int                                   `json:"version"`
	ProjectID   string                                `json:"project_id"`
	ProjectIDs  []string                              `json:"project_ids"`
	Request     protocol.SessionRestartAllRequestBody `json:"request"`
	Targets     []sessionRestartAllTarget             `json:"targets"`
	Cursor      int                                   `json:"cursor"`
	Results     []protocol.SessionRestartAllItem      `json:"results"`
	Current     *sessionRestartRecoveryPlan           `json:"current,omitempty"`
	Recoverable []sessionRestartBatchRecovery         `json:"recoverable,omitempty"`
	Stage       string                                `json:"stage"`
}

type sessionRestartBatchRecovery struct {
	Index int                        `json:"index"`
	Plan  sessionRestartRecoveryPlan `json:"plan"`
}

type sessionRestartBatchProgress struct {
	plan *sessionRestartBatchPlan
}

type sessionRestartBatchProgressKey struct{}

type restartStageResult[T any] struct {
	value T
	err   error
}

func restartStage(detail, status, message string, timeout time.Duration) protocol.SessionRestartStage {
	name := restartLifecycleStage(detail, status)
	if detail = strings.TrimSpace(detail); detail != "" && detail != name {
		message = detail + ": " + message
	}
	return protocol.SessionRestartStage{Name: name, Status: status, Message: message, TimeoutMS: timeout.Milliseconds()}
}

func restartLifecycleStage(detail, status string) string {
	if status == "failed" || status == "timeout" {
		return sessionRestartLifecycleFailed
	}
	if status == "refused" || detail == sessionRestartLifecycleCompensated {
		return sessionRestartLifecycleCompensated
	}
	switch detail {
	case "rooted_invalidate", "persist_rooted_invalidate_ready", "replace_preflight", "persist_replace_ready":
		return sessionRestartLifecycleTerminating
	case "replace":
		return sessionRestartLifecycleLaunching
	case "observe", "persist_observe", "prompt_handoff", "rooted_bootstrap":
		return sessionRestartLifecycleVerifying
	case "persist_complete", "recover", sessionRestartLifecycleCompleted:
		return sessionRestartLifecycleCompleted
	case sessionRestartLifecycleRequested, sessionRestartLifecycleTerminating, sessionRestartLifecycleLaunching,
		sessionRestartLifecycleVerifying, sessionRestartLifecycleFailed:
		return detail
	default:
		return sessionRestartLifecycleRequested
	}
}

func runRestartStage[T any](ctx context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error, bool) {
	var zero T
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ch := make(chan restartStageResult[T], 1)
	go func() { value, err := fn(stageCtx); ch <- restartStageResult[T]{value: value, err: err} }()
	select {
	case result := <-ch:
		return result.value, result.err, false
	case <-stageCtx.Done():
		return zero, stageCtx.Err(), errors.Is(stageCtx.Err(), context.DeadlineExceeded)
	}
}

func appendRestartStageFailure(item *protocol.SessionRestartAllItem, name string, timeout time.Duration, err error, timedOut bool) {
	status := "failed"
	if timedOut {
		status = "timeout"
	}
	item.Outcome = "partial_failure"
	item.Error = err.Error()
	item.Stages = append(item.Stages, restartStage(name, status, item.Error, timeout))
}

func (d *Daemon) restartManagedAgentPane(ctx context.Context, target sessionRestartAllTarget, body protocol.SessionRestartAllRequestBody, item protocol.SessionRestartAllItem, rootedIdentity *domain.OrchestratorIdentity) protocol.SessionRestartAllItem {
	source := sourceForInvariant(daemonInvariantManagedAgentRestart)
	if !usesProjectionSource(source) || !usesTmuxSource(source) {
		item.Outcome = "partial_failure"
		item.Error = fmt.Sprintf("managed-agent restart requires hybrid invariant source, got %s", source)
		return item
	}
	store := d.sessionRuntimeStateStoreIfConfigured(target.ProjectID)
	if store == nil {
		item.Outcome, item.Error = "partial_failure", "managed identity store unavailable"
		return item
	}
	identities, err, timedOut := runRestartStage(ctx, sessionRestartPreflightTimeout, func(stageCtx context.Context) ([]daemonstate.ManagedAgentIdentity, error) {
		return store.ListManagedAgentIdentities(stageCtx, target.ProjectID, target.SessionID)
	})
	if err != nil {
		appendRestartStageFailure(&item, "identity", sessionRestartPreflightTimeout, err, timedOut)
		return item
	}
	if len(identities) == 0 {
		item.Outcome = "no_agent"
		if strings.EqualFold(strings.TrimSpace(target.Activity), "no-agent") || strings.EqualFold(strings.TrimSpace(target.Activity), "shell-only") {
			item.Outcome = "shell_only"
		}
		item.Reason, item.Skipped = "no_managed_agent_identity", true
		item.Stages = append(item.Stages, restartStage("identity", "refused", item.Reason, sessionRestartPreflightTimeout))
		return item
	}
	old := identities[0]
	for _, candidate := range identities {
		if candidate.LogicalPaneID == "agent" {
			old = candidate
			break
		}
	}
	item.OldIdentity = restartProtocolIdentity(old)
	item.OperationID = target.ProjectID + "/" + target.SessionID + "/" + old.LogicalPaneID + "/" + old.AgentIncarnation

	d.sessionRestartMu.Lock()
	if d.sessionRestartPending == nil {
		d.sessionRestartPending = make(map[string]*sessionRestartExecution)
	}
	if pending := d.sessionRestartPending[item.OperationID]; pending != nil {
		d.sessionRestartMu.Unlock()
		select {
		case <-ctx.Done():
			item.Outcome, item.Error = "partial_failure", ctx.Err().Error()
			return item
		case <-pending.done:
			return pending.item
		}
	}
	exec := &sessionRestartExecution{done: make(chan struct{})}
	d.sessionRestartPending[item.OperationID] = exec
	d.sessionRestartMu.Unlock()
	defer func() {
		d.sessionRestartMu.Lock()
		exec.item = item
		close(exec.done)
		delete(d.sessionRestartPending, item.OperationID)
		d.sessionRestartMu.Unlock()
	}()
	item = d.restartManagedAgentPaneWithIdentity(ctx, store, old, target, body, item, rootedIdentity)
	return item
}

func (d *Daemon) restartManagedAgentPaneWithIdentity(ctx context.Context, store *daemonstate.RuntimeStateStore, old daemonstate.ManagedAgentIdentity, target sessionRestartAllTarget, body protocol.SessionRestartAllRequestBody, item protocol.SessionRestartAllItem, rootedIdentity *domain.OrchestratorIdentity) protocol.SessionRestartAllItem {
	var lockedItem protocol.SessionRestartAllItem
	lockErr := store.WithManagedAgentRestartTransition(ctx, target.ProjectID, target.SessionID, old.LogicalPaneID, func(lockCtx context.Context) error {
		lockedItem = d.restartManagedAgentPaneLocked(lockCtx, store, old, target, body, item, rootedIdentity)
		return nil
	})
	if lockErr != nil {
		item.Outcome, item.Error = "partial_failure", fmt.Sprintf("serialize exact managed-agent replacement: %v", lockErr)
		return item
	}
	return lockedItem
}

func (d *Daemon) restartManagedAgentPaneLocked(ctx context.Context, store *daemonstate.RuntimeStateStore, old daemonstate.ManagedAgentIdentity, target sessionRestartAllTarget, body protocol.SessionRestartAllRequestBody, item protocol.SessionRestartAllItem, rootedIdentity *domain.OrchestratorIdentity) protocol.SessionRestartAllItem {
	current, found, err := store.GetManagedAgentIdentity(ctx, target.ProjectID, target.SessionID, old.LogicalPaneID)
	if err != nil {
		appendRestartStageFailure(&item, "identity_refresh", sessionRestartPreflightTimeout, err, errors.Is(err, context.DeadlineExceeded))
		return item
	}
	if !found || !sameManagedRestartIdentity(current, old) {
		item.Outcome, item.Reason, item.Skipped = "superseded", "managed_agent_identity_changed", true
		item.Stages = append(item.Stages, restartStage("identity_refresh", "refused", item.Reason, sessionRestartPreflightTimeout))
		return item
	}
	panes, err, timedOut := runRestartStage(ctx, sessionRestartPreflightTimeout, func(stageCtx context.Context) ([]tmux.PaneInfo, error) { return d.tmux.ListPaneInfos(stageCtx) })
	if err != nil {
		appendRestartStageFailure(&item, "preflight", sessionRestartPreflightTimeout, err, timedOut)
		return item
	}
	matched := false
	for _, pane := range panes {
		if pane.SessionName == target.SessionID && sanitizeRuntimePaneID(pane.PaneID) == sanitizeRuntimePaneID(old.TmuxPaneID) && pane.PanePID == old.PanePID {
			matched = true
			break
		}
	}
	if !matched {
		item.Outcome, item.Reason, item.Skipped = "crashed", "managed_agent_identity_not_live", true
		item.Stages = append(item.Stages, restartStage("preflight", "refused", item.Reason, sessionRestartPreflightTimeout))
		return item
	}
	item.Stages = append(item.Stages, restartStage("preflight", "complete", "durable identity matches live pane", sessionRestartPreflightTimeout))
	if strings.EqualFold(target.Activity, "busy") && !body.ForceBusy {
		item.Outcome, item.Reason, item.Skipped = "busy", "busy_requires_force", true
		item.Stages = append(item.Stages, restartStage("busy_gate", "refused", item.Reason, sessionRestartPreflightTimeout))
		return item
	}
	item.Stages = append(item.Stages, restartStage("busy_gate", "complete", target.Activity, sessionRestartPreflightTimeout))

	incarnation, err := newRestartIncarnation()
	if err != nil {
		item.Outcome, item.Error = "partial_failure", err.Error()
		return item
	}
	launchPrompt := sessionRestartContinuePrompt
	if rootedIdentity != nil {
		launchPrompt = ""
	}
	promptHandoffRequired := strings.TrimSpace(launchPrompt) != "" && !strings.EqualFold(strings.TrimSpace(d.runtimeConfigForProject(target.ProjectID).CLITool), "codex")
	promptHandoffType := sessionRestartPromptHandoffTypeNone
	if promptHandoffRequired {
		promptHandoffType = sessionRestartPromptHandoffTypeOwnerOnlyArtifact
	}
	plan := sessionRestartRecoveryPlan{
		ProjectID: target.ProjectID, SessionID: target.SessionID, IssueID: target.IssueID, Activity: target.Activity,
		Old: old, PlannedIncarnation: incarnation, PromptHandoffRequired: promptHandoffRequired,
		PromptHandoffType: promptHandoffType, Stage: "prepare",
	}
	if rootedIdentity != nil {
		identity := *rootedIdentity
		plan.RootedIdentity = &identity
	}
	if err := reportSessionRestartProgress(ctx, plan); err != nil {
		appendRestartStageFailure(&item, "persist_prepare", sessionRestartPreflightTimeout, err, false)
		return item
	}
	type preparedRestart struct {
		artifact sessionLaunchArtifact
		worktree string
	}
	prepared, err, timedOut := runRestartStage(ctx, sessionRestartPrepareTimeout, func(stageCtx context.Context) (preparedRestart, error) {
		artifact, prepareErr := d.prepareSessionLaunchArtifact(sessionLaunchSpec{Mode: sessionLaunchResume, ProjectID: target.ProjectID, IssueID: target.IssueID, SessionID: target.SessionID, Yolo: body.Yolo, ImagePaths: body.ImagePaths, Prompt: launchPrompt, LogicalPaneID: old.LogicalPaneID, AgentIncarnation: incarnation})
		if prepareErr != nil {
			return preparedRestart{}, prepareErr
		}
		worktree := d.cfg.RepoDir
		if worktreeStore := d.worktreeRuntimeStateStoreIfConfigured(target.ProjectID); worktreeStore != nil && strings.TrimSpace(target.IssueID) != "" {
			if projected, found, projectErr := worktreeStore.GetWorktreeStateByIssueID(stageCtx, target.ProjectID, target.IssueID); projectErr != nil {
				artifact.remove()
				return preparedRestart{}, projectErr
			} else if found && strings.TrimSpace(projected.Path) != "" {
				worktree = projected.Path
			}
		}
		if stageCtx.Err() != nil {
			artifact.remove()
			return preparedRestart{}, stageCtx.Err()
		}
		return preparedRestart{artifact: artifact, worktree: worktree}, nil
	})
	if err != nil {
		appendRestartStageFailure(&item, "prepare", sessionRestartPrepareTimeout, err, timedOut)
		return item
	}
	item.Stages = append(item.Stages, restartStage("prepare", "complete", "canonical launch artifact and worktree prepared", sessionRestartPrepareTimeout))
	plan.PromptPath = prepared.artifact.PromptHandoff.PromptPath
	if (strings.TrimSpace(plan.PromptPath) != "") != plan.PromptHandoffRequired {
		prepared.artifact.remove()
		appendRestartStageFailure(&item, "prepare", sessionRestartPrepareTimeout, errors.New("prepared prompt handoff does not match persisted restart metadata"), false)
		return item
	}
	if rootedIdentity != nil {
		plan.Stage = sessionRestartStageRootedInvalidateReady
		if err := reportSessionRestartProgress(ctx, plan); err != nil {
			prepared.artifact.remove()
			appendRestartStageFailure(&item, "persist_rooted_invalidate_ready", sessionRestartPreflightTimeout, err, errors.Is(err, context.DeadlineExceeded))
			return item
		}
		authority := daemonstate.NewRootedBootstrapAcknowledgementAuthority(store)
		acknowledgement, found, rootErr := authority.Get(ctx, *rootedIdentity)
		if rootErr == nil && found {
			rootErr = d.invalidateRootedBootstrapAcknowledgement(ctx, acknowledgement)
		}
		if rootErr == nil {
			rootErr = d.tmux.SetEnvironment(ctx, target.SessionID, rootedOrchestratorBootstrapNonceEnvironment, "")
		}
		if rootErr != nil {
			prepared.artifact.remove()
			appendRestartStageFailure(&item, "rooted_invalidate", sessionRestartPreflightTimeout, rootErr, errors.Is(rootErr, context.DeadlineExceeded))
			return item
		}
		item.Stages = append(item.Stages, restartStage("rooted_invalidate", "complete", "durable and runtime bootstrap markers invalidated", sessionRestartPreflightTimeout))
	}
	paneTarget := strings.TrimSpace(old.TmuxPaneID)
	if !strings.HasPrefix(paneTarget, "%") {
		paneTarget = "%" + paneTarget
	}
	// replace_ready is persisted before the destructive call. Recovery can
	// distinguish an unchanged exact old identity (the call did not take effect)
	// from a changed pane process (replacement took effect before the daemon died).
	plan.Stage = "replace_ready"
	if err := reportSessionRestartProgress(ctx, plan); err != nil {
		prepared.artifact.remove()
		appendRestartStageFailure(&item, "persist_replace_ready", sessionRestartPreflightTimeout, err, false)
		return item
	}
	current, found, err = store.GetManagedAgentIdentity(ctx, target.ProjectID, target.SessionID, old.LogicalPaneID)
	if err != nil {
		prepared.artifact.remove()
		appendRestartStageFailure(&item, "replace_preflight", sessionRestartPreflightTimeout, err, errors.Is(err, context.DeadlineExceeded))
		return item
	}
	if !found || !sameManagedRestartIdentity(current, old) {
		prepared.artifact.remove()
		item.Outcome, item.Reason, item.Skipped = "superseded", "managed_agent_identity_changed_before_replace", true
		item.Stages = append(item.Stages, restartStage("replace_preflight", "refused", item.Reason, sessionRestartPreflightTimeout))
		return item
	}
	livePanes, liveErr, liveTimedOut := runRestartStage(ctx, sessionRestartPreflightTimeout, func(stageCtx context.Context) ([]tmux.PaneInfo, error) { return d.tmux.ListPaneInfos(stageCtx) })
	if liveErr != nil {
		prepared.artifact.remove()
		appendRestartStageFailure(&item, "replace_preflight", sessionRestartPreflightTimeout, liveErr, liveTimedOut)
		return item
	}
	if !managedRestartIdentityLive(target.SessionID, old, livePanes) {
		prepared.artifact.remove()
		item.Outcome, item.Reason, item.Skipped = "superseded", "managed_agent_process_changed_before_replace", true
		item.Stages = append(item.Stages, restartStage("replace_preflight", "refused", item.Reason, sessionRestartPreflightTimeout))
		return item
	}
	item.Stages = append(item.Stages, restartStage("replace_preflight", "complete", "exact durable identity still matches live pane", sessionRestartPreflightTimeout))
	err, ambiguous := d.respawnManagedAgentPane(ctx, paneTarget, prepared.worktree, prepared.artifact.Command)
	if err != nil {
		if !ambiguous {
			prepared.artifact.remove()
			appendRestartStageFailure(&item, "replace", sessionRestartReplaceTimeout, err, false)
			return item
		}
		// A timed-out tmux client cannot prove that the server rejected the
		// destructive command. Keep the stable transition lock while observing
		// the exact planned incarnation so another daemon cannot replace the
		// same pane across this ambiguous acceptance window.
		item.Stages = append(item.Stages, restartStage("replace", "ambiguous", err.Error(), sessionRestartReplaceTimeout))
	} else {
		item.Stages = append(item.Stages, restartStage("replace", "complete", "old pane process terminated and replacement launched", sessionRestartReplaceTimeout))
	}
	// The durable replace_ready checkpoint authorizes a bounded reconciliation
	// independent of caller cancellation. One deadline covers every remaining
	// authority step while the rooted and pane transition locks stay held.
	reconcileCtx, cancelReconcile := context.WithTimeout(context.WithoutCancel(ctx), sessionRestartObservationTimeout)
	defer cancelReconcile()
	plan.Stage = "observe"
	observeProgressErr := reportSessionRestartProgress(reconcileCtx, plan)
	if observeProgressErr != nil {
		item.Stages = append(item.Stages, restartStage("persist_observe", "warning", observeProgressErr.Error(), sessionRestartPreflightTimeout))
	}

	// replace_ready was durable before dispatch. Once dispatch is accepted or
	// ambiguous, caller cancellation and intermediate progress failures cannot
	// release the stable pane fence before exact-incarnation reconciliation.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-reconcileCtx.Done():
			appendRestartStageFailure(&item, "observe", sessionRestartObservationTimeout, errors.Join(reconcileCtx.Err(), observeProgressErr), errors.Is(reconcileCtx.Err(), context.DeadlineExceeded))
			return item
		case <-ticker.C:
			current, found, loadErr := store.GetManagedAgentIdentity(reconcileCtx, target.ProjectID, target.SessionID, old.LogicalPaneID)
			if loadErr != nil {
				appendRestartStageFailure(&item, "observe", sessionRestartObservationTimeout, loadErr, false)
				return item
			}
			liveReplacement := false
			if found {
				if livePanes, liveErr := d.tmux.ListPaneInfos(reconcileCtx); liveErr == nil {
					liveReplacement = managedRestartIdentityLive(target.SessionID, current, livePanes)
				}
			}
			if found && liveReplacement && current.AgentIncarnation == incarnation && current.PanePID != old.PanePID {
				item.NewIdentity = restartProtocolIdentity(current)
				item.Stages = append(item.Stages, restartStage("observe", "complete", "distinct pane process and hook incarnation acknowledged", sessionRestartObservationTimeout))
				handoffErr := d.waitForSessionRestartPromptHandoff(reconcileCtx, prepared.artifact.PromptHandoff)
				if handoffErr != nil {
					prepared.artifact.remove()
					appendRestartStageFailure(&item, "prompt_handoff", sessionRestartObservationTimeout, handoffErr, errors.Is(handoffErr, context.DeadlineExceeded))
					return item
				}
				if strings.TrimSpace(prepared.artifact.PromptHandoff.PromptPath) != "" {
					item.Stages = append(item.Stages, restartStage("prompt_handoff", "complete", "replacement consumed continuation handoff", sessionRestartObservationTimeout))
				}
				if rootedIdentity != nil {
					prompt, promptErr := d.rootedOrchestratorBootstrapPrompt(reconcileCtx, target.ProjectID, rootedIdentity.Scope)
					if promptErr == nil {
						_, promptErr = d.ensureRootedOrchestratorBootstrap(reconcileCtx, target.ProjectID, rootedIdentity.Scope, target.SessionID, prompt, false)
					}
					if promptErr != nil {
						appendRestartStageFailure(&item, "rooted_bootstrap", sessionRestartObservationTimeout, promptErr, errors.Is(promptErr, context.DeadlineExceeded))
						return item
					}
					item.Stages = append(item.Stages, restartStage("rooted_bootstrap", "complete", "replacement rooted orchestrator acknowledged bootstrap", sessionRestartObservationTimeout))
				}
				item.Outcome = restartSuccessOutcome(target.Activity)
				plan.Stage = "complete"
				if err := reportSessionRestartProgress(reconcileCtx, plan); err != nil {
					appendRestartStageFailure(&item, "persist_complete", sessionRestartPreflightTimeout, err, errors.Is(err, context.DeadlineExceeded))
					return item
				}
				item.Restarted = true
				item.Stages = append(item.Stages, restartStage("persist_complete", "complete", "restart completion checkpoint persisted", sessionRestartPreflightTimeout))
				return item
			}
		}
	}
}

func (d *Daemon) respawnManagedAgentPane(ctx context.Context, paneTarget, worktree, command string) (error, bool) {
	if d != nil && d.sessionRestartRespawn != nil {
		return d.sessionRestartRespawn(ctx, paneTarget, worktree, command)
	}
	return runRestartRespawnStage(ctx, sessionRestartReplaceTimeout, func(stageCtx context.Context) error {
		return d.tmux.RespawnPane(stageCtx, paneTarget, worktree, command)
	})
}

func runRestartRespawnStage(ctx context.Context, timeout time.Duration, fn func(context.Context) error) (error, bool) {
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- fn(stageCtx) }()
	select {
	case err := <-result:
		// CommandContext may return at the same instant its context becomes
		// terminal. The result arm winning does not prove tmux rejected dispatch.
		// tmux exposes no typed accepted/rejected disposition. Any error after
		// dispatch is therefore conservatively ambiguous, including result-only
		// cancellation/deadline errors and untyped command failures.
		return err, err != nil
	case <-stageCtx.Done():
		return stageCtx.Err(), true
	}
}

func (d *Daemon) waitForSessionRestartPromptHandoff(ctx context.Context, handoff sessionPromptHandoff) error {
	if strings.TrimSpace(handoff.PromptPath) == "" {
		return nil
	}
	if d != nil && d.sessionRestartPromptHandoffWait != nil {
		return d.sessionRestartPromptHandoffWait(ctx, handoff)
	}
	return waitForSessionPromptHandoffConsumed(ctx, handoff)
}

func (d *Daemon) validateRecoveredSessionRestartPromptHandoff(plan sessionRestartRecoveryPlan) (sessionPromptHandoff, error) {
	handoffType := strings.TrimSpace(plan.PromptHandoffType)
	path := strings.TrimSpace(plan.PromptPath)
	if !plan.PromptHandoffRequired {
		if handoffType != sessionRestartPromptHandoffTypeNone || path != "" {
			return sessionPromptHandoff{}, errors.New("restart recovery prompt handoff metadata is inconsistent")
		}
		return sessionPromptHandoff{}, nil
	}
	if handoffType != sessionRestartPromptHandoffTypeOwnerOnlyArtifact {
		return sessionPromptHandoff{}, fmt.Errorf("unsupported restart recovery prompt handoff type %q", handoffType)
	}
	if path == "" {
		return sessionPromptHandoff{}, errors.New("restart recovery prompt handoff path is required")
	}
	if path != plan.PromptPath || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return sessionPromptHandoff{}, errors.New("restart recovery prompt handoff path is not canonical")
	}
	dir, err := filepath.Abs(filepath.Clean(d.sessionLaunchArtifactDir()))
	if err != nil {
		return sessionPromptHandoff{}, fmt.Errorf("resolve session launch artifact directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return sessionPromptHandoff{}, fmt.Errorf("inspect session launch artifact directory: %w", err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || dirInfo.Mode().Perm() != 0o700 {
		return sessionPromptHandoff{}, errors.New("session launch artifact directory is not owner-only")
	}
	if filepath.Dir(path) != dir {
		return sessionPromptHandoff{}, errors.New("restart recovery prompt handoff is outside session launch artifact directory")
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, sessionLaunchArtifactPrefix) || !strings.HasSuffix(base, ".prompt") || len(base) <= len(sessionLaunchArtifactPrefix)+len(".prompt") {
		return sessionPromptHandoff{}, errors.New("restart recovery prompt handoff is not an expected launch artifact")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return sessionPromptHandoff{PromptPath: path}, nil
	}
	if err != nil {
		return sessionPromptHandoff{}, fmt.Errorf("inspect restart recovery prompt handoff: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return sessionPromptHandoff{}, errors.New("restart recovery prompt handoff is not an owner-only regular artifact")
	}
	return sessionPromptHandoff{PromptPath: path}, nil
}

func (d *Daemon) repairRecoveredSessionRestartRootedBootstrap(ctx context.Context, plan sessionRestartRecoveryPlan) error {
	if plan.RootedIdentity == nil {
		return nil
	}
	identity, err := domain.NewOrchestratorIdentity(plan.RootedIdentity.ProjectID, plan.RootedIdentity.Scope)
	if err != nil {
		return fmt.Errorf("validate recovered rooted orchestrator identity: %w", err)
	}
	if identity != *plan.RootedIdentity || identity.ProjectID != plan.ProjectID || identity.Scope.Kind != domain.OrchestrationScopeRooted {
		return errors.New("recovered rooted orchestrator identity does not match restart target")
	}
	if d.sessionRestartRootedBootstrapRepair != nil {
		err = d.sessionRestartRootedBootstrapRepair(ctx, identity, plan.SessionID)
	} else {
		var prompt string
		prompt, err = d.rootedOrchestratorBootstrapPrompt(ctx, plan.ProjectID, identity.Scope)
		if err == nil {
			_, err = d.ensureRootedOrchestratorBootstrap(ctx, plan.ProjectID, identity.Scope, plan.SessionID, prompt, false)
		}
	}
	if err != nil {
		return fmt.Errorf("repair recovered rooted orchestrator bootstrap: %w", err)
	}
	store := d.sessionRuntimeStateStoreIfConfigured(plan.ProjectID)
	if store == nil {
		return errors.New("verify recovered rooted bootstrap acknowledgement: store unavailable")
	}
	acknowledgement, found, err := daemonstate.NewRootedBootstrapAcknowledgementAuthority(store).Get(ctx, identity)
	if err != nil {
		return fmt.Errorf("verify recovered rooted bootstrap acknowledgement: %w", err)
	}
	if !found || acknowledgement.Identity != identity || acknowledgement.SessionID != plan.SessionID || strings.TrimSpace(acknowledgement.PromptHash) == "" || strings.TrimSpace(acknowledgement.RuntimeNonce) == "" || acknowledgement.AcknowledgedAt.IsZero() {
		return errors.New("recovered rooted bootstrap acknowledgement is incomplete")
	}
	runtimeNonce, runtimeNonceFound, err := d.tmux.EnvironmentValue(ctx, plan.SessionID, rootedOrchestratorBootstrapNonceEnvironment)
	if err != nil {
		return fmt.Errorf("verify recovered rooted bootstrap runtime nonce: %w", err)
	}
	if !runtimeNonceFound || strings.TrimSpace(runtimeNonce) == "" || runtimeNonce != acknowledgement.RuntimeNonce {
		return errors.New("recovered rooted bootstrap acknowledgement does not match live runtime nonce")
	}
	return nil
}

func sameManagedRestartIdentity(a, b daemonstate.ManagedAgentIdentity) bool {
	return a.ProjectID == b.ProjectID && a.SessionID == b.SessionID && a.LogicalPaneID == b.LogicalPaneID &&
		sanitizeRuntimePaneID(a.TmuxPaneID) == sanitizeRuntimePaneID(b.TmuxPaneID) && a.PanePID == b.PanePID && a.AgentIncarnation == b.AgentIncarnation
}

func reportSessionRestartProgress(ctx context.Context, plan sessionRestartRecoveryPlan) error {
	if progress, ok := ctx.Value(sessionRestartBatchProgressKey{}).(*sessionRestartBatchProgress); ok && progress != nil && progress.plan != nil {
		copyPlan := plan
		progress.plan.Current = &copyPlan
		progress.plan.Stage = restartBatchLifecycleStage(plan.Stage)
		return reportSessionRestartBatchProgress(ctx, *progress.plan)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	progressCtx, cancel := context.WithTimeout(ctx, sessionRestartPreflightTimeout)
	defer cancel()
	return daemonops.ReportProgress(progressCtx, daemonops.Progress{Phase: "session.restart_all." + plan.Stage, Message: string(body), Current: 1, Total: 1, Unit: "pane"})
}

func withSessionRestartBatchProgress(ctx context.Context, plan *sessionRestartBatchPlan) context.Context {
	return context.WithValue(ctx, sessionRestartBatchProgressKey{}, &sessionRestartBatchProgress{plan: plan})
}

func reportSessionRestartBatchProgress(ctx context.Context, plan sessionRestartBatchPlan) error {
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	progressCtx, cancel := context.WithTimeout(ctx, sessionRestartPreflightTimeout)
	defer cancel()
	total := int64(len(plan.Targets))
	return daemonops.ReportProgress(progressCtx, daemonops.Progress{
		Phase: "session.restart_all.batch." + plan.Stage, Message: string(body),
		Current: int64(plan.Cursor), Total: total, Unit: "pane",
	})
}

func restartBatchLifecycleStage(stage string) string {
	switch stage {
	case "replace_ready", sessionRestartStageRootedInvalidateReady:
		return sessionRestartLifecycleTerminating
	case "observe":
		return sessionRestartLifecycleVerifying
	case "complete":
		return sessionRestartLifecycleCompleted
	default:
		return sessionRestartLifecycleRequested
	}
}

func decodeSessionRestartBatchPlan(record daemonops.Record) (sessionRestartBatchPlan, bool) {
	if record.Progress == nil || !strings.HasPrefix(record.Progress.Phase, "session.restart_all.batch.") {
		return sessionRestartBatchPlan{}, false
	}
	var plan sessionRestartBatchPlan
	if json.Unmarshal([]byte(record.Progress.Message), &plan) != nil || plan.Version != sessionRestartBatchPlanVersion || plan.ProjectID == "" {
		return sessionRestartBatchPlan{}, false
	}
	phaseStage := strings.TrimPrefix(record.Progress.Phase, "session.restart_all.batch.")
	if phaseStage == "" || phaseStage != plan.Stage || plan.Cursor < 0 || plan.Cursor > len(plan.Targets) || len(plan.Results) != plan.Cursor {
		return sessionRestartBatchPlan{}, false
	}
	if !validSessionRestartLifecycleStage(plan.Stage) || protocol.NormalizeProjectID(plan.Request.ProjectID.String()) != protocol.NormalizeProjectID(plan.ProjectID) {
		return sessionRestartBatchPlan{}, false
	}
	targetKeys := make(map[string]struct{}, len(plan.Targets))
	for index, target := range plan.Targets {
		key := strings.TrimSpace(target.ProjectID) + "\x00" + strings.TrimSpace(target.SessionID)
		if strings.TrimSpace(target.ProjectID) == "" || strings.TrimSpace(target.SessionID) == "" {
			return sessionRestartBatchPlan{}, false
		}
		if _, duplicate := targetKeys[key]; duplicate {
			return sessionRestartBatchPlan{}, false
		}
		targetKeys[key] = struct{}{}
		if index < plan.Cursor && !sessionRestartItemMatchesTarget(plan.Results[index], target) {
			return sessionRestartBatchPlan{}, false
		}
	}
	if plan.Cursor == len(plan.Targets) && plan.Current != nil {
		return sessionRestartBatchPlan{}, false
	}
	if plan.Current != nil && (plan.Cursor >= len(plan.Targets) || !sessionRestartRecoveryPlanMatchesTarget(*plan.Current, plan.Targets[plan.Cursor])) {
		return sessionRestartBatchPlan{}, false
	}
	seenRecoveries := make(map[int]struct{}, len(plan.Recoverable))
	for _, pending := range plan.Recoverable {
		if pending.Index < 0 || pending.Index >= plan.Cursor || pending.Index >= len(plan.Results) || !sessionRestartRecoveryPlanMatchesTarget(pending.Plan, plan.Targets[pending.Index]) {
			return sessionRestartBatchPlan{}, false
		}
		if _, duplicate := seenRecoveries[pending.Index]; duplicate {
			return sessionRestartBatchPlan{}, false
		}
		seenRecoveries[pending.Index] = struct{}{}
	}
	if recordProjectID := protocol.TrimProjectID(record.ProjectID); recordProjectID != "" && recordProjectID != protocol.NormalizeProjectID(plan.ProjectID) {
		return sessionRestartBatchPlan{}, false
	}
	return plan, true
}

func validSessionRestartLifecycleStage(stage string) bool {
	switch stage {
	case sessionRestartLifecycleRequested, sessionRestartLifecycleTerminating, sessionRestartLifecycleLaunching,
		sessionRestartLifecycleVerifying, sessionRestartLifecycleCompleted, sessionRestartLifecycleFailed,
		sessionRestartLifecycleCompensated:
		return true
	default:
		return false
	}
}

func sessionRestartRecoveryPlanMatchesTarget(plan sessionRestartRecoveryPlan, target sessionRestartAllTarget) bool {
	return strings.TrimSpace(plan.ProjectID) == strings.TrimSpace(target.ProjectID) &&
		strings.TrimSpace(plan.SessionID) == strings.TrimSpace(target.SessionID) &&
		strings.TrimSpace(plan.IssueID) == strings.TrimSpace(target.IssueID) &&
		strings.TrimSpace(plan.Activity) == strings.TrimSpace(target.Activity) &&
		strings.TrimSpace(plan.PlannedIncarnation) != "" && strings.TrimSpace(plan.Old.LogicalPaneID) != ""
}

func sessionRestartItemMatchesTarget(item protocol.SessionRestartAllItem, target sessionRestartAllTarget) bool {
	return strings.TrimSpace(item.ProjectID.String()) == strings.TrimSpace(target.ProjectID) &&
		strings.TrimSpace(item.SessionID.String()) == strings.TrimSpace(target.SessionID) &&
		strings.TrimSpace(item.IssueID.String()) == strings.TrimSpace(target.IssueID)
}
func managedRestartIdentityLive(sessionID string, identity daemonstate.ManagedAgentIdentity, panes []tmux.PaneInfo) bool {
	for _, pane := range panes {
		if pane.SessionName == sessionID && sanitizeRuntimePaneID(pane.PaneID) == sanitizeRuntimePaneID(identity.TmuxPaneID) && pane.PanePID == identity.PanePID {
			return true
		}
	}
	return false
}
func decodeSessionRestartRecoveryPlan(record daemonops.Record) (sessionRestartRecoveryPlan, bool) {
	if record.Progress == nil || !strings.HasPrefix(record.Progress.Phase, "session.restart_all.") {
		return sessionRestartRecoveryPlan{}, false
	}
	var plan sessionRestartRecoveryPlan
	if json.Unmarshal([]byte(record.Progress.Message), &plan) != nil || plan.ProjectID == "" || plan.SessionID == "" || plan.PlannedIncarnation == "" {
		return sessionRestartRecoveryPlan{}, false
	}
	phaseStage := strings.TrimPrefix(record.Progress.Phase, "session.restart_all.")
	if phaseStage == "" || phaseStage != strings.TrimSpace(plan.Stage) {
		return sessionRestartRecoveryPlan{}, false
	}
	if recordProjectID := protocol.TrimProjectID(record.ProjectID); recordProjectID != "" && recordProjectID != protocol.NormalizeProjectID(plan.ProjectID) {
		return sessionRestartRecoveryPlan{}, false
	}
	if plan.RootedIdentity != nil {
		identity, err := domain.NewOrchestratorIdentity(plan.RootedIdentity.ProjectID, plan.RootedIdentity.Scope)
		if err != nil || identity != *plan.RootedIdentity || identity.ProjectID != protocol.NormalizeProjectID(plan.ProjectID) ||
			identity.Scope.Kind != domain.OrchestrationScopeRooted || identity.Scope.RootIssueID.String() != strings.TrimSpace(plan.IssueID) {
			return sessionRestartRecoveryPlan{}, false
		}
	}
	return plan, true
}
func restartProtocolIdentity(i daemonstate.ManagedAgentIdentity) *protocol.ManagedAgentIdentity {
	return &protocol.ManagedAgentIdentity{LogicalPaneID: i.LogicalPaneID, TmuxPaneID: i.TmuxPaneID, PanePID: i.PanePID, AgentIncarnation: i.AgentIncarnation}
}
func restartSuccessOutcome(activity string) string {
	switch strings.ToLower(strings.TrimSpace(activity)) {
	case "idle":
		return "idle"
	case "waiting", "waiting_human", "waiting_ai":
		return "waiting"
	case "busy":
		return "busy_forced"
	default:
		return "unknown"
	}
}
func newRestartIncarnation() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate restart incarnation: %w", err)
	}
	return "restart-" + hex.EncodeToString(b[:]), nil
}
