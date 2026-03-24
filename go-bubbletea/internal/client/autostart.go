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

// AutostartOrchestrator coordinates attach/start/reattach with singleflight.
type AutostartOrchestrator struct {
	handshaker  Handshaker
	starter     Starter
	group       singleflight.Group
	maxRetries  int
	backoffFn   func(attempt int) time.Duration
	sleepFn     func(time.Duration)
	onceMu      sync.Mutex
	spawned     bool
	startKey    string
}

// NewAutostartOrchestrator returns a default autostart orchestrator.
func NewAutostartOrchestrator(handshaker Handshaker, starter Starter) *AutostartOrchestrator {
	return &AutostartOrchestrator{
		handshaker: handshaker,
		starter:    starter,
		maxRetries: 3,
		backoffFn: func(attempt int) time.Duration {
			return time.Duration(attempt+1) * 50 * time.Millisecond
		},
		sleepFn:  time.Sleep,
		startKey: "daemon-autostart",
	}
}

// EnsureAttached performs handshake and autostarts daemon if needed.
func (o *AutostartOrchestrator) EnsureAttached(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	ack, err := o.handshaker.Handshake(ctx, hello)
	if err == nil && ack.Accepted {
		return ack, nil
	}
	if err == nil && ack.ErrorCode.IsCompatibilityFailure() && !ack.RetryAfterRestart {
		return ack, ErrUpgradeRequired
	}

	if _, startErr, _ := o.group.Do(o.startKey, func() (any, error) {
		if o.isSpawned() {
			return nil, nil
		}
		if err := o.starter.Start(ctx); err != nil {
			return nil, err
		}
		o.markSpawned()
		return nil, nil
	}); startErr != nil {
		return protocol.HelloAck{}, fmt.Errorf("autostart daemon: %w", startErr)
	}

	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		ack, err = o.handshaker.Handshake(ctx, hello)
		if err == nil && ack.Accepted {
			return ack, nil
		}
		if err == nil && ack.ErrorCode.IsCompatibilityFailure() && !ack.RetryAfterRestart {
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
