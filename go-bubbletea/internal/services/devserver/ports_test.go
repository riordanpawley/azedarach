package devserver

import (
	"fmt"
	"net"
	"testing"
)

func TestPortAllocator_Allocate(t *testing.T) {
	pa := NewPortAllocator(9000)

	// Allocate first port
	port1, err := pa.Allocate("bead-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port1 < 9000 {
		t.Errorf("expected port >= 9000, got %d", port1)
	}

	// Allocate second port
	port2, err := pa.Allocate("bead-2")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port2 < 9000 {
		t.Errorf("expected port >= 9000, got %d", port2)
	}
	if port2 == port1 {
		t.Errorf("expected different ports, got %d for both", port1)
	}
}

func TestPortAllocator_AllocateSameBead(t *testing.T) {
	pa := NewPortAllocator(9000)

	// Allocate port for bead
	port1, err := pa.Allocate("bead-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Allocate again for same bead - should return same port
	port2, err := pa.Allocate("bead-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port1 != port2 {
		t.Errorf("expected same port %d, got %d", port1, port2)
	}
}

func TestPortAllocator_AllocateSkipsOccupied(t *testing.T) {
	basePort := 9100

	// Occupy a port in the range
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", basePort))
	if err != nil {
		t.Fatalf("failed to occupy port %d: %v", basePort, err)
	}
	defer ln.Close()

	pa := NewPortAllocator(basePort)

	// Should skip the occupied port and allocate the next one
	port, err := pa.Allocate("bead-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port == basePort {
		t.Errorf("expected to skip occupied port %d, got %d", basePort, port)
	}
	if port < basePort || port > basePort+100 {
		t.Errorf("expected port in range [%d, %d], got %d", basePort, basePort+100, port)
	}
}

func TestPortAllocator_Release(t *testing.T) {
	pa := NewPortAllocator(9000)

	// Allocate port
	port, err := pa.Allocate("bead-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify allocation
	if gotPort, ok := pa.GetPort("bead-1"); !ok || gotPort != port {
		t.Errorf("expected port %d, got %d (ok=%v)", port, gotPort, ok)
	}

	// Release port
	pa.Release("bead-1")

	// Verify release
	if gotPort, ok := pa.GetPort("bead-1"); ok {
		t.Errorf("expected no port after release, got %d", gotPort)
	}

	// Should be able to allocate again
	newPort, err := pa.Allocate("bead-2")
	if err != nil {
		t.Fatalf("expected no error after release, got %v", err)
	}
	if newPort != port {
		t.Logf("note: allocated different port after release (expected %d, got %d)", port, newPort)
	}
}

func TestPortAllocator_GetPort(t *testing.T) {
	pa := NewPortAllocator(9000)

	// Non-existent bead
	if port, ok := pa.GetPort("non-existent"); ok {
		t.Errorf("expected no port for non-existent bead, got %d", port)
	}

	// Allocate and verify
	expectedPort, err := pa.Allocate("bead-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	gotPort, ok := pa.GetPort("bead-1")
	if !ok {
		t.Errorf("expected port to be found")
	}
	if gotPort != expectedPort {
		t.Errorf("expected port %d, got %d", expectedPort, gotPort)
	}
}

func TestPortAllocator_AllocationLimit(t *testing.T) {
	// Use a high base port to avoid conflicts
	basePort := 50000

	// Occupy many ports to force allocation failure
	listeners := make([]net.Listener, 0, 100)
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()

	// Occupy first 100 ports
	for i := 0; i < 100; i++ {
		port := basePort + i
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			// Skip if port unavailable
			continue
		}
		listeners = append(listeners, ln)
	}

	pa := NewPortAllocator(basePort)

	// Should fail to allocate since all ports are occupied
	_, err := pa.Allocate("bead-1")
	if err == nil {
		t.Error("expected error when all ports occupied, got nil")
	}
}

func TestPortAllocator_ConcurrentAccess(t *testing.T) {
	pa := NewPortAllocator(9200)

	// Allocate ports concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			beadID := fmt.Sprintf("bead-%d", id)
			_, err := pa.Allocate(beadID)
			if err != nil {
				t.Errorf("failed to allocate for %s: %v", beadID, err)
			}

			// Get port
			if _, ok := pa.GetPort(beadID); !ok {
				t.Errorf("failed to get port for %s", beadID)
			}

			// Release
			pa.Release(beadID)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
