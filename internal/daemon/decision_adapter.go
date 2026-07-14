package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type issueDecisionService struct {
	daemon                            *Daemon
	beforeDecisionPropagationMutation func(command string)
}

const decisionPropagationMutationMaxAttempts = 8

func retryDecisionPropagationMutation[T any](ctx context.Context, service issueDecisionService, client *issues.Client, decisionID, command string, derive func() (issues.DecisionPropagationIntent, error), mutate func(issues.DecisionPropagationIntent) (T, error)) (T, error) {
	var zero T
	var priorIntents []issues.DecisionPropagationIntent
	for attempt := 1; attempt <= decisionPropagationMutationMaxAttempts; attempt++ {
		before, err := client.DecisionRevision(ctx, decisionID)
		if err != nil {
			return zero, err
		}
		beforeObservation, err := client.IssueObservationRevision(ctx)
		if err != nil {
			return zero, err
		}
		beforeDecisionAudit, err := client.DecisionAuditRevision(ctx)
		if err != nil {
			return zero, err
		}
		beforeSpecAudit, err := client.SpecAuditRevision(ctx)
		if err != nil {
			return zero, err
		}
		intent, err := derive()
		if err != nil {
			return zero, err
		}
		after, err := client.DecisionRevision(ctx, decisionID)
		if err != nil {
			return zero, err
		}
		afterDecisionAudit, err := client.DecisionAuditRevision(ctx)
		if err != nil {
			return zero, err
		}
		afterSpecAudit, err := client.SpecAuditRevision(ctx)
		if err != nil {
			return zero, err
		}
		afterObservation, err := client.IssueObservationRevision(ctx)
		if err != nil {
			return zero, err
		}
		if before != after || beforeDecisionAudit != afterDecisionAudit || beforeSpecAudit != afterSpecAudit || beforeObservation != afterObservation {
			priorIntents = append(priorIntents, intent)
			continue
		}
		intent = reconcileRetriedDecisionPropagationIntent(intent, priorIntents)
		intent.ExpectedRevision = after
		intent.ExpectedDecisionAuditRevision = afterDecisionAudit
		intent.ExpectedSpecAuditRevision = afterSpecAudit
		intent.ExpectedObservationRevision = afterObservation
		if service.beforeDecisionPropagationMutation != nil {
			service.beforeDecisionPropagationMutation(command)
		}
		result, err := mutate(intent)
		if errors.Is(err, issues.ErrDecisionPropagationRevisionChanged) {
			priorIntents = append(priorIntents, intent)
			continue
		}
		return result, err
	}
	return zero, fmt.Errorf("%w after %d attempts for decision %s", issues.ErrDecisionPropagationRevisionChanged, decisionPropagationMutationMaxAttempts, decisionID)
}

func reconcileRetriedDecisionPropagationIntent(current issues.DecisionPropagationIntent, prior []issues.DecisionPropagationIntent) issues.DecisionPropagationIntent {
	withdrawalCandidates := append([]string(nil), current.WithdrawnIssueIDs...)
	for _, stale := range prior {
		withdrawalCandidates = append(withdrawalCandidates, stale.WithdrawnIssueIDs...)
		withdrawalCandidates = append(withdrawalCandidates, stale.ChangedIssueIDs...)
	}
	current.WithdrawnIssueIDs = normalizeDecisionIssueIDs(decisionIssueDifference(withdrawalCandidates, current.ChangedIssueIDs))
	return current
}

func normalizeDecisionIssueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func (s issueDecisionService) client(ctx context.Context) (*issues.Client, error) {
	if s.daemon == nil {
		return nil, errors.New("issue store unavailable")
	}
	client := s.daemon.issueClientForProject(daemonProjectIDFromContext(ctx))
	if client == nil {
		return nil, errors.New("issue store unavailable")
	}
	return client, nil
}

func (s issueDecisionService) ListDecisions(ctx context.Context, req protocol.DecisionListRequestBody) (protocol.DecisionListResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionListResponseBody{}, err
	}
	filter := issues.DecisionFilter{
		LocalIDs: req.IDs,
		IssueID:  req.IssueID.String(),
		Query:    req.Query,
	}
	if req.RequirementID != "" {
		filter.RequirementID = req.RequirementID.String()
	}
	rows, err := c.ListDecisions(ctx, filter)
	if err != nil {
		return protocol.DecisionListResponseBody{}, err
	}
	out := make([]protocol.Decision, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapDecisionToProtocol(row))
	}
	return protocol.DecisionListResponseBody{Decisions: out}, nil
}

func (s issueDecisionService) GetDecision(ctx context.Context, req protocol.DecisionGetRequestBody) (protocol.DecisionGetResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionGetResponseBody{}, err
	}
	decision, err := c.GetDecision(ctx, req.ID)
	if err != nil {
		return protocol.DecisionGetResponseBody{}, err
	}
	out := protocol.DecisionGetResponseBody{Decision: mapDecisionToProtocol(decision)}
	if req.IncludeLinks {
		links, err := c.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: req.ID})
		if err != nil {
			return protocol.DecisionGetResponseBody{}, err
		}
		mapped := make([]protocol.DecisionLink, 0, len(links))
		for _, link := range links {
			mapped = append(mapped, mapDecisionLinkToProtocol(link))
		}
		out.Links = mapped
	}
	return out, nil
}

func (s issueDecisionService) RecordDecision(ctx context.Context, req protocol.DecisionRecordRequestBody) (protocol.DecisionRecordResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionRecordResponseBody{}, err
	}
	decision, err := c.RecordDecision(ctx, issues.RecordDecisionParams{
		Title:        req.Title,
		Rationale:    req.Rationale,
		Context:      req.Context,
		Consequences: req.Consequences,
	})
	if err != nil {
		return protocol.DecisionRecordResponseBody{}, err
	}
	return protocol.DecisionRecordResponseBody{Decision: mapDecisionToProtocol(decision)}, nil
}

func (s issueDecisionService) UpdateDecision(ctx context.Context, req protocol.DecisionUpdateRequestBody) (protocol.DecisionUpdateResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionUpdateResponseBody{}, err
	}
	params := issues.UpdateDecisionParams{
		Title:        req.Title,
		Rationale:    req.Rationale,
		Context:      req.Context,
		Consequences: req.Consequences,
	}
	projectID := daemonProjectIDFromContext(ctx)
	decision, err := retryDecisionPropagationMutation(ctx, s, c, req.ID, protocol.CommandDecisionUpdate, func() (issues.DecisionPropagationIntent, error) {
		affected, err := s.daemon.decisionAffectedIssues(ctx, projectID, req.ID)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		intent, err := s.daemon.newDecisionPropagationIntent(ctx, projectID, req.ID, affected, nil, protocol.CommandDecisionUpdate)
		if err == nil && req.Title != nil {
			intent.Payload["title"] = strings.TrimSpace(*req.Title)
		}
		return intent, err
	}, func(intent issues.DecisionPropagationIntent) (issues.Decision, error) {
		return c.UpdateDecisionWithPropagation(ctx, req.ID, params, intent)
	})
	if err != nil {
		return protocol.DecisionUpdateResponseBody{}, err
	}
	s.daemon.reconcileDecisionPropagationBestEffort(ctx, projectID)
	return protocol.DecisionUpdateResponseBody{Decision: mapDecisionToProtocol(decision)}, nil
}

func (s issueDecisionService) DeleteDecision(ctx context.Context, req protocol.DecisionDeleteRequestBody) (protocol.DecisionDeleteResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionDeleteResponseBody{}, err
	}
	projectID := daemonProjectIDFromContext(ctx)
	_, err = retryDecisionPropagationMutation(ctx, s, c, req.ID, protocol.CommandDecisionDelete, func() (issues.DecisionPropagationIntent, error) {
		affected, err := s.daemon.decisionAffectedIssues(ctx, projectID, req.ID)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		return s.daemon.newDecisionPropagationIntent(ctx, projectID, req.ID, nil, affected, protocol.CommandDecisionDelete)
	}, func(intent issues.DecisionPropagationIntent) (struct{}, error) {
		return struct{}{}, c.DeleteDecisionWithPropagation(ctx, req.ID, intent)
	})
	if err != nil {
		return protocol.DecisionDeleteResponseBody{}, err
	}
	s.daemon.reconcileDecisionPropagationBestEffort(ctx, projectID)
	return protocol.DecisionDeleteResponseBody{ID: req.ID, Deleted: true}, nil
}

func (s issueDecisionService) ListDecisionLinks(ctx context.Context, req protocol.DecisionLinkListRequestBody) (protocol.DecisionLinkListResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionLinkListResponseBody{}, err
	}
	filter := issues.DecisionLinkFilter{
		DecisionID: req.DecisionID,
		TargetID:   req.TargetID,
	}
	if req.TargetKind != "" {
		filter.TargetKind = issues.DecisionTargetKind(req.TargetKind)
	}
	links, err := c.ListDecisionLinks(ctx, filter)
	if err != nil {
		return protocol.DecisionLinkListResponseBody{}, err
	}
	out := make([]protocol.DecisionLink, 0, len(links))
	for _, link := range links {
		out = append(out, mapDecisionLinkToProtocol(link))
	}
	resp := protocol.DecisionLinkListResponseBody{Links: out}
	if req.IncludeDecisions && len(links) > 0 {
		seen := make(map[string]struct{}, len(links))
		ids := make([]string, 0, len(links))
		for _, link := range links {
			if _, ok := seen[link.DecisionID]; ok {
				continue
			}
			seen[link.DecisionID] = struct{}{}
			ids = append(ids, link.DecisionID)
		}
		decisions, err := c.ListDecisions(ctx, issues.DecisionFilter{LocalIDs: ids})
		if err != nil {
			return protocol.DecisionLinkListResponseBody{}, err
		}
		mapped := make([]protocol.Decision, 0, len(decisions))
		for _, d := range decisions {
			mapped = append(mapped, mapDecisionToProtocol(d))
		}
		resp.Decisions = mapped
	}
	return resp, nil
}

func (s issueDecisionService) AddDecisionLink(ctx context.Context, req protocol.DecisionLinkAddRequestBody) (protocol.DecisionLinkAddResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionLinkAddResponseBody{}, err
	}
	params := issues.AddDecisionLinkParams{
		DecisionID: req.DecisionID,
		TargetKind: issues.DecisionTargetKind(req.TargetKind),
		TargetID:   req.TargetID,
		Relation:   issues.DecisionRelation(req.Relation),
	}
	if req.Note != "" {
		params.Note = &req.Note
	}
	projectID := daemonProjectIDFromContext(ctx)
	link, err := retryDecisionPropagationMutation(ctx, s, c, req.DecisionID, protocol.CommandDecisionLinkAdd, func() (issues.DecisionPropagationIntent, error) {
		priorRelation, found, err := decisionLinkRelation(ctx, c, req.DecisionID, issues.DecisionTargetKind(req.TargetKind), req.TargetID)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		if !((found && materialDecisionRelation(priorRelation)) || materialDecisionRelation(issues.DecisionRelation(req.Relation))) {
			return issues.DecisionPropagationIntent{}, nil
		}
		before, err := s.daemon.decisionAffectedIssues(ctx, projectID, req.DecisionID)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		override := &decisionLinkOverride{DecisionID: req.DecisionID, TargetKind: issues.DecisionTargetKind(req.TargetKind), TargetID: req.TargetID, Relation: issues.DecisionRelation(req.Relation)}
		after, err := s.daemon.decisionAffectedIssuesWithLinkOverride(ctx, projectID, req.DecisionID, override)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		intent, err := s.daemon.newDecisionPropagationIntent(ctx, projectID, req.DecisionID, after, decisionIssueDifference(before, after), protocol.CommandDecisionLinkAdd)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		if issues.DecisionRelation(req.Relation) == issues.DecisionRelationRevises && issues.DecisionTargetKind(req.TargetKind) == issues.DecisionTargetDecision {
			intent.Payload["supersedes_decision_id"] = strings.TrimSpace(req.TargetID)
		}
		if issues.DecisionRelation(req.Relation) == issues.DecisionRelationGoverns && issues.DecisionTargetKind(req.TargetKind) == issues.DecisionTargetIssue {
			intent.Payload["source_issue_id"] = strings.TrimSpace(req.TargetID)
		}
		return intent, nil
	}, func(intent issues.DecisionPropagationIntent) (issues.DecisionLink, error) {
		return c.AddDecisionLinkWithPropagation(ctx, params, intent)
	})
	if err != nil {
		return protocol.DecisionLinkAddResponseBody{}, err
	}
	s.daemon.reconcileDecisionPropagationBestEffort(ctx, projectID)
	return protocol.DecisionLinkAddResponseBody{Link: mapDecisionLinkToProtocol(link)}, nil
}

func (s issueDecisionService) AcknowledgeDecision(ctx context.Context, req protocol.DecisionAcknowledgeRequestBody) (protocol.DecisionAcknowledgeResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionAcknowledgeResponseBody{}, err
	}
	issueID := strings.TrimSpace(req.IssueID.String())
	decisionID := strings.TrimSpace(req.DecisionID)
	disposition := strings.ToLower(strings.TrimSpace(req.Disposition))
	if !domain.ValidDecisionAcknowledgementDisposition(disposition) {
		return protocol.DecisionAcknowledgeResponseBody{}, fmt.Errorf("invalid decision acknowledgement disposition %q", disposition)
	}
	if disposition == domain.DecisionAcknowledgementCompatible && strings.TrimSpace(req.Note) == "" {
		return protocol.DecisionAcknowledgeResponseBody{}, fmt.Errorf("compatible acknowledgement requires --note evidence")
	}
	if err := s.daemon.reconcileDecisionPropagationOutbox(ctx, daemonProjectIDFromContext(ctx)); err != nil {
		return protocol.DecisionAcknowledgeResponseBody{}, fmt.Errorf("reconcile decision propagation before acknowledgement: %w", err)
	}
	events, err := c.ListIssueDecisionObservationEvents(ctx, issueID)
	if err != nil {
		return protocol.DecisionAcknowledgeResponseBody{}, err
	}
	var latestRevision int64
	for _, event := range events {
		if event.Type != domain.IssueEventDecisionChanged || strings.TrimSpace(fmt.Sprint(event.Payload["decision_id"])) != decisionID {
			continue
		}
		if revision := observationPayloadInt64(event.Payload["revision"]); revision > latestRevision {
			latestRevision = revision
		}
	}
	if latestRevision == 0 {
		return protocol.DecisionAcknowledgeResponseBody{}, fmt.Errorf("no material decision change %s is recorded for issue %s", decisionID, issueID)
	}
	if latestRevision != req.Revision {
		return protocol.DecisionAcknowledgeResponseBody{}, fmt.Errorf("stale decision acknowledgement: %s revision %d requested, latest is %d", decisionID, req.Revision, latestRevision)
	}
	pending := domain.ReducePendingDecisionChanges(events)
	pendingExact := false
	for _, change := range pending {
		if change.DecisionID == decisionID && change.Revision == req.Revision {
			pendingExact = true
			break
		}
	}
	if !pendingExact {
		for _, event := range events {
			if event.Type == domain.IssueEventDecisionAcknowledged && event.Source == "daemon-decision" && event.SourceCommand == protocol.CommandDecisionAcknowledge && strings.TrimSpace(fmt.Sprint(event.Payload["decision_id"])) == decisionID && observationPayloadInt64(event.Payload["revision"]) == req.Revision && domain.ValidDecisionAcknowledgementDisposition(strings.TrimSpace(fmt.Sprint(event.Payload["disposition"]))) {
				return protocol.DecisionAcknowledgeResponseBody{IssueID: req.IssueID, DecisionID: decisionID, Revision: req.Revision, Disposition: strings.TrimSpace(fmt.Sprint(event.Payload["disposition"])), EventID: event.ID}, nil
			}
		}
		return protocol.DecisionAcknowledgeResponseBody{}, fmt.Errorf("decision %s revision %d is not pending for issue %s", decisionID, req.Revision, issueID)
	}
	event, err := c.AcknowledgeDecisionPropagation(ctx, issueID, decisionID, req.Revision, disposition, req.Note)
	if err != nil {
		return protocol.DecisionAcknowledgeResponseBody{}, err
	}
	s.daemon.reconcileDecisionPropagationBestEffort(ctx, daemonProjectIDFromContext(ctx))
	acknowledgedDisposition := strings.TrimSpace(fmt.Sprint(event.Payload["disposition"]))
	return protocol.DecisionAcknowledgeResponseBody{IssueID: req.IssueID, DecisionID: decisionID, Revision: req.Revision, Disposition: acknowledgedDisposition, EventID: event.ID}, nil
}

func (s issueDecisionService) RemoveDecisionLink(ctx context.Context, req protocol.DecisionLinkRemoveRequestBody) (protocol.DecisionLinkRemoveResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionLinkRemoveResponseBody{}, err
	}
	projectID := daemonProjectIDFromContext(ctx)
	_, err = retryDecisionPropagationMutation(ctx, s, c, req.DecisionID, protocol.CommandDecisionLinkRemove, func() (issues.DecisionPropagationIntent, error) {
		priorRelation, found, err := decisionLinkRelation(ctx, c, req.DecisionID, issues.DecisionTargetKind(req.TargetKind), req.TargetID)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		if !found || !materialDecisionRelation(priorRelation) {
			return issues.DecisionPropagationIntent{}, nil
		}
		before, err := s.daemon.decisionAffectedIssues(ctx, projectID, req.DecisionID)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		override := &decisionLinkOverride{DecisionID: req.DecisionID, TargetKind: issues.DecisionTargetKind(req.TargetKind), TargetID: req.TargetID, Remove: true}
		after, err := s.daemon.decisionAffectedIssuesWithLinkOverride(ctx, projectID, req.DecisionID, override)
		if err != nil {
			return issues.DecisionPropagationIntent{}, err
		}
		return s.daemon.newDecisionPropagationIntent(ctx, projectID, req.DecisionID, after, decisionIssueDifference(before, after), protocol.CommandDecisionLinkRemove)
	}, func(intent issues.DecisionPropagationIntent) (struct{}, error) {
		return struct{}{}, c.RemoveDecisionLinkWithPropagation(ctx, req.DecisionID, issues.DecisionTargetKind(req.TargetKind), req.TargetID, intent)
	})
	if err != nil {
		return protocol.DecisionLinkRemoveResponseBody{}, err
	}
	s.daemon.reconcileDecisionPropagationBestEffort(ctx, projectID)
	return protocol.DecisionLinkRemoveResponseBody{
		DecisionID: req.DecisionID,
		TargetKind: req.TargetKind,
		TargetID:   req.TargetID,
		Removed:    true,
	}, nil
}

func mapDecisionToProtocol(d issues.Decision) protocol.Decision {
	return protocol.Decision{
		ID:           d.LocalID,
		Title:        d.Title,
		Rationale:    d.Rationale,
		Context:      d.Context,
		Consequences: d.Consequences,
		CreatedAt:    d.CreatedAt,
		UpdatedAt:    d.UpdatedAt,
	}
}

func decisionLinkRelation(ctx context.Context, client *issues.Client, decisionID string, targetKind issues.DecisionTargetKind, targetID string) (issues.DecisionRelation, bool, error) {
	links, err := client.ListDecisionLinks(ctx, issues.DecisionLinkFilter{DecisionID: decisionID, TargetKind: targetKind, TargetID: targetID})
	if err != nil {
		return "", false, err
	}
	for _, link := range links {
		if link.DecisionID == strings.TrimSpace(decisionID) && link.TargetKind == targetKind && link.TargetID == strings.TrimSpace(targetID) {
			return link.Relation, true, nil
		}
	}
	return "", false, nil
}

func materialDecisionRelation(relation issues.DecisionRelation) bool {
	return relation == issues.DecisionRelationGoverns || relation == issues.DecisionRelationRevises
}

func mapDecisionLinkToProtocol(l issues.DecisionLink) protocol.DecisionLink {
	out := protocol.DecisionLink{
		ID:         l.ID,
		DecisionID: l.DecisionID,
		TargetKind: protocol.DecisionTargetKind(l.TargetKind),
		TargetID:   l.TargetID,
		Relation:   protocol.DecisionRelation(l.Relation),
	}
	if l.Note != nil {
		out.Note = *l.Note
	}
	return out
}
