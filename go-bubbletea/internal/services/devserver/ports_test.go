package devserver

import (
	"fmt"
	"net"
	"testing"
)

func TestPortAllocator_Allocate(t *testing.T) {
	pa := NewPortAllocator(9000)

	// Allocate first port
	port1, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port1 < 9000 {
		t.Errorf("expected port >= 9000, got %d", port1)
	}

	// Allocate second port
	port2, err := pa.Allocate("issue-2")
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

func TestPortAllocator_AllocateSameIssue(t *testing.T) {
	pa := NewPortAllocator(9000)

	// Allocate port for issue
	port1, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Allocate again for same issue - should return same port
	port2, err := pa.Allocate("issue-1")
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
	port, err := pa.Allocate("issue-1")
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
	port, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify allocation
	if gotPort, ok := pa.GetPort("issue-1"); !ok || gotPort != port {
		t.Errorf("expected port %d, got %d (ok=%v)", port, gotPort, ok)
	}

	// Release port
	pa.Release("issue-1")

	// Verify release
	if gotPort, ok := pa.GetPort("issue-1"); ok {
		t.Errorf("expected no port after release, got %d", gotPort)
	}

	// Should be able to allocate again
	newPort, err := pa.Allocate("issue-2")
	if err != nil {
		t.Fatalf("expected no error after release, got %v", err)
	}
	if newPort != port {
		t.Logf("note: allocated different port after release (expected %d, got %d)", port, newPort)
	}
}

func TestPortAllocator_GetPort(t *testing.T) {
	pa := NewPortAllocator(9000)

	// Non-existent issue
	if port, ok := pa.GetPort("non-existent"); ok {
		t.Errorf("expected no port for non-existent issue, got %d", port)
	}

	// Allocate and verify
	expectedPort, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	gotPort, ok := pa.GetPort("issue-1")
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
	_, err := pa.Allocate("issue-1")
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
			issueID := fmt.Sprintf("issue-%d", id)
			_, err := pa.Allocate(issueID)
			if err != nil {
				t.Errorf("failed to allocate for %s: %v", issueID, err)
			}

			// Get port
			if _, ok := pa.GetPort(issueID); !ok {
				t.Errorf("failed to get port for %s", issueID)
			}

			// Release
			pa.Release(issueID)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
