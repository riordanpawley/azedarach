package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const workerMailWakeIntentPrefix = "worker-mail-wake:"

func actionableWorkerMailType(eventType string) bool {
	projected, ok := projectedMailboxEventType(domain.IssueObservationEventType(eventType))
	if !ok {
		return false
	}
	switch projected {
	case "orchestrator-message", "worker-guidance", "review-finding":
		return true
	default:
		return false
	}
}

func (d *Daemon) reconcileWorkerMailWake(ctx context.Context, projectID string, event daemonMailEvent) error {
	if !actionableWorkerMailType(event.Type) {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	issueID, err := naming.ParseIssueID(strings.TrimSpace(event.IssueID))
	if err != nil {
		return fmt.Errorf("invalid worker mail issue: %w", err)
	}
	if strings.TrimSpace(event.To) != "" && !naming.IssueIDsEqual(event.To, issueID.String()) {
		return fmt.Errorf("worker mail target %s does not match issue %s", event.To, issueID)
	}
	sessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope(projectID), issueID).String()
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return errors.New("runtime state store unavailable")
	}
	session, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		return fmt.Errorf("refresh worker mail session: %w", err)
	}
	if !found || !workerMailSessionMatchesIssue(session, issueID.String()) {
		return fmt.Errorf("active worker session unavailable for issue %s", issueID)
	}
	target, found, err := d.currentAgentInputTarget(ctx, projectID, sessionID)
	if err != nil {
		return fmt.Errorf("resolve worker mail target: %w", err)
	}
	if !found || d.agentInputService() == nil {
		return errors.New("managed worker identity unavailable")
	}
	observationID, err := workerMailObservationID(event)
	if err != nil {
		return err
	}
	result, err := d.agentInputService().Deliver(ctx, domain.AgentInputDeliveryRequest{
		ProjectID: projectID,
		SessionID: sessionID,
		Target:    target,
		Tool:      d.runtimeConfigForProject(projectID).CLITool,
		Kind:      domain.AgentInputMessageWorkerMailWake,
		Payload:   formatWorkerMailWake(event),
		IntentKey: workerMailWakeIntentPrefix + strconv.FormatInt(observationID, 10),
	})
	if err != nil {
		return fmt.Errorf("deliver worker mail wake: %w", err)
	}
	if result.Outcome != domain.AgentInputDelivered {
		return fmt.Errorf("%s: %s", result.Outcome, result.Reason)
	}
	return nil
}

func workerMailObservationID(event daemonMailEvent) (int64, error) {
	if event.ObservationID <= 0 {
		return 0, errors.New("durable worker mail observation identity unavailable")
	}
	return event.ObservationID, nil
}

func formatWorkerMailWake(event daemonMailEvent) string {
	return fmt.Sprintf("Orchestrator %s for issue %s under root %s:\n\n%s\n\nContinue from the current state. Process mailbox sequence %d exactly once, then continue the assigned issue work and report worker progress, blockage, or integration readiness as appropriate.",
		strings.TrimSpace(event.Type), strings.TrimSpace(event.IssueID), strings.TrimSpace(event.ParentIssue), strings.TrimSpace(event.Body), event.Seq)
}

func (d *Daemon) reconcilePendingWorkerMailWakes(ctx context.Context, projectID string) error {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return nil
	}
	observations, err := client.ListUnmaterializedWorkerMailWakeEvents(ctx, projectID, 5000)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		event, decodeErr := daemonMailEventFromObservation(observation)
		if decodeErr != nil {
			return fmt.Errorf("decode worker mail observation %d: %w", observation.ID, decodeErr)
		}
		if reconcileErr := d.reconcileWorkerMailWake(ctx, projectID, event); reconcileErr != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("worker mail wake remains undelivered", "project_id", projectID, "observation_id", observation.ID, "error", reconcileErr)
			}
		}
	}
	return nil
}

func (d *Daemon) workerMailWakeDeliveryEligible(ctx context.Context, request domain.AgentInputDeliveryRequest) (bool, error) {
	rawID := strings.TrimPrefix(strings.TrimSpace(request.IntentKey), workerMailWakeIntentPrefix)
	if rawID == request.IntentKey {
		return false, nil
	}
	observationID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || observationID <= 0 {
		return false, nil
	}
	client := d.issueClientForProject(request.ProjectID)
	if client == nil {
		return false, nil
	}
	observations, err := client.GetProjectIssueObservationEventsByIDs(ctx, []int64{observationID})
	if err != nil || len(observations) != 1 {
		return false, err
	}
	event, err := daemonMailEventFromObservation(observations[0])
	if err != nil || !actionableWorkerMailType(event.Type) {
		return false, err
	}
	issueID, err := naming.ParseIssueID(event.IssueID)
	if err != nil {
		return false, nil
	}
	expectedSession := naming.CanonicalSessionIDForIssue(d.sessionNamingScope(request.ProjectID), issueID).String()
	if request.SessionID != expectedSession ||
		request.Payload != formatWorkerMailWake(event) ||
		(strings.TrimSpace(event.To) != "" && !naming.IssueIDsEqual(event.To, issueID.String())) {
		return false, nil
	}
	store := d.sessionRuntimeStateStoreIfConfigured(request.ProjectID)
	if store == nil {
		return false, nil
	}
	session, found, err := store.GetSessionState(ctx, request.ProjectID, request.SessionID)
	if err != nil || !found {
		return false, err
	}
	return workerMailSessionMatchesIssue(session, issueID.String()), nil
}

func workerMailSessionMatchesIssue(session daemonstate.Session, issueID string) bool {
	return session.Role == daemonstate.SessionRoleWorker &&
		session.ScopeKind == daemonstate.SessionScopeIssue &&
		naming.IssueIDsEqual(session.ScopeID, issueID) &&
		naming.IssueIDsEqual(session.IssueID, issueID)
}
