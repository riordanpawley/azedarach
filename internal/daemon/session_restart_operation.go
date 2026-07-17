package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
)

type sessionRestartExecution struct {
	done chan struct{}
	item protocol.SessionRestartAllItem
}

type sessionRestartRecoveryPlan struct {
	ProjectID          string                           `json:"project_id"`
	SessionID          string                           `json:"session_id"`
	IssueID            string                           `json:"issue_id,omitempty"`
	Activity           string                           `json:"activity"`
	Old                daemonstate.ManagedAgentIdentity `json:"old"`
	PlannedIncarnation string                           `json:"planned_incarnation"`
	Stage              string                           `json:"stage"`
}

type restartStageResult[T any] struct {
	value T
	err   error
}

func restartStage(name, status, message string, timeout time.Duration) protocol.SessionRestartStage {
	return protocol.SessionRestartStage{Name: name, Status: status, Message: message, TimeoutMS: timeout.Milliseconds()}
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
	plan := sessionRestartRecoveryPlan{ProjectID: target.ProjectID, SessionID: target.SessionID, IssueID: target.IssueID, Activity: target.Activity, Old: old, PlannedIncarnation: incarnation, Stage: "prepare"}
	if err := reportSessionRestartProgress(ctx, plan); err != nil {
		appendRestartStageFailure(&item, "persist_prepare", sessionRestartPreflightTimeout, err, false)
		return item
	}
	type preparedRestart struct {
		artifact sessionLaunchArtifact
		worktree string
	}
	launchPrompt := sessionRestartContinuePrompt
	if rootedIdentity != nil {
		launchPrompt = ""
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
	if rootedIdentity != nil {
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
	_, err, timedOut = runRestartStage(ctx, sessionRestartReplaceTimeout, func(stageCtx context.Context) (struct{}, error) {
		return struct{}{}, d.tmux.RespawnPane(stageCtx, paneTarget, prepared.worktree, prepared.artifact.Command)
	})
	if err != nil {
		prepared.artifact.remove()
		appendRestartStageFailure(&item, "replace", sessionRestartReplaceTimeout, err, timedOut)
		return item
	}
	item.Stages = append(item.Stages, restartStage("replace", "complete", "old pane process terminated and replacement launched", sessionRestartReplaceTimeout))
	plan.Stage = "observe"
	if err := reportSessionRestartProgress(ctx, plan); err != nil {
		appendRestartStageFailure(&item, "persist_observe", sessionRestartPreflightTimeout, err, false)
		return item
	}

	observeCtx, cancel := context.WithTimeout(ctx, sessionRestartObservationTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-observeCtx.Done():
			appendRestartStageFailure(&item, "observe", sessionRestartObservationTimeout, observeCtx.Err(), errors.Is(observeCtx.Err(), context.DeadlineExceeded))
			return item
		case <-ticker.C:
			current, found, loadErr := store.GetManagedAgentIdentity(observeCtx, target.ProjectID, target.SessionID, old.LogicalPaneID)
			if loadErr != nil {
				appendRestartStageFailure(&item, "observe", sessionRestartObservationTimeout, loadErr, false)
				return item
			}
			liveReplacement := false
			if found {
				if livePanes, liveErr := d.tmux.ListPaneInfos(observeCtx); liveErr == nil {
					liveReplacement = managedRestartIdentityLive(target.SessionID, current, livePanes)
				}
			}
			if found && liveReplacement && current.AgentIncarnation == incarnation && current.PanePID != old.PanePID {
				item.NewIdentity = restartProtocolIdentity(current)
				if rootedIdentity != nil {
					prompt, promptErr := d.rootedOrchestratorBootstrapPrompt(ctx, target.ProjectID, rootedIdentity.Scope)
					if promptErr == nil {
						_, promptErr = d.ensureRootedOrchestratorBootstrap(ctx, target.ProjectID, rootedIdentity.Scope, target.SessionID, prompt, false)
					}
					if promptErr != nil {
						appendRestartStageFailure(&item, "rooted_bootstrap", sessionRestartObservationTimeout, promptErr, errors.Is(promptErr, context.DeadlineExceeded))
						return item
					}
					item.Stages = append(item.Stages, restartStage("rooted_bootstrap", "complete", "replacement rooted orchestrator acknowledged bootstrap", sessionRestartObservationTimeout))
				}
				item.Restarted = true
				item.Outcome = restartSuccessOutcome(target.Activity)
				item.Stages = append(item.Stages, restartStage("observe", "complete", "distinct pane process and hook incarnation acknowledged", sessionRestartObservationTimeout))
				plan.Stage = "complete"
				if err := reportSessionRestartProgress(ctx, plan); err != nil {
					appendRestartStageFailure(&item, "persist_complete", sessionRestartPreflightTimeout, err, errors.Is(err, context.DeadlineExceeded))
					return item
				}
				item.Stages = append(item.Stages, restartStage("persist_complete", "complete", "restart completion checkpoint persisted", sessionRestartPreflightTimeout))
				return item
			}
		}
	}
}

func reportSessionRestartProgress(ctx context.Context, plan sessionRestartRecoveryPlan) error {
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	progressCtx, cancel := context.WithTimeout(ctx, sessionRestartPreflightTimeout)
	defer cancel()
	return daemonops.ReportProgress(progressCtx, daemonops.Progress{Phase: "session.restart_all." + plan.Stage, Message: string(body), Current: 1, Total: 1, Unit: "pane"})
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
