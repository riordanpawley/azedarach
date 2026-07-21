package domain

import "time"

// AgentInputMessageKind identifies the semantic class of automated input.
// Text and control keys deliberately use separate delivery paths.
type AgentInputMessageKind string

const (
	AgentInputMessageSessionMessage   AgentInputMessageKind = "session_message"
	AgentInputMessageOrchestratorWake AgentInputMessageKind = "orchestrator_wake"
	AgentInputMessageDecisionChange   AgentInputMessageKind = "decision_change"
	AgentInputMessageRootedBootstrap  AgentInputMessageKind = "rooted_bootstrap"
)

// AgentInputDeliveryOutcome is safe to expose in diagnostics. It never carries
// composer or message content.
type AgentInputDeliveryOutcome string

const (
	AgentInputDelivered            AgentInputDeliveryOutcome = "delivered"
	AgentInputWaitingNotReady      AgentInputDeliveryOutcome = "waiting_not_ready"
	AgentInputWaitingInputNonempty AgentInputDeliveryOutcome = "waiting_input_nonempty"
	AgentInputWaitingHumanAttached AgentInputDeliveryOutcome = "waiting_human_attached"
	AgentInputRejectedStaleTarget  AgentInputDeliveryOutcome = "rejected_stale_target"
	AgentInputExpired              AgentInputDeliveryOutcome = "expired"
	AgentInputFailed               AgentInputDeliveryOutcome = "failed"
)

type AgentInputDeliveryRequest struct {
	ProjectID string
	SessionID string
	Target    ManagedAgentRuntimeIdentity
	Tool      string
	Kind      AgentInputMessageKind
	Payload   string
	IntentKey string
	ExpiresAt time.Time
}

type AgentInputDeliveryResult struct {
	Outcome AgentInputDeliveryOutcome
	Reason  string
}
