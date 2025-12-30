package network

import (
	"context"
	"net/http"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// StatusChecker monitors network connectivity status
type StatusChecker struct {
	mu        sync.RWMutex
	isOnline  bool
	lastCheck time.Time
	client    *http.Client
}

// StatusMsg is sent when the network status changes
type StatusMsg struct {
	Online bool
}

// NewStatusChecker creates a new network status checker
func NewStatusChecker() *StatusChecker {
	return &StatusChecker{
		isOnline: true, // Optimistically assume online
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}
}

// Check performs a connectivity check to github.com
// Returns true if online, false if offline
func (s *StatusChecker) Check(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://github.com", nil)
	if err != nil {
		s.setOnline(false)
		return false
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.setOnline(false)
		return false
	}
	defer resp.Body.Close()

	// Any 2xx or 3xx response means we're online
	online := resp.StatusCode >= 200 && resp.StatusCode < 400

	s.setOnline(online)
	return online
}

// IsOnline returns the cached online status
func (s *StatusChecker) IsOnline() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isOnline
}

// LastCheck returns the time of the last connectivity check
func (s *StatusChecker) LastCheck() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastCheck
}

// setOnline updates the cached online status
func (s *StatusChecker) setOnline(online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isOnline = online
	s.lastCheck = time.Now()
}

// StartMonitoring begins polling network status at the specified interval
// Sends StatusMsg to the program when status changes
func (s *StatusChecker) StartMonitoring(ctx context.Context, program *tea.Program, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial check
	wasOnline := s.Check(ctx)

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			isOnline := s.Check(ctx)

			// Send message only on status change
			if isOnline != wasOnline {
				program.Send(StatusMsg{Online: isOnline})
				wasOnline = isOnline
			}
		}
	}
}

// CheckCmd returns a tea.Cmd that performs a one-time connectivity check
func (s *StatusChecker) CheckCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		online := s.Check(ctx)
		return StatusMsg{Online: online}
	}
}
