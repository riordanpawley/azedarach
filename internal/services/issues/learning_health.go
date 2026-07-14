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
	windowStart, windowEnd := domain.LearningHealthWindow(now)
	out := domain.LearningPortfolioHealth{ProjectID: projectID, GeneratedAt: now.Format(time.RFC3339Nano), WindowStart: windowStart.Format(time.RFC3339Nano), WindowEnd: windowEnd.Format(time.RFC3339Nano), WindowDays: domain.LearningHealthWindowDays}
	// Expiry is telemetry, not deletion: abandoned proposals remain auditable.
	if _, err = db.ExecContext(ctx, `UPDATE learning_activation_proposals SET status='abandoned' WHERE project_id=? AND status='proposed' AND proposed_at<?`, projectID, formatTimestamp(domain.LearningActivationProposalExpiry(now))); err != nil {
		return out, fmt.Errorf("expire activation proposals: %w", err)
	}
	var oldest, totalAge sql.NullFloat64
	err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(AVG((julianday(?) - julianday(created_at))*24),0), COALESCE(MAX((julianday(?) - julianday(created_at))*24),0) FROM agent_learnings WHERE project_id=? AND status='candidate' AND evidence_private=0 AND deleted_at IS NULL`, formatTimestamp(now), formatTimestamp(now), projectID).Scan(&out.CandidateCount, &totalAge, &oldest)
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
	var labeled, useful, contradicted int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(resolved_outcome IN ('helpful','followed')),0), COALESCE(SUM(resolved_outcome='contradicted'),0) FROM learning_activations WHERE project_id=? AND delivered_at>=? AND resolved_outcome NOT IN ('','unknown')`, projectID, formatTimestamp(windowStart)).Scan(&labeled, &useful, &contradicted); err != nil {
		return out, fmt.Errorf("outcome health: %w", err)
	}
	var reviewed, promoted int
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(reviewed_at IS NOT NULL),0), COALESCE(SUM(promoted_at IS NOT NULL),0) FROM agent_learnings WHERE project_id=? AND evidence_private=0 AND deleted_at IS NULL`, projectID).Scan(&reviewed, &promoted); err != nil {
		return out, err
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learnings WHERE project_id=? AND evidence_private=0 AND deleted_at IS NULL AND promoted_at>=?`, projectID, formatTimestamp(windowStart)).Scan(&out.PromotionEventCount); err != nil {
		return out, err
	}
	var eligible, deliveredUnique int
	activePredicate, activeArgs := learningActiveSQL("l", now)
	populationArgs := append([]any{projectID}, activeArgs...)
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(l.recall_count),0) FROM agent_learnings l WHERE l.project_id=? AND l.evidence_private=0 AND `+activePredicate, populationArgs...).Scan(&eligible, &out.SelectionCount); err != nil {
		return out, err
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT d.learning_id) FROM learning_activation_deliveries d JOIN agent_learnings l ON l.local_id=d.learning_id WHERE d.project_id=? AND l.evidence_private=0 AND `+activePredicate, populationArgs...).Scan(&deliveredUnique); err != nil {
		return out, err
	}
	out.ContextualCoverage = domain.NewLearningHealthRate(deliveredUnique, eligible)
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(token_cost),0) FROM learning_activations WHERE project_id=? AND delivered_at>=?`, projectID, formatTimestamp(windowStart)).Scan(&out.DeliveryCount, &out.DeliveredTokenCost); err != nil {
		return out, err
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM learning_activations WHERE project_id=? AND delivered_at>=? AND resolved_outcome IN ('helpful','followed')`, projectID, formatTimestamp(windowStart)).Scan(&out.UsefulDeliveryCount); err != nil {
		return out, fmt.Errorf("useful activation health: %w", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status='abandoned'),0),COALESCE(SUM(status='proposed'),0) FROM learning_activation_proposals WHERE project_id=? AND proposed_at>=?`, projectID, formatTimestamp(windowStart)).Scan(&out.ProposalCount, &out.AbandonedProposalCount, &out.PendingProposalCount); err != nil {
		return out, fmt.Errorf("proposal health: %w", err)
	}
	out.SelectionCount = out.ProposalCount
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN reason='suppressed' THEN learning_count ELSE 0 END),0),COALESCE(SUM(CASE WHEN reason='budget' THEN learning_count ELSE 0 END),0) FROM learning_activation_exclusions WHERE project_id=? AND recorded_at>=?`, projectID, formatTimestamp(windowStart)).Scan(&out.SuppressionExclusionCount, &out.BudgetExclusionCount); err != nil {
		return out, fmt.Errorf("exclusion health: %w", err)
	}
	domain.DeriveLearningHealthMetrics(&out, duplicates, pairs, useful, contradicted, labeled, reviewed, promoted)
	return out, nil
}
