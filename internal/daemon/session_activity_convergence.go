package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const staleBusyPromptProbeAfter = 15 * time.Second

// reconcileStaleBusySessionActivity is the bounded fallback for a completed
// interactive turn whose idle hook was lost. Periodic runtime reconcile calls
// it without requiring new pane output, and the recovered observation replaces
// the stale hook source before canonical session materialization.
func (d *Daemon) reconcileStaleBusySessionActivity(ctx context.Context, projectID string, issueIDs []string) (int, error) {
	if d == nil || d.tmux == nil {
		return 0, nil
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return 0, nil
	}
	source := sourceForInvariant(daemonInvariantSessionActivityConverge)
	if !usesProjectionSource(source) || !usesTmuxSource(source) {
		return 0, fmt.Errorf("unsupported session activity convergence invariant source: %s", source)
	}
	evidenceRows, err := store.ListSessionActivityEvidence(ctx, projectID, issueIDs)
	if err != nil {
		return 0, fmt.Errorf("list activity evidence for terminal prompt convergence: %w", err)
	}
	sessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("list sessions for terminal prompt convergence: %w", err)
	}
	sessionByID := make(map[string]daemonstate.Session, len(sessions))
	for _, session := range sessions {
		sessionByID[strings.TrimSpace(session.ID)] = session
	}

	now := timeNow().UTC()
	converged := 0
	for _, evidence := range daemonstate.AggregateSessionActivityEvidence(evidenceRows) {
		if normalizeSessionActivity(evidence.Activity) != "busy" || normalizeSessionActivitySource(evidence.ActivitySource, "") != "hooks" || evidence.ObservedAt.IsZero() || now.Sub(evidence.ObservedAt) < staleBusyPromptProbeAfter {
			continue
		}
		session, ok := sessionByID[strings.TrimSpace(evidence.SessionID)]
		if !ok || daemonstate.NormalizeSessionState(session.ObservedState) == daemonstate.SessionStateStopped {
			continue
		}
		probeKey := projectID + "\x00" + session.ID
		_, terminalFailureCached, shouldProbe := d.cachedTerminalFailureProbe(probeKey, evidence.ObservedAt, now, true)
		if terminalFailureCached || !shouldProbe {
			continue
		}
		output, captureErr := d.tmux.CapturePane(ctx, session.ID, terminalFailureProbeLines)
		if captureErr != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("stale busy terminal prompt probe failed", "project_id", projectID, "issue_id", evidence.IssueID, "session_id", session.ID, "error", captureErr)
			}
			continue
		}
		if reason, terminalFailure := domain.ClassifyAgentTerminalOutput(output); terminalFailure {
			d.recordTerminalFailureProbe(probeKey, evidence.ObservedAt, now, sha256.Sum256([]byte(output)), reason, true, false)
			continue
		}
		if !domain.ClassifyAgentTerminalIdle(output) {
			d.recordTerminalFailureProbe(probeKey, evidence.ObservedAt, now, sha256.Sum256([]byte(output)), "", false, false)
			continue
		}
		recovered := evidence
		recovered.Activity = "idle"
		recovered.ActivitySource = "terminal"
		recovered.Hook = "terminal_prompt_probe"
		recovered.Event = "idle_prompt_recovered"
		recovered.ObservedAt = now
		recovered.UpdatedAt = now
		if err := store.UpsertSessionActivityEvidence(ctx, recovered); err != nil {
			return converged, fmt.Errorf("persist terminal prompt convergence for %s: %w", session.ID, err)
		}
		winner, found, err := store.GetSessionActivityEvidence(ctx, projectID, session.ID)
		if err != nil {
			return converged, fmt.Errorf("verify terminal prompt convergence for %s: %w", session.ID, err)
		}
		if !found || normalizeSessionActivity(winner.Activity) != "idle" || normalizeSessionActivitySource(winner.ActivitySource, "") != "terminal" || winner.Event != recovered.Event || !winner.ObservedAt.Equal(now) {
			continue
		}
		changed, applied, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
			ProjectID: projectID, SessionID: session.ID, ObservedState: daemonstate.SessionStateRunning,
			Activity: "idle", ActivitySource: "terminal", UpdatedAt: now,
		})
		if err != nil {
			return converged, fmt.Errorf("materialize terminal prompt convergence for %s: %w", session.ID, err)
		}
		if !applied {
			continue
		}
		writer := d.runtimeProjectionStateWriter()
		for _, changedSession := range changed {
			writer.PublishSessionProjectionEvent(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, changedSession)
		}
		converged++
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("stale busy session activity converged from terminal prompt",
				"project_id", projectID,
				"issue_id", evidence.IssueID,
				"session_id", session.ID,
				"previous_activity", evidence.Activity,
				"previous_source", evidence.ActivitySource,
				"stale_for_ms", now.Sub(evidence.ObservedAt).Milliseconds(),
				"activity", "idle",
				"activity_source", "terminal",
			)
		}
	}
	return converged, nil
}
