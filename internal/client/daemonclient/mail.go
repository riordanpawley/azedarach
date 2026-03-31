package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) MailSend(ctx context.Context, body protocol.MailSendCommandBody) (protocol.MailEvent, error) {
	var out protocol.MailEvent
	if err := c.commandJSON(ctx, protocol.CommandMailSend, body, &out); err != nil {
		return protocol.MailEvent{}, err
	}
	return out, nil
}

func (c *Client) MailList(ctx context.Context, body protocol.MailListCommandBody) ([]protocol.MailEvent, error) {
	var out []protocol.MailEvent
	if err := c.commandJSON(ctx, protocol.CommandMailList, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) MailWatch(ctx context.Context, body protocol.MailWatchCommandBody) ([]protocol.MailEvent, error) {
	var out []protocol.MailEvent
	if err := c.commandJSON(ctx, protocol.CommandMailWatch, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
