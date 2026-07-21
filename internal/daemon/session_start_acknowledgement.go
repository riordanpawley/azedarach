package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	// The acknowledgement state is authoritative; this deadline only contains a
	// wedged external agent startup. Keep enough cold-start margin that host
	// reboot load is not mistaken for a semantic bootstrap failure.
	sessionStartAcknowledgementTimeout       = protocol.OrchestratorSessionStartAcknowledgementBudget
	sessionStartAcknowledgementPoll          = 50 * time.Millisecond
	sessionStartAcknowledgementSampleTimeout = 500 * time.Millisecond
	sessionStartCompensationTimeout          = 8 * time.Second
)

type sessionStartBootstrapFailureReason string

const (
	sessionStartBootstrapAgentExited         sessionStartBootstrapFailureReason = "agent_exited"
	sessionStartBootstrapShellFallback       sessionStartBootstrapFailureReason = "shell_fallback"
	sessionStartBootstrapDiagnosticError     sessionStartBootstrapFailureReason = "bootstrap_error"
	sessionStartBootstrapAcknowledgementLost sessionStartBootstrapFailureReason = "acknowledgement_missing"
)

type sessionStartBootstrapError struct {
	Reason         sessionStartBootstrapFailureReason
	SessionID      string
	Incarnation    string
	CurrentCommand string
	Diagnostics    string
	Cause          error
}

func (e *sessionStartBootstrapError) Error() string {
	parts := []string{
		fmt.Sprintf("agent bootstrap acknowledgement failed (%s)", e.Reason),
		"session=" + strings.TrimSpace(e.SessionID),
		"incarnation=" + strings.TrimSpace(e.Incarnation),
	}
	if command := strings.TrimSpace(e.CurrentCommand); command != "" {
		parts = append(parts, "pane_command="+command)
	}
	if diagnostics := strings.TrimSpace(e.Diagnostics); diagnostics != "" {
		parts = append(parts, "diagnostics="+compactSessionStartCommandDetail(diagnostics, 1200))
	}
	if e.Cause != nil {
		parts = append(parts, "cause="+e.Cause.Error())
	}
	return strings.Join(parts, "\n")
}

func (e *sessionStartBootstrapError) Unwrap() error { return e.Cause }

type sessionStartAcknowledgementSample struct {
	promptConsumed  bool
	promptSubmitted bool
	identity        daemonstate.ManagedAgentIdentity
	identityFound   bool
	pane            tmux.PaneInfo
	paneFound       bool
}

type managedAgentIdentityProjection struct {
	identity     daemonstate.ManagedAgentIdentity
	acknowledged bool
}

func managedAgentIdentityProjectionKey(projectID, sessionID, logicalPaneID string) string {
	return strings.Join([]string{protocol.NormalizeProjectID(projectID), strings.TrimSpace(sessionID), strings.TrimSpace(logicalPaneID)}, "\x00")
}

func (d *Daemon) recordManagedAgentIdentityProjection(identity daemonstate.ManagedAgentIdentity, acknowledged bool) {
	if d == nil {
		return
	}
	key := managedAgentIdentityProjectionKey(identity.ProjectID, identity.SessionID, identity.LogicalPaneID)
	d.managedAgentIdentityMu.Lock()
	if d.managedAgentIdentityProjection == nil {
		d.managedAgentIdentityProjection = map[string]managedAgentIdentityProjection{}
	}
	current, found := d.managedAgentIdentityProjection[key]
	if found && current.identity.AgentIncarnation == identity.AgentIncarnation && current.identity.TmuxPaneID == identity.TmuxPaneID && current.identity.PanePID == identity.PanePID {
		if current.acknowledged {
			acknowledged = true
			identity = current.identity
		} else if acknowledged {
			identity.ObservedAt = current.identity.ObservedAt
		}
	}
	d.managedAgentIdentityProjection[key] = managedAgentIdentityProjection{identity: identity, acknowledged: acknowledged}
	d.managedAgentIdentityMu.Unlock()
}

func (d *Daemon) projectedManagedAgentIdentity(projectID, sessionID, logicalPaneID string) (managedAgentIdentityProjection, bool) {
	if d == nil {
		return managedAgentIdentityProjection{}, false
	}
	d.managedAgentIdentityMu.RLock()
	projection, found := d.managedAgentIdentityProjection[managedAgentIdentityProjectionKey(projectID, sessionID, logicalPaneID)]
	d.managedAgentIdentityMu.RUnlock()
	return projection, found
}

func (d *Daemon) purgeManagedAgentIdentityProjectionForSession(projectID, sessionID string) {
	if d == nil {
		return
	}
	prefix := strings.Join([]string{protocol.NormalizeProjectID(projectID), strings.TrimSpace(sessionID), ""}, "\x00")
	d.managedAgentIdentityMu.Lock()
	for key := range d.managedAgentIdentityProjection {
		if strings.HasPrefix(key, prefix) {
			delete(d.managedAgentIdentityProjection, key)
		}
	}
	d.managedAgentIdentityMu.Unlock()
}

func (d *Daemon) purgeManagedAgentIdentityProjectionForProject(projectID string) {
	if d == nil {
		return
	}
	prefix := protocol.NormalizeProjectID(projectID) + "\x00"
	d.managedAgentIdentityMu.Lock()
	for key := range d.managedAgentIdentityProjection {
		if strings.HasPrefix(key, prefix) {
			delete(d.managedAgentIdentityProjection, key)
		}
	}
	d.managedAgentIdentityMu.Unlock()
}

func (d *Daemon) readManagedAgentIdentity(ctx context.Context, store *daemonstate.RuntimeStateStore, projectID, sessionID, logicalPaneID string) (daemonstate.ManagedAgentIdentity, bool, error) {
	if d != nil && d.managedAgentIdentityRead != nil {
		return d.managedAgentIdentityRead(ctx, store, projectID, sessionID, logicalPaneID)
	}
	return store.GetManagedAgentIdentity(ctx, projectID, sessionID, logicalPaneID)
}

func (d *Daemon) validateRecoveredSessionStartPromptHandoff(path string) (sessionPromptHandoff, error) {
	required := strings.TrimSpace(path) != ""
	handoffType := sessionRestartPromptHandoffTypeNone
	if required {
		handoffType = sessionRestartPromptHandoffTypeOwnerOnlyArtifact
	}
	return d.validateRecoveredSessionRestartPromptHandoff(sessionRestartRecoveryPlan{
		PromptHandoffRequired: required,
		PromptHandoffType:     handoffType,
		PromptPath:            path,
	})
}

func (d *Daemon) waitForInitialManagedAgentAcknowledgement(ctx context.Context, projectID, sessionID, incarnation string, handoff sessionPromptHandoff) error {
	return d.waitForInitialManagedAgentAcknowledgementForPane(ctx, projectID, sessionID, "agent", incarnation, handoff)
}

func (d *Daemon) waitForInitialManagedAgentAcknowledgementForPane(ctx context.Context, projectID, sessionID, logicalPaneID, incarnation string, handoff sessionPromptHandoff) error {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return &sessionStartBootstrapError{Reason: sessionStartBootstrapAcknowledgementLost, SessionID: sessionID, Incarnation: incarnation, Cause: errors.New("session runtime store unavailable")}
	}
	if d.tmux == nil {
		return &sessionStartBootstrapError{Reason: sessionStartBootstrapAcknowledgementLost, SessionID: sessionID, Incarnation: incarnation, Cause: errors.New("tmux adapter unavailable")}
	}
	waitCtx, cancel := context.WithTimeout(ctx, sessionStartAcknowledgementTimeout)
	defer cancel()
	ticker := time.NewTicker(sessionStartAcknowledgementPoll)
	defer ticker.Stop()
	return d.waitForInitialManagedAgentAcknowledgementWithPoll(waitCtx, store, projectID, sessionID, logicalPaneID, incarnation, handoff, ticker.C)
}

func (d *Daemon) waitForInitialManagedAgentAcknowledgementWithPoll(waitCtx context.Context, store *daemonstate.RuntimeStateStore, projectID, sessionID, logicalPaneID, incarnation string, handoff sessionPromptHandoff, poll <-chan time.Time) error {
	var last sessionStartAcknowledgementSample
	var lastSampleErr error
	exactPaneObserved := false
	finalSample := false
	for {
		sampleParent := waitCtx
		if waitCtx.Err() != nil {
			if finalSample {
				reason := sessionStartBootstrapAcknowledgementLost
				if sessionStartPaneIsShellFallback(d.runtimeConfigForProject(projectID).SessionShell, last.pane.CurrentCommand) {
					reason = sessionStartBootstrapShellFallback
				}
				return d.initialSessionBootstrapFailure(context.WithoutCancel(waitCtx), sessionID, incarnation, last, reason, errors.Join(waitCtx.Err(), lastSampleErr))
			}
			finalSample = true
			sampleParent = context.WithoutCancel(waitCtx)
		}
		sampleCtx, sampleCancel := context.WithTimeout(sampleParent, sessionStartAcknowledgementSampleTimeout)
		sample, err := d.sampleInitialManagedAgentAcknowledgement(sampleCtx, store, projectID, sessionID, logicalPaneID, incarnation, handoff)
		sampleCancel()
		if err != nil {
			lastSampleErr = err
			if finalSample {
				continue
			}
			select {
			case <-waitCtx.Done():
				continue
			case <-poll:
				continue
			}
		}
		last = sample
		exactPane := sample.identityFound && sample.paneFound && managedAgentIdentityMatchesPane(sample.identity, sample.pane, incarnation)
		if exactPane {
			exactPaneObserved = true
		}
		if (sample.promptConsumed || sample.promptSubmitted) && exactPane {
			if sessionStartPaneIsShellFallback(d.runtimeConfigForProject(projectID).SessionShell, sample.pane.CurrentCommand) {
				return d.initialSessionBootstrapFailure(waitCtx, sessionID, incarnation, sample, sessionStartBootstrapShellFallback, errors.New("managed agent exited to a shell before acknowledgement"))
			}
			return nil
		}
		if exactPaneObserved && !sample.paneFound {
			return d.initialSessionBootstrapFailure(waitCtx, sessionID, incarnation, sample, sessionStartBootstrapAgentExited, errors.New("managed tmux pane disappeared before acknowledgement"))
		}
		select {
		case <-waitCtx.Done():
			reason := sessionStartBootstrapAcknowledgementLost
			if sessionStartPaneIsShellFallback(d.runtimeConfigForProject(projectID).SessionShell, last.pane.CurrentCommand) {
				reason = sessionStartBootstrapShellFallback
			}
			return d.initialSessionBootstrapFailure(context.WithoutCancel(waitCtx), sessionID, incarnation, last, reason, waitCtx.Err())
		case <-poll:
		}
	}
}

func (d *Daemon) sampleInitialManagedAgentAcknowledgement(ctx context.Context, store *daemonstate.RuntimeStateStore, projectID, sessionID, logicalPaneID, incarnation string, handoff sessionPromptHandoff) (sessionStartAcknowledgementSample, error) {
	consumed, err := sessionPromptHandoffConsumed(handoff)
	if err != nil {
		return sessionStartAcknowledgementSample{}, err
	}
	logicalPaneID = strings.TrimSpace(logicalPaneID)
	projection, projected := d.projectedManagedAgentIdentity(projectID, sessionID, logicalPaneID)
	projectionMatchesIncarnation := projected && strings.TrimSpace(projection.identity.AgentIncarnation) == strings.TrimSpace(incarnation)
	identity, found, submitted := projection.identity, projectionMatchesIncarnation, projectionMatchesIncarnation && projection.acknowledged
	if !projectionMatchesIncarnation || !projection.acknowledged {
		durableIdentity, durableFound, err := d.readManagedAgentIdentity(ctx, store, projectID, sessionID, logicalPaneID)
		if err != nil {
			if !projectionMatchesIncarnation {
				return sessionStartAcknowledgementSample{}, fmt.Errorf("load managed agent acknowledgement: %w", err)
			}
		} else if durableFound {
			identity, found = durableIdentity, true
			submitted = durableIdentity.UpdatedAt.After(durableIdentity.ObservedAt)
			if submitted {
				d.recordManagedAgentIdentityProjection(durableIdentity, true)
			}
		}
	}
	panes, err := d.tmux.ListPaneInfosForSession(ctx, sessionID)
	if err != nil {
		return sessionStartAcknowledgementSample{}, fmt.Errorf("inspect managed agent pane: %w", err)
	}
	sample := sessionStartAcknowledgementSample{promptConsumed: consumed, promptSubmitted: submitted, identity: identity, identityFound: found}
	for _, pane := range panes {
		if strings.TrimSpace(pane.SessionName) == strings.TrimSpace(sessionID) &&
			(!found || sanitizeRuntimePaneID(pane.PaneID) == sanitizeRuntimePaneID(identity.TmuxPaneID)) {
			sample.pane, sample.paneFound = pane, true
			break
		}
	}
	return sample, nil
}

func (d *Daemon) validateExistingManagedAgentReadiness(ctx context.Context, projectID, sessionID, logicalPaneID string) error {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil || d.tmux == nil {
		return &sessionStartBootstrapError{Reason: sessionStartBootstrapAcknowledgementLost, SessionID: sessionID, Cause: errors.New("managed agent readiness authority unavailable")}
	}
	identity, found, err := store.GetManagedAgentIdentity(ctx, projectID, sessionID, strings.TrimSpace(logicalPaneID))
	if err != nil {
		return &sessionStartBootstrapError{Reason: sessionStartBootstrapAcknowledgementLost, SessionID: sessionID, Cause: err}
	}
	if !found || strings.TrimSpace(identity.AgentIncarnation) == "" {
		return &sessionStartBootstrapError{Reason: sessionStartBootstrapAcknowledgementLost, SessionID: sessionID, Cause: errors.New("durable managed agent identity is missing")}
	}
	panes, err := d.tmux.ListPaneInfosForSession(ctx, sessionID)
	if err != nil {
		return &sessionStartBootstrapError{Reason: sessionStartBootstrapAcknowledgementLost, SessionID: sessionID, Incarnation: identity.AgentIncarnation, Cause: err}
	}
	for _, pane := range panes {
		if strings.TrimSpace(pane.SessionName) != strings.TrimSpace(sessionID) || !managedAgentIdentityMatchesPane(identity, pane, identity.AgentIncarnation) {
			continue
		}
		if sessionStartPaneIsShellFallback(d.runtimeConfigForProject(projectID).SessionShell, pane.CurrentCommand) {
			return &sessionStartBootstrapError{Reason: sessionStartBootstrapShellFallback, SessionID: sessionID, Incarnation: identity.AgentIncarnation, CurrentCommand: pane.CurrentCommand, Cause: errors.New("managed agent exited to a shell")}
		}
		return nil
	}
	return &sessionStartBootstrapError{Reason: sessionStartBootstrapAgentExited, SessionID: sessionID, Incarnation: identity.AgentIncarnation, Cause: errors.New("exact managed agent pane is not live")}
}

func managedAgentIdentityMatchesPane(identity daemonstate.ManagedAgentIdentity, pane tmux.PaneInfo, incarnation string) bool {
	return strings.TrimSpace(identity.AgentIncarnation) == strings.TrimSpace(incarnation) &&
		sanitizeRuntimePaneID(identity.TmuxPaneID) == sanitizeRuntimePaneID(pane.PaneID) &&
		identity.PanePID > 0 && identity.PanePID == pane.PanePID
}

func sessionPromptHandoffConsumed(handoff sessionPromptHandoff) (bool, error) {
	path := strings.TrimSpace(handoff.PromptPath)
	if path == "" {
		return true, nil
	}
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}

func sessionStartPaneIsShellFallback(configuredShell, currentCommand string) bool {
	command := strings.ToLower(strings.TrimSpace(filepath.Base(currentCommand)))
	configured := strings.ToLower(strings.TrimSpace(filepath.Base(configuredShell)))
	if configured != "" && command == configured {
		return true
	}
	switch command {
	case "sh", "bash", "zsh", "fish", "dash", "ksh":
		return true
	default:
		return false
	}
}

func (d *Daemon) initialSessionBootstrapFailure(ctx context.Context, sessionID, incarnation string, sample sessionStartAcknowledgementSample, reason sessionStartBootstrapFailureReason, cause error) error {
	diagnostics := ""
	if d.tmux != nil {
		captureCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if output, err := d.tmux.CapturePane(captureCtx, sessionID, 120); err == nil {
			diagnostics = strings.TrimSpace(output)
			if reason == sessionStartBootstrapShellFallback && diagnostics != "" {
				reason = sessionStartBootstrapDiagnosticError
			}
		}
	}
	return &sessionStartBootstrapError{Reason: reason, SessionID: sessionID, Incarnation: incarnation, CurrentCommand: sample.pane.CurrentCommand, Diagnostics: diagnostics, Cause: cause}
}
