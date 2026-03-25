package eventticker

import (
	"reflect"
	"testing"
)

func TestRing_PushAndLatest(t *testing.T) {
	ring := NewRing(3)

	ring.Push("first")
	ring.Push("second")

	if got := ring.Latest(); got != "second" {
		t.Fatalf("Latest() = %q, want %q", got, "second")
	}

	if got := ring.Snapshot(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, []string{"first", "second"})
	}
}

func TestRing_WrapsAroundInOrder(t *testing.T) {
	ring := NewRing(3)

	ring.Push("first")
	ring.Push("second")
	ring.Push("third")
	ring.Push("fourth")

	if got := ring.Latest(); got != "fourth" {
		t.Fatalf("Latest() = %q, want %q", got, "fourth")
	}

	if got := ring.Snapshot(); !reflect.DeepEqual(got, []string{"second", "third", "fourth"}) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, []string{"second", "third", "fourth"})
	}
}

func TestRing_DefaultsToCapacityOne(t *testing.T) {
	ring := NewRing(0)

	if got := ring.Capacity(); got != 1 {
		t.Fatalf("Capacity() = %d, want %d", got, 1)
	}

	ring.Push("first")
	ring.Push("second")

	if got := ring.Snapshot(); !reflect.DeepEqual(got, []string{"second"}) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, []string{"second"})
	}
}
