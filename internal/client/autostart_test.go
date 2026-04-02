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
	startCalls   atomic.Int32
	replaceCalls atomic.Int32
	start        func()
	replace      func(context.Context) error
	startErr     error
}

func (s *fakeStarter) Start(ctx context.Context) error {
	_ = ctx
	s.startCalls.Add(1)
	if s.start != nil {
		s.start()
	}
	if s.startErr != nil {
		return s.startErr
	}
	return nil
}

func (s *fakeStarter) Replace(ctx context.Context) error {
	s.replaceCalls.Add(1)
	if s.replace != nil {
		return s.replace(ctx)
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

	if got := s.startCalls.Load(); got != 1 {
		t.Fatalf("starter calls = %d, want 1", got)
	}
}

func TestEnsureAttachedTransientHandshakeFailureDoesNotAutostart(t *testing.T) {
	var calls atomic.Int32
	h := &fakeHandshaker{
		fn: func() (protocol.HelloAck, error) {
			if calls.Add(1) == 1 {
				return protocol.HelloAck{}, errors.New("dial unavailable")
			}
			return protocol.HelloAck{Accepted: true}, nil
		},
	}
	s := &fakeStarter{}
	o := NewAutostartOrchestrator(h, s)
	o.sleepFn = func(_ time.Duration) {}

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
	if got := s.startCalls.Load(); got != 0 {
		t.Fatalf("starter calls = %d, want 0", got)
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
	if got := s.startCalls.Load(); got != 0 {
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
	s := &fakeStarter{
		replace: func(context.Context) error {
			started.Store(true)
			return nil
		},
	}
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
	if got := s.replaceCalls.Load(); got != 1 {
		t.Fatalf("replace calls = %d, want 1", got)
	}
}

func TestEnsureAttachedReplacementFailureTypedError(t *testing.T) {
	h := &fakeHandshaker{
		fn: func() (protocol.HelloAck, error) {
			return protocol.HelloAck{
				Accepted:          false,
				ErrorCode:         protocol.ErrorCodeIncompatible,
				RetryAfterRestart: true,
			}, nil
		},
	}
	s := &fakeStarter{
		replace: func(context.Context) error {
			return errors.New("replace failed")
		},
	}
	o := NewAutostartOrchestrator(h, s)

	_, err := o.EnsureAttached(context.Background(), protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "cli",
		ClientVersion:   "dev",
	})
	if !errors.Is(err, ErrUpgradeRequired) {
		t.Fatalf("err = %v, want ErrUpgradeRequired", err)
	}
	if got := s.replaceCalls.Load(); got != 1 {
		t.Fatalf("replace calls = %d, want 1", got)
	}
	if got := s.startCalls.Load(); got != 0 {
		t.Fatalf("start calls = %d, want 0", got)
	}
}

func TestEnsureAttachedReplacementSingleflight(t *testing.T) {
	var replaced atomic.Bool
	h := &fakeHandshaker{
		fn: func() (protocol.HelloAck, error) {
			if replaced.Load() {
				return protocol.HelloAck{Accepted: true}, nil
			}
			return protocol.HelloAck{
				Accepted:          false,
				ErrorCode:         protocol.ErrorCodeIncompatible,
				RetryAfterRestart: true,
			}, nil
		},
	}
	s := &fakeStarter{
		replace: func(context.Context) error {
			replaced.Store(true)
			return nil
		},
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

	if got := s.replaceCalls.Load(); got != 1 {
		t.Fatalf("replace calls = %d, want 1", got)
	}
	if got := s.startCalls.Load(); got != 0 {
		t.Fatalf("start calls = %d, want 0", got)
	}
}

func TestEnsureAttachedReplacementCancellationDeterministic(t *testing.T) {
	h := &fakeHandshaker{
		fn: func() (protocol.HelloAck, error) {
			return protocol.HelloAck{
				Accepted:          false,
				ErrorCode:         protocol.ErrorCodeIncompatible,
				RetryAfterRestart: true,
			}, nil
		},
	}
	s := &fakeStarter{
		replace: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	o := NewAutostartOrchestrator(h, s)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := o.EnsureAttached(ctx, protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "tui",
		ClientVersion:   "dev",
	})
	if !errors.Is(err, ErrUpgradeRequired) {
		t.Fatalf("err = %v, want ErrUpgradeRequired", err)
	}
	if got := s.replaceCalls.Load(); got != 1 {
		t.Fatalf("replace calls = %d, want 1", got)
	}
}
