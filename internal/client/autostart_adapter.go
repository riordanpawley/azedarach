package client

import (
	"context"
	"fmt"

	"github.com/riordanpawley/azedarach/internal/client/compatibility"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

// DaemonHandshaker adapts daemonclient.Client to AutostartOrchestrator handshaker contract.
type DaemonHandshaker struct {
	client *daemonclient.Client
}

// NewDaemonHandshaker returns an autostart-compatible handshaker wrapper.
func NewDaemonHandshaker(client *daemonclient.Client) DaemonHandshaker {
	return DaemonHandshaker{client: client}
}

// Handshake returns transport/connect failures as errors and protocol mismatches in ack.
func (h DaemonHandshaker) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	ack, diag := h.client.Handshake(ctx, hello)
	if diag == nil {
		return ack, nil
	}
	if diag.Code == protocol.ErrorCodeUnavailable {
		if diag.Err != nil {
			return ack, diag.Err
		}
		return ack, fmt.Errorf("daemon unavailable")
	}
	if diag.Err != nil && diag.Err == compatibility.ErrOffline {
		return ack, diag.Err
	}
	return ack, nil
}
