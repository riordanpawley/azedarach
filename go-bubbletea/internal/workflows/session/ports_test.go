package session

import "testing"

func TestDeterministicPortAllocatorDeterminism(t *testing.T) {
	t.Parallel()

	allocator := NewDeterministicPortAllocator(7000, 200)

	first := allocator.Allocate("feature/session")
	second := allocator.Allocate("feature/session")
	if first != second {
		t.Fatalf("expected stable port allocation, got %d and %d", first, second)
	}
}

func TestDeterministicPortAllocatorBoundsAndSpread(t *testing.T) {
	t.Parallel()

	allocator := NewDeterministicPortAllocator(4200, 50)

	alpha := allocator.Allocate("alpha")
	beta := allocator.Allocate("beta")

	if alpha < 4200 || alpha >= 4250 {
		t.Fatalf("alpha port out of range: %d", alpha)
	}
	if beta < 4200 || beta >= 4250 {
		t.Fatalf("beta port out of range: %d", beta)
	}
	if alpha == beta {
		t.Fatalf("expected different ports for different names, both got %d", alpha)
	}
}

func TestDeterministicPortAllocatorDefaults(t *testing.T) {
	t.Parallel()

	allocator := NewDeterministicPortAllocator(0, 0)
	port := allocator.Allocate("sess-default")
	if port < defaultBasePort || port >= defaultBasePort+defaultPortSpan {
		t.Fatalf("default-allocated port out of range: %d", port)
	}
}
