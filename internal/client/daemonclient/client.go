package daemonclient

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/compatibility"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
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
	projectID naming.ProjectID
}

// New returns a shared daemon client with default reconnect policy.
func New(transport TransportClient) *Client {
	defaultProjectID, err := naming.ParseProjectID(protocol.DefaultProjectID)
	if err != nil {
		defaultProjectID = naming.ProjectID(protocol.DefaultProjectID)
	}
	return &Client{
		transport: transport,
		policy:    reconnect.DefaultPolicy(),
		readWait:  DefaultReadWaitPolicy(),
		projectID: defaultProjectID,
	}
}

// WithProjectID sets the default project route used for command metadata and fallback subscriptions.
func (c *Client) WithProjectID(projectID string) *Client {
	c.projectID = normalizeRouteProjectID(projectID)
	return c
}

// WithProjectRouteID sets the default project route using an explicit typed identifier.
func (c *Client) WithProjectRouteID(projectID naming.ProjectID) *Client {
	c.projectID = normalizeRouteProjectID(projectID.String())
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
	populateClientAuditMetadata(&req.Meta)

	var lastErr error
	for attempt := 0; c.policy.ShouldRetry(attempt); attempt++ {
		attemptStartedAt := time.Now()
		resp, err := c.transport.Command(ctx, req)
		if err == nil {
			latencytrace.LogPhase(slog.Default(), "cli", "daemonclient.command_attempt", attemptStartedAt, "command", req.Command, "request_id", req.RequestID, "attempt", attempt+1, "ok", resp.OK)
			if shouldRetryReadCommandResponse(req.Command, resp) {
				if !c.policy.ShouldRetry(attempt + 1) {
					return resp, nil
				}
				if err := sleepCommandRetry(ctx, c.policy.Delay(attempt)); err != nil {
					return protocol.ResponseEnvelope{}, err
				}
				continue
			}
			return resp, nil
		}
		latencytrace.LogPhase(slog.Default(), "cli", "daemonclient.command_attempt", attemptStartedAt, "command", req.Command, "request_id", req.RequestID, "attempt", attempt+1, "error", err)
		lastErr = err
		if !reconnect.IsTransientTransportError(err) || !c.policy.ShouldRetry(attempt+1) {
			break
		}
		if err := sleepCommandRetry(ctx, c.policy.Delay(attempt)); err != nil {
			return protocol.ResponseEnvelope{}, err
		}
	}
	return protocol.ResponseEnvelope{}, fmt.Errorf("daemon command transport: %w", lastErr)
}

func sleepCommandRetry(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func shouldRetryReadCommandResponse(command string, resp protocol.ResponseEnvelope) bool {
	if resp.OK || resp.Error == nil {
		return false
	}
	if !isDaemonReadCommand(command) {
		return false
	}
	return resp.Error.Code == protocol.ErrorCodeUnavailable && resp.Error.Retryable
}

func isDaemonReadCommand(command string) bool {
	switch command {
	case CommandBoardFetch,
		CommandTaskList,
		CommandTaskGet,
		CommandTaskGetMany,
		CommandTaskEvents,
		CommandTaskGraphReadiness,
		CommandTaskCompleteCheck,
		CommandTaskIntegrationReady,
		CommandTaskMergeBaseTarget,
		CommandTaskFollowOnMerge,
		CommandSyncConflicts,
		CommandSessionStatus,
		CommandDevServerStatus,
		CommandDevServerList,
		CommandWorktreeList,
		CommandSpecRequirementList,
		CommandSpecRequirementGet,
		CommandSpecLinkList,
		CommandSpecRead,
		CommandSpecPack,
		CommandSpecLint,
		CommandSpecParity,
		CommandSpecExport,
		CommandDecisionList,
		CommandDecisionGet,
		CommandDecisionLinkList,
		protocol.CommandLearnRecall,
		protocol.CommandLearnShow,
		CommandGitBranchBehind,
		CommandGitDiffStat,
		CommandGitStatus,
		CommandGitRuntimeSignals,
		CommandGitMergePreflight,
		CommandGitWorktreeForBranch,
		protocol.CommandNoticeList,
		protocol.CommandNoticeGet,
		protocol.CommandScheduledScriptsStatus,
		protocol.CommandMailList,
		protocol.CommandMailWatch:
		return true
	default:
		return false
	}
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

func normalizeRouteProjectID(projectID string) naming.ProjectID {
	normalized := protocol.NormalizeProjectID(projectID)
	parsed, err := naming.ParseProjectID(normalized)
	if err == nil {
		return parsed
	}
	fallback, fallbackErr := naming.ParseProjectID(protocol.DefaultProjectID)
	if fallbackErr == nil {
		return fallback
	}
	return naming.ProjectID(protocol.DefaultProjectID)
}
