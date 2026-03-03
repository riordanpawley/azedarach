package testkit

import (
	"sync"
	"time"
)

// DeterministicClock provides explicit, test-controlled time progression.
type DeterministicClock struct {
	mu      sync.Mutex
	current time.Time
}

// NewDeterministicClock creates a clock pinned to a fixed starting time.
func NewDeterministicClock(start time.Time) *DeterministicClock {
	return &DeterministicClock{current: start}
}

// Now returns the current deterministic timestamp.
func (c *DeterministicClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.current
}

// Set rewrites the deterministic timestamp.
func (c *DeterministicClock) Set(next time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.current = next
}

// Advance moves the deterministic timestamp forward (or backward) by delta.
func (c *DeterministicClock) Advance(delta time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.current = c.current.Add(delta)
	return c.current
}
