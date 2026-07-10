package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type InteractionIssueChanges struct {
	Title, Description, Design, Acceptance *string
	Priority                               *int
}
type InteractionDecisionEffect struct{ Title, Rationale, Context, Consequences string }
type InteractionResolution struct {
	Request          domain.InteractionRequest
	ExpectedRevision int64
	IssueChanges     InteractionIssueChanges
	Decision         *InteractionDecisionEffect
}

// CreateInteraction persists a new durable decision request.
func (c *Client) CreateInteraction(ctx context.Context, request domain.InteractionRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate interaction: %w", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal interaction: %w", err)
	}
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO interaction_requests
		(id, issue_id, decision_key, state, revision, request_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, request.ID, request.IssueID, request.DecisionKey,
		request.State, request.Revision, raw, request.CreatedAt.UTC().Format(time.RFC3339Nano), request.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			if _, found, lookupErr := c.GetInteraction(ctx, request.ID); lookupErr == nil && found {
				return fmt.Errorf("interaction %s already exists", request.ID)
			}
			return fmt.Errorf("%w: issue %s decision key %s", domain.ErrDuplicateUnresolvedDecision, request.IssueID, request.DecisionKey)
		}
		return fmt.Errorf("create interaction %s: %w", request.ID, err)
	}
	return c.RefreshInteractionProjection(ctx)
}

// UpdateInteraction applies an optimistic-revision replacement.
func (c *Client) UpdateInteraction(ctx context.Context, request domain.InteractionRequest, expectedRevision int64) error {
	if expectedRevision < 1 || request.Revision != expectedRevision+1 {
		return fmt.Errorf("%w: expected replacement revision %d", domain.ErrStaleInteractionRevision, expectedRevision+1)
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate interaction: %w", err)
	}
	current, found, err := c.GetInteraction(ctx, request.ID)
	if err != nil {
		return err
	}
	if !found || current.Revision != expectedRevision {
		return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, request.ID, expectedRevision)
	}
	if current.IssueID != request.IssueID || current.DecisionKey != request.DecisionKey || !current.CreatedAt.Equal(request.CreatedAt) {
		return fmt.Errorf("interaction identity and creation audit are immutable")
	}
	if _, err := current.Transition(request.State, expectedRevision, request.UpdatedAt); err != nil {
		return fmt.Errorf("validate interaction transition: %w", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal interaction: %w", err)
	}
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE interaction_requests SET state=?, revision=?, request_json=?, updated_at=? WHERE id=? AND revision=?`, request.State, request.Revision, raw, request.UpdatedAt.UTC().Format(time.RFC3339Nano), request.ID, expectedRevision)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return fmt.Errorf("%w: issue %s decision key %s", domain.ErrDuplicateUnresolvedDecision, request.IssueID, request.DecisionKey)
		}
		return fmt.Errorf("update interaction %s: %w", request.ID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, request.ID, expectedRevision)
	}
	return c.RefreshInteractionProjection(ctx)
}

// RefreshInteractionProjection reloads the complete durable projection before evaluation.
func (c *Client) RefreshInteractionProjection(ctx context.Context) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT request_json FROM interaction_requests ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("refresh interaction projection: %w", err)
	}
	defer rows.Close()
	next := make(map[string]domain.InteractionRequest)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var r domain.InteractionRequest
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("decode interaction projection: %w", err)
		}
		next[r.ID] = r
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.interactionMu.Lock()
	c.interactionCache = next
	c.interactionMu.Unlock()
	return nil
}

func (c *Client) GetInteraction(ctx context.Context, id string) (domain.InteractionRequest, bool, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return domain.InteractionRequest{}, false, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	r, ok := c.interactionCache[strings.TrimSpace(id)]
	return r, ok, nil
}

func (c *Client) InteractionByDecisionKey(ctx context.Context, issueID, decisionKey string) (domain.InteractionRequest, bool, error) {
	requests, err := c.InteractionsForIssue(ctx, issueID)
	if err != nil {
		return domain.InteractionRequest{}, false, err
	}
	for _, r := range requests {
		if r.DecisionKey == decisionKey && r.Unresolved() {
			return r, true, nil
		}
	}
	return domain.InteractionRequest{}, false, nil
}

func (c *Client) InteractionsForIssue(ctx context.Context, issueID string) ([]domain.InteractionRequest, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make([]domain.InteractionRequest, 0)
	for _, r := range c.interactionCache {
		if r.IssueID == issueID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (c *Client) ListInteractions(ctx context.Context) ([]domain.InteractionRequest, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make([]domain.InteractionRequest, 0, len(c.interactionCache))
	for _, r := range c.interactionCache {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ResolveInteraction atomically applies explicitly supplied effects and resolves the request.
func (c *Client) ResolveInteraction(ctx context.Context, in InteractionResolution) error {
	if in.ExpectedRevision < 1 || in.Request.Revision != in.ExpectedRevision+1 {
		return fmt.Errorf("%w: expected replacement revision %d", domain.ErrStaleInteractionRevision, in.ExpectedRevision+1)
	}
	if err := in.Request.Validate(); err != nil {
		return fmt.Errorf("validate interaction: %w", err)
	}
	if in.IssueChanges.Title != nil && strings.TrimSpace(*in.IssueChanges.Title) == "" {
		return fmt.Errorf("approved issue title must be non-empty")
	}
	if in.IssueChanges.Priority != nil && (*in.IssueChanges.Priority < 0 || *in.IssueChanges.Priority > 4) {
		return fmt.Errorf("approved issue priority is invalid")
	}
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var raw []byte
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT request_json, revision FROM interaction_requests WHERE id=?`, in.Request.ID).Scan(&raw, &revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		if revision != in.ExpectedRevision {
			return fmt.Errorf("%w: interaction %s expected revision %d", domain.ErrStaleInteractionRevision, in.Request.ID, in.ExpectedRevision)
		}
		var current domain.InteractionRequest
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		current.FinalAnswer = in.Request.FinalAnswer
		current.Proposal = in.Request.Proposal
		current.Effects = in.Request.Effects
		if _, err := current.Transition(domain.InteractionResolved, in.ExpectedRevision, in.Request.UpdatedAt); err != nil {
			return err
		}
		if current.IssueID != in.Request.IssueID || current.DecisionKey != in.Request.DecisionKey || !current.CreatedAt.Equal(in.Request.CreatedAt) {
			return fmt.Errorf("interaction identity and creation audit are immutable")
		}
		ch := in.IssueChanges
		if interactionIssueChangesApproved(ch) {
			res, err := tx.ExecContext(ctx, `UPDATE issues SET title=CASE WHEN ? THEN ? ELSE title END,description=CASE WHEN ? THEN ? ELSE description END,design=CASE WHEN ? THEN ? ELSE design END,acceptance=CASE WHEN ? THEN ? ELSE acceptance END,priority=CASE WHEN ? THEN ? ELSE priority END,updated_at=? WHERE id=?`, ch.Title != nil, valueString(ch.Title), ch.Description != nil, valueString(ch.Description), ch.Design != nil, valueString(ch.Design), ch.Acceptance != nil, valueString(ch.Acceptance), ch.Priority != nil, valueInt(ch.Priority), in.Request.UpdatedAt.UTC().Format(time.RFC3339Nano), in.Request.IssueID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return domain.ErrNotFound
			}
			if err := c.appendIssueObservationEvent(ctx, tx, in.Request.IssueID, domain.IssueEventIssueDetailsChanged, map[string]any{"source": "interaction.resolve", "interaction_id": in.Request.ID}); err != nil {
				return err
			}
		}
		if in.Decision != nil {
			d := in.Decision
			if strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Rationale) == "" {
				return fmt.Errorf("decision title and rationale are required")
			}
			now := in.Request.UpdatedAt.UTC().Format(time.RFC3339Nano)
			result, err := tx.ExecContext(ctx, `INSERT INTO decisions(local_id,title,rationale,context,consequences,created_at,updated_at,deleted_at) VALUES('',?,?,?,?,?,?,NULL)`, d.Title, d.Rationale, d.Context, d.Consequences, now, now)
			if err != nil {
				return err
			}
			id, err := result.LastInsertId()
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE decisions SET local_id=? WHERE id=?`, fmt.Sprintf("dec-%d", id), id); err != nil {
				return err
			}
			decision := Decision{LocalID: fmt.Sprintf("dec-%d", id), Title: d.Title, Rationale: d.Rationale, Context: d.Context, Consequences: d.Consequences, CreatedAt: in.Request.UpdatedAt, UpdatedAt: in.Request.UpdatedAt}
			if err := c.insertDecisionAuditRow(ctx, tx, decisionEntityKind, decision.LocalID, decisionOpCreate, nil, decision); err != nil {
				return err
			}
		}
		nextRaw, err := json.Marshal(in.Request)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE interaction_requests SET state=?,revision=?,request_json=?,updated_at=? WHERE id=? AND revision=?`, in.Request.State, in.Request.Revision, nextRaw, in.Request.UpdatedAt.UTC().Format(time.RFC3339Nano), in.Request.ID, in.ExpectedRevision)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: interaction %s", domain.ErrStaleInteractionRevision, in.Request.ID)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.RefreshInteractionProjection(ctx)
	})
}
func interactionIssueChangesApproved(ch InteractionIssueChanges) bool {
	return ch.Title != nil || ch.Description != nil || ch.Design != nil || ch.Acceptance != nil || ch.Priority != nil
}
func valueString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func valueInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func (c *Client) IssueHasUnresolvedInteraction(ctx context.Context, issueID string) (bool, error) {
	requests, err := c.InteractionsForIssue(ctx, issueID)
	if err != nil {
		return false, err
	}
	return domain.IssueWaitingHuman(issueID, requests), nil
}

func (c *Client) UnresolvedInteractionIssueIDs(ctx context.Context) (map[string]struct{}, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make(map[string]struct{})
	for _, r := range c.interactionCache {
		if r.Unresolved() {
			out[r.IssueID] = struct{}{}
		}
	}
	return out, nil
}
