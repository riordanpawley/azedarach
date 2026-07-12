package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const MaxLearningActivationTokens = 32768

type LearningActivation struct {
	ActivationID, ProjectID, Surface, ContextFingerprint string
	LearningIDs                                          []string
	TokenCost                                            int
	Explanation                                          string
	DeliveredAt                                          time.Time
}
type LearningActivationOutcome struct {
	ActivationID, IdempotencyKey string
	Outcome                      domain.LearningActivationOutcome
	Source                       domain.LearningOutcomeSource
	Explanation                  string
	RecordedAt                   time.Time
}
type RecordLearningActivationParams struct {
	ProjectID, Surface, ContextFingerprint, Purpose, SessionID string
	LearningIDs                                                []string
	TokenCost                                                  int
	Explanation                                                string
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
	ids := make([]string, 0, len(p.LearningIDs))
	seen := map[string]struct{}{}
	for _, id := range p.LearningIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		var private int
		err = db.QueryRowContext(ctx, `SELECT evidence_private FROM agent_learnings WHERE local_id = ? AND project_id = ? AND deleted_at IS NULL`, id, p.ProjectID).Scan(&private)
		if err != nil {
			return LearningActivation{}, fmt.Errorf("validate activation learning %s: %w", id, err)
		}
		if private != 0 {
			return LearningActivation{}, fmt.Errorf("private learning %s cannot be activated", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
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
	if in.ActivationID == "" || in.IdempotencyKey == "" || !in.Outcome.Valid() || !in.Source.Valid() {
		return LearningActivationOutcome{}, false, errors.New("feedback requires activation id, idempotency key, valid outcome, and source")
	}
	db, err := c.dbHandle()
	if err != nil {
		return LearningActivationOutcome{}, false, err
	}
	in.RecordedAt = time.Now().UTC()
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO learning_activation_outcomes(activation_id,idempotency_key,outcome,source,explanation,recorded_at) VALUES(?,?,?,?,?,?)`, in.ActivationID, in.IdempotencyKey, in.Outcome, in.Source, in.Explanation, formatTimestamp(in.RecordedAt))
	if err != nil {
		return LearningActivationOutcome{}, false, fmt.Errorf("record activation outcome: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return in, true, nil
	}
	var at string
	err = db.QueryRowContext(ctx, `SELECT outcome,source,explanation,recorded_at FROM learning_activation_outcomes WHERE activation_id=? AND idempotency_key=?`, in.ActivationID, in.IdempotencyKey).Scan(&in.Outcome, &in.Source, &in.Explanation, &at)
	if err != nil {
		return LearningActivationOutcome{}, false, err
	}
	in.RecordedAt = parseTimestamp(at)
	return in, false, nil
}
