package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (d *Daemon) handleRuntimeSignalIngest(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	ctx = withoutSynchronousProjectReadRuntimeRefresh(ctx)
	var cmd protocol.RuntimeSignalIngestCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	cmd = normalizeRuntimeSignalCommand(cmd)
	cmd = applyAgentTerminalFailureClassification(cmd)
	if cmd.Source == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: source"), nil
	}
	if cmd.Kind == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: kind"), nil
	}
	switch cmd.Kind {
	case protocol.RuntimeSignalKindGitWorktreeChanged, protocol.RuntimeSignalKindAgentActivityChanged:
	default:
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("unsupported runtime signal kind: %s", cmd.Kind)), nil
	}

	projectID := strings.TrimSpace(cmd.ProjectID)
	if projectID == "" {
		projectID = d.projectID(req.Meta)
	}
	out := protocol.RuntimeSignalIngestResponseBody{
		Accepted: true,
		SignalID: runtimeSignalID(projectID, cmd),
	}

	if cmd.Log {
		evt, err := runtimeSignalHookLogEvent(projectID, cmd)
		if err != nil {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "hook_log", OK: false, Message: err.Error()})
		} else if rev, _, err := d.publishHookLogEvent(projectID, evt); err != nil {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "hook_log", OK: false, Message: err.Error()})
		} else {
			out.ProjectionRevisions = appendRevision(out.ProjectionRevisions, rev)
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "hook_log", OK: true, Revision: rev})
		}
	}

	switch cmd.Kind {
	case protocol.RuntimeSignalKindGitWorktreeChanged:
		d.ingestGitWorktreeSignal(ctx, projectID, cmd, &out)
	case protocol.RuntimeSignalKindAgentActivityChanged:
		d.ingestAgentActivitySignal(ctx, req, projectID, cmd, &out)
	}

	body, err := json.Marshal(out)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal runtime signal response: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func applyAgentTerminalFailureClassification(cmd protocol.RuntimeSignalIngestCommandBody) protocol.RuntimeSignalIngestCommandBody {
	if cmd.Source != protocol.RuntimeSignalSourceAgentHook || cmd.Kind != protocol.RuntimeSignalKindAgentActivityChanged {
		return cmd
	}
	reason, ok := domain.ClassifyAgentTerminalFailure(cmd.Event, cmd.Payload)
	if !ok {
		return cmd
	}
	blocking := true
	agent := strings.TrimSpace(cmd.Agent)
	if agent == "" {
		agent = "agent"
	}
	cmd.Activity = string(domain.SessionError)
	cmd.Level = "error"
	cmd.Blocking = &blocking
	cmd.Message = fmt.Sprintf("%s hook: %s (terminal agent failure: %s)", agent, cmd.Event, reason)
	return cmd
}

func normalizeRuntimeSignalCommand(cmd protocol.RuntimeSignalIngestCommandBody) protocol.RuntimeSignalIngestCommandBody {
	cmd.Source = strings.TrimSpace(cmd.Source)
	cmd.Kind = strings.TrimSpace(cmd.Kind)
	cmd.ProjectID = strings.TrimSpace(cmd.ProjectID)
	cmd.IssueID = strings.TrimSpace(cmd.IssueID)
	cmd.SessionID = strings.TrimSpace(cmd.SessionID)
	cmd.Worktree = strings.TrimSpace(cmd.Worktree)
	cmd.TmuxPane = strings.TrimSpace(cmd.TmuxPane)
	cmd.LogicalPaneID = strings.TrimSpace(cmd.LogicalPaneID)
	cmd.AgentIncarnation = strings.TrimSpace(cmd.AgentIncarnation)
	cmd.Agent = strings.TrimSpace(cmd.Agent)
	cmd.Hook = strings.TrimSpace(cmd.Hook)
	cmd.Command = strings.TrimSpace(cmd.Command)
	cmd.Event = strings.TrimSpace(cmd.Event)
	cmd.Activity = strings.TrimSpace(cmd.Activity)
	cmd.Level = strings.TrimSpace(cmd.Level)
	cmd.Message = strings.TrimSpace(cmd.Message)
	return cmd
}

func runtimeSignalID(projectID string, cmd protocol.RuntimeSignalIngestCommandBody) string {
	type stableSignal struct {
		Source           string `json:"source"`
		Kind             string `json:"kind"`
		ProjectID        string `json:"project_id"`
		IssueID          string `json:"issue_id,omitempty"`
		SessionID        string `json:"session_id,omitempty"`
		Worktree         string `json:"worktree,omitempty"`
		TmuxPane         string `json:"tmux_pane,omitempty"`
		LogicalPaneID    string `json:"logical_pane_id,omitempty"`
		PanePID          int    `json:"pane_pid,omitempty"`
		AgentIncarnation string `json:"agent_incarnation,omitempty"`
		Agent            string `json:"agent,omitempty"`
		Hook             string `json:"hook,omitempty"`
		Event            string `json:"event,omitempty"`
		Activity         string `json:"activity,omitempty"`
	}
	data, _ := json.Marshal(stableSignal{
		Source:           cmd.Source,
		Kind:             cmd.Kind,
		ProjectID:        projectID,
		IssueID:          cmd.IssueID,
		SessionID:        cmd.SessionID,
		Worktree:         cmd.Worktree,
		TmuxPane:         cmd.TmuxPane,
		LogicalPaneID:    cmd.LogicalPaneID,
		PanePID:          cmd.PanePID,
		AgentIncarnation: cmd.AgentIncarnation,
		Agent:            cmd.Agent,
		Hook:             cmd.Hook,
		Event:            cmd.Event,
		Activity:         cmd.Activity,
	})
	sum := sha256.Sum256(data)
	return "sig-" + hex.EncodeToString(sum[:8])
}

func runtimeSignalHookLogEvent(projectID string, cmd protocol.RuntimeSignalIngestCommandBody) (protocol.HookLogEvent, error) {
	source := cmd.Source
	message := cmd.Message
	switch cmd.Source {
	case protocol.RuntimeSignalSourceGitHook:
		source = "githooks.hook"
		if message == "" {
			message = "runtime signal ingested"
		}
	case protocol.RuntimeSignalSourceAgentHook:
		if cmd.Agent != "" {
			source = cmd.Agent + ".hook"
		}
		if message == "" {
			message = "agent hook runtime signal ingested"
		}
	}
	hook := cmd.Hook
	if hook == "" {
		hook = cmd.Event
	}
	level := cmd.Level
	if level == "" {
		level = "info"
	}
	evt, err := normalizeHookLogEvent(projectID, protocol.HookLogEvent{
		ProjectID:  naming.ProjectID(projectID),
		IssueID:    naming.IssueID(cmd.IssueID),
		Hook:       hook,
		Command:    cmd.Command,
		Worktree:   cmd.Worktree,
		Source:     source,
		Level:      level,
		Message:    message,
		ElapsedMS:  cmd.ElapsedMS,
		ExitStatus: cmd.ExitStatus,
		Blocking:   cmd.Blocking,
	})
	if err != nil {
		return protocol.HookLogEvent{}, err
	}
	return evt, nil
}

func (d *Daemon) ingestGitWorktreeSignal(ctx context.Context, projectID string, cmd protocol.RuntimeSignalIngestCommandBody, out *protocol.RuntimeSignalIngestResponseBody) {
	if d.gitStatusAdapter == nil {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_refresh_queued", OK: false, Message: "git status adapter unavailable"})
		return
	}
	if cmd.Worktree == "" {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_refresh_queued", OK: false, Message: "missing worktree"})
		return
	}
	_, admitted, err := d.gitStatusAdapter.queueDurableGitHookRefresh(ctx, projectID, cmd.Worktree)
	if err != nil {
		out.Accepted = false
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_refresh_queued", OK: false, Message: err.Error()})
		return
	}
	if !admitted {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_refresh_queued", OK: true, Message: "ineligible worktree ignored"})
		return
	}
	out.EnrichmentQueued = true
	out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_refresh_queued", OK: true})
}

func (d *Daemon) ingestAgentActivitySignal(ctx context.Context, req protocol.RequestEnvelope, projectID string, cmd protocol.RuntimeSignalIngestCommandBody, out *protocol.RuntimeSignalIngestResponseBody) {
	sessionID := cmd.SessionID
	if sessionID == "" && cmd.IssueID != "" {
		sessionID = naming.CanonicalSessionID(projectID, cmd.IssueID)
	}
	if sessionID == "" {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: false, Message: "missing session_id"})
		return
	}
	if cmd.LogicalPaneID != "" || cmd.AgentIncarnation != "" {
		accepted, message, err := d.validateManagedAgentSignalIdentity(ctx, projectID, sessionID, cmd)
		if err != nil {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "managed_agent_identity", OK: false, Message: err.Error()})
			return
		}
		if !accepted {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "managed_agent_identity", OK: false, Message: message})
			return
		}
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "managed_agent_identity", OK: true, Message: message})
	}
	if cmd.IssueID == "" {
		store := d.sessionRuntimeStateStoreIfConfigured(projectID)
		if store == nil {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: false, Message: "session runtime store unavailable"})
			return
		}
		projection, found, loadErr := store.GetSessionState(ctx, projectID, sessionID)
		if loadErr != nil || !found || projection.Role != daemonstate.SessionRoleOrchestrator {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: false, Message: "missing issue_id for non-orchestrator session"})
			return
		}
	}
	command, activity, ok := runtimeSignalAgentLifecycle(cmd)
	if !ok {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: true, Message: "lifecycle neutral"})
		return
	}
	observedState, stateOK := lifecycleCommandState(command)
	if !stateOK {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: false, Message: fmt.Sprintf("unsupported observed lifecycle command %q", command)})
		return
	}
	revisions, err := d.recordPhysicalSessionObservation(ctx, req.Meta, projectID, sessionID, cmd.IssueID, observedState, activity, "hooks")
	if err != nil {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: false, Message: err.Error()})
		return
	}
	for _, rev := range revisions {
		out.ProjectionRevisions = appendRevision(out.ProjectionRevisions, rev)
	}
	rev := lastRevision(revisions)
	out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: true, Revision: rev})
	orchestratorEnded := false
	if parentSessionID, _, ok := agentScopedSessionParentAndPane(sessionID); ok {
		canonicalRev, err := d.recordAgentHookActivityEvidenceAndMaterialize(ctx, req.Meta, projectID, parentSessionID, sessionID, cmd.IssueID, activity, cmd)
		if err != nil {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity_canonical", OK: false, Message: err.Error()})
			return
		}
		out.ProjectionRevisions = appendRevision(out.ProjectionRevisions, canonicalRev)
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity_evidence", OK: true})
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity_canonical", OK: true, Revision: canonicalRev})
	}
	if strings.TrimSpace(cmd.Event) == "session_end" {
		orchestratorSessionID := sessionID
		if parentSessionID, _, ok := agentScopedSessionParentAndPane(sessionID); ok {
			orchestratorSessionID = parentSessionID
		}
		paused, pauseErr := d.pauseEndedOrchestratorSession(ctx, req.Meta, projectID, orchestratorSessionID)
		if pauseErr != nil {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "orchestrator_session_end", OK: false, Message: pauseErr.Error()})
		} else if paused {
			orchestratorEnded = true
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "orchestrator_session_end", OK: true})
		}
	}
	if !orchestratorEnded && orchestratorActivityWakeRequired(activity) {
		if err := d.reconcileOrchestratorLifecycles(ctx, projectID, time.Now().UTC()); err != nil {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "orchestrator_continuation", OK: false, Message: err.Error()})
		} else {
			out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "orchestrator_continuation", OK: true})
		}
	}
}

func (d *Daemon) validateManagedAgentSignalIdentity(ctx context.Context, projectID, sessionID string, cmd protocol.RuntimeSignalIngestCommandBody) (bool, string, error) {
	if cmd.LogicalPaneID == "" || cmd.TmuxPane == "" || cmd.AgentIncarnation == "" {
		return false, "incomplete managed agent identity", nil
	}
	if d.tmux == nil {
		return false, "tmux adapter unavailable", nil
	}
	physicalSessionID := sessionID
	if parent, _, ok := agentScopedSessionParentAndPane(sessionID); ok {
		physicalSessionID = parent
	}
	panes, err := d.tmux.ListPaneInfos(ctx)
	if err != nil {
		return false, "", fmt.Errorf("list managed agent panes: %w", err)
	}
	paneID := sanitizeRuntimePaneID(cmd.TmuxPane)
	livePanePID := 0
	for _, pane := range panes {
		if pane.SessionName == physicalSessionID && sanitizeRuntimePaneID(pane.PaneID) == paneID {
			livePanePID = pane.PanePID
			break
		}
	}
	if livePanePID <= 0 {
		return false, "managed tmux pane is not live", nil
	}
	if cmd.PanePID > 0 && cmd.PanePID != livePanePID {
		return false, "stale or reused tmux pane process", nil
	}
	identity := daemonstate.ManagedAgentIdentity{ProjectID: projectID, SessionID: physicalSessionID, LogicalPaneID: cmd.LogicalPaneID,
		TmuxPaneID: paneID, PanePID: livePanePID, AgentIncarnation: cmd.AgentIncarnation, ObservedAt: time.Now().UTC()}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return false, "session runtime store unavailable", nil
	}
	if strings.TrimSpace(cmd.Event) == "session_start" {
		if err := store.UpsertManagedAgentIdentity(ctx, identity); err != nil {
			return false, "", err
		}
		return true, "managed agent incarnation bound", nil
	}
	matched, err := store.MatchManagedAgentIdentity(ctx, identity)
	if err != nil {
		return false, "", err
	}
	if !matched {
		return false, "stale or reused managed agent incarnation", nil
	}
	return true, "managed agent incarnation matched", nil
}

// recordPhysicalSessionObservation persists one fact about the physical tmux
// runtime, then fans its observed state and activity into every logical intent
// associated with that runtime. Desired logical state and typed identity are
// deliberately preserved.
func (d *Daemon) recordPhysicalSessionObservation(ctx context.Context, meta protocol.Metadata, projectID, sessionID, _ string, observedState daemonstate.SessionState, activity, activitySource string) ([]uint64, error) {
	store := d.sessionRuntimeStateStore(projectID)
	if store == nil {
		return nil, errors.New("session runtime store unavailable")
	}
	now := time.Now().UTC()
	activity = normalizeSessionActivity(activity)
	activitySource = normalizeSessionActivitySource(activitySource, "hooks")
	if observedState == daemonstate.SessionStateStopped {
		activity, activitySource = "", ""
	}
	_, applied, revisions, err := d.runtimeProjectionStateWriter().ApplyPhysicalSessionObservationAndPublish(ctx, projectID, meta, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: sessionID, ObservedState: observedState,
		Activity: activity, ActivitySource: activitySource, UpdatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, nil
	}
	return revisions, nil
}

func (d *Daemon) recordAgentHookActivityEvidenceAndMaterialize(ctx context.Context, meta protocol.Metadata, projectID, sessionID, sourceSessionID, issueID, activity string, cmd protocol.RuntimeSignalIngestCommandBody) (uint64, error) {
	if d == nil {
		return 0, nil
	}
	activity = normalizeSessionActivity(activity)
	if activity == "" {
		return 0, nil
	}
	now := time.Now().UTC()
	evidence := daemonstate.SessionActivityEvidence{
		ProjectID:       projectID,
		SessionID:       strings.TrimSpace(sessionID),
		IssueID:         strings.TrimSpace(issueID),
		Activity:        activity,
		ActivitySource:  "hooks",
		SourceSessionID: strings.TrimSpace(sourceSessionID),
		Agent:           strings.TrimSpace(cmd.Agent),
		Hook:            strings.TrimSpace(cmd.Hook),
		Event:           strings.TrimSpace(cmd.Event),
		ObservedAt:      now,
		UpdatedAt:       now,
	}
	store := d.sessionRuntimeStateStore(projectID)
	if store == nil {
		return 0, nil
	}
	if err := store.UpsertSessionActivityEvidence(ctx, evidence); err != nil {
		return 0, err
	}
	aggregate, found, err := store.GetSessionActivityEvidence(ctx, projectID, sessionID)
	if err != nil {
		return 0, err
	}
	if found {
		evidence = aggregate
	}
	return d.applySessionActivityEvidenceToCanonicalSession(ctx, meta, evidence)
}

func (d *Daemon) applySessionActivityEvidenceToCanonicalSession(ctx context.Context, meta protocol.Metadata, evidence daemonstate.SessionActivityEvidence) (uint64, error) {
	if d == nil {
		return 0, nil
	}
	projectID := strings.TrimSpace(evidence.ProjectID)
	sessionID := strings.TrimSpace(evidence.SessionID)
	issueID := strings.TrimSpace(evidence.IssueID)
	activity := normalizeSessionActivity(evidence.Activity)
	activitySource := normalizeSessionActivitySource(evidence.ActivitySource, "hooks")
	if projectID == "" || sessionID == "" || issueID == "" || activity == "" {
		return 0, nil
	}

	revisions, err := d.recordPhysicalSessionObservation(ctx, meta, projectID, sessionID, issueID, daemonstate.SessionStateRunning, activity, activitySource)
	return lastRevision(revisions), err
}

func (d *Daemon) materializeSessionActivityEvidence(ctx context.Context, meta protocol.Metadata, projectID string, issueIDs []string) error {
	if d == nil {
		return nil
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	evidenceRows, err := store.ListSessionActivityEvidence(ctx, projectID, issueIDs)
	if err != nil {
		return fmt.Errorf("load session activity evidence: %w", err)
	}
	for _, evidence := range daemonstate.AggregateSessionActivityEvidence(evidenceRows) {
		if _, err := d.applySessionActivityEvidenceToCanonicalSession(ctx, meta, evidence); err != nil {
			return fmt.Errorf("materialize session activity evidence %s/%s: %w", projectID, evidence.SessionID, err)
		}
	}
	return nil
}

func runtimeSignalAgentLifecycle(cmd protocol.RuntimeSignalIngestCommandBody) (string, string, bool) {
	switch normalizeSessionActivity(cmd.Activity) {
	case "busy":
		return daemonhandlers.CommandSessionResume, "busy", true
	case "idle":
		return daemonhandlers.CommandSessionPause, "idle", true
	case "waiting":
		return daemonhandlers.CommandSessionPause, "waiting", true
	case "error":
		return daemonhandlers.CommandSessionPause, "error", true
	}
	switch strings.TrimSpace(cmd.Event) {
	case "idle_prompt", "permission_request":
		return daemonhandlers.CommandSessionPause, "waiting", true
	case "session_end":
		if cmd.ExitStatus != nil && *cmd.ExitStatus != 0 {
			return daemonhandlers.CommandSessionPause, "error", true
		}
		return daemonhandlers.CommandSessionPause, "idle", true
	case "stop", "subagent_stop":
		return daemonhandlers.CommandSessionPause, "idle", true
	case "session_start", "subagent_start", "user_prompt_submit", "pre_tool_use":
		return daemonhandlers.CommandSessionResume, "busy", true
	default:
		return "", "", false
	}
}

func appendRevision(revisions []uint64, rev uint64) []uint64 {
	if rev == 0 {
		return revisions
	}
	return append(revisions, rev)
}

func lastRevision(revisions []uint64) uint64 {
	if len(revisions) == 0 {
		return 0
	}
	return revisions[len(revisions)-1]
}
