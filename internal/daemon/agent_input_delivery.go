package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

// authoritativeAgentInputReceiver is deliberately stronger than a terminal
// transport. Implementations must atomically prove the exact incarnation and
// an empty tool-owned composer, exclude human input through submission, and
// idempotently return an acknowledgement for the same intent after retries.
// tmux send-keys/paste-buffer cannot implement this contract.
type authoritativeAgentInputReceiver interface {
	DeliverAgentInput(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error)
}

type authoritativeAgentInputRequest struct {
	Delivery   domain.AgentInputDeliveryRequest
	LeaseToken string
}

type authoritativeAgentInputAcknowledgement struct {
	ProjectID            string
	IntentKey            string
	AgentIncarnation     string
	LeaseToken           string
	AcknowledgementToken string
}

var errAuthoritativeAgentInputUnavailable = errors.New("authoritative agent input receiver unavailable")

type unavailableAgentInputReceiver struct{}

func (unavailableAgentInputReceiver) DeliverAgentInput(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	return authoritativeAgentInputAcknowledgement{}, errAuthoritativeAgentInputUnavailable
}

type agentInputDeliveryService struct {
	stores       func(string) *state.RuntimeStateStore
	issueClients func(string) *issues.Client
	receiver     authoritativeAgentInputReceiver
	owner        string
	now          func() time.Time
}

func newAgentInputDeliveryService(stores func(string) *state.RuntimeStateStore, issueClients func(string) *issues.Client, receiver authoritativeAgentInputReceiver, owner string) *agentInputDeliveryService {
	if receiver == nil {
		receiver = unavailableAgentInputReceiver{}
	}
	if strings.TrimSpace(owner) == "" {
		owner = "daemon-agent-input"
	}
	return &agentInputDeliveryService{stores: stores, issueClients: issueClients, receiver: receiver, owner: owner, now: func() time.Time { return time.Now().UTC() }}
}

func (s *agentInputDeliveryService) Deliver(ctx context.Context, request domain.AgentInputDeliveryRequest) (domain.AgentInputDeliveryResult, error) {
	if err := request.Target.Validate(); err != nil || strings.TrimSpace(request.Payload) == "" || strings.TrimSpace(request.IntentKey) == "" {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "invalid delivery request"}, err
	}
	client := s.issueClients(request.ProjectID)
	if client == nil {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "issue store unavailable"}, nil
	}
	intent, err := client.EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "persist delivery intent"}, err
	}
	if intent.State == "delivered" {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputDelivered, Reason: "durably acknowledged"}, nil
	}
	if intent.State == "expired" {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputExpired, Reason: "delivery expired"}, nil
	}
	if intent.State == "stale" {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "managed agent incarnation changed"}, nil
	}

	claimed, acquired, err := client.ClaimAgentInputDeliveryIntent(ctx, request.ProjectID, request.IntentKey, s.owner, s.now(), 30*time.Second)
	if err != nil {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "claim durable delivery intent"}, err
	}
	if !acquired {
		if claimed.State == "delivered" {
			return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputDelivered, Reason: "durably acknowledged"}, nil
		}
		if claimed.State == "expired" {
			return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputExpired, Reason: "delivery expired"}, nil
		}
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "delivery owned by another daemon"}, nil
	}
	release := func(stale bool) {
		_ = client.ReleaseAgentInputDeliveryIntent(context.WithoutCancel(ctx), request.ProjectID, request.IntentKey, claimed.LeaseToken, stale, s.now())
	}
	current, result, err := s.observeIdentity(ctx, request)
	if err != nil || result.Outcome != "" {
		release(result.Outcome == domain.AgentInputRejectedStaleTarget)
		return result, err
	}
	currentAgain, result, err := s.observeIdentity(ctx, request)
	if err != nil || result.Outcome != "" || !current.SameIncarnation(currentAgain) {
		release(true)
		if result.Outcome == "" {
			result = domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "managed agent incarnation changed before delivery"}
		}
		return result, err
	}

	ack, err := s.receiver.DeliverAgentInput(ctx, authoritativeAgentInputRequest{Delivery: request, LeaseToken: claimed.LeaseToken})
	if err != nil {
		release(false)
		if errors.Is(err, errAuthoritativeAgentInputUnavailable) {
			return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "authoritative composer and exclusion proof unavailable; intent remains queued"}, nil
		}
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "authoritative receiver failed"}, err
	}
	if ack.ProjectID != request.ProjectID || ack.IntentKey != request.IntentKey || ack.AgentIncarnation != request.Target.AgentIncarnation || ack.LeaseToken != claimed.LeaseToken || strings.TrimSpace(ack.AcknowledgementToken) == "" {
		release(false)
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "receiver acknowledgement did not match exact intent and incarnation"}, nil
	}
	acknowledged, err := client.AcknowledgeAgentInputDeliveryIntent(ctx, request.ProjectID, request.IntentKey, request.Target.AgentIncarnation, claimed.LeaseToken, ack.AcknowledgementToken, s.now())
	if err != nil {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "persist receiver acknowledgement"}, err
	}
	if !acknowledged {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "delivery lease changed before acknowledgement"}, nil
	}
	return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputDelivered, Reason: "receiver acknowledged exact intent and incarnation"}, nil
}

func (s *agentInputDeliveryService) RetryPending(ctx context.Context, projectID string, limit int) error {
	client := s.issueClients(projectID)
	if client == nil {
		return nil
	}
	intents, err := client.ListPendingAgentInputDeliveryIntents(ctx, projectID, s.now(), limit)
	if err != nil {
		return fmt.Errorf("list pending agent input intents: %w", err)
	}
	for _, intent := range intents {
		if _, err := s.Deliver(ctx, intent.Request); err != nil && !errors.Is(err, errAuthoritativeAgentInputUnavailable) {
			return fmt.Errorf("retry agent input intent %s: %w", intent.Request.IntentKey, err)
		}
	}
	return nil
}

func (s *agentInputDeliveryService) observeIdentity(ctx context.Context, request domain.AgentInputDeliveryRequest) (domain.ManagedAgentRuntimeIdentity, domain.AgentInputDeliveryResult, error) {
	store := s.stores(request.ProjectID)
	if store == nil {
		return domain.ManagedAgentRuntimeIdentity{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "runtime store unavailable"}, nil
	}
	identity, found, err := store.GetManagedAgentIdentity(ctx, request.ProjectID, request.SessionID, string(request.Target.LogicalPaneID))
	if err != nil {
		return domain.ManagedAgentRuntimeIdentity{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "identity lookup failed"}, fmt.Errorf("lookup managed agent identity: %w", err)
	}
	current := domain.ManagedAgentRuntimeIdentity{LogicalPaneID: domain.ManagedAgentPaneID(identity.LogicalPaneID), TmuxPaneID: identity.TmuxPaneID, PanePID: identity.PanePID, AgentIncarnation: identity.AgentIncarnation}
	if !found || !request.Target.SameIncarnation(current) {
		return current, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "managed agent incarnation changed"}, nil
	}
	activity, found, err := store.GetPhysicalSessionObservation(ctx, request.ProjectID, request.SessionID)
	if err != nil {
		return current, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "activity lookup failed"}, err
	}
	if !found || !strings.EqualFold(activity.ActivitySource, "hooks") || (activity.Activity != "idle" && activity.Activity != "waiting") {
		return current, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "hook-backed receiver readiness not proven"}, nil
	}
	return current, domain.AgentInputDeliveryResult{}, nil
}

func (d *Daemon) agentInputService() *agentInputDeliveryService { return d.agentInput }

func (d *Daemon) currentAgentInputTarget(ctx context.Context, projectID, sessionID string) (domain.ManagedAgentRuntimeIdentity, bool, error) {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return domain.ManagedAgentRuntimeIdentity{}, false, nil
	}
	identity, found, err := store.GetManagedAgentIdentity(ctx, projectID, sessionID, "agent")
	return domain.ManagedAgentRuntimeIdentity{LogicalPaneID: domain.ManagedAgentPaneID(identity.LogicalPaneID), TmuxPaneID: identity.TmuxPaneID, PanePID: identity.PanePID, AgentIncarnation: identity.AgentIncarnation}, found, err
}
