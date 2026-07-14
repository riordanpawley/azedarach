package daemonclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) GlobalSnapshot(ctx context.Context, query string) (protocol.GlobalSnapshotResponseBody, error) {
	return c.GlobalViewSnapshot(ctx, protocol.GlobalSnapshotRequestBody{Query: query})
}

func (c *Client) GlobalViewSnapshot(ctx context.Context, request protocol.GlobalSnapshotRequestBody) (protocol.GlobalSnapshotResponseBody, error) {
	resp, err := c.commandJSONResponse(ctx, protocol.CommandGlobalSnapshot, request)
	if err != nil {
		return protocol.GlobalSnapshotResponseBody{}, err
	}
	var out protocol.GlobalSnapshotResponseBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return out, fmt.Errorf("decode global snapshot: %w", err)
	}
	if out.SchemaVersion != protocol.GlobalProjectionSchemaVersion {
		return out, fmt.Errorf("global snapshot schema version %d, want %d", out.SchemaVersion, protocol.GlobalProjectionSchemaVersion)
	}
	return out, nil
}

func (c *Client) RebuildGlobalProjection(ctx context.Context) (protocol.GlobalSnapshotResponseBody, error) {
	var submitted protocol.OperationSubmitResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{ProjectID: c.projectID, Kind: protocol.CommandGlobalProjectionRebuild}, &submitted); err != nil {
		return protocol.GlobalSnapshotResponseBody{}, err
	}
	record := submitted.Operation
	var err error
	if !isTerminalOperationState(record.State) {
		record, err = c.WaitForOperation(ctx, record.OperationID.String(), 0)
		if err != nil {
			return protocol.GlobalSnapshotResponseBody{}, fmt.Errorf("wait for global projection rebuild %s: %w", record.OperationID, err)
		}
	}
	if record.State != protocol.OperationStateDone {
		if record.Error != nil {
			return protocol.GlobalSnapshotResponseBody{}, fmt.Errorf("global projection rebuild %s: %s", record.State, record.Error.Message)
		}
		return protocol.GlobalSnapshotResponseBody{}, fmt.Errorf("global projection rebuild %s", record.State)
	}
	var out protocol.GlobalSnapshotResponseBody
	if err = json.Unmarshal(record.Result, &out); err != nil {
		return out, fmt.Errorf("decode global projection rebuild operation %s: %w", record.OperationID, err)
	}
	if out.SchemaVersion != protocol.GlobalProjectionSchemaVersion {
		return out, fmt.Errorf("global projection rebuild schema version %d, want %d", out.SchemaVersion, protocol.GlobalProjectionSchemaVersion)
	}
	return out, nil
}
