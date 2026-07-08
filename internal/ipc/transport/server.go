package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ipc/codec"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/observability"
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

var errSocketInUse = errors.New("socket path is already in use")

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
	ctx, endSpan := latencytrace.StartSpan(ctx, "daemon", "ipc.serve_start", "transport", "unix")
	var spanErr error
	defer func() { endSpan(spanErr) }()
	if s.handlers.Handshake == nil || s.handlers.Command == nil || s.handlers.Subscribe == nil {
		spanErr = fmt.Errorf("missing required handlers")
		return spanErr
	}
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		spanErr = err
		return fmt.Errorf("create socket dir: %w", err)
	}
	if err := clearStaleSocketPath(ctx, s.socketPath); err != nil {
		spanErr = err
		return err
	}

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		spanErr = err
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
			spanErr = err
			return fmt.Errorf("accept connection: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func clearStaleSocketPath(ctx context.Context, socketPath string) error {
	ctx, endSpan := latencytrace.StartSpan(ctx, "daemon", "ipc.clear_stale_socket", "transport", "unix")
	var spanErr error
	defer func() { endSpan(spanErr) }()
	info, err := os.Stat(socketPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		spanErr = err
		return fmt.Errorf("stat socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		spanErr = fmt.Errorf("socket path exists and is not a unix socket")
		return fmt.Errorf("%w: %s", spanErr, socketPath)
	}

	dialer := net.Dialer{Timeout: 150 * time.Millisecond}
	conn, dialErr := dialer.DialContext(ctx, "unix", socketPath)
	if dialErr == nil {
		_ = conn.Close()
		spanErr = errSocketInUse
		return fmt.Errorf("%w: %s", spanErr, socketPath)
	}
	if isSocketDialPermissionError(dialErr) {
		spanErr = dialErr
		return fmt.Errorf("%w: %s: %v", errSocketInUse, socketPath, dialErr)
	}
	if !isSocketDialDefinitelyStale(dialErr) {
		spanErr = dialErr
		return fmt.Errorf("%w: %s: %v", errSocketInUse, socketPath, dialErr)
	}
	if rmErr := os.Remove(socketPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		spanErr = rmErr
		return fmt.Errorf("remove stale socket path: %w", rmErr)
	}
	return nil
}

func isSocketDialPermissionError(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

func isSocketDialDefinitelyStale(err error) bool {
	// Only classify as stale when the error strongly indicates there is no
	// active listener behind the socket path.
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ENOTSOCK) ||
		errors.Is(err, net.ErrClosed)
}

func (s *Server) Close() error {
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	ctx, endConnSpan := latencytrace.StartSpan(ctx, "daemon", "ipc.connection", "transport", "unix")
	var connSpanErr error
	defer func() { endConnSpan(connSpanErr) }()
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			connSpanErr = fmt.Errorf("panic: %T", r)
			_, _ = fmt.Fprintf(os.Stderr, "azd ipc server recovered panic in connection handler: %v\n%s", r, debug.Stack())
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(serverFrameTimeout))

	first, err := readFrame(conn, s.codec)
	if err != nil {
		connSpanErr = err
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch first.Type {
	case frameTypeHello:
		helloCtx, endSpan := latencytrace.StartSpan(ctx, "daemon", "ipc.handshake", "transport", "unix")
		var spanErr error
		defer func() {
			if r := recover(); r != nil {
				spanErr = fmt.Errorf("panic: %T", r)
				endSpan(spanErr)
				panic(r)
			}
			endSpan(spanErr)
		}()
		if first.Hello == nil {
			spanErr = fmt.Errorf("missing hello payload")
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeInvalidRequest,
					Message: "missing hello payload",
				},
			})
			return
		}
		ack, err := s.handlers.Handshake(helloCtx, *first.Hello)
		if err != nil {
			spanErr = err
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
		if err := writeFrame(conn, s.codec, rpcFrame{
			Type:     frameTypeHelloAck,
			HelloAck: &ack,
		}); err != nil {
			spanErr = err
			connSpanErr = err
		}
	case frameTypeCommand:
		if first.Request == nil {
			connSpanErr = fmt.Errorf("missing command payload")
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeInvalidRequest,
					Message: "missing command payload",
				},
			})
			return
		}
		commandCtx, cancelCommand := context.WithCancel(ctx)
		commandCtx = observability.ExtractMetadata(commandCtx, first.Request.Meta)
		commandCtx, endSpan := latencytrace.StartSpan(commandCtx, "daemon", "ipc.command", "command", first.Request.Command, "request_id", first.Request.RequestID, "project_id", first.Request.Meta.ProjectID.String())
		var spanErr error
		defer func() {
			if r := recover(); r != nil {
				spanErr = fmt.Errorf("panic: %T", r)
				endSpan(spanErr)
				panic(r)
			}
			endSpan(spanErr)
		}()
		doneWatchingCommandConn, stopWatchingCommandConn := watchCommandConnClose(commandCtx, conn, cancelCommand)
		defer func() {
			stopWatchingCommandConn()
			<-doneWatchingCommandConn
		}()
		resp, err := s.handlers.Command(commandCtx, *first.Request)
		stopWatchingCommandConn()
		<-doneWatchingCommandConn
		if err != nil {
			spanErr = err
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
		} else if resp.Error != nil {
			spanErr = fmt.Errorf("daemon response error: %s", resp.Error.Code)
		}
		_ = conn.SetWriteDeadline(time.Now().Add(serverFrameTimeout))
		if err := writeFrame(conn, s.codec, rpcFrame{
			Type:     frameTypeResponse,
			Response: &resp,
		}); err != nil {
			spanErr = err
			connSpanErr = err
		}
	case frameTypeSubscribe:
		subscribeProjectID := ""
		if first.Subscribe != nil {
			subscribeProjectID = first.Subscribe.ProjectID
		}
		subscribeCtx, endSpan := latencytrace.StartSpan(ctx, "daemon", "ipc.subscribe", "project_id", subscribeProjectID, "transport", "unix")
		var spanErr error
		defer func() {
			if r := recover(); r != nil {
				spanErr = fmt.Errorf("panic: %T", r)
				endSpan(spanErr)
				panic(r)
			}
			endSpan(spanErr)
		}()
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
		if first.Subscribe == nil || first.Subscribe.ProjectID == "" {
			spanErr = fmt.Errorf("missing subscribe payload")
			_ = writeFrame(conn, s.codec, rpcFrame{
				Type: frameTypeError,
				Error: &protocol.ErrorEnvelope{
					Code:    protocol.ErrorCodeInvalidRequest,
					Message: "missing subscribe payload",
				},
			})
			return
		}
		subscribeCtx, cancelSubscribe := context.WithCancel(subscribeCtx)
		doneWatchingSubscribeConn, stopWatchingSubscribeConn := watchCommandConnClose(subscribeCtx, conn, cancelSubscribe)
		defer func() {
			stopWatchingSubscribeConn()
			<-doneWatchingSubscribeConn
		}()
		ch, cancel, err := s.handlers.Subscribe(subscribeCtx, first.Subscribe.ProjectID, first.Subscribe.FromRevision)
		if err != nil {
			spanErr = err
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
			case <-subscribeCtx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(serverFrameTimeout))
				if err := writeFrame(conn, s.codec, rpcFrame{
					Type:  frameTypeEvent,
					Event: &evt,
				}); err != nil {
					spanErr = err
					connSpanErr = err
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

func watchCommandConnClose(ctx context.Context, conn net.Conn, cancel context.CancelFunc) (<-chan struct{}, func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(serverFrameTimeout))
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, err := conn.Read(buf)
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() && ctx.Err() == nil {
					continue
				}
				if ctx.Err() == nil {
					cancel()
				}
				return
			}
		}
	}()
	stop := func() {
		cancel()
		_ = conn.SetReadDeadline(time.Now())
	}
	return done, stop
}
