package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
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
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	sessionID := d.orchestratorSessionID(projectID, body.Scope)
	result := protocol.OrchestratorSessionResult{Scope: body.Scope, SessionID: sessionID}
	switch req.Command {
	case protocol.CommandOrchestratorSessionStart:
		acquired, acquireErr := authority.Acquire(ctx, identity, sessionID, d.tmux.HasSession)
		if acquireErr != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, acquireErr.Error()), nil
		}
		result.Disposition = string(acquired.Disposition)
		result.Lifecycle = acquired.Lease.Lifecycle
		launchedHere := false
		live, probeErr := d.tmux.HasSession(ctx, acquired.Lease.SessionID)
		if probeErr != nil {
			if acquired.Disposition != daemonstate.OrchestratorLeaseAttached {
				_ = authority.Release(ctx, identity, acquired.Lease.SessionID)
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, probeErr.Error()), nil
		}
		if !live {
			if acquired.Disposition == daemonstate.OrchestratorLeaseAttached {
				result.Disposition = string(daemonstate.OrchestratorLeaseRecoveredStale)
			}
			if body.Scope.Kind == domain.OrchestrationScopeRooted {
				startBody, _ := json.Marshal(sessionCommandBody{ProjectID: projectID, SessionID: acquired.Lease.SessionID})
				startReq := req
				startReq.Command, startReq.Body = "session.start", startBody
				startResp, startErr := d.handleSessionStartDirect(ctx, startReq)
				if startErr != nil || startResp.Error != nil {
					appeared, _ := d.tmux.HasSession(ctx, acquired.Lease.SessionID)
					if appeared {
						result.Disposition = string(daemonstate.OrchestratorLeaseAttached)
						live = true
					} else {
						_ = authority.Release(ctx, identity, acquired.Lease.SessionID)
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
				command := d.buildSessionLaunchCommand(projectID, "", acquired.Lease.SessionID, false, nil, prompt)
				if launchErr := d.tmux.NewSessionWithCommand(ctx, acquired.Lease.SessionID, workdir, command); launchErr != nil {
					appeared, _ := d.tmux.HasSession(ctx, acquired.Lease.SessionID)
					if !appeared {
						_ = authority.Release(ctx, identity, acquired.Lease.SessionID)
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
						_ = authority.Release(ctx, identity, acquired.Lease.SessionID)
					}
					return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("set project orchestrator session context: %v", err)), nil
				}
			}
		}
		if err := d.persistOrchestratorSessionProjection(ctx, req.Meta, projectID, body.Scope, acquired.Lease.SessionID); err != nil {
			if launchedHere && body.Scope.Kind == domain.OrchestrationScopeProject {
				_ = d.tmux.KillSession(ctx, acquired.Lease.SessionID)
				_ = authority.Release(ctx, identity, acquired.Lease.SessionID)
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		result.Live = true
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
	writer.PublishSessionProjectionEvent(ctx, projectID, meta, projection)
	return nil
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
