package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
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

func TestCommandHonorsContextDeadlineOverDefaultTimeout(t *testing.T) {
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
			time.Sleep(6 * time.Second)
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

	client := NewClient(socket)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cmdCancel()

	_, err := client.Command(cmdCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-context-deadline",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		SentAt:          time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("command: %v", err)
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
			time.Sleep(800 * time.Millisecond)
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

	client := NewClient(socket).WithTimeout(10 * time.Second)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
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
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket did not become ready: %s", socket)
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
	return fmt.Sprintf("%s/azd-ipc-%d.sock", os.TempDir(), time.Now().UnixNano())
}
