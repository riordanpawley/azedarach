package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) FanoutPlan(ctx context.Context, body protocol.FanoutCommandBody) (protocol.FanoutPlan, error) {
	var out protocol.FanoutPlan
	body.Apply = false
	if err := c.commandJSON(ctx, protocol.CommandIssueFanout, body, &out); err != nil {
		return protocol.FanoutPlan{}, err
	}
	return out, nil
}

func (c *Client) FanoutApply(ctx context.Context, body protocol.FanoutCommandBody) (protocol.FanoutApplyResult, error) {
	var out protocol.FanoutApplyResult
	body.Apply = true
	if err := c.commandJSON(ctx, protocol.CommandIssueFanout, body, &out); err != nil {
		return protocol.FanoutApplyResult{}, err
	}
	return out, nil
}

func (c *Client) FanoutDrift(ctx context.Context, body protocol.FanoutDriftCommandBody) (protocol.FanoutDriftResult, error) {
	var out protocol.FanoutDriftResult
	if err := c.commandJSON(ctx, protocol.CommandIssueFanoutDrift, body, &out); err != nil {
		return protocol.FanoutDriftResult{}, err
	}
	return out, nil
}
