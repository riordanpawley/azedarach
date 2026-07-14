package daemon

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	terminalFailureProbeStaleAfter = 15 * time.Second
	terminalFailureProbeMinBackoff = 30 * time.Second
	terminalFailureProbeMaxBackoff = 5 * time.Minute
	terminalFailureProbeLines      = 8
)

type terminalFailureProbeState struct {
	lastProbe        time.Time
	fingerprint      [sha256.Size]byte
	detected         bool
	detectedHookAt   time.Time
	idlePrompt       bool
	idlePromptHookAt time.Time
	reason           domain.AgentTerminalFailureReason
	nextProbe        time.Time
	misses           uint8
}

// applyTerminalFailureProbes is the daemon-owned fallback for providers that
// render a terminal failure but emit no lifecycle hook. It locally parses only
// eight trailing lines from stale hook-backed busy sessions and caches
// observations to avoid pane polling or any AI/model request.
func (d *Daemon) applyTerminalFailureProbes(
	ctx context.Context,
	projectID string,
	sessions []state.Session,
	namingScope string,
	activityByKey map[string]sessionDisplayActivity,
) map[string]sessionDisplayActivity {
	if d.tmux == nil || len(sessions) == 0 || len(activityByKey) == 0 {
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
		cached, useCached, shouldProbe := d.cachedTerminalFailureProbe(probeKey, activity.UpdatedAt, now, false)
		if useCached {
			activityByKey[issueKey] = sessionDisplayActivity{Activity: "error", Source: "terminal", UpdatedAt: cached.lastProbe}
			continue
		}
		if !shouldProbe {
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
		if d.recordTerminalFailureProbe(probeKey, activity.UpdatedAt, now, fingerprint, reason, detected, domain.ClassifyAgentTerminalIdle(output)) {
			activityByKey[issueKey] = sessionDisplayActivity{Activity: "error", Source: "terminal", UpdatedAt: now}
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("agent terminal failure detected from sparse pane probe", "project_id", projectID, "session_id", session.ID, "reason", reason)
			}
		}
	}
	return activityByKey
}

func (d *Daemon) cachedTerminalFailureProbe(key string, hookAt, now time.Time, revalidateIdlePrompt bool) (terminalFailureProbeState, bool, bool) {
	d.terminalFailureProbeMu.Lock()
	defer d.terminalFailureProbeMu.Unlock()
	if d.terminalFailureProbes == nil {
		d.terminalFailureProbes = make(map[string]terminalFailureProbeState)
	}
	probe := d.terminalFailureProbes[key]
	if probe.detected && !hookAt.After(probe.detectedHookAt) {
		return probe, true, false
	}
	if probe.idlePrompt && hookAt.After(probe.idlePromptHookAt) {
		probe.idlePrompt = false
		probe.idlePromptHookAt = time.Time{}
		probe.misses = 0
		probe.nextProbe = time.Time{}
		d.terminalFailureProbes[key] = probe
	}
	if revalidateIdlePrompt && probe.idlePrompt && !hookAt.After(probe.idlePromptHookAt) {
		probe.lastProbe = now
		probe.nextProbe = now.Add(terminalFailureProbeMinBackoff)
		probe.idlePrompt = false
		probe.idlePromptHookAt = time.Time{}
		d.terminalFailureProbes[key] = probe
		return probe, false, true
	}
	if now.Before(probe.nextProbe) {
		return probe, false, false
	}
	probe.lastProbe = now
	probe.nextProbe = now.Add(terminalFailureProbeMinBackoff)
	d.terminalFailureProbes[key] = probe
	return probe, false, true
}

func (d *Daemon) recordTerminalFailureProbe(key string, hookAt, now time.Time, fingerprint [sha256.Size]byte, reason domain.AgentTerminalFailureReason, detected, idlePrompt bool) bool {
	d.terminalFailureProbeMu.Lock()
	defer d.terminalFailureProbeMu.Unlock()
	probe := d.terminalFailureProbes[key]
	probe.lastProbe = now
	if !detected {
		probe.detected = false
		probe.idlePrompt = idlePrompt
		if idlePrompt {
			probe.idlePromptHookAt = hookAt
		} else {
			probe.idlePromptHookAt = time.Time{}
		}
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
	probe.idlePrompt = false
	probe.idlePromptHookAt = time.Time{}
	probe.misses = 0
	probe.detectedHookAt = hookAt
	probe.fingerprint = fingerprint
	probe.reason = reason
	d.terminalFailureProbes[key] = probe
	return true
}
