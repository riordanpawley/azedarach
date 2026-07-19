package daemon

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// startTmuxObservationWorker owns routine tmux inventory and sparse pane
// observation. Snapshot and watch handlers only consume its current-state
// products and never trigger a poll themselves.
func (d *Daemon) startTmuxObservationWorker(ctx context.Context) {
	if d == nil || d.tmux == nil {
		return
	}
	interval := d.cfg.TmuxObservationInterval
	if interval <= 0 {
		interval = defaultTmuxObservationInterval
	}
	d.tmuxObservationWG.Add(1)
	go func() {
		defer d.tmuxObservationWG.Done()
		defer func() {
			if recovered := recover(); recovered != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Error("tmux observation worker panicked", "panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		d.runTmuxObservationCycle(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.runTmuxObservationCycle(ctx)
			}
		}
	}()
}

func (d *Daemon) runTmuxObservationCycle(ctx context.Context) {
	if ctx.Err() != nil || d == nil || d.tmux == nil {
		return
	}
	timeout := d.cfg.TmuxObservationTimeout
	if timeout <= 0 {
		timeout = defaultTmuxObservationTimeout
	}
	inventoryCtx, cancelInventory := context.WithTimeout(ctx, timeout)
	observedAt := timeNow().UTC()
	sessionInfos, err := d.tmux.ListSessionInfos(inventoryCtx)
	if err != nil {
		cancelInventory()
		d.logTmuxObservationFailure("inventory_sessions", err)
		return
	}
	paneInfos, err := d.tmux.ListPaneInfos(inventoryCtx)
	if err != nil {
		cancelInventory()
		d.logTmuxObservationFailure("inventory_panes", err)
		return
	}
	projects, err := d.runtimeReconcileKnownProjectIDs(inventoryCtx)
	cancelInventory()
	if err != nil {
		d.logTmuxObservationFailure("known_projects", err)
		return
	}
	live := newTmuxRuntimeLiveness(sessionInfos, paneInfos)
	provenance := domain.CurrentTmuxObservationProvenance(observedAt)
	if err := domain.ValidateCurrentExternalObservation(provenance, domain.ExternalObservationProductSessionRuntime); err != nil {
		d.logTmuxObservationFailure("payload_admission", err)
		return
	}
	projectCtx, cancelProjects := context.WithTimeout(ctx, timeout)
	defer cancelProjects()
	d.observeTmuxProjects(projectCtx, projects, func(projectCtx context.Context, projectID string) {
		if err := d.observeTmuxProject(projectCtx, projectID, live, provenance); err != nil {
			d.logTmuxObservationFailure("project", fmt.Errorf("%s: %w", projectID, err))
		}
	})
}

// observeTmuxProjects rotates the first project before every bounded sweep.
// A project that consumes the remaining sweep budget therefore cannot remain
// first and starve the same later projects on every observation cycle.
func (d *Daemon) observeTmuxProjects(ctx context.Context, projects []string, observe func(context.Context, string)) {
	if len(projects) == 0 || observe == nil {
		return
	}
	d.tmuxObservationCursorMu.Lock()
	start := d.tmuxObservationCursor % len(projects)
	d.tmuxObservationCursor = (start + 1) % len(projects)
	d.tmuxObservationCursorMu.Unlock()
	for offset := range projects {
		if ctx.Err() != nil {
			return
		}
		observe(ctx, projects[(start+offset)%len(projects)])
	}
}

func (d *Daemon) observeTmuxProject(ctx context.Context, projectID string, live tmuxRuntimeLiveness, provenance domain.ExternalObservationProvenance) error {
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	sessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list current session projection: %w", err)
	}
	for _, session := range sessions {
		before := session
		info, present := live.sessionByID[strings.TrimSpace(session.ID)]
		applyObservedRuntimeLiveness(&session, info, present, live)
		if !sessionRuntimeProjectionChanged(before, session) {
			continue
		}
		session.UpdatedAt = provenance.ObservedAt
		activity, activitySource := session.Activity, session.ActivitySource
		if daemonstate.NormalizeSessionState(session.ObservedState) == daemonstate.SessionStateStopped {
			activity, activitySource = "", ""
		}
		changed, applied, err := d.applyPhysicalSessionObservationWithProjectionCleanup(ctx, store, projectID, daemonstate.PhysicalSessionObservation{
			ProjectID: projectID, SessionID: session.ID, ObservedState: session.ObservedState,
			Activity: activity, ActivitySource: activitySource, UpdatedAt: provenance.ObservedAt,
			TmuxAttachedCount: &session.TmuxAttachedCount, StartedAt: session.StartedAt,
		})
		if err != nil {
			return fmt.Errorf("apply session observation %s: %w", session.ID, err)
		}
		if !applied {
			continue
		}
		for _, row := range changed {
			d.publishObservedSessionProjectionEvent(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, row, provenance)
		}
	}
	// Reload after liveness writes so a sparse terminal observation receives a
	// strictly newer physical observation version and publishes the full row.
	sessions, err = store.ListSessionStates(ctx, projectID)
	if err != nil {
		return fmt.Errorf("reload current session projection: %w", err)
	}
	activity := sessionDisplayActivityByIssueKeyFromSessions(sessions, d.sessionNamingScope(projectID))
	if _, err := d.observeTerminalFailureProbes(ctx, projectID, sessions, d.sessionNamingScope(projectID), activity); err != nil {
		return fmt.Errorf("observe sparse pane classification: %w", err)
	}
	if _, err := d.reconcileStaleBusySessionActivity(ctx, projectID, nil); err != nil {
		return fmt.Errorf("observe stale busy activity: %w", err)
	}
	d.taskListRuntimeRefreshMu.Lock()
	if d.taskListRuntimeLastRefresh == nil {
		d.taskListRuntimeLastRefresh = map[string]time.Time{}
	}
	d.taskListRuntimeLastRefresh[projectID] = provenance.ObservedAt
	d.taskListRuntimeRefreshMu.Unlock()
	return nil
}

func (d *Daemon) logTmuxObservationFailure(phase string, err error) {
	if d != nil && d.cfg.Logger != nil && err != nil {
		d.cfg.Logger.Debug("tmux observation cycle failed", "phase", phase, "error", err)
	}
}
