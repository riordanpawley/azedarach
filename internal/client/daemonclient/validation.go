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

func (c *Client) ValidationAuthorizeNested(ctx context.Context, req protocol.ValidationAuthorizeNestedRequest) (protocol.ValidationRequestResponse, error) {
	var out protocol.ValidationRequestResponse
	if err := c.commandJSON(ctx, protocol.CommandValidationNested, req, &out); err != nil {
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

func (c *Client) ValidationArtifactRead(ctx context.Context, req protocol.ValidationArtifactReadRequest) (protocol.ValidationArtifactReadResponse, error) {
	var out protocol.ValidationArtifactReadResponse
	if err := c.commandJSON(ctx, protocol.CommandValidationArtifactRead, req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) PublicationEvidenceRecord(ctx context.Context, req protocol.PublicationEvidenceRecordRequest) (protocol.PublicationEvidenceRecordResponse, error) {
	var out protocol.PublicationEvidenceRecordResponse
	if err := c.commandJSON(ctx, protocol.CommandPublicationEvidenceRecord, req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) PublicationEvidenceStatus(ctx context.Context, req protocol.PublicationEvidenceStatusRequest) (protocol.PublicationEvidenceStatusResponse, error) {
	var out protocol.PublicationEvidenceStatusResponse
	if err := c.commandJSON(ctx, protocol.CommandPublicationEvidenceStatus, req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) PublicationEvidenceEvaluate(ctx context.Context, req protocol.PublicationEvidenceEvaluateRequest) (protocol.PublicationEvidenceEvaluateResponse, error) {
	var out protocol.PublicationEvidenceEvaluateResponse
	if err := c.commandJSON(ctx, protocol.CommandPublicationEvidenceEvaluate, req, &out); err != nil {
		return out, err
	}
	return out, nil
}
