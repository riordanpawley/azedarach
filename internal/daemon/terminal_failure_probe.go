package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	terminalFailureProbeStaleAfter = 15 * time.Second
	terminalFailureProbeMinBackoff = 30 * time.Second
	terminalFailureProbeMaxBackoff = 5 * time.Minute
	terminalFailureProbeLines      = 8
)

type terminalFailureProbeState struct {
	lastProbe      time.Time
	fingerprint    [sha256.Size]byte
	detected       bool
	detectedHookAt time.Time
	reason         domain.AgentTerminalFailureReason
	nextProbe      time.Time
	misses         uint8
}

// applyTerminalFailureProbes is the daemon-owned fallback for providers that
// render a terminal failure but emit no lifecycle hook. It locally parses only
// eight trailing lines from stale hook-backed busy sessions and caches
// observations to avoid pane polling or any AI/model request.
func (d *Daemon) applyTerminalFailureProbes(
	_ context.Context,
	projectID string,
	sessions []state.Session,
	namingScope string,
	activityByKey map[string]sessionDisplayActivity,
) map[string]sessionDisplayActivity {
	if len(sessions) == 0 || len(activityByKey) == 0 {
		return activityByKey
	}
	now := timeNow().UTC()
	sessionByKey := sessionProjectionAggregateByIssueKey(sessions, namingScope)
	for issueKey, activity := range activityByKey {
		if activity.Activity != "busy" || activity.Source != "hooks" || activity.UpdatedAt.IsZero() || now.Sub(activity.UpdatedAt) < terminalFailureProbeStaleAfter {
			continue
		}
		session, ok := sessionByKey[issueKey]
		if !ok || session.ID == "" {
			continue
		}
		probeKey := projectID + "\x00" + session.ID
		cached, useCached, _ := d.cachedTerminalFailureProbeReadOnly(probeKey, activity.UpdatedAt, now)
		if useCached {
			activityByKey[issueKey] = sessionDisplayActivity{Activity: "error", Source: "terminal", UpdatedAt: cached.lastProbe}
			continue
		}
	}
	return activityByKey
}

// observeTerminalFailureProbes performs the bounded external pane read owned by
// the asynchronous observer. A detected failure is persisted and published as
// a typed current observation; read handlers consume that projection and may
// also project the cache during the coalescing window without reaching tmux.
func (d *Daemon) observeTerminalFailureProbes(
	ctx context.Context,
	projectID string,
	sessions []state.Session,
	namingScope string,
	activityByKey map[string]sessionDisplayActivity,
) (int, error) {
	if d.tmux == nil || len(sessions) == 0 || len(activityByKey) == 0 {
		return 0, nil
	}
	now := timeNow().UTC()
	published := 0
	sessionByKey := sessionProjectionAggregateByIssueKey(sessions, namingScope)
	for issueKey, activity := range activityByKey {
		if activity.Activity != "busy" || activity.Source != "hooks" || activity.UpdatedAt.IsZero() || now.Sub(activity.UpdatedAt) < terminalFailureProbeStaleAfter {
			continue
		}
		session, ok := sessionByKey[issueKey]
		if !ok || session.ID == "" {
			continue
		}
		probeKey := projectID + "\x00" + session.ID
		_, cached, shouldProbe := d.cachedTerminalFailureProbe(probeKey, activity.UpdatedAt, now)
		if cached || !shouldProbe {
			continue
		}
		output, err := d.tmux.CapturePane(ctx, session.ID, terminalFailureProbeLines)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("terminal failure pane probe failed", "project_id", projectID, "session_id", session.ID, "error", err)
			}
			continue
		}
		reason, detected := domain.ClassifyAgentTerminalOutput(output)
		fingerprint := sha256.Sum256([]byte(output))
		if !detected {
			d.recordTerminalFailureProbe(probeKey, activity.UpdatedAt, now, fingerprint, reason, false)
			continue
		}
		count, err := d.materializeTerminalFailureObservation(ctx, projectID, session, activity.UpdatedAt, now, fingerprint, reason)
		if err != nil {
			return published, err
		}
		published += count
		if count > 0 && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("agent terminal failure detected from asynchronous sparse pane probe", "project_id", projectID, "session_id", session.ID, "reason", reason)
		}
	}
	return published, nil
}

func (d *Daemon) materializeTerminalFailureObservation(
	ctx context.Context,
	projectID string,
	session state.Session,
	hookAt, probedAt time.Time,
	fingerprint [sha256.Size]byte,
	reason domain.AgentTerminalFailureReason,
) (int, error) {
	probeKey := projectID + "\x00" + session.ID
	if !d.recordTerminalFailureProbe(probeKey, hookAt, probedAt, fingerprint, reason, true) {
		return 0, nil
	}
	// Cache-only callers are retained for focused projection rendering tests.
	// Daemon runtime paths have a configured store and follow the durable
	// projection-and-publication path below.
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return 0, nil
	}
	observedAt := probedAt.UTC()
	if !observedAt.After(session.UpdatedAt) {
		observedAt = session.UpdatedAt.Add(time.Nanosecond)
	}
	observedState := state.NormalizeSessionState(session.ObservedState)
	if observedState == "" {
		observedState = state.NormalizeSessionState(session.State)
	}
	if observedState == state.SessionStateStopped {
		d.rollbackTerminalFailureProbe(probeKey, fingerprint, probedAt)
		return 0, nil
	}
	changed, applied, err := store.ApplyPhysicalSessionObservation(ctx, state.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: session.ID, ObservedState: observedState,
		Activity: "error", ActivitySource: "terminal", UpdatedAt: observedAt,
	})
	if err != nil {
		d.rollbackTerminalFailureProbe(probeKey, fingerprint, probedAt)
		return 0, fmt.Errorf("persist terminal failure observation for %s: %w", session.ID, err)
	}
	if !applied {
		d.rollbackTerminalFailureProbe(probeKey, fingerprint, probedAt)
		return 0, nil
	}
	provenance := domain.CurrentTmuxObservationProvenance(observedAt)
	if err := domain.ValidateCurrentExternalObservation(provenance, domain.ExternalObservationProductSessionRuntime); err != nil {
		return 0, err
	}
	for _, changedSession := range changed {
		d.publishObservedSessionProjectionEvent(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, changedSession, provenance)
	}
	return len(changed), nil
}

func (d *Daemon) rollbackTerminalFailureProbe(key string, fingerprint [sha256.Size]byte, probedAt time.Time) {
	d.terminalFailureProbeMu.Lock()
	defer d.terminalFailureProbeMu.Unlock()
	probe := d.terminalFailureProbes[key]
	if probe.fingerprint != fingerprint || !probe.lastProbe.Equal(probedAt) {
		return
	}
	probe.detected = false
	probe.nextProbe = time.Time{}
	d.terminalFailureProbes[key] = probe
}

func (d *Daemon) cachedTerminalFailureProbeReadOnly(key string, hookAt, now time.Time) (terminalFailureProbeState, bool, bool) {
	d.terminalFailureProbeMu.Lock()
	defer d.terminalFailureProbeMu.Unlock()
	probe := d.terminalFailureProbes[key]
	if probe.detected && !hookAt.After(probe.detectedHookAt) {
		return probe, true, false
	}
	return probe, false, !now.Before(probe.nextProbe)
}

func (d *Daemon) cachedTerminalFailureProbe(key string, hookAt, now time.Time) (terminalFailureProbeState, bool, bool) {
	d.terminalFailureProbeMu.Lock()
	defer d.terminalFailureProbeMu.Unlock()
	if d.terminalFailureProbes == nil {
		d.terminalFailureProbes = make(map[string]terminalFailureProbeState)
	}
	probe := d.terminalFailureProbes[key]
	if probe.detected && !hookAt.After(probe.detectedHookAt) {
		return probe, true, false
	}
	if now.Before(probe.nextProbe) {
		return probe, false, false
	}
	probe.lastProbe = now
	probe.nextProbe = now.Add(terminalFailureProbeMinBackoff)
	d.terminalFailureProbes[key] = probe
	return probe, false, true
}

func (d *Daemon) recordTerminalFailureProbe(key string, hookAt, now time.Time, fingerprint [sha256.Size]byte, reason domain.AgentTerminalFailureReason, detected bool) bool {
	d.terminalFailureProbeMu.Lock()
	defer d.terminalFailureProbeMu.Unlock()
	probe := d.terminalFailureProbes[key]
	probe.lastProbe = now
	if !detected {
		probe.detected = false
		if probe.misses < 4 {
			probe.misses++
		}
		backoff := terminalFailureProbeMinBackoff << (probe.misses - 1)
		if backoff > terminalFailureProbeMaxBackoff {
			backoff = terminalFailureProbeMaxBackoff
		}
		probe.nextProbe = now.Add(backoff)
		d.terminalFailureProbes[key] = probe
		return false
	}
	// A newer hook proves progress resumed. Do not re-raise the exact pane image
	// that was already handled before that hook.
	if probe.fingerprint == fingerprint && !probe.detectedHookAt.IsZero() && !hookAt.Before(probe.detectedHookAt) {
		probe.detected = false
		probe.detectedHookAt = hookAt
		probe.nextProbe = now.Add(terminalFailureProbeMaxBackoff)
		d.terminalFailureProbes[key] = probe
		return false
	}
	probe.detected = true
	probe.misses = 0
	probe.detectedHookAt = hookAt
	probe.fingerprint = fingerprint
	probe.reason = reason
	d.terminalFailureProbes[key] = probe
	return true
}
