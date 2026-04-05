package daemonclient

import (
	"context"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/compatibility"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// TransportClient is the daemon RPC transport abstraction.
type TransportClient interface {
	Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error)
	Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error)
}

// Client is the shared thin daemon client used by CLI and TUI.
type Client struct {
	transport TransportClient
	policy    reconnect.Policy
	readWait  ReadWaitPolicy
	projectID string
}

// New returns a shared daemon client with default reconnect policy.
func New(transport TransportClient) *Client {
	return &Client{
		transport: transport,
		policy:    reconnect.DefaultPolicy(),
		readWait:  DefaultReadWaitPolicy(),
	}
}

// WithProjectID sets the default project route used for command metadata and fallback subscriptions.
func (c *Client) WithProjectID(projectID string) *Client {
	c.projectID = protocol.NormalizeProjectID(projectID)
	return c
}

// WithReconnectPolicy overrides reconnect policy settings.
func (c *Client) WithReconnectPolicy(policy reconnect.Policy) *Client {
	c.policy = policy
	return c
}

// WithReadWaitPolicy overrides the bounded read wait budgets used by snapshot reads.
func (c *Client) WithReadWaitPolicy(policy ReadWaitPolicy) *Client {
	c.readWait = policy.Normalize()
	return c
}

// Handshake performs attach/reconnect compatibility validation.
func (c *Client) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, *compatibility.Diagnostic) {
	ack, err := c.transport.Handshake(ctx, hello)
	if err != nil {
		return protocol.HelloAck{}, compatibility.ClassifyConnectError(err)
	}
	if diag := compatibility.ClassifyHandshake(ack); diag != nil {
		return ack, diag
	}
	return ack, nil
}

// Command executes one daemon command envelope.
func (c *Client) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if metaProjectID := protocol.TrimProjectID(req.Meta.ProjectID.String()); metaProjectID != "" {
		req.Meta.ProjectID = naming.ProjectID(metaProjectID)
	} else {
		req.Meta.ProjectID = naming.ProjectID(c.projectRoute())
	}

	var lastErr error
	for attempt := 0; c.policy.ShouldRetry(attempt); attempt++ {
		resp, err := c.transport.Command(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !reconnect.IsTransientTransportError(err) || !c.policy.ShouldRetry(attempt+1) {
			break
		}
		select {
		case <-ctx.Done():
			return protocol.ResponseEnvelope{}, ctx.Err()
		case <-time.After(c.policy.Delay(attempt)):
		}
	}
	return protocol.ResponseEnvelope{}, fmt.Errorf("daemon command transport: %w", lastErr)
}

// Subscribe opens a daemon event stream with reconnect attempts.
func (c *Client) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	if projectID = protocol.TrimProjectID(projectID); projectID == "" {
		projectID = c.projectRoute()
	}
	var lastErr error
	for attempt := 0; c.policy.ShouldRetry(attempt); attempt++ {
		ch, err := c.transport.Subscribe(ctx, projectID, fromRevision)
		if err == nil {
			return ch, nil
		}
		lastErr = err
		if !reconnect.IsTransientTransportError(err) || !c.policy.ShouldRetry(attempt+1) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.policy.Delay(attempt)):
		}
	}
	return nil, fmt.Errorf("subscribe failed after retries: %w", lastErr)
}
