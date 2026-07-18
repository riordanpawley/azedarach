package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	defaultOrchestratorStopGracePeriod  = 2 * time.Second
	defaultOrchestratorStopPollInterval = 100 * time.Millisecond
)

func (d *Daemon) handleOrchestratorSession(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.OrchestratorSessionRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	projectID := d.projectID(req.Meta)
	identity, err := domain.NewOrchestratorIdentity(projectID, body.Scope)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil || d.tmux == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "orchestrator session runtime unavailable"), nil
	}
	if orchestratorSessionCommandMutates(req.Command) {
		var response protocol.ResponseEnvelope
		var commandErr error
		lockErr := store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
			response, commandErr = d.handleOrchestratorSessionLocked(lockCtx, req, body, projectID, identity, store)
			return nil
		})
		if lockErr != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("serialize orchestrator scope transition: %v", lockErr)), nil
		}
		return response, commandErr
	}
	return d.handleOrchestratorSessionLocked(ctx, req, body, projectID, identity, store)
}

func orchestratorSessionCommandMutates(command string) bool {
	switch command {
	case protocol.CommandOrchestratorSessionStart, protocol.CommandOrchestratorSessionStop, protocol.CommandOrchestratorSessionAttach:
		return true
	default:
		return false
	}
}

func (d *Daemon) handleOrchestratorSessionLocked(ctx context.Context, req protocol.RequestEnvelope, body protocol.OrchestratorSessionRequest, projectID string, identity domain.OrchestratorIdentity, store *daemonstate.RuntimeStateStore) (protocol.ResponseEnvelope, error) {
	var err error
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	sessionID := d.orchestratorSessionID(projectID, body.Scope)
	result := protocol.OrchestratorSessionResult{Scope: body.Scope, SessionID: sessionID}
	switch req.Command {
	case protocol.CommandOrchestratorSessionStart:
		rootedPrompt := ""
		if body.Scope.Kind == domain.OrchestrationScopeRooted {
			rootedPrompt, err = d.rootedOrchestratorBootstrapPrompt(ctx, projectID, body.Scope)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
		}
		var previousRootedProjection daemonstate.Session
		previousRootedProjectionFound := false
		if body.Scope.Kind == domain.OrchestrationScopeRooted {
			previousRootedProjection, previousRootedProjectionFound, err = store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, body.Scope.RootIssueID.String())
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("load rooted orchestrator projection before transition: %v", err)), nil
			}
		}
		var acquired daemonstate.OrchestratorLeaseAcquireResult
		var acquireErr error
		if body.Scope.Kind == domain.OrchestrationScopeRooted {
			startingProjection := previousRootedProjection
			if !previousRootedProjectionFound {
				startingProjection = daemonstate.Session{ID: sessionID, IssueID: body.Scope.RootIssueID.String()}
			}
			startingProjection.ID = sessionID
			startingProjection.IssueID = body.Scope.RootIssueID.String()
			startingProjection.Role = daemonstate.SessionRoleOrchestrator
			startingProjection.ScopeKind = daemonstate.SessionScopeOrchestration
			startingProjection.ScopeID = body.Scope.RootIssueID.String()
			startingProjection.State = daemonstate.SessionStateStarting
			startingProjection.UpdatedAt = time.Now().UTC()
			acquired, acquireErr = authority.AcquireRooted(ctx, identity, startingProjection, d.tmux.HasSession)
		} else {
			acquired, acquireErr = authority.Acquire(ctx, identity, sessionID, d.tmux.HasSession)
		}
		if acquireErr != nil {
			code := protocol.ErrorCodeInternal
			if errors.Is(acquireErr, daemonstate.ErrOrchestratorLeaseConflict) {
				code = protocol.ErrorCodeConflict
			}
			return d.errorResponse(req, code, acquireErr.Error()), nil
		}
		result.Disposition = string(acquired.Disposition)
		result.Lifecycle = acquired.Lease.Lifecycle
		preserveLeaseOnFailure := acquired.Disposition == daemonstate.OrchestratorLeaseAttached
		previousLifecycle := acquired.Lease.Lifecycle
		restoreRootedProjectionAfterFailure := func() {
			if body.Scope.Kind != domain.OrchestrationScopeRooted {
				return
			}
			if previousRootedProjectionFound {
				_ = d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, previousRootedProjection)
				return
			}
			_ = d.transitionRootedOrchestratorSessionIntent(ctx, projectID, body.Scope.RootIssueID.String(), acquired.Lease.SessionID, daemonstate.SessionStateStopped)
		}
		restoreLeaseAfterProbeFailure := func() {
			restoreRootedProjectionAfterFailure()
			if preserveLeaseOnFailure {
				_, _ = authority.SetLifecycle(ctx, identity, acquired.Lease.SessionID, previousLifecycle)
				return
			}
			_ = authority.Release(ctx, identity, acquired.Lease.SessionID)
		}
		pauseOrReleaseLease := func() {
			restoreRootedProjectionAfterFailure()
			if preserveLeaseOnFailure {
				_, _ = authority.SetLifecycle(ctx, identity, acquired.Lease.SessionID, domain.OrchestratorPaused)
				return
			}
			_ = authority.Release(ctx, identity, acquired.Lease.SessionID)
		}
		if acquired.Lease.Lifecycle == domain.OrchestratorPaused {
			acquired.Lease, err = authority.SetLifecycle(ctx, identity, acquired.Lease.SessionID, domain.OrchestratorWorking)
			if err != nil {
				restoreRootedProjectionAfterFailure()
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("resume orchestrator session lease: %v", err)), nil
			}
			result.Disposition = "resumed"
			result.Lifecycle = acquired.Lease.Lifecycle
		}
		launchedHere := false
		live, probeErr := d.tmux.HasSession(ctx, acquired.Lease.SessionID)
		if probeErr != nil {
			restoreLeaseAfterProbeFailure()
			return d.errorResponse(req, protocol.ErrorCodeInternal, probeErr.Error()), nil
		}
		if !live {
			if acquired.Disposition == daemonstate.OrchestratorLeaseAttached && result.Disposition != "resumed" {
				result.Disposition = string(daemonstate.OrchestratorLeaseRecoveredStale)
			}
			if body.Scope.Kind == domain.OrchestrationScopeRooted {
				startBody, _ := json.Marshal(sessionCommandBody{ProjectID: projectID, IssueID: body.Scope.RootIssueID.String(), SessionID: acquired.Lease.SessionID, Prompt: rootedPrompt})
				startReq := req
				startReq.Command, startReq.Body = "session.start", startBody
				startResp, startErr := d.handleSessionStartDirectWithOptions(ctx, startReq, sessionStartOptions{
					intent: sessionIntentSelector{
						Role:      daemonstate.SessionRoleOrchestrator,
						ScopeKind: daemonstate.SessionScopeOrchestration,
						ScopeID:   body.Scope.RootIssueID.String(),
					},
					rootedOrchestrator: true,
				})
				if startErr != nil || startResp.Error != nil {
					appeared, _ := d.tmux.HasSession(ctx, acquired.Lease.SessionID)
					if appeared {
						result.Disposition = string(daemonstate.OrchestratorLeaseAttached)
						live = true
					} else {
						pauseOrReleaseLease()
						if startErr != nil {
							return d.errorResponse(req, protocol.ErrorCodeInternal, startErr.Error()), nil
						}
						return d.errorResponse(req, startResp.Error.Code, startResp.Error.Message), nil
					}
				} else {
					launchedHere = true
				}
			} else {
				workdir := d.resolveRepoDirForProject(projectID)
				prompt := "You are the project orchestrator for this Azedarach project. Run `az prime`, then remain in the active orchestration loop until project completion."
				artifact, artifactErr := d.prepareSessionLaunchArtifact(sessionLaunchSpec{Mode: sessionLaunchInitial, ProjectID: projectID, SessionID: acquired.Lease.SessionID, Prompt: prompt})
				if artifactErr != nil {
					pauseOrReleaseLease()
					return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("prepare project orchestrator launch artifact: %v", artifactErr)), nil
				}
				if launchErr := d.tmux.NewSessionWithCommandAndEnvironment(ctx, acquired.Lease.SessionID, workdir, artifact.Command, nil); launchErr != nil {
					appeared, _ := d.tmux.HasSession(ctx, acquired.Lease.SessionID)
					if !appeared {
						artifact.remove()
						pauseOrReleaseLease()
						return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("start project orchestrator session: %v", launchErr)), nil
					}
					result.Disposition = string(daemonstate.OrchestratorLeaseAttached)
					live = true
				} else {
					launchedHere = true
				}
				if err := d.setSessionContextEnv(ctx, projectID, "", acquired.Lease.SessionID); err != nil {
					if launchedHere {
						_ = d.tmux.KillSession(ctx, acquired.Lease.SessionID)
						pauseOrReleaseLease()
					}
					return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("set project orchestrator session context: %v", err)), nil
				}
			}
		}
		if body.Scope.Kind == domain.OrchestrationScopeRooted {
			bootstrapDisposition, bootstrapErr := d.ensureRootedOrchestratorBootstrap(ctx, projectID, body.Scope, acquired.Lease.SessionID, rootedPrompt, launchedHere)
			err = bootstrapErr
			if err != nil {
				if launchedHere {
					_ = d.tmux.KillSession(ctx, acquired.Lease.SessionID)
					pauseOrReleaseLease()
				} else {
					restoreLeaseAfterProbeFailure()
				}
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
			if d.cfg.Logger != nil {
				d.cfg.Logger.Info("rooted orchestrator bootstrap confirmed", "project_id", projectID, "root_id", body.Scope.RootIssueID, "session_id", acquired.Lease.SessionID, "disposition", bootstrapDisposition)
			}
		}
		if err := d.persistOrchestratorSessionProjection(ctx, req.Meta, projectID, body.Scope, acquired.Lease.SessionID); err != nil {
			if launchedHere {
				_ = d.tmux.KillSession(ctx, acquired.Lease.SessionID)
				pauseOrReleaseLease()
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		acquired.Lease, err = authority.SetLifecycle(ctx, identity, acquired.Lease.SessionID, domain.OrchestratorWorking)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("finalize orchestrator session start: %v", err)), nil
		}
		result.Lifecycle = acquired.Lease.Lifecycle
		result.Live = true
	case protocol.CommandOrchestratorSessionStop:
		lease, found, loadErr := authority.Get(ctx, identity)
		if loadErr != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, loadErr.Error()), nil
		}
		if !found {
			result.Disposition = "not-found"
			break
		}
		if expected := strings.TrimSpace(body.ExpectedSessionID); expected != "" && expected != lease.SessionID {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("orchestrator scope belongs to session %s, not expected session %s", lease.SessionID, expected)), nil
		}
		result.SessionID, result.Lifecycle = lease.SessionID, lease.Lifecycle
		result.Live, err = d.tmux.HasSession(ctx, lease.SessionID)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		if lease.Lifecycle == domain.OrchestratorPaused && !result.Live {
			if err := d.persistStoppedOrchestratorSessionProjection(ctx, req.Meta, projectID, body.Scope, lease.SessionID, daemonstate.SessionStateStopped); err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
			result.Disposition = "already-stopped"
			break
		}
		lease, err = authority.SetLifecycle(ctx, identity, lease.SessionID, domain.OrchestratorPaused)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("pause orchestrator session lease: %v", err)), nil
		}
		result.Lifecycle = lease.Lifecycle
		observed := daemonstate.SessionStateStopped
		if result.Live {
			observed = daemonstate.SessionStateRunning
		}
		if err := d.persistStoppedOrchestratorSessionProjection(ctx, req.Meta, projectID, body.Scope, lease.SessionID, observed); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		if d.orchestratorStopAfterIntentPersisted != nil {
			d.orchestratorStopAfterIntentPersisted()
		}
		if result.Live {
			result.Forced, err = d.gracefullyStopOrchestratorRuntime(ctx, lease.SessionID)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("stop orchestrator session runtime: %v", err)), nil
			}
		}
		// Win any concurrent attach/resume transition that raced with graceful
		// shutdown. Once runtime cleanup has completed, the exact scope must
		// durably agree that it is paused.
		lease, err = authority.SetLifecycle(ctx, identity, lease.SessionID, domain.OrchestratorPaused)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("finalize orchestrator session pause: %v", err)), nil
		}
		result.Lifecycle = lease.Lifecycle
		if err := d.persistStoppedOrchestratorSessionProjection(ctx, req.Meta, projectID, body.Scope, lease.SessionID, daemonstate.SessionStateStopped); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		result.Live = false
		result.Disposition = "stopped"
		if result.Forced {
			result.Disposition = "stopped-forced"
		}
	case protocol.CommandOrchestratorSessionAttach, protocol.CommandOrchestratorSessionStatus:
		lease, found, loadErr := authority.Get(ctx, identity)
		if loadErr != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, loadErr.Error()), nil
		}
		if !found && req.Command == protocol.CommandOrchestratorSessionStatus {
			result.Disposition = "not-found"
			break
		}
		if !found {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "orchestrator session not found for scope"), nil
		}
		result.SessionID, result.Lifecycle = lease.SessionID, lease.Lifecycle
		result.Live, err = d.tmux.HasSession(ctx, lease.SessionID)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		if req.Command == protocol.CommandOrchestratorSessionAttach {
			if !result.Live {
				return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "orchestrator session runtime is not live"), nil
			}
			lease, err = authority.SetLifecycle(ctx, identity, lease.SessionID, domain.OrchestratorWorking)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record orchestrator session attach transition: %v", err)), nil
			}
			result.Lifecycle = lease.Lifecycle
			result.Disposition = "attached"
			if body.Scope.Kind == domain.OrchestrationScopeRooted {
				if err := d.persistOrchestratorSessionProjection(ctx, req.Meta, projectID, body.Scope, lease.SessionID); err != nil {
					return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("persist rooted orchestrator attach transition: %v", err)), nil
				}
			}
		} else if !result.Live && lease.Lifecycle != domain.OrchestratorPaused {
			result.Disposition = "stale-runtime"
		}
	}
	encoded, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, encodeErr.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = encoded
	return resp, nil
}

func (d *Daemon) gracefullyStopOrchestratorRuntime(ctx context.Context, sessionID string) (bool, error) {
	grace := d.orchestratorStopGracePeriod
	if grace <= 0 {
		grace = defaultOrchestratorStopGracePeriod
	}
	if err := d.tmux.PasteTextAndSubmit(ctx, sessionID, "/exit"); err == nil {
		stopped, waitErr := d.waitForOrchestratorRuntimeExit(ctx, sessionID, grace)
		if waitErr != nil {
			return false, waitErr
		}
		if stopped {
			return false, nil
		}
	}
	if err := d.tmux.SendKeys(ctx, sessionID, "exit"); err == nil {
		stopped, waitErr := d.waitForOrchestratorRuntimeExit(ctx, sessionID, grace)
		if waitErr != nil {
			return false, waitErr
		}
		if stopped {
			return false, nil
		}
	}
	if err := d.tmux.KillSession(ctx, sessionID); err != nil {
		live, probeErr := d.tmux.HasSession(ctx, sessionID)
		if probeErr == nil && !live {
			return true, nil
		}
		return true, err
	}
	live, err := d.tmux.HasSession(ctx, sessionID)
	if err != nil {
		return true, err
	}
	if live {
		return true, fmt.Errorf("tmux session %s remained live after forced cleanup", sessionID)
	}
	return true, nil
}

func (d *Daemon) waitForOrchestratorRuntimeExit(ctx context.Context, sessionID string, timeout time.Duration) (bool, error) {
	poll := d.orchestratorStopPollInterval
	if poll <= 0 {
		poll = defaultOrchestratorStopPollInterval
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		live, err := d.tmux.HasSession(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if !live {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func (d *Daemon) persistStoppedOrchestratorSessionProjection(ctx context.Context, meta protocol.Metadata, projectID string, scope domain.OrchestrationScope, sessionID string, observed daemonstate.SessionState) error {
	projection, found, err := d.sessionRuntimeStateStoreIfConfigured(projectID).GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, orchestrationScopeID(scope))
	if err != nil {
		return fmt.Errorf("load orchestrator session projection for stop: %w", err)
	}
	if !found {
		projection = daemonstate.Session{ID: sessionID, IssueID: scope.RootIssueID.String()}
	}
	projection.ID = sessionID
	projection.Role = daemonstate.SessionRoleOrchestrator
	projection.ScopeKind = daemonstate.SessionScopeOrchestration
	projection.ScopeID = orchestrationScopeID(scope)
	projection.State = daemonstate.SessionStateStopped
	projection.ObservedState = observed
	projection.Activity, projection.ActivitySource = "", ""
	projection.TmuxAttachedCount = 0
	updatedAt := time.Now().UTC()
	if projection.UpdatedAt.After(updatedAt) {
		updatedAt = projection.UpdatedAt
	}
	projection.UpdatedAt = updatedAt
	writer := d.runtimeProjectionStateWriter()
	if err := writer.PersistSessionProjection(ctx, projectID, projection); err != nil {
		return fmt.Errorf("persist stopped orchestrator session projection: %w", err)
	}
	if _, err := writer.PublishSessionProjectionEvent(ctx, projectID, meta, projection); err != nil {
		return fmt.Errorf("publish stopped orchestrator session projection: %w", err)
	}
	if err := d.persistObservedRuntimeProjection(ctx, projectID, meta, projection); err != nil {
		return fmt.Errorf("persist stopped orchestrator runtime observation: %w", err)
	}
	return nil
}

func (d *Daemon) transitionRootedOrchestratorSessionIntent(ctx context.Context, projectID, rootID, sessionID string, state daemonstate.SessionState) error {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	projection, found, err := store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, rootID)
	if err != nil {
		return err
	}
	if !found {
		projection = daemonstate.Session{ID: sessionID, IssueID: rootID}
	}
	projection.ID = sessionID
	projection.IssueID = rootID
	projection.Role = daemonstate.SessionRoleOrchestrator
	projection.ScopeKind = daemonstate.SessionScopeOrchestration
	projection.ScopeID = rootID
	projection.State = state
	projection.UpdatedAt = time.Now().UTC()
	return d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, projection)
}

func (d *Daemon) pauseEndedOrchestratorSession(ctx context.Context, meta protocol.Metadata, projectID, sessionID string) (bool, error) {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return false, nil
	}
	projection, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found {
		return false, err
	}
	if projection.Role != daemonstate.SessionRoleOrchestrator || projection.ScopeKind != daemonstate.SessionScopeOrchestration {
		return false, nil
	}
	var scope domain.OrchestrationScope
	if projection.ScopeID == string(domain.OrchestrationScopeProject) {
		scope = domain.ProjectOrchestrationScope()
	} else {
		scope, err = domain.RootedOrchestrationScope(projection.ScopeID)
		if err != nil {
			return false, err
		}
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		return false, err
	}
	paused := false
	err = store.WithOrchestratorScopeTransition(ctx, identity, func(scopeCtx context.Context) error {
		currentProjection, currentFound, loadErr := store.GetSessionState(scopeCtx, projectID, sessionID)
		if loadErr != nil || !currentFound || currentProjection.Role != daemonstate.SessionRoleOrchestrator || currentProjection.ScopeKind != daemonstate.SessionScopeOrchestration || currentProjection.ScopeID != projection.ScopeID {
			return loadErr
		}
		authority := daemonstate.NewOrchestratorLeaseAuthority(store)
		lease, leaseFound, leaseErr := authority.Get(scopeCtx, identity)
		if leaseErr != nil || !leaseFound || lease.SessionID != sessionID {
			return leaseErr
		}
		if lease.Lifecycle != domain.OrchestratorPaused {
			if _, leaseErr = authority.SetLifecycle(scopeCtx, identity, sessionID, domain.OrchestratorPaused); leaseErr != nil {
				return leaseErr
			}
		}
		if persistErr := d.persistStoppedOrchestratorSessionProjection(scopeCtx, meta, projectID, scope, sessionID, daemonstate.SessionStatePaused); persistErr != nil {
			return persistErr
		}
		paused = true
		return nil
	})
	return paused, err
}

func (d *Daemon) persistOrchestratorSessionProjection(ctx context.Context, meta protocol.Metadata, projectID string, scope domain.OrchestrationScope, sessionID string) error {
	projection, found, err := d.sessionRuntimeStateStoreIfConfigured(projectID).GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, orchestrationScopeID(scope))
	if err != nil {
		return fmt.Errorf("load orchestrator session projection: %w", err)
	}
	if !found {
		projection = daemonstate.Session{ID: sessionID, IssueID: scope.RootIssueID.String(), State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "runtime", UpdatedAt: time.Now().UTC()}
	}
	projection.Role = daemonstate.SessionRoleOrchestrator
	projection.ScopeKind = daemonstate.SessionScopeOrchestration
	projection.ScopeID = orchestrationScopeID(scope)
	projection.State = daemonstate.SessionStateRunning
	projection.ObservedState = daemonstate.SessionStateRunning
	projection.UpdatedAt = time.Now().UTC()
	writer := d.runtimeProjectionStateWriter()
	if err := writer.PersistSessionProjection(ctx, projectID, projection); err != nil {
		return fmt.Errorf("persist orchestrator session projection: %w", err)
	}
	return d.persistObservedRuntimeProjection(ctx, projectID, meta, projection)
}

func (d *Daemon) orchestratorSessionID(projectID string, scope domain.OrchestrationScope) string {
	scopeID := "project"
	namingScope := d.resolveRepoDirForProject(projectID)
	if scope.Kind == domain.OrchestrationScopeRooted {
		return naming.CanonicalSessionID(namingScope, scope.RootIssueID.String())
	}
	return naming.CanonicalSessionID(namingScope, "orchestrator-"+scopeID)
}

func orchestrationScopeID(scope domain.OrchestrationScope) string {
	if scope.Kind == domain.OrchestrationScopeRooted {
		return scope.RootIssueID.String()
	}
	return string(domain.OrchestrationScopeProject)
}
