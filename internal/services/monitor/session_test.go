package monitor

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// mockTmuxClient implements TmuxClient for testing
type mockTmuxClient struct {
	mu             sync.RWMutex
	output         string
	err            error
	captureStarted chan struct{}
	releaseCapture chan struct{}
}

func (m *mockTmuxClient) CapturePane(ctx context.Context, sessionName string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.captureStarted != nil {
		m.captureStarted <- struct{}{}
	}
	if m.releaseCapture != nil {
		<-m.releaseCapture
	}
	return m.output, m.err
}

func (m *mockTmuxClient) setOutput(output string) {
	m.mu.Lock()
	m.output = output
	m.mu.Unlock()
}

// mockProgram implements a minimal tea.Program for testing
type mockProgram struct {
	mu       sync.Mutex
	messages []tea.Msg
	sent     chan tea.Msg
}

func (m *mockProgram) Send(msg tea.Msg) {
	m.mu.Lock()
	m.messages = append(m.messages, msg)
	m.mu.Unlock()
	if m.sent != nil {
		m.sent <- msg
	}
}

type manualMonitorTicker struct {
	ticks chan time.Time
}

func newManualSessionMonitor(t *testing.T, tmux *mockTmuxClient) (*SessionMonitor, *manualMonitorTicker) {
	t.Helper()
	ticker := &manualMonitorTicker{ticks: make(chan time.Time, 1)}
	monitor := NewSessionMonitor(
		tmux,
		withSessionMonitorTickerFactory(func(time.Duration) sessionMonitorTicker {
			return sessionMonitorTicker{ticks: ticker.ticks, stop: func() {}}
		}),
	)
	return monitor, ticker
}

func (t *manualMonitorTicker) tick() {
	t.ticks <- time.Now()
}

func waitForMonitorMessage(t *testing.T, program *mockProgram) SessionStateMsg {
	t.Helper()
	select {
	case msg := <-program.sent:
		stateMsg, ok := msg.(SessionStateMsg)
		if !ok {
			t.Fatalf("monitor message type = %T, want SessionStateMsg", msg)
		}
		return stateMsg
	case <-time.After(time.Second):
		t.Fatal("monitor did not emit state change")
		return SessionStateMsg{}
	}
}

func TestNewSessionMonitor(t *testing.T) {
	tmux := &mockTmuxClient{}
	monitor := NewSessionMonitor(tmux)

	if monitor == nil {
		t.Fatal("NewSessionMonitor() returned nil")
	}
	if monitor.tmux != tmux {
		t.Error("NewSessionMonitor() did not set tmux client")
	}
	if monitor.sessions == nil {
		t.Error("NewSessionMonitor() did not initialize sessions map")
	}
	if monitor.pollInterval != 500*time.Millisecond {
		t.Errorf("poll interval = %v, want production default 500ms", monitor.pollInterval)
	}
	configured := NewSessionMonitor(tmux, WithSessionMonitorPollInterval(2*time.Millisecond))
	if configured.pollInterval != 2*time.Millisecond {
		t.Errorf("configured poll interval = %v, want 2ms", configured.pollInterval)
	}
}

func TestSessionMonitor_StartStop(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring
	monitor.Start(ctx, "test-issue", program)

	// Verify session is tracked
	state := monitor.GetState("test-issue")
	if state == domain.SessionIdle {
		// Initial state is idle, which is expected before first poll
	}

	// Stop monitoring
	monitor.Stop("test-issue")

	// Verify session is no longer tracked
	state = monitor.GetState("test-issue")
	if state != domain.SessionIdle {
		t.Errorf("GetState() after Stop() = %v, want %v", state, domain.SessionIdle)
	}
}

func TestSessionMonitor_GetState(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring
	monitor.Start(ctx, "test-issue", program)

	// Get state
	state := monitor.GetState("test-issue")
	if state != domain.SessionBusy && state != domain.SessionIdle {
		t.Errorf("GetState() = %v, want %v or %v", state, domain.SessionBusy, domain.SessionIdle)
	}

	// Get state for non-existent session
	state = monitor.GetState("non-existent")
	if state != domain.SessionIdle {
		t.Errorf("GetState() for non-existent session = %v, want %v", state, domain.SessionIdle)
	}

	monitor.Stop("test-issue")
}

func TestSessionMonitor_StopAll(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring multiple sessions
	monitor.Start(ctx, "issue-1", program)
	monitor.Start(ctx, "issue-2", program)
	monitor.Start(ctx, "issue-3", program)

	// Verify all sessions are tracked
	if len(monitor.sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(monitor.sessions))
	}

	// Stop all
	monitor.StopAll()

	// Verify all sessions are removed
	if len(monitor.sessions) != 0 {
		t.Errorf("Expected 0 sessions after StopAll(), got %d", len(monitor.sessions))
	}

	// Verify states return idle
	for _, issueID := range []string{"issue-1", "issue-2", "issue-3"} {
		state := monitor.GetState(issueID)
		if state != domain.SessionIdle {
			t.Errorf("GetState(%q) after StopAll() = %v, want %v", issueID, state, domain.SessionIdle)
		}
	}
}

func TestSessionMonitor_StateDetection(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		expectedState domain.SessionState
	}{
		{"waiting state", "Do you want to continue? [y/n]", domain.SessionWaiting},
		{"done state", "Task completed successfully", domain.SessionDone},
		{"error state", "Error: something went wrong", domain.SessionError},
		{"busy state", "Processing files...", domain.SessionBusy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux := &mockTmuxClient{output: tt.output}
			monitor, ticker := newManualSessionMonitor(t, tmux)
			program := &mockProgram{sent: make(chan tea.Msg, 1)}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Start monitoring
			monitor.Start(ctx, "test-issue", program)

			ticker.tick()
			waitForMonitorMessage(t, program)

			// Check state
			state := monitor.GetState("test-issue")
			if state != tt.expectedState {
				t.Errorf("GetState() = %v, want %v", state, tt.expectedState)
			}

			monitor.Stop("test-issue")
		})
	}
}

func TestSessionMonitor_StateChangeMessage(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor, ticker := newManualSessionMonitor(t, tmux)
	program := &mockProgram{sent: make(chan tea.Msg, 2)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring
	monitor.Start(ctx, "test-issue", program)

	ticker.tick()
	waitForMonitorMessage(t, program)

	// Change output to trigger state change
	tmux.setOutput("Error: something went wrong")

	ticker.tick()
	waitForMonitorMessage(t, program)

	// Check that state change message was sent
	foundStateChange := false
	for _, msg := range program.messages {
		if stateMsg, ok := msg.(SessionStateMsg); ok {
			if stateMsg.IssueID == "test-issue" && stateMsg.State == domain.SessionError {
				foundStateChange = true
				break
			}
		}
	}

	if !foundStateChange {
		t.Error("Expected SessionStateMsg with Error state, but none found")
	}

	monitor.Stop("test-issue")
}

func TestSessionMonitor_RestartSession(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring
	monitor.Start(ctx, "test-issue", program)

	// Start again (should cancel previous)
	monitor.Start(ctx, "test-issue", program)

	// Should still have only one session
	monitor.mu.RLock()
	count := len(monitor.sessions)
	monitor.mu.RUnlock()

	if count != 1 {
		t.Errorf("Expected 1 session after restart, got %d", count)
	}

	monitor.Stop("test-issue")
}

func TestSessionMonitor_RestartIgnoresOldInFlightCapture(t *testing.T) {
	tmux := &mockTmuxClient{
		output:         "Error: stale old-session output",
		captureStarted: make(chan struct{}, 1),
		releaseCapture: make(chan struct{}),
	}
	createdTickers := make(chan chan time.Time, 2)
	monitor := NewSessionMonitor(tmux, withSessionMonitorTickerFactory(func(time.Duration) sessionMonitorTicker {
		ticks := make(chan time.Time, 1)
		createdTickers <- ticks
		return sessionMonitorTicker{ticks: ticks, stop: func() {}}
	}))
	program := &mockProgram{sent: make(chan tea.Msg, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	monitor.Start(ctx, "test-issue", program)
	oldTicks := <-createdTickers
	oldTicks <- time.Now()
	<-tmux.captureStarted

	monitor.Start(ctx, "test-issue", program)
	newTicks := <-createdTickers
	close(tmux.releaseCapture)

	select {
	case msg := <-program.sent:
		t.Fatalf("old monitor emitted stale message after replacement: %#v", msg)
	case <-time.After(20 * time.Millisecond):
	}
	if state := monitor.GetState("test-issue"); state != domain.SessionIdle {
		t.Fatalf("replacement state = %v, want idle before its first poll", state)
	}

	newTicks <- time.Now()
	msg := waitForMonitorMessage(t, program)
	if msg.State != domain.SessionError {
		t.Fatalf("new monitor state = %v, want error", msg.State)
	}
	monitor.Stop("test-issue")
}

func TestSessionMonitor_ConcurrentAccess(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start multiple sessions concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			issueID := "issue-" + string(rune('0'+id))
			monitor.Start(ctx, issueID, program)
			_ = monitor.GetState(issueID)
			monitor.Stop(issueID)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// All sessions should be stopped
	monitor.mu.RLock()
	count := len(monitor.sessions)
	monitor.mu.RUnlock()

	if count != 0 {
		t.Errorf("Expected 0 sessions after concurrent access, got %d", count)
	}
}

func TestSessionMonitor_ContextCancellation(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())

	// Start monitoring
	monitor.Start(ctx, "test-issue", program)

	// Cancel context
	cancel()
	monitor.wg.Wait()

	// Session should still be in map (Stop() wasn't called)
	// but the monitoring goroutine should have exited
	state := monitor.GetState("test-issue")
	if state == domain.SessionIdle {
		// This is acceptable - session might have been cleaned up
	}

	monitor.Stop("test-issue")
}
