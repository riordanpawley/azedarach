package daemon

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
)

// startGitObservationWorker owns routine Git observation. Task, board, detail,
// and orchestration reads consume its disposable projections and never poll
// Git or the worktree registry themselves.
func (d *Daemon) startGitObservationWorker(ctx context.Context) {
	if d == nil || d.gitStatusAdapter == nil {
		return
	}
	interval := d.cfg.GitObservationInterval
	if interval <= 0 {
		interval = defaultGitObservationInterval
	}
	d.gitObservationWG.Add(1)
	go func() {
		defer d.gitObservationWG.Done()
		defer func() {
			if recovered := recover(); recovered != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Error("git observation worker panicked", "panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		d.runGitObservationCycle(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.runGitObservationCycle(ctx)
			}
		}
	}()
}

func (d *Daemon) runGitObservationCycle(ctx context.Context) {
	if ctx.Err() != nil || d == nil || d.gitStatusAdapter == nil {
		return
	}
	timeout := d.cfg.GitObservationTimeout
	if timeout <= 0 {
		timeout = defaultGitObservationTimeout
	}
	cycleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	projects, err := d.runtimeReconcileKnownProjectIDs(cycleCtx)
	if err != nil {
		d.logGitObservationFailure("known_projects", err)
		return
	}
	d.gitObservationCursorMu.Lock()
	start := 0
	if len(projects) > 0 {
		start = d.gitObservationCursor % len(projects)
		d.gitObservationCursor = (start + 1) % len(projects)
	}
	d.gitObservationCursorMu.Unlock()
	for offset := range projects {
		if cycleCtx.Err() != nil {
			return
		}
		projectID := projects[(start+offset)%len(projects)]
		if _, err := d.observeGitProject(cycleCtx, projectID); err != nil {
			d.logGitObservationFailure("project", fmt.Errorf("%s: %w", projectID, err))
		}
	}
}

func (d *Daemon) observeGitProject(ctx context.Context, projectID string) (int, error) {
	startedAt := time.Now()
	projectID = d.canonicalProjectID(projectID)
	store := d.worktreeRuntimeStateStoreIfConfigured(projectID)
	if store == nil || d.gitStatusAdapter == nil {
		return 0, nil
	}
	rows, err := store.ListWorktreeStates(ctx, projectID)
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "git_observation.projection_read", startedAt, "project_id", projectID, "worktree_count", len(rows))
	if err != nil {
		return 0, fmt.Errorf("list worktree projections: %w", err)
	}
	rows = d.rotateGitObservationRows(projectID, rows, defaultGitObservationProjectLimit)
	issueIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if issueID := strings.TrimSpace(row.IssueID); issueID != "" {
			issueIDs = append(issueIDs, issueID)
		}
	}
	taskByIssue := d.runtimeWorktreeIssueTaskContext(ctx, projectID, issueIDs)
	scheduled := 0
	scheduleStartedAt := time.Now()
	for _, row := range rows {
		issueID, worktree := strings.TrimSpace(row.IssueID), strings.TrimSpace(row.Path)
		task, found := runtimeTaskByIssueID(taskByIssue, issueID)
		if !found || !gitObservationTaskEligible(task) || worktree == "" {
			continue
		}
		if d.suppressProjectedStaleWorktreeGitRefresh(ctx, projectID, issueID, worktree, nil) {
			continue
		}
		if _, err := d.gitStatusAdapter.queueGitStatusRefresh(projectID, worktree, reconcilePriorityBackground, "observer"); err != nil {
			return scheduled, fmt.Errorf("schedule Git observation: %w", err)
		}
		scheduled++
	}
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "git_observation.schedule", scheduleStartedAt, "project_id", projectID, "candidate_count", len(rows), "scheduled_count", scheduled)
	return scheduled, nil
}

func (d *Daemon) rotateGitObservationRows(projectID string, rows []daemonstate.WorktreeState, limit int) []daemonstate.WorktreeState {
	if len(rows) == 0 || limit <= 0 || len(rows) <= limit {
		return rows
	}
	d.gitObservationCursorMu.Lock()
	defer d.gitObservationCursorMu.Unlock()
	if d.gitObservationIssueCursor == nil {
		d.gitObservationIssueCursor = map[string]int{}
	}
	start := d.gitObservationIssueCursor[projectID] % len(rows)
	d.gitObservationIssueCursor[projectID] = (start + limit) % len(rows)
	rotated := make([]daemonstate.WorktreeState, 0, limit)
	for offset := 0; offset < limit; offset++ {
		rotated = append(rotated, rows[(start+offset)%len(rows)])
	}
	return rotated
}

func gitObservationTaskEligible(task domain.Task) bool {
	return !task.IssueClosed() && !task.State.IsArchived() && task.HasTmuxSession && task.Session != nil
}

func (d *Daemon) logGitObservationFailure(phase string, err error) {
	if d != nil && d.cfg.Logger != nil && err != nil {
		d.cfg.Logger.Debug("git observation cycle failed", "phase", phase, "error", err)
	}
}
