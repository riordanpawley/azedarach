package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// OrchestratorLoopCheckpoint is the durable recovery point for one steward
// loop. The action key is persisted beside the cursor so a daemon that crashes
// after applying an action can replay the same idempotent request.
type OrchestratorLoopCheckpoint struct {
	Identity         domain.OrchestratorIdentity
	WatchCursor      int64
	LastActionKey    string
	LastActionKind   string
	LastActionStatus string
	UpdatedAt        time.Time
}

func (s *RuntimeStateStore) GetOrchestratorLoopCheckpoint(ctx context.Context, identity domain.OrchestratorIdentity) (OrchestratorLoopCheckpoint, bool, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorLoopCheckpoint{}, false, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return OrchestratorLoopCheckpoint{}, false, err
	}
	var checkpoint OrchestratorLoopCheckpoint
	var updated string
	err = db.QueryRowContext(ctx, `SELECT watch_cursor, last_action_key, last_action_kind, last_action_status, updated_at FROM `+orchestratorLoopTable+` WHERE project_id = ? AND scope_kind = ? AND root_issue_id = ?`, identity.ProjectID, identity.Scope.Kind, identity.Scope.RootIssueID).Scan(&checkpoint.WatchCursor, &checkpoint.LastActionKey, &checkpoint.LastActionKind, &checkpoint.LastActionStatus, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return OrchestratorLoopCheckpoint{Identity: identity}, false, nil
	}
	if err != nil {
		return OrchestratorLoopCheckpoint{}, false, fmt.Errorf("get orchestrator loop checkpoint: %w", err)
	}
	checkpoint.Identity = identity
	checkpoint.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return OrchestratorLoopCheckpoint{}, false, fmt.Errorf("parse orchestrator loop checkpoint updated_at: %w", err)
	}
	return checkpoint, true, nil
}

// AdvanceOrchestratorLoopCheckpoint uses cursor compare-and-swap. Competing
// daemons may both replay an action, but only one advances the shared cursor;
// the action itself is protected by its deterministic idempotency key.
func (s *RuntimeStateStore) AdvanceOrchestratorLoopCheckpoint(ctx context.Context, checkpoint OrchestratorLoopCheckpoint, expectedCursor int64) (bool, error) {
	identity, err := normalizeOrchestratorIdentity(checkpoint.Identity)
	if err != nil {
		return false, err
	}
	if checkpoint.WatchCursor < expectedCursor {
		return false, fmt.Errorf("orchestrator loop cursor cannot move backwards: %d < %d", checkpoint.WatchCursor, expectedCursor)
	}
	checkpoint.Identity = identity
	checkpoint.UpdatedAt = checkpoint.UpdatedAt.UTC()
	if checkpoint.UpdatedAt.IsZero() {
		checkpoint.UpdatedAt = time.Now().UTC()
	}
	var advanced bool
	err = s.withWriteLock(ctx, func() error {
		db, dbErr := s.dbHandle()
		if dbErr != nil {
			return dbErr
		}
		actionKey, actionKind, actionStatus := strings.TrimSpace(checkpoint.LastActionKey), strings.TrimSpace(checkpoint.LastActionKind), strings.TrimSpace(checkpoint.LastActionStatus)
		args := []any{checkpoint.WatchCursor, actionKey, actionKind, actionStatus, checkpoint.UpdatedAt.Format(time.RFC3339Nano), identity.ProjectID, identity.Scope.Kind, identity.Scope.RootIssueID, expectedCursor, checkpoint.WatchCursor, actionKey, actionKind, actionStatus}
		result, execErr := db.ExecContext(ctx, `UPDATE `+orchestratorLoopTable+` SET watch_cursor=?, last_action_key=?, last_action_kind=?, last_action_status=?, updated_at=? WHERE project_id=? AND scope_kind=? AND root_issue_id=? AND watch_cursor=? AND (watch_cursor<>? OR last_action_key<>? OR last_action_kind<>? OR last_action_status<>?)`, args...)
		if execErr != nil {
			return fmt.Errorf("advance orchestrator loop checkpoint: %w", execErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("read orchestrator loop checkpoint result: %w", rowsErr)
		}
		advanced = rows > 0
		if !advanced && expectedCursor == 0 {
			result, execErr = db.ExecContext(ctx, `INSERT OR IGNORE INTO `+orchestratorLoopTable+` (project_id, scope_kind, root_issue_id, watch_cursor, last_action_key, last_action_kind, last_action_status, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, identity.ProjectID, identity.Scope.Kind, identity.Scope.RootIssueID, checkpoint.WatchCursor, actionKey, actionKind, actionStatus, checkpoint.UpdatedAt.Format(time.RFC3339Nano))
			if execErr != nil {
				return fmt.Errorf("initialize orchestrator loop checkpoint: %w", execErr)
			}
			rows, rowsErr = result.RowsAffected()
			if rowsErr != nil {
				return fmt.Errorf("read orchestrator loop checkpoint initialization: %w", rowsErr)
			}
			advanced = rows > 0
		}
		return nil
	})
	return advanced, err
}

// CompleteOrchestratorLoopAction transitions one exact pending action to its
// terminal result. Including the persisted key and applying state in the CAS
// gives concurrent recovery daemons a single completion/event winner.
func (s *RuntimeStateStore) CompleteOrchestratorLoopAction(ctx context.Context, checkpoint OrchestratorLoopCheckpoint) (bool, error) {
	identity, err := normalizeOrchestratorIdentity(checkpoint.Identity)
	if err != nil {
		return false, err
	}
	checkpoint.UpdatedAt = checkpoint.UpdatedAt.UTC()
	if checkpoint.UpdatedAt.IsZero() {
		checkpoint.UpdatedAt = time.Now().UTC()
	}
	actionKey, actionStatus := strings.TrimSpace(checkpoint.LastActionKey), strings.TrimSpace(checkpoint.LastActionStatus)
	if actionKey == "" || actionStatus == "" || actionStatus == "applying" {
		return false, fmt.Errorf("complete orchestrator loop action requires a keyed terminal status")
	}
	var completed bool
	err = s.withWriteLock(ctx, func() error {
		db, dbErr := s.dbHandle()
		if dbErr != nil {
			return dbErr
		}
		result, execErr := db.ExecContext(ctx, `UPDATE `+orchestratorLoopTable+` SET last_action_status=?, updated_at=? WHERE project_id=? AND scope_kind=? AND root_issue_id=? AND watch_cursor=? AND last_action_key=? AND last_action_kind='start' AND last_action_status='applying'`, actionStatus, checkpoint.UpdatedAt.Format(time.RFC3339Nano), identity.ProjectID, identity.Scope.Kind, identity.Scope.RootIssueID, checkpoint.WatchCursor, actionKey)
		if execErr != nil {
			return fmt.Errorf("complete orchestrator loop action: %w", execErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("read orchestrator loop action completion: %w", rowsErr)
		}
		completed = rows > 0
		return nil
	})
	return completed, err
}
