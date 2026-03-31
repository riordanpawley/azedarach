package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestIdleTimeoutTriggersGracefulShutdownOrder(t *testing.T) {
	var mu sync.Mutex
	steps := make([]string, 0, 3)
	addStep := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, step)
	}

	s := NewIdleSupervisor(25*time.Millisecond, ShutdownHooks{
		StopIntake: func() error { addStep("stop_intake"); return nil },
		DrainInFlight: func(context.Context) error {
			addStep("drain")
			return nil
		},
		CloseTransport: func() error { addStep("close_transport"); return nil },
	})
	s.Start()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.WaitStopped(ctx); err != nil {
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
	s := NewIdleSupervisor(20*time.Millisecond, ShutdownHooks{
		StopIntake: func() error { return nil },
		DrainInFlight: func(context.Context) error {
			return nil
		},
		CloseTransport: func() error {
			reachedClose <- struct{}{}
			return nil
		},
	})
	s.Start()
	if err := s.BeginOperation(); err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	select {
	case <-reachedClose:
		t.Fatal("shutdown closed transport before in-flight operation ended")
	default:
	}

	s.EndOperation()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.WaitStopped(ctx); err != nil {
		t.Fatalf("WaitStopped after EndOperation: %v", err)
	}
}

func TestRecordActivityResetsIdleShutdownTimer(t *testing.T) {
	reachedClose := make(chan struct{}, 1)
	s := NewIdleSupervisor(40*time.Millisecond, ShutdownHooks{
		StopIntake: func() error { return nil },
		DrainInFlight: func(context.Context) error {
			return nil
		},
		CloseTransport: func() error {
			reachedClose <- struct{}{}
			return nil
		},
	})
	s.Start()

	time.Sleep(15 * time.Millisecond)
	s.RecordActivity()

	time.Sleep(35 * time.Millisecond)
	select {
	case <-reachedClose:
		t.Fatal("shutdown fired before refreshed idle deadline")
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.WaitStopped(ctx); err != nil {
		t.Fatalf("WaitStopped after RecordActivity: %v", err)
	}
}

func TestBeginOperationRejectedWhenStopping(t *testing.T) {
	s := NewIdleSupervisor(10*time.Millisecond, ShutdownHooks{})
	s.Start()
	time.Sleep(40 * time.Millisecond)

	err := s.BeginOperation()
	if err == nil {
		t.Fatal("expected ErrShuttingDown")
	}
	if err != ErrShuttingDown {
		t.Fatalf("BeginOperation err = %v, want %v", err, ErrShuttingDown)
	}
}
