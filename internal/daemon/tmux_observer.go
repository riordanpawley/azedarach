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
	cycleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	observedAt := timeNow().UTC()
	sessionInfos, err := d.tmux.ListSessionInfos(cycleCtx)
	if err != nil {
		d.logTmuxObservationFailure("inventory_sessions", err)
		return
	}
	paneInfos, err := d.tmux.ListPaneInfos(cycleCtx)
	if err != nil {
		d.logTmuxObservationFailure("inventory_panes", err)
		return
	}
	projects, err := d.runtimeReconcileKnownProjectIDs(cycleCtx)
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
	for _, projectID := range projects {
		if cycleCtx.Err() != nil {
			return
		}
		if err := d.observeTmuxProject(cycleCtx, projectID, live, provenance); err != nil {
			d.logTmuxObservationFailure("project", fmt.Errorf("%s: %w", projectID, err))
		}
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
		changed, applied, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
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
	if _, err := d.reconcileStaleBusySessionActivity(ctx, projectID, nil); err != nil {
		return fmt.Errorf("observe sparse pane classification: %w", err)
	}
	activity := sessionDisplayActivityByIssueKeyFromSessions(sessions, d.sessionNamingScope(projectID))
	d.observeTerminalFailureProbes(ctx, projectID, sessions, d.sessionNamingScope(projectID), activity)
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
