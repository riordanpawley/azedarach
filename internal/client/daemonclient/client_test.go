package daemonclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/compatibility"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type fakeTransport struct {
	handshakeFn func(context.Context, protocol.Hello) (protocol.HelloAck, error)
	commandFn   func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	subscribeFn func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error)
}

func (f *fakeTransport) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	if f.handshakeFn == nil {
		return protocol.HelloAck{}, nil
	}
	return f.handshakeFn(ctx, hello)
}
func (f *fakeTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if f.commandFn == nil {
		return protocol.ResponseEnvelope{OK: true}, nil
	}
	return f.commandFn(ctx, req)
}
func (f *fakeTransport) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	if f.subscribeFn == nil {
		return nil, nil
	}
	return f.subscribeFn(ctx, projectID, fromRevision)
}

func TestClientHandshakeDiagnostics(t *testing.T) {
	c := New(&fakeTransport{
		handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{
				Accepted:  false,
				ErrorCode: protocol.ErrorCodeUpgradeRequired,
				Reason:    "upgrade needed",
			}, nil
		},
	})
	_, diag := c.Handshake(context.Background(), protocol.Hello{ProtocolVersion: protocol.CurrentVersion})
	if diag == nil || !errors.Is(diag.Err, compatibility.ErrUpgradeRequired) {
		t.Fatalf("expected upgrade diagnostic, got %+v", diag)
	}
}

func TestClientCommandPassThrough(t *testing.T) {
	c := New(&fakeTransport{
		handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		commandFn: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{OK: true}, nil
		},
	})

	resp, err := c.Command(context.Background(), protocol.RequestEnvelope{Command: "session.start"})
	if err != nil {
		t.Fatalf("Command error: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected OK command response")
	}
}

func TestClientCommandRetryOnTransientTransportError(t *testing.T) {
	attempts := 0
	c := New(&fakeTransport{
		commandFn: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			attempts++
			if attempts < 3 {
				return protocol.ResponseEnvelope{}, errors.New("dial unix /tmp/daemon.sock: connect: connection refused")
			}
			return protocol.ResponseEnvelope{OK: true}, nil
		},
	}).WithReconnectPolicy(reconnect.Policy{
		MaxAttempts: 3,
		BaseBackoff: 0,
		MaxBackoff:  0,
	})

	resp, err := c.Command(context.Background(), protocol.RequestEnvelope{Command: "git.merge"})
	if err != nil {
		t.Fatalf("Command error: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected OK response after retry")
	}
	if attempts != 3 {
		t.Fatalf("command attempts = %d, want 3", attempts)
	}
}

func TestClientCommandDoesNotRetryNonTransientTransportError(t *testing.T) {
	attempts := 0
	c := New(&fakeTransport{
		commandFn: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			attempts++
			return protocol.ResponseEnvelope{}, errors.New("permission denied")
		},
	}).WithReconnectPolicy(reconnect.Policy{
		MaxAttempts: 5,
		BaseBackoff: 0,
		MaxBackoff:  0,
	})

	_, err := c.Command(context.Background(), protocol.RequestEnvelope{Command: "task.list"})
	if err == nil {
		t.Fatal("expected command transport error")
	}
	if attempts != 1 {
		t.Fatalf("command attempts = %d, want 1", attempts)
	}
}

func TestClientSubscribeRetry(t *testing.T) {
	attempts := 0
	c := New(&fakeTransport{
		handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
			return protocol.HelloAck{Accepted: true}, nil
		},
		subscribeFn: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("dial unix /tmp/daemon.sock: connect: connection refused")
			}
			ch := make(chan protocol.EventEnvelope, 1)
			ch <- protocol.EventEnvelope{Revision: 7, Event: "ok"}
			return ch, nil
		},
	}).WithReconnectPolicy(reconnect.Policy{
		MaxAttempts: 5,
		BaseBackoff: 0,
		MaxBackoff:  0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, err := c.Subscribe(ctx, "proj", 0)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	evt := <-ch
	if evt.Revision != 7 {
		t.Fatalf("event revision = %d, want 7", evt.Revision)
	}
	if attempts != 3 {
		t.Fatalf("subscribe attempts = %d, want 3", attempts)
	}
}

func TestClientSubscribeDoesNotRetryPermanentTransportError(t *testing.T) {
	attempts := 0
	c := New(&fakeTransport{
		subscribeFn: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
			attempts++
			return nil, errors.New("permission denied")
		},
	}).WithReconnectPolicy(reconnect.Policy{
		MaxAttempts: 5,
		BaseBackoff: 0,
		MaxBackoff:  0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Subscribe(ctx, "proj", 0); err == nil {
		t.Fatal("expected permanent subscribe error")
	}
	if attempts != 1 {
		t.Fatalf("subscribe attempts = %d, want 1", attempts)
	}
}

func TestClientProjectRouting(t *testing.T) {
	var gotCommandProjectID string
	var gotSubscribeProjectID string

	c := New(&fakeTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			gotCommandProjectID = req.Meta.ProjectID
			return protocol.ResponseEnvelope{OK: true}, nil
		},
		subscribeFn: func(_ context.Context, projectID string, _ uint64) (<-chan protocol.EventEnvelope, error) {
			gotSubscribeProjectID = projectID
			ch := make(chan protocol.EventEnvelope, 1)
			return ch, nil
		},
	}).WithProjectID("proj-a")

	if _, err := c.Command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		Command:         "task.list",
	}); err != nil {
		t.Fatalf("Command error: %v", err)
	}
	if gotCommandProjectID != "proj-a" {
		t.Fatalf("command project_id = %q, want proj-a", gotCommandProjectID)
	}

	if _, err := c.Command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		Meta: protocol.Metadata{
			ProjectID: "proj-explicit",
		},
		Command: "task.list",
	}); err != nil {
		t.Fatalf("Command with explicit metadata error: %v", err)
	}
	if gotCommandProjectID != "proj-explicit" {
		t.Fatalf("explicit command project_id = %q, want proj-explicit", gotCommandProjectID)
	}

	if _, err := c.Subscribe(context.Background(), "", 0); err != nil {
		t.Fatalf("Subscribe fallback error: %v", err)
	}
	if gotSubscribeProjectID != "proj-a" {
		t.Fatalf("subscribe project_id = %q, want proj-a", gotSubscribeProjectID)
	}

	if _, err := c.Subscribe(context.Background(), "proj-b", 0); err != nil {
		t.Fatalf("Subscribe explicit error: %v", err)
	}
	if gotSubscribeProjectID != "proj-b" {
		t.Fatalf("explicit subscribe project_id = %q, want proj-b", gotSubscribeProjectID)
	}
}
