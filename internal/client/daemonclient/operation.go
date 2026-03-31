package daemonclient

import (
	"context"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const defaultOperationPollInterval = 250 * time.Millisecond

type OperationListOptions struct {
	IssueID string
	Kind    string
	States  []protocol.OperationState
	Limit   int
}

func (c *Client) GetOperation(ctx context.Context, operationID string) (protocol.OperationRecord, error) {
	var out protocol.OperationGetResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationGet, protocol.OperationGetRequestBody{
		ProjectID:   c.projectRoute(),
		OperationID: operationID,
	}, &out); err != nil {
		return protocol.OperationRecord{}, err
	}
	return out.Operation, nil
}

func (c *Client) ListOperations(ctx context.Context, opts OperationListOptions) ([]protocol.OperationRecord, error) {
	var out protocol.OperationListResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationList, protocol.OperationListRequestBody{
		ProjectID: c.projectRoute(),
		IssueID:   opts.IssueID,
		Kind:      opts.Kind,
		States:    opts.States,
		Limit:     opts.Limit,
	}, &out); err != nil {
		return nil, err
	}
	return out.Operations, nil
}

func (c *Client) CancelOperation(ctx context.Context, operationID, reason string) (protocol.OperationRecord, error) {
	var out protocol.OperationCancelResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationCancel, protocol.OperationCancelRequestBody{
		ProjectID:   c.projectRoute(),
		OperationID: operationID,
		Reason:      reason,
	}, &out); err != nil {
		return protocol.OperationRecord{}, err
	}
	return out.Operation, nil
}

func (c *Client) WaitForOperation(ctx context.Context, operationID string, pollInterval time.Duration) (protocol.OperationRecord, error) {
	if pollInterval <= 0 {
		pollInterval = defaultOperationPollInterval
	}

	for {
		record, err := c.GetOperation(ctx, operationID)
		if err != nil {
			return protocol.OperationRecord{}, err
		}
		if isTerminalOperationState(record.State) {
			return record, nil
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return protocol.OperationRecord{}, ctx.Err()
		case <-timer.C:
		}
	}
}
