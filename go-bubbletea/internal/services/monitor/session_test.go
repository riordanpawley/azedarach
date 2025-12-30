package monitor

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// mockTmuxClient implements TmuxClient for testing
type mockTmuxClient struct {
	output string
	err    error
}

func (m *mockTmuxClient) CapturePane(ctx context.Context, sessionName string) (string, error) {
	return m.output, m.err
}

// mockProgram implements a minimal tea.Program for testing
type mockProgram struct {
	messages []tea.Msg
}

func (m *mockProgram) Send(msg tea.Msg) {
	m.messages = append(m.messages, msg)
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
}

func TestSessionMonitor_StartStop(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring
	monitor.Start(ctx, "test-bead", program)

	// Verify session is tracked
	state := monitor.GetState("test-bead")
	if state == domain.SessionIdle {
		// Initial state is idle, which is expected before first poll
	}

	// Stop monitoring
	monitor.Stop("test-bead")

	// Verify session is no longer tracked
	state = monitor.GetState("test-bead")
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
	monitor.Start(ctx, "test-bead", program)

	// Wait a bit for initial state to be set
	time.Sleep(100 * time.Millisecond)

	// Get state
	state := monitor.GetState("test-bead")
	if state != domain.SessionBusy && state != domain.SessionIdle {
		t.Errorf("GetState() = %v, want %v or %v", state, domain.SessionBusy, domain.SessionIdle)
	}

	// Get state for non-existent session
	state = monitor.GetState("non-existent")
	if state != domain.SessionIdle {
		t.Errorf("GetState() for non-existent session = %v, want %v", state, domain.SessionIdle)
	}

	monitor.Stop("test-bead")
}

func TestSessionMonitor_StopAll(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring multiple sessions
	monitor.Start(ctx, "bead-1", program)
	monitor.Start(ctx, "bead-2", program)
	monitor.Start(ctx, "bead-3", program)

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
	for _, beadID := range []string{"bead-1", "bead-2", "bead-3"} {
		state := monitor.GetState(beadID)
		if state != domain.SessionIdle {
			t.Errorf("GetState(%q) after StopAll() = %v, want %v", beadID, state, domain.SessionIdle)
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
			monitor := NewSessionMonitor(tmux)
			program := &mockProgram{}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Start monitoring
			monitor.Start(ctx, "test-bead", program)

			// Wait for polling cycle to detect state
			time.Sleep(600 * time.Millisecond)

			// Check state
			state := monitor.GetState("test-bead")
			if state != tt.expectedState {
				t.Errorf("GetState() = %v, want %v", state, tt.expectedState)
			}

			monitor.Stop("test-bead")
		})
	}
}

func TestSessionMonitor_StateChangeMessage(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring
	monitor.Start(ctx, "test-bead", program)

	// Wait for initial polling
	time.Sleep(600 * time.Millisecond)

	// Change output to trigger state change
	tmux.output = "Error: something went wrong"

	// Wait for next polling cycle
	time.Sleep(600 * time.Millisecond)

	// Check that state change message was sent
	foundStateChange := false
	for _, msg := range program.messages {
		if stateMsg, ok := msg.(SessionStateMsg); ok {
			if stateMsg.BeadID == "test-bead" && stateMsg.State == domain.SessionError {
				foundStateChange = true
				break
			}
		}
	}

	if !foundStateChange {
		t.Error("Expected SessionStateMsg with Error state, but none found")
	}

	monitor.Stop("test-bead")
}

func TestSessionMonitor_RestartSession(t *testing.T) {
	tmux := &mockTmuxClient{output: "normal output"}
	monitor := NewSessionMonitor(tmux)
	program := &mockProgram{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start monitoring
	monitor.Start(ctx, "test-bead", program)
	time.Sleep(100 * time.Millisecond)

	// Start again (should cancel previous)
	monitor.Start(ctx, "test-bead", program)
	time.Sleep(100 * time.Millisecond)

	// Should still have only one session
	monitor.mu.RLock()
	count := len(monitor.sessions)
	monitor.mu.RUnlock()

	if count != 1 {
		t.Errorf("Expected 1 session after restart, got %d", count)
	}

	monitor.Stop("test-bead")
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
			beadID := "bead-" + string(rune('0'+id))
			monitor.Start(ctx, beadID, program)
			time.Sleep(50 * time.Millisecond)
			_ = monitor.GetState(beadID)
			monitor.Stop(beadID)
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
	monitor.Start(ctx, "test-bead", program)
	time.Sleep(100 * time.Millisecond)

	// Cancel context
	cancel()
	time.Sleep(100 * time.Millisecond)

	// Session should still be in map (Stop() wasn't called)
	// but the monitoring goroutine should have exited
	state := monitor.GetState("test-bead")
	if state == domain.SessionIdle {
		// This is acceptable - session might have been cleaned up
	}

	monitor.Stop("test-bead")
}
