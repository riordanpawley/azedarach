package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) AppendHookLogEvent(ctx context.Context, evt protocol.HookLogEvent) (protocol.HookLogEvent, error) {
	var out protocol.HookLogEvent
	if err := c.commandJSON(ctx, protocol.CommandHookLogAppend, protocol.HookLogAppendCommandBody{Event: evt}, &out); err != nil {
		return protocol.HookLogEvent{}, err
	}
	return out, nil
}

func (c *Client) ListHookLogEvents(ctx context.Context, limit int) ([]protocol.HookLogEvent, error) {
	var out []protocol.HookLogEvent
	if err := c.commandJSON(ctx, protocol.CommandHookLogList, protocol.HookLogListCommandBody{Limit: limit}, &out); err != nil {
		return nil, err
	}
	return out, nil
}
