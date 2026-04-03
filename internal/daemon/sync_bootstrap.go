package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
)

type daemonLockManager interface {
	Acquire() (*lifecycle.Lease, error)
	Release() error
}

type daemonServer interface {
	Serve(context.Context) error
}

type syncBootstrapDiagnostic struct {
	State  string
	Ready  bool
	Reason string
}

func (d syncBootstrapDiagnostic) message() string {
	switch {
	case d.Ready:
		return "sync bootstrap ready"
	case strings.TrimSpace(d.Reason) != "":
		return fmt.Sprintf("sync bootstrap failed: %s", strings.TrimSpace(d.Reason))
	default:
		return "sync bootstrap not ready"
	}
}

type syncBootstrapState struct {
	mu     sync.RWMutex
	ready  bool
	reason string
}

func (s *syncBootstrapState) markReady() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = true
	s.reason = ""
}

func (s *syncBootstrapState) markFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = false
	if err == nil {
		s.reason = ""
		return
	}
	s.reason = err.Error()
}

func (s *syncBootstrapState) diagnostic() syncBootstrapDiagnostic {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := "pending"
	if s.ready {
		state = "ready"
	} else if strings.TrimSpace(s.reason) != "" {
		state = "failed"
	}
	return syncBootstrapDiagnostic{
		State:  state,
		Ready:  s.ready,
		Reason: s.reason,
	}
}

func (d *Daemon) bootstrapSyncOrchestrator(ctx context.Context) error {
	if d.syncBootstrapFn == nil {
		d.syncBootstrapFn = d.defaultSyncBootstrap
	}
	if err := d.syncBootstrapFn(ctx); err != nil {
		d.syncBootstrapState.markFailed(err)
		return fmt.Errorf("sync bootstrap: %w", err)
	}
	d.syncBootstrapState.markReady()
	return nil
}

func (d *Daemon) defaultSyncBootstrap(ctx context.Context) error {
	client := d.issueClientForProject(protocol.DefaultProjectID)
	if client == nil {
		return errors.New("issue store unavailable")
	}
	if _, err := client.DBStats(); err != nil {
		return fmt.Errorf("open issue store: %w", err)
	}
	runtimeStore := d.runtimeStateStoreForProject(protocol.DefaultProjectID)
	if runtimeStore == nil {
		return nil
	}
	if _, err := runtimeStore.ListProjectIDs(ctx); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("open runtime state store failed during bootstrap", "error", err)
		}
		return nil
	}
	if err := d.migrateLegacyRuntimeState(ctx); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("migrate runtime state store failed during bootstrap", "error", err)
		}
	}
	return nil
}

func (d *Daemon) syncBootstrapDiagnostic() syncBootstrapDiagnostic {
	return d.syncBootstrapState.diagnostic()
}

func (d *Daemon) guardSyncDependentCommand(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, bool) {
	if !d.shouldGuardSyncDependentCommand(req) {
		return protocol.ResponseEnvelope{}, false
	}
	diag := d.syncBootstrapDiagnostic()
	if diag.Ready {
		return protocol.ResponseEnvelope{}, false
	}
	return d.errorResponse(req, protocol.ErrorCodeUnavailable, diag.message()), true
}

func (d *Daemon) shouldGuardSyncDependentCommand(req protocol.RequestEnvelope) bool {
	return daemonhandlers.CommandRequiresSyncBootstrap(req)
}
