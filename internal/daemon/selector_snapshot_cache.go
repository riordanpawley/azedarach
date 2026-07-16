package daemon

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

const maxSelectorSnapshotCacheEntries = 32

// selectorSnapshotCache keeps only successful, immutable response bodies. The
// singleflight group coalesces both cold fills and warm refreshes by effective
// request key; callers always receive a copy they can safely marshal or mutate.
type selectorSnapshotCache struct {
	mu      sync.RWMutex
	entries map[string][]byte
	order   []string
	loads   singleflight.Group
}

func newSelectorSnapshotCache() *selectorSnapshotCache {
	return &selectorSnapshotCache{entries: make(map[string][]byte)}
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
	}
	c.mu.Unlock()
}

func (c *selectorSnapshotCache) loadCold(ctx context.Context, key string, load func(context.Context) ([]byte, error)) ([]byte, error) {
	result := c.loads.DoChan(key, func() (any, error) {
		body, err := load(ctx)
		if err != nil {
			return nil, err
		}
		c.store(key, body)
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

// refresh starts or joins a refresh and reports its eventual error. The result
// channel is buffered by singleflight, so callers may intentionally ignore it.
func (c *selectorSnapshotCache) refresh(ctx context.Context, key string, load func(context.Context) ([]byte, error)) <-chan error {
	done := make(chan error, 1)
	result := c.loads.DoChan(key, func() (any, error) {
		body, err := load(ctx)
		if err != nil {
			return nil, err
		}
		c.store(key, body)
		return nil, nil
	})
	go func() {
		response := <-result
		done <- response.Err
		close(done)
	}()
	return done
}
