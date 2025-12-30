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
	BeadID    string
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

// Start starts a dev server for a bead
func (m *Manager) Start(ctx context.Context, beadID, name, command string) (*Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already running
	if srv, exists := m.servers[beadID]; exists && srv.Status == "running" {
		return srv, nil
	}

	// Allocate port
	port, err := m.allocator.Allocate(beadID)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate port: %w", err)
	}

	server := &Server{
		ID:        beadID,
		Name:      name,
		Port:      port,
		Status:    "running",
		BeadID:    beadID,
		Command:   command,
		StartedAt: time.Now(),
	}

	m.servers[beadID] = server
	m.logger.Info("dev server started", "bead_id", beadID, "port", port)

	return server, nil
}

// Stop stops a dev server
func (m *Manager) Stop(ctx context.Context, beadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	srv, exists := m.servers[beadID]
	if !exists {
		return fmt.Errorf("server not found: %s", beadID)
	}

	srv.Status = "stopped"
	srv.Uptime = time.Since(srv.StartedAt)
	m.allocator.Release(beadID)

	m.logger.Info("dev server stopped", "bead_id", beadID)
	return nil
}

// Toggle starts or stops a dev server
func (m *Manager) Toggle(ctx context.Context, beadID string) error {
	m.mu.RLock()
	srv, exists := m.servers[beadID]
	m.mu.RUnlock()

	if exists && srv.Status == "running" {
		return m.Stop(ctx, beadID)
	}

	_, err := m.Start(ctx, beadID, beadID, "")
	return err
}

// Restart restarts a dev server
func (m *Manager) Restart(ctx context.Context, beadID string) error {
	if err := m.Stop(ctx, beadID); err != nil {
		// Ignore "not found" errors for restart
		m.logger.Debug("server not running, starting fresh", "bead_id", beadID)
	}

	_, err := m.Start(ctx, beadID, beadID, "")
	return err
}

// Get returns a server by bead ID
func (m *Manager) Get(beadID string) (*Server, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	srv, exists := m.servers[beadID]
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
