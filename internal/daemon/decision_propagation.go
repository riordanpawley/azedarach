package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type decisionPropagationScope struct {
	IssueIDs           []string
	SupersedesDecision string
}

// propagateDecisionChange publishes one durable, revision-aware fact to every
// live issue whose rooted work scope overlaps a governing decision link. The
// observation log owns replay; direct tmux delivery is best-effort notification
// only and is never the acknowledgement authority.
func (d *Daemon) propagateDecisionChange(ctx context.Context, projectID, decisionID, reason string) error {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return fmt.Errorf("propagate decision change: issue store unavailable")
	}
	scope, err := decisionPropagationScopes(ctx, client, decisionID, map[string]bool{})
	if err != nil {
		return fmt.Errorf("resolve decision propagation scope for %s: %w", decisionID, err)
	}
	if len(scope.IssueIDs) == 0 {
		return nil
	}
	revision, err := client.DecisionRevision(ctx, decisionID)
	if err != nil {
		return fmt.Errorf("resolve decision revision for %s: %w", decisionID, err)
	}
	title := decisionID
	if decision, getErr := client.GetDecision(ctx, decisionID); getErr == nil && strings.TrimSpace(decision.Title) != "" {
		title = strings.TrimSpace(decision.Title)
	}
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return fmt.Errorf("load decision propagation graph: %w", err)
	}
	affected := overlappingDecisionScopeIssues(tasks, scope.IssueIDs)
	for _, issueID := range affected {
		events, listErr := client.ListIssueDecisionObservationEvents(ctx, issueID)
		if listErr != nil {
			return fmt.Errorf("inspect prior decision propagation for %s: %w", issueID, listErr)
		}
		if decisionChangeRevisionExists(events, decisionID, revision) {
			continue
		}
		payload := map[string]any{
			"decision_id": decisionID, "revision": revision, "title": title,
			"reason": strings.TrimSpace(reason), "material": true,
			"source_issue_id": firstDecisionScopeIssue(scope.IssueIDs),
		}
		if scope.SupersedesDecision != "" {
			payload["supersedes_decision_id"] = scope.SupersedesDecision
		}
		event, appendErr := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventDecisionChanged, Source: "daemon-decision",
			SourceCommand: strings.TrimSpace(reason), Payload: payload,
		})
		if appendErr != nil {
			return fmt.Errorf("record decision change for %s: %w", issueID, appendErr)
		}
		d.deliverDecisionChange(ctx, projectID, issueID, event, title)
	}
	return nil
}

func (d *Daemon) decisionAffectedIssues(ctx context.Context, projectID, decisionID string) ([]string, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return nil, fmt.Errorf("decision affected issues: issue store unavailable")
	}
	scope, err := decisionPropagationScopes(ctx, client, decisionID, map[string]bool{})
	if err != nil {
		return nil, err
	}
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return overlappingDecisionScopeIssues(tasks, scope.IssueIDs), nil
}

func (d *Daemon) propagateDecisionWithdrawal(ctx context.Context, projectID, decisionID, reason string, issueIDs []string) error {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return fmt.Errorf("propagate decision withdrawal: issue store unavailable")
	}
	revision, err := client.DecisionRevision(ctx, decisionID)
	if err != nil {
		return err
	}
	for _, issueID := range uniqueNonEmpty(issueIDs) {
		events, listErr := client.ListIssueDecisionObservationEvents(ctx, issueID)
		if listErr != nil {
			return listErr
		}
		if decisionChangeRevisionExists(events, decisionID, revision) {
			continue
		}
		if _, appendErr := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventDecisionChanged, Source: "daemon-decision", SourceCommand: reason,
			Payload: map[string]any{"decision_id": decisionID, "revision": revision, "reason": reason, "material": true, "withdrawn": true},
		}); appendErr != nil {
			return appendErr
		}
	}
	return nil
}

func decisionIssueDifference(left, right []string) []string {
	remove := make(map[string]struct{}, len(right))
	for _, value := range right {
		remove[value] = struct{}{}
	}
	out := make([]string, 0)
	for _, value := range left {
		if _, found := remove[value]; !found {
			out = append(out, value)
		}
	}
	return out
}

func decisionPropagationScopes(ctx context.Context, client *issues.Client, decisionID string, visiting map[string]bool) (decisionPropagationScope, error) {
	decisionID = strings.TrimSpace(decisionID)
	if decisionID == "" || visiting[decisionID] {
		return decisionPropagationScope{}, nil
	}
	visiting[decisionID] = true
	defer delete(visiting, decisionID)
	links, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decisionID})
	if err != nil {
		return decisionPropagationScope{}, err
	}
	scope := decisionPropagationScope{}
	for _, link := range links {
		switch {
		case link.Relation == issues.DecisionRelationGoverns && link.TargetKind == issues.DecisionTargetIssue:
			scope.IssueIDs = append(scope.IssueIDs, link.TargetID)
		case link.Relation == issues.DecisionRelationGoverns && link.TargetKind == issues.DecisionTargetRequirement:
			requirement, reqErr := client.GetRequirement(ctx, link.TargetID)
			if reqErr != nil {
				return decisionPropagationScope{}, reqErr
			}
			if requirement.IssueID != nil {
				scope.IssueIDs = append(scope.IssueIDs, strings.TrimSpace(*requirement.IssueID))
			}
			specLinks, linkErr := client.ListSpecLinksByRequirementLocalID(ctx, link.TargetID)
			if linkErr != nil {
				return decisionPropagationScope{}, linkErr
			}
			for _, specLink := range specLinks {
				scope.IssueIDs = append(scope.IssueIDs, specLink.IssueID)
			}
		case link.Relation == issues.DecisionRelationRevises && link.TargetKind == issues.DecisionTargetDecision:
			scope.SupersedesDecision = link.TargetID
			inherited, inheritedErr := decisionPropagationScopes(ctx, client, link.TargetID, visiting)
			if inheritedErr != nil {
				return decisionPropagationScope{}, inheritedErr
			}
			scope.IssueIDs = append(scope.IssueIDs, inherited.IssueIDs...)
		}
	}
	scope.IssueIDs = uniqueNonEmpty(scope.IssueIDs)
	sort.Strings(scope.IssueIDs)
	return scope, nil
}

func overlappingDecisionScopeIssues(tasks []domain.Task, scopeIssueIDs []string) []string {
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	roots := make(map[string]struct{}, len(scopeIssueIDs))
	for _, issueID := range scopeIssueIDs {
		if _, ok := byID[issueID]; ok {
			roots[decisionScopeRoot(issueID, byID)] = struct{}{}
		}
	}
	result := make([]string, 0)
	for _, task := range tasks {
		if task.Status == domain.StatusDone {
			continue
		}
		if _, matches := roots[decisionScopeRoot(task.ID.String(), byID)]; matches {
			result = append(result, task.ID.String())
		}
	}
	sort.Strings(result)
	return result
}

func decisionScopeRoot(issueID string, byID map[string]domain.Task) string {
	current := issueID
	seen := map[string]struct{}{}
	for {
		if _, duplicate := seen[current]; duplicate {
			return current
		}
		seen[current] = struct{}{}
		task, ok := byID[current]
		if !ok || task.ParentID == nil || strings.TrimSpace(task.ParentID.String()) == "" {
			return current
		}
		current = task.ParentID.String()
	}
}

func decisionChangeRevisionExists(events []domain.IssueObservationEvent, decisionID string, revision int64) bool {
	for _, event := range events {
		if event.Type != domain.IssueEventDecisionChanged {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(event.Payload["decision_id"])) == decisionID && observationPayloadInt64(event.Payload["revision"]) == revision {
			return true
		}
	}
	return false
}

func observationPayloadInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func firstDecisionScopeIssue(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (d *Daemon) deliverDecisionChange(ctx context.Context, projectID, issueID string, event domain.IssueObservationEvent, title string) {
	message := fmt.Sprintf("Integration-affecting decision changed for issue %s: %s (%s), revision %d. Reconcile it now or prove compatibility before review handoff. Acknowledge the exact revision with `az decision acknowledge --issue %s --id %s --revision %d --disposition reconciled|compatible --note \"<evidence>\"`. Durable event: decision.changed #%d.", issueID, title, event.Payload["decision_id"], observationPayloadInt64(event.Payload["revision"]), issueID, event.Payload["decision_id"], observationPayloadInt64(event.Payload["revision"]), event.ID)
	body, err := json.Marshal(sessionCommandBody{ProjectID: projectID, SessionID: issueID, Message: message})
	if err != nil {
		return
	}
	req := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID(fmt.Sprintf("decision-change-%d-%s", event.ID, issueID)), Kind: protocol.EnvelopeKindCommand, Command: "session.message", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID), ClientActor: "daemon-decision"}, Body: body}
	resp, sendErr := d.handleSessionMessage(ctx, req)
	if sendErr == nil && resp.OK {
		return
	}
	if d.cfg.Logger != nil {
		failure := responseErrorMessage(resp)
		if sendErr != nil {
			failure = sendErr.Error()
		}
		d.cfg.Logger.Debug("decision change stored without live worker delivery", "project_id", projectID, "issue_id", issueID, "event_id", event.ID, "error", failure)
	}
}
