package daemon

import (
	"context"
	"errors"
	"fmt"
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
	load := func(context.Context) ([]byte, error) {
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
	load := func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("fresh"), nil
	}

	cache.refresh(context.Background(), "key", load)
	<-started
	for range 4 {
		cache.refresh(context.Background(), "key", load)
	}
	if cached, ok := cache.get("key"); !ok || string(cached) != "stale" {
		t.Fatalf("warm read = %q,%v, want stale,true", cached, ok)
	}
	close(release)
	waitForSelectorSnapshot(t, cache, "key", "fresh")
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestSelectorSnapshotCacheFailedRefreshPreservesLastGoodValue(t *testing.T) {
	cache := newSelectorSnapshotCache()
	cache.store("key", []byte("last-good"))
	done := cache.refresh(context.Background(), "key", func(context.Context) ([]byte, error) {
		return nil, errors.New("refresh failed")
	})
	if err := <-done; err == nil {
		t.Fatal("refresh error = nil, want failure")
	}
	cached, ok := cache.get("key")
	if !ok || string(cached) != "last-good" {
		t.Fatalf("cached result = %q,%v, want last-good,true", cached, ok)
	}
}

func TestSelectorSnapshotCacheBoundsDistinctRequestKeys(t *testing.T) {
	cache := newSelectorSnapshotCache()
	for index := 0; index < maxSelectorSnapshotCacheEntries+5; index++ {
		cache.store(fmt.Sprintf("key-%02d", index), []byte("value"))
	}
	cache.mu.RLock()
	got := len(cache.entries)
	cache.mu.RUnlock()
	if got != maxSelectorSnapshotCacheEntries {
		t.Fatalf("cache entries = %d, want %d", got, maxSelectorSnapshotCacheEntries)
	}
	if _, ok := cache.get("key-00"); ok {
		t.Fatal("oldest cache entry was not evicted")
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
