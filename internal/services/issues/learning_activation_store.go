package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	MaxLearningActivationTokens = 32768
)

type LearningActivation struct {
	ActivationID, ProjectID, Surface, ContextFingerprint string
	LearningIDs                                          []string
	TokenCost                                            int
	Explanation                                          string
	DeliveredAt                                          time.Time
}
type LearningActivationOutcome struct {
	ActivationID, ProjectID, IdempotencyKey string
	Outcome                                 domain.LearningActivationOutcome
	Source                                  domain.LearningOutcomeSource
	ResolvedOutcome                         domain.LearningActivationOutcome
	ResolvedSource                          domain.LearningOutcomeSource
	Explanation                             string
	RecordedAt                              time.Time
}
type RecordLearningActivationParams struct {
	ProjectID, Surface, ContextFingerprint, Purpose, SessionID string
	LearningIDs                                                []string
	TokenCost                                                  int
	Explanation                                                string
}

type LearningActivationProposal struct {
	ActivationID, ProjectID, Surface, ContextFingerprint, Purpose, SessionID, Explanation string
	LearningIDs                                                                           []string
	ProposedAt                                                                            time.Time
}

func (c *Client) ProposeLearningActivation(ctx context.Context, p RecordLearningActivationParams) (LearningActivationProposal, error) {
	p.ProjectID, p.Surface, p.ContextFingerprint = strings.TrimSpace(p.ProjectID), strings.TrimSpace(p.Surface), strings.TrimSpace(p.ContextFingerprint)
	if p.ProjectID == "" || p.Surface == "" || p.ContextFingerprint == "" || strings.TrimSpace(p.SessionID) == "" || len(p.LearningIDs) == 0 {
		return LearningActivationProposal{}, errors.New("proposal requires project, surface, context fingerprint, session, and learning ids")
	}
	db, err := c.dbHandle()
	if err != nil {
		return LearningActivationProposal{}, err
	}
	ids, err := c.validateActiveLearningIDs(ctx, db, p.ProjectID, p.LearningIDs)
	if err != nil {
		return LearningActivationProposal{}, err
	}
	b, _ := json.Marshal(ids)
	now := time.Now().UTC()
	if _, err = db.ExecContext(ctx, `DELETE FROM learning_activation_proposals WHERE project_id=? AND proposed_at<?`, p.ProjectID, formatTimestamp(domain.LearningActivationProposalExpiry(now))); err != nil {
		return LearningActivationProposal{}, fmt.Errorf("expire learning activation proposals: %w", err)
	}
	out := LearningActivationProposal{ActivationID: "act-" + uuid.NewString(), ProjectID: p.ProjectID, Surface: p.Surface, ContextFingerprint: p.ContextFingerprint, Purpose: strings.TrimSpace(p.Purpose), SessionID: strings.TrimSpace(p.SessionID), Explanation: strings.TrimSpace(p.Explanation), LearningIDs: ids, ProposedAt: now}
	_, err = db.ExecContext(ctx, `INSERT INTO learning_activation_proposals(activation_id,project_id,surface,context_fingerprint,learning_ids_json,explanation,purpose,session_id,proposed_at) VALUES(?,?,?,?,?,?,?,?,?)`, out.ActivationID, out.ProjectID, out.Surface, out.ContextFingerprint, string(b), out.Explanation, out.Purpose, out.SessionID, formatTimestamp(now))
	if err != nil {
		return LearningActivationProposal{}, fmt.Errorf("record learning activation proposal: %w", err)
	}
	return out, nil
}

func (c *Client) ConfirmLearningActivation(ctx context.Context, projectID, activationID string, tokenCost int) (LearningActivation, error) {
	projectID = strings.TrimSpace(projectID)
	activationID = strings.TrimSpace(activationID)
	if projectID == "" || activationID == "" || tokenCost < 0 || tokenCost > MaxLearningActivationTokens {
		return LearningActivation{}, errors.New("valid project, activation id, and token cost required")
	}
	db, err := c.dbHandle()
	if err != nil {
		return LearningActivation{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LearningActivation{}, err
	}
	defer tx.Rollback()
	var out LearningActivation
	var idsJSON, purpose, sessionID, proposedAt string
	err = tx.QueryRowContext(ctx, `SELECT project_id,surface,context_fingerprint,learning_ids_json,explanation,purpose,session_id,proposed_at FROM learning_activation_proposals WHERE activation_id=? AND project_id=?`, activationID, projectID).Scan(&out.ProjectID, &out.Surface, &out.ContextFingerprint, &idsJSON, &out.Explanation, &purpose, &sessionID, &proposedAt)
	if errors.Is(err, sql.ErrNoRows) {
		var delivered string
		err = tx.QueryRowContext(ctx, `SELECT project_id,surface,context_fingerprint,learning_ids_json,token_cost,explanation,delivered_at FROM learning_activations WHERE activation_id=? AND project_id=?`, activationID, projectID).Scan(&out.ProjectID, &out.Surface, &out.ContextFingerprint, &idsJSON, &out.TokenCost, &out.Explanation, &delivered)
		if err != nil {
			return LearningActivation{}, fmt.Errorf("activation proposal not found: %w", err)
		}
		out.ActivationID = activationID
		out.DeliveredAt = parseTimestamp(delivered)
		_ = json.Unmarshal([]byte(idsJSON), &out.LearningIDs)
		return out, nil
	}
	if err != nil {
		return LearningActivation{}, err
	}
	_ = json.Unmarshal([]byte(idsJSON), &out.LearningIDs)
	predicate, predicateArgs := learningActiveSQL("", time.Now().UTC())
	for _, id := range out.LearningIDs {
		args := append([]any{id, out.ProjectID}, predicateArgs...)
		var found string
		if err = tx.QueryRowContext(ctx, `SELECT local_id FROM agent_learnings WHERE local_id=? AND project_id=? AND evidence_private=0 AND `+predicate, args...).Scan(&found); err != nil {
			return LearningActivation{}, fmt.Errorf("proposal learning %s is no longer canonically active: %w", id, err)
		}
	}
	out.ActivationID = activationID
	out.TokenCost = tokenCost
	out.DeliveredAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO learning_activations(activation_id,project_id,surface,context_fingerprint,learning_ids_json,token_cost,explanation,delivered_at,purpose,session_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, out.ActivationID, out.ProjectID, out.Surface, out.ContextFingerprint, idsJSON, tokenCost, out.Explanation, formatTimestamp(out.DeliveredAt), purpose, sessionID)
	if err != nil {
		return LearningActivation{}, fmt.Errorf("confirm activation: %w", err)
	}
	for _, id := range out.LearningIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO learning_activation_deliveries(activation_id,project_id,session_id,learning_id) VALUES(?,?,?,?)`, activationID, out.ProjectID, sessionID, id); err != nil {
			return LearningActivation{}, fmt.Errorf("confirm delivery %s: %w", id, classifySQLiteConstraint(err))
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM learning_activation_proposals WHERE activation_id=?`, activationID); err != nil {
		return LearningActivation{}, err
	}
	if err = tx.Commit(); err != nil {
		return LearningActivation{}, err
	}
	return out, nil
}

func (c *Client) validateActiveLearningIDs(ctx context.Context, db *sql.DB, projectID string, input []string) ([]string, error) {
	ids := make([]string, 0, len(input))
	seen := map[string]struct{}{}
	predicate, predicateArgs := learningActiveSQL("", time.Now().UTC())
	for _, id := range input {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		var found string
		args := append([]any{id, projectID}, predicateArgs...)
		err := db.QueryRowContext(ctx, `SELECT local_id FROM agent_learnings WHERE local_id=? AND project_id=? AND evidence_private=0 AND `+predicate, args...).Scan(&found)
		if err != nil {
			return nil, fmt.Errorf("learning %s is not canonically active: %w", id, err)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("activation requires active learning ids")
	}
	return ids, nil
}

func (c *Client) RecordLearningActivation(ctx context.Context, p RecordLearningActivationParams) (LearningActivation, error) {
	p.ProjectID, p.Surface, p.ContextFingerprint, p.Explanation = strings.TrimSpace(p.ProjectID), strings.TrimSpace(p.Surface), strings.TrimSpace(p.ContextFingerprint), strings.TrimSpace(p.Explanation)
	if p.ProjectID == "" || p.Surface == "" || p.ContextFingerprint == "" || len(p.LearningIDs) == 0 {
		return LearningActivation{}, fmt.Errorf("activation requires project, surface, context fingerprint, and learning ids")
	}
	if p.TokenCost < 0 || p.TokenCost > MaxLearningActivationTokens {
		return LearningActivation{}, fmt.Errorf("activation token cost must be between 0 and %d", MaxLearningActivationTokens)
	}
	db, err := c.dbHandle()
	if err != nil {
		return LearningActivation{}, err
	}
	ids, err := c.validateActiveLearningIDs(ctx, db, p.ProjectID, p.LearningIDs)
	if err != nil {
		return LearningActivation{}, err
	}
	if len(ids) == 0 {
		return LearningActivation{}, errors.New("activation requires learning ids")
	}
	b, _ := json.Marshal(ids)
	now := time.Now().UTC()
	out := LearningActivation{ActivationID: "act-" + uuid.NewString(), ProjectID: p.ProjectID, Surface: p.Surface, ContextFingerprint: p.ContextFingerprint, LearningIDs: ids, TokenCost: p.TokenCost, Explanation: p.Explanation, DeliveredAt: now}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LearningActivation{}, fmt.Errorf("begin learning activation: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO learning_activations(activation_id,project_id,surface,context_fingerprint,learning_ids_json,token_cost,explanation,delivered_at,purpose,session_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, out.ActivationID, out.ProjectID, out.Surface, out.ContextFingerprint, string(b), out.TokenCost, out.Explanation, formatTimestamp(now), strings.TrimSpace(p.Purpose), strings.TrimSpace(p.SessionID))
	if err != nil {
		return LearningActivation{}, fmt.Errorf("record learning activation: %w", err)
	}
	if sessionID := strings.TrimSpace(p.SessionID); sessionID != "" {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO learning_activation_deliveries(activation_id,project_id,session_id,learning_id) VALUES(?,?,?,?)`, out.ActivationID, out.ProjectID, sessionID, id); err != nil {
				return LearningActivation{}, fmt.Errorf("record session learning delivery %s: %w", id, classifySQLiteConstraint(err))
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return LearningActivation{}, fmt.Errorf("commit learning activation: %w", err)
	}
	return out, nil
}

func (c *Client) DeliveredLearningIDs(ctx context.Context, projectID, sessionID string) (map[string]struct{}, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT learning_id FROM learning_activation_deliveries WHERE project_id=? AND session_id=?`, strings.TrimSpace(projectID), strings.TrimSpace(sessionID))
	if err != nil {
		return nil, fmt.Errorf("list session learning activations: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func (c *Client) RecordLearningActivationOutcome(ctx context.Context, in LearningActivationOutcome) (LearningActivationOutcome, bool, error) {
	in.ActivationID, in.IdempotencyKey, in.Explanation = strings.TrimSpace(in.ActivationID), strings.TrimSpace(in.IdempotencyKey), strings.TrimSpace(in.Explanation)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if in.ProjectID == "" || in.ActivationID == "" || in.IdempotencyKey == "" || !in.Outcome.Valid() || !in.Source.Valid() {
		return LearningActivationOutcome{}, false, errors.New("feedback requires project, activation id, idempotency key, valid outcome, and source")
	}
	db, err := c.dbHandle()
	if err != nil {
		return LearningActivationOutcome{}, false, err
	}
	in.RecordedAt = time.Now().UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LearningActivationOutcome{}, false, fmt.Errorf("begin activation outcome: %w", err)
	}
	defer tx.Rollback()
	var activationProject string
	if err = tx.QueryRowContext(ctx, `SELECT project_id FROM learning_activations WHERE activation_id=? AND project_id=?`, in.ActivationID, in.ProjectID).Scan(&activationProject); err != nil {
		return LearningActivationOutcome{}, false, fmt.Errorf("activation not found in project: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO learning_activation_outcomes(activation_id,idempotency_key,outcome,source,explanation,recorded_at) VALUES(?,?,?,?,?,?)`, in.ActivationID, in.IdempotencyKey, in.Outcome, in.Source, in.Explanation, formatTimestamp(in.RecordedAt))
	if err != nil {
		return LearningActivationOutcome{}, false, fmt.Errorf("record activation outcome: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		priority := in.Source.ResolutionPriority()
		_, err = tx.ExecContext(ctx, `UPDATE learning_activations SET resolved_outcome=?, resolved_source=?, resolved_priority=? WHERE activation_id=? AND ?>resolved_priority`, in.Outcome, in.Source, priority, in.ActivationID, priority)
		if err != nil {
			return LearningActivationOutcome{}, false, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT resolved_outcome,resolved_source FROM learning_activations WHERE activation_id=?`, in.ActivationID).Scan(&in.ResolvedOutcome, &in.ResolvedSource); err != nil {
			return LearningActivationOutcome{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return LearningActivationOutcome{}, false, err
		}
		return in, true, nil
	}
	var at string
	err = tx.QueryRowContext(ctx, `SELECT outcome,source,explanation,recorded_at FROM learning_activation_outcomes WHERE activation_id=? AND idempotency_key=?`, in.ActivationID, in.IdempotencyKey).Scan(&in.Outcome, &in.Source, &in.Explanation, &at)
	if err != nil {
		return LearningActivationOutcome{}, false, err
	}
	in.RecordedAt = parseTimestamp(at)
	if err = tx.QueryRowContext(ctx, `SELECT resolved_outcome,resolved_source FROM learning_activations WHERE activation_id=?`, in.ActivationID).Scan(&in.ResolvedOutcome, &in.ResolvedSource); err != nil {
		return LearningActivationOutcome{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return LearningActivationOutcome{}, false, err
	}
	return in, false, nil
}
