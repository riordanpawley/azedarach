package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	CommandInteractionCreate   = "interaction.create"
	CommandInteractionList     = "interaction.list"
	CommandInteractionGet      = "interaction.get"
	CommandInteractionDiscuss  = "interaction.discuss"
	CommandInteractionPropose  = "interaction.propose"
	CommandInteractionAnswer   = "interaction.answer"
	CommandInteractionResolve  = "interaction.resolve"
	CommandInteractionWithdraw = "interaction.withdraw"
	EventInteractionResolved   = "interaction.resolved"
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
}
type InteractionListResponseBody struct {
	Requests []domain.InteractionRequest `json:"requests" msgpack:"requests"`
}
type InteractionResolvedEventBody struct {
	ID         string    `json:"id" msgpack:"id"`
	IssueID    string    `json:"issue_id" msgpack:"issue_id"`
	Revision   int64     `json:"revision" msgpack:"revision"`
	ResolvedAt time.Time `json:"resolved_at" msgpack:"resolved_at"`
}
