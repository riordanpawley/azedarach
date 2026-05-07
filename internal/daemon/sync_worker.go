package daemon

import (
	"context"
	"time"
)

func (d *Daemon) startLinearSyncWorker(ctx context.Context) {
	if d == nil || d.cfg.Logger == nil {
		return
	}
	interval := time.Duration(d.cfg.RuntimeReconcileInterval)
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	projectID := d.canonicalProjectID("")
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				service, summary, err := d.linearSyncService(ctx, projectID)
				if err != nil {
					d.cfg.Logger.Debug("linear sync worker unavailable", "project_id", projectID, "error", err)
					continue
				}
				if service == nil || summary.Skipped {
					continue
				}
				runCtx, cancel := context.WithTimeout(ctx, interval)
				_, err = service.Run(runCtx)
				cancel()
				if err != nil {
					d.cfg.Logger.Warn("linear sync worker run failed", "project_id", projectID, "error", err)
				}
			}
		}
	}()
}
