package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	defaultRuntimeReconcileInterval = 30 * time.Second
	defaultRuntimeReconcileTimeout  = 5 * time.Second
)

type runtimeReconciler interface {
	Reconcile(context.Context, string) (protocol.RuntimeReconcileResponseBody, error)
}

type runtimeReconcileService struct {
	daemon *Daemon
}

type runtimeReconcileCommandBody struct {
	ProjectID string `json:"project_id"`
}

func newRuntimeReconcileService(d *Daemon) *runtimeReconcileService {
	return &runtimeReconcileService{daemon: d}
}

func (s *runtimeReconcileService) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	result := protocol.RuntimeReconcileResponseBody{
		ProjectID: protocol.NormalizeProjectID(projectID),
	}
	d := s.daemon
	if d == nil {
		return result, nil
	}
	if d.worktree == nil || d.tmux == nil || d.sessionStore == nil || d.projectionStore == nil {
		return result, nil
	}

	var errs []error
	if worktreeCount, err := d.refreshWorktreeProjectionCache(ctx, result.ProjectID); err != nil {
		errs = append(errs, fmt.Errorf("refresh worktree projections: %w", err))
	} else {
		result.WorktreesRefreshed = worktreeCount
	}

	if sessionResult, err := d.reconcileTmuxAndDaemonSessions(ctx, result.ProjectID, ""); err != nil {
		errs = append(errs, fmt.Errorf("reconcile sessions: %w", err))
	} else {
		result.RecreatedTmuxSessions = sessionResult.RecreatedTmuxSessions
		result.AlignedDaemonSessions = sessionResult.AlignedDaemonSessions
	}

	if err := d.refreshSessionProjectionCache(ctx, result.ProjectID); err != nil {
		errs = append(errs, fmt.Errorf("refresh session projections: %w", err))
	}

	return result, errors.Join(errs...)
}

func (d *Daemon) runtimeReconcileInterval() time.Duration {
	if d.cfg.RuntimeReconcileInterval > 0 {
		return d.cfg.RuntimeReconcileInterval
	}
	return defaultRuntimeReconcileInterval
}

func (d *Daemon) runtimeReconcileTimeout() time.Duration {
	if d.cfg.RuntimeReconcileTimeout > 0 {
		return d.cfg.RuntimeReconcileTimeout
	}
	return defaultRuntimeReconcileTimeout
}

func (d *Daemon) runtimeReconcileProjectID(req protocol.RequestEnvelope) string {
	projectID := strings.TrimSpace(d.projectID(req.Meta))
	if projectID == "" {
		return protocol.DefaultProjectID
	}
	return projectID
}

func (d *Daemon) ensureRuntimeReconciler() runtimeReconciler {
	if d.runtimeReconciler == nil {
		d.runtimeReconciler = newRuntimeReconcileService(d)
	}
	return d.runtimeReconciler
}

func (d *Daemon) handleRuntimeReconcile(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body runtimeReconcileCommandBody
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
		}
	}
	projectID := strings.TrimSpace(body.ProjectID)
	if projectID == "" {
		projectID = d.runtimeReconcileProjectID(req)
	} else {
		projectID = protocol.NormalizeProjectID(projectID)
	}

	result, err := d.ensureRuntimeReconciler().Reconcile(ctx, projectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}

	resp := d.successResponse(req)
	resp.Revision = d.currentRevision(projectID)
	bodyBytes, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err)), nil
	}
	resp.Body = bodyBytes
	return resp, nil
}

func (d *Daemon) runStartupRuntimeReconcile(ctx context.Context) (protocol.RuntimeReconcileResponseBody, error) {
	if d == nil {
		return protocol.RuntimeReconcileResponseBody{}, nil
	}
	timeout := d.runtimeReconcileTimeout()
	if timeout <= 0 {
		timeout = defaultRuntimeReconcileTimeout
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return d.ensureRuntimeReconciler().Reconcile(reconcileCtx, d.runtimeReconcileDaemonProjectID())
}

func (d *Daemon) startRuntimeReconcileWorker(ctx context.Context) {
	if d == nil {
		return
	}
	interval := d.runtimeReconcileInterval()
	if interval <= 0 {
		interval = defaultRuntimeReconcileInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.runRuntimeReconcileCycle(ctx)
			}
		}
	}()
}

func (d *Daemon) runRuntimeReconcileCycle(ctx context.Context) {
	if d == nil {
		return
	}
	timeout := d.runtimeReconcileTimeout()
	if timeout <= 0 {
		timeout = defaultRuntimeReconcileTimeout
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := d.ensureRuntimeReconciler().Reconcile(reconcileCtx, d.runtimeReconcileDaemonProjectID())
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("daemon runtime reconcile cycle failed",
				"project_id", result.ProjectID,
				"error", err,
			)
		}
		return
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon runtime reconcile cycle completed",
			"project_id", result.ProjectID,
			"worktrees_refreshed", result.WorktreesRefreshed,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
		)
	}
}

func (d *Daemon) runtimeReconcileDaemonProjectID() string {
	if d == nil {
		return protocol.DefaultProjectID
	}
	repoDir := strings.TrimSpace(d.cfg.RepoDir)
	if repoDir == "" {
		return protocol.DefaultProjectID
	}
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("resolve runtime reconcile project id failed", "repo_dir", repoDir, "error", err)
		}
		return protocol.DefaultProjectID
	}
	return protocol.NormalizeProjectID(projectID)
}
