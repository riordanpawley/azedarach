package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type OrchestrationStartAttempt struct {
	ProjectID     string
	IssueID       string
	IntentKey     string
	ActorID       string
	DedupeKey     string
	State         string
	ClaimAcquired bool
	OperationID   string
	LastError     string
}

// BeginOrchestrationStart atomically reserves an issue for one orchestrator and
// records the durable saga step that must either submit a session start or be
// compensated. Existing ownership by the same actor is preserved and is never
// released by compensation for this attempt.
func (c *Client) BeginOrchestrationStart(ctx context.Context, projectID, issueID, intentKey, actorID, dedupeKey string) (OrchestrationStartAttempt, error) {
	projectID, issueID, intentKey, actorID, dedupeKey = strings.TrimSpace(projectID), strings.TrimSpace(issueID), strings.TrimSpace(intentKey), strings.TrimSpace(actorID), strings.TrimSpace(dedupeKey)
	if projectID == "" || issueID == "" || intentKey == "" || actorID == "" || dedupeKey == "" {
		return OrchestrationStartAttempt{}, errors.New("project, issue, intent, actor, and dedupe key are required")
	}
	var result OrchestrationStartAttempt
	err := c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		result, err = orchestrationStartAttemptForUpdate(ctx, tx, projectID, issueID, intentKey)
		if err == nil {
			if !strings.EqualFold(result.ActorID, actorID) {
				return fmt.Errorf("%w: orchestration intent owned by %s", domain.ErrConflict, result.ActorID)
			}
			if result.State == "compensated" {
				return fmt.Errorf("%w: orchestration intent was compensated after %s", domain.ErrConflict, result.LastError)
			}
			return tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		task, err := c.issueOwnershipForUpdate(ctx, tx, issueID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		claimAcquired := task.Ownership == nil || task.Ownership.IsExpired(now)
		if task.Ownership != nil && !task.Ownership.IsExpired(now) && !strings.EqualFold(task.Ownership.OwnerID, actorID) {
			return fmt.Errorf("%w: execution lease owned by %s", domain.ErrConflict, task.Ownership.OwnerID)
		}
		nowRaw := now.Format(time.RFC3339Nano)
		if claimAcquired {
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_coordination_leases
					(issue_id, purpose, owner_id, owner_kind, claimed_at, expires_at)
					VALUES (?, ?, ?, 'agent', ?, NULL)
					ON CONFLICT(issue_id, purpose) DO UPDATE SET owner_id=excluded.owner_id,
					owner_kind=excluded.owner_kind, claimed_at=excluded.claimed_at, expires_at=NULL`, issueID, domain.CoordinationLeaseExecution, actorID, nowRaw); err != nil {
				return err
			}
			if err := c.appendIssueObservationEvent(ctx, tx, issueID, domain.IssueEventIssueOwnershipChanged, map[string]any{"action": "claimed", "owner_id": actorID, "owner_kind": "agent", "purpose": domain.CoordinationLeaseExecution, "orchestration_intent_key": intentKey}); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO orchestration_start_attempts
				(project_id, issue_id, intent_key, actor_id, dedupe_key, state, claim_acquired, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 'claimed', ?, ?, ?)`, projectID, issueID, intentKey, actorID, dedupeKey, claimAcquired, nowRaw, nowRaw); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return fmt.Errorf("%w: issue already has an active orchestration start", domain.ErrConflict)
			}
			return err
		}
		result = OrchestrationStartAttempt{ProjectID: projectID, IssueID: issueID, IntentKey: intentKey, ActorID: actorID, DedupeKey: dedupeKey, State: "claimed", ClaimAcquired: claimAcquired}
		return tx.Commit()
	})
	if err != nil {
		return OrchestrationStartAttempt{}, c.wrapError("begin-orchestration-start", issueID, err)
	}
	return result, nil
}

func (c *Client) CompleteOrchestrationStart(ctx context.Context, attempt OrchestrationStartAttempt, operationID string) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return errors.New("operation id is required to complete orchestration start")
	}
	return c.finishOrchestrationStart(ctx, attempt, "submitted", operationID, "")
}

func (c *Client) CompensateOrchestrationStart(ctx context.Context, attempt OrchestrationStartAttempt, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return c.finishOrchestrationStart(ctx, attempt, "compensated", "", message)
}

// CompensateOrchestrationStartOperation releases a claim acquired for a
// session-start operation that reached a non-success terminal state. Matching
// by the durable dedupe key also covers the narrow race where the operation
// fails before its operation ID has been recorded on the attempt.
func (c *Client) CompensateOrchestrationStartOperation(ctx context.Context, projectID, dedupeKey, operationID string, cause error) (bool, error) {
	projectID, dedupeKey, operationID = strings.TrimSpace(projectID), strings.TrimSpace(dedupeKey), strings.TrimSpace(operationID)
	if projectID == "" || dedupeKey == "" || operationID == "" {
		return false, errors.New("project, dedupe key, and operation id are required")
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	compensated := false
	err := c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		rows, err := tx.QueryContext(ctx, `SELECT project_id, issue_id, intent_key, actor_id, dedupe_key, state, claim_acquired, COALESCE(operation_id,''), COALESCE(last_error,'')
			FROM orchestration_start_attempts
			WHERE project_id=? AND dedupe_key=? AND state IN ('claimed','submitted') AND (operation_id=? OR (state='claimed' AND operation_id IS NULL))
			ORDER BY CASE WHEN operation_id=? THEN 0 ELSE 1 END, updated_at ASC`, projectID, dedupeKey, operationID, operationID)
		if err != nil {
			return err
		}
		attempts := make([]OrchestrationStartAttempt, 0, 2)
		for rows.Next() {
			var current OrchestrationStartAttempt
			if err := rows.Scan(&current.ProjectID, &current.IssueID, &current.IntentKey, &current.ActorID, &current.DedupeKey, &current.State, &current.ClaimAcquired, &current.OperationID, &current.LastError); err != nil {
				_ = rows.Close()
				return err
			}
			attempts = append(attempts, current)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, current := range attempts {
			if current.OperationID != "" && current.OperationID != operationID {
				return fmt.Errorf("orchestration start references operation %s, not %s", current.OperationID, operationID)
			}
			current.OperationID = operationID
			if err := c.compensateOrchestrationStartTx(ctx, tx, current, message); err != nil {
				return err
			}
			compensated = true
		}
		return tx.Commit()
	})
	if err != nil {
		return false, c.wrapError("compensate-orchestration-start-operation", dedupeKey, err)
	}
	return compensated, nil
}

func (c *Client) finishOrchestrationStart(ctx context.Context, attempt OrchestrationStartAttempt, state, operationID, lastError string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		current, err := orchestrationStartAttemptForUpdate(ctx, tx, attempt.ProjectID, attempt.IssueID, attempt.IntentKey)
		if err != nil {
			return err
		}
		if current.State == state {
			if state == "submitted" && current.OperationID != operationID {
				return fmt.Errorf("submitted orchestration start already references operation %s", current.OperationID)
			}
			return tx.Commit()
		}
		if current.State != "claimed" {
			return fmt.Errorf("cannot transition orchestration start from %s to %s", current.State, state)
		}
		if state == "compensated" {
			if err := c.compensateOrchestrationStartTx(ctx, tx, current, lastError); err != nil {
				return err
			}
			return tx.Commit()
		}
		nowRaw := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE orchestration_start_attempts SET state=?, operation_id=NULLIF(?, ''), last_error=NULLIF(?, ''), updated_at=? WHERE project_id=? AND issue_id=? AND intent_key=?`, state, operationID, lastError, nowRaw, current.ProjectID, current.IssueID, current.IntentKey); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (c *Client) compensateOrchestrationStartTx(ctx context.Context, tx *sql.Tx, current OrchestrationStartAttempt, lastError string) error {
	if current.ClaimAcquired {
		res, err := tx.ExecContext(ctx, `DELETE FROM issue_coordination_leases WHERE issue_id=? AND purpose=? AND owner_id=?`, current.IssueID, domain.CoordinationLeaseExecution, current.ActorID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			if err := c.appendIssueObservationEvent(ctx, tx, current.IssueID, domain.IssueEventIssueOwnershipChanged, map[string]any{"action": "released", "released_by": current.ActorID, "purpose": domain.CoordinationLeaseExecution, "orchestration_compensation": true, "orchestration_intent_key": current.IntentKey}); err != nil {
				return err
			}
		}
	}
	nowRaw := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `UPDATE orchestration_start_attempts SET state='compensated', operation_id=NULLIF(?, ''), last_error=NULLIF(?, ''), updated_at=? WHERE project_id=? AND issue_id=? AND intent_key=?`, current.OperationID, lastError, nowRaw, current.ProjectID, current.IssueID, current.IntentKey)
	return err
}

func (c *Client) PendingOrchestrationStarts(ctx context.Context, projectID string) ([]OrchestrationStartAttempt, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT project_id, issue_id, intent_key, actor_id, dedupe_key, state, claim_acquired, COALESCE(operation_id,''), COALESCE(last_error,'') FROM orchestration_start_attempts WHERE project_id=? AND state='claimed' ORDER BY created_at, issue_id`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrchestrationStartAttempt
	for rows.Next() {
		var a OrchestrationStartAttempt
		if err := rows.Scan(&a.ProjectID, &a.IssueID, &a.IntentKey, &a.ActorID, &a.DedupeKey, &a.State, &a.ClaimAcquired, &a.OperationID, &a.LastError); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func orchestrationStartAttemptForUpdate(ctx context.Context, tx *sql.Tx, projectID, issueID, intentKey string) (OrchestrationStartAttempt, error) {
	var a OrchestrationStartAttempt
	err := tx.QueryRowContext(ctx, `SELECT project_id, issue_id, intent_key, actor_id, dedupe_key, state, claim_acquired, COALESCE(operation_id,''), COALESCE(last_error,'') FROM orchestration_start_attempts WHERE project_id=? AND issue_id=? AND intent_key=?`, projectID, issueID, intentKey).Scan(&a.ProjectID, &a.IssueID, &a.IntentKey, &a.ActorID, &a.DedupeKey, &a.State, &a.ClaimAcquired, &a.OperationID, &a.LastError)
	return a, err
}
