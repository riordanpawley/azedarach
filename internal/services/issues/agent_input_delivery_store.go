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
			var previousIncarnation, previousToken, previousExpiry string
			err = tx.QueryRowContext(lockCtx, `SELECT agent_incarnation,lease_token,lease_expires_at FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=?`, projectID, sessionID).Scan(&previousIncarnation, &previousToken, &previousExpiry)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				err = nil
				_, err = tx.ExecContext(lockCtx, `INSERT INTO agent_input_delivery_session_leases(project_id,session_id,agent_incarnation,lease_owner,lease_token,lease_expires_at,updated_at) VALUES(?,?,?,?,?,?,?)`, projectID, sessionID, incarnation, owner, token, formatTimestamp(expires), formatTimestamp(now))
			case err != nil:
				return err
			case parseTimestamp(previousExpiry).After(now):
				return nil
			default:
				result, updateErr := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases SET agent_incarnation=?,lease_owner=?,lease_token=?,lease_expires_at=?,updated_at=? WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_token=? AND julianday(lease_expires_at)<=julianday(?)`, incarnation, owner, token, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, previousIncarnation, previousToken, formatTimestamp(now))
				if updateErr != nil {
					return updateErr
				}
				n, rowsErr := result.RowsAffected()
				if rowsErr != nil || n != 1 {
					return rowsErr
				}
				lease.PreviousAgentIncarnation = previousIncarnation
				lease.PreviousLeaseToken = previousToken
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
		lease.AgentIncarnation = incarnation
		lease.LeaseOwner = owner
		lease.LeaseToken = token
		lease.LeaseExpires = expires
	}
	return lease, acquired, err
}

func (c *Client) RenewAgentInputDeliverySessionLease(ctx context.Context, projectID, sessionID, incarnation, leaseToken string, now time.Time, leaseDuration time.Duration) (time.Time, bool, error) {
	var renewed bool
	expires := now.Add(leaseDuration)
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			result, err := db.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases SET lease_expires_at=?,updated_at=? WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_token=? AND julianday(lease_expires_at)>julianday(?)`, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, incarnation, leaseToken, formatTimestamp(now))
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			renewed = n == 1
			return err
		})
	})
	if !renewed {
		expires = time.Time{}
	}
	return expires, renewed, err
}

func (c *Client) ReleaseAgentInputDeliverySessionLease(ctx context.Context, projectID, sessionID, incarnation, leaseToken string) error {
	return c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			_, err = db.ExecContext(lockCtx, `DELETE FROM agent_input_delivery_session_leases WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_token=?`, projectID, sessionID, incarnation, leaseToken)
			return err
		})
	})
}

// ClaimAgentInputDeliverySessionLeaseRecovery atomically fences recovery of a
// persisted gate. A different current token, including a takeover owner, can
// never be restored by an old gate record.
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
	token, err := randomAgentInputToken()
	if err != nil {
		return lease, false, err
	}
	expires := now.Add(leaseDuration)
	acquired := false
	err = c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			result, err := db.ExecContext(lockCtx, `INSERT INTO agent_input_delivery_session_leases(project_id,session_id,agent_incarnation,lease_owner,lease_token,lease_expires_at,updated_at)
				VALUES(?,?,?,?,?,?,?)
				ON CONFLICT(project_id,session_id) DO UPDATE SET agent_incarnation=excluded.agent_incarnation,lease_owner=excluded.lease_owner,lease_token=excluded.lease_token,lease_expires_at=excluded.lease_expires_at,updated_at=excluded.updated_at
				WHERE agent_input_delivery_session_leases.agent_incarnation=? AND agent_input_delivery_session_leases.lease_token=? AND julianday(agent_input_delivery_session_leases.lease_expires_at)<=julianday(?)`,
				projectID, sessionID, incarnation, owner, token, formatTimestamp(expires), formatTimestamp(now), incarnation, expectedToken, formatTimestamp(now))
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			acquired = n == 1
			return err
		})
	})
	if acquired {
		lease = AgentInputDeliverySessionLease{ProjectID: projectID, SessionID: sessionID, AgentIncarnation: incarnation, LeaseOwner: owner, LeaseToken: token, LeaseExpires: expires}
	}
	return lease, acquired, err
}

func (c *Client) ListPendingAgentInputDeliveryIntents(ctx context.Context, projectID string, now time.Time, limit int) ([]AgentInputDeliveryIntent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(lockCtx context.Context) error {
			_, err := db.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='expired',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE project_id=? AND state IN ('queued','leased','ambiguous') AND expires_at IS NOT NULL AND expires_at<=?`, formatTimestamp(now), projectID, formatTimestamp(now))
			return err
		})
	}); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT intent_key FROM agent_input_delivery_intents WHERE project_id=? AND state IN ('queued','leased') AND (expires_at IS NULL OR expires_at>?) AND (state='queued' OR lease_expires_at<=?) ORDER BY created_at,intent_key LIMIT ?`, projectID, formatTimestamp(now), formatTimestamp(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	intents := make([]AgentInputDeliveryIntent, 0, len(keys))
	for _, key := range keys {
		intent, err := c.loadAgentInputDeliveryIntent(ctx, db, projectID, key)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
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
				project_id,intent_key,session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,
				tool,message_kind,payload,state,expires_at,created_at,updated_at
			) VALUES(?,?,?,?,?,?,?,?,?,?, 'queued',?,?,?) ON CONFLICT(project_id,intent_key) DO NOTHING`,
				strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.IntentKey), strings.TrimSpace(request.SessionID),
				strings.TrimSpace(string(request.Target.LogicalPaneID)), strings.TrimSpace(request.Target.TmuxPaneID), request.Target.PanePID,
				strings.TrimSpace(request.Target.AgentIncarnation), strings.TrimSpace(request.Tool), string(request.Kind), request.Payload,
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
			if intent.State == "leased" && now.Before(intent.LeaseExpires) {
				return tx.Commit()
			}
			token, err := randomAgentInputToken()
			if err != nil {
				return err
			}
			expires := now.Add(leaseDuration)
			result, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_intents SET state='leased',lease_owner=?,lease_token=?,lease_expires_at=?,attempt_count=attempt_count+1,updated_at=? WHERE project_id=? AND intent_key=? AND state IN ('queued','leased') AND (state='queued' OR lease_expires_at<=?)`, owner, token, formatTimestamp(expires), formatTimestamp(now), projectID, intentKey, formatTimestamp(now))
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
func (c *Client) BeginAgentInputDeliverySubmission(ctx context.Context, projectID, intentKey, intentLeaseToken, sessionID, incarnation, sessionLeaseToken string, now time.Time, leaseDuration time.Duration) (time.Time, bool, error) {
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
			sessionResult, err := tx.ExecContext(lockCtx, `UPDATE agent_input_delivery_session_leases SET lease_expires_at=?,updated_at=? WHERE project_id=? AND session_id=? AND agent_incarnation=? AND lease_token=? AND julianday(lease_expires_at)>julianday(?)`, formatTimestamp(expires), formatTimestamp(now), projectID, sessionID, incarnation, sessionLeaseToken, formatTimestamp(now))
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
	err := q.QueryRowContext(ctx, `SELECT session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,tool,message_kind,payload,state,expires_at,COALESCE(lease_owner,''),COALESCE(lease_token,''),lease_expires_at,attempt_count,acknowledged_at FROM agent_input_delivery_intents WHERE project_id=? AND intent_key=?`, projectID, intentKey).Scan(
		&out.Request.SessionID, &logicalPane, &out.Request.Target.TmuxPaneID, &out.Request.Target.PanePID, &out.Request.Target.AgentIncarnation, &out.Request.Tool, &kind, &out.Request.Payload, &out.State, &expires, &out.LeaseOwner, &out.LeaseToken, &leaseExpires, &out.AttemptCount, &acknowledged)
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
func randomAgentInputToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
