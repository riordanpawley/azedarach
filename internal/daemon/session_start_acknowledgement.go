package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const (
	sessionStartAcknowledgementTimeout = 8 * time.Second
	sessionStartAcknowledgementPoll    = 50 * time.Millisecond
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
	promptConsumed bool
	identity       daemonstate.ManagedAgentIdentity
	identityFound  bool
	pane           tmux.PaneInfo
	paneFound      bool
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
	var last sessionStartAcknowledgementSample
	for {
		sampleCtx := waitCtx
		var sampleCancel context.CancelFunc
		if waitCtx.Err() != nil {
			sampleCtx, sampleCancel = context.WithTimeout(context.WithoutCancel(waitCtx), time.Second)
		}
		sample, err := d.sampleInitialManagedAgentAcknowledgement(sampleCtx, store, projectID, sessionID, incarnation, handoff)
		if sampleCancel != nil {
			sampleCancel()
		}
		if err != nil {
			return &sessionStartBootstrapError{Reason: sessionStartBootstrapAcknowledgementLost, SessionID: sessionID, Incarnation: incarnation, Cause: err}
		}
		last = sample
		if sample.promptConsumed && sample.identityFound && sample.paneFound && managedAgentIdentityMatchesPane(sample.identity, sample.pane, incarnation) {
			if sessionStartPaneIsShellFallback(d.runtimeConfigForProject(projectID).SessionShell, sample.pane.CurrentCommand) {
				return d.initialSessionBootstrapFailure(waitCtx, sessionID, incarnation, sample, sessionStartBootstrapShellFallback, errors.New("managed agent exited to a shell before acknowledgement"))
			}
			return nil
		}
		if !sample.paneFound {
			return d.initialSessionBootstrapFailure(waitCtx, sessionID, incarnation, sample, sessionStartBootstrapAgentExited, errors.New("managed tmux pane disappeared before acknowledgement"))
		}
		select {
		case <-waitCtx.Done():
			reason := sessionStartBootstrapAcknowledgementLost
			if sessionStartPaneIsShellFallback(d.runtimeConfigForProject(projectID).SessionShell, last.pane.CurrentCommand) {
				reason = sessionStartBootstrapShellFallback
			}
			return d.initialSessionBootstrapFailure(context.WithoutCancel(waitCtx), sessionID, incarnation, last, reason, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (d *Daemon) sampleInitialManagedAgentAcknowledgement(ctx context.Context, store *daemonstate.RuntimeStateStore, projectID, sessionID, incarnation string, handoff sessionPromptHandoff) (sessionStartAcknowledgementSample, error) {
	consumed, err := sessionPromptHandoffConsumed(handoff)
	if err != nil {
		return sessionStartAcknowledgementSample{}, err
	}
	identity, found, err := store.GetManagedAgentIdentity(ctx, projectID, sessionID, "agent")
	if err != nil {
		return sessionStartAcknowledgementSample{}, fmt.Errorf("load managed agent acknowledgement: %w", err)
	}
	panes, err := d.tmux.ListPaneInfos(ctx)
	if err != nil {
		return sessionStartAcknowledgementSample{}, fmt.Errorf("inspect managed agent pane: %w", err)
	}
	sample := sessionStartAcknowledgementSample{promptConsumed: consumed, identity: identity, identityFound: found}
	for _, pane := range panes {
		if strings.TrimSpace(pane.SessionName) == strings.TrimSpace(sessionID) {
			sample.pane, sample.paneFound = pane, true
			break
		}
	}
	return sample, nil
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
