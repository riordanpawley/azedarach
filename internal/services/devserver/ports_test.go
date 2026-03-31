package devserver

import (
	"fmt"
	"testing"
)

func newTestPortAllocator(basePort int, available func(int) bool) *PortAllocator {
	return &PortAllocator{
		allocated: make(map[int]string),
		basePort:  basePort,
		available: available,
	}
}

func TestPortAllocator_Allocate(t *testing.T) {
	pa := newTestPortAllocator(9000, func(int) bool { return true })

	port1, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port1 != 9000 {
		t.Fatalf("expected port 9000, got %d", port1)
	}

	port2, err := pa.Allocate("issue-2")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port2 != 9001 {
		t.Fatalf("expected port 9001, got %d", port2)
	}
}

func TestPortAllocator_AllocateSameIssue(t *testing.T) {
	pa := newTestPortAllocator(9000, func(int) bool { return true })

	port1, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	port2, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port1 != port2 {
		t.Fatalf("expected same port %d, got %d", port1, port2)
	}
}

func TestPortAllocator_AllocateSkipsOccupied(t *testing.T) {
	pa := newTestPortAllocator(9100, func(port int) bool {
		return port != 9100
	})

	port, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if port != 9101 {
		t.Fatalf("expected to skip occupied port 9100 and allocate 9101, got %d", port)
	}
}

func TestPortAllocator_Release(t *testing.T) {
	pa := newTestPortAllocator(9000, func(int) bool { return true })

	port, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if gotPort, ok := pa.GetPort("issue-1"); !ok || gotPort != port {
		t.Fatalf("expected port %d, got %d (ok=%v)", port, gotPort, ok)
	}

	pa.Release("issue-1")

	if gotPort, ok := pa.GetPort("issue-1"); ok {
		t.Fatalf("expected no port after release, got %d", gotPort)
	}

	newPort, err := pa.Allocate("issue-2")
	if err != nil {
		t.Fatalf("expected no error after release, got %v", err)
	}
	if newPort != port {
		t.Fatalf("expected released port %d to be reused, got %d", port, newPort)
	}
}

func TestPortAllocator_GetPort(t *testing.T) {
	pa := newTestPortAllocator(9000, func(int) bool { return true })

	if port, ok := pa.GetPort("non-existent"); ok {
		t.Fatalf("expected no port for non-existent issue, got %d", port)
	}

	expectedPort, err := pa.Allocate("issue-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	gotPort, ok := pa.GetPort("issue-1")
	if !ok {
		t.Fatalf("expected port to be found")
	}
	if gotPort != expectedPort {
		t.Fatalf("expected port %d, got %d", expectedPort, gotPort)
	}
}

func TestPortAllocator_AllocationLimit(t *testing.T) {
	basePort := 50000
	occupied := make(map[int]struct{}, 100)
	for i := 0; i < 100; i++ {
		occupied[basePort+i] = struct{}{}
	}

	pa := newTestPortAllocator(basePort, func(port int) bool {
		_, blocked := occupied[port]
		return !blocked
	})

	_, err := pa.Allocate("issue-1")
	if err == nil {
		t.Fatal("expected error when all ports occupied, got nil")
	}
}

func TestPortAllocator_ConcurrentAccess(t *testing.T) {
	pa := newTestPortAllocator(9200, func(int) bool { return true })

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			issueID := fmt.Sprintf("issue-%d", id)
			_, err := pa.Allocate(issueID)
			if err != nil {
				t.Errorf("failed to allocate for %s: %v", issueID, err)
			}

			if _, ok := pa.GetPort(issueID); !ok {
				t.Errorf("failed to get port for %s", issueID)
			}

			pa.Release(issueID)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
