package session

import "hash/fnv"

const (
	defaultBasePort = 4000
	defaultPortSpan = 1000
)

type DeterministicPortAllocator struct {
	basePort int
	span     int
}

func NewDeterministicPortAllocator(basePort, span int) DeterministicPortAllocator {
	if basePort <= 0 {
		basePort = defaultBasePort
	}
	if span <= 0 {
		span = defaultPortSpan
	}

	return DeterministicPortAllocator{basePort: basePort, span: span}
}

func (a DeterministicPortAllocator) Allocate(sessionName string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionName))

	offset := int(h.Sum32() % uint32(a.span))
	return a.basePort + offset
}
