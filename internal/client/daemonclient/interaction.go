package daemonclient

import (
	"context"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	CommandInteractionCreate    = protocol.CommandInteractionCreate
	CommandInteractionList      = protocol.CommandInteractionList
	CommandInteractionGet       = protocol.CommandInteractionGet
	CommandInteractionDiscuss   = protocol.CommandInteractionDiscuss
	CommandInteractionPropose   = protocol.CommandInteractionPropose
	CommandInteractionAnswer    = protocol.CommandInteractionAnswer
	CommandInteractionResolve   = protocol.CommandInteractionResolve
	CommandInteractionWithdraw  = protocol.CommandInteractionWithdraw
	CommandInteractionSupersede = protocol.CommandInteractionSupersede
	CommandInteractionRecover   = protocol.CommandInteractionRecover
)

func (c *Client) CreateInteraction(ctx context.Context, in protocol.InteractionCreateRequestBody) (protocol.InteractionResponseBody, error) {
	var out protocol.InteractionResponseBody
	err := c.commandJSON(ctx, CommandInteractionCreate, in, &out)
	return out, err
}
func (c *Client) ListInteractions(ctx context.Context, in protocol.InteractionListRequestBody) (protocol.InteractionListResponseBody, error) {
	var out protocol.InteractionListResponseBody
	err := c.commandJSON(ctx, CommandInteractionList, in, &out)
	return out, err
}
func (c *Client) GetInteraction(ctx context.Context, id string) (protocol.InteractionResponseBody, error) {
	var out protocol.InteractionResponseBody
	err := c.commandJSON(ctx, CommandInteractionGet, protocol.InteractionGetRequestBody{ID: id}, &out)
	return out, err
}
func (c *Client) MutateInteraction(ctx context.Context, command string, in protocol.InteractionMutationRequestBody) (protocol.InteractionResponseBody, error) {
	var out protocol.InteractionResponseBody
	err := c.commandJSON(ctx, command, in, &out)
	return out, err
}
func (c *Client) ResolveInteraction(ctx context.Context, in protocol.InteractionResolveRequestBody) (protocol.InteractionResponseBody, error) {
	var out protocol.InteractionResponseBody
	err := c.commandJSON(ctx, CommandInteractionResolve, in, &out)
	return out, err
}
