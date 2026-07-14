package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) ValidationAcquire(ctx context.Context, req protocol.ValidationAcquireRequest) (protocol.ValidationRequestResponse, error) {
	var out protocol.ValidationRequestResponse
	if err := c.commandJSON(ctx, protocol.CommandValidationAcquire, req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) ValidationHeartbeat(ctx context.Context, req protocol.ValidationHeartbeatRequest) (protocol.ValidationRequestResponse, error) {
	var out protocol.ValidationRequestResponse
	if err := c.commandJSON(ctx, protocol.CommandValidationHeartbeat, req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) ValidationFinish(ctx context.Context, req protocol.ValidationFinishRequest) (protocol.ValidationRequestResponse, error) {
	var out protocol.ValidationRequestResponse
	if err := c.commandJSON(ctx, protocol.CommandValidationFinish, req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) ValidationStatus(ctx context.Context) (protocol.ValidationStatusResponse, error) {
	var out protocol.ValidationStatusResponse
	if err := c.commandJSON(ctx, protocol.CommandValidationStatus, protocol.ValidationStatusRequest{}, &out); err != nil {
		return out, err
	}
	return out, nil
}
