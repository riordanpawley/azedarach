package daemon

import (
	"context"
	"strings"
	"time"

	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
)

const (
	heavySessionStartBackgroundDeferReason = "heavy_session_start_active"
	heavySessionStartSignalCheckTimeout    = 250 * time.Millisecond
)

func (d *Daemon) shouldDeferBackgroundScanForHeavySessionStart(ctx context.Context, projectID, scan string, priority reconcileQueuePriority) bool {
	if d == nil || priority > reconcilePriorityBackground {
		return false
	}
	checkCtx, cancel := heavySessionStartSignalCheckContext(ctx)
	defer cancel()
	active, err := d.hasActiveHeavySessionStart(checkCtx, projectID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("heavy session-start scan check failed open",
				"project_id", d.canonicalProjectID(projectID),
				"scan", strings.TrimSpace(scan),
				"error", err,
			)
		}
		return false
	}
	if !active {
		return false
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("background scan deferred during heavy session start",
			"project_id", d.canonicalProjectID(projectID),
			"scan", strings.TrimSpace(scan),
			"reason", heavySessionStartBackgroundDeferReason,
		)
	}
	return true
}

func heavySessionStartSignalCheckContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, heavySessionStartSignalCheckTimeout)
}

func (d *Daemon) hasActiveHeavySessionStart(ctx context.Context, projectID string) (bool, error) {
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		return false, nil
	}
	projectID = d.canonicalProjectID(projectID)
	records, err := d.operationRuntime.manager.List(ctx, daemonops.Query{
		ProjectID: projectID,
		Kind:      daemonhandlers.CommandSessionStart,
		States:    []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
	})
	if err != nil {
		return false, err
	}
	return len(records) > 0, nil
}
