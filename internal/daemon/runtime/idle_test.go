package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

type manualIdleTimer struct {
	callback   func()
	resetCalls int
	resetAfter time.Duration
}

func (t *manualIdleTimer) Reset(after time.Duration) bool {
	t.resetCalls++
	t.resetAfter = after
	return true
}

func manualIdleTimerOption(target **manualIdleTimer) IdleSupervisorOption {
	return WithIdleTimerFactory(func(_ time.Duration, callback func()) IdleTimer {
		timer := &manualIdleTimer{callback: callback}
		*target = timer
		return timer
	})
}

func TestIdleTimeoutTriggersGracefulShutdownOrder(t *testing.T) {
	var mu sync.Mutex
	steps := make([]string, 0, 3)
	addStep := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, step)
	}

	var timer *manualIdleTimer
	s := NewIdleSupervisor(25*time.Millisecond, ShutdownHooks{
		StopIntake: func() error { addStep("stop_intake"); return nil },
		DrainInFlight: func(context.Context) error {
			addStep("drain")
			return nil
		},
		CloseTransport: func() error { addStep("close_transport"); return nil },
	}, manualIdleTimerOption(&timer))
	s.Start()
	timer.callback()

	if err := s.WaitStopped(context.Background()); err != nil {
		t.Fatalf("WaitStopped: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), steps...)
	mu.Unlock()
	want := []string{"stop_intake", "drain", "close_transport"}
	if len(got) != len(want) {
		t.Fatalf("steps len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestShutdownWaitsForInFlightOperations(t *testing.T) {
	reachedClose := make(chan struct{}, 1)
	stopping := make(chan struct{})
	var timer *manualIdleTimer
	s := NewIdleSupervisor(20*time.Millisecond, ShutdownHooks{
		StopIntake: func() error { close(stopping); return nil },
		DrainInFlight: func(context.Context) error {
			return nil
		},
		CloseTransport: func() error {
			reachedClose <- struct{}{}
			return nil
		},
	}, manualIdleTimerOption(&timer))
	s.Start()
	if err := s.BeginOperation(); err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}

	go timer.callback()
	<-stopping
	select {
	case <-reachedClose:
		t.Fatal("shutdown closed transport before in-flight operation ended")
	default:
	}

	s.EndOperation()
	if err := s.WaitStopped(context.Background()); err != nil {
		t.Fatalf("WaitStopped after EndOperation: %v", err)
	}
}

func TestRecordActivityResetsIdleShutdownTimer(t *testing.T) {
	var timer *manualIdleTimer
	s := NewIdleSupervisor(200*time.Millisecond, ShutdownHooks{
		StopIntake: func() error { return nil },
		DrainInFlight: func(context.Context) error {
			return nil
		},
		CloseTransport: func() error { return nil },
	}, manualIdleTimerOption(&timer))
	s.Start()
	s.RecordActivity()
	if timer.resetCalls != 1 || timer.resetAfter != 200*time.Millisecond {
		t.Fatalf("timer reset = %d calls after %v, want one call after 200ms", timer.resetCalls, timer.resetAfter)
	}

	timer.callback()
	if err := s.WaitStopped(context.Background()); err != nil {
		t.Fatalf("WaitStopped after RecordActivity: %v", err)
	}
}

func TestBeginOperationRejectedWhenStopping(t *testing.T) {
	var timer *manualIdleTimer
	s := NewIdleSupervisor(10*time.Millisecond, ShutdownHooks{}, manualIdleTimerOption(&timer))
	s.Start()
	timer.callback()

	err := s.BeginOperation()
	if err == nil {
		t.Fatal("expected ErrShuttingDown")
	}
	if err != ErrShuttingDown {
		t.Fatalf("BeginOperation err = %v, want %v", err, ErrShuttingDown)
	}
}
