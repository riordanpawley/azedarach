package daemon

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReconcileQueueDedupesRunningJob(t *testing.T) {
	t.Parallel()

	q := newReconcileQueue[string](reconcileQueueConfig{
		Name:    "test_dedupe_running",
		Workers: 1,
	})
	t.Cleanup(func() {
		_ = q.Close()
	})

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32

	first, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "same",
		Priority: reconcilePriorityBackground,
		Reason:   "first",
		Work: func(context.Context) (string, error) {
			calls.Add(1)
			started <- struct{}{}
			<-release
			return "done", nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first job to start")
	}

	second, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "same",
		Priority: reconcilePriorityManual,
		Reason:   "second",
		Work: func(context.Context) (string, error) {
			t.Fatal("deduped running job should not execute twice")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if !second.Deduped {
		t.Fatal("expected second submission to dedupe")
	}
	if second.Reprioritized {
		t.Fatal("running job should not reprioritize")
	}

	close(release)

	firstResult, err := first.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait first: %v", err)
	}
	secondResult, err := second.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait second: %v", err)
	}
	if firstResult.Err != nil || secondResult.Err != nil {
		t.Fatalf("unexpected result errors: first=%v second=%v", firstResult.Err, secondResult.Err)
	}
	if firstResult.Value != "done" || secondResult.Value != "done" {
		t.Fatalf("unexpected results: first=%q second=%q", firstResult.Value, secondResult.Value)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("work calls = %d, want 1", got)
	}

	counters := q.snapshotCounters()
	if counters.Enqueued != 1 || counters.Dequeued != 1 || counters.Deduped != 1 || counters.Reprioritized != 0 {
		t.Fatalf("queue counters = %+v", counters)
	}
}

func TestReconcileQueueReprioritizesPendingJobOrdering(t *testing.T) {
	t.Parallel()

	q := newReconcileQueue[string](reconcileQueueConfig{
		Name:    "test_reprioritize",
		Workers: 1,
	})
	t.Cleanup(func() {
		_ = q.Close()
	})

	busyStarted := make(chan struct{}, 1)
	releaseBusy := make(chan struct{})
	var (
		orderMu sync.Mutex
		order   []string
	)

	appendOrder := func(key string) {
		orderMu.Lock()
		defer orderMu.Unlock()
		order = append(order, key)
	}

	busy, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "busy",
		Priority: reconcilePriorityBackground,
		Reason:   "busy",
		Work: func(context.Context) (string, error) {
			appendOrder("busy")
			busyStarted <- struct{}{}
			<-releaseBusy
			return "busy", nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue busy: %v", err)
	}
	select {
	case <-busyStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for busy job to start")
	}

	later, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "later",
		Priority: reconcilePriorityBackground,
		Reason:   "later",
		Work: func(context.Context) (string, error) {
			appendOrder("later")
			return "later", nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue later: %v", err)
	}

	target, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "target",
		Priority: reconcilePriorityBackground,
		Reason:   "target-background",
		Work: func(context.Context) (string, error) {
			appendOrder("target")
			return "target", nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue target: %v", err)
	}

	targetManual, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "target",
		Priority: reconcilePriorityManual,
		Reason:   "target-manual",
		Work: func(context.Context) (string, error) {
			t.Fatal("reprioritized pending job should reuse existing work")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue target manual: %v", err)
	}
	if !targetManual.Deduped || !targetManual.Reprioritized {
		t.Fatalf("target manual submission = %+v, want deduped reprioritized", targetManual)
	}

	snapshot := q.snapshot()
	if len(snapshot.Pending) != 2 || snapshot.Pending[0] != "target" || snapshot.Pending[1] != "later" {
		t.Fatalf("pending order = %v, want [target later]", snapshot.Pending)
	}

	close(releaseBusy)

	for _, sub := range []reconcileQueueSubmission[string]{busy, later, target, targetManual} {
		if _, err := sub.Wait(context.Background()); err != nil {
			t.Fatalf("wait submission: %v", err)
		}
	}

	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	wantOrder := []string{"busy", "target", "later"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("execution order = %v, want %v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("execution order = %v, want %v", gotOrder, wantOrder)
		}
	}

	counters := q.snapshotCounters()
	if counters.Enqueued != 3 || counters.Dequeued != 3 || counters.Deduped != 1 || counters.Reprioritized != 1 {
		t.Fatalf("queue counters = %+v", counters)
	}
}

func TestReconcileQueueHonorsWorkerLimit(t *testing.T) {
	t.Parallel()

	q := newReconcileQueue[int](reconcileQueueConfig{
		Name:    "test_worker_limit",
		Workers: 2,
	})
	t.Cleanup(func() {
		_ = q.Close()
	})

	release := make(chan struct{})
	started := make(chan struct{}, 5)
	var current atomic.Int32
	var maxSeen atomic.Int32

	updateMax := func(v int32) {
		for {
			prev := maxSeen.Load()
			if v <= prev {
				return
			}
			if maxSeen.CompareAndSwap(prev, v) {
				return
			}
		}
	}

	subs := make([]reconcileQueueSubmission[int], 0, 5)
	for i := 0; i < 5; i++ {
		sub, err := q.Enqueue(reconcileQueueRequest[int]{
			Key:      string(rune('a' + i)),
			Priority: reconcilePriorityBackground,
			Reason:   "worker-limit",
			Work: func(context.Context) (int, error) {
				running := current.Add(1)
				updateMax(running)
				started <- struct{}{}
				<-release
				current.Add(-1)
				return 1, nil
			},
		})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		subs = append(subs, sub)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for workers to start")
		}
	}

	time.Sleep(25 * time.Millisecond)
	if got := maxSeen.Load(); got != 2 {
		t.Fatalf("max concurrent workers = %d, want 2", got)
	}

	close(release)
	for _, sub := range subs {
		if _, err := sub.Wait(context.Background()); err != nil {
			t.Fatalf("wait submission: %v", err)
		}
	}

	if got := maxSeen.Load(); got != 2 {
		t.Fatalf("max concurrent workers after completion = %d, want 2", got)
	}
}

func TestReconcileQueueWorkerRecoversFromPanicAndContinues(t *testing.T) {
	t.Parallel()

	q := newReconcileQueue[string](reconcileQueueConfig{
		Name:    "test_recover_panic",
		Workers: 1,
	})
	t.Cleanup(func() {
		_ = q.Close()
	})

	bad, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "bad",
		Priority: reconcilePriorityBackground,
		Reason:   "panic",
		Work: func(context.Context) (string, error) {
			panic("boom")
		},
	})
	if err != nil {
		t.Fatalf("enqueue bad: %v", err)
	}

	good, err := q.Enqueue(reconcileQueueRequest[string]{
		Key:      "good",
		Priority: reconcilePriorityBackground,
		Reason:   "after-panic",
		Work: func(context.Context) (string, error) {
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue good: %v", err)
	}

	badResult, err := bad.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait bad: %v", err)
	}
	if badResult.Err == nil {
		t.Fatal("expected panic job to return error")
	}
	if !strings.Contains(badResult.Err.Error(), "panic in work") {
		t.Fatalf("panic error missing marker: %v", badResult.Err)
	}

	goodResult, err := good.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait good: %v", err)
	}
	if goodResult.Err != nil {
		t.Fatalf("good job should not fail: %v", goodResult.Err)
	}
	if goodResult.Value != "ok" {
		t.Fatalf("good result value = %q, want ok", goodResult.Value)
	}
}
