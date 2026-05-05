package daemon

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

func buildRuntimeProjection(projectID string, session *daemonstate.Session, worktree *daemonstate.WorktreeState) protocol.RuntimeProjection {
	projection := protocol.RuntimeProjection{
		ProjectID: normalizeRuntimeProjectionProjectID(projectID),
	}

	if session != nil {
		updatedAt := session.UpdatedAt.UTC()
		observedState := session.ObservedState
		if strings.TrimSpace(string(observedState)) == "" {
			observedState = session.State
		}
		hasSession := observedState != daemonstate.SessionStateStopped
		projection.IssueID = parseIssueIDOrZero(session.IssueID)
		projection.Session = protocol.RuntimeSessionProjection{
			HasSession: hasSession,
			SessionID:  parseSessionIDOrZero(session.ID),
			State:      protocol.SessionLifecycleState(observedState),
			StartedAt:  timePtrFrom(session.StartedAt),
			UpdatedAt:  timePtr(updatedAt),
			Worktree:   strings.TrimSpace(projection.Worktree.Path),
		}
		projection.Agent = protocol.RuntimeAgentProjection{
			Status:    string(observedState),
			SessionID: parseSessionIDOrZero(session.ID),
			UpdatedAt: timePtr(updatedAt),
		}
	}

	if worktree != nil {
		updatedAt := worktree.UpdatedAt.UTC()
		path := strings.TrimSpace(worktree.Path)
		branch := strings.TrimSpace(worktree.Branch)
		if projection.IssueID == "" {
			projection.IssueID = parseIssueIDOrZero(worktree.IssueID)
		}
		projection.Worktree = protocol.RuntimeWorktreeProjection{
			Exists:             path != "",
			Path:               path,
			Branch:             branch,
			Healthy:            path != "" && branch != "",
			GitStatusUpdatedAt: timePtrFrom(worktree.GitStatusUpdated),
		}
		if projection.Session.SessionID != "" && projection.Session.Worktree == "" {
			projection.Session.Worktree = path
		}
		if len(worktree.GitStatusRaw) > 0 {
			var status git.GitStatus
			if err := json.Unmarshal(worktree.GitStatusRaw, &status); err == nil {
				projection.Git.HasUncommittedChanges = status.HasChanges
				projection.Git.HasConflicts = status.HasConflicts
				projection.Git.ConflictFiles = append([]string(nil), status.Conflicted...)
				projection.Git.GitAdditions = status.GitAdditions
				projection.Git.GitDeletions = status.GitDeletions
				projection.Git.GitAheadCount = status.GitAheadCount
				projection.Git.GitBehindCount = status.GitBehindCount
			}
		}
		if projection.Agent.UpdatedAt == nil {
			projection.Agent.UpdatedAt = timePtr(updatedAt)
		}
	}

	return projection
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
