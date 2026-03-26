package linearsync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type manualTicker struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func newManualTicker() *manualTicker {
	return &manualTicker{ch: make(chan time.Time, 16)}
}

func (t *manualTicker) C() <-chan time.Time { return t.ch }

func (t *manualTicker) Stop() { t.stopped.Store(true) }

func (t *manualTicker) Tick(ts time.Time) {
	t.ch <- ts
}

func TestQueueBurstAndSustainedRate(t *testing.T) {
	ticker := newManualTicker()
	var nowNanos atomic.Int64
	nowNanos.Store(time.Unix(0, 0).UnixNano())

	q, err := New(Config{
		RatePerSecond: 1,
		Burst:         2,
		Now: func() time.Time {
			return time.Unix(0, nowNanos.Add(int64(time.Second))).UTC()
		},
		NewTicker: func(time.Duration) Ticker {
			return ticker
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- q.Run(ctx)
	}()

	started := make(chan string, 3)
	release := make(chan struct{})
	work := func(id string) func(context.Context) error {
		return func(context.Context) error {
			started <- id
			<-release
			return nil
		}
	}

	sub1, err := q.Submit(ctx, Request{ID: "one", ProjectID: "proj", IssueID: "az-1", Kind: "sync", DedupeKey: "one", Work: work("one")})
	if err != nil {
		t.Fatalf("Submit(one) error = %v", err)
	}
	sub2, err := q.Submit(ctx, Request{ID: "two", ProjectID: "proj", IssueID: "az-2", Kind: "sync", DedupeKey: "two", Work: work("two")})
	if err != nil {
		t.Fatalf("Submit(two) error = %v", err)
	}
	sub3, err := q.Submit(ctx, Request{ID: "three", ProjectID: "proj", IssueID: "az-3", Kind: "sync", DedupeKey: "three", Work: work("three")})
	if err != nil {
		t.Fatalf("Submit(three) error = %v", err)
	}

	expectStartSet := func(want ...string) {
		t.Helper()
		remaining := make(map[string]struct{}, len(want))
		for _, id := range want {
			remaining[id] = struct{}{}
		}
		deadline := time.After(time.Second)
		for len(remaining) > 0 {
			select {
			case got := <-started:
				if _, ok := remaining[got]; !ok {
					t.Fatalf("unexpected start %q, want one of %v", got, want)
				}
				delete(remaining, got)
			case <-deadline:
				t.Fatalf("timed out waiting for starts %v", want)
			}
		}
	}

	expectNoStart := func() {
		t.Helper()
		select {
		case got := <-started:
			t.Fatalf("unexpected start before refill: %q", got)
		default:
		}
	}

	expectStartSet("one", "two")
	expectNoStart()

	ticker.Tick(time.Unix(0, nowNanos.Load()).Add(time.Second))
	select {
	case got := <-started:
		if got != "three" {
			t.Fatalf("start = %q, want %q", got, "three")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for third start")
	}
	expectNoStart()

	close(release)

	checkResult := func(name string, ch <-chan Result) {
		t.Helper()
		select {
		case res, ok := <-ch:
			if !ok {
				t.Fatalf("%s result channel closed without value", name)
			}
			if res.Err != nil {
				t.Fatalf("%s result error = %v, want nil", name, res.Err)
			}
			if res.RequestID == "" || res.ProjectID != "proj" || res.IssueID == "" || res.Kind != "sync" {
				t.Fatalf("%s result = %+v, want populated request metadata", name, res)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s result", name)
		}
	}

	checkResult("one", sub1.Done)
	checkResult("two", sub2.Done)
	checkResult("three", sub3.Done)

	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue shutdown")
	}
}

func TestQueueDedupesInFlightRequests(t *testing.T) {
	ticker := newManualTicker()
	var nowNanos atomic.Int64
	nowNanos.Store(time.Unix(0, 0).UnixNano())

	q, err := New(Config{
		RatePerSecond: 1,
		Burst:         1,
		Now: func() time.Time {
			return time.Unix(0, nowNanos.Add(int64(time.Second))).UTC()
		},
		NewTicker: func(time.Duration) Ticker {
			return ticker
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- q.Run(ctx)
	}()

	var executions atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	work := func(context.Context) error {
		executions.Add(1)
		started <- struct{}{}
		<-release
		return nil
	}

	sub1, err := q.Submit(ctx, Request{ID: "one", ProjectID: "proj", IssueID: "az-1", Kind: "sync", DedupeKey: "same", Work: work})
	if err != nil {
		t.Fatalf("Submit(one) error = %v", err)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first execution to start")
	}

	sub2, err := q.Submit(ctx, Request{ID: "two", ProjectID: "proj", IssueID: "az-1", Kind: "sync", DedupeKey: "same", Work: work})
	if err != nil {
		t.Fatalf("Submit(two) error = %v", err)
	}
	if !sub2.Deduped {
		t.Fatal("duplicate submission should report Deduped=true")
	}

	close(release)

	checkResult := func(name string, ch <-chan Result) Result {
		t.Helper()
		select {
		case res, ok := <-ch:
			if !ok {
				t.Fatalf("%s result channel closed without value", name)
			}
			if res.Err != nil {
				t.Fatalf("%s result error = %v, want nil", name, res.Err)
			}
			return res
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s result", name)
		}
		return Result{}
	}

	got1 := checkResult("first", sub1.Done)
	got2 := checkResult("duplicate", sub2.Done)

	if got1.RequestID != got2.RequestID || got1.ProjectID != got2.ProjectID || got1.IssueID != got2.IssueID || got1.Kind != got2.Kind {
		t.Fatalf("duplicate result = %+v, want to match original result %+v", got2, got1)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions = %d, want 1", executions.Load())
	}

	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue shutdown")
	}
}

func TestQueueRejectsInvalidConfigAndRequest(t *testing.T) {
	if _, err := New(Config{RatePerSecond: 0, Burst: 1}); err == nil {
		t.Fatal("New() with zero rate should fail")
	}
	if _, err := New(Config{RatePerSecond: 1, Burst: 0}); err == nil {
		t.Fatal("New() with zero burst should fail")
	}

	q, err := New(Config{RatePerSecond: 1, Burst: 1})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = q.Submit(context.Background(), Request{ID: "x", ProjectID: "proj", IssueID: "az-1", Kind: "sync"})
	if err == nil {
		t.Fatal("Submit() without work should fail")
	}
}
