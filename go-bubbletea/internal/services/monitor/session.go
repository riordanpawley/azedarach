package monitor

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// TmuxClient defines the interface for tmux operations needed by the monitor
type TmuxClient interface {
	// CapturePane captures the content of a tmux pane
	CapturePane(ctx context.Context, sessionName string) (string, error)
}

// SessionMonitor monitors tmux sessions and detects state changes
type SessionMonitor struct {
	tmux     TmuxClient
	mu       sync.RWMutex
	sessions map[string]*monitoredSession
	wg       sync.WaitGroup
}

// monitoredSession represents a session being monitored
type monitoredSession struct {
	beadID string
	cancel context.CancelFunc
	state  domain.SessionState
}

// SessionStateMsg is sent to the Bubble Tea program when state changes
type SessionStateMsg struct {
	BeadID string
	State  domain.SessionState
}

// NewSessionMonitor creates a new session monitor
func NewSessionMonitor(tmux TmuxClient) *SessionMonitor {
	return &SessionMonitor{
		tmux:     tmux,
		sessions: make(map[string]*monitoredSession),
	}
}

// Start begins monitoring a session
// Polls every 500ms and sends SessionStateMsg to the program when state changes
func (m *SessionMonitor) Start(ctx context.Context, beadID string, program *tea.Program) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop existing monitor if any
	if existing, ok := m.sessions[beadID]; ok {
		existing.cancel()
	}

	// Create cancellable context
	monitorCtx, cancel := context.WithCancel(ctx)

	session := &monitoredSession{
		beadID: beadID,
		cancel: cancel,
		state:  domain.SessionIdle,
	}
	m.sessions[beadID] = session

	// Start monitoring goroutine
	m.wg.Add(1)
	go m.monitor(monitorCtx, beadID, program)
}

// Stop stops monitoring a session
func (m *SessionMonitor) Stop(beadID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[beadID]; ok {
		session.cancel()
		delete(m.sessions, beadID)
	}
}

// GetState returns the current state of a monitored session
func (m *SessionMonitor) GetState(beadID string) domain.SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if session, ok := m.sessions[beadID]; ok {
		return session.state
	}
	return domain.SessionIdle
}

// StopAll stops monitoring all sessions
func (m *SessionMonitor) StopAll() {
	m.mu.Lock()
	for beadID, session := range m.sessions {
		session.cancel()
		delete(m.sessions, beadID)
	}
	m.mu.Unlock()

	// Wait for all monitoring goroutines to finish
	m.wg.Wait()
}

// monitor is the main monitoring loop for a session
func (m *SessionMonitor) monitor(ctx context.Context, beadID string, program *tea.Program) {
	defer m.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Capture tmux pane output
			output, err := m.tmux.CapturePane(ctx, beadID)
			if err != nil {
				// On error, continue monitoring (session might not be ready yet)
				continue
			}

			// Detect state from output
			newState := DetectState(output)

			// Check if state changed
			m.mu.Lock()
			session, ok := m.sessions[beadID]
			if !ok {
				m.mu.Unlock()
				return // Session was stopped
			}

			if session.state != newState {
				session.state = newState
				m.mu.Unlock()

				// Send state change message to program
				if program != nil {
					program.Send(SessionStateMsg{
						BeadID: beadID,
						State:  newState,
					})
				}
			} else {
				m.mu.Unlock()
			}
		}
	}
}
