package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// reconcileOrchestratorLifecycles is the hybrid project-completion invariant:
// issue and session projections are refreshed first, then session presence is
// compared with live tmux before durable lifecycle state is changed.
func (d *Daemon) reconcileOrchestratorLifecycles(ctx context.Context, projectID string, now time.Time) error {
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
	}
	return nil
}

func (d *Daemon) wakePausedOrchestratorsForRecovery(ctx context.Context, projectID string, now time.Time) error {
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
		if lease.Lifecycle != domain.OrchestratorPaused {
			continue
		}
		if _, _, err := authority.Wake(ctx, lease.Identity, now, domain.OrchestratorWakeRecovery, policy); err != nil {
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
	var tasks []domain.Task
	var err error
	if lease.Identity.Scope.Kind == domain.OrchestrationScopeRooted {
		tasks, err = issueClient.ListParentChildSubtreeWithRuntime(ctx, projectID, lease.Identity.Scope.RootIssueID.String())
	} else {
		tasks, err = issueClient.ListWithRuntime(ctx, projectID)
	}
	if err != nil {
		return domain.OrchestratorLifecycleFacts{}, time.Time{}, fmt.Errorf("refresh orchestrator issue projection: %w", err)
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
