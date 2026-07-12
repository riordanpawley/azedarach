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

func (c *Client) CaptureLearningObservation(ctx context.Context, req protocol.LearnCaptureRequestBody) (protocol.LearnCaptureResponseBody, error) {
	var out protocol.LearnCaptureResponseBody
	if err := c.commandJSON(ctx, protocol.CommandLearnCapture, req, &out); err != nil {
		return protocol.LearnCaptureResponseBody{}, err
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

func (c *Client) LearningHealth(ctx context.Context, req protocol.LearnHealthRequestBody) (protocol.LearnHealthResponseBody, error) {
	var out protocol.LearnHealthResponseBody
	if err := c.commandJSON(ctx, protocol.CommandLearnHealth, req, &out); err != nil {
		return out, err
	}
	return out, nil
}
