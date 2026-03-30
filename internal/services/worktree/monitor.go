package worktree

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

// StateChangeCallback is called when a session's state changes
type StateChangeCallback func(issueID string, state domain.SessionState)

// SessionMonitor monitors tmux sessions for Claude state changes
type SessionMonitor struct {
	tmux      *tmux.Client
	mu        sync.RWMutex
	monitors  map[string]*monitorState
	wg        sync.WaitGroup
	logger    *slog.Logger
	pollLines int // Number of lines to capture from tmux pane
}

// monitorState tracks the monitoring state for a single session
type monitorState struct {
	issueID  string
	cancel   context.CancelFunc
	state    domain.SessionState
	callback StateChangeCallback
}

// NewSessionMonitor creates a new session monitor
func NewSessionMonitor(tmuxClient *tmux.Client, logger *slog.Logger) *SessionMonitor {
	if logger == nil {
		logger = slog.Default()
	}

	return &SessionMonitor{
		tmux:      tmuxClient,
		monitors:  make(map[string]*monitorState),
		logger:    logger,
		pollLines: 100, // Capture last 100 lines by default
	}
}

// Start starts monitoring a session for state changes
// The callback will be invoked whenever the session state changes
func (m *SessionMonitor) Start(ctx context.Context, issueID string, callback StateChangeCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("starting session monitor", "issueID", issueID)

	// Stop existing monitor if any
	if existing, ok := m.monitors[issueID]; ok {
		m.logger.Debug("stopping existing monitor", "issueID", issueID)
		existing.cancel()
	}

	// Create cancellable context
	monitorCtx, cancel := context.WithCancel(ctx)

	state := &monitorState{
		issueID:  issueID,
		cancel:   cancel,
		state:    domain.SessionIdle,
		callback: callback,
	}
	m.monitors[issueID] = state

	// Start monitoring goroutine
	m.wg.Add(1)
	go m.monitorLoop(monitorCtx, state)
}

// Stop stops monitoring a session
func (m *SessionMonitor) Stop(issueID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("stopping session monitor", "issueID", issueID)

	if state, ok := m.monitors[issueID]; ok {
		state.cancel()
		delete(m.monitors, issueID)
	}
}

// GetState returns the current state of a monitored session
func (m *SessionMonitor) GetState(issueID string) domain.SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if state, ok := m.monitors[issueID]; ok {
		return state.state
	}
	return domain.SessionIdle
}

// StopAll stops monitoring all sessions and waits for goroutines to finish
func (m *SessionMonitor) StopAll() {
	m.logger.Info("stopping all session monitors")

	m.mu.Lock()
	for issueID, state := range m.monitors {
		m.logger.Debug("stopping monitor", "issueID", issueID)
		state.cancel()
		delete(m.monitors, issueID)
	}
	m.mu.Unlock()

	// Wait for all monitoring goroutines to finish
	m.wg.Wait()

	m.logger.Info("all session monitors stopped")
}

// monitorLoop is the main monitoring loop for a session
func (m *SessionMonitor) monitorLoop(ctx context.Context, state *monitorState) {
	defer m.wg.Done()

	m.logger.Debug("monitor loop started", "issueID", state.issueID)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Debug("monitor loop stopped", "issueID", state.issueID)
			return

		case <-ticker.C:
			// Capture tmux pane output
			output, err := m.tmux.CapturePane(ctx, state.issueID, m.pollLines)
			if err != nil {
				// Session might not be ready yet or was killed
				m.logger.Debug("failed to capture pane", "issueID", state.issueID, "error", err)
				continue
			}

			// Detect state from output using monitor package patterns
			newState := monitor.DetectState(output)

			// Check if state changed
			m.mu.Lock()
			if state.state != newState {
				m.logger.Info("session state changed",
					"issueID", state.issueID,
					"oldState", state.state,
					"newState", newState,
				)

				state.state = newState

				// Call callback if provided
				if state.callback != nil {
					// Call callback outside of lock to avoid deadlock
					callback := state.callback
					issueID := state.issueID
					m.mu.Unlock()

					// Execute callback
					callback(issueID, newState)
				} else {
					m.mu.Unlock()
				}
			} else {
				m.mu.Unlock()
			}
		}
	}
}

// SetPollLines sets the number of lines to capture from the tmux pane
func (m *SessionMonitor) SetPollLines(lines int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pollLines = lines
}
