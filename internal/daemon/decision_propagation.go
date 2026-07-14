package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type decisionPropagationScope struct {
	IssueIDs           []string
	SupersedesDecision string
}

func (d *Daemon) newDecisionPropagationIntent(ctx context.Context, projectID, decisionID string, changed, withdrawn []string, sourceCommand string) (issues.DecisionPropagationIntent, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return issues.DecisionPropagationIntent{}, fmt.Errorf("decision propagation intent: issue store unavailable")
	}
	payload := map[string]any{"reason": strings.TrimSpace(sourceCommand)}
	if decision, err := client.GetDecision(ctx, decisionID); err == nil {
		payload["title"] = strings.TrimSpace(decision.Title)
	}
	scope, err := decisionPropagationScopes(ctx, client, decisionID, map[string]bool{})
	if err != nil {
		return issues.DecisionPropagationIntent{}, err
	}
	if scope.SupersedesDecision != "" {
		payload["supersedes_decision_id"] = scope.SupersedesDecision
	}
	if sourceIssue := firstDecisionScopeIssue(scope.IssueIDs); sourceIssue != "" {
		payload["source_issue_id"] = sourceIssue
	}
	return issues.DecisionPropagationIntent{
		ChangedIssueIDs: changed, WithdrawnIssueIDs: withdrawn,
		SourceCommand: sourceCommand, Payload: payload,
	}, nil
}

func (d *Daemon) reconcileDecisionPropagationBestEffort(ctx context.Context, projectID string) {
	if err := d.reconcileDecisionPropagationOutbox(ctx, projectID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("decision propagation outbox remains pending", "project_id", projectID, "error", err)
	}
}

func (d *Daemon) reconcileAllDecisionPropagationOutboxes(ctx context.Context) {
	d.issueClientsMu.Lock()
	openClients := make(map[string]*issues.Client, len(d.issueClientsByProject))
	for projectID, client := range d.issueClientsByProject {
		openClients[projectID] = client
	}
	d.issueClientsMu.Unlock()
	openProjectIDs := make([]string, 0, len(openClients))
	for projectID := range openClients {
		openProjectIDs = append(openProjectIDs, projectID)
	}
	sort.Strings(openProjectIDs)
	seen := map[*issues.Client]struct{}{}
	// Clients already opened by an authoritative command are safe to reconcile
	// directly. They must not become stranded merely because a registry alias
	// was later renamed or removed.
	for _, projectID := range openProjectIDs {
		client := openClients[projectID]
		if client == nil {
			continue
		}
		if _, duplicate := seen[client]; duplicate {
			continue
		}
		seen[client] = struct{}{}
		d.reconcileDecisionPropagationBestEffort(ctx, projectID)
	}

	// A fresh global daemon has only opened its root project's store. Discover
	// every registered project on each pass so a restart cannot strand another
	// project's durable outbox until an unrelated command happens to route to it.
	projectIDs := make(map[string]struct{})
	if !d.cfg.ScopedRuntime {
		registry, err := appconfig.LoadProjectsRegistry()
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("decision propagation project discovery failed", "error", err)
			}
		} else if registry != nil {
			for _, project := range registry.Projects {
				projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
				if projectID != "" {
					projectIDs[projectID] = struct{}{}
				}
			}
		}
	}
	orderedProjectIDs := make([]string, 0, len(projectIDs))
	for projectID := range projectIDs {
		orderedProjectIDs = append(orderedProjectIDs, projectID)
	}
	sort.Strings(orderedProjectIDs)
	for _, projectID := range orderedProjectIDs {
		client, err := d.issueClientForExistingProjectStore(projectID)
		if err != nil || client == nil {
			if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Debug("decision propagation project unavailable", "project_id", projectID, "error", err)
			}
			continue
		}
		if _, duplicate := seen[client]; duplicate {
			continue
		}
		seen[client] = struct{}{}
		d.reconcileDecisionPropagationBestEffort(ctx, projectID)
	}
}

// reconcileDecisionPropagationOutbox materializes the atomic decision outbox
// into per-issue authority events. Entries remain active, and therefore retry
// live delivery, until the exact revision is authoritatively acknowledged or
// becomes superseded/withdrawn.
func (d *Daemon) reconcileDecisionPropagationOutbox(ctx context.Context, projectID string) error {
	if sourceForInvariant(daemonInvariantDecisionPropagation) != daemonInvariantSourceHybrid {
		return fmt.Errorf("unsupported decision propagation invariant source: %s", sourceForInvariant(daemonInvariantDecisionPropagation))
	}
	d.decisionPropagationMu.Lock()
	defer d.decisionPropagationMu.Unlock()
	client := d.issueClientForProject(projectID)
	if client == nil {
		return fmt.Errorf("reconcile decision propagation: issue store unavailable")
	}
	entries, err := client.ListActiveDecisionPropagationOutbox(ctx, 0)
	if err != nil {
		return fmt.Errorf("list decision propagation outbox: %w", err)
	}
	materialized := make(map[int64]domain.IssueObservationEvent, len(entries))
	for _, entry := range entries {
		changeEvent, err := client.MaterializeDecisionPropagationOutbox(ctx, entry)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				if retireErr := client.RetireDecisionPropagationOutbox(ctx, entry.ID); retireErr != nil {
					return retireErr
				}
				continue
			}
			return fmt.Errorf("materialize decision propagation for %s: %w", entry.IssueID, err)
		}
		materialized[entry.ID] = changeEvent
	}
	for _, entry := range entries {
		changeEvent, materializedOK := materialized[entry.ID]
		if !materializedOK {
			continue
		}
		events, listErr := client.ListIssueDecisionObservationEvents(ctx, entry.IssueID)
		if listErr != nil {
			return fmt.Errorf("inspect decision propagation checkpoint for %s: %w", entry.IssueID, listErr)
		}
		if entry.EventKind == issues.DecisionPropagationWithdrawn {
			if err := client.RetireDecisionPropagationOutbox(ctx, entry.ID); err != nil {
				return err
			}
			continue
		}
		pending := domain.ReducePendingDecisionChanges(events)
		if !pendingDecisionRevisionExists(pending, entry.DecisionID, entry.Revision) {
			if decisionPropagationRevisionTerminal(events, entry.DecisionID, entry.Revision) {
				if err := client.RetireDecisionPropagationOutbox(ctx, entry.ID); err != nil {
					return err
				}
			}
			continue
		}
		title := strings.TrimSpace(fmt.Sprint(entry.Payload["title"]))
		if title == "" {
			title = entry.DecisionID
		}
		d.deliverDecisionChange(ctx, projectID, entry.IssueID, changeEvent, title)
	}
	return nil
}

func decisionPropagationRevisionTerminal(events []domain.IssueObservationEvent, decisionID string, revision int64) bool {
	for _, event := range events {
		if strings.TrimSpace(fmt.Sprint(event.Payload["decision_id"])) != decisionID {
			continue
		}
		eventRevision := observationPayloadInt64(event.Payload["revision"])
		switch event.Type {
		case domain.IssueEventDecisionChanged:
			if eventRevision > revision || (eventRevision >= revision && event.Payload["withdrawn"] == true) {
				return true
			}
		case domain.IssueEventDecisionAcknowledged:
			if eventRevision == revision && event.Source == "daemon-decision" && event.SourceCommand == protocol.CommandDecisionAcknowledge && domain.ValidDecisionAcknowledgementDisposition(strings.TrimSpace(fmt.Sprint(event.Payload["disposition"]))) {
				return true
			}
		}
	}
	return false
}

func (d *Daemon) startDecisionPropagationReconcileWorker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.reconcileAllDecisionPropagationOutboxes(ctx)
			}
		}
	}()
}

func (d *Daemon) decisionAffectedIssues(ctx context.Context, projectID, decisionID string) ([]string, error) {
	return d.decisionAffectedIssuesWithLinkOverride(ctx, projectID, decisionID, nil)
}

type decisionLinkOverride struct {
	DecisionID string
	TargetKind issues.DecisionTargetKind
	TargetID   string
	Relation   issues.DecisionRelation
	Remove     bool
}

func (d *Daemon) decisionAffectedIssuesWithLinkOverride(ctx context.Context, projectID, decisionID string, override *decisionLinkOverride) ([]string, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return nil, fmt.Errorf("decision affected issues: issue store unavailable")
	}
	scope, err := decisionPropagationScopesWithOverride(ctx, client, decisionID, override, map[string]bool{})
	if err != nil {
		return nil, err
	}
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return overlappingDecisionScopeIssues(tasks, scope.IssueIDs), nil
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
	return decisionPropagationScopesWithOverride(ctx, client, decisionID, nil, visiting)
}

func decisionPropagationScopesWithOverride(ctx context.Context, client *issues.Client, decisionID string, override *decisionLinkOverride, visiting map[string]bool) (decisionPropagationScope, error) {
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
	overrideMatched := false
	for _, link := range links {
		if override != nil && link.DecisionID == override.DecisionID && link.TargetKind == override.TargetKind && link.TargetID == override.TargetID {
			overrideMatched = true
			if override.Remove {
				continue
			}
			link.Relation = override.Relation
		}
		if err := accumulateDecisionPropagationLink(ctx, client, link, override, visiting, &scope); err != nil {
			return decisionPropagationScope{}, err
		}
	}
	if override != nil && override.DecisionID == decisionID && !override.Remove && !overrideMatched {
		link := issues.DecisionLink{DecisionID: decisionID, TargetKind: override.TargetKind, TargetID: override.TargetID, Relation: override.Relation}
		if err := accumulateDecisionPropagationLink(ctx, client, link, override, visiting, &scope); err != nil {
			return decisionPropagationScope{}, err
		}
	}
	scope.IssueIDs = uniqueNonEmpty(scope.IssueIDs)
	sort.Strings(scope.IssueIDs)
	return scope, nil
}

func accumulateDecisionPropagationLink(ctx context.Context, client *issues.Client, link issues.DecisionLink, override *decisionLinkOverride, visiting map[string]bool, scope *decisionPropagationScope) error {
	switch {
	case link.Relation == issues.DecisionRelationGoverns && link.TargetKind == issues.DecisionTargetIssue:
		scope.IssueIDs = append(scope.IssueIDs, link.TargetID)
	case link.Relation == issues.DecisionRelationGoverns && link.TargetKind == issues.DecisionTargetRequirement:
		requirement, reqErr := client.GetRequirement(ctx, link.TargetID)
		if reqErr != nil {
			return reqErr
		}
		if requirement.IssueID != nil {
			scope.IssueIDs = append(scope.IssueIDs, strings.TrimSpace(*requirement.IssueID))
		}
		specLinks, linkErr := client.ListSpecLinksByRequirementLocalID(ctx, link.TargetID)
		if linkErr != nil {
			return linkErr
		}
		for _, specLink := range specLinks {
			scope.IssueIDs = append(scope.IssueIDs, specLink.IssueID)
		}
	case link.Relation == issues.DecisionRelationRevises && link.TargetKind == issues.DecisionTargetDecision:
		scope.SupersedesDecision = link.TargetID
		inherited, inheritedErr := decisionPropagationScopesWithOverride(ctx, client, link.TargetID, override, visiting)
		if inheritedErr != nil {
			return inheritedErr
		}
		scope.IssueIDs = append(scope.IssueIDs, inherited.IssueIDs...)
	}
	return nil
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
		if task.Status == domain.StatusDone || task.State.Visibility != domain.IssueVisibilityLive {
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

func pendingDecisionRevisionExists(pending []domain.PendingDecisionChange, decisionID string, revision int64) bool {
	for _, change := range pending {
		if change.DecisionID == decisionID && change.Revision == revision {
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
