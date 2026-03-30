package devserver

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Server represents a dev server instance
type Server struct {
	ID        string
	Name      string
	Port      int
	Status    string // "running", "stopped", "error"
	IssueID   string
	Command   string
	Uptime    time.Duration
	StartedAt time.Time
}

// Manager manages dev server instances
type Manager struct {
	allocator *PortAllocator
	servers   map[string]*Server
	mu        sync.RWMutex
	logger    *slog.Logger
}

// NewManager creates a new dev server manager
func NewManager(allocator *PortAllocator, logger *slog.Logger) *Manager {
	return &Manager{
		allocator: allocator,
		servers:   make(map[string]*Server),
		logger:    logger,
	}
}

// Start starts a dev server for a issue
func (m *Manager) Start(ctx context.Context, issueID, name, command string) (*Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already running
	if srv, exists := m.servers[issueID]; exists && srv.Status == "running" {
		return srv, nil
	}

	// Allocate port
	port, err := m.allocator.Allocate(issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate port: %w", err)
	}

	server := &Server{
		ID:        issueID,
		Name:      name,
		Port:      port,
		Status:    "running",
		IssueID:   issueID,
		Command:   command,
		StartedAt: time.Now(),
	}

	m.servers[issueID] = server
	m.logger.Info("dev server started", "issue_id", issueID, "port", port)

	return server, nil
}

// Stop stops a dev server
func (m *Manager) Stop(ctx context.Context, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	srv, exists := m.servers[issueID]
	if !exists {
		return fmt.Errorf("server not found: %s", issueID)
	}

	srv.Status = "stopped"
	srv.Uptime = time.Since(srv.StartedAt)
	m.allocator.Release(issueID)

	m.logger.Info("dev server stopped", "issue_id", issueID)
	return nil
}

// Toggle starts or stops a dev server
func (m *Manager) Toggle(ctx context.Context, issueID string) error {
	m.mu.RLock()
	srv, exists := m.servers[issueID]
	m.mu.RUnlock()

	if exists && srv.Status == "running" {
		return m.Stop(ctx, issueID)
	}

	_, err := m.Start(ctx, issueID, issueID, "")
	return err
}

// Restart restarts a dev server
func (m *Manager) Restart(ctx context.Context, issueID string) error {
	if err := m.Stop(ctx, issueID); err != nil {
		// Ignore "not found" errors for restart
		m.logger.Debug("server not running, starting fresh", "issue_id", issueID)
	}

	_, err := m.Start(ctx, issueID, issueID, "")
	return err
}

// Get returns a server by issue ID
func (m *Manager) Get(issueID string) (*Server, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	srv, exists := m.servers[issueID]
	return srv, exists
}

// List returns all servers
func (m *Manager) List() []*Server {
	m.mu.RLock()
	defer m.mu.RUnlock()

	servers := make([]*Server, 0, len(m.servers))
	for _, srv := range m.servers {
		// Update uptime for running servers
		if srv.Status == "running" {
			srv.Uptime = time.Since(srv.StartedAt)
		}
		servers = append(servers, srv)
	}
	return servers
}
