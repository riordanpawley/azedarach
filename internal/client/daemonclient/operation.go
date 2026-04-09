package daemonclient

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const defaultOperationPollInterval = 250 * time.Millisecond

type OperationListOptions struct {
	IssueID string
	Kind    string
	States  []protocol.OperationState
	Limit   int
}

func (c *Client) GetOperation(ctx context.Context, operationID string) (protocol.OperationRecord, error) {
	parsedOperationID, err := naming.ParseOperationID(operationID)
	if err != nil {
		return protocol.OperationRecord{}, fmt.Errorf("invalid operation id: %w", err)
	}
	var out protocol.OperationGetResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationGet, protocol.OperationGetRequestBody{
		ProjectID:   c.projectID,
		OperationID: parsedOperationID,
	}, &out); err != nil {
		return protocol.OperationRecord{}, err
	}
	return out.Operation, nil
}

func (c *Client) ListOperations(ctx context.Context, opts OperationListOptions) ([]protocol.OperationRecord, error) {
	var issueID naming.IssueID
	if trimmed := strings.TrimSpace(opts.IssueID); trimmed != "" {
		parsedIssueID, err := naming.ParseIssueID(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid issue id: %w", err)
		}
		issueID = parsedIssueID
	}
	var out protocol.OperationListResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationList, protocol.OperationListRequestBody{
		ProjectID: c.projectID,
		IssueID:   issueID,
		Kind:      opts.Kind,
		States:    opts.States,
		Limit:     opts.Limit,
	}, &out); err != nil {
		return nil, err
	}
	return out.Operations, nil
}

func (c *Client) CancelOperation(ctx context.Context, operationID, reason string) (protocol.OperationRecord, error) {
	parsedOperationID, err := naming.ParseOperationID(operationID)
	if err != nil {
		return protocol.OperationRecord{}, fmt.Errorf("invalid operation id: %w", err)
	}
	var out protocol.OperationCancelResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationCancel, protocol.OperationCancelRequestBody{
		ProjectID:   c.projectID,
		OperationID: parsedOperationID,
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
