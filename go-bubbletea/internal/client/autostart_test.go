package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type fakeStarter struct {
	calls atomic.Int32
	start func()
}

func (s *fakeStarter) Start(ctx context.Context) error {
	s.calls.Add(1)
	if s.start != nil {
		s.start()
	}
	return nil
}

type fakeHandshaker struct {
	mu sync.Mutex
	fn func() (protocol.HelloAck, error)
}

func (h *fakeHandshaker) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fn()
}

func TestEnsureAttachedSingleflightStart(t *testing.T) {
	var started atomic.Bool
	h := &fakeHandshaker{
		fn: func() (protocol.HelloAck, error) {
			if started.Load() {
				return protocol.HelloAck{Accepted: true}, nil
			}
			return protocol.HelloAck{}, errors.New("dial unavailable")
		},
	}
	s := &fakeStarter{
		start: func() { started.Store(true) },
	}
	o := NewAutostartOrchestrator(h, s)
	o.sleepFn = func(_ time.Duration) {}
	o.backoffFn = func(_ int) time.Duration { return 0 }

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.EnsureAttached(context.Background(), protocol.Hello{
				ProtocolVersion: protocol.CurrentVersion,
				ClientName:      "tui",
				ClientVersion:   "dev",
			}); err != nil {
				t.Errorf("EnsureAttached err: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := s.calls.Load(); got != 1 {
		t.Fatalf("starter calls = %d, want 1", got)
	}
}

func TestEnsureAttachedUpgradeRequiredNoRetry(t *testing.T) {
	h := &fakeHandshaker{
		fn: func() (protocol.HelloAck, error) {
			return protocol.HelloAck{
				Accepted:          false,
				ErrorCode:         protocol.ErrorCodeUpgradeRequired,
				RetryAfterRestart: false,
			}, nil
		},
	}
	s := &fakeStarter{}
	o := NewAutostartOrchestrator(h, s)

	_, err := o.EnsureAttached(context.Background(), protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "cli",
		ClientVersion:   "dev",
	})
	if !errors.Is(err, ErrUpgradeRequired) {
		t.Fatalf("err = %v, want ErrUpgradeRequired", err)
	}
	if got := s.calls.Load(); got != 0 {
		t.Fatalf("starter calls = %d, want 0", got)
	}
}

func TestEnsureAttachedRetryAfterRestart(t *testing.T) {
	var started atomic.Bool
	h := &fakeHandshaker{
		fn: func() (protocol.HelloAck, error) {
			if started.Load() {
				return protocol.HelloAck{Accepted: true}, nil
			}
			return protocol.HelloAck{
				Accepted:          false,
				ErrorCode:         protocol.ErrorCodeIncompatible,
				RetryAfterRestart: true,
			}, nil
		},
	}
	s := &fakeStarter{start: func() { started.Store(true) }}
	o := NewAutostartOrchestrator(h, s)
	o.sleepFn = func(_ time.Duration) {}
	o.backoffFn = func(_ int) time.Duration { return 0 }

	ack, err := o.EnsureAttached(context.Background(), protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "cli",
		ClientVersion:   "dev",
	})
	if err != nil {
		t.Fatalf("EnsureAttached err: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("ack.Accepted = false, want true")
	}
}
