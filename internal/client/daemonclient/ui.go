package daemonclient

import (
	"context"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (c *Client) OpenTaskWorkspace(ctx context.Context, issueID naming.IssueID) (protocol.UICommandResponseBody, error) {
	body := protocol.UICommandRequestBody{
		IssueID: issueID,
		Command: protocol.UICommandOpenTaskWorkspace,
	}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	var out protocol.UICommandResponseBody
	if err := c.commandJSON(ctx, protocol.CommandUIOpenTaskWorkspace, body, &out); err != nil {
		return protocol.UICommandResponseBody{}, err
	}
	return out, nil
}

func (c *Client) GetUIState(ctx context.Context, key string) (protocol.UIStateResponseBody, error) {
	body := protocol.UIStateGetRequestBody{Key: strings.TrimSpace(key)}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	var out protocol.UIStateResponseBody
	if err := c.commandJSON(ctx, protocol.CommandUIStateGet, body, &out); err != nil {
		return protocol.UIStateResponseBody{}, err
	}
	return out, nil
}

func (c *Client) SetUIState(ctx context.Context, key, value string) (protocol.UIStateResponseBody, error) {
	body := protocol.UIStateSetRequestBody{
		Key:   strings.TrimSpace(key),
		Value: value,
	}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	var out protocol.UIStateResponseBody
	if err := c.commandJSON(ctx, protocol.CommandUIStateSet, body, &out); err != nil {
		return protocol.UIStateResponseBody{}, err
	}
	return out, nil
}
