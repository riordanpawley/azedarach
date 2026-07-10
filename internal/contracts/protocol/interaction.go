package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	CommandInteractionCreate    = "interaction.create"
	CommandInteractionList      = "interaction.list"
	CommandInteractionGet       = "interaction.get"
	CommandInteractionDiscuss   = "interaction.discuss"
	CommandInteractionPropose   = "interaction.propose"
	CommandInteractionAnswer    = "interaction.answer"
	CommandInteractionResolve   = "interaction.resolve"
	CommandInteractionWithdraw  = "interaction.withdraw"
	CommandInteractionSupersede = "interaction.supersede"
	CommandInteractionRecover   = "interaction.recover"
	EventInteractionResolved    = "interaction.resolved"
	EventInteractionStale       = "interaction.stale"
	EventInteractionReminder    = "interaction.reminder"
	EventInteractionWithdrawn   = "interaction.withdrawn"
	EventInteractionSuperseded  = "interaction.superseded"
	EventInteractionRecovered   = "interaction.recovered"
)

type InteractionCreateRequestBody struct {
	Request domain.InteractionRequest `json:"request" msgpack:"request"`
}
type InteractionListRequestBody struct {
	IssueID string `json:"issue_id,omitempty" msgpack:"issue_id,omitempty"`
}
type InteractionGetRequestBody struct {
	ID string `json:"id" msgpack:"id"`
}
type InteractionMutationRequestBody struct {
	ID               string                          `json:"id" msgpack:"id"`
	ExpectedRevision int64                           `json:"expected_revision" msgpack:"expected_revision"`
	Answer           domain.InteractionAnswerPayload `json:"answer,omitempty" msgpack:"answer,omitempty"`
	Actor            string                          `json:"actor" msgpack:"actor"`
	SessionID        string                          `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
	Reason           string                          `json:"reason,omitempty" msgpack:"reason,omitempty"`
	ReplacementID    string                          `json:"replacement_id,omitempty" msgpack:"replacement_id,omitempty"`
}
type InteractionDecisionEffect = domain.InteractionDecisionEffect
type InteractionResolveRequestBody struct {
	InteractionMutationRequestBody
	Decision *InteractionDecisionEffect `json:"decision,omitempty" msgpack:"decision,omitempty"`
}
type InteractionResponseBody struct {
	Request         domain.InteractionRequest `json:"request" msgpack:"request"`
	SessionStarted  bool                      `json:"session_started,omitempty" msgpack:"session_started,omitempty"`
	SessionAttached bool                      `json:"session_attached,omitempty" msgpack:"session_attached,omitempty"`
	SessionResumed  bool                      `json:"session_resumed,omitempty" msgpack:"session_resumed,omitempty"`
	Age             domain.InteractionAgeView `json:"age" msgpack:"age"`
}
type InteractionListResponseBody struct {
	Requests []domain.InteractionRequest          `json:"requests" msgpack:"requests"`
	Ages     map[string]domain.InteractionAgeView `json:"ages" msgpack:"ages"`
}

type InteractionLifecycleEventBody struct {
	ID            string    `json:"id" msgpack:"id"`
	IssueID       string    `json:"issue_id" msgpack:"issue_id"`
	Revision      int64     `json:"revision" msgpack:"revision"`
	Sequence      int       `json:"sequence,omitempty" msgpack:"sequence,omitempty"`
	ReplacementID string    `json:"replacement_id,omitempty" msgpack:"replacement_id,omitempty"`
	OccurredAt    time.Time `json:"occurred_at" msgpack:"occurred_at"`
}
type InteractionResolvedEventBody struct {
	ID         string    `json:"id" msgpack:"id"`
	IssueID    string    `json:"issue_id" msgpack:"issue_id"`
	Revision   int64     `json:"revision" msgpack:"revision"`
	ResolvedAt time.Time `json:"resolved_at" msgpack:"resolved_at"`
}
