package testharness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
)

// Config controls daemon integration harness lifecycle behavior.
type Config struct {
	BaseDir      string
	ProjectID    string
	OTELExporter string
}

// Harness provides deterministic daemon fixture lifecycle helpers.
type Harness struct {
	cfg         Config
	lockManager *lifecycle.LockManager
	lockLease   *lifecycle.Lease
	logFilePath string
	mu          sync.Mutex
	running     bool
}

// New creates a harness fixture rooted in config BaseDir.
func New(cfg Config) *Harness {
	return &Harness{
		cfg: cfg,
	}
}

// Boot creates deterministic filesystem state and acquires singleton lock.
func (h *Harness) Boot() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return nil
	}
	if h.cfg.BaseDir == "" {
		return fmt.Errorf("harness base dir required")
	}
	if err := os.MkdirAll(h.cfg.BaseDir, 0o755); err != nil {
		return fmt.Errorf("create harness base dir: %w", err)
	}

	h.lockManager = lifecycle.NewLockManager(filepath.Join(h.cfg.BaseDir, "daemon.lock"))
	lease, err := h.lockManager.Acquire()
	if err != nil {
		return fmt.Errorf("acquire daemon lock: %w", err)
	}
	h.lockLease = lease
	h.logFilePath = filepath.Join(h.cfg.BaseDir, "daemon-events.log")

	if err := h.appendEvent("daemon.harness.boot", map[string]any{
		"project_id": h.cfg.ProjectID,
		"otel":       h.cfg.OTELExporter != "",
	}); err != nil {
		return err
	}
	h.running = true
	return nil
}

// Shutdown releases fixture resources and lock ownership.
func (h *Harness) Shutdown() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.running {
		return nil
	}
	if err := h.appendEvent("daemon.harness.shutdown", map[string]any{
		"project_id": h.cfg.ProjectID,
	}); err != nil {
		return err
	}
	if h.lockLease != nil {
		if err := h.lockLease.Release(); err != nil {
			return fmt.Errorf("release lock lease: %w", err)
		}
	}
	h.running = false
	return nil
}

// LogFilePath returns structured event log file path for assertions.
func (h *Harness) LogFilePath() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logFilePath
}

func (h *Harness) appendEvent(name string, attrs map[string]any) error {
	f, err := os.OpenFile(h.logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open harness log file: %w", err)
	}
	defer f.Close()

	record := map[string]any{
		"event":       name,
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"project_id":  h.cfg.ProjectID,
		"otel_target": h.cfg.OTELExporter,
	}
	for k, v := range attrs {
		record[k] = v
	}
	b, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal harness event: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write harness event: %w", err)
	}
	return nil
}
