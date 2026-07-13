package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestClientServerHandshakeAndCommand(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := tempSocketPath(t)
	defer os.Remove(socket)
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{
				Accepted:              true,
				DaemonProtocolVersion: protocol.CurrentVersion,
				DaemonVersion:         "test",
			}, nil
		},
		Command: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				CompletedAt:     time.Now().UTC(),
				OK:              true,
				Revision:        7,
			}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			ch := make(chan protocol.EventEnvelope)
			close(ch)
			return ch, func() {}, nil
		},
	})
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx)
	}()
	waitForSocket(t, socket, errCh)

	client := NewClient(socket)
	ack, err := client.Handshake(ctx, protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "tui",
		ClientVersion:   "test",
	})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("ack not accepted: %+v", ack)
	}

	resp, err := client.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-1",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		SentAt:          time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !resp.OK || resp.Revision != 7 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestSubscribeStreamsEvents(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := tempSocketPath(t)
	defer os.Remove(socket)
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{OK: true}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			ch := make(chan protocol.EventEnvelope, 1)
			ch <- protocol.EventEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				ProjectID:       "proj",
				Revision:        2,
				Event:           "task.updated",
				Kind:            protocol.EnvelopeKindEvent,
				EmittedAt:       time.Now().UTC(),
			}
			close(ch)
			return ch, func() {}, nil
		},
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, socket, errCh)

	client := NewClient(socket)
	ch, err := client.Subscribe(ctx, "proj", 1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before first event")
		}
		if evt.Revision != 2 || evt.Event != "task.updated" {
			t.Fatalf("event = %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestSubscribeContextCancelClosesServerSubscriber(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := tempSocketPath(t)
	defer os.Remove(socket)
	serverCanceled := make(chan struct{})
	events := make(chan protocol.EventEnvelope)
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{OK: true}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			return events, func() { close(serverCanceled) }, nil
		},
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, socket, errCh)

	client := NewClient(socket)
	subCtx, subCancel := context.WithCancel(context.Background())
	ch, err := client.Subscribe(subCtx, "proj", 1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	subCancel()

	select {
	case <-serverCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("server subscriber was not canceled after client context cancellation")
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("client event channel should close after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client event channel did not close after context cancellation")
	}
}

func TestServerLifecycleSpansDoNotShareCommandTraceRoots(t *testing.T) {
	t.Setenv(observability.EnvVar, "true")
	t.Setenv(latencytrace.EnvVar, "")
	latencytrace.SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
		latencytrace.SetConfigEnabled(false)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := tempSocketPath(t)
	defer os.Remove(socket)
	subscribed := make(chan struct{})
	serverSubscriberCanceled := make(chan struct{})
	events := make(chan protocol.EventEnvelope)
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				CompletedAt:     time.Now().UTC(),
				OK:              true,
			}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			select {
			case <-subscribed:
			default:
				close(subscribed)
			}
			return events, func() {
				select {
				case <-serverSubscriberCanceled:
				default:
					close(serverSubscriberCanceled)
				}
			}, nil
		},
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, socket, errCh)

	client := NewClient(socket)
	subCtx, subCancel := context.WithCancel(context.Background())
	ch, err := client.Subscribe(subCtx, "proj", 1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("server subscriber did not attach")
	}

	for _, requestID := range []string{"req-trace-a", "req-trace-b"} {
		resp, err := client.Command(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID(requestID),
			Kind:            protocol.EnvelopeKindCommand,
			Command:         "task.list",
			SentAt:          time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("command %s: %v", requestID, err)
		}
		if !resp.OK {
			t.Fatalf("command %s response not OK: %+v", requestID, resp)
		}
	}
	propagatedCtx, propagatedSpan := otel.Tracer("transport_test").Start(context.Background(), "cli.command.propagated")
	propagatedTraceID := propagatedSpan.SpanContext().TraceID().String()
	var propagatedMeta protocol.Metadata
	observability.InjectMetadata(propagatedCtx, &propagatedMeta)
	resp, err := client.Command(propagatedCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-propagated"),
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            propagatedMeta,
		Command:         "task.list",
		SentAt:          time.Now().UTC(),
	})
	propagatedSpan.End()
	if err != nil {
		t.Fatalf("propagated command: %v", err)
	}
	if !resp.OK {
		t.Fatalf("propagated command response not OK: %+v", resp)
	}

	subCancel()
	select {
	case <-serverSubscriberCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("server subscriber was not canceled")
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("client subscribe channel should close after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client subscribe channel did not close")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}

	commandTraceIDs := map[string]string{}
	lifecycleTraceIDs := map[string][]string{}
	for _, span := range recorder.Ended() {
		traceID := span.SpanContext().TraceID().String()
		switch span.Name() {
		case "daemon.ipc.command":
			requestID := spanStringAttr(span, "request_id")
			if requestID != "" {
				commandTraceIDs[requestID] = traceID
			}
		case "daemon.ipc.serve_start", "daemon.ipc.connection", "daemon.ipc.subscribe":
			lifecycleTraceIDs[span.Name()] = append(lifecycleTraceIDs[span.Name()], traceID)
		}
	}
	if len(commandTraceIDs) != 3 {
		t.Fatalf("daemon command trace ids = %+v, want three request_id keyed spans", commandTraceIDs)
	}
	if commandTraceIDs["req-trace-a"] == commandTraceIDs["req-trace-b"] {
		t.Fatalf("command trace ids should be isolated by request_id, got shared trace %s", commandTraceIDs["req-trace-a"])
	}
	if commandTraceIDs["req-propagated"] != propagatedTraceID {
		t.Fatalf("propagated command trace id = %s, want %s", commandTraceIDs["req-propagated"], propagatedTraceID)
	}
	for _, name := range []string{"daemon.ipc.serve_start", "daemon.ipc.connection", "daemon.ipc.subscribe"} {
		if len(lifecycleTraceIDs[name]) == 0 {
			t.Fatalf("missing lifecycle span %s", name)
		}
	}
	for lifecycleName, traceIDs := range lifecycleTraceIDs {
		for _, traceID := range traceIDs {
			for requestID, commandTraceID := range commandTraceIDs {
				if traceID == commandTraceID {
					t.Fatalf("%s span shared command trace root for %s: %s", lifecycleName, requestID, traceID)
				}
			}
		}
	}
}

func spanStringAttr(span sdktrace.ReadOnlySpan, key string) string {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func TestCommandHonorsContextDeadlineOverDefaultTimeout(t *testing.T) {
	client := NewClient("unused")
	if client.timeout != 30*time.Second {
		t.Fatalf("default client timeout = %v, want 30s", client.timeout)
	}

	want := time.Now().Add(time.Hour).Round(0)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()
	conn := &deadlineRecordingConn{}
	client.setDeadline(ctx, conn)
	if !conn.deadline.Equal(want) {
		t.Fatalf("socket deadline = %v, want context deadline %v", conn.deadline, want)
	}
}

func TestCommandTimesOutWhenContextDeadlineIsShorterThanClientTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := tempSocketPath(t)
	defer os.Remove(socket)
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			// This is the minimum real-time smoke coverage: a Unix socket must
			// surface the OS deadline as net.Error rather than only storing it.
			time.Sleep(25 * time.Millisecond)
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				CompletedAt:     time.Now().UTC(),
				OK:              true,
			}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			ch := make(chan protocol.EventEnvelope)
			close(ch)
			return ch, func() {}, nil
		},
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, socket, errCh)

	client := NewClient(socket).WithTimeout(100 * time.Millisecond)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cmdCancel()

	_, err := client.Command(cmdCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-short-context",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		SentAt:          time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestWatchCommandConnCloseStopPreventsStaleFutureReadDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	conn := newDeadlineRaceConn()
	done, stop := watchCommandConnClose(ctx, conn, cancel)
	defer conn.Close()

	select {
	case <-conn.futureDeadlineStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not set the initial future read deadline")
	}

	stop()
	close(conn.allowFutureDeadline)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop after cancellation")
	}
	select {
	case <-conn.readCalled:
		t.Fatal("watcher read after stop cancellation")
	default:
	}
}

func TestCommandContextCancelsWhenClientDisconnects(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := tempSocketPath(t)
	defer os.Remove(socket)
	handlerCanceled := make(chan struct{})
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			select {
			case <-ctx.Done():
				close(handlerCanceled)
				return protocol.ResponseEnvelope{}, ctx.Err()
			case <-time.After(2 * time.Second):
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					CompletedAt:     time.Now().UTC(),
					OK:              true,
				}, nil
			}
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			ch := make(chan protocol.EventEnvelope)
			close(ch)
			return ch, func() {}, nil
		},
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, socket, errCh)

	client := NewClient(socket).WithTimeout(10 * time.Second)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cmdCancel()

	_, err := client.Command(cmdCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-client-disconnect",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		SentAt:          time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected client command timeout")
	}

	select {
	case <-handlerCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("server command context was not canceled after client disconnected")
	}
}

type deadlineRaceConn struct {
	futureDeadlineStarted chan struct{}
	allowFutureDeadline   chan struct{}
	readCalled            chan struct{}
	closed                chan struct{}
	closeOnce             sync.Once
	futureBlocked         atomic.Bool
}

type deadlineRecordingConn struct {
	deadline time.Time
}

func (c *deadlineRecordingConn) Read([]byte) (int, error)          { return 0, nil }
func (c *deadlineRecordingConn) Write(p []byte) (int, error)       { return len(p), nil }
func (c *deadlineRecordingConn) Close() error                      { return nil }
func (c *deadlineRecordingConn) LocalAddr() net.Addr               { return dummyAddr("local") }
func (c *deadlineRecordingConn) RemoteAddr() net.Addr              { return dummyAddr("remote") }
func (c *deadlineRecordingConn) SetDeadline(value time.Time) error { c.deadline = value; return nil }
func (c *deadlineRecordingConn) SetReadDeadline(time.Time) error   { return nil }
func (c *deadlineRecordingConn) SetWriteDeadline(time.Time) error  { return nil }

func newDeadlineRaceConn() *deadlineRaceConn {
	return &deadlineRaceConn{
		futureDeadlineStarted: make(chan struct{}),
		allowFutureDeadline:   make(chan struct{}),
		readCalled:            make(chan struct{}),
		closed:                make(chan struct{}),
	}
}

func (c *deadlineRaceConn) Read(_ []byte) (int, error) {
	select {
	case <-c.readCalled:
	default:
		close(c.readCalled)
	}
	<-c.closed
	return 0, net.ErrClosed
}

func (c *deadlineRaceConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *deadlineRaceConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *deadlineRaceConn) LocalAddr() net.Addr              { return dummyAddr("local") }
func (c *deadlineRaceConn) RemoteAddr() net.Addr             { return dummyAddr("remote") }
func (c *deadlineRaceConn) SetDeadline(time.Time) error      { return nil }
func (c *deadlineRaceConn) SetWriteDeadline(time.Time) error { return nil }
func (c *deadlineRaceConn) SetReadDeadline(deadline time.Time) error {
	if time.Until(deadline) > time.Second && c.futureBlocked.CompareAndSwap(false, true) {
		close(c.futureDeadlineStarted)
		<-c.allowFutureDeadline
	}
	return nil
}

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

func TestServerRecoversFromCommandHandlerPanic(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socket := tempSocketPath(t)
	defer os.Remove(socket)

	var calls atomic.Int32
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if calls.Add(1) == 1 {
				panic("boom")
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				CompletedAt:     time.Now().UTC(),
				OK:              true,
				Revision:        99,
			}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			ch := make(chan protocol.EventEnvelope)
			close(ch)
			return ch, func() {}, nil
		},
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, socket, errCh)

	client := NewClient(socket)
	_, err := client.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-panics-1",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		SentAt:          time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected first command to fail due to panic")
	}

	resp, err := client.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-panics-2",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		SentAt:          time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("second command should succeed after panic recovery: %v", err)
	}
	if !resp.OK || resp.Revision != 99 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestServerServeFailsWhenSocketPathInUse(t *testing.T) {
	t.Parallel()
	socket := tempSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		if isSocketPermissionError(err) {
			t.Skipf("sandbox does not permit socket bind/listen: %v", err)
		}
		t.Fatalf("listen existing socket: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	})

	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{OK: true}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			ch := make(chan protocol.EventEnvelope)
			close(ch)
			return ch, func() {}, nil
		},
	})

	err = srv.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("Serve() error = %v, want active socket in-use error", err)
	}
}

func TestServerServeRemovesStaleSocketPath(t *testing.T) {
	t.Parallel()
	socket := tempSocketPath(t)

	stale, err := net.Listen("unix", socket)
	if err != nil {
		if isSocketPermissionError(err) {
			t.Skipf("sandbox does not permit socket bind/listen: %v", err)
		}
		t.Fatalf("listen stale setup: %v", err)
	}
	_ = stale.Close()
	t.Cleanup(func() { _ = os.Remove(socket) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer(socket, Handlers{
		Handshake: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		Command: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{OK: true}, nil
		},
		Subscribe: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, func(), error) {
			ch := make(chan protocol.EventEnvelope)
			close(ch)
			return ch, func() {}, nil
		},
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, socket, errCh)
}

func TestSocketDialStaleClassification(t *testing.T) {
	if !isSocketDialDefinitelyStale(syscall.ECONNREFUSED) {
		t.Fatal("expected ECONNREFUSED to be classified as stale")
	}
	if !isSocketDialDefinitelyStale(syscall.ENOENT) {
		t.Fatal("expected ENOENT to be classified as stale")
	}
	if isSocketDialDefinitelyStale(syscall.EAGAIN) {
		t.Fatal("did not expect EAGAIN to be classified as stale")
	}
	if isSocketDialDefinitelyStale(syscall.ETIMEDOUT) {
		t.Fatal("did not expect ETIMEDOUT to be classified as stale")
	}
}

func TestSocketReadinessRequiresDialableListener(t *testing.T) {
	socket := tempSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close unix socket: %v", err)
	}
	if socketDialReady(socket) {
		t.Fatal("stale socket path reported ready without a listener")
	}

	if err := os.Remove(socket); err != nil {
		t.Fatalf("remove stale socket: %v", err)
	}
	listener, err = net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen replacement unix socket: %v", err)
	}
	defer listener.Close()
	if !socketDialReady(socket) {
		t.Fatal("live unix listener did not report ready")
	}
}

func waitForSocket(t *testing.T, socket string, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			if isSocketPermissionError(err) {
				t.Skipf("sandbox does not permit socket bind/listen: %v", err)
			}
			if err != nil {
				t.Fatalf("server exited before socket became ready: %v", err)
			}
			t.Fatalf("server exited before socket became ready")
		default:
		}
		if socketDialReady(socket) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket did not become ready: %s", socket)
}

func socketDialReady(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func isSocketPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "operation not permitted") || strings.Contains(text, "permission denied")
}

func tempSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "azd-ipc-*")
	if err != nil {
		t.Fatalf("create temp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "ipc.sock")
}
