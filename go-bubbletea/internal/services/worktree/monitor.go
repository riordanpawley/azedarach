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
type StateChangeCallback func(beadID string, state domain.SessionState)

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
	beadID   string
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
func (m *SessionMonitor) Start(ctx context.Context, beadID string, callback StateChangeCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("starting session monitor", "beadID", beadID)

	// Stop existing monitor if any
	if existing, ok := m.monitors[beadID]; ok {
		m.logger.Debug("stopping existing monitor", "beadID", beadID)
		existing.cancel()
	}

	// Create cancellable context
	monitorCtx, cancel := context.WithCancel(ctx)

	state := &monitorState{
		beadID:   beadID,
		cancel:   cancel,
		state:    domain.SessionIdle,
		callback: callback,
	}
	m.monitors[beadID] = state

	// Start monitoring goroutine
	m.wg.Add(1)
	go m.monitorLoop(monitorCtx, state)
}

// Stop stops monitoring a session
func (m *SessionMonitor) Stop(beadID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("stopping session monitor", "beadID", beadID)

	if state, ok := m.monitors[beadID]; ok {
		state.cancel()
		delete(m.monitors, beadID)
	}
}

// GetState returns the current state of a monitored session
func (m *SessionMonitor) GetState(beadID string) domain.SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if state, ok := m.monitors[beadID]; ok {
		return state.state
	}
	return domain.SessionIdle
}

// StopAll stops monitoring all sessions and waits for goroutines to finish
func (m *SessionMonitor) StopAll() {
	m.logger.Info("stopping all session monitors")

	m.mu.Lock()
	for beadID, state := range m.monitors {
		m.logger.Debug("stopping monitor", "beadID", beadID)
		state.cancel()
		delete(m.monitors, beadID)
	}
	m.mu.Unlock()

	// Wait for all monitoring goroutines to finish
	m.wg.Wait()

	m.logger.Info("all session monitors stopped")
}

// monitorLoop is the main monitoring loop for a session
func (m *SessionMonitor) monitorLoop(ctx context.Context, state *monitorState) {
	defer m.wg.Done()

	m.logger.Debug("monitor loop started", "beadID", state.beadID)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Debug("monitor loop stopped", "beadID", state.beadID)
			return

		case <-ticker.C:
			// Capture tmux pane output
			output, err := m.tmux.CapturePane(ctx, state.beadID, m.pollLines)
			if err != nil {
				// Session might not be ready yet or was killed
				m.logger.Debug("failed to capture pane", "beadID", state.beadID, "error", err)
				continue
			}

			// Detect state from output using monitor package patterns
			newState := monitor.DetectState(output)

			// Check if state changed
			m.mu.Lock()
			if state.state != newState {
				m.logger.Info("session state changed",
					"beadID", state.beadID,
					"oldState", state.state,
					"newState", newState,
				)

				state.state = newState

				// Call callback if provided
				if state.callback != nil {
					// Call callback outside of lock to avoid deadlock
					callback := state.callback
					beadID := state.beadID
					m.mu.Unlock()

					// Execute callback
					callback(beadID, newState)
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
