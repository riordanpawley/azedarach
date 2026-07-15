package daemon

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type agentInputDeliveryService struct {
	tmux      *tmux.Client
	stores    func(string) *state.RuntimeStateStore
	mu        sync.Mutex
	panes     map[string]*sync.Mutex
	delivered map[string]struct{}
}

func newAgentInputDeliveryService(client *tmux.Client, stores func(string) *state.RuntimeStateStore) *agentInputDeliveryService {
	return &agentInputDeliveryService{tmux: client, stores: stores, panes: map[string]*sync.Mutex{}, delivered: map[string]struct{}{}}
}

func (s *agentInputDeliveryService) paneLease(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease := s.panes[key]; lease != nil {
		return lease
	}
	lease := &sync.Mutex{}
	s.panes[key] = lease
	return lease
}

func (s *agentInputDeliveryService) Deliver(ctx context.Context, request domain.AgentInputDeliveryRequest) (domain.AgentInputDeliveryResult, error) {
	if request.ExpiresAt.IsZero() == false && !time.Now().Before(request.ExpiresAt) {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputExpired, Reason: "delivery expired"}, nil
	}
	if err := request.Target.Validate(); err != nil || strings.TrimSpace(request.Payload) == "" || strings.TrimSpace(request.IntentKey) == "" {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "invalid delivery request"}, err
	}
	lease := s.paneLease(request.ProjectID + "\x00" + request.SessionID + "\x00" + string(request.Target.LogicalPaneID))
	lease.Lock()
	defer lease.Unlock()
	if !request.ExpiresAt.IsZero() && !time.Now().Before(request.ExpiresAt) {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputExpired, Reason: "delivery expired"}, nil
	}
	dedupe := request.Kind != domain.AgentInputMessageDecisionChange
	s.mu.Lock()
	_, duplicate := s.delivered[request.ProjectID+"\x00"+request.IntentKey]
	s.mu.Unlock()
	if dedupe && duplicate {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputDelivered, Reason: "intent already delivered"}, nil
	}

	current, observation, result, err := s.observe(ctx, request)
	if err != nil || result.Outcome != "" {
		return result, err
	}
	if observation.AttachedCount > 0 {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingHumanAttached, Reason: "tmux client attached"}, nil
	}
	if !agentComposerEmpty(request.Tool, observation.Capture) {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingInputNonempty, Reason: "empty composer not proven"}, nil
	}
	if !agentCommandMatches(request.Tool, observation.CurrentCommand) {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "target command does not match receiver tool"}, nil
	}
	currentAgain, observationAgain, result, err := s.observe(ctx, request)
	if err != nil || result.Outcome != "" {
		return result, err
	}
	if !current.SameIncarnation(currentAgain) || observationAgain.AttachedCount > 0 || observation.Capture != observationAgain.Capture || !agentComposerEmpty(request.Tool, observationAgain.Capture) || !agentCommandMatches(request.Tool, observationAgain.CurrentCommand) {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "target changed during delivery proof"}, nil
	}
	if err := s.tmux.PasteAgentTextAndSubmit(ctx, request.Target.TmuxPaneID, request.Payload); err != nil {
		return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "tmux delivery failed"}, err
	}
	if dedupe {
		s.mu.Lock()
		s.delivered[request.ProjectID+"\x00"+request.IntentKey] = struct{}{}
		s.mu.Unlock()
	}
	return domain.AgentInputDeliveryResult{Outcome: domain.AgentInputDelivered}, nil
}

func (s *agentInputDeliveryService) observe(ctx context.Context, request domain.AgentInputDeliveryRequest) (domain.ManagedAgentRuntimeIdentity, tmux.AgentInputTargetObservation, domain.AgentInputDeliveryResult, error) {
	store := s.stores(request.ProjectID)
	if store == nil {
		return domain.ManagedAgentRuntimeIdentity{}, tmux.AgentInputTargetObservation{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "runtime store unavailable"}, nil
	}
	identity, found, err := store.GetManagedAgentIdentity(ctx, request.ProjectID, request.SessionID, string(request.Target.LogicalPaneID))
	if err != nil {
		return domain.ManagedAgentRuntimeIdentity{}, tmux.AgentInputTargetObservation{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "identity lookup failed"}, err
	}
	current := domain.ManagedAgentRuntimeIdentity{LogicalPaneID: domain.ManagedAgentPaneID(identity.LogicalPaneID), TmuxPaneID: identity.TmuxPaneID, PanePID: identity.PanePID, AgentIncarnation: identity.AgentIncarnation}
	if !found || !request.Target.SameIncarnation(current) {
		return current, tmux.AgentInputTargetObservation{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "managed agent incarnation changed"}, nil
	}
	activity, found, err := store.GetPhysicalSessionObservation(ctx, request.ProjectID, request.SessionID)
	if err != nil {
		return current, tmux.AgentInputTargetObservation{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputFailed, Reason: "activity lookup failed"}, err
	}
	if !found || !strings.EqualFold(activity.ActivitySource, "hooks") || (activity.Activity != "idle" && activity.Activity != "waiting") {
		return current, tmux.AgentInputTargetObservation{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputWaitingNotReady, Reason: "hook-backed receiver readiness not proven"}, nil
	}
	observation, err := s.tmux.ObserveAgentInputTarget(ctx, request.SessionID, current.TmuxPaneID)
	if err != nil {
		return current, tmux.AgentInputTargetObservation{}, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "live pane identity unavailable"}, nil
	}
	if observation.Pane.PanePID != current.PanePID {
		return current, observation, domain.AgentInputDeliveryResult{Outcome: domain.AgentInputRejectedStaleTarget, Reason: "tmux pane process changed"}, nil
	}
	return current, observation, domain.AgentInputDeliveryResult{}, nil
}

func agentComposerEmpty(tool, capture string) bool {
	lines := strings.Split(strings.ReplaceAll(capture, "\r", ""), "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = strings.TrimSpace(lines[i])
			break
		}
	}
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "codex", "codex-app-server":
		return last == "›" || last == ">"
	case "claude", "opencode":
		return last == ">" || last == "❯"
	case "sh", "bash", "zsh", "fish":
		return last == "$" || last == "%" || last == "#" || last == ">"
	default:
		return false
	}
}

func agentCommandMatches(tool, command string) bool {
	tool = strings.ToLower(strings.TrimSpace(tool))
	command = strings.ToLower(strings.TrimSpace(command))
	switch tool {
	case "codex", "codex-app-server":
		return command == "codex" || command == "codex-app-server"
	case "claude":
		return command == "claude"
	case "opencode":
		return command == "opencode"
	case "sh", "bash", "zsh", "fish":
		return command == tool
	default:
		return false
	}
}

func (d *Daemon) agentInputService() *agentInputDeliveryService {
	return d.agentInput
}

func (d *Daemon) currentAgentInputTarget(ctx context.Context, projectID, sessionID string) (domain.ManagedAgentRuntimeIdentity, bool, error) {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return domain.ManagedAgentRuntimeIdentity{}, false, nil
	}
	identity, found, err := store.GetManagedAgentIdentity(ctx, projectID, sessionID, "agent")
	return domain.ManagedAgentRuntimeIdentity{LogicalPaneID: domain.ManagedAgentPaneID(identity.LogicalPaneID), TmuxPaneID: identity.TmuxPaneID, PanePID: identity.PanePID, AgentIncarnation: identity.AgentIncarnation}, found, err
}
