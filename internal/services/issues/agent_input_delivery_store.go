package issues

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

var ErrAgentInputIntentConflict = errors.New("agent input intent conflicts with durable intent")

type AgentInputDeliveryIntent struct {
	Request      domain.AgentInputDeliveryRequest
	State        string
	LeaseOwner   string
	LeaseToken   string
	LeaseExpires time.Time
	AttemptCount int
	Acknowledged time.Time
}

type AgentInputDeliverySessionLease struct {
	ProjectID                string
	SessionID                string
	AgentIncarnation         string
	LeaseOwner               string
	LeaseToken               string
	LeaseExpires             time.Time
	PreviousAgentIncarnation string
	PreviousLeaseToken       string
	TakeoverPending          bool
}

func (c *Client) CountAgentInputDeliveryIntentsByKind(ctx context.Context, projectID string, kind domain.AgentInputMessageKind) (map[string]int, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT state, COUNT(*) FROM agent_input_delivery_intents WHERE project_id=? AND message_kind=? GROUP BY state ORDER BY state`, strings.TrimSpace(projectID), string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		counts[state] = count
	}
	return counts, rows.Err()
}

// ClaimAgentInputDeliverySessionLease excludes every automated delivery to the
// same durable session. Incarnation is a fenced value, not part of the key, so
// an old and new incarnation can never own overlapping session-global gates.
func (c *Client) ClaimAgentInputDeliverySessionLease(ctx context.Context, projectID, sessionID, incarnation, owner string, now time.Time, leaseDuration time.Duration) (AgentInputDeliverySessionLease, bool, error) {
	var lease AgentInputDeliverySessionLease
	projectID = strings.TrimSpace(projectID)
	sessionID = strings.TrimSpace(sessionID)
	incarnation = strings.TrimSpace(incarnation)
	owner = strings.TrimSpace(owner)
	if projectID == "" || sessionID == "" || incarnation == "" || owner == "" || leaseDuration <= 0 {
		return lease, false, errors.New("invalid agent input session lease")
	}
	token, err := randomAgentInputToken()
	if err != nil {
		return lease, false, err
	}
	expires := now.Add(leaseDuration)
	acquired := false
	err = c.retrySQLiteBusy(ctx, func() error {
		acquired = false
		lease = AgentInputDeliverySessionLease{}
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
			var previousIncarnation, previousOwner, previousToken, previousExpiry string
			err = tx.QueryRowContext(lockCtx, `SELECT agent_incarnation,lease_owner,lease_token,lease_expires_at FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=?`, projectID, sessionID).Scan(&previousIncarnation, &previousOwner, &previousToken, &previousExpiry)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				err = nil
				_, err = tx.ExecContext(lockCtx, `INSERT INTO agent_input_delivery_session_leases(project_id,session_id,agent_incarnation,lease_owner,lease_token,lease_expires_at,updated_at) VALUES(?,?,?,?,?,?,?)`, projectID, sessionID, incarnation, owner, token, formatTimestamp(expires), formatTimestamp(now))
			case err != nil:
				return err
			case parseTimestamp(previousExpiry).IsZero():
				return fmt.Errorf("invalid durable agent input session lease expiry %q", previousExpiry)
			case now.Before(parseTimestamp(previousExpiry)):
				return nil
			default:
				// Take ownership of the expired fence without rotating its identity.
				// A persisted tmux gate still names this exact incarnation/token, so
				// changing either before restoration would destroy the only durable
				// recovery authority if this daemon crashed mid-takeover.
				result, updateErr := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases SET lease_owner=?,lease_expires_at=?,updated_at=? WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=? AND lease_expires_at=?`, owner, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, previousIncarnation, previousOwner, previousToken, previousExpiry)
				if updateErr != nil {
					return updateErr
				}
				n, rowsErr := result.RowsAffected()
				if rowsErr != nil || n != 1 {
					return rowsErr
				}
				lease.PreviousAgentIncarnation = previousIncarnation
				lease.PreviousLeaseToken = previousToken
				lease.TakeoverPending = true
				lease.AgentIncarnation = previousIncarnation
				lease.LeaseToken = previousToken
			}
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			acquired = true
			return nil
		})
	})
	if acquired {
		lease.ProjectID = projectID
		lease.SessionID = sessionID
		if !lease.TakeoverPending {
			lease.AgentIncarnation = incarnation
		}
		lease.LeaseOwner = owner
		if !lease.TakeoverPending {
			lease.LeaseToken = token
		}
		lease.LeaseExpires = expires
	}
	return lease, acquired, err
}

func (c *Client) RenewAgentInputDeliverySessionLease(ctx context.Context, projectID, sessionID, incarnation, leaseOwner, leaseToken string, now time.Time, leaseDuration time.Duration) (time.Time, bool, error) {
	var renewed bool
	expires := now.Add(leaseDuration)
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
			var storedExpiry string
			if err := tx.QueryRowContext(lockCtx, `SELECT lease_expires_at FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=?`, projectID, sessionID, incarnation, leaseOwner, leaseToken).Scan(&storedExpiry); errors.Is(err, sql.ErrNoRows) {
				return tx.Commit()
			} else if err != nil {
				return err
			}
			parsedExpiry := parseTimestamp(storedExpiry)
			if parsedExpiry.IsZero() {
				return fmt.Errorf("invalid durable agent input session lease expiry %q", storedExpiry)
			}
			if !now.Before(parsedExpiry) {
				return tx.Commit()
			}
			result, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases SET lease_expires_at=?,updated_at=? WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=? AND lease_expires_at=?`, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, incarnation, leaseOwner, leaseToken, storedExpiry)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			renewed = n == 1
			if err != nil {
				return err
			}
			return tx.Commit()
		})
	})
	if !renewed {
		expires = time.Time{}
	}
	return expires, renewed, err
}

// CompleteAgentInputDeliverySessionLeaseTakeover rotates a takeover fence only
// after the caller has restored and removed the persisted gate that names the
// old incarnation/token. The exact old expiry participates in the CAS so a
// concurrent renewal or recovery claim cannot be overwritten.
func (c *Client) CompleteAgentInputDeliverySessionLeaseTakeover(ctx context.Context, projectID, sessionID, previousIncarnation, previousToken, incarnation, owner string, now time.Time, leaseDuration time.Duration) (AgentInputDeliverySessionLease, bool, error) {
	var lease AgentInputDeliverySessionLease
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(previousIncarnation) == "" || strings.TrimSpace(previousToken) == "" || strings.TrimSpace(incarnation) == "" || strings.TrimSpace(owner) == "" || leaseDuration <= 0 {
		return lease, false, errors.New("invalid agent input session lease takeover")
	}
	token, err := randomAgentInputToken()
	if err != nil {
		return lease, false, err
	}
	expires := now.Add(leaseDuration)
	completed := false
	err = c.retrySQLiteBusy(ctx, func() error {
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
			var storedOwner, storedExpiry string
			if err := tx.QueryRowContext(lockCtx, `SELECT lease_owner,lease_expires_at FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_token=?`, projectID, sessionID, previousIncarnation, previousToken).Scan(&storedOwner, &storedExpiry); errors.Is(err, sql.ErrNoRows) {
				return tx.Commit()
			} else if err != nil {
				return err
			}
			parsedExpiry := parseTimestamp(storedExpiry)
			if parsedExpiry.IsZero() {
				return fmt.Errorf("invalid durable agent input session lease expiry %q", storedExpiry)
			}
			if storedOwner != owner || !now.Before(parsedExpiry) {
				return tx.Commit()
			}
			result, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases SET agent_incarnation=?,lease_token=?,lease_expires_at=?,updated_at=? WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=? AND lease_expires_at=?`, incarnation, token, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, previousIncarnation, owner, previousToken, storedExpiry)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			completed = n == 1
			return tx.Commit()
		})
	})
	if completed {
		lease = AgentInputDeliverySessionLease{ProjectID: projectID, SessionID: sessionID, AgentIncarnation: incarnation, LeaseOwner: owner, LeaseToken: token, LeaseExpires: expires}
	}
	return lease, completed, err
}

func (c *Client) ReleaseAgentInputDeliverySessionLease(ctx context.Context, projectID, sessionID, incarnation, leaseOwner, leaseToken string) error {
	return c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			_, err = db.ExecContext(lockCtx, `DELETE FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=?`, projectID, sessionID, incarnation, leaseOwner, leaseToken)
			return err
		})
	})
}

// ClaimAgentInputDeliverySessionLeaseRecovery atomically transfers ownership
// of an expired persisted-gate fence without rotating its token. Keeping the
// marker's exact token makes crashes before marker persistence recoverable;
// owner-aware renew/release calls fence the superseded daemon.
func (c *Client) ClaimAgentInputDeliverySessionLeaseRecovery(ctx context.Context, projectID, sessionID, incarnation, expectedToken, owner string, now time.Time, leaseDuration time.Duration) (AgentInputDeliverySessionLease, bool, error) {
	var lease AgentInputDeliverySessionLease
	projectID = strings.TrimSpace(projectID)
	sessionID = strings.TrimSpace(sessionID)
	incarnation = strings.TrimSpace(incarnation)
	expectedToken = strings.TrimSpace(expectedToken)
	owner = strings.TrimSpace(owner)
	if projectID == "" || sessionID == "" || incarnation == "" || expectedToken == "" || owner == "" || leaseDuration <= 0 {
		return lease, false, errors.New("invalid agent input session recovery lease")
	}
	expires := now.Add(leaseDuration)
	acquired := false
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
			var currentOwner, currentExpiry string
			if err := tx.QueryRowContext(lockCtx, `SELECT lease_owner,lease_expires_at FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_token=?`, projectID, sessionID, incarnation, expectedToken).Scan(&currentOwner, &currentExpiry); errors.Is(err, sql.ErrNoRows) {
				return tx.Commit()
			} else if err != nil {
				return err
			}
			parsedExpiry := parseTimestamp(currentExpiry)
			if parsedExpiry.IsZero() {
				return fmt.Errorf("invalid durable agent input session lease expiry %q", currentExpiry)
			}
			if now.Before(parsedExpiry) {
				lease = AgentInputDeliverySessionLease{ProjectID: projectID, SessionID: sessionID, AgentIncarnation: incarnation, LeaseOwner: currentOwner, LeaseToken: expectedToken, LeaseExpires: parsedExpiry}
				return tx.Commit()
			}
			result, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases
				SET lease_owner=?,lease_expires_at=?,updated_at=?
				WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=? AND lease_expires_at=?`,
				owner, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, incarnation, currentOwner, expectedToken, currentExpiry)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			acquired = n == 1
			if err != nil {
				return err
			}
			return tx.Commit()
		})
	})
	if acquired {
		lease = AgentInputDeliverySessionLease{ProjectID: projectID, SessionID: sessionID, AgentIncarnation: incarnation, LeaseOwner: owner, LeaseToken: expectedToken, LeaseExpires: expires}
	}
	return lease, acquired, err
}

func (c *Client) ListPendingAgentInputDeliveryIntents(ctx context.Context, projectID string, now time.Time, limit int) ([]AgentInputDeliveryIntent, error) {
	if limit <= 0 {
		limit = 100
	}
	var intents []AgentInputDeliveryIntent
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
			rows, err := tx.QueryContext(lockCtx, `SELECT intent_key FROM agent_input_delivery_intents WHERE project_id=? AND state IN ('queued','leased','ambiguous') ORDER BY created_at,intent_key`, projectID)
			if err != nil {
				return err
			}
			var keys []string
			for rows.Next() {
				var key string
				if err := rows.Scan(&key); err != nil {
					rows.Close()
					return err
				}
				keys = append(keys, key)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			intents = make([]AgentInputDeliveryIntent, 0, min(limit, len(keys)))
			for _, key := range keys {
				intent, err := c.loadAgentInputDeliveryIntent(lockCtx, tx, projectID, key)
				if err != nil {
					return err
				}
				if !intent.Request.ExpiresAt.IsZero() && !now.Before(intent.Request.ExpiresAt) {
					if _, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='expired',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE project_id=? AND intent_key=? AND state IN ('queued','leased','ambiguous')`, formatTimestamp(now), projectID, key); err != nil {
						return err
					}
					continue
				}
				if len(intents) >= limit || intent.State == "ambiguous" || (intent.State == "leased" && (intent.LeaseExpires.IsZero() || now.Before(intent.LeaseExpires))) {
					continue
				}
				intents = append(intents, intent)
			}
			return tx.Commit()
		})
	})
	if err != nil {
		return nil, err
	}
	return intents, nil
}

func (c *Client) EnsureAgentInputDeliveryIntent(ctx context.Context, request domain.AgentInputDeliveryRequest) (AgentInputDeliveryIntent, error) {
	var intent AgentInputDeliveryIntent
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			_, err = db.ExecContext(lockCtx, `INSERT INTO agent_input_delivery_intents (
				project_id,intent_key,session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,agent_thread_id,
				tool,message_kind,payload,state,expires_at,created_at,updated_at
			) VALUES(?,?,?,?,?,?,?,?,?,?,?, 'queued',?,?,?) ON CONFLICT(project_id,intent_key) DO NOTHING`,
				strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.IntentKey), strings.TrimSpace(request.SessionID),
				strings.TrimSpace(string(request.Target.LogicalPaneID)), strings.TrimSpace(request.Target.TmuxPaneID), request.Target.PanePID,
				strings.TrimSpace(request.Target.AgentIncarnation), nullableTrimmedString(request.Target.AgentThreadID), strings.TrimSpace(request.Tool), string(request.Kind), request.Payload,
				nullableTimestamp(request.ExpiresAt), formatTimestamp(now), formatTimestamp(now))
			if err != nil {
				return fmt.Errorf("insert agent input intent: %w", err)
			}
			intent, err = c.loadAgentInputDeliveryIntent(lockCtx, db, request.ProjectID, request.IntentKey)
			if err != nil {
				return err
			}
			if !sameAgentInputRequest(intent.Request, request) {
				return ErrAgentInputIntentConflict
			}
			if (intent.State == "queued" || intent.State == "leased" || intent.State == "ambiguous") &&
				!intent.Request.ExpiresAt.IsZero() && !now.Before(intent.Request.ExpiresAt) {
				if _, err := db.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='expired',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE project_id=? AND intent_key=? AND state IN ('queued','leased','ambiguous')`, formatTimestamp(now), request.ProjectID, request.IntentKey); err != nil {
					return err
				}
				intent.State = "expired"
				intent.LeaseOwner = ""
				intent.LeaseToken = ""
				intent.LeaseExpires = time.Time{}
			}
			return nil
		})
	})
	return intent, err
}

func (c *Client) ClaimAgentInputDeliveryIntent(ctx context.Context, projectID, intentKey, owner string, now time.Time, leaseDuration time.Duration) (AgentInputDeliveryIntent, bool, error) {
	var intent AgentInputDeliveryIntent
	claimed := false
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
			intent, err = c.loadAgentInputDeliveryIntent(lockCtx, tx, projectID, intentKey)
			if err != nil {
				return err
			}
			if intent.State == "delivered" || intent.State == "expired" || intent.State == "stale" {
				return tx.Commit()
			}
			if !intent.Request.ExpiresAt.IsZero() && !now.Before(intent.Request.ExpiresAt) {
				_, err = tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='expired',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE project_id=? AND intent_key=?`, formatTimestamp(now), projectID, intentKey)
				intent.State = "expired"
				if err != nil {
					return err
				}
				return tx.Commit()
			}
			if intent.State == "ambiguous" {
				return tx.Commit()
			}
			if intent.State == "leased" {
				if intent.LeaseExpires.IsZero() || now.Before(intent.LeaseExpires) {
					return tx.Commit()
				}
			}
			token, err := randomAgentInputToken()
			if err != nil {
				return err
			}
			expires := now.Add(leaseDuration)
			var result sql.Result
			if intent.State == "leased" {
				result, err = tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='leased',lease_owner=?,lease_token=?,lease_expires_at=?,attempt_count=attempt_count+1,updated_at=? WHERE project_id=? AND intent_key=? AND state='leased' AND lease_token=?`, owner, token, formatTimestamp(expires), formatTimestamp(now), projectID, intentKey, intent.LeaseToken)
			} else {
				result, err = tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='leased',lease_owner=?,lease_token=?,lease_expires_at=?,attempt_count=attempt_count+1,updated_at=? WHERE project_id=? AND intent_key=? AND state='queued'`, owner, token, formatTimestamp(expires), formatTimestamp(now), projectID, intentKey)
			}
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			claimed = n == 1
			if claimed {
				intent.State = "leased"
				intent.LeaseOwner = owner
				intent.LeaseToken = token
				intent.LeaseExpires = expires
				intent.AttemptCount++
			}
			return tx.Commit()
		})
	})
	return intent, claimed, err
}

// BeginAgentInputDeliverySubmission durably crosses the no-automatic-retry
// boundary and renews the exact session fence in one transaction immediately
// before app-server turn/start. A daemon crash after this transition leaves the
// intent ambiguous instead of risking a duplicate turn.
func (c *Client) BeginAgentInputDeliverySubmission(ctx context.Context, projectID, intentKey, intentLeaseToken, sessionID, incarnation, sessionLeaseOwner, sessionLeaseToken string, now time.Time, leaseDuration time.Duration) (time.Time, bool, error) {
	var begun bool
	expires := now.Add(leaseDuration)
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
			var storedExpiry string
			if err := tx.QueryRowContext(lockCtx, `SELECT lease_expires_at FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=?`, projectID, sessionID, incarnation, sessionLeaseOwner, sessionLeaseToken).Scan(&storedExpiry); errors.Is(err, sql.ErrNoRows) {
				return tx.Commit()
			} else if err != nil {
				return err
			}
			parsedExpiry := parseTimestamp(storedExpiry)
			if parsedExpiry.IsZero() {
				return fmt.Errorf("invalid durable agent input session lease expiry %q", storedExpiry)
			}
			if !now.Before(parsedExpiry) {
				return tx.Commit()
			}
			sessionResult, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases SET lease_expires_at=?,updated_at=? WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_owner=? AND lease_token=? AND lease_expires_at=?`, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, incarnation, sessionLeaseOwner, sessionLeaseToken, storedExpiry)
			if err != nil {
				return err
			}
			sessionRows, err := sessionResult.RowsAffected()
			if err != nil || sessionRows != 1 {
				return err
			}
			intentResult, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='ambiguous',updated_at=? WHERE project_id=? AND intent_key=? AND session_id=? AND agent_incarnation=? AND state='leased' AND lease_token=?`, formatTimestamp(now), projectID, intentKey, sessionID, incarnation, intentLeaseToken)
			if err != nil {
				return err
			}
			intentRows, err := intentResult.RowsAffected()
			if err != nil || intentRows != 1 {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			begun = true
			return nil
		})
	})
	if !begun {
		expires = time.Time{}
	}
	return expires, begun, err
}

func (c *Client) AcknowledgeAgentInputDeliveryIntent(ctx context.Context, projectID, intentKey, incarnation, leaseToken, acknowledgement string, now time.Time) (bool, error) {
	if strings.TrimSpace(acknowledgement) == "" {
		return false, errors.New("empty agent input acknowledgement")
	}
	var acknowledged bool
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			result, err := db.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='delivered',acknowledgement_token=?,acknowledged_at=?,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE project_id=? AND intent_key=? AND agent_incarnation=? AND state='ambiguous' AND lease_token=?`, acknowledgement, formatTimestamp(now), formatTimestamp(now), projectID, intentKey, incarnation, leaseToken)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			acknowledged = n == 1
			return err
		})
	})
	return acknowledged, err
}

func (c *Client) ReleaseAgentInputDeliveryIntent(ctx context.Context, projectID, intentKey, leaseToken string, stale bool, now time.Time) error {
	state := "queued"
	if stale {
		state = "stale"
	}
	return c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			_, err = db.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state=?,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE project_id=? AND intent_key=? AND state='leased' AND lease_token=?`, state, formatTimestamp(now), projectID, intentKey, leaseToken)
			return err
		})
	})
}

// ResolveAgentInputDeliverySubmissionRefusal leaves an ambiguous intent
// retryable (or stale) only after the caller proved that turn/start was not
// attempted or app-server authoritatively rejected it. Transport failures and
// daemon crashes must never call this path.
func (c *Client) ResolveAgentInputDeliverySubmissionRefusal(ctx context.Context, projectID, intentKey, leaseToken string, stale bool, now time.Time) error {
	state := "queued"
	if stale {
		state = "stale"
	}
	return c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			_, err = db.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state=?,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE project_id=? AND intent_key=? AND state='ambiguous' AND lease_token=?`, state, formatTimestamp(now), projectID, intentKey, leaseToken)
			return err
		})
	})
}

func (c *Client) loadAgentInputDeliveryIntent(ctx context.Context, q sqlIssueDBTX, projectID, intentKey string) (AgentInputDeliveryIntent, error) {
	var out AgentInputDeliveryIntent
	var logicalPane, kind, expires, leaseExpires, acknowledged sql.NullString
	err := q.QueryRowContext(ctx, `SELECT session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,COALESCE(agent_thread_id,''),tool,message_kind,payload,state,expires_at,COALESCE(lease_owner,''),COALESCE(lease_token,''),lease_expires_at,attempt_count,acknowledged_at FROM agent_input_delivery_intents WHERE project_id=? AND intent_key=?`, projectID, intentKey).Scan(
		&out.Request.SessionID, &logicalPane, &out.Request.Target.TmuxPaneID, &out.Request.Target.PanePID, &out.Request.Target.AgentIncarnation, &out.Request.Target.AgentThreadID, &out.Request.Tool, &kind, &out.Request.Payload, &out.State, &expires, &out.LeaseOwner, &out.LeaseToken, &leaseExpires, &out.AttemptCount, &acknowledged)
	if err != nil {
		return out, err
	}
	out.Request.ProjectID = projectID
	out.Request.IntentKey = intentKey
	out.Request.Target.LogicalPaneID = domain.ManagedAgentPaneID(logicalPane.String)
	out.Request.Kind = domain.AgentInputMessageKind(kind.String)
	if expires.Valid {
		out.Request.ExpiresAt = parseTimestamp(expires.String)
	}
	if leaseExpires.Valid {
		out.LeaseExpires = parseTimestamp(leaseExpires.String)
	}
	if acknowledged.Valid {
		out.Acknowledged = parseTimestamp(acknowledged.String)
	}
	return out, nil
}

func sameAgentInputRequest(a, b domain.AgentInputDeliveryRequest) bool {
	// Expiry is attempt-time delivery policy, not part of the logical intent.
	// The first durable expiry wins so an idempotent retry cannot extend the
	// delivery window, while exact target, kind, and payload remain fenced.
	return a.ProjectID == b.ProjectID && a.IntentKey == b.IntentKey && a.SessionID == b.SessionID && a.Target.SameIncarnation(b.Target) && a.Tool == b.Tool && a.Kind == b.Kind && a.Payload == b.Payload
}
func nullableTimestamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTimestamp(t.UTC())
}

func nullableTrimmedString(value string) any {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return nil
}
func randomAgentInputToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
