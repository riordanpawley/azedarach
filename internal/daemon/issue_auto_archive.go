package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const defaultIssueAutoArchiveInterval = 24 * time.Hour

type issueAutoArchiveWorker struct {
	daemon *Daemon
	logger *slog.Logger

	closeCh   chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func newIssueAutoArchiveWorker(d *Daemon, logger *slog.Logger) *issueAutoArchiveWorker {
	return &issueAutoArchiveWorker{
		daemon:  d,
		logger:  logger,
		closeCh: make(chan struct{}),
	}
}

func (d *Daemon) startIssueAutoArchiveWorker(ctx context.Context) {
	if d == nil || d.issueAutoArchive == nil {
		return
	}
	d.issueAutoArchive.Start(ctx)
}

func (w *issueAutoArchiveWorker) Start(ctx context.Context) {
	if w == nil || w.daemon == nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runAll(runCtx, timeNow().UTC())
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-w.closeCh:
				return
			case now := <-ticker.C:
				w.runAll(runCtx, now.UTC())
			}
		}
	}()
}

func (w *issueAutoArchiveWorker) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		close(w.closeCh)
	})
	w.wg.Wait()
}

func (w *issueAutoArchiveWorker) runAll(ctx context.Context, now time.Time) {
	if w == nil || w.daemon == nil {
		return
	}
	for _, projectID := range w.projectIDs() {
		if _, err := w.daemon.runIssueAutoArchiveOnce(ctx, projectID, now); err != nil && w.logger != nil {
			w.logger.Warn("issue auto archive failed", "project_id", projectID, "error", err)
		}
	}
}

func (w *issueAutoArchiveWorker) projectIDs() []string {
	if w == nil || w.daemon == nil {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(projectID string, out *[]string) {
		projectID = w.daemon.canonicalProjectID(projectID)
		if strings.TrimSpace(projectID) == "" {
			projectID = protocol.DefaultProjectID
		}
		if _, ok := seen[projectID]; ok {
			return
		}
		seen[projectID] = struct{}{}
		*out = append(*out, projectID)
	}
	out := make([]string, 0, 4)
	add("", &out)
	if !w.daemon.cfg.ScopedRuntime {
		if registry, err := appconfig.LoadProjectsRegistry(); err == nil && registry != nil {
			for _, project := range registry.Projects {
				add(project.Name, &out)
			}
		}
	}
	return out
}

func (d *Daemon) runIssueAutoArchiveOnce(ctx context.Context, projectID string, now time.Time) (int, error) {
	if d == nil {
		return 0, nil
	}
	projectID = d.canonicalProjectID(projectID)
	cfg := d.runtimeConfigForProject(projectID).IssueAutoArchive
	if !cfg.Enabled {
		return 0, nil
	}
	if cfg.ClosedAfterDays < 1 {
		return 0, fmt.Errorf("issues.autoArchive.closedAfterDays must be >= 1")
	}
	interval := issueAutoArchiveInterval(cfg)
	if !d.issueAutoArchiveDue(projectID, now, interval) {
		return 0, nil
	}
	tasks, _, err := d.loadCleanupTasks(ctx, projectID, nil, false)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(map[string]string{"source": "issues.autoArchive"})
	if err != nil {
		return 0, err
	}
	cutoff := now.UTC().AddDate(0, 0, -cfg.ClosedAfterDays)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID(fmt.Sprintf("issue-auto-archive-%d", now.UnixNano())),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandProjectCleanup,
		Meta: protocol.Metadata{
			ProjectID:   naming.ProjectID(projectID),
			ClientActor: "issue-auto-archive",
		},
		Body:   body,
		SentAt: now.UTC(),
	}
	archived, err := d.cleanupArchiveDoneTasksBefore(ctx, req, projectID, tasks, cutoff)
	if d.cfg.Logger != nil && archived > 0 {
		d.cfg.Logger.Info("issue auto archive completed", "project_id", projectID, "archived", archived, "closed_after_days", cfg.ClosedAfterDays)
	}
	return archived, err
}

func issueAutoArchiveInterval(cfg appconfig.IssueAutoArchiveConfig) time.Duration {
	interval, err := time.ParseDuration(strings.TrimSpace(cfg.Interval))
	if err != nil || interval <= 0 {
		return defaultIssueAutoArchiveInterval
	}
	return interval
}

func (d *Daemon) issueAutoArchiveDue(projectID string, now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		interval = defaultIssueAutoArchiveInterval
	}
	projectID = d.canonicalProjectID(projectID)
	d.projectConfigMu.Lock()
	defer d.projectConfigMu.Unlock()
	if d.issueAutoArchiveLastRun == nil {
		d.issueAutoArchiveLastRun = map[string]time.Time{}
	}
	last := d.issueAutoArchiveLastRun[projectID]
	if !last.IsZero() && now.Sub(last) < interval {
		return false
	}
	d.issueAutoArchiveLastRun[projectID] = now.UTC()
	return true
}
