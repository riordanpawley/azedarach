package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ipc/codec"
)

const defaultTimeout = 5 * time.Second

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
		timeout:    defaultTimeout,
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
	dialer := &net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("daemon socket unavailable: %w", err)
		}
		return nil, err
	}
	return conn, nil
}

func (c *Client) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return protocol.HelloAck{}, err
	}
	defer conn.Close()
	c.setDeadline(ctx, conn)

	if err := writeFrame(conn, c.codec, rpcFrame{
		Type:  frameTypeHello,
		Hello: &hello,
	}); err != nil {
		return protocol.HelloAck{}, err
	}
	reply, err := readFrame(conn, c.codec)
	if err != nil {
		return protocol.HelloAck{}, err
	}
	if reply.Type == frameTypeError && reply.Error != nil {
		return protocol.HelloAck{}, fmt.Errorf("handshake error: %s", reply.Error.Message)
	}
	if reply.HelloAck == nil {
		return protocol.HelloAck{}, fmt.Errorf("invalid handshake response")
	}
	return *reply.HelloAck, nil
}

func (c *Client) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return protocol.ResponseEnvelope{}, err
	}
	defer conn.Close()
	c.setDeadline(ctx, conn)

	if err := writeFrame(conn, c.codec, rpcFrame{
		Type:    frameTypeCommand,
		Request: &req,
	}); err != nil {
		return protocol.ResponseEnvelope{}, err
	}
	reply, err := readFrame(conn, c.codec)
	if err != nil {
		return protocol.ResponseEnvelope{}, err
	}
	if reply.Type == frameTypeError && reply.Error != nil {
		return protocol.ResponseEnvelope{}, fmt.Errorf("command error: %s", reply.Error.Message)
	}
	if reply.Response == nil {
		return protocol.ResponseEnvelope{}, fmt.Errorf("invalid command response")
	}
	return *reply.Response, nil
}

func (c *Client) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	conn, err := c.dial(ctx)
	if err != nil {
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
		return nil, err
	}

	ch := make(chan protocol.EventEnvelope, 32)
	go func() {
		defer close(ch)
		defer conn.Close()
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
