package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"strings"
	"time"
)

type issueInteractionService struct{ daemon *Daemon }

func (s issueInteractionService) client(ctx context.Context) (*issues.Client, error) {
	projectID := daemonProjectIDFromContext(ctx)
	c := s.daemon.issueClientForProject(projectID)
	if c == nil {
		return nil, fmt.Errorf("interaction store unavailable")
	}
	return c, nil
}
func (s issueInteractionService) CreateInteraction(ctx context.Context, in protocol.InteractionCreateRequestBody) (protocol.InteractionResponseBody, error) {
	if in.Request.State != domain.InteractionOpen || in.Request.Revision != 1 || in.Request.Proposal != nil || in.Request.FinalAnswer != nil {
		return protocol.InteractionResponseBody{}, fmt.Errorf("new interaction requests must start open at revision 1 without answers")
	}
	c, e := s.client(ctx)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	if e = c.CreateInteraction(ctx, in.Request); e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	return protocol.InteractionResponseBody{Request: in.Request}, nil
}
func (s issueInteractionService) ListInteractions(ctx context.Context, in protocol.InteractionListRequestBody) (protocol.InteractionListResponseBody, error) {
	c, e := s.client(ctx)
	if e != nil {
		return protocol.InteractionListResponseBody{}, e
	}
	var rs []domain.InteractionRequest
	if strings.TrimSpace(in.IssueID) != "" {
		rs, e = c.InteractionsForIssue(ctx, in.IssueID)
	} else {
		rs, e = c.ListInteractions(ctx)
	}
	return protocol.InteractionListResponseBody{Requests: rs}, e
}
func (s issueInteractionService) GetInteraction(ctx context.Context, in protocol.InteractionGetRequestBody) (protocol.InteractionResponseBody, error) {
	c, e := s.client(ctx)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	r, ok, e := c.GetInteraction(ctx, in.ID)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	if !ok {
		return protocol.InteractionResponseBody{}, domain.ErrNotFound
	}
	return protocol.InteractionResponseBody{Request: r}, nil
}
func (s issueInteractionService) MutateInteraction(ctx context.Context, command string, in protocol.InteractionMutationRequestBody) (protocol.InteractionResponseBody, error) {
	c, e := s.client(ctx)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	r, ok, e := c.GetInteraction(ctx, in.ID)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	if !ok {
		return protocol.InteractionResponseBody{}, domain.ErrNotFound
	}
	now := time.Now().UTC()
	switch command {
	case protocol.CommandInteractionDiscuss:
		r.SessionID = strings.TrimSpace(in.SessionID)
		r, e = r.Transition(domain.InteractionDiscussing, in.ExpectedRevision, now)
	case protocol.CommandInteractionPropose:
		r.Proposal = &domain.InteractionAnswerAudit{Answer: strings.TrimSpace(in.Answer), Actor: strings.TrimSpace(in.Actor), CreatedAt: now}
		r, e = r.Transition(domain.InteractionAnswerProposed, in.ExpectedRevision, now)
	case protocol.CommandInteractionAnswer:
		if !humanActor(in.Actor) {
			return protocol.InteractionResponseBody{}, fmt.Errorf("only the human respondent may answer interaction requests")
		}
		return s.resolve(ctx, c, r, protocol.InteractionResolveRequestBody{InteractionMutationRequestBody: in})
	case protocol.CommandInteractionWithdraw:
		if !humanActor(in.Actor) && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(in.Actor)), "orchestrator") {
			return protocol.InteractionResponseBody{}, fmt.Errorf("advisor sessions have no withdrawal authority")
		}
		r, e = r.Transition(domain.InteractionWithdrawn, in.ExpectedRevision, now)
	}
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	if e = c.UpdateInteraction(ctx, r, in.ExpectedRevision); e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	return protocol.InteractionResponseBody{Request: r}, nil
}
func (s issueInteractionService) ResolveInteraction(ctx context.Context, in protocol.InteractionResolveRequestBody) (protocol.InteractionResponseBody, error) {
	c, e := s.client(ctx)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	r, ok, e := c.GetInteraction(ctx, in.ID)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	if !ok {
		return protocol.InteractionResponseBody{}, domain.ErrNotFound
	}
	if !humanActor(in.Actor) {
		return protocol.InteractionResponseBody{}, fmt.Errorf("only the human respondent may resolve interaction requests")
	}
	return s.resolve(ctx, c, r, in)
}
func (s issueInteractionService) resolve(ctx context.Context, c *issues.Client, r domain.InteractionRequest, in protocol.InteractionResolveRequestBody) (protocol.InteractionResponseBody, error) {
	now := time.Now().UTC()
	r.FinalAnswer = &domain.InteractionAnswerAudit{Answer: strings.TrimSpace(in.Answer), Actor: strings.TrimSpace(in.Actor), CreatedAt: now}
	r.Effects = interactionResolutionEffects(in)
	next, e := r.Transition(domain.InteractionResolved, in.ExpectedRevision, now)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	next.FinalAnswer = r.FinalAnswer
	// Transition validates after state change, so validate the answer-bearing aggregate explicitly.
	if e = next.Validate(); e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	ch := in.IssueChanges
	resolution := issues.InteractionResolution{Request: next, ExpectedRevision: in.ExpectedRevision, IssueChanges: issues.InteractionIssueChanges{Title: ch.Title, Description: ch.Description, Design: ch.Design, Acceptance: ch.Acceptance, Priority: ch.Priority}}
	if in.Decision != nil {
		resolution.Decision = &issues.InteractionDecisionEffect{Title: in.Decision.Title, Rationale: in.Decision.Rationale, Context: in.Decision.Context, Consequences: in.Decision.Consequences}
	}
	if e = c.ResolveInteraction(ctx, resolution); e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	projectID := daemonProjectIDFromContext(ctx)
	rev := s.daemon.nextRevision(projectID)
	body, _ := json.Marshal(protocol.InteractionResolvedEventBody{ID: next.ID, IssueID: next.IssueID, Revision: next.Revision, ResolvedAt: now})
	s.daemon.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID), Revision: rev, Event: protocol.EventInteractionResolved, Kind: protocol.EnvelopeKindEvent, EmittedAt: now, Body: body})
	return protocol.InteractionResponseBody{Request: next}, nil
}

func interactionResolutionEffects(in protocol.InteractionResolveRequestBody) []domain.InteractionResolutionEffect {
	var out []domain.InteractionResolutionEffect
	add := func(kind, target, summary string, approved bool) {
		if approved {
			out = append(out, domain.InteractionResolutionEffect{Kind: kind, Target: target, Summary: summary})
		}
	}
	add("issue_field", in.ID, "title", in.IssueChanges.Title != nil)
	add("issue_field", in.ID, "description", in.IssueChanges.Description != nil)
	add("issue_field", in.ID, "design", in.IssueChanges.Design != nil)
	add("issue_field", in.ID, "acceptance", in.IssueChanges.Acceptance != nil)
	add("issue_field", in.ID, "priority", in.IssueChanges.Priority != nil)
	add("decision", in.ID, "record significant decision", in.Decision != nil)
	return out
}
func humanActor(actor string) bool {
	a := strings.ToLower(strings.TrimSpace(actor))
	return a == "human" || strings.HasPrefix(a, "human:")
}
