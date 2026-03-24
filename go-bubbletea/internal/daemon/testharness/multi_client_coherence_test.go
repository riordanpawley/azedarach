package testharness

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

type mutation struct {
	taskID string
	value  string
}

type publishedEvent struct {
	revision int
	mutation mutation
}

type clientProjection struct {
	mu          sync.Mutex
	tasks       map[string]string
	lastRev     int
	appliedRevs []int
}

func newClientProjection() *clientProjection {
	return &clientProjection{
		tasks: make(map[string]string),
	}
}

func (c *clientProjection) applyEvent(evt publishedEvent) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Ignore stale/duplicate revisions.
	if evt.revision <= c.lastRev {
		return false
	}
	c.tasks[evt.mutation.taskID] = evt.mutation.value
	c.lastRev = evt.revision
	c.appliedRevs = append(c.appliedRevs, evt.revision)
	return true
}

func (c *clientProjection) snapshot() (map[string]string, int, []int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cp := make(map[string]string, len(c.tasks))
	for k, v := range c.tasks {
		cp[k] = v
	}
	revs := append([]int(nil), c.appliedRevs...)
	return cp, c.lastRev, revs
}

type coherenceScenario struct {
	mu       sync.Mutex
	revision int
	state    map[string]string
}

func newCoherenceScenario() *coherenceScenario {
	return &coherenceScenario{
		state: make(map[string]string),
	}
}

func (s *coherenceScenario) mutate(m mutation) publishedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.revision++
	s.state[m.taskID] = m.value
	return publishedEvent{
		revision: s.revision,
		mutation: m,
	}
}

func (s *coherenceScenario) snapshot() (map[string]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := make(map[string]string, len(s.state))
	for k, v := range s.state {
		cp[k] = v
	}
	return cp, s.revision
}

func TestMultiClientMutationCoherenceAC(t *testing.T) {
	base := t.TempDir()
	h := New(Config{
		BaseDir:      base,
		ProjectID:    "proj-afp",
		OTELExporter: "http://127.0.0.1:4318",
	})

	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		if err := h.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	scenario := newCoherenceScenario()
	clientA := newClientProjection()
	clientB := newClientProjection()

	mutations := []mutation{
		{taskID: "T-1", value: "in_progress"},
		{taskID: "T-2", value: "blocked"},
		{taskID: "T-1", value: "done"},
		{taskID: "T-3", value: "todo"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, len(mutations))
	evtCh := make(chan publishedEvent, len(mutations))

	for i, m := range mutations {
		wg.Add(1)
		go func(i int, m mutation) {
			defer wg.Done()
			<-start
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			evt := scenario.mutate(m)
			if err := h.appendEvent("daemon.event.publish", map[string]any{
				"client_id": "client-" + string(rune('A'+(i%2))),
				"task_id":   m.taskID,
				"value":     m.value,
				"revision":  evt.revision,
			}); err != nil {
				errCh <- err
				return
			}
			evtCh <- evt
		}(i, m)
	}

	close(start)
	wg.Wait()
	close(evtCh)
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("mutation scenario error: %v", err)
		}
	}

	var events []publishedEvent
	for evt := range evtCh {
		events = append(events, evt)
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].revision < events[j].revision
	})
	for _, evt := range events {
		if !clientA.applyEvent(evt) {
			t.Fatalf("client A dropped revision %d", evt.revision)
		}
		if !clientB.applyEvent(evt) {
			t.Fatalf("client B dropped revision %d", evt.revision)
		}
	}

	daemonState, daemonRev := scenario.snapshot()
	clientAState, clientARev, clientARevs := clientA.snapshot()
	clientBState, clientBRev, clientBRevs := clientB.snapshot()

	if daemonRev != len(mutations) {
		t.Fatalf("daemon revision = %d, want %d", daemonRev, len(mutations))
	}
	if clientARev != daemonRev || clientBRev != daemonRev {
		t.Fatalf("client revisions (%d, %d) want %d", clientARev, clientBRev, daemonRev)
	}
	if len(clientARevs) != len(mutations) || len(clientBRevs) != len(mutations) {
		t.Fatalf("client event counts (%d, %d) want %d", len(clientARevs), len(clientBRevs), len(mutations))
	}
	for i := 1; i < len(clientARevs); i++ {
		if clientARevs[i] <= clientARevs[i-1] {
			t.Fatalf("client A revisions not monotonic: %v", clientARevs)
		}
		if clientBRevs[i] <= clientBRevs[i-1] {
			t.Fatalf("client B revisions not monotonic: %v", clientBRevs)
		}
	}

	if len(clientAState) != len(daemonState) || len(clientBState) != len(daemonState) {
		t.Fatalf("state size mismatch daemon=%d A=%d B=%d", len(daemonState), len(clientAState), len(clientBState))
	}
	for taskID, want := range daemonState {
		if got := clientAState[taskID]; got != want {
			t.Fatalf("client A state mismatch for %s: got %q want %q", taskID, got, want)
		}
		if got := clientBState[taskID]; got != want {
			t.Fatalf("client B state mismatch for %s: got %q want %q", taskID, got, want)
		}
	}
}
