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
	_, _, err = client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ActivationID: a.ActivationID, IdempotencyKey: "one", Outcome: domain.LearningOutcomeHelpful, Source: domain.LearningOutcomeExplicit})
	require.NoError(t, err)
	health, err := client.LearningPortfolioHealth(ctx, "proj", time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, 3, health.SelectionCount, "recall selections must remain separate from delivery")
	require.Equal(t, 1, health.DeliveryCount)
	require.Equal(t, domain.NewLearningHealthRate(1, 1), health.UsefulnessRate)
	require.Equal(t, domain.NewLearningHealthRate(1, 1), health.ContextualCoverage)
	require.Equal(t, domain.NewLearningHealthRate(12, 1), health.TokensPerUsefulActivation)
}
