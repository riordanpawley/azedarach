package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestSelectorSnapshotCacheCoalescesColdLoadsAndIsolatesResults(t *testing.T) {
	cache := newSelectorSnapshotCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	load := func() ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("fresh"), nil
	}

	results := make(chan error, 1)
	go func() {
		_, err := cache.loadCold(context.Background(), "key", load)
		results <- err
	}()
	<-started
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.loadCold(canceledCtx, "key", load); !errors.Is(err, context.Canceled) {
		t.Fatalf("coalesced canceled load error = %v, want context canceled", err)
	}
	close(release)
	if err := <-results; err != nil {
		t.Fatalf("cold load failed: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
	first, ok := cache.get("key")
	if !ok {
		t.Fatal("cached result missing")
	}
	second, ok := cache.get("key")
	if !ok {
		t.Fatal("second cached result missing")
	}
	first[0] = 'X'
	if string(second) != "fresh" {
		t.Fatalf("shared result mutated through caller: %q", second)
	}
	cached, ok := cache.get("key")
	if !ok || string(cached) != "fresh" {
		t.Fatalf("cached result = %q,%v, want fresh,true", cached, ok)
	}
}

func TestSelectorSnapshotCacheWarmReadDoesNotWaitForRefresh(t *testing.T) {
	cache := newSelectorSnapshotCache()
	cache.store("key", []byte("stale"))
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	load := func() ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("fresh"), nil
	}

	refresh, ok := cache.beginRefresh("key")
	if !ok {
		t.Fatal("initial refresh reservation failed")
	}
	done := make(chan error, 1)
	go func() {
		body, err := load()
		cache.finishLoad(refresh, body, err)
		done <- err
	}()
	<-started
	for range 4 {
		if _, ok := cache.beginRefresh("key"); ok {
			t.Fatal("duplicate refresh reservation succeeded")
		}
	}
	if cached, ok := cache.get("key"); !ok || string(cached) != "stale" {
		t.Fatalf("warm read = %q,%v, want stale,true", cached, ok)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitForSelectorSnapshot(t, cache, "key", "fresh")
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestSelectorSnapshotCacheRefreshReservationCanBeReleasedAfterFailure(t *testing.T) {
	cache := newSelectorSnapshotCache()
	cache.store("key", []byte("last-good"))
	refresh, ok := cache.beginRefresh("key")
	if !ok {
		t.Fatal("refresh reservation failed")
	}
	cache.finishLoad(refresh, nil, errors.New("refresh failed"))
	refresh, ok = cache.beginRefresh("key")
	if !ok {
		t.Fatal("released refresh reservation could not be reacquired")
	}
	cache.finishLoad(refresh, nil, errors.New("refresh failed again"))
	cached, ok := cache.get("key")
	if !ok || string(cached) != "last-good" {
		t.Fatalf("cached result = %q,%v, want last-good,true", cached, ok)
	}
}

func TestSelectorSnapshotCacheRejectsRefreshOlderThanEvictedColdLoad(t *testing.T) {
	cache := newSelectorSnapshotCache()
	cache.store("key", []byte("initial"))
	oldRefresh, ok := cache.beginRefresh("key")
	if !ok {
		t.Fatal("old refresh reservation failed")
	}
	for index := 0; index < maxSelectorSnapshotCacheEntries; index++ {
		cache.store(fmt.Sprintf("churn-%02d", index), []byte("churn"))
	}
	if _, ok := cache.get("key"); ok {
		t.Fatal("key was not evicted while its refresh was in flight")
	}

	newer, err := cache.loadCold(context.Background(), "key", func() ([]byte, error) {
		return []byte("newer-cold"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(newer) != "newer-cold" {
		t.Fatalf("cold result = %q, want newer-cold", newer)
	}
	if applied := cache.finishLoad(oldRefresh, []byte("older-refresh"), nil); applied {
		t.Fatal("older refresh overwrote a newer cold load")
	}
	if cached, ok := cache.get("key"); !ok || string(cached) != "newer-cold" {
		t.Fatalf("cached result = %q,%v, want newer-cold,true", cached, ok)
	}
}

func TestSelectorSnapshotCacheColdLeaderCancellationDoesNotCancelSurvivingWaiter(t *testing.T) {
	cache := newSelectorSnapshotCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	load := func() ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("fresh"), nil
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := cache.loadCold(leaderCtx, "key", load)
		leaderResult <- err
	}()
	<-started
	waiterResult := make(chan struct {
		body []byte
		err  error
	}, 1)
	waiterCtx := &observedDoneContext{Context: context.Background(), observed: make(chan struct{})}
	go func() {
		body, err := cache.loadCold(waiterCtx, "key", load)
		waiterResult <- struct {
			body []byte
			err  error
		}{body: body, err: err}
	}()
	<-waiterCtx.observed
	cancelLeader()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context canceled", err)
	}
	close(release)
	waiter := <-waiterResult
	if waiter.err != nil || string(waiter.body) != "fresh" {
		t.Fatalf("surviving waiter = %q,%v, want fresh,nil", waiter.body, waiter.err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestSelectorSnapshotCacheBoundsRefreshesAcrossKeys(t *testing.T) {
	cache := newSelectorSnapshotCache()
	loads := make([]selectorSnapshotLoad, 0, maxSelectorSnapshotRefreshes)
	for index := 0; index < maxSelectorSnapshotRefreshes; index++ {
		load, ok := cache.beginRefresh(fmt.Sprintf("key-%02d", index))
		if !ok {
			t.Fatalf("refresh %d was rejected before global capacity", index)
		}
		loads = append(loads, load)
	}
	if _, ok := cache.beginRefresh("saturated"); ok {
		t.Fatal("refresh above daemon-wide capacity was admitted")
	}
	cache.finishLoad(loads[0], []byte("fresh"), nil)
	load, ok := cache.beginRefresh("after-release")
	if !ok {
		t.Fatal("refresh was not admitted after capacity was released")
	}
	cache.finishLoad(load, []byte("fresh"), nil)
	for _, load := range loads[1:] {
		cache.finishLoad(load, []byte("fresh"), nil)
	}
}

func TestSelectorSnapshotCacheConcurrentRefreshAdmissionNeverExceedsGlobalBound(t *testing.T) {
	cache := newSelectorSnapshotCache()
	const contenders = 64
	start := make(chan struct{})
	reservations := make(chan selectorSnapshotLoad, contenders)
	var ready sync.WaitGroup
	var finished sync.WaitGroup
	ready.Add(contenders)
	finished.Add(contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			defer finished.Done()
			ready.Done()
			<-start
			if load, ok := cache.beginRefresh(fmt.Sprintf("key-%02d", index)); ok {
				reservations <- load
			}
		}(index)
	}
	ready.Wait()
	close(start)
	finished.Wait()
	close(reservations)
	admitted := 0
	for load := range reservations {
		admitted++
		cache.finishLoad(load, []byte("fresh"), nil)
	}
	if admitted != maxSelectorSnapshotRefreshes {
		t.Fatalf("concurrent refresh admissions = %d, want %d", admitted, maxSelectorSnapshotRefreshes)
	}
}

func TestSelectorSnapshotCacheBoundsDistinctRequestKeys(t *testing.T) {
	cache := newSelectorSnapshotCache()
	for index := 0; index < maxSelectorSnapshotCacheEntries+5; index++ {
		cache.store(fmt.Sprintf("key-%02d", index), []byte("value"))
	}
	cache.mu.RLock()
	got := len(cache.entries)
	metadata := len(cache.latestStarted)
	inFlight := len(cache.inFlight)
	cache.mu.RUnlock()
	if got != maxSelectorSnapshotCacheEntries {
		t.Fatalf("cache entries = %d, want %d", got, maxSelectorSnapshotCacheEntries)
	}
	if _, ok := cache.get("key-00"); ok {
		t.Fatal("oldest cache entry was not evicted")
	}
	if metadata != maxSelectorSnapshotCacheEntries || inFlight != 0 {
		t.Fatalf("cache metadata entries,in-flight = %d,%d, want %d,0", metadata, inFlight, maxSelectorSnapshotCacheEntries)
	}
}

func TestSelectorSnapshotRequestKeyKeepsInheritedAndExplicitAllProjectsScopesDistinct(t *testing.T) {
	inherited, err := selectorSnapshotRequestKey(protocol.GlobalSnapshotRequestBody{Consumer: protocol.GlobalViewConsumerTmuxSelector})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := selectorSnapshotRequestKey(protocol.GlobalSnapshotRequestBody{
		Consumer: protocol.GlobalViewConsumerTmuxSelector,
		Scope:    protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeAllProjects},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inherited == explicit {
		t.Fatal("inherited persisted scope and explicit all-projects override share a cache key")
	}
}

func waitForSelectorSnapshot(t *testing.T, cache *selectorSnapshotCache, key, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, ok := cache.get(key); ok && string(got) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	got, _ := cache.get(key)
	t.Fatalf("cached result = %q, want %q", got, want)
}
