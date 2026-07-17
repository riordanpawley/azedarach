package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type MailboxObservationProjectionCutover struct {
	State         string `json:"state"`
	Version       int    `json:"version"`
	ImportedCount int    `json:"imported_count,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
}

type LegacyMailboxObservation struct {
	IssueID      string
	EventType    domain.IssueObservationEventType
	ObservedAt   time.Time
	Source       string
	WorktreePath string
	ParentIssue  string
	Sequence     int64
	Payload      map[string]any
}

func (c *Client) MailboxObservationProjectionCutoverState(ctx context.Context) (MailboxObservationProjectionCutover, error) {
	db, err := c.dbHandle()
	if err != nil {
		return MailboxObservationProjectionCutover{}, err
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, mailboxObservationProjectionCutoverMetaKey).Scan(&raw); err != nil {
		return MailboxObservationProjectionCutover{}, c.wrapError("mailbox-projection-cutover-state", "", err)
	}
	var marker MailboxObservationProjectionCutover
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return MailboxObservationProjectionCutover{}, c.wrapError("mailbox-projection-cutover-state", "", err)
	}
	if marker.Version != 1 || marker.State != "pending" && marker.State != "complete" {
		return MailboxObservationProjectionCutover{}, c.wrapError("mailbox-projection-cutover-state", "", fmt.Errorf("unsupported state=%q version=%d", marker.State, marker.Version))
	}
	return marker, nil
}

// CompleteMailboxObservationProjectionCutover imports one bounded filesystem
// snapshot and advances the durable marker in the same transaction. A retry is
// idempotent by the original mailbox parent/sequence identity.
func (c *Client) CompleteMailboxObservationProjectionCutover(ctx context.Context, observations []LegacyMailboxObservation) (int, error) {
	imported := 0
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			attemptImported := 0
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("complete-mailbox-projection-cutover", "", err)
			}
			defer tx.Rollback()
			marker, err := mailboxProjectionCutoverStateTx(ctx, tx)
			if err != nil {
				return c.wrapError("complete-mailbox-projection-cutover", "", err)
			}
			if marker.State == "complete" {
				return nil
			}
			for _, observation := range observations {
				issueID := strings.TrimSpace(observation.IssueID)
				parentIssue := strings.TrimSpace(observation.ParentIssue)
				if issueID == "" || parentIssue == "" || observation.Sequence <= 0 || strings.TrimSpace(string(observation.EventType)) == "" {
					continue
				}
				var issueExists bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`, issueID).Scan(&issueExists); err != nil {
					return err
				}
				if !issueExists {
					continue
				}
				var exists bool
				if err := tx.QueryRowContext(ctx, `
					SELECT EXISTS (
						SELECT 1 FROM issue_observation_events
						WHERE issue_id = ? AND event_type = ?
						  AND json_extract(payload_json, '$.mail_event.parent_issue') = ?
						  AND CAST(json_extract(payload_json, '$.mail_event.seq') AS INTEGER) = ?
					)
				`, issueID, string(observation.EventType), parentIssue, observation.Sequence).Scan(&exists); err != nil {
					return err
				}
				if exists {
					continue
				}
				observedAt := observation.ObservedAt.UTC()
				if observedAt.IsZero() {
					observedAt = time.Unix(0, 0).UTC()
				}
				canonicalPayload, err := canonicalMailboxObservationPayload(observation.Payload)
				if err != nil {
					return fmt.Errorf("canonicalize mailbox observation %s/%d: %w", parentIssue, observation.Sequence, err)
				}
				if _, err := c.insertIssueObservationEvent(ctx, tx, issueID, IssueObservationEventParams{
					Type: observation.EventType, ObservedAt: observedAt, Source: strings.TrimSpace(observation.Source),
					SourceCommand: "mailbox.cutover", WorktreePath: strings.TrimSpace(observation.WorktreePath), Payload: canonicalPayload,
				}); err != nil {
					return err
				}
				attemptImported++
			}
			if c.mailboxProjectionFailureHook != nil {
				if err := c.mailboxProjectionFailureHook("before_complete"); err != nil {
					return err
				}
			}
			marker.State = "complete"
			marker.ImportedCount += attemptImported
			marker.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
			raw, err := json.Marshal(marker)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = ?`, string(raw), mailboxObservationProjectionCutoverMetaKey); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			imported = attemptImported
			return nil
		})
	})
	if err != nil {
		return 0, c.wrapError("complete-mailbox-projection-cutover", "", err)
	}
	return imported, nil
}

func canonicalMailboxObservationPayload(payload map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	canonical, _, err := canonicalMailboxObservationJSON(string(raw))
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(canonical), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func mailboxProjectionCutoverStateTx(ctx context.Context, tx *sql.Tx) (MailboxObservationProjectionCutover, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, mailboxObservationProjectionCutoverMetaKey).Scan(&raw); err != nil {
		return MailboxObservationProjectionCutover{}, err
	}
	var marker MailboxObservationProjectionCutover
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return MailboxObservationProjectionCutover{}, err
	}
	if marker.Version != 1 || marker.State != "pending" && marker.State != "complete" {
		return MailboxObservationProjectionCutover{}, fmt.Errorf("unsupported state=%q version=%d", marker.State, marker.Version)
	}
	return marker, nil
}
