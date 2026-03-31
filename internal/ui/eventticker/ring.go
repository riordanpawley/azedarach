package eventticker

// Ring stores the most recent events in insertion order with fixed capacity.
type Ring struct {
	capacity int
	values   []string
	head     int
	size     int
}

// NewRing returns a ring buffer with the requested capacity.
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{
		capacity: capacity,
		values:   make([]string, capacity),
	}
}

// Push appends an event, evicting the oldest entry when the ring is full.
func (r *Ring) Push(value string) {
	if r == nil {
		return
	}
	if r.capacity < 1 {
		r.capacity = 1
	}
	if len(r.values) != r.capacity {
		r.values = make([]string, r.capacity)
		r.head = 0
		r.size = 0
	}

	if r.size < r.capacity {
		index := (r.head + r.size) % r.capacity
		r.values[index] = value
		r.size++
		return
	}

	r.values[r.head] = value
	r.head = (r.head + 1) % r.capacity
}

// Latest returns the most recent event in the ring.
func (r *Ring) Latest() string {
	if r == nil || r.size == 0 {
		return ""
	}
	index := (r.head + r.size - 1) % r.capacity
	return r.values[index]
}

// Snapshot returns the events in oldest-to-newest order.
func (r *Ring) Snapshot() []string {
	if r == nil || r.size == 0 {
		return nil
	}

	out := make([]string, r.size)
	for i := 0; i < r.size; i++ {
		out[i] = r.values[(r.head+i)%r.capacity]
	}
	return out
}

// Len returns the number of stored events.
func (r *Ring) Len() int {
	if r == nil {
		return 0
	}
	return r.size
}

// Capacity returns the fixed capacity of the ring.
func (r *Ring) Capacity() int {
	if r == nil {
		return 0
	}
	return r.capacity
}
