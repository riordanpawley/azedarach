package daemon

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

func buildRuntimeProjection(projectID string, session *daemonstate.Session, worktree *daemonstate.WorktreeState) protocol.RuntimeProjection {
	projection := protocol.RuntimeProjection{
		ProjectID: normalizeRuntimeProjectionProjectID(projectID),
	}

	if session != nil {
		updatedAt := session.UpdatedAt.UTC()
		projection.IssueID = strings.TrimSpace(session.IssueID)
		projection.Session = protocol.RuntimeSessionProjection{
			HasSession: true,
			SessionID:  strings.TrimSpace(session.ID),
			State:      protocol.SessionLifecycleState(session.State),
			UpdatedAt:  timePtr(updatedAt),
			Worktree:   strings.TrimSpace(projection.Worktree.Path),
		}
		projection.Agent = protocol.RuntimeAgentProjection{
			Status:    string(session.State),
			SessionID: strings.TrimSpace(session.ID),
			UpdatedAt: timePtr(updatedAt),
		}
	}

	if worktree != nil {
		updatedAt := worktree.UpdatedAt.UTC()
		path := strings.TrimSpace(worktree.Path)
		branch := strings.TrimSpace(worktree.Branch)
		if projection.IssueID == "" {
			projection.IssueID = strings.TrimSpace(worktree.IssueID)
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
			}
		}
		if projection.Agent.UpdatedAt == nil {
			projection.Agent.UpdatedAt = timePtr(updatedAt)
		}
	}

	return projection
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

func normalizeRuntimeProjectionProjectID(projectID string) string {
	return protocol.NormalizeProjectID(projectID)
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
