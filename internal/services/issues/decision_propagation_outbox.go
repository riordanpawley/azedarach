package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	DecisionPropagationChanged   = "changed"
	DecisionPropagationWithdrawn = "withdrawn"
)

var ErrDecisionPropagationRevisionChanged = errors.New("decision propagation revision changed")

// DecisionPropagationIntent is captured in the same transaction as the
// decision audit revision. The daemon may crash at any later point without
// losing the exact issue fanout it must reconcile.
type DecisionPropagationIntent struct {
	ChangedIssueIDs             []string
	WithdrawnIssueIDs           []string
	SourceCommand               string
	Payload                     map[string]any
	ExpectedRevision            int64
	ExpectedObservationRevision int64
}

type DecisionPropagationOutboxEntry struct {
	ID                  int64
	DecisionID          string
	Revision            int64
	IssueID             string
	EventKind           string
	SourceCommand       string
	Payload             map[string]any
	CreatedAt           time.Time
	MaterializedEventID int64
}

func (c *Client) insertDecisionPropagationOutbox(ctx context.Context, tx *sql.Tx, decisionID string, revision int64, intent DecisionPropagationIntent) error {
	if revision <= 0 {
		return errors.New("decision propagation revision must be positive")
	}
	for _, batch := range []struct {
		kind     string
		issueIDs []string
	}{
		{kind: DecisionPropagationChanged, issueIDs: intent.ChangedIssueIDs},
		{kind: DecisionPropagationWithdrawn, issueIDs: intent.WithdrawnIssueIDs},
	} {
		for _, issueID := range normalizeOrderedIDs(batch.issueIDs) {
			if err := c.requireIssueExists(ctx, tx, issueID, "insert-decision-propagation-outbox"); err != nil {
				return err
			}
			payload := make(map[string]any, len(intent.Payload)+4)
			for key, value := range intent.Payload {
				payload[key] = value
			}
			payload["decision_id"] = strings.TrimSpace(decisionID)
			payload["revision"] = revision
			payload["material"] = true
			if batch.kind == DecisionPropagationWithdrawn {
				payload["withdrawn"] = true
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("marshal decision propagation payload: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO decision_propagation_outbox (
					decision_id, revision, issue_id, event_kind,
					source_command, payload_json, created_at, retired_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
			`, strings.TrimSpace(decisionID), revision, issueID, batch.kind,
				strings.TrimSpace(intent.SourceCommand), string(encoded), formatTimestamp(time.Now().UTC())); err != nil {
				return fmt.Errorf("insert decision propagation outbox: %w", err)
			}
		}
	}
	return nil
}

func (c *Client) ListActiveDecisionPropagationOutbox(ctx context.Context, limit int) ([]DecisionPropagationOutboxEntry, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = -1
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, decision_id, revision, issue_id, event_kind,
		       source_command, payload_json, created_at,
		       COALESCE(materialized_event_id, 0)
		FROM decision_propagation_outbox
		WHERE retired_at IS NULL
		ORDER BY id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, c.wrapError("list-decision-propagation-outbox", "", err)
	}
	defer rows.Close()
	entries := make([]DecisionPropagationOutboxEntry, 0)
	for rows.Next() {
		var entry DecisionPropagationOutboxEntry
		var payloadJSON, createdAt string
		if err := rows.Scan(&entry.ID, &entry.DecisionID, &entry.Revision, &entry.IssueID, &entry.EventKind, &entry.SourceCommand, &payloadJSON, &createdAt, &entry.MaterializedEventID); err != nil {
			return nil, c.wrapError("list-decision-propagation-outbox", "", err)
		}
		if err := json.Unmarshal([]byte(payloadJSON), &entry.Payload); err != nil {
			return nil, c.wrapError("list-decision-propagation-outbox", entry.DecisionID, err)
		}
		entry.CreatedAt = parseTimestamp(createdAt)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-decision-propagation-outbox", "", err)
	}
	return entries, nil
}

// MaterializeDecisionPropagationOutbox atomically appends the authority event
// and checkpoints its event ID on the outbox row. Concurrent daemons therefore
// converge on one event instead of duplicating a fanout revision.
func (c *Client) MaterializeDecisionPropagationOutbox(ctx context.Context, entry DecisionPropagationOutboxEntry) (domain.IssueObservationEvent, error) {
	var eventID int64
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(lockCtx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			var existing sql.NullInt64
			var payloadJSON string
			if err := tx.QueryRowContext(lockCtx, `
				SELECT decision_id, revision, issue_id, event_kind, source_command, payload_json, materialized_event_id
				FROM decision_propagation_outbox WHERE id = ?
			`, entry.ID).Scan(&entry.DecisionID, &entry.Revision, &entry.IssueID, &entry.EventKind, &entry.SourceCommand, &payloadJSON, &existing); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(payloadJSON), &entry.Payload); err != nil {
				return err
			}
			if existing.Valid && existing.Int64 > 0 {
				eventID = existing.Int64
				return tx.Commit()
			}
			if err := c.requireIssueExists(lockCtx, tx, entry.IssueID, "materialize-decision-propagation"); err != nil {
				return err
			}
			eventID, err = c.insertIssueObservationEvent(lockCtx, tx, entry.IssueID, IssueObservationEventParams{
				Type: domain.IssueEventDecisionChanged, Source: "daemon-decision",
				SourceCommand: entry.SourceCommand, Payload: entry.Payload,
			})
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(lockCtx, `UPDATE decision_propagation_outbox SET materialized_event_id = ? WHERE id = ? AND materialized_event_id IS NULL`, eventID, entry.ID)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil || rows != 1 {
				return fmt.Errorf("checkpoint decision propagation outbox %d: rows=%d err=%v", entry.ID, rows, err)
			}
			return tx.Commit()
		})
	})
	if err != nil {
		return domain.IssueObservationEvent{}, c.wrapError("materialize-decision-propagation-outbox", entry.DecisionID, err)
	}
	return c.getIssueObservationEventByID(ctx, eventID)
}

func (c *Client) RetireDecisionPropagationOutbox(ctx context.Context, entryID int64) error {
	if entryID <= 0 {
		return errors.New("decision propagation outbox id must be positive")
	}
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			_, err = db.ExecContext(lockCtx, `
				UPDATE decision_propagation_outbox
				SET retired_at = COALESCE(retired_at, ?)
				WHERE id = ?
			`, formatTimestamp(time.Now().UTC()), entryID)
			return err
		})
	})
	if err != nil {
		return c.wrapError("retire-decision-propagation-outbox", fmt.Sprint(entryID), err)
	}
	return nil
}

// AcknowledgeDecisionPropagation appends one daemon-authoritative exact-
// revision acknowledgement, or returns the existing event for an idempotent
// retry. The check and append share one SQLite transaction across daemons.
func (c *Client) AcknowledgeDecisionPropagation(ctx context.Context, issueID, decisionID string, revision int64, disposition, note string) (domain.IssueObservationEvent, error) {
	issueID, decisionID = strings.TrimSpace(issueID), strings.TrimSpace(decisionID)
	disposition = strings.ToLower(strings.TrimSpace(disposition))
	if issueID == "" || decisionID == "" || revision <= 0 || !domain.ValidDecisionAcknowledgementDisposition(disposition) {
		return domain.IssueObservationEvent{}, errors.New("valid issue, decision, revision, and acknowledgement disposition are required")
	}
	var eventID int64
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(lockCtx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := c.requireIssueExists(lockCtx, tx, issueID, "acknowledge-decision-propagation"); err != nil {
				return err
			}
			err = tx.QueryRowContext(lockCtx, `
				SELECT id
				FROM issue_observation_events
				WHERE issue_id = ? AND event_type = ?
				  AND source = 'daemon-decision'
				  AND source_command = 'decision.acknowledge'
				  AND TRIM(CAST(json_extract(payload_json, '$.decision_id') AS TEXT)) = ?
				  AND CAST(json_extract(payload_json, '$.revision') AS INTEGER) = ?
				ORDER BY id ASC LIMIT 1
			`, issueID, string(domain.IssueEventDecisionAcknowledged), decisionID, revision).Scan(&eventID)
			if err == nil {
				return tx.Commit()
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			eventID, err = c.insertIssueObservationEvent(lockCtx, tx, issueID, IssueObservationEventParams{
				Type: domain.IssueEventDecisionAcknowledged, Source: "daemon-decision", SourceCommand: "decision.acknowledge",
				Payload: map[string]any{"decision_id": decisionID, "revision": revision, "disposition": disposition, "note": strings.TrimSpace(note)},
			})
			if err != nil {
				return err
			}
			return tx.Commit()
		})
	})
	if err != nil {
		return domain.IssueObservationEvent{}, c.wrapError("acknowledge-decision-propagation", decisionID, err)
	}
	return c.getIssueObservationEventByID(ctx, eventID)
}
