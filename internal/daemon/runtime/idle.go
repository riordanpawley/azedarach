package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

var (
	// ErrShuttingDown indicates the daemon is no longer accepting work.
	ErrShuttingDown = errors.New("daemon shutting down")
)

// ShutdownHooks define graceful shutdown primitives.
type ShutdownHooks struct {
	StopIntake     func() error
	DrainInFlight  func(context.Context) error
	CloseTransport func() error
	Logger         *slog.Logger
}

// IdleTimer is the resettable timer used to begin idle shutdown.
type IdleTimer interface {
	Reset(time.Duration) bool
}

// IdleSupervisorOption configures optional idle supervisor behavior.
type IdleSupervisorOption func(*IdleSupervisor)

// WithIdleTimerFactory supplies the timer implementation used by Start. Tests
// can use this seam to advance the idle transition with an explicit barrier.
func WithIdleTimerFactory(factory func(time.Duration, func()) IdleTimer) IdleSupervisorOption {
	return func(s *IdleSupervisor) {
		if factory != nil {
			s.timerFactory = factory
		}
	}
}

// IdleSupervisor coordinates idle shutdown and graceful draining.
type IdleSupervisor struct {
	timeout time.Duration
	hooks   ShutdownHooks

	mu           sync.Mutex
	cond         *sync.Cond
	timer        IdleTimer
	timerFactory func(time.Duration, func()) IdleTimer
	inFlight     int
	stopping     bool
	stopped      bool

	stoppedCh chan struct{}
}

// NewIdleSupervisor returns an idle shutdown supervisor.
func NewIdleSupervisor(timeout time.Duration, hooks ShutdownHooks, opts ...IdleSupervisorOption) *IdleSupervisor {
	s := &IdleSupervisor{
		timeout:   timeout,
		hooks:     hooks,
		stoppedCh: make(chan struct{}),
		timerFactory: func(timeout time.Duration, callback func()) IdleTimer {
			return time.AfterFunc(timeout, callback)
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Start activates idle timeout tracking.
func (s *IdleSupervisor) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil || s.stopped {
		return
	}
	s.timer = s.timerFactory(s.timeout, s.shutdown)
}

// RecordActivity resets the idle timer while running.
func (s *IdleSupervisor) RecordActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer == nil || s.stopping || s.stopped {
		return
	}
	s.timer.Reset(s.timeout)
}

// BeginOperation marks an in-flight operation if intake is still open.
func (s *IdleSupervisor) BeginOperation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.stopped {
		return ErrShuttingDown
	}
	s.inFlight++
	return nil
}

// EndOperation marks completion of an in-flight operation.
func (s *IdleSupervisor) EndOperation() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight > 0 {
		s.inFlight--
		if s.inFlight == 0 {
			s.cond.Broadcast()
		}
	}
}

// WaitStopped blocks until shutdown sequence completes or context expires.
func (s *IdleSupervisor) WaitStopped(ctx context.Context) error {
	select {
	case <-s.stoppedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *IdleSupervisor) shutdown() {
	s.mu.Lock()
	if s.stopping || s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	s.mu.Unlock()

	if s.hooks.StopIntake != nil {
		if err := s.hooks.StopIntake(); err != nil && s.hooks.Logger != nil {
			s.hooks.Logger.Warn("idle shutdown stop intake failed", "error", err)
		}
	}

	s.mu.Lock()
	for s.inFlight > 0 {
		s.cond.Wait()
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.hooks.DrainInFlight != nil {
		if err := s.hooks.DrainInFlight(ctx); err != nil && s.hooks.Logger != nil {
			s.hooks.Logger.Warn("idle shutdown drain in-flight failed", "error", err)
		}
	}
	if s.hooks.CloseTransport != nil {
		if err := s.hooks.CloseTransport(); err != nil && s.hooks.Logger != nil {
			s.hooks.Logger.Warn("idle shutdown close transport failed", "error", err)
		}
	}

	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	close(s.stoppedCh)
}

// Status returns runtime shutdown state for diagnostics.
func (s *IdleSupervisor) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.stopped:
		return "stopped"
	case s.stopping:
		return "stopping"
	default:
		return fmt.Sprintf("running:%d", s.inFlight)
	}
}
