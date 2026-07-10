package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const runtimeSignalFastGitStatusTimeout = 1500 * time.Millisecond

func (d *Daemon) handleRuntimeSignalIngest(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.RuntimeSignalIngestCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	cmd = normalizeRuntimeSignalCommand(cmd)
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

func normalizeRuntimeSignalCommand(cmd protocol.RuntimeSignalIngestCommandBody) protocol.RuntimeSignalIngestCommandBody {
	cmd.Source = strings.TrimSpace(cmd.Source)
	cmd.Kind = strings.TrimSpace(cmd.Kind)
	cmd.ProjectID = strings.TrimSpace(cmd.ProjectID)
	cmd.IssueID = strings.TrimSpace(cmd.IssueID)
	cmd.SessionID = strings.TrimSpace(cmd.SessionID)
	cmd.Worktree = strings.TrimSpace(cmd.Worktree)
	cmd.TmuxPane = strings.TrimSpace(cmd.TmuxPane)
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
		Source    string `json:"source"`
		Kind      string `json:"kind"`
		ProjectID string `json:"project_id"`
		IssueID   string `json:"issue_id,omitempty"`
		SessionID string `json:"session_id,omitempty"`
		Worktree  string `json:"worktree,omitempty"`
		TmuxPane  string `json:"tmux_pane,omitempty"`
		Agent     string `json:"agent,omitempty"`
		Hook      string `json:"hook,omitempty"`
		Event     string `json:"event,omitempty"`
		Activity  string `json:"activity,omitempty"`
	}
	data, _ := json.Marshal(stableSignal{
		Source:    cmd.Source,
		Kind:      cmd.Kind,
		ProjectID: projectID,
		IssueID:   cmd.IssueID,
		SessionID: cmd.SessionID,
		Worktree:  cmd.Worktree,
		TmuxPane:  cmd.TmuxPane,
		Agent:     cmd.Agent,
		Hook:      cmd.Hook,
		Event:     cmd.Event,
		Activity:  cmd.Activity,
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
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_fast", OK: false, Message: "git status adapter unavailable"})
		return
	}
	if cmd.Worktree == "" {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_fast", OK: false, Message: "missing worktree"})
		return
	}
	statusCtx, cancel := context.WithTimeout(ctx, runtimeSignalFastGitStatusTimeout)
	defer cancel()
	_, rev, err := d.gitStatusAdapter.refreshGitStatusPorcelainWriteThroughResult(statusCtx, projectID, cmd.Worktree, true, true)
	if err != nil {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_fast", OK: false, Message: err.Error()})
		return
	}
	out.ProjectionRevisions = appendRevision(out.ProjectionRevisions, rev)
	out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_fast", OK: true, Revision: rev})
	if _, err := d.gitStatusAdapter.queueGitStatusRefresh(projectID, cmd.Worktree, reconcilePriorityBackground, "runtime-signal-enrichment"); err != nil {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_enrichment", OK: false, Message: err.Error()})
		return
	}
	out.EnrichmentQueued = true
	out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "git_status_enrichment", OK: true})
}

func (d *Daemon) ingestAgentActivitySignal(ctx context.Context, req protocol.RequestEnvelope, projectID string, cmd protocol.RuntimeSignalIngestCommandBody, out *protocol.RuntimeSignalIngestResponseBody) {
	sessionID := cmd.SessionID
	if sessionID == "" && cmd.IssueID != "" {
		sessionID = naming.CanonicalSessionID(projectID, cmd.IssueID)
	}
	if sessionID == "" || cmd.IssueID == "" {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: false, Message: "missing issue_id or session_id"})
		return
	}
	command, activity, ok := runtimeSignalAgentLifecycle(cmd)
	if !ok {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: true, Message: "lifecycle neutral"})
		return
	}
	before := d.currentRevision(projectID)
	if err := d.applySessionLifecycleTransitionWithActivity(ctx, req, projectID, sessionID, cmd.IssueID, command, activity, "hooks"); err != nil {
		out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: false, Message: err.Error()})
		return
	}
	after := d.currentRevision(projectID)
	rev := after
	if rev == before {
		rev = 0
	}
	out.ProjectionRevisions = appendRevision(out.ProjectionRevisions, rev)
	out.Stages = append(out.Stages, protocol.RuntimeSignalStageOutcome{Name: "agent_activity", OK: true, Revision: rev})

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

	now := time.Now().UTC()
	if !evidence.UpdatedAt.IsZero() {
		now = evidence.UpdatedAt.UTC()
	}
	state := daemonstate.SessionStateRunning
	session := daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          state,
		ObservedState:  state,
		Activity:       activity,
		ActivitySource: activitySource,
		UpdatedAt:      now,
	}

	seededFromSessionStore := false
	if d.sessionStore != nil {
		if existing, err := d.sessionStore.Session(projectID, sessionID); err == nil {
			if daemonstate.NormalizeSessionState(existing.State) == daemonstate.SessionStateStopped ||
				daemonstate.NormalizeSessionState(existing.ObservedState) == daemonstate.SessionStateStopped {
				return 0, nil
			}
			session = existing
			seededFromSessionStore = true
		}
	}

	if runtimeStore := d.sessionRuntimeStateStore(projectID); runtimeStore != nil {
		existing, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
		if err != nil {
			return 0, fmt.Errorf("load canonical session projection: %w", err)
		}
		if found {
			if !seededFromSessionStore &&
				(daemonstate.NormalizeSessionState(existing.State) == daemonstate.SessionStateStopped ||
					daemonstate.NormalizeSessionState(existing.ObservedState) == daemonstate.SessionStateStopped) {
				return 0, nil
			}
			if !seededFromSessionStore {
				session = existing
			} else if strings.TrimSpace(string(session.State)) == "" {
				session.State = existing.State
			}
			if seededFromSessionStore && strings.TrimSpace(string(session.ObservedState)) == "" {
				session.ObservedState = existing.ObservedState
			}
			if existing.StartedAt != nil && !existing.StartedAt.IsZero() {
				started := existing.StartedAt.UTC()
				session.StartedAt = &started
			}
			if existing.TmuxAttachedCount > 0 {
				session.TmuxAttachedCount = existing.TmuxAttachedCount
			}
		}
	}

	session.ID = sessionID
	issueID = strings.TrimSpace(issueID)
	session.IssueID = issueID
	session.Activity = activity
	session.ActivitySource = activitySource
	session.UpdatedAt = now
	return d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(ctx, projectID, meta, session), nil
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
