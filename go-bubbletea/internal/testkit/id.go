package testkit

import "fmt"

// DeterministicIDGenerator emits stable, incrementing identifiers for tests.
type DeterministicIDGenerator struct {
	prefix string
	next   uint64
}

// NewDeterministicIDGenerator creates a generator with a prefix and start value.
func NewDeterministicIDGenerator(prefix string, start uint64) *DeterministicIDGenerator {
	return &DeterministicIDGenerator{prefix: prefix, next: start}
}

// Next returns the next stable identifier and increments internal state.
func (g *DeterministicIDGenerator) Next() string {
	id := fmt.Sprintf("%s%d", g.prefix, g.next)
	g.next++
	return id
}
