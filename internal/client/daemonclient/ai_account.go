package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) BackupAIAccount(ctx context.Context, req protocol.AIAccountBackupRequestBody) (protocol.AIAccountBackupResponseBody, error) {
	var out protocol.AIAccountBackupResponseBody
	if err := c.commandJSON(ctx, protocol.CommandAIAccountBackup, req, &out); err != nil {
		return protocol.AIAccountBackupResponseBody{}, err
	}
	return out, nil
}

func (c *Client) ListAIAccounts(ctx context.Context, req protocol.AIAccountListRequestBody) (protocol.AIAccountListResponseBody, error) {
	var out protocol.AIAccountListResponseBody
	if err := c.commandJSON(ctx, protocol.CommandAIAccountList, req, &out); err != nil {
		return protocol.AIAccountListResponseBody{}, err
	}
	return out, nil
}

func (c *Client) StatusAIAccounts(ctx context.Context, req protocol.AIAccountStatusRequestBody) (protocol.AIAccountStatusResponseBody, error) {
	var out protocol.AIAccountStatusResponseBody
	if err := c.commandJSON(ctx, protocol.CommandAIAccountStatus, req, &out); err != nil {
		return protocol.AIAccountStatusResponseBody{}, err
	}
	return out, nil
}

func (c *Client) ActivateAIAccount(ctx context.Context, req protocol.AIAccountActivateRequestBody) (protocol.AIAccountActivateResponseBody, error) {
	var out protocol.AIAccountActivateResponseBody
	if err := c.commandJSON(ctx, protocol.CommandAIAccountActivate, req, &out); err != nil {
		return protocol.AIAccountActivateResponseBody{}, err
	}
	return out, nil
}

func (c *Client) DeleteAIAccount(ctx context.Context, req protocol.AIAccountDeleteRequestBody) (protocol.AIAccountDeleteResponseBody, error) {
	var out protocol.AIAccountDeleteResponseBody
	if err := c.commandJSON(ctx, protocol.CommandAIAccountDelete, req, &out); err != nil {
		return protocol.AIAccountDeleteResponseBody{}, err
	}
	return out, nil
}
