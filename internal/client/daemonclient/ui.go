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
