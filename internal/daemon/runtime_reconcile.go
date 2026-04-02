package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	defaultRuntimeReconcileInterval = 30 * time.Second
	defaultRuntimeReconcileTimeout  = 5 * time.Second
	scopedRuntimeReconcileTimeout   = 20 * time.Second
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
	if d.worktree == nil || d.tmux == nil || d.sessionStore == nil || d.sessionRuntimeStateStore() == nil || d.worktreeRuntimeStateStore() == nil {
		return result, nil
	}

	var errs []error
	if worktreeCount, err := d.refreshWorktreeRuntimeState(ctx, result.ProjectID); err != nil {
		errs = append(errs, fmt.Errorf("refresh worktree runtime state: %w", err))
	} else {
		result.WorktreesRefreshed = worktreeCount
	}

	if sessionResult, err := d.reconcileTmuxAndDaemonSessions(ctx, result.ProjectID, ""); err != nil {
		errs = append(errs, fmt.Errorf("reconcile sessions: %w", err))
	} else {
		result.RecreatedTmuxSessions = sessionResult.RecreatedTmuxSessions
		result.AlignedDaemonSessions = sessionResult.AlignedDaemonSessions
	}

	if err := d.refreshSessionRuntimeState(ctx, result.ProjectID); err != nil {
		errs = append(errs, fmt.Errorf("refresh session runtime state: %w", err))
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
	if isScopedDaemonModeEnv(os.Getenv("AZEDARACH_DAEMON_SCOPE"), os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE")) {
		return scopedRuntimeReconcileTimeout
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
	results, err := d.runRuntimeReconcileSweep(reconcileCtx)
	return summarizeRuntimeReconcileSweep(results), err
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

	results, err := d.runRuntimeReconcileSweep(reconcileCtx)
	result := summarizeRuntimeReconcileSweep(results)
	projectCount := len(results)
	if projectCount == 0 {
		projectCount = 1
	}
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("daemon runtime reconcile cycle failed",
				"project_id", result.ProjectID,
				"project_count", projectCount,
				"error", err,
			)
		}
		return
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon runtime reconcile cycle completed",
			"project_id", result.ProjectID,
			"project_count", projectCount,
			"worktrees_refreshed", result.WorktreesRefreshed,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
		)
	}
}

func (d *Daemon) runRuntimeReconcileSweep(ctx context.Context) ([]protocol.RuntimeReconcileResponseBody, error) {
	projectIDs, err := d.runtimeReconcileKnownProjectIDs(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]protocol.RuntimeReconcileResponseBody, 0, len(projectIDs))
	var errs []error
	for _, projectID := range projectIDs {
		result, reconcileErr := d.ensureRuntimeReconciler().Reconcile(ctx, projectID)
		results = append(results, result)
		if reconcileErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", projectID, reconcileErr))
		}
	}
	return results, errors.Join(errs...)
}

func (d *Daemon) runtimeReconcileKnownProjectIDs(ctx context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	projectIDs := make([]string, 0, 8)
	add := func(projectID string) {
		normalized := protocol.NormalizeProjectID(projectID)
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		projectIDs = append(projectIDs, normalized)
	}

	if d == nil {
		return []string{protocol.DefaultProjectID}, nil
	}
	repoDir := strings.TrimSpace(d.cfg.RepoDir)
	scopedMode := isScopedDaemonModeEnv(os.Getenv("AZEDARACH_DAEMON_SCOPE"), os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE"))
	repoProjectID := ""
	repoNameProjectID := ""
	if repoDir != "" {
		repoNameProjectID = protocol.NormalizeProjectID(filepath.Base(repoDir))
		projectID, err := appconfig.ProjectIDForRoot(repoDir)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("resolve runtime reconcile seed project id failed", "repo_dir", repoDir, "error", err)
			}
		} else {
			repoProjectID = protocol.NormalizeProjectID(projectID)
			add(projectID)
		}
	}

	if d.sessionStore != nil {
		for _, projectID := range d.sessionStore.ProjectIDs() {
			add(projectID)
		}
	}

	addRuntimeStateProjects := func(store interface {
		ListProjectIDs(context.Context) ([]string, error)
	}) error { known, err := store.ListProjectIDs(ctx); if err != nil {
		return fmt.Errorf("list runtime-state project ids: %w", err)
	}; for _, projectID := range known {
		add(projectID)
	}; return nil }
	if sessionStore := d.sessionRuntimeStateStore(); sessionStore != nil {
		if err := addRuntimeStateProjects(sessionStore); err != nil {
			return nil, err
		}
	}
	if worktreeStore := d.worktreeRuntimeStateStore(); worktreeStore != nil && worktreeStore != d.sessionRuntimeStateStore() {
		if err := addRuntimeStateProjects(worktreeStore); err != nil {
			return nil, err
		}
	}

	d.revMu.Lock()
	for projectID := range d.revision {
		add(projectID)
	}
	d.revMu.Unlock()

	if len(projectIDs) == 0 {
		add(protocol.DefaultProjectID)
	}
	if scopedMode {
		projectIDs = prioritizeProjectIDs(projectIDs, []string{repoNameProjectID, repoProjectID})
	} else {
		slices.Sort(projectIDs)
	}
	return projectIDs, nil
}

func isScopedDaemonModeEnv(mode, source string) bool {
	mode = strings.TrimSpace(strings.ToLower(mode))
	source = strings.TrimSpace(strings.ToLower(source))
	switch mode {
	case "worktree", "scoped", "local":
		return source == "just-run"
	default:
		return false
	}
}

func prioritizeProjectIDs(projectIDs []string, preferred []string) []string {
	if len(projectIDs) == 0 {
		return projectIDs
	}
	ordered := append([]string(nil), projectIDs...)
	rest := make([]string, 0, len(ordered))
	seenPreferred := make(map[string]struct{}, len(preferred))
	prioritized := make([]string, 0, len(preferred))
	for _, id := range preferred {
		norm := protocol.NormalizeProjectID(id)
		if norm == "" {
			continue
		}
		if _, exists := seenPreferred[norm]; exists {
			continue
		}
		seenPreferred[norm] = struct{}{}
		prioritized = append(prioritized, norm)
	}
	found := make(map[string]struct{}, len(prioritized))
	for _, projectID := range ordered {
		if _, preferred := seenPreferred[projectID]; preferred {
			found[projectID] = struct{}{}
		} else {
			rest = append(rest, projectID)
		}
	}
	slices.Sort(rest)
	result := make([]string, 0, len(ordered))
	for _, projectID := range prioritized {
		if _, ok := found[projectID]; ok {
			result = append(result, projectID)
		}
	}
	return append(result, rest...)
}

func summarizeRuntimeReconcileSweep(results []protocol.RuntimeReconcileResponseBody) protocol.RuntimeReconcileResponseBody {
	if len(results) == 0 {
		return protocol.RuntimeReconcileResponseBody{ProjectID: protocol.DefaultProjectID}
	}
	summary := protocol.RuntimeReconcileResponseBody{
		ProjectID: results[0].ProjectID,
	}
	if len(results) > 1 {
		summary.ProjectID = "multi"
	}
	for _, result := range results {
		summary.WorktreesRefreshed += result.WorktreesRefreshed
		summary.RecreatedTmuxSessions += result.RecreatedTmuxSessions
		summary.AlignedDaemonSessions += result.AlignedDaemonSessions
	}
	return summary
}
