package daemon

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

func buildRuntimeProjection(projectID string, session *daemonstate.Session, worktree *daemonstate.WorktreeState) protocol.RuntimeProjection {
	projection := protocol.RuntimeProjection{
		ProjectID: normalizeRuntimeProjectionProjectID(projectID),
	}

	if session != nil {
		updatedAt := session.UpdatedAt.UTC()
		observedState := projectionSessionState(session.State, session.ObservedState)
		hasSession := observedState != daemonstate.SessionStateStopped
		projection.IssueID = parseIssueIDOrZero(session.IssueID)
		projection.Session = protocol.RuntimeSessionProjection{
			HasSession:        hasSession,
			SessionID:         parseSessionIDOrZero(session.ID),
			Role:              protocol.SessionRole(session.Role),
			ScopeKind:         protocol.SessionScopeKind(session.ScopeKind),
			ScopeID:           strings.TrimSpace(session.ScopeID),
			State:             protocol.SessionLifecycleState(daemonstate.NormalizeSessionState(observedState)),
			TmuxAttached:      session.TmuxAttachedCount > 0,
			TmuxAttachedCount: session.TmuxAttachedCount,
			StartedAt:         timePtrFrom(session.StartedAt),
			UpdatedAt:         timePtr(updatedAt),
			Worktree:          strings.TrimSpace(projection.Worktree.Path),
		}
		if hasSession {
			if activity := normalizeSessionActivity(session.Activity); activity != "" {
				projection.Agent = protocol.RuntimeAgentProjection{
					Status:    activity,
					Source:    normalizeSessionActivitySource(session.ActivitySource, "session"),
					SessionID: projection.Session.SessionID,
					UpdatedAt: timePtr(updatedAt),
				}
			}
		}
	}

	if worktree != nil {
		path := strings.TrimSpace(worktree.Path)
		branch := strings.TrimSpace(worktree.Branch)
		if projection.IssueID == "" {
			projection.IssueID = parseIssueIDOrZero(worktree.IssueID)
		}
		gitStatusState := domain.GitFactsStatusMissing
		var status git.GitStatus
		if len(worktree.GitStatusRaw) > 0 {
			status, gitStatusState = decodeRuntimeGitStatus(worktree.GitStatusRaw)
		}
		gitFacts := domain.DeriveGitFactsObservation(path != "", gitStatusState, timeValue(worktree.GitStatusUpdated), timeNow().UTC(), domain.DefaultGitFactsStaleAfter)
		projection.Worktree = protocol.RuntimeWorktreeProjection{
			Exists:               path != "",
			Path:                 path,
			Branch:               branch,
			Healthy:              path != "" && branch != "",
			GitStatusUpdatedAt:   timePtrFrom(worktree.GitStatusUpdated),
			GitFactsAvailability: string(gitFacts.Availability),
			GitFactsReason:       gitFacts.Reason,
		}
		if projection.Session.SessionID != "" && projection.Session.Worktree == "" {
			projection.Session.Worktree = path
		}
		if gitStatusState == domain.GitFactsStatusValid {
			projection.Git.HasUncommittedChanges = status.HasChanges
			projection.Git.HasConflicts = status.HasConflicts
			projection.Git.ConflictFiles = append([]string(nil), status.Conflicted...)
			projection.Git.GitAdditions = status.GitAdditions
			projection.Git.GitDeletions = status.GitDeletions
			projection.Git.GitAheadCount = status.GitAheadCount
			projection.Git.GitBehindCount = status.GitBehindCount
		}
	}
	if worktree == nil {
		projection.Worktree.GitFactsAvailability = string(domain.GitFactsUnavailable)
		projection.Worktree.GitFactsReason = "worktree_unavailable"
	}

	return projection
}

func decodeRuntimeGitStatus(raw []byte) (git.GitStatus, domain.GitFactsStatusState) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return git.GitStatus{}, domain.GitFactsStatusInvalid
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return git.GitStatus{}, domain.GitFactsStatusInvalid
	}
	for _, field := range []string{"modified", "added", "deleted", "untracked", "staged"} {
		value, ok := envelope[field]
		if !ok || !isRuntimeGitStatusStringArray(value) {
			return git.GitStatus{}, domain.GitFactsStatusInvalid
		}
	}
	hasChanges, ok := envelope["has_changes"]
	if !ok {
		return git.GitStatus{}, domain.GitFactsStatusInvalid
	}
	var hasChangesValue *bool
	if err := json.Unmarshal(hasChanges, &hasChangesValue); err != nil || hasChangesValue == nil {
		return git.GitStatus{}, domain.GitFactsStatusInvalid
	}
	var status git.GitStatus
	if err := json.Unmarshal(trimmed, &status); err != nil {
		return git.GitStatus{}, domain.GitFactsStatusInvalid
	}
	return status, domain.GitFactsStatusValid
}

func isRuntimeGitStatusStringArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	var values []string
	return json.Unmarshal(trimmed, &values) == nil
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func projectionSessionState(desired, observed daemonstate.SessionState) daemonstate.SessionState {
	desired = daemonstate.NormalizeSessionState(desired)
	observed = daemonstate.NormalizeSessionState(observed)
	if desired == daemonstate.SessionStatePaused {
		return daemonstate.SessionStatePaused
	}
	if strings.TrimSpace(string(observed)) != "" {
		return observed
	}
	return desired
}

func applyRuntimeSessionCounts(projection *protocol.RuntimeProjection, counts sessionProjectionCounts) {
	if projection == nil || counts.Total == 0 {
		return
	}
	projection.Session.TotalCount = counts.Total
	projection.Session.ActiveCount = counts.Active
	projection.Session.PausedCount = counts.Paused
	projection.Session.TmuxAttached = counts.TmuxAttachedCount > 0
	projection.Session.TmuxAttachedCount = counts.TmuxAttachedCount
	if counts.PaneScoped == 0 {
		return
	}
	if !isAgentScopedSessionID(string(projection.Session.SessionID)) {
		return
	}
	if counts.Active > 0 {
		projection.Session.State = protocol.SessionLifecycleState(daemonstate.SessionStateRunning)
	} else {
		projection.Session.State = protocol.SessionLifecycleState(daemonstate.SessionStatePaused)
	}
}

func parseIssueIDOrZero(raw string) naming.IssueID {
	parsed, err := naming.ParseIssueID(raw)
	if err != nil {
		return ""
	}
	return parsed
}

func parseSessionIDOrZero(raw string) naming.SessionID {
	parsed, err := naming.ParseSessionIDLoose(raw)
	if err != nil {
		return ""
	}
	return parsed
}

func buildRuntimeProjectionSnapshot(projectID string, revision uint64, projections []protocol.RuntimeProjection) protocol.RuntimeProjectionSnapshotPayload {
	return protocol.RuntimeProjectionSnapshotPayload{
		SchemaVersion:    protocol.RuntimeProjectionSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: revision,
		ProjectID:        normalizeRuntimeProjectionProjectID(projectID),
		Projections:      append([]protocol.RuntimeProjection(nil), projections...),
	}
}

func buildRuntimeProjectionEventBody(projectID string, revision uint64, projection protocol.RuntimeProjection) protocol.RuntimeProjectionEventBody {
	return protocol.RuntimeProjectionEventBody{
		ProjectID:  normalizeRuntimeProjectionProjectID(projectID),
		Revision:   revision,
		Projection: projection,
	}
}

func normalizeRuntimeProjectionProjectID(projectID string) naming.ProjectID {
	return naming.ProjectID(protocol.NormalizeProjectID(projectID))
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func timePtrFrom(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}
