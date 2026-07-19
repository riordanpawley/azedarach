package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

// authoritativeAgentInputReceiver is deliberately stronger than a terminal
// transport. Implementations must prove the exact incarnation, exclude managed
// human submission through app-server acceptance, and return an exact
// acknowledgement. tmux send-keys/paste-buffer cannot implement this contract.
type authoritativeAgentInputReceiver interface {
	DeliverAgentInput(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error)
}

type authoritativeAgentInputRequest struct {
	Delivery                  domain.AgentInputDeliveryRequest
	LeaseToken                string
	SessionLeaseOwner         string
	SessionLeaseToken         string
	PreviousAgentIncarnation  string
	PreviousSessionLeaseToken string
	CompleteSessionTakeover   func(context.Context) (issues.AgentInputDeliverySessionLease, error)
	BeginSubmission           func(context.Context) (time.Time, error)
	RevalidateSubmissionFence func(context.Context) (time.Time, error)
	RenewRestoreFence         func(context.Context) (bool, error)
}

type authoritativeAgentInputAcknowledgement struct {
	ProjectID            string
	IntentKey            string
	AgentIncarnation     string
	LeaseToken           string
	AcknowledgementToken string
}

var errAuthoritativeAgentInputUnavailable = errors.New("authoritative agent input receiver unavailable")

const (
	agentInputSessionLeaseDuration  = 30 * time.Second
	agentInputSessionLeaseHeartbeat = 10 * time.Second
)

type agentInputRefusalError struct {
	outcome     string
	safeToRetry bool
	cause       error
}

func (e agentInputRefusalError) Error() string {
	return "authoritative agent input refused: " + e.outcome
}

func (e agentInputRefusalError) Unwrap() error { return e.cause }

type unavailableAgentInputReceiver struct{}

func (unavailableAgentInputReceiver) DeliverAgentInput(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	return authoritativeAgentInputAcknowledgement{}, errAuthoritativeAgentInputUnavailable
}

type agentInputDeliveryService struct {
	stores                func(string) *state.RuntimeStateStore
	issueClients          func(string) *issues.Client
	receiver              authoritativeAgentInputReceiver
	owner                 string
	now                   func() time.Time
	sessionLeaseDuration  time.Duration
	sessionLeaseHeartbeat time.Duration
	deliveryEligible      func(context.Context, domain.AgentInputDeliveryRequest) (bool, error)
}

func newAgentInputDeliveryService(stores func(string) *state.RuntimeStateStore, issueClients func(string) *issues.Client, receiver authoritativeAgentInputReceiver, owner string) *agentInputDeliveryService {
	if receiver == nil {
		receiver = unavailableAgentInputReceiver{}
	}
	if strings.TrimSpace(owner) == "" {
		owner = "daemon-agent-input"
	}
	return &agentInputDeliveryService{stores: stores, issueClients: issueClients, receiver: receiver, owner: owner, now: func() time.Time { return time.Now().UTC() }, sessionLeaseDuration: agentInputSessionLeaseDuration, sessionLeaseHeartbeat: agentInputSessionLeaseHeartbeat}
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
	if intent.State == "ambiguous" {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "app-server submission acceptance is ambiguous; automatic retry is disabled"}, nil
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
	if s.deliveryEligible != nil {
		eligible, eligibilityErr := s.deliveryEligible(ctx, request)
		if eligibilityErr != nil {
			release(false)
			return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "validate delivery eligibility"}, eligibilityErr
		}
		if !eligible {
			release(true)
			return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "delivery action is no longer current"}, nil
		}
	}
	leaseNow := s.now()
	sessionLease, sessionLeaseAcquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, request.ProjectID, request.SessionID, request.Target.AgentIncarnation, s.owner, leaseNow, s.sessionLeaseDuration)
	if err != nil {
		release(false)
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "claim durable session delivery lease"}, err
	}
	if !sessionLeaseAcquired {
		release(false)
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "session delivery owned by another daemon"}, nil
	}
	deliveryCtx, cancelDelivery := context.WithCancelCause(ctx)
	releaseSessionLease := true
	var sessionFenceMu sync.Mutex
	currentSessionFence := sessionLease
	setSessionFence := func(lease issues.AgentInputDeliverySessionLease) {
		currentSessionFence = lease
	}
	leaseBoundContext := func(parent context.Context) (context.Context, context.CancelFunc) {
		expires := currentSessionFence.LeaseExpires
		return context.WithDeadline(parent, expires)
	}
	renewSessionFence := func(parent context.Context) (time.Time, bool, error) {
		sessionFenceMu.Lock()
		defer sessionFenceMu.Unlock()
		renewCtx, cancelRenew := leaseBoundContext(parent)
		defer cancelRenew()
		expires, renewed, renewErr := client.RenewAgentInputDeliverySessionLease(renewCtx, request.ProjectID, request.SessionID, currentSessionFence.AgentIncarnation, currentSessionFence.LeaseOwner, currentSessionFence.LeaseToken, s.now(), s.sessionLeaseDuration)
		if renewed {
			currentSessionFence.LeaseExpires = expires
		}
		return expires, renewed, renewErr
	}
	heartbeatStop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(s.sessionLeaseHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-deliveryCtx.Done():
				return
			case <-ticker.C:
				_, renewed, renewErr := renewSessionFence(deliveryCtx)
				if renewErr != nil || !renewed {
					if renewErr == nil {
						renewErr = errors.New("session delivery lease changed")
					}
					cancelDelivery(fmt.Errorf("renew durable session delivery lease: %w", renewErr))
					return
				}
			}
		}
	}()
	defer func() {
		cancelDelivery(nil)
		close(heartbeatStop)
		<-heartbeatDone
		if releaseSessionLease {
			sessionFenceMu.Lock()
			fence := currentSessionFence
			sessionFenceMu.Unlock()
			_ = client.ReleaseAgentInputDeliverySessionLease(context.WithoutCancel(ctx), request.ProjectID, request.SessionID, fence.AgentIncarnation, fence.LeaseOwner, fence.LeaseToken)
		}
	}()
	current, result, err := s.observeIdentity(ctx, request)
	if err != nil || result.Outcome != "" {
		release(result.Outcome == domain.AgentInputRejectedStaleTarget)
		return result, err
	}
	submissionBegun := false
	completeSessionTakeover := func(takeoverCtx context.Context) (issues.AgentInputDeliverySessionLease, error) {
		sessionFenceMu.Lock()
		defer sessionFenceMu.Unlock()
		if !currentSessionFence.TakeoverPending {
			return currentSessionFence, nil
		}
		takeoverLease, completed, err := client.CompleteAgentInputDeliverySessionLeaseTakeover(takeoverCtx, request.ProjectID, request.SessionID, currentSessionFence.AgentIncarnation, currentSessionFence.LeaseToken, request.Target.AgentIncarnation, s.owner, s.now(), s.sessionLeaseDuration)
		if err != nil {
			return issues.AgentInputDeliverySessionLease{}, fmt.Errorf("complete durable session fence takeover: %w", err)
		}
		if !completed {
			return issues.AgentInputDeliverySessionLease{}, errors.New("durable session fence changed before takeover completion")
		}
		setSessionFence(takeoverLease)
		return takeoverLease, nil
	}
	beginSubmission := func(beginCtx context.Context) (time.Time, error) {
		if s.deliveryEligible != nil {
			eligible, eligibilityErr := s.deliveryEligible(beginCtx, request)
			if eligibilityErr != nil {
				return time.Time{}, fmt.Errorf("validate delivery eligibility before submission: %w", eligibilityErr)
			}
			if !eligible {
				return time.Time{}, agentInputRefusalError{outcome: "superseded", safeToRetry: true}
			}
		}
		currentAgain, result, err := s.observeIdentity(beginCtx, request)
		if err != nil {
			return time.Time{}, err
		}
		if result.Outcome != "" || !current.SameIncarnation(currentAgain) {
			return time.Time{}, agentInputRefusalError{outcome: "stale_incarnation"}
		}
		sessionFenceMu.Lock()
		defer sessionFenceMu.Unlock()
		fenceCtx, cancelFence := leaseBoundContext(beginCtx)
		expires, begun, err := client.BeginAgentInputDeliverySubmission(fenceCtx, request.ProjectID, request.IntentKey, claimed.LeaseToken, request.SessionID, currentSessionFence.AgentIncarnation, currentSessionFence.LeaseOwner, currentSessionFence.LeaseToken, s.now(), s.sessionLeaseDuration)
		cancelFence()
		if err != nil {
			return time.Time{}, fmt.Errorf("persist app-server submission boundary: %w", err)
		}
		if !begun {
			return time.Time{}, errors.New("delivery or session fence changed before app-server submission")
		}
		submissionBegun = true
		currentSessionFence.LeaseExpires = expires
		return expires, nil
	}
	revalidateSubmissionFence := func(revalidateCtx context.Context) (time.Time, error) {
		if s.deliveryEligible != nil {
			eligible, eligibilityErr := s.deliveryEligible(revalidateCtx, request)
			if eligibilityErr != nil {
				return time.Time{}, fmt.Errorf("revalidate delivery eligibility: %w", eligibilityErr)
			}
			if !eligible {
				return time.Time{}, agentInputRefusalError{outcome: "superseded", safeToRetry: true}
			}
		}
		currentAgain, result, err := s.observeIdentity(revalidateCtx, request)
		if err != nil {
			return time.Time{}, err
		}
		if result.Outcome == domain.AgentInputRejectedStaleTarget || !current.SameIncarnation(currentAgain) {
			return time.Time{}, agentInputRefusalError{outcome: "stale_incarnation", safeToRetry: true}
		}
		if result.Outcome != "" {
			return time.Time{}, agentInputRefusalError{outcome: "not_ready", safeToRetry: true}
		}
		expires, renewed, err := renewSessionFence(revalidateCtx)
		if err != nil {
			return time.Time{}, fmt.Errorf("renew final submission fence: %w", err)
		}
		if !renewed {
			return time.Time{}, errors.New("session submission fence changed or expired")
		}
		return expires, nil
	}
	renewRestoreFence := func(restoreCtx context.Context) (bool, error) {
		_, renewed, err := renewSessionFence(restoreCtx)
		return renewed, err
	}
	ack, err := s.receiver.DeliverAgentInput(deliveryCtx, authoritativeAgentInputRequest{Delivery: request, LeaseToken: claimed.LeaseToken, SessionLeaseOwner: s.owner, SessionLeaseToken: sessionLease.LeaseToken, PreviousAgentIncarnation: sessionLease.PreviousAgentIncarnation, PreviousSessionLeaseToken: sessionLease.PreviousLeaseToken, CompleteSessionTakeover: completeSessionTakeover, BeginSubmission: beginSubmission, RevalidateSubmissionFence: revalidateSubmissionFence, RenewRestoreFence: renewRestoreFence})
	if err != nil {
		if errors.Is(err, errCodexGateRestoreIncomplete) {
			releaseSessionLease = false
		}
		var refusal agentInputRefusalError
		if errors.As(err, &refusal) {
			stale := refusal.outcome == "stale_incarnation" || refusal.outcome == "superseded"
			if refusal.safeToRetry && submissionBegun {
				_ = client.ResolveAgentInputDeliverySubmissionRefusal(context.WithoutCancel(ctx), request.ProjectID, request.IntentKey, claimed.LeaseToken, stale, s.now())
			} else if !submissionBegun {
				release(stale)
			}
			switch refusal.outcome {
			case "composer_nonempty":
				return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingInputNonempty, Reason: "managed input gate reports local input is not empty; intent remains queued"}, nil
			case "human_attached":
				return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingHumanAttached, Reason: "managed tmux gate could not exclude attached human input; intent remains queued"}, nil
			case "stale_incarnation", "superseded":
				return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "app-server gate rejected stale delivery authority"}, nil
			default:
				return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "app-server turn is not ready; intent remains queued"}, nil
			}
		}
		if !submissionBegun {
			release(false)
		}
		if errors.Is(err, errAuthoritativeAgentInputUnavailable) {
			return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "authoritative app-server and managed-input exclusion proof unavailable; intent remains queued"}, nil
		}
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "authoritative receiver failed"}, err
	}
	if !submissionBegun {
		release(false)
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "receiver skipped durable app-server submission boundary"}, nil
	}
	if ack.ProjectID != request.ProjectID || ack.IntentKey != request.IntentKey || ack.AgentIncarnation != request.Target.AgentIncarnation || ack.LeaseToken != claimed.LeaseToken || strings.TrimSpace(ack.AcknowledgementToken) == "" {
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
	var retryErrors []error
	for _, intent := range intents {
		if _, err := s.Deliver(ctx, intent.Request); err != nil && !errors.Is(err, errAuthoritativeAgentInputUnavailable) {
			retryErrors = append(retryErrors, fmt.Errorf("retry agent input intent %s: %w", intent.Request.IntentKey, err))
		}
	}
	return errors.Join(retryErrors...)
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
