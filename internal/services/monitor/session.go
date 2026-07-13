package monitor

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const defaultSessionMonitorPollInterval = 500 * time.Millisecond

type sessionMonitorTicker struct {
	ticks <-chan time.Time
	stop  func()
}

type sessionMonitorTickerFactory func(time.Duration) sessionMonitorTicker

// SessionMonitorOption configures optional monitor behavior.
type SessionMonitorOption func(*SessionMonitor)

// WithSessionMonitorPollInterval overrides the pane polling interval.
func WithSessionMonitorPollInterval(interval time.Duration) SessionMonitorOption {
	return func(m *SessionMonitor) {
		if interval > 0 {
			m.pollInterval = interval
		}
	}
}

func withSessionMonitorTickerFactory(factory sessionMonitorTickerFactory) SessionMonitorOption {
	return func(m *SessionMonitor) {
		if factory != nil {
			m.newTicker = factory
		}
	}
}

func newRealSessionMonitorTicker(interval time.Duration) sessionMonitorTicker {
	ticker := time.NewTicker(interval)
	return sessionMonitorTicker{ticks: ticker.C, stop: ticker.Stop}
}

// TmuxClient defines the interface for tmux operations needed by the monitor
type TmuxClient interface {
	// CapturePane captures the content of a tmux pane
	CapturePane(ctx context.Context, sessionName string) (string, error)
}

// ProgramSender defines the interface for sending messages to a Bubble Tea program
type ProgramSender interface {
	Send(msg tea.Msg)
}

// SessionMonitor monitors tmux sessions and detects state changes
type SessionMonitor struct {
	tmux         TmuxClient
	pollInterval time.Duration
	newTicker    sessionMonitorTickerFactory
	mu           sync.RWMutex
	sessions     map[string]*monitoredSession
	wg           sync.WaitGroup
}

// monitoredSession represents a session being monitored
type monitoredSession struct {
	issueID string
	cancel  context.CancelFunc
	state   domain.SessionState
}

// SessionStateMsg is sent to the Bubble Tea program when state changes
type SessionStateMsg struct {
	IssueID string
	State   domain.SessionState
}

// NewSessionMonitor creates a new session monitor
func NewSessionMonitor(tmux TmuxClient, opts ...SessionMonitorOption) *SessionMonitor {
	monitor := &SessionMonitor{
		tmux:         tmux,
		pollInterval: defaultSessionMonitorPollInterval,
		newTicker:    newRealSessionMonitorTicker,
		sessions:     make(map[string]*monitoredSession),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(monitor)
		}
	}
	return monitor
}

// Start begins monitoring a session
// Polls every 500ms and sends SessionStateMsg to the program when state changes
func (m *SessionMonitor) Start(ctx context.Context, issueID string, program ProgramSender) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop existing monitor if any
	if existing, ok := m.sessions[issueID]; ok {
		existing.cancel()
	}

	// Create cancellable context
	monitorCtx, cancel := context.WithCancel(ctx)

	session := &monitoredSession{
		issueID: issueID,
		cancel:  cancel,
		state:   domain.SessionIdle,
	}
	m.sessions[issueID] = session

	// Start monitoring goroutine
	m.wg.Add(1)
	go m.monitor(monitorCtx, session, program)
}

// Stop stops monitoring a session
func (m *SessionMonitor) Stop(issueID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[issueID]; ok {
		session.cancel()
		delete(m.sessions, issueID)
	}
}

// GetState returns the current state of a monitored session
func (m *SessionMonitor) GetState(issueID string) domain.SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if session, ok := m.sessions[issueID]; ok {
		return session.state
	}
	return domain.SessionIdle
}

// StopAll stops monitoring all sessions
func (m *SessionMonitor) StopAll() {
	m.mu.Lock()
	for issueID, session := range m.sessions {
		session.cancel()
		delete(m.sessions, issueID)
	}
	m.mu.Unlock()

	// Wait for all monitoring goroutines to finish
	m.wg.Wait()
}

// monitor is the main monitoring loop for a session
func (m *SessionMonitor) monitor(ctx context.Context, session *monitoredSession, program ProgramSender) {
	defer m.wg.Done()
	issueID := session.issueID

	ticker := m.newTicker(m.pollInterval)
	defer ticker.stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.ticks:
			// Capture tmux pane output
			output, err := m.tmux.CapturePane(ctx, issueID)
			if err != nil {
				// On error, continue monitoring (session might not be ready yet)
				continue
			}

			// Detect state from output
			newState := DetectState(output)

			// Check if state changed
			m.mu.Lock()
			current, ok := m.sessions[issueID]
			if !ok || current != session {
				m.mu.Unlock()
				return // Session was stopped or replaced.
			}

			if current.state != newState {
				current.state = newState
				m.mu.Unlock()

				// Send state change message to program
				if program != nil {
					program.Send(SessionStateMsg{
						IssueID: issueID,
						State:   newState,
					})
				}
			} else {
				m.mu.Unlock()
			}
		}
	}
}
