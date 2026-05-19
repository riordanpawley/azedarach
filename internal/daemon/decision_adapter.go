package daemon

import (
	"context"
	"errors"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type issueDecisionService struct {
	daemon *Daemon
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
	for _, status := range req.Statuses {
		filter.Statuses = append(filter.Statuses, issues.DecisionStatus(status))
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

func (s issueDecisionService) CreateDecision(ctx context.Context, req protocol.DecisionCreateRequestBody) (protocol.DecisionCreateResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionCreateResponseBody{}, err
	}
	params := issues.CreateDecisionParams{
		LocalID:      req.ID,
		Title:        req.Title,
		Context:      req.Context,
		Decision:     req.Decision,
		Consequences: req.Consequences,
		Status:       issues.DecisionStatus(req.Status),
	}
	decision, err := c.CreateDecision(ctx, params)
	if err != nil {
		return protocol.DecisionCreateResponseBody{}, err
	}
	return protocol.DecisionCreateResponseBody{Decision: mapDecisionToProtocol(decision)}, nil
}

func (s issueDecisionService) UpdateDecision(ctx context.Context, req protocol.DecisionUpdateRequestBody) (protocol.DecisionUpdateResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionUpdateResponseBody{}, err
	}
	params := issues.UpdateDecisionParams{
		Title:        req.Title,
		Context:      req.Context,
		Decision:     req.Decision,
		Consequences: req.Consequences,
	}
	if req.Status != nil {
		status := issues.DecisionStatus(*req.Status)
		params.Status = &status
	}
	decision, err := c.UpdateDecision(ctx, req.ID, params)
	if err != nil {
		return protocol.DecisionUpdateResponseBody{}, err
	}
	return protocol.DecisionUpdateResponseBody{Decision: mapDecisionToProtocol(decision)}, nil
}

func (s issueDecisionService) DeleteDecision(ctx context.Context, req protocol.DecisionDeleteRequestBody) (protocol.DecisionDeleteResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionDeleteResponseBody{}, err
	}
	if err := c.DeleteDecision(ctx, req.ID); err != nil {
		return protocol.DecisionDeleteResponseBody{}, err
	}
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
	link, err := c.AddDecisionLink(ctx, params)
	if err != nil {
		return protocol.DecisionLinkAddResponseBody{}, err
	}
	return protocol.DecisionLinkAddResponseBody{Link: mapDecisionLinkToProtocol(link)}, nil
}

func (s issueDecisionService) RemoveDecisionLink(ctx context.Context, req protocol.DecisionLinkRemoveRequestBody) (protocol.DecisionLinkRemoveResponseBody, error) {
	c, err := s.client(ctx)
	if err != nil {
		return protocol.DecisionLinkRemoveResponseBody{}, err
	}
	if err := c.RemoveDecisionLink(ctx, req.DecisionID, issues.DecisionTargetKind(req.TargetKind), req.TargetID); err != nil {
		return protocol.DecisionLinkRemoveResponseBody{}, err
	}
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
		Context:      d.Context,
		Decision:     d.Decision,
		Consequences: d.Consequences,
		Status:       protocol.DecisionStatus(d.Status),
		CreatedAt:    d.CreatedAt,
		UpdatedAt:    d.UpdatedAt,
	}
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
