package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) ActivateLearnings(ctx context.Context, req protocol.LearnActivateRequestBody) (protocol.LearnActivateResponseBody, error) {
	var out protocol.LearnActivateResponseBody
	if err := c.commandJSON(ctx, protocol.CommandLearnActivate, req, &out); err != nil {
		return protocol.LearnActivateResponseBody{}, err
	}
	return out, nil
}

func (c *Client) RecordLearningFeedback(ctx context.Context, req protocol.LearnFeedbackRequestBody) (protocol.LearnFeedbackResponseBody, error) {
	var out protocol.LearnFeedbackResponseBody
	if err := c.commandJSON(ctx, protocol.CommandLearnFeedback, req, &out); err != nil {
		return protocol.LearnFeedbackResponseBody{}, err
	}
	return out, nil
}

func (c *Client) ActivateContextualLearnings(ctx context.Context, req protocol.LearnContextualActivateRequestBody) (protocol.LearnContextualActivateResponseBody, error) {
	var out protocol.LearnContextualActivateResponseBody
	if err := c.commandJSON(ctx, protocol.CommandLearnContextualActivate, req, &out); err != nil {
		return protocol.LearnContextualActivateResponseBody{}, err
	}
	return out, nil
}
