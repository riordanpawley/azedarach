package daemon

import (
	"context"
	"errors"
	"testing"
)

func TestContextOperationLockAttributesImmediatePredecessor(t *testing.T) {
	var lock contextOperationLock
	releaseHolder, err := acquireOperationLockForTest(&lock, context.Background(), "holder")
	if err != nil {
		t.Fatal(err)
	}
	queued := make(chan struct{})
	attributed := make(chan string, 1)
	ctx := withContextOperationLockQueuedHookForTest(context.Background(), func(string) { close(queued) })
	ctx = withContextOperationLockWaitHookForTest(ctx, func(_, holder string) { attributed <- holder })
	done := make(chan error, 1)
	go func() {
		predecessor, err := lock.acquire(ctx, "waiter")
		if err == nil {
			lock.release()
		}
		if predecessor != "holder" && err == nil {
			t.Errorf("predecessor = %q, want holder", predecessor)
		}
		done <- err
	}()
	<-queued
	releaseHolder()
	if got := <-attributed; got != "holder" {
		t.Fatalf("attributed holder = %q, want holder", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestContextOperationLockAttributesHolderTurnover(t *testing.T) {
	var lock contextOperationLock
	releaseA, err := acquireOperationLockForTest(&lock, context.Background(), "holder-a")
	if err != nil {
		t.Fatal(err)
	}
	bQueued := make(chan struct{})
	bAcquired := make(chan struct{})
	releaseB := make(chan struct{})
	bDone := make(chan error, 1)
	bCtx := withContextOperationLockQueuedHookForTest(context.Background(), func(string) { close(bQueued) })
	go func() {
		predecessor, err := lock.acquire(bCtx, "holder-b")
		if err == nil && predecessor != "holder-a" {
			t.Errorf("holder B predecessor = %q, want holder-a", predecessor)
		}
		close(bAcquired)
		if err == nil {
			<-releaseB
			lock.release()
		}
		bDone <- err
	}()
	<-bQueued

	wQueued := make(chan struct{})
	wAttributed := make(chan string, 1)
	wDone := make(chan error, 1)
	wCtx := withContextOperationLockQueuedHookForTest(context.Background(), func(string) { close(wQueued) })
	wCtx = withContextOperationLockWaitHookForTest(wCtx, func(_, holder string) { wAttributed <- holder })
	go func() {
		predecessor, err := lock.acquire(wCtx, "waiter")
		if err == nil {
			lock.release()
		}
		if predecessor != "holder-b" && err == nil {
			t.Errorf("waiter predecessor = %q, want holder-b", predecessor)
		}
		wDone <- err
	}()
	<-wQueued
	releaseA()
	<-bAcquired
	close(releaseB)
	if got := <-wAttributed; got != "holder-b" {
		t.Fatalf("attributed holder = %q, want holder-b", got)
	}
	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
	if err := <-wDone; err != nil {
		t.Fatal(err)
	}
}

func TestContextOperationLockQueuedCancellationPreservesHolder(t *testing.T) {
	var lock contextOperationLock
	releaseHolder, err := acquireOperationLockForTest(&lock, context.Background(), "holder")
	if err != nil {
		t.Fatal(err)
	}
	queued := make(chan struct{})
	waitCtx, cancel := context.WithCancel(context.Background())
	waitCtx = withContextOperationLockQueuedHookForTest(waitCtx, func(string) { close(queued) })
	done := make(chan error, 1)
	go func() {
		_, err := lock.acquire(waitCtx, "canceled-waiter")
		done <- err
	}()
	<-queued
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation error = %v, want context.Canceled", err)
	}
	if holder := lock.currentHolder(); holder != "holder" {
		t.Fatalf("holder after queued cancellation = %q, want holder", holder)
	}
	releaseHolder()
}

func acquireOperationLockForTest(lock *contextOperationLock, ctx context.Context, operation string) (func(), error) {
	_, err := lock.acquire(ctx, operation)
	if err != nil {
		return nil, err
	}
	return lock.release, nil
}
