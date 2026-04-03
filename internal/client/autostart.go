package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
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
	handshaker Handshaker
	starter    Starter
	replacer   Replacer
	group      singleflight.Group
	preStartRetries int
	preStartBackoff func(attempt int) time.Duration
	maxRetries int
	backoffFn  func(attempt int) time.Duration
	sleepFn    func(time.Duration)
	onceMu     sync.Mutex
	spawned    bool
	replaced   bool
	startKey   string
	replaceKey string
}

// NewAutostartOrchestrator returns a default autostart orchestrator.
func NewAutostartOrchestrator(handshaker Handshaker, starter Starter) *AutostartOrchestrator {
	var replacer Replacer
	if r, ok := starter.(Replacer); ok {
		replacer = r
	}

	return &AutostartOrchestrator{
		handshaker: handshaker,
		starter:    starter,
		replacer:   replacer,
		// Avoid unnecessary daemon spawn when handshake has a short transient blip.
		preStartRetries: 3,
		preStartBackoff: func(_ int) time.Duration { return 100 * time.Millisecond },
		// Daemon boot can take >300ms on cold starts; allow a wider attach window.
		maxRetries: 20,
		backoffFn: func(attempt int) time.Duration {
			return time.Duration(attempt+1) * 100 * time.Millisecond
		},
		sleepFn:    time.Sleep,
		startKey:   "daemon-autostart",
		replaceKey: "daemon-replace",
	}
}

// EnsureAttached performs handshake and autostarts daemon if needed.
func (o *AutostartOrchestrator) EnsureAttached(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	ack, err := o.handshaker.Handshake(ctx, hello)
	if err == nil && ack.Accepted {
		return ack, nil
	}
	if err != nil {
		for attempt := 0; attempt < o.preStartRetries; attempt++ {
			if ctx.Err() != nil {
				break
			}
			o.sleepFn(o.preStartBackoff(attempt))
			ack, err = o.handshaker.Handshake(ctx, hello)
			if err == nil && ack.Accepted {
				return ack, nil
			}
		}
	}
	if err == nil {
		if ack.ErrorCode.IsCompatibilityFailure() && !ack.RetryAfterRestart {
			return ack, ErrUpgradeRequired
		}
		if ack.ErrorCode.IsCompatibilityFailure() && ack.RetryAfterRestart {
			if replaceErr := o.replaceDaemon(ctx); replaceErr != nil {
				return protocol.HelloAck{}, fmt.Errorf("%w: replace daemon: %v", ErrUpgradeRequired, replaceErr)
			}
		} else {
			// Daemon responded, so do not restart/restart-replace on generic handshake rejection.
			return ack, fmt.Errorf("daemon handshake rejected: %s", ack.Reason)
		}
	} else {
		if startErr := o.startDaemon(ctx); startErr != nil {
			return protocol.HelloAck{}, fmt.Errorf("autostart daemon: %w", startErr)
		}
	}

	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		ack, err = o.handshaker.Handshake(ctx, hello)
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
			o.sleepFn(o.backoffFn(attempt))
		}
	}

	if err != nil {
		return protocol.HelloAck{}, fmt.Errorf("attach after autostart: %w", err)
	}
	if ack.ErrorCode.IsCompatibilityFailure() {
		return ack, ErrUpgradeRequired
	}
	return ack, fmt.Errorf("attach rejected after autostart: %s", ack.ErrorCode)
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

func (o *AutostartOrchestrator) replaceDaemon(ctx context.Context) error {
	_, replaceErr, _ := o.group.Do(o.replaceKey, func() (any, error) {
		if o.isReplaced() {
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
		o.markReplaced()
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

func (o *AutostartOrchestrator) isReplaced() bool {
	o.onceMu.Lock()
	defer o.onceMu.Unlock()
	return o.replaced
}

func (o *AutostartOrchestrator) markReplaced() {
	o.onceMu.Lock()
	defer o.onceMu.Unlock()
	o.replaced = true
}
