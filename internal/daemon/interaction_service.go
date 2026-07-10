package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"strings"
	"time"
)

type issueInteractionService struct{ daemon *Daemon }

func interactionStalenessPolicy(significance domain.InteractionSignificance) domain.InteractionStalenessPolicy {
	switch significance {
	case domain.InteractionSignificanceCritical:
		return domain.InteractionStalenessPolicy{StaleAfter: 4 * time.Hour, ReminderInterval: 4 * time.Hour}
	case domain.InteractionSignificanceMaterial:
		return domain.InteractionStalenessPolicy{StaleAfter: 24 * time.Hour, ReminderInterval: 24 * time.Hour}
	default:
		return domain.InteractionStalenessPolicy{StaleAfter: 72 * time.Hour, ReminderInterval: 48 * time.Hour}
	}
}

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
	return s.response(in.Request, time.Now().UTC()), nil
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
	if e == nil {
		rs, e = s.reconcileStaleness(ctx, c, rs, time.Now().UTC())
	}
	ages := make(map[string]domain.InteractionAgeView, len(rs))
	now := time.Now().UTC()
	for _, r := range rs {
		ages[r.ID] = r.AgeView(now, interactionStalenessPolicy(r.Significance))
	}
	return protocol.InteractionListResponseBody{Requests: rs, Ages: ages}, e
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
	rs, e := s.reconcileStaleness(ctx, c, []domain.InteractionRequest{r}, time.Now().UTC())
	if e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	r = rs[0]
	return s.response(r, time.Now().UTC()), nil
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
	runtimeResult := advisorSessionRuntimeResult{}
	switch command {
	case protocol.CommandInteractionDiscuss:
		if !interactionRuntimeActor(in.Actor) {
			return protocol.InteractionResponseBody{}, fmt.Errorf("advisor sessions have no discussion lifecycle authority")
		}
		projectID := daemonProjectIDFromContext(ctx)
		priorSessionID := strings.TrimSpace(r.SessionID)
		var acquireErr error
		runtimeResult, acquireErr = s.daemon.ensureAdvisorSessionRuntime(ctx, projectID, r)
		if acquireErr != nil {
			return protocol.InteractionResponseBody{}, acquireErr
		}
		r.SessionID = runtimeResult.Session.SessionID
		if r.State == domain.InteractionDiscussing && priorSessionID == runtimeResult.Session.SessionID {
			return s.runtimeResponse(r, now, runtimeResult), nil
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
		r.Disposition = &domain.InteractionDispositionAudit{Actor: strings.TrimSpace(in.Actor), Reason: strings.TrimSpace(in.Reason), CreatedAt: now}
		r, e = r.Transition(domain.InteractionWithdrawn, in.ExpectedRevision, now)
	case protocol.CommandInteractionSupersede:
		if !humanActor(in.Actor) && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(in.Actor)), "orchestrator") {
			return protocol.InteractionResponseBody{}, fmt.Errorf("advisor sessions have no supersession authority")
		}
		if strings.TrimSpace(in.ReplacementID) == r.ID {
			return protocol.InteractionResponseBody{}, fmt.Errorf("superseding interaction replacement must differ from the current request")
		}
		r.Disposition = &domain.InteractionDispositionAudit{Actor: strings.TrimSpace(in.Actor), Reason: strings.TrimSpace(in.Reason), ReplacementID: strings.TrimSpace(in.ReplacementID), CreatedAt: now}
		r, e = r.Transition(domain.InteractionSuperseded, in.ExpectedRevision, now)
	case protocol.CommandInteractionRecover:
		if !interactionRuntimeActor(in.Actor) {
			return protocol.InteractionResponseBody{}, fmt.Errorf("advisor sessions have no recovery authority")
		}
		var recoverErr error
		runtimeResult, recoverErr = s.daemon.ensureAdvisorSessionRuntime(ctx, daemonProjectIDFromContext(ctx), r)
		if recoverErr != nil {
			return protocol.InteractionResponseBody{}, recoverErr
		}
		r, e = r.Recover(in.Actor, runtimeResult.Session.SessionID, in.ExpectedRevision, now)
	}
	if e != nil {
		if runtimeResult.Started {
			s.cleanupAdvisorAfterFailedInteractionMutation(ctx, c, in.ID, runtimeResult.Session.SessionID)
		}
		return protocol.InteractionResponseBody{}, e
	}
	if command == protocol.CommandInteractionRecover {
		e = c.UpdateInteractionMetadata(ctx, r, in.ExpectedRevision)
	} else {
		e = c.UpdateInteraction(ctx, r, in.ExpectedRevision)
	}
	if e != nil {
		if runtimeResult.Started {
			s.cleanupAdvisorAfterFailedInteractionMutation(ctx, c, in.ID, runtimeResult.Session.SessionID)
		}
		return protocol.InteractionResponseBody{}, e
	}
	switch command {
	case protocol.CommandInteractionWithdraw:
		s.cleanupAdvisorAfterTerminalInteraction(ctx, r.ID)
		s.publishLifecycle(ctx, protocol.EventInteractionWithdrawn, r, 0, "")
	case protocol.CommandInteractionSupersede:
		s.cleanupAdvisorAfterTerminalInteraction(ctx, r.ID)
		s.publishLifecycle(ctx, protocol.EventInteractionSuperseded, r, 0, in.ReplacementID)
	case protocol.CommandInteractionRecover:
		s.publishLifecycle(ctx, protocol.EventInteractionRecovered, r, 0, "")
	}
	response := s.response(r, now)
	if command == protocol.CommandInteractionDiscuss || command == protocol.CommandInteractionRecover {
		response = s.runtimeResponse(r, now, runtimeResult)
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
	if in.Decision != nil {
		resolution.Decision = &issues.InteractionDecisionEffect{Title: in.Decision.Title, Rationale: in.Decision.Rationale, Context: in.Decision.Context, Consequences: in.Decision.Consequences}
	}
	if e = c.ResolveInteraction(ctx, resolution); e != nil {
		return protocol.InteractionResponseBody{}, e
	}
	s.cleanupAdvisorAfterTerminalInteraction(ctx, next.ID)
	projectID := daemonProjectIDFromContext(ctx)
	rev := s.daemon.nextRevision(projectID)
	body, _ := json.Marshal(protocol.InteractionResolvedEventBody{ID: next.ID, IssueID: next.IssueID, Revision: next.Revision, ResolvedAt: now})
	s.daemon.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID), Revision: rev, Event: protocol.EventInteractionResolved, Kind: protocol.EnvelopeKindEvent, EmittedAt: now, Body: body})
	return s.response(next, now), nil
}

func (s issueInteractionService) cleanupAdvisorAfterTerminalInteraction(ctx context.Context, requestID string) {
	if err := s.daemon.cleanupAdvisorSessionRuntime(ctx, daemonProjectIDFromContext(ctx), requestID); err != nil && s.daemon.cfg.Logger != nil {
		s.daemon.cfg.Logger.Warn("advisor session cleanup after terminal interaction failed", "request_id", requestID, "error", err)
	}
}

func (s issueInteractionService) cleanupAdvisorAfterFailedInteractionMutation(ctx context.Context, c *issues.Client, requestID, sessionID string) {
	current, found, err := c.GetInteraction(ctx, requestID)
	if err == nil && found && current.Unresolved() && strings.TrimSpace(current.SessionID) == strings.TrimSpace(sessionID) {
		return
	}
	s.cleanupAdvisorAfterTerminalInteraction(ctx, requestID)
}

func (s issueInteractionService) runtimeResponse(r domain.InteractionRequest, now time.Time, runtime advisorSessionRuntimeResult) protocol.InteractionResponseBody {
	response := s.response(r, now)
	response.SessionStarted = runtime.Started
	response.SessionAttached = runtime.Attached
	response.SessionResumed = runtime.Resumed
	return response
}

func (s issueInteractionService) response(r domain.InteractionRequest, now time.Time) protocol.InteractionResponseBody {
	return protocol.InteractionResponseBody{Request: r, Age: r.AgeView(now, interactionStalenessPolicy(r.Significance))}
}

func (s issueInteractionService) reconcileStaleness(ctx context.Context, c *issues.Client, requests []domain.InteractionRequest, now time.Time) ([]domain.InteractionRequest, error) {
	for i := range requests {
		next, marked, reminded, err := requests[i].ReconcileStaleness(now, interactionStalenessPolicy(requests[i].Significance))
		if err != nil {
			return nil, err
		}
		if !marked && !reminded {
			continue
		}
		if err := c.UpdateInteractionMetadata(ctx, next, requests[i].Revision); err != nil {
			if !errors.Is(err, domain.ErrStaleInteractionRevision) {
				return nil, err
			}
			current, found, refreshErr := c.GetInteraction(ctx, next.ID)
			if refreshErr != nil || !found {
				if refreshErr != nil {
					return nil, refreshErr
				}
				return nil, domain.ErrNotFound
			}
			requests[i] = current
			continue
		}
		requests[i] = next
		if marked {
			s.publishLifecycle(ctx, protocol.EventInteractionStale, next, 0, "")
		}
		if reminded {
			s.publishLifecycle(ctx, protocol.EventInteractionReminder, next, len(next.Reminders), "")
		}
	}
	return requests, nil
}

func (s issueInteractionService) publishLifecycle(ctx context.Context, event string, r domain.InteractionRequest, sequence int, replacementID string) {
	projectID := daemonProjectIDFromContext(ctx)
	now := r.UpdatedAt
	body, _ := json.Marshal(protocol.InteractionLifecycleEventBody{ID: r.ID, IssueID: r.IssueID, Revision: r.Revision, Sequence: sequence, ReplacementID: strings.TrimSpace(replacementID), OccurredAt: now})
	s.daemon.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID), Revision: s.daemon.nextRevision(projectID), Event: event, Kind: protocol.EnvelopeKindEvent, EmittedAt: now, Body: body})
}

func humanActor(actor string) bool {
	a := strings.ToLower(strings.TrimSpace(actor))
	return a == "human" || strings.HasPrefix(a, "human:")
}

func interactionRuntimeActor(actor string) bool {
	a := strings.ToLower(strings.TrimSpace(actor))
	return humanActor(a) || strings.HasPrefix(a, "orchestrator") || strings.HasPrefix(a, "daemon:")
}
