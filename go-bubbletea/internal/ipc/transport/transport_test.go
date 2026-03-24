package transport

import (
	"context"
	"fmt"
	"os"
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
	go func() {
		_ = srv.Serve(ctx)
	}()
	waitForSocket(t, socket)

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
	go func() { _ = srv.Serve(ctx) }()
	waitForSocket(t, socket)

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

func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket did not become ready: %s", socket)
}

func tempSocketPath(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s/azd-ipc-%d.sock", os.TempDir(), time.Now().UnixNano())
}
