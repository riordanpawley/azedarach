package issues

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// LearningPortfolioHealth deterministically aggregates privacy-safe audit fields.
func (c *Client) LearningPortfolioHealth(ctx context.Context, projectID string, now time.Time) (domain.LearningPortfolioHealth, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.LearningPortfolioHealth{}, err
	}
	now = now.UTC()
	out := domain.LearningPortfolioHealth{ProjectID: projectID, GeneratedAt: now.Format(time.RFC3339Nano)}
	var oldest, totalAge sql.NullFloat64
	err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(AVG((julianday(?) - julianday(created_at))*24),0), COALESCE(MAX((julianday(?) - julianday(created_at))*24),0) FROM agent_learnings WHERE project_id=? AND status='candidate' AND deleted_at IS NULL`, formatTimestamp(now), formatTimestamp(now), projectID).Scan(&out.CandidateCount, &totalAge, &oldest)
	if err != nil {
		return out, fmt.Errorf("candidate health: %w", err)
	}
	out.CandidateAgeAverageHours, out.CandidateAgeMaximumHours = totalAge.Float64, oldest.Float64
	var duplicates, publicCount int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learning_suggestions s JOIN agent_learnings l ON l.id=s.left_learning_id JOIN agent_learnings r ON r.id=s.right_learning_id WHERE s.project_id=? AND s.kind='duplicate' AND s.status='pending' AND l.evidence_private=0 AND r.evidence_private=0 AND l.deleted_at IS NULL AND r.deleted_at IS NULL AND l.consolidated_into_id IS NULL AND r.consolidated_into_id IS NULL`, projectID).Scan(&duplicates); err != nil {
		return out, fmt.Errorf("duplicate health: %w", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learnings WHERE project_id=? AND evidence_private=0 AND deleted_at IS NULL AND consolidated_into_id IS NULL`, projectID).Scan(&publicCount); err != nil {
		return out, err
	}
	pairs := publicCount * (publicCount - 1) / 2
	out.DuplicateDensity = domain.NewLearningHealthRate(duplicates, pairs)
	var labeled, useful, contradicted int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(outcome IN ('helpful','followed')),0), COALESCE(SUM(outcome='contradicted'),0) FROM learning_activation_outcomes o JOIN learning_activations a ON a.activation_id=o.activation_id WHERE a.project_id=? AND outcome<>'unknown'`, projectID).Scan(&labeled, &useful, &contradicted); err != nil {
		return out, fmt.Errorf("outcome health: %w", err)
	}
	out.UsefulnessRate, out.ContradictionRate = domain.NewLearningHealthRate(useful, labeled), domain.NewLearningHealthRate(contradicted, labeled)
	var reviewed, promoted int
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(reviewed_at IS NOT NULL),0), COALESCE(SUM(promoted_at IS NOT NULL),0) FROM agent_learnings WHERE project_id=? AND deleted_at IS NULL`, projectID).Scan(&reviewed, &promoted); err != nil {
		return out, err
	}
	out.PromotionThroughput = domain.NewLearningHealthRate(promoted, reviewed)
	var eligible, deliveredUnique int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(recall_count),0) FROM agent_learnings WHERE project_id=? AND evidence_private=0 AND consolidated_into_id IS NULL AND deleted_at IS NULL AND status IN ('accepted','promoted')`, projectID).Scan(&eligible, &out.SelectionCount); err != nil {
		return out, err
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT d.learning_id) FROM learning_activation_deliveries d JOIN agent_learnings l ON l.local_id=d.learning_id WHERE d.project_id=? AND l.evidence_private=0`, projectID).Scan(&deliveredUnique); err != nil {
		return out, err
	}
	out.ContextualCoverage = domain.NewLearningHealthRate(deliveredUnique, eligible)
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(token_cost),0) FROM learning_activations WHERE project_id=?`, projectID).Scan(&out.DeliveryCount, &out.DeliveredTokenCost); err != nil {
		return out, err
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT o.activation_id) FROM learning_activation_outcomes o JOIN learning_activations a ON a.activation_id=o.activation_id WHERE a.project_id=? AND o.outcome IN ('helpful','followed')`, projectID).Scan(&out.UsefulDeliveryCount); err != nil {
		return out, fmt.Errorf("useful activation health: %w", err)
	}
	out.TokensPerUsefulActivation = domain.NewLearningHealthRate(out.DeliveredTokenCost, out.UsefulDeliveryCount)
	return out, nil
}
