package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ipc/codec"
)

// Handlers defines daemon runtime hooks used by IPC server.
type Handlers struct {
	Handshake func(context.Context, protocol.Hello) (protocol.HelloAck, error)
	Command   func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	Subscribe func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error)
}

// Server serves daemon IPC over a Unix socket.
type Server struct {
	socketPath string
	codec      *codec.Codec
	handlers   Handlers
	listener   net.Listener
}

const serverFrameTimeout = 5 * time.Second

// NewServer returns an unstarted IPC server.
func NewServer(socketPath string, handlers Handlers) *Server {
	return &Server{
		socketPath: socketPath,
		codec:      codec.NewCodec(),
		handlers:   handlers,
	}
}

// Serve starts listening and blocks until context cancellation or fatal error.
func (s *Server) Serve(ctx context.Context) error {
	if s.handlers.Handshake == nil || s.handlers.Command == nil || s.handlers.Subscribe == nil {
		return fmt.Errorf("missing required handlers")
	}
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix socket: %w", err)
	}
	s.listener = l
	defer func() {
		_ = l.Close()
		_ = os.Remove(s.socketPath)
	}()

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) Close() error {
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(os.Stderr, "azd ipc server recovered panic in connection handler: %v\n%s", r, debug.Stack())
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(serverFrameTimeout))

	first, err := readFrame(conn, s.codec)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch first.Type {
	case frameTypeHello:
		if first.Hello == nil {
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeInvalidRequest,
					Message: "missing hello payload",
				},
			})
			return
		}
		ack, err := s.handlers.Handshake(ctx, *first.Hello)
		if err != nil {
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeUnavailable,
					Message: err.Error(),
				},
			})
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(serverFrameTimeout))
		_ = writeFrame(conn, s.codec, rpcFrame{
			Type:     frameTypeHelloAck,
			HelloAck: &ack,
		})
	case frameTypeCommand:
		if first.Request == nil {
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeInvalidRequest,
					Message: "missing command payload",
				},
			})
			return
		}
		resp, err := s.handlers.Command(ctx, *first.Request)
		if err != nil {
			resp = protocol.ResponseEnvelope{
				ProtocolVersion: first.Request.ProtocolVersion,
				RequestID:       first.Request.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Meta:            first.Request.Meta,
				CompletedAt:     time.Now().UTC(),
				Error: &protocol.ErrorEnvelope{
					Code:      protocol.ErrorCodeInternal,
					Message:   err.Error(),
					Retryable: false,
				},
			}
		}
		_ = conn.SetWriteDeadline(time.Now().Add(serverFrameTimeout))
		_ = writeFrame(conn, s.codec, rpcFrame{
			Type:     frameTypeResponse,
			Response: &resp,
		})
	case frameTypeSubscribe:
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
		if first.Subscribe == nil || first.Subscribe.ProjectID == "" {
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeInvalidRequest,
					Message: "missing subscribe payload",
				},
			})
			return
		}
		ch, cancel, err := s.handlers.Subscribe(ctx, first.Subscribe.ProjectID, first.Subscribe.FromRevision)
		if err != nil {
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeUnavailable,
					Message: err.Error(),
				},
			})
			return
		}
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				if err := writeFrame(conn, s.codec, rpcFrame{
					Type:  frameTypeEvent,
					Event: &evt,
				}); err != nil {
					return
				}
			}
		}
	default:
		_ = writeFrame(conn, s.codec, rpcFrame{
			Type: frameTypeError,
			Error: &protocol.ErrorEnvelope{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "unsupported frame type",
			},
		})
	}
}
