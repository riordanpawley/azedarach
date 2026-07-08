package transport

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ipc/codec"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
)

const defaultClientTimeout = 30 * time.Second

// Client is a Unix-socket daemon transport implementation.
type Client struct {
	socketPath string
	codec      *codec.Codec
	timeout    time.Duration
}

// NewClient returns a daemon transport client over Unix sockets.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		codec:      codec.NewCodec(),
		timeout:    defaultClientTimeout,
	}
}

// WithTimeout overrides per-request socket timeout.
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	if timeout > 0 {
		c.timeout = timeout
	}
	return c
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "ipc.dial",
		"dependency.name", "unix_socket",
		"dependency.operation", "dial",
		"transport", "unix",
	)
	var spanErr error
	defer func() { endSpan(spanErr) }()
	dialer := &net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		spanErr = err
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("daemon socket unavailable: %w", err)
		}
		return nil, err
	}
	return conn, nil
}

func (c *Client) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "cli", "transport.handshake", "client_name", hello.ClientName)
	var spanErr error
	defer func() { endSpan(spanErr) }()
	dialStartedAt := time.Now()
	conn, err := c.dial(ctx)
	if err != nil {
		spanErr = err
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.handshake.dial", dialStartedAt, "socket", c.socketPath, "error", err)
		return protocol.HelloAck{}, err
	}
	latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.handshake.dial", dialStartedAt, "socket", c.socketPath)
	defer conn.Close()
	c.setDeadline(ctx, conn)

	writeStartedAt := time.Now()
	if err := writeFrame(conn, c.codec, rpcFrame{
		Type:  frameTypeHello,
		Hello: &hello,
	}); err != nil {
		spanErr = err
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.handshake.write", writeStartedAt, "socket", c.socketPath, "error", err)
		return protocol.HelloAck{}, err
	}
	latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.handshake.write", writeStartedAt, "socket", c.socketPath)
	readStartedAt := time.Now()
	reply, err := readFrame(conn, c.codec)
	if err != nil {
		spanErr = err
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.handshake.read", readStartedAt, "socket", c.socketPath, "error", err)
		return protocol.HelloAck{}, err
	}
	latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.handshake.read", readStartedAt, "socket", c.socketPath)
	if reply.Type == frameTypeError && reply.Error != nil {
		spanErr = fmt.Errorf("handshake error: %s", reply.Error.Message)
		return protocol.HelloAck{}, spanErr
	}
	if reply.HelloAck == nil {
		spanErr = fmt.Errorf("invalid handshake response")
		return protocol.HelloAck{}, spanErr
	}
	return *reply.HelloAck, nil
}

func (c *Client) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "cli", "transport.command", "command", req.Command, "request_id", req.RequestID)
	var spanErr error
	defer func() { endSpan(spanErr) }()
	dialStartedAt := time.Now()
	conn, err := c.dial(ctx)
	if err != nil {
		spanErr = err
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.command.dial", dialStartedAt, "socket", c.socketPath, "command", req.Command, "request_id", req.RequestID, "error", err)
		return protocol.ResponseEnvelope{}, err
	}
	latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.command.dial", dialStartedAt, "socket", c.socketPath, "command", req.Command, "request_id", req.RequestID)
	defer conn.Close()
	c.setDeadline(ctx, conn)

	writeStartedAt := time.Now()
	if err := writeFrame(conn, c.codec, rpcFrame{
		Type:    frameTypeCommand,
		Request: &req,
	}); err != nil {
		spanErr = err
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.command.write", writeStartedAt, "socket", c.socketPath, "command", req.Command, "request_id", req.RequestID, "error", err)
		return protocol.ResponseEnvelope{}, err
	}
	latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.command.write", writeStartedAt, "socket", c.socketPath, "command", req.Command, "request_id", req.RequestID)
	readStartedAt := time.Now()
	reply, err := readFrame(conn, c.codec)
	if err != nil {
		spanErr = err
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.command.read", readStartedAt, "socket", c.socketPath, "command", req.Command, "request_id", req.RequestID, "error", err)
		return protocol.ResponseEnvelope{}, err
	}
	latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "transport.command.read", readStartedAt, "socket", c.socketPath, "command", req.Command, "request_id", req.RequestID)
	if reply.Type == frameTypeError && reply.Error != nil {
		spanErr = fmt.Errorf("command error: %s", reply.Error.Message)
		return protocol.ResponseEnvelope{}, spanErr
	}
	if reply.Response == nil {
		spanErr = fmt.Errorf("invalid command response")
		return protocol.ResponseEnvelope{}, spanErr
	}
	if reply.Response.Error != nil {
		spanErr = fmt.Errorf("daemon response error: %s", reply.Response.Error.Code)
	}
	return *reply.Response, nil
}

func (c *Client) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "cli", "transport.subscribe", "project_id", projectID)
	var spanErr error
	defer func() { endSpan(spanErr) }()
	conn, err := c.dial(ctx)
	if err != nil {
		spanErr = err
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	if err := writeFrame(conn, c.codec, rpcFrame{
		Type: frameTypeSubscribe,
		Subscribe: &subscribeRequest{
			ProjectID:    projectID,
			FromRevision: fromRevision,
		},
	}); err != nil {
		_ = conn.Close()
		spanErr = err
		return nil, err
	}

	ch := make(chan protocol.EventEnvelope, 32)
	readCtx, stopReadWatcher := context.WithCancel(ctx)
	go func() {
		<-readCtx.Done()
		_ = conn.Close()
	}()
	go func() {
		defer close(ch)
		defer conn.Close()
		defer stopReadWatcher()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			msg, err := readFrame(conn, c.codec)
			if err != nil {
				return
			}
			if msg.Type == frameTypeError {
				return
			}
			if msg.Event == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case ch <- *msg.Event:
			}
		}
	}()
	return ch, nil
}

func writeFrame(conn net.Conn, c *codec.Codec, frame rpcFrame) error {
	payload, err := c.Encode(frame)
	if err != nil {
		return err
	}
	return c.WriteFrame(conn, payload)
}

func readFrame(conn net.Conn, c *codec.Codec) (rpcFrame, error) {
	payload, err := c.ReadFrame(conn)
	if err != nil {
		return rpcFrame{}, err
	}
	var frame rpcFrame
	if err := c.Decode(payload, &frame); err != nil {
		return rpcFrame{}, err
	}
	return frame, nil
}

func (c *Client) setDeadline(ctx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		return
	}
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
}
