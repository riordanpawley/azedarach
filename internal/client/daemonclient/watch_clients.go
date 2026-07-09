package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const CommandDaemonWatchClients = protocol.CommandDaemonWatchClients

func (c *Client) DaemonWatchClients(ctx context.Context, body protocol.DaemonWatchClientsCommandBody) (protocol.DaemonWatchClientsResult, error) {
	var out protocol.DaemonWatchClientsResult
	if err := c.commandJSON(ctx, protocol.CommandDaemonWatchClients, body, &out); err != nil {
		return protocol.DaemonWatchClientsResult{}, err
	}
	return out, nil
}
