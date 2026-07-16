package daemon

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

const (
	maxSelectorSnapshotCacheEntries = 32
	maxSelectorSnapshotRefreshes    = 4
)

type selectorSnapshotLoad struct {
	key        string
	generation uint64
	refresh    bool
}

// selectorSnapshotCache keeps only successful, immutable response bodies. The
// singleflight group coalesces both cold fills and warm refreshes by effective
// request key; callers always receive a copy they can safely marshal or mutate.
type selectorSnapshotCache struct {
	mu              sync.RWMutex
	entries         map[string][]byte
	order           []string
	latestStarted   map[string]uint64
	inFlight        map[string]int
	refreshing      map[string]struct{}
	nextGeneration  uint64
	activeRefreshes int
	loads           singleflight.Group
}

func newSelectorSnapshotCache() *selectorSnapshotCache {
	return &selectorSnapshotCache{
		entries:       make(map[string][]byte),
		latestStarted: make(map[string]uint64),
		inFlight:      make(map[string]int),
		refreshing:    make(map[string]struct{}),
	}
}

func (c *selectorSnapshotCache) get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	body, ok := c.entries[key]
	cloned := append([]byte(nil), body...)
	c.mu.RUnlock()
	return cloned, ok
}

func (c *selectorSnapshotCache) store(key string, body []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	load := c.beginLoadLocked(key, false)
	c.finishLoadLocked(load, body, nil)
	c.mu.Unlock()
}

func (c *selectorSnapshotCache) storeLocked(key string, body []byte) {
	if c.entries == nil {
		c.entries = make(map[string][]byte)
	}
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = append([]byte(nil), body...)
	for len(c.order) > maxSelectorSnapshotCacheEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
		c.cleanupKeyLocked(oldest)
	}
}

func (c *selectorSnapshotCache) loadCold(ctx context.Context, key string, load func() ([]byte, error)) ([]byte, error) {
	result := c.loads.DoChan(key, func() (any, error) {
		c.mu.Lock()
		reservation := c.beginLoadLocked(key, false)
		c.mu.Unlock()
		body, err := load()
		c.finishLoad(reservation, body, err)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), body...), nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-result:
		if response.Err != nil {
			return nil, response.Err
		}
		body, _ := response.Val.([]byte)
		return append([]byte(nil), body...), nil
	}
}

// beginRefresh reserves the only refresh slot for key. Unlike singleflight,
// this avoids allocating a waiter channel and goroutine for every warm request.
func (c *selectorSnapshotCache) beginRefresh(key string) (selectorSnapshotLoad, bool) {
	if c == nil {
		return selectorSnapshotLoad{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing == nil {
		c.refreshing = make(map[string]struct{})
	}
	if _, exists := c.refreshing[key]; exists {
		return selectorSnapshotLoad{}, false
	}
	if c.activeRefreshes >= maxSelectorSnapshotRefreshes {
		return selectorSnapshotLoad{}, false
	}
	load := c.beginLoadLocked(key, true)
	c.refreshing[key] = struct{}{}
	c.activeRefreshes++
	return load, true
}

// finishLoad applies only the newest load started for a key. Older work may
// finish successfully after eviction and a newer cold fill, but can never
// overwrite that newer snapshot.
func (c *selectorSnapshotCache) finishLoad(load selectorSnapshotLoad, body []byte, err error) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	applied := c.finishLoadLocked(load, body, err)
	c.mu.Unlock()
	return applied
}

func (c *selectorSnapshotCache) beginLoadLocked(key string, refresh bool) selectorSnapshotLoad {
	if c.latestStarted == nil {
		c.latestStarted = make(map[string]uint64)
	}
	if c.inFlight == nil {
		c.inFlight = make(map[string]int)
	}
	c.nextGeneration++
	load := selectorSnapshotLoad{key: key, generation: c.nextGeneration, refresh: refresh}
	c.latestStarted[key] = load.generation
	c.inFlight[key]++
	return load
}

func (c *selectorSnapshotCache) finishLoadLocked(load selectorSnapshotLoad, body []byte, err error) bool {
	applied := err == nil && c.latestStarted[load.key] == load.generation
	if applied {
		c.storeLocked(load.key, body)
	}
	if c.inFlight[load.key] > 1 {
		c.inFlight[load.key]--
	} else {
		delete(c.inFlight, load.key)
	}
	if load.refresh {
		delete(c.refreshing, load.key)
		if c.activeRefreshes > 0 {
			c.activeRefreshes--
		}
	}
	c.cleanupKeyLocked(load.key)
	return applied
}

func (c *selectorSnapshotCache) cleanupKeyLocked(key string) {
	if _, cached := c.entries[key]; cached || c.inFlight[key] > 0 {
		return
	}
	delete(c.latestStarted, key)
}
