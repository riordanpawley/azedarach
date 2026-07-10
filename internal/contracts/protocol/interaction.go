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
	ID               string `json:"id" msgpack:"id"`
	ExpectedRevision int64  `json:"expected_revision" msgpack:"expected_revision"`
	Answer           string `json:"answer,omitempty" msgpack:"answer,omitempty"`
	Actor            string `json:"actor" msgpack:"actor"`
	SessionID        string `json:"session_id,omitempty" msgpack:"session_id,omitempty"`
}
type InteractionIssueChanges struct {
	Title       *string `json:"title,omitempty" msgpack:"title,omitempty"`
	Description *string `json:"description,omitempty" msgpack:"description,omitempty"`
	Design      *string `json:"design,omitempty" msgpack:"design,omitempty"`
	Acceptance  *string `json:"acceptance,omitempty" msgpack:"acceptance,omitempty"`
	Priority    *int    `json:"priority,omitempty" msgpack:"priority,omitempty"`
}
type InteractionDecisionEffect struct {
	Title        string `json:"title" msgpack:"title"`
	Rationale    string `json:"rationale" msgpack:"rationale"`
	Context      string `json:"context,omitempty" msgpack:"context,omitempty"`
	Consequences string `json:"consequences,omitempty" msgpack:"consequences,omitempty"`
}
type InteractionResolveRequestBody struct {
	InteractionMutationRequestBody
	IssueChanges InteractionIssueChanges    `json:"issue_changes,omitempty" msgpack:"issue_changes,omitempty"`
	Decision     *InteractionDecisionEffect `json:"decision,omitempty" msgpack:"decision,omitempty"`
}
type InteractionResponseBody struct {
	Request domain.InteractionRequest `json:"request" msgpack:"request"`
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
