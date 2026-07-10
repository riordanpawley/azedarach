package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

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

// Interactions returns the complete durable request projection in stable order.
func (c *Client) Interactions(ctx context.Context) ([]domain.InteractionRequest, error) {
	if err := c.RefreshInteractionProjection(ctx); err != nil {
		return nil, err
	}
	c.interactionMu.RLock()
	defer c.interactionMu.RUnlock()
	out := make([]domain.InteractionRequest, 0, len(c.interactionCache))
	for _, request := range c.interactionCache {
		out = append(out, request)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
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
