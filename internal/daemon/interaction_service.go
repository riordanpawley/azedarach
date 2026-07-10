package daemon

import (
	"context"
	"crypto/sha256"
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
	discussAttached := false
	switch command {
	case protocol.CommandInteractionDiscuss:
		projectID := daemonProjectIDFromContext(ctx)
		priorSessionID := strings.TrimSpace(r.SessionID)
		advisor, attached, acquireErr := s.daemon.ensureAdvisorSessionRuntime(ctx, projectID, r)
		if acquireErr != nil {
			return protocol.InteractionResponseBody{}, acquireErr
		}
		discussAttached = attached
		r.SessionID = advisor.SessionID
		if attached && r.State == domain.InteractionDiscussing && priorSessionID == advisor.SessionID {
			return protocol.InteractionResponseBody{Request: r, SessionAttached: true}, nil
		}
		r, e = r.Transition(domain.InteractionDiscussing, in.ExpectedRevision, now)
	case protocol.CommandInteractionPropose:
		if in.Answer.Revision != in.ExpectedRevision {
			return protocol.InteractionResponseBody{}, fmt.Errorf("%w: answer authored at revision %d, current mutation expects %d", domain.ErrStaleInteractionRevision, in.Answer.Revision, in.ExpectedRevision)
		}
		r.Proposal = &domain.InteractionAnswerAudit{Answer: in.Answer, Actor: strings.TrimSpace(in.Actor), CreatedAt: now}
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
	if command == protocol.CommandInteractionWithdraw {
		s.cleanupAdvisorAfterTerminalInteraction(ctx, r.ID)
	}
	response := protocol.InteractionResponseBody{Request: r}
	if command == protocol.CommandInteractionDiscuss {
		response.SessionStarted = !discussAttached
		response.SessionAttached = discussAttached
	}
	return response, nil
}

func advisorSessionID(requestID string) string {
	raw := strings.TrimSpace(requestID)
	var b strings.Builder
	b.WriteString("advisor-")
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	prefix := strings.TrimRight(b.String(), "-")
	if len(prefix) > 48 {
		prefix = prefix[:48]
	}
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%s-%x", prefix, digest[:6])
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
	if in.Answer.Revision != in.ExpectedRevision {
		return protocol.InteractionResponseBody{}, fmt.Errorf("%w: answer authored at revision %d, current mutation expects %d", domain.ErrStaleInteractionRevision, in.Answer.Revision, in.ExpectedRevision)
	}
	if in.Decision != nil {
		if in.Answer.ApprovedDecisionEffect != nil {
			return protocol.InteractionResponseBody{}, fmt.Errorf("%w: decision effect supplied twice", domain.ErrInvalidInteractionAnswer)
		}
		decision := *in.Decision
		in.Answer.ApprovedDecisionEffect = &decision
	}
	now := time.Now().UTC()
	r.FinalAnswer = &domain.InteractionAnswerAudit{Answer: in.Answer, Actor: strings.TrimSpace(in.Actor), CreatedAt: now}
	next, e := r.Transition(domain.InteractionResolved, in.ExpectedRevision, now)
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	next.FinalAnswer = r.FinalAnswer
	// Transition validates after state change, so validate the answer-bearing aggregate explicitly.
	if e = next.Validate(); e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	resolution := issues.InteractionResolution{Request: next, ExpectedRevision: in.ExpectedRevision}
	if next, e = c.ResolveInteraction(ctx, resolution); e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	s.cleanupAdvisorAfterTerminalInteraction(ctx, next.ID)
	projectID := daemonProjectIDFromContext(ctx)
	rev := s.daemon.nextRevision(projectID)
	body, _ := json.Marshal(protocol.InteractionResolvedEventBody{ID: next.ID, IssueID: next.IssueID, Revision: next.Revision, ResolvedAt: now})
	s.daemon.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID), Revision: rev, Event: protocol.EventInteractionResolved, Kind: protocol.EnvelopeKindEvent, EmittedAt: now, Body: body})
	return protocol.InteractionResponseBody{Request: next}, nil
}

func (s issueInteractionService) cleanupAdvisorAfterTerminalInteraction(ctx context.Context, requestID string) {
	if err := s.daemon.cleanupAdvisorSessionRuntime(ctx, daemonProjectIDFromContext(ctx), requestID); err != nil && s.daemon.cfg.Logger != nil {
		s.daemon.cfg.Logger.Warn("advisor session cleanup after terminal interaction failed", "request_id", requestID, "error", err)
	}
}

func humanActor(actor string) bool {
	return domain.HumanInteractionActor(actor)
}
