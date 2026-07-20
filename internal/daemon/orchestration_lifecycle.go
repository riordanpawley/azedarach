package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// reconcileOrchestratorLifecycles is the hybrid project-completion invariant:
// issue and session projections are refreshed first, then session presence is
// compared with live tmux before durable lifecycle state is changed.
func (d *Daemon) reconcileOrchestratorLifecycles(ctx context.Context, projectID string, now time.Time) error {
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil || d.tmux == nil {
		return nil
	}
	orchestrationConfig := d.runtimeConfigForProject(projectID).Orchestration
	policy, err := domain.ParseOrchestratorLifecyclePolicy(orchestrationConfig.CompleteGrace, orchestrationConfig.WakeDebounce)
	if err != nil {
		return err
	}
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	leases, err := store.ListOrchestratorScopeLeases(ctx, projectID)
	if err != nil {
		return fmt.Errorf("refresh orchestrator lifecycle projection: %w", err)
	}
	for _, lease := range leases {
		if err := store.WithOrchestratorScopeTransition(ctx, lease.Identity, func(scopeCtx context.Context) error {
			return d.reconcileOrchestratorLifecycleScope(scopeCtx, authority, store, lease.Identity, projectID, now, policy)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) reconcileOrchestratorLifecycleScope(ctx context.Context, authority *daemonstate.OrchestratorLeaseAuthority, store *daemonstate.RuntimeStateStore, identity domain.OrchestratorIdentity, projectID string, now time.Time, policy domain.OrchestratorLifecyclePolicy) error {
	lease, found, err := authority.Get(ctx, identity)
	if err != nil || !found {
		return err
	}
	explicitlyStopped, err := orchestratorLeaseHasStoppedSessionIntent(ctx, store, lease)
	if err != nil {
		return err
	}
	if lease.Lifecycle == domain.OrchestratorPaused && explicitlyStopped {
		return nil
	}
	facts, latestChange, err := d.orchestratorLifecycleFacts(ctx, lease, projectID)
	if err != nil {
		return err
	}
	if lease.Lifecycle == domain.OrchestratorPaused {
		reason := orchestratorWakeReason(facts, latestChange, lease.UpdatedAt)
		if reason != "" {
			if _, _, err := authority.Wake(ctx, lease.Identity, now, reason, policy); err != nil {
				return err
			}
		}
	}
	evaluated, err := authority.Evaluate(ctx, lease.Identity, lease.SessionID, now, facts, policy)
	if err != nil {
		return err
	}
	if evaluated.Identity.Scope.Kind == domain.OrchestrationScopeProject && evaluated.Lifecycle == domain.OrchestratorWorking {
		if _, err := d.runProjectOrchestratorLoopStep(ctx, evaluated, now); err != nil {
			return fmt.Errorf("run project orchestrator loop: %w", err)
		}
	}
	return d.enforceOrchestratorContinuation(ctx, authority, evaluated, projectID, now, policy)
}

func orchestratorLeaseHasStoppedSessionIntent(ctx context.Context, store *daemonstate.RuntimeStateStore, lease daemonstate.OrchestratorScopeLease) (bool, error) {
	scopeID := orchestrationScopeID(lease.Identity.Scope)
	session, found, err := store.GetSessionIntent(ctx, lease.Identity.ProjectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, scopeID)
	if err != nil {
		return false, fmt.Errorf("refresh orchestrator stop intent for %s: %w", scopeID, err)
	}
	return found && session.ID == lease.SessionID && daemonstate.NormalizeSessionState(session.State) == daemonstate.SessionStateStopped, nil
}

func (d *Daemon) enforceOrchestratorContinuation(ctx context.Context, authority *daemonstate.OrchestratorLeaseAuthority, lease daemonstate.OrchestratorScopeLease, projectID string, now time.Time, policy domain.OrchestratorLifecyclePolicy) error {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	_, found, err := store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, lease.Identity.Scope.RootIssueID.String())
	if lease.Identity.Scope.Kind == domain.OrchestrationScopeProject {
		_, found, err = store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, "project")
	}
	if err != nil {
		return fmt.Errorf("refresh orchestrator activity: %w", err)
	}
	if !found {
		return nil
	}
	if lease.Identity.Scope.Kind == domain.OrchestrationScopeRooted {
		complete, err := d.taskCompleteCheck(ctx, projectID, lease.Identity.Scope.RootIssueID.String())
		if err != nil {
			return fmt.Errorf("evaluate parent orchestrator complete-check: %w", err)
		}
		if complete.Pass {
			if _, checkpointErr := checkpointRootedOrchestratorAction(ctx, store, lease, orchestratorActionableState{}, false, now); checkpointErr != nil {
				return checkpointErr
			}
			return nil
		}
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, projectID, protocol.OrchestrationSnapshotRequest{Scope: lease.Identity.Scope})
	if err != nil {
		return fmt.Errorf("build orchestrator continuation snapshot: %w", err)
	}
	action, actionable := orchestratorActionableContinuation(lease.Identity.Scope, snapshot)
	var reviewPrompt appconfig.ResolvedReviewPrompt
	if actionable && action.Kind == "review" {
		reviewPrompt, err = d.resolvedReviewPrompt(projectID)
		if err != nil {
			return err
		}
		action = bindReviewActionPrompt(action, reviewPrompt)
	}
	if lease.Identity.Scope.Kind == domain.OrchestrationScopeRooted {
		generation, checkpointErr := checkpointRootedOrchestratorAction(ctx, store, lease, action, actionable, now)
		if checkpointErr != nil {
			return checkpointErr
		}
		if actionable {
			action.Revision = fmt.Sprintf("%d-%s", generation, action.Revision)
		}
	}
	if !actionable {
		return nil
	}
	setOrchestratorContinuationProjection(&snapshot, lease.Identity.Scope, action)
	if action.Kind == "review" {
		snapshot.ContinuationContract = composeReviewWakePrompt(snapshot.ContinuationContract, reviewPrompt, reviewEpochManifest(snapshot.ReviewQueue))
	}
	reason := domain.OrchestratorWakeOpenWork
	if action.Kind == "review" {
		reason = domain.OrchestratorWakeReviewRequest
	}
	woken, _, err := authority.Wake(ctx, lease.Identity, now, reason, policy)
	if err != nil {
		return fmt.Errorf("record orchestrator continuation wake: %w", err)
	}
	target, found, err := d.currentAgentInputTarget(ctx, projectID, woken.SessionID)
	if err != nil {
		return fmt.Errorf("resolve orchestrator continuation target: %w", err)
	}
	if !found || d.agentInputService() == nil {
		return nil
	}
	result, err := d.agentInputService().Deliver(ctx, domain.AgentInputDeliveryRequest{
		ProjectID: projectID, SessionID: woken.SessionID, Target: target,
		Tool: d.runtimeConfigForProject(projectID).CLITool, Kind: domain.AgentInputMessageOrchestratorWake,
		Payload:   snapshot.ContinuationContract,
		IntentKey: orchestratorWakeIntentKey(woken.Identity.Scope, action.Revision, woken.SessionID, target),
	})
	if err != nil {
		return fmt.Errorf("deliver orchestrator continuation wake: %w", err)
	}
	if result.Outcome != domain.AgentInputDelivered {
		return nil // fail closed; durable wake is retried by reconciliation
	}
	return nil
}

func orchestratorWakeIntentKey(scope domain.OrchestrationScope, actionRevision, sessionID string, target domain.ManagedAgentRuntimeIdentity) string {
	targetDigest := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(target.AgentIncarnation)))
	return fmt.Sprintf("orchestrator-wake:%s:%s:%s:%x", scope.Kind, scope.RootIssueID, strings.TrimSpace(actionRevision), targetDigest[:12])
}

func checkpointRootedOrchestratorAction(ctx context.Context, store *daemonstate.RuntimeStateStore, lease daemonstate.OrchestratorScopeLease, action orchestratorActionableState, actionable bool, now time.Time) (int64, error) {
	desiredKey, desiredKind, desiredStatus := "", "observe", "idle"
	if actionable {
		desiredKey, desiredKind, desiredStatus = action.Revision, action.Kind, "actionable"
	}
	for attempts := 0; attempts < 3; attempts++ {
		checkpoint, _, err := store.GetOrchestratorLoopCheckpoint(ctx, lease.Identity)
		if err != nil {
			return 0, fmt.Errorf("refresh rooted orchestrator action checkpoint: %w", err)
		}
		if checkpoint.LastActionKey == desiredKey && checkpoint.LastActionKind == desiredKind && checkpoint.LastActionStatus == desiredStatus {
			return checkpoint.WatchCursor, nil
		}
		next := daemonstate.OrchestratorLoopCheckpoint{
			Identity: lease.Identity, WatchCursor: checkpoint.WatchCursor + 1,
			LastActionKey: desiredKey, LastActionKind: desiredKind, LastActionStatus: desiredStatus, UpdatedAt: now,
		}
		advanced, err := store.AdvanceOrchestratorLoopCheckpoint(ctx, next, checkpoint.WatchCursor)
		if err != nil {
			return 0, fmt.Errorf("advance rooted orchestrator action checkpoint: %w", err)
		}
		if advanced {
			return next.WatchCursor, nil
		}
	}
	return 0, fmt.Errorf("advance rooted orchestrator action checkpoint: concurrent transition did not converge")
}

func orchestratorActivityWakeRequired(activity string) bool {
	switch strings.ToLower(strings.TrimSpace(activity)) {
	case "idle", "done", "paused":
		return true
	default:
		return false
	}
}

func (d *Daemon) agentInputDeliveryEligible(ctx context.Context, request domain.AgentInputDeliveryRequest, now time.Time) (bool, error) {
	if request.Kind != domain.AgentInputMessageOrchestratorWake {
		return true, nil
	}
	const prefix = "orchestrator-wake:"
	key := strings.TrimPrefix(strings.TrimSpace(request.IntentKey), prefix)
	if key == request.IntentKey {
		return false, nil
	}
	parts := strings.SplitN(key, ":", 4)
	if len(parts) != 4 || strings.TrimSpace(parts[2]) == "" || strings.TrimSpace(parts[3]) == "" {
		return false, nil
	}
	lease, found, err := d.resolveOrchestratorSession(ctx, request.ProjectID, request.SessionID)
	if err != nil || !found {
		return false, err
	}
	if string(lease.Identity.Scope.Kind) != parts[0] || lease.Identity.Scope.RootIssueID.String() != parts[1] {
		return false, nil
	}
	if request.IntentKey != orchestratorWakeIntentKey(lease.Identity.Scope, parts[2], request.SessionID, request.Target) {
		return false, nil
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, request.ProjectID, protocol.OrchestrationSnapshotRequest{Scope: lease.Identity.Scope})
	if err != nil {
		return false, err
	}
	action, actionable := orchestratorActionableContinuation(lease.Identity.Scope, snapshot)
	if actionable && action.Kind == "review" {
		prompt, promptErr := d.resolvedReviewPrompt(request.ProjectID)
		if promptErr != nil {
			return false, promptErr
		}
		action = bindReviewActionPrompt(action, prompt)
	}
	if lease.Identity.Scope.Kind == domain.OrchestrationScopeRooted {
		store := d.sessionRuntimeStateStoreIfConfigured(request.ProjectID)
		if store == nil {
			return false, nil
		}
		if !actionable {
			_, checkpointErr := checkpointRootedOrchestratorAction(ctx, store, lease, action, false, now)
			return false, checkpointErr
		}
		checkpoint, found, checkpointErr := store.GetOrchestratorLoopCheckpoint(ctx, lease.Identity)
		if checkpointErr != nil || !found {
			return false, checkpointErr
		}
		if checkpoint.LastActionStatus != "actionable" || checkpoint.LastActionKey != action.Revision || checkpoint.LastActionKind != action.Kind {
			return false, nil
		}
		action.Revision = fmt.Sprintf("%d-%s", checkpoint.WatchCursor, action.Revision)
	}
	if !actionable {
		return false, nil
	}
	return action.Revision == parts[2], nil
}

func (d *Daemon) wakePausedOrchestratorsForRecovery(ctx context.Context, projectID string, now time.Time) error {
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	config := d.runtimeConfigForProject(projectID).Orchestration
	policy, err := domain.ParseOrchestratorLifecyclePolicy(config.CompleteGrace, config.WakeDebounce)
	if err != nil {
		return err
	}
	leases, err := store.ListOrchestratorScopeLeases(ctx, projectID)
	if err != nil {
		return err
	}
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	for _, lease := range leases {
		if err := store.WithOrchestratorScopeTransition(ctx, lease.Identity, func(scopeCtx context.Context) error {
			current, found, loadErr := authority.Get(scopeCtx, lease.Identity)
			if loadErr != nil || !found {
				return loadErr
			}
			// Rooted leases are resumed by the full continuation guard so a durable
			// wake record is paired with its scope-bound semantic continuation.
			if current.Identity.Scope.Kind == domain.OrchestrationScopeRooted || current.Lifecycle != domain.OrchestratorPaused {
				return nil
			}
			explicitlyStopped, stopErr := orchestratorLeaseHasStoppedSessionIntent(scopeCtx, store, current)
			if stopErr != nil || explicitlyStopped {
				return stopErr
			}
			_, _, wakeErr := authority.Wake(scopeCtx, current.Identity, now, domain.OrchestratorWakeRecovery, policy)
			return wakeErr
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) orchestratorLifecycleFacts(ctx context.Context, lease daemonstate.OrchestratorScopeLease, projectID string) (domain.OrchestratorLifecycleFacts, time.Time, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.OrchestratorLifecycleFacts{}, time.Time{}, fmt.Errorf("issue store unavailable")
	}
	tasks, _, err := d.projectReadSnapshot(projectID)
	if err != nil {
		return domain.OrchestratorLifecycleFacts{}, time.Time{}, fmt.Errorf("refresh orchestrator issue projection: %w", err)
	}
	if lease.Identity.Scope.Kind == domain.OrchestrationScopeRooted {
		tasks = materializedParentChildClosure(tasks, lease.Identity.Scope.RootIssueID.String())
	}
	facts := domain.OrchestratorLifecycleFacts{}
	latestChange := time.Time{}
	issueIDs := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		issueIDs[task.ID.String()] = struct{}{}
		if task.UpdatedAt.After(latestChange) {
			latestChange = task.UpdatedAt
		}
		if task.RuntimeUpdatedAt.After(latestChange) {
			latestChange = task.RuntimeUpdatedAt
		}
		switch task.State.Workflow() {
		case domain.IssueWorkflowOpen:
			facts.OpenIssues++
		case domain.IssueWorkflowActive:
			facts.ActiveIssues++
		}
		if task.State.Review() == domain.IssueReviewRequested {
			facts.ReviewRequests++
		}
		if task.Facts.WaitingHuman {
			facts.UnresolvedInteractions++
		}
	}
	interactions, err := issueClient.Interactions(ctx)
	if err != nil {
		return facts, latestChange, fmt.Errorf("refresh orchestrator interaction projection: %w", err)
	}
	for _, interaction := range interactions {
		if _, scoped := issueIDs[interaction.IssueID]; !scoped {
			continue
		}
		if interaction.UpdatedAt.After(latestChange) {
			latestChange = interaction.UpdatedAt
		}
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	projected, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return facts, latestChange, fmt.Errorf("refresh orchestrator session projection: %w", err)
	}
	live, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return facts, latestChange, fmt.Errorf("query orchestrator live sessions: %w", err)
	}
	facts.ActiveSessions = hybridActiveSessionCount(lease, projected, live, issueIDs)
	return facts, latestChange, nil
}

func hybridActiveSessionCount(lease daemonstate.OrchestratorScopeLease, projected []daemonstate.Session, live []string, issueIDs map[string]struct{}) int {
	liveSet := make(map[string]struct{}, len(live))
	for _, id := range live {
		liveSet[id] = struct{}{}
	}
	counted := make(map[string]struct{})
	for _, session := range projected {
		if session.ID == lease.SessionID {
			continue
		}
		if _, scoped := issueIDs[session.IssueID]; !scoped {
			continue
		}
		projectionActive := session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped
		_, runtimeActive := liveSet[session.ID]
		if projectionActive || runtimeActive {
			counted[session.ID] = struct{}{}
		}
	}
	// Project scope owns every project session. A live session missing from the
	// durable projection is divergence, not evidence of completion.
	if lease.Identity.Scope.Kind == domain.OrchestrationScopeProject {
		for sessionID := range liveSet {
			if sessionID != lease.SessionID {
				counted[sessionID] = struct{}{}
			}
		}
	}
	return len(counted)
}

func orchestratorWakeReason(facts domain.OrchestratorLifecycleFacts, latestChange, leaseUpdated time.Time) domain.OrchestratorWakeReason {
	if facts.ReviewRequests > 0 {
		return domain.OrchestratorWakeReviewRequest
	}
	if facts.OpenIssues > 0 || facts.ActiveIssues > 0 {
		return domain.OrchestratorWakeOpenWork
	}
	if !latestChange.IsZero() && latestChange.After(leaseUpdated) {
		return domain.OrchestratorWakeHumanAnswer
	}
	return domain.OrchestratorWakeReason(strings.TrimSpace(""))
}
