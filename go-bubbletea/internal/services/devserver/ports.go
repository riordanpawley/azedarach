package devserver

import (
	"fmt"
	"net"
	"sync"
)

// PortAllocator manages port allocation for dev servers.
// It ensures each bead gets a unique available port.
type PortAllocator struct {
	mu        sync.Mutex
	allocated map[int]string // port -> beadID
	basePort  int
}

// NewPortAllocator creates a new port allocator starting from the given base port.
func NewPortAllocator(basePort int) *PortAllocator {
	return &PortAllocator{
		allocated: make(map[int]string),
		basePort:  basePort,
	}
}

// Allocate finds an available port starting from basePort and assigns it to the beadID.
// Returns an error if the bead already has a port or if no ports are available.
func (p *PortAllocator) Allocate(beadID string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if bead already has a port allocated
	for port, id := range p.allocated {
		if id == beadID {
			return port, nil
		}
	}

	// Try up to 100 ports
	const maxAttempts = 100
	for i := 0; i < maxAttempts; i++ {
		port := p.basePort + i

		// Skip if already allocated
		if _, exists := p.allocated[port]; exists {
			continue
		}

		// Check if port is actually available
		if !isPortAvailable(port) {
			continue
		}

		// Allocate port
		p.allocated[port] = beadID
		return port, nil
	}

	return 0, fmt.Errorf("no available ports found (tried %d ports starting from %d)", maxAttempts, p.basePort)
}

// Release frees the port allocated to the given beadID.
func (p *PortAllocator) Release(beadID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port, id := range p.allocated {
		if id == beadID {
			delete(p.allocated, port)
			return
		}
	}
}

// GetPort returns the port allocated to the given beadID.
// Returns 0 and false if no port is allocated.
func (p *PortAllocator) GetPort(beadID string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port, id := range p.allocated {
		if id == beadID {
			return port, true
		}
	}
	return 0, false
}

// isPortAvailable checks if a port is available by attempting to listen on it.
func isPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
