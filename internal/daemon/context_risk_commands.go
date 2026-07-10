package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const issueContextRiskDefaultWindow = 14 * 24 * time.Hour

func (d *Daemon) handleTaskContextRisk(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID  string    `json:"task_id"`
		RepoDir string    `json:"repo_dir,omitempty"`
		Since   time.Time `json:"since,omitempty"`
		Compact bool      `json:"compact,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.taskContextRisk(ctx, projectID, cmd.TaskID, cmd.RepoDir, cmd.Since)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if cmd.Compact {
		result = domain.CompactIssueContextRisk(result)
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) taskContextRiskForCloseout(ctx context.Context, projectID, issueID, repoDir string) *domain.IssueContextRiskPacket {
	packet, err := d.taskContextRisk(ctx, projectID, issueID, repoDir, time.Now().UTC().Add(-issueContextRiskDefaultWindow))
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("context risk closeout read failed", "project_id", projectID, "issue_id", issueID, "error", err)
		}
		return nil
	}
	packet = domain.CompactIssueContextRisk(packet)
	return &packet
}

func (d *Daemon) taskContextRisk(ctx context.Context, projectID, issueID, repoDir string, since time.Time) (domain.IssueContextRiskPacket, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.IssueContextRiskPacket{}, fmt.Errorf("issue client unavailable for project %s", projectID)
	}
	contextTasks, err := issueClient.GetWithDependencyContextRuntime(ctx, projectID, strings.TrimSpace(issueID))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return degradedIssueContextRiskPacket(issueID, "", since, ctxErr), nil
		}
		return domain.IssueContextRiskPacket{}, fmt.Errorf("load issue dependency context: %w", err)
	}
	target, ok := findDaemonTaskByID(contextTasks, issueID)
	if !ok {
		return domain.IssueContextRiskPacket{}, fmt.Errorf("issue not found: %s", strings.TrimSpace(issueID))
	}

	parentID := ""
	if target.ParentID != nil {
		parentID = strings.TrimSpace(target.ParentID.String())
	}
	targetMailboxParentID := parentID
	if targetMailboxParentID == "" {
		targetMailboxParentID = target.ID.String()
	}
	candidates := map[string]domain.Task{}
	if parentID != "" {
		subtree, err := issueClient.ListParentChildSubtreeWithRuntime(ctx, projectID, parentID)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return degradedIssueContextRiskPacket(target.ID.String(), parentID, since, ctxErr), nil
			}
			return domain.IssueContextRiskPacket{}, fmt.Errorf("load sibling context for %s: %w", parentID, err)
		}
		for _, task := range subtree {
			if task.ID == target.ID || task.ParentID == nil || !strings.EqualFold(strings.TrimSpace(task.ParentID.String()), parentID) {
				continue
			}
			candidates[task.ID.String()] = task
		}
	}
	for _, task := range contextTasks {
		if task.ID == target.ID || task.ID.String() == parentID {
			continue
		}
		if issueContextRiskRelated(target, task) {
			candidates[task.ID.String()] = task
		}
	}

	if strings.TrimSpace(repoDir) == "" {
		repoDir = strings.TrimSpace(d.cfg.RepoDir)
	}
	targetEvidence := d.issueContextRiskEvidence(ctx, issueClient, target, "target", repoDir, since, targetMailboxParentID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return d.buildIssueContextRiskPacket(targetEvidence, parentID, nil, since, ctxErr), nil
	}
	candidateEvidence := make([]domain.IssueContextRiskEvidence, 0, len(candidates))
	for _, candidate := range candidates {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return d.buildIssueContextRiskPacket(targetEvidence, parentID, candidateEvidence, since, ctxErr), nil
		}
		relationship := "related"
		if candidate.ParentID != nil && parentID != "" && strings.EqualFold(strings.TrimSpace(candidate.ParentID.String()), parentID) {
			relationship = "sibling"
		}
		evidence := d.issueContextRiskEvidence(ctx, issueClient, candidate, relationship, repoDir, since, issueContextRiskMailboxParent(candidate, parentID))
		candidateEvidence = append(candidateEvidence, evidence)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return d.buildIssueContextRiskPacket(targetEvidence, parentID, candidateEvidence, since, ctxErr), nil
		}
	}
	return d.buildIssueContextRiskPacket(targetEvidence, parentID, candidateEvidence, since, nil), nil
}

func (d *Daemon) buildIssueContextRiskPacket(targetEvidence domain.IssueContextRiskEvidence, parentID string, candidateEvidence []domain.IssueContextRiskEvidence, since time.Time, ctxErr error) domain.IssueContextRiskPacket {
	packet := domain.BuildIssueContextRisk(domain.IssueContextRiskInput{
		Target:        targetEvidence,
		ParentIssueID: parentID,
		Candidates:    candidateEvidence,
		Since:         since,
		GeneratedAt:   time.Now().UTC(),
	})
	if ctxErr != nil {
		packet.Degraded = true
		packet.Timeout = ctxErr == context.DeadlineExceeded
		packet.DegradedReason = ctxErr.Error()
	}
	return packet
}

func degradedIssueContextRiskPacket(issueID, parentID string, since time.Time, ctxErr error) domain.IssueContextRiskPacket {
	packet := domain.BuildIssueContextRisk(domain.IssueContextRiskInput{
		Target: domain.IssueContextRiskEvidence{
			IssueID: strings.TrimSpace(issueID),
		},
		ParentIssueID: strings.TrimSpace(parentID),
		Since:         since,
		GeneratedAt:   time.Now().UTC(),
	})
	packet.Degraded = true
	packet.Timeout = ctxErr == context.DeadlineExceeded
	packet.DegradedReason = ctxErr.Error()
	return packet
}

func issueContextRiskRelated(target, candidate domain.Task) bool {
	related := func(deps []domain.Dependency, otherID string) bool {
		for _, dep := range deps {
			if !strings.EqualFold(strings.TrimSpace(dep.ID.String()), strings.TrimSpace(otherID)) {
				continue
			}
			switch dep.Type {
			case domain.DependencyRelatedTo, domain.DependencyDiscovered, domain.DependencyCreatedIn:
				return true
			}
		}
		return false
	}
	return related(target.Dependencies, candidate.ID.String()) || related(candidate.Dependencies, target.ID.String())
}

func issueContextRiskMailboxParent(task domain.Task, fallbackParentID string) string {
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		return strings.TrimSpace(task.ParentID.String())
	}
	if strings.TrimSpace(fallbackParentID) != "" {
		return strings.TrimSpace(fallbackParentID)
	}
	return task.ID.String()
}

func (d *Daemon) issueContextRiskEvidence(ctx context.Context, issueClient *issues.Client, task domain.Task, relationship, repoDir string, since time.Time, mailboxParentID string) domain.IssueContextRiskEvidence {
	evidence := domain.IssueContextRiskEvidence{
		IssueID:      task.ID.String(),
		Relationship: relationship,
		ObservedAt:   task.UpdatedAt,
	}
	if strings.TrimSpace(mailboxParentID) != "" && strings.TrimSpace(repoDir) != "" {
		if mailboxEvidence := issueContextRiskMailboxEvidence(repoDir, mailboxParentID, task.ID.String(), since); mailboxEvidence.IssueID != "" {
			evidence = mergeIssueContextRiskEvidence(evidence, mailboxEvidence)
		}
	}
	if issueClient != nil {
		events, err := issueClient.ListIssueObservationEvents(ctx, task.ID.String(), issues.IssueObservationEventListOptions{Limit: 50, NewestFirst: true})
		if err == nil {
			for _, event := range events {
				if !since.IsZero() && !event.ObservedAt.IsZero() && event.ObservedAt.Before(since) {
					continue
				}
				evidence = mergeIssueContextRiskEvidence(evidence, issueContextRiskObservationEvidence(event))
			}
		} else if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("context risk issue events read failed", "issue_id", task.ID.String(), "error", err)
		}
	}
	return evidence
}

func issueContextRiskMailboxEvidence(repoDir, parentIssueID, issueID string, since time.Time) domain.IssueContextRiskEvidence {
	events, err := readMailboxEvents(repoDir, parentIssueID)
	if err != nil {
		return domain.IssueContextRiskEvidence{}
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if !strings.EqualFold(strings.TrimSpace(event.IssueID), strings.TrimSpace(issueID)) || !daemonWorkerIntegrationReadyMailType(event.Type) {
			continue
		}
		if !since.IsZero() && !event.CreatedAt.IsZero() && event.CreatedAt.Before(since) {
			continue
		}
		packet, validation := domain.ParseWorkerEvidencePacketBody(event.Body)
		if !validation.Complete {
			return domain.IssueContextRiskEvidence{}
		}
		evidence := domain.IssueContextRiskEvidence{
			IssueID:       issueID,
			Files:         packet.FilesChanged,
			Validation:    strings.Join(packet.KeyAssertions, "; "),
			ObservedAt:    event.CreatedAt,
			EvidenceKinds: []string{"worker_evidence.v1"},
		}
		if strings.EqualFold(strings.TrimSpace(packet.Review.Status), "findings") {
			evidence.EvidenceKinds = append(evidence.EvidenceKinds, "review_findings")
			evidence.RiskNotes = append(evidence.RiskNotes, packet.Review.Findings...)
		}
		for _, risk := range packet.Risks {
			if !strings.EqualFold(strings.TrimSpace(risk), "none") {
				evidence.RiskNotes = append(evidence.RiskNotes, risk)
			}
		}
		return evidence
	}
	return domain.IssueContextRiskEvidence{}
}

func issueContextRiskObservationEvidence(event domain.IssueObservationEvent) domain.IssueContextRiskEvidence {
	evidence := domain.IssueContextRiskEvidence{
		IssueID:       event.IssueID.String(),
		ObservedAt:    event.ObservedAt,
		EvidenceKinds: []string{string(event.Type)},
	}
	payload := event.Payload
	evidence.Files = append(evidence.Files, issueContextRiskStringList(payload, "files_changed", "changed_files", "files")...)
	evidence.Symbols = append(evidence.Symbols, issueContextRiskStringList(payload, "changed_symbols", "symbols")...)
	evidence.Tests = append(evidence.Tests, issueContextRiskStringList(payload, "tests_changed", "tests")...)
	evidence.RelatedConsumersAudited = append(evidence.RelatedConsumersAudited, issueContextRiskStringList(payload, "related_consumers_audited")...)
	evidence.RootCause = issueContextRiskString(payload, "root_cause")
	evidence.Invariant = issueContextRiskString(payload, "invariant")
	evidence.Validation = issueContextRiskString(payload, "regression_validation", "validation")
	switch event.Type {
	case domain.IssueEventRiskRecorded, domain.IssueEventValidationFailed, domain.IssueEventReviewCompleted:
		evidence.RiskNotes = append(evidence.RiskNotes, issueContextRiskString(payload, "summary", "message", "reason", "body"))
	}
	return evidence
}

func issueContextRiskString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func issueContextRiskStringList(payload map[string]any, keys ...string) []string {
	var out []string
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			out = append(out, typed...)
		case []any:
			for _, item := range typed {
				out = append(out, fmt.Sprint(item))
			}
		case string:
			if strings.Contains(typed, "\n") {
				out = append(out, strings.Split(typed, "\n")...)
			} else if strings.Contains(typed, ",") {
				out = append(out, strings.Split(typed, ",")...)
			} else {
				out = append(out, typed)
			}
		}
	}
	return out
}

func mergeIssueContextRiskEvidence(left, right domain.IssueContextRiskEvidence) domain.IssueContextRiskEvidence {
	if strings.TrimSpace(left.IssueID) == "" {
		left.IssueID = right.IssueID
	}
	left.Files = append(left.Files, right.Files...)
	left.Symbols = append(left.Symbols, right.Symbols...)
	left.Tests = append(left.Tests, right.Tests...)
	left.RelatedConsumersAudited = append(left.RelatedConsumersAudited, right.RelatedConsumersAudited...)
	if strings.TrimSpace(left.RootCause) == "" {
		left.RootCause = right.RootCause
	}
	if strings.TrimSpace(left.Invariant) == "" {
		left.Invariant = right.Invariant
	}
	if strings.TrimSpace(left.Validation) == "" {
		left.Validation = right.Validation
	}
	left.RiskNotes = append(left.RiskNotes, right.RiskNotes...)
	left.EvidenceKinds = append(left.EvidenceKinds, right.EvidenceKinds...)
	if right.ObservedAt.After(left.ObservedAt) {
		left.ObservedAt = right.ObservedAt
	}
	return left
}
