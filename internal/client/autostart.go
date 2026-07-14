package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"golang.org/x/sync/singleflight"
)

var (
	// ErrUpgradeRequired indicates persistent protocol incompatibility.
	ErrUpgradeRequired = errors.New("daemon/client upgrade required")
)

// Handshaker performs daemon compatibility handshake.
type Handshaker interface {
	Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error)
}

// Starter performs daemon process startup.
type Starter interface {
	Start(ctx context.Context) error
}

// Replacer performs controlled daemon replacement on compatibility mismatch.
type Replacer interface {
	Replace(ctx context.Context) error
}

// AutostartOrchestrator coordinates attach/start/reattach with singleflight.
type AutostartOrchestrator struct {
	handshaker      Handshaker
	starter         Starter
	replacer        Replacer
	group           singleflight.Group
	preStartRetries int
	preStartBackoff func(attempt int) time.Duration
	maxRetries      int
	backoffFn       func(attempt int) time.Duration
	sleepFn         func(time.Duration)
	onceMu          sync.Mutex
	spawned         bool
	startKey        string
	replaceKey      string
}

// AutostartRetryPolicy controls bounded handshake retries around daemon start.
type AutostartRetryPolicy struct {
	PreStartRetries   int
	PreStartBackoff   time.Duration
	MaxAttachRetries  int
	AttachBackoffStep time.Duration
}

// DefaultAutostartRetryPolicy preserves production attach behavior.
func DefaultAutostartRetryPolicy() AutostartRetryPolicy {
	return AutostartRetryPolicy{
		PreStartRetries:   3,
		PreStartBackoff:   100 * time.Millisecond,
		MaxAttachRetries:  20,
		AttachBackoffStep: 100 * time.Millisecond,
	}
}

// NewAutostartOrchestrator returns a default autostart orchestrator.
func NewAutostartOrchestrator(handshaker Handshaker, starter Starter) *AutostartOrchestrator {
	var replacer Replacer
	if r, ok := starter.(Replacer); ok {
		replacer = r
	}

	policy := DefaultAutostartRetryPolicy()
	return &AutostartOrchestrator{
		handshaker: handshaker,
		starter:    starter,
		replacer:   replacer,
		// Avoid unnecessary daemon spawn when handshake has a short transient blip.
		preStartRetries: policy.PreStartRetries,
		preStartBackoff: func(_ int) time.Duration { return policy.PreStartBackoff },
		// Daemon boot can take >300ms on cold starts; allow a wider attach window.
		maxRetries: policy.MaxAttachRetries,
		backoffFn: func(attempt int) time.Duration {
			return time.Duration(attempt+1) * policy.AttachBackoffStep
		},
		startKey:   "daemon-autostart",
		replaceKey: "daemon-replace",
	}
}

// WithRetryPolicy overrides bounded autostart retry counts and backoffs.
func (o *AutostartOrchestrator) WithRetryPolicy(policy AutostartRetryPolicy) *AutostartOrchestrator {
	if o == nil {
		return nil
	}
	o.preStartRetries = max(policy.PreStartRetries, 0)
	o.preStartBackoff = func(_ int) time.Duration { return max(policy.PreStartBackoff, 0) }
	o.maxRetries = max(policy.MaxAttachRetries, 0)
	o.backoffFn = func(attempt int) time.Duration {
		return time.Duration(attempt+1) * max(policy.AttachBackoffStep, 0)
	}
	return o
}

// EnsureAttached performs handshake and autostarts daemon if needed.
func (o *AutostartOrchestrator) EnsureAttached(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	startedAt := time.Now()
	defer func() {
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "autostart.ensure_attached", startedAt, "client_name", hello.ClientName)
	}()
	handshakeStartedAt := time.Now()
	ack, err := o.handshaker.Handshake(ctx, hello)
	latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "autostart.initial_handshake", handshakeStartedAt, "client_name", hello.ClientName, "accepted", err == nil && ack.Accepted, "error", err)
	if err == nil && ack.Accepted {
		return ack, nil
	}
	if err != nil {
		for attempt := 0; attempt < o.preStartRetries; attempt++ {
			if ctx.Err() != nil {
				break
			}
			if !o.sleep(ctx, o.preStartBackoff(attempt)) {
				break
			}
			retryStartedAt := time.Now()
			ack, err = o.handshaker.Handshake(ctx, hello)
			latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "autostart.pre_start_handshake", retryStartedAt, "client_name", hello.ClientName, "attempt", attempt+1, "accepted", err == nil && ack.Accepted, "error", err)
			if err == nil && ack.Accepted {
				return ack, nil
			}
		}
		if ctx.Err() != nil {
			return protocol.HelloAck{}, ctx.Err()
		}
	}
	if err == nil {
		if ack.ErrorCode.IsCompatibilityFailure() && !ack.RetryAfterRestart {
			return ack, ErrUpgradeRequired
		}
		if ack.ErrorCode.IsCompatibilityFailure() && ack.RetryAfterRestart {
			if replaceErr := o.replaceDaemonAfterCompatibilityFailure(ctx, hello); replaceErr != nil {
				return protocol.HelloAck{}, fmt.Errorf("%w: replace daemon: %v", ErrUpgradeRequired, replaceErr)
			}
		} else {
			// Daemon responded, so do not restart/restart-replace on generic handshake rejection.
			return ack, fmt.Errorf("daemon handshake rejected: %s", ack.Reason)
		}
	} else {
		startDaemonStartedAt := time.Now()
		if startErr := o.startDaemon(ctx); startErr != nil {
			latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "autostart.start_daemon", startDaemonStartedAt, "client_name", hello.ClientName, "error", startErr)
			return protocol.HelloAck{}, fmt.Errorf("autostart daemon: %w", startErr)
		}
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "autostart.start_daemon", startDaemonStartedAt, "client_name", hello.ClientName)
	}

	return o.awaitAttached(ctx, hello)
}

func (o *AutostartOrchestrator) awaitAttached(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	var (
		ack protocol.HelloAck
		err error
	)
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		handshakeStartedAt := time.Now()
		ack, err = o.handshaker.Handshake(ctx, hello)
		latencytrace.LogPhaseContext(ctx, slog.Default(), "cli", "autostart.await_handshake", handshakeStartedAt, "client_name", hello.ClientName, "attempt", attempt+1, "accepted", err == nil && ack.Accepted, "error", err)
		if err == nil && ack.Accepted {
			return ack, nil
		}
		if err == nil && ack.ErrorCode.IsCompatibilityFailure() {
			if ack.RetryAfterRestart {
				return ack, fmt.Errorf("%w: compatibility mismatch persisted after replacement", ErrUpgradeRequired)
			}
			return ack, ErrUpgradeRequired
		}
		if attempt < o.maxRetries {
			if !o.sleep(ctx, o.backoffFn(attempt)) {
				break
			}
		}
	}
	if ctx.Err() != nil {
		return protocol.HelloAck{}, ctx.Err()
	}

	if err != nil {
		return protocol.HelloAck{}, fmt.Errorf("attach after autostart: %w", err)
	}
	if ack.ErrorCode.IsCompatibilityFailure() {
		return ack, ErrUpgradeRequired
	}
	return ack, fmt.Errorf("attach rejected after autostart: %s", ack.ErrorCode)
}

func (o *AutostartOrchestrator) sleep(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	if o.sleepFn == nil {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	done := make(chan struct{})
	go func() {
		o.sleepFn(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return false
	case <-done:
		return true
	}
}

func (o *AutostartOrchestrator) startDaemon(ctx context.Context) error {
	_, startErr, _ := o.group.Do(o.startKey, func() (any, error) {
		if o.isSpawned() {
			return nil, nil
		}
		if err := o.starter.Start(ctx); err != nil {
			return nil, err
		}
		o.markSpawned()
		return nil, nil
	})
	return startErr
}

func (o *AutostartOrchestrator) replaceDaemonAfterCompatibilityFailure(ctx context.Context, hello protocol.Hello) error {
	_, replaceErr, _ := o.group.Do(o.replaceKey, func() (any, error) {
		// Singleflight only collapses replacement work that is already in flight.
		// Re-check inside the group so late callers with a stale incompatibility
		// result do not perform another daemon replacement.
		ack, err := o.handshaker.Handshake(ctx, hello)
		if err == nil && ack.Accepted {
			return nil, nil
		}
		if o.replacer != nil {
			if err := o.replacer.Replace(ctx); err != nil {
				return nil, err
			}
		} else {
			if err := o.starter.Start(ctx); err != nil {
				return nil, err
			}
		}
		o.markSpawned()
		return nil, nil
	})
	return replaceErr
}

func (o *AutostartOrchestrator) isSpawned() bool {
	o.onceMu.Lock()
	defer o.onceMu.Unlock()
	return o.spawned
}

func (o *AutostartOrchestrator) markSpawned() {
	o.onceMu.Lock()
	defer o.onceMu.Unlock()
	o.spawned = true
}
