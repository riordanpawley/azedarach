package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/daemon/notices"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const interactionNoticeCategory = "interaction_request"

type interactionNoticeAdapter struct {
	projectID string
	notices   *notices.Service
}

func (d *Daemon) reconcileInteractionNotices(ctx context.Context, projectID string) error {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil
	}
	requests, err := issueClient.Interactions(ctx)
	if err != nil {
		return fmt.Errorf("refresh interaction notice projection: %w", err)
	}
	adapter := interactionNoticeAdapter{projectID: d.canonicalProjectID(projectID), notices: d.noticeService}
	for _, request := range requests {
		if err := adapter.ProjectInteractionNotice(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

func (a interactionNoticeAdapter) ProjectInteractionNotice(ctx context.Context, request domain.InteractionRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("project interaction notice: %w", err)
	}
	candidate := interactionNoticeCandidate(a.projectID, request)
	record, _, _, err := a.notices.Project(ctx, candidate)
	if err != nil {
		return fmt.Errorf("project interaction notice %s: %w", request.ID, err)
	}
	if request.Unresolved() || record.State != notices.StateActive {
		return nil
	}
	_, _, _, err = a.notices.Update(ctx, notices.UpdateParams{
		ProjectID: a.projectID, NoticeID: record.NoticeID, State: notices.StateResolved, Now: request.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("resolve interaction notice %s: %w", request.ID, err)
	}
	return nil
}

func interactionNoticeCandidate(projectID string, request domain.InteractionRequest) notices.Candidate {
	severity := notices.SeverityInfo
	switch request.Significance {
	case domain.InteractionSignificanceMaterial:
		severity = notices.SeverityWarning
	case domain.InteractionSignificanceCritical:
		severity = notices.SeverityError
	}
	return notices.Candidate{
		NoticeID: "interaction-" + strings.TrimSpace(request.ID), ProjectID: projectID,
		Scope: notices.Scope{Type: "task", ID: request.IssueID}, Severity: severity,
		Source:   &notices.Source{InteractionID: request.ID, InteractionRevision: request.Revision, Producer: interactionNoticeCategory},
		Category: interactionNoticeCategory, Title: "Human decision required", Summary: request.Question,
		Detail: request.Why, DedupeKey: "interaction:" + request.ID, OccurredAt: request.UpdatedAt,
		RetentionClass: notices.RetentionAudit,
		Actions:        []notices.Action{{ActionID: "open_task", Kind: "client.open_task", Label: "Open task", Enabled: true, TargetScope: notices.Scope{Type: "task", ID: request.IssueID}}},
	}
}
