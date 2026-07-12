package issues

import (
	"context"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLearningPortfolioHealthUsesExplicitDeliveryDenominators(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "public", Evidence: "safe"})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET status='accepted', reviewed_at=?, recall_count=3 WHERE local_id=?`, formatTimestamp(time.Now().UTC()), learning.LocalID)
	require.NoError(t, err)
	a, err := client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "prime", ContextFingerprint: "sha256:x", Purpose: "session_start", SessionID: "s-1", LearningIDs: []string{learning.LocalID}, TokenCost: 12})
	require.NoError(t, err)
	_, _, err = client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ProjectID: "proj", ActivationID: a.ActivationID, IdempotencyKey: "one", Outcome: domain.LearningOutcomeHelpful, Source: domain.LearningOutcomeExplicit})
	require.NoError(t, err)
	health, err := client.LearningPortfolioHealth(ctx, "proj", time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, 0, health.SelectionCount, "recall_count and direct/manual delivery are not contextual selection proposals")
	require.Equal(t, 1, health.DeliveryCount)
	require.Equal(t, domain.NewLearningHealthRate(1, 1), health.UsefulnessRate)
	require.Equal(t, domain.NewLearningHealthRate(1, 1), health.ContextualCoverage)
	require.Equal(t, domain.NewLearningHealthRate(12, 1), health.TokensPerUsefulActivation)
}

func TestLearningPortfolioHealthUsesProposalConfirmationAndExclusionLedger(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "public", Evidence: "safe"})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET status='accepted',reviewed_at=? WHERE local_id=?`, formatTimestamp(now), learning.LocalID)
	require.NoError(t, err)
	confirmed, err := client.ProposeLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "prime", ContextFingerprint: "sha256:confirmed", Purpose: "session_start", SessionID: "s-1", LearningIDs: []string{learning.LocalID}})
	require.NoError(t, err)
	_, err = client.ConfirmLearningActivation(ctx, "proj", confirmed.ActivationID, 7)
	require.NoError(t, err)
	abandoned, err := client.ProposeLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "prime", ContextFingerprint: "sha256:lost", Purpose: "session_start", SessionID: "s-2", LearningIDs: []string{learning.LocalID}})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE learning_activation_proposals SET proposed_at=? WHERE activation_id=?`, formatTimestamp(now.Add(-25*time.Hour)), abandoned.ActivationID)
	require.NoError(t, err)
	require.NoError(t, client.RecordLearningActivationExclusion(ctx, "proj", "prime", "session_start", "s-3", "suppressed", 1))
	require.NoError(t, client.RecordLearningActivationExclusion(ctx, "proj", "prime", "session_start", "s-4", "budget", 1))
	health, err := client.LearningPortfolioHealth(ctx, "proj", now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, health.ProposalCount)
	require.Equal(t, 2, health.SelectionCount)
	require.Equal(t, 1, health.DeliveryCount)
	require.Equal(t, 1, health.AbandonedProposalCount)
	require.Zero(t, health.PendingProposalCount)
	require.Equal(t, 1, health.SuppressionExclusionCount)
	require.Equal(t, 1, health.BudgetExclusionCount)
	require.Equal(t, 30, health.WindowDays)
}

func TestLearningPortfolioHealthCoverageUsesCurrentRecallEligibility(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)
	now := time.Now().UTC()
	ids := make(map[string]string)
	for _, name := range []string{"active", "stale", "superseded", "consolidated", "deleted", "inactive-target"} {
		learning, createErr := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: name, Evidence: "safe"})
		require.NoError(t, createErr)
		ids[name] = learning.LocalID
		_, updateErr := db.ExecContext(ctx, `UPDATE agent_learnings SET status='accepted', reviewed_at=? WHERE local_id=?`, formatTimestamp(now), learning.LocalID)
		require.NoError(t, updateErr)
		_, activateErr := client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "prime", ContextFingerprint: "sha256:" + name, Purpose: "session_start", SessionID: "session-" + name, LearningIDs: []string{learning.LocalID}, TokenCost: 1})
		require.NoError(t, activateErr)
	}
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET status='stale', stale_at=? WHERE local_id=?`, formatTimestamp(now), ids["stale"])
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET superseded_at=? WHERE local_id=?`, formatTimestamp(now), ids["superseded"])
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET consolidated_into_id=(SELECT id FROM agent_learnings WHERE local_id=?) WHERE local_id=?`, ids["active"], ids["consolidated"])
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET deleted_at=? WHERE local_id=?`, formatTimestamp(now), ids["deleted"])
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET status='promoted', promotion_target='agents', promotion_target_id='AGENTS.md', target_state='retired', target_retired_at=? WHERE local_id=?`, formatTimestamp(now), ids["inactive-target"])
	require.NoError(t, err)

	health, err := client.LearningPortfolioHealth(ctx, "proj", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, domain.NewLearningHealthRate(1, 1), health.ContextualCoverage)
	require.LessOrEqual(t, health.ContextualCoverage.Numerator, health.ContextualCoverage.Denominator)
	require.LessOrEqual(t, health.ContextualCoverage.Value, 1.0)
}
