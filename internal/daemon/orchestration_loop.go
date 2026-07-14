package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const projectOrchestratorEventBatch = 5000

type projectOrchestratorLoopResult struct {
	Cursor       int64
	ActionKey    string
	ActionKind   string
	ActionStatus string
	Advanced     bool
}

// runProjectOrchestratorLoopStep composes the projection-backed orchestration
// authorities into one recoverable daemon step. Review judgment remains an
// explicit orchestrator decision. Runnable work may start alongside queued
// review so review inspection can be delegated without stalling the project.
func (d *Daemon) runProjectOrchestratorLoopStep(ctx context.Context, lease daemonstate.OrchestratorScopeLease, now time.Time) (projectOrchestratorLoopResult, error) {
	if lease.Identity.Scope.Kind != domain.OrchestrationScopeProject {
		return projectOrchestratorLoopResult{}, nil
	}
	if !usesProjectionSource(sourceForInvariant(daemonInvariantOrchestrationLoop)) {
		return projectOrchestratorLoopResult{}, fmt.Errorf("unsupported project loop invariant source: %s", sourceForInvariant(daemonInvariantOrchestrationLoop))
	}
	store := d.sessionRuntimeStateStoreIfConfigured(lease.Identity.ProjectID)
	issueClient := d.issueClientForProject(lease.Identity.ProjectID)
	if store == nil || issueClient == nil {
		return projectOrchestratorLoopResult{}, nil
	}
	checkpoint, _, err := store.GetOrchestratorLoopCheckpoint(ctx, lease.Identity)
	if err != nil {
		return projectOrchestratorLoopResult{}, fmt.Errorf("refresh project loop checkpoint: %w", err)
	}
	events, err := issueClient.ListProjectIssueObservationEvents(ctx, checkpoint.WatchCursor, projectOrchestratorEventBatch)
	if err != nil {
		return projectOrchestratorLoopResult{}, fmt.Errorf("read project loop watch cursor: %w", err)
	}
	nextCursor := checkpoint.WatchCursor
	if len(events) > 0 {
		nextCursor = events[len(events)-1].ID
	}
	actorID := "project-steward:" + lease.Identity.ProjectID
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(lease.Identity.ProjectID))
	if repoDir == "" {
		repoDir = strings.TrimSpace(d.resolveRepoDirForProject(lease.Identity.ProjectID))
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, lease.Identity.ProjectID, protocol.OrchestrationSnapshotRequest{
		Scope: lease.Identity.Scope, ActorID: actorID, RepoDir: repoDir,
	})
	if err != nil {
		return projectOrchestratorLoopResult{}, fmt.Errorf("refresh project loop snapshot: %w", err)
	}
	actionKind, actionStatus := "observe", "idle"
	actionKey, err := projectOrchestratorActionKey(lease.Identity.ProjectID, nextCursor, snapshot)
	if err != nil {
		return projectOrchestratorLoopResult{}, err
	}
	replaying := checkpoint.LastActionKind == "start" && checkpoint.LastActionStatus == "applying" && strings.TrimSpace(checkpoint.LastActionKey) != ""
	if replaying {
		// The prior daemon advanced the cursor before applying the action. Reuse
		// that exact key and leave newer events for the following loop step.
		nextCursor, actionKey, actionKind = checkpoint.WatchCursor, checkpoint.LastActionKey, "start"
	} else {
		actionKind, actionStatus = projectOrchestratorNextAction(snapshot)
	}
	if actionKind == "start" {
		if !replaying {
			pending := daemonstate.OrchestratorLoopCheckpoint{Identity: lease.Identity, WatchCursor: nextCursor, LastActionKey: actionKey, LastActionKind: actionKind, LastActionStatus: "applying", UpdatedAt: now}
			claimed, claimErr := store.AdvanceOrchestratorLoopCheckpoint(ctx, pending, checkpoint.WatchCursor)
			if claimErr != nil {
				return projectOrchestratorLoopResult{}, claimErr
			}
			if !claimed {
				return projectOrchestratorLoopResult{Cursor: nextCursor, ActionKey: actionKey, ActionKind: actionKind, ActionStatus: "contended", Advanced: false}, nil
			}
		}
		result, applyErr := d.orchestrationAuthority().Apply(ctx, lease.Identity.ProjectID, protocol.OrchestrationIntentRequest{Scope: lease.Identity.Scope, Kind: protocol.OrchestrationIntentStart, IntentKey: actionKey, ActorID: actorID, RepoDir: repoDir})
		if applyErr != nil {
			actionStatus = "error:" + applyErr.Error()
		} else {
			actionStatus = projectOrchestratorIntentStatus(result)
		}
	}
	next := daemonstate.OrchestratorLoopCheckpoint{
		Identity: lease.Identity, WatchCursor: nextCursor,
		LastActionKey: actionKey, LastActionKind: actionKind,
		LastActionStatus: actionStatus, UpdatedAt: now,
	}
	var advanced bool
	if actionKind == "start" {
		advanced, err = store.CompleteOrchestratorLoopAction(ctx, next)
	} else {
		advanced, err = store.AdvanceOrchestratorLoopCheckpoint(ctx, next, checkpoint.WatchCursor)
	}
	if err != nil {
		return projectOrchestratorLoopResult{}, err
	}
	if advanced && d.hub != nil {
		body, marshalErr := json.Marshal(protocol.OrchestrationLoopEventBody{Scope: lease.Identity.Scope, WatchCursor: nextCursor, ActionKey: actionKey, ActionKind: actionKind, ActionStatus: actionStatus, UpdatedAt: now})
		if marshalErr != nil {
			return projectOrchestratorLoopResult{}, fmt.Errorf("encode project loop event: %w", marshalErr)
		}
		d.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(lease.Identity.ProjectID), Revision: d.nextRevision(lease.Identity.ProjectID), Event: protocol.EventOrchestrationLoopUpdated, Kind: protocol.EnvelopeKindEvent, EmittedAt: now, Body: body})
	}
	return projectOrchestratorLoopResult{Cursor: nextCursor, ActionKey: actionKey, ActionKind: actionKind, ActionStatus: actionStatus, Advanced: advanced}, nil
}

func projectOrchestratorNextAction(snapshot protocol.OrchestrationSnapshot) (string, string) {
	reviewID := firstActionableReview(snapshot.ReviewQueue)
	startActionable := projectOrchestratorSnapshotActionable(snapshot)
	startCapacity := snapshot.Constraints.AgentCapacity <= 0 || snapshot.Capacity.TotalCountingCapacityCount < snapshot.Constraints.AgentCapacity
	if startActionable && (reviewID == "" || startCapacity) {
		return "start", "idle"
	}
	if reviewID != "" {
		return "review", "pending:" + reviewID
	}
	if startActionable {
		return "start", "idle"
	}
	return "observe", "idle"
}

func projectOrchestratorSnapshotActionable(snapshot protocol.OrchestrationSnapshot) bool {
	if !snapshot.Health.Healthy {
		return false
	}
	if len(snapshot.Runnable) > 0 {
		return true
	}
	for _, candidate := range snapshot.Candidates {
		if candidate.Classification == string(domain.IssuePremature) {
			if _, ok := domain.PrematureRouteGuidance(candidate.Executability); ok {
				return true
			}
		}
	}
	return false
}

func projectOrchestratorActionKey(projectID string, cursor int64, snapshot protocol.OrchestrationSnapshot) (string, error) {
	type candidateSignature struct {
		IssueID        string `json:"issue_id"`
		Eligible       bool   `json:"eligible"`
		Sufficient     bool   `json:"sufficient"`
		Classification string `json:"classification"`
		Reason         string `json:"reason"`
	}
	type reviewSignature struct {
		IssueID    string   `json:"issue_id"`
		Actionable bool     `json:"actionable"`
		Reasons    []string `json:"reasons,omitempty"`
	}
	candidates := make([]candidateSignature, 0, len(snapshot.Candidates))
	for _, candidate := range snapshot.Candidates {
		candidates = append(candidates, candidateSignature{IssueID: candidate.IssueID, Eligible: candidate.Eligible, Sufficient: candidate.Sufficient, Classification: candidate.Classification, Reason: candidate.Reason})
	}
	reviews := make([]reviewSignature, 0, len(snapshot.ReviewQueue))
	for _, review := range snapshot.ReviewQueue {
		reviews = append(reviews, reviewSignature{IssueID: review.IssueID, Actionable: review.Actionable, Reasons: review.Reasons})
	}
	signature := struct {
		Project    string                         `json:"project"`
		Cursor     int64                          `json:"cursor"`
		Runnable   []string                       `json:"runnable"`
		Candidates []candidateSignature           `json:"candidates"`
		Reviews    []reviewSignature              `json:"reviews"`
		Capacity   protocol.OrchestrationCapacity `json:"capacity"`
	}{projectID, cursor, snapshot.Runnable, candidates, reviews, snapshot.Capacity}
	encoded, err := json.Marshal(signature)
	if err != nil {
		return "", fmt.Errorf("encode project orchestrator action key: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "project-loop:" + hex.EncodeToString(digest[:12]), nil
}

func projectOrchestratorIntentStatus(result protocol.OrchestrationIntentResult) string {
	switch {
	case len(result.Failed) > 0:
		return fmt.Sprintf("partial:started=%d,routed=%d,failed=%d", len(result.Started), len(result.Routed), len(result.Failed))
	case len(result.Pending) > 0:
		return fmt.Sprintf("pending:started=%d,routed=%d,pending=%d", len(result.Started), len(result.Routed), len(result.Pending))
	default:
		return fmt.Sprintf("applied:started=%d,routed=%d", len(result.Started), len(result.Routed))
	}
}
