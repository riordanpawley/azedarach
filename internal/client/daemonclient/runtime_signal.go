package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func (c *Client) RuntimeSignalIngest(ctx context.Context, req protocol.RuntimeSignalIngestCommandBody) (protocol.RuntimeSignalIngestResponseBody, error) {
	var out protocol.RuntimeSignalIngestResponseBody
	if err := c.commandJSON(ctx, protocol.CommandRuntimeSignalIngest, req, &out); err != nil {
		return protocol.RuntimeSignalIngestResponseBody{}, err
	}
	return out, nil
}
