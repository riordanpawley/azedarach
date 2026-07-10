package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (d *Daemon) ensureAdvisorSessionRuntime(ctx context.Context, projectID string, request domain.InteractionRequest) (daemonstate.AdvisorSession, bool, error) {
	if d == nil || d.tmux == nil {
		return daemonstate.AdvisorSession{}, false, fmt.Errorf("advisor tmux runtime unavailable")
	}
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStore(projectID)
	if store == nil {
		return daemonstate.AdvisorSession{}, false, fmt.Errorf("advisor session runtime store unavailable for project %s", projectID)
	}
	sessionID := advisorSessionID(request.ID)
	workdir := strings.TrimSpace(d.resolveRepoDirForProject(projectID))
	if workdir == "" {
		return daemonstate.AdvisorSession{}, false, fmt.Errorf("advisor project workdir unavailable for project %s", projectID)
	}
	prompt := buildAdvisorSessionPrompt(request)
	projection := daemonstate.Session{ID: sessionID, IssueID: request.IssueID, Role: daemonstate.SessionRoleAdvisor, ScopeKind: daemonstate.SessionScopeInteraction, ScopeID: request.ID, State: daemonstate.SessionStateStarting, ObservedState: daemonstate.SessionStateStarting, UpdatedAt: time.Now().UTC()}
	if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, projection); err != nil {
		return daemonstate.AdvisorSession{}, false, err
	}
	advisor, attached, err := store.EnsureAdvisorSession(ctx, projectID, request.ID, request.IssueID, sessionID,
		func(ctx context.Context, sessionID string) (bool, error) { return d.tmux.HasSession(ctx, sessionID) },
		func(ctx context.Context, advisor daemonstate.AdvisorSession) error {
			return d.tmux.NewSessionWithCommand(ctx, advisor.SessionID, workdir, d.buildAdvisorLaunchCommand(projectID, advisor, prompt))
		})
	if err != nil {
		return advisor, attached, err
	}
	projection.ID, projection.IssueID, projection.ScopeID = advisor.SessionID, advisor.IssueID, advisor.RequestID
	projection.State, projection.ObservedState, projection.Activity, projection.ActivitySource, projection.UpdatedAt = daemonstate.SessionStateRunning, daemonstate.SessionStateRunning, "busy", "runtime", time.Now().UTC()
	d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(protocol.NormalizeProjectID(projectID))}, projection)
	return advisor, attached, nil
}

func (d *Daemon) buildAdvisorLaunchCommand(projectID string, advisor daemonstate.AdvisorSession, prompt string) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	toolCommand := d.buildCLIToolCommand(projectID, "", advisor.SessionID, false, nil, prompt)
	toolCommand = "AZEDARACH_SESSION_ROLE=advisor AZEDARACH_INTERACTION_ID=" + singleQuoteForShell(advisor.RequestID) + " " + toolCommand
	shell := strings.TrimSpace(projectCfg.SessionShell)
	if shell == "" {
		shell = "zsh"
	}
	inner := toolCommand + "; " + sessionAgentProcessExitCommand(projectCfg.CLITool) + "; exec " + shell
	return fmt.Sprintf("%s -i -c %s", shell, singleQuoteForShell(inner))
}

func buildAdvisorSessionPrompt(request domain.InteractionRequest) string {
	return fmt.Sprintf("Role: advisor\nInteraction request: %s\nAttached issue: %s\nQuestion: %s\nWhy this decision is needed: %s\nContext: %s\n\nYou are read-only. Do not claim implementation work, edit repository files, mutate issue/spec/decision state, resolve or withdraw the interaction, or exercise orchestrator authority. Discuss the decision with the human and use only interaction.propose when asked to submit a proposal.", request.ID, request.IssueID, request.Question, request.Why, request.Context)
}

// cleanupAdvisorSessionRuntime owns only advisor runtime/projection resources;
// it deliberately never reads or mutates the interaction request.
func (d *Daemon) cleanupAdvisorSessionRuntime(ctx context.Context, projectID, requestID string) error {
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStore(projectID)
	if store == nil {
		return fmt.Errorf("advisor session runtime store unavailable for project %s", projectID)
	}
	advisor, found, err := store.GetAdvisorSession(ctx, projectID, requestID)
	if err != nil || !found {
		return err
	}
	if d.tmux != nil {
		live, probeErr := d.tmux.HasSession(ctx, advisor.SessionID)
		if probeErr != nil {
			return probeErr
		}
		if live {
			if killErr := d.tmux.KillSession(ctx, advisor.SessionID); killErr != nil {
				return killErr
			}
		}
	}
	projection, projected, err := store.GetSessionState(ctx, projectID, advisor.SessionID)
	if err != nil {
		return err
	}
	if projected {
		projection.State, projection.ObservedState, projection.Activity, projection.ActivitySource, projection.UpdatedAt = daemonstate.SessionStateStopped, daemonstate.SessionStateStopped, "", "", time.Now().UTC()
		d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(ctx, projectID, protocol.Metadata{ProjectID: naming.ProjectID(protocol.NormalizeProjectID(projectID))}, projection)
	}
	return store.DeleteAdvisorSession(ctx, projectID, requestID)
}
