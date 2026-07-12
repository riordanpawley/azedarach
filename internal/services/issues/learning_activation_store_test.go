package issues

import (
	"context"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLearningActivationDeliveryFeedbackAndPrivacy(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	public, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "public", Evidence: "safe"})
	require.NoError(t, err)
	private, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "private", Evidence: "secret", EvidencePrivate: true})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	var tables int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('learning_activations','learning_activation_outcomes')`).Scan(&tables))
	require.Equal(t, 2, tables)
	_, err = client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "primer", ContextFingerprint: "sha256:test", LearningIDs: []string{private.LocalID}, TokenCost: 1})
	require.ErrorContains(t, err, "private learning")
	a, err := client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "primer", ContextFingerprint: "sha256:test", LearningIDs: []string{public.LocalID, public.LocalID}, TokenCost: 17, Explanation: "issue match"})
	require.NoError(t, err)
	require.Len(t, a.LearningIDs, 1)
	require.NotEmpty(t, a.ActivationID)
	in := LearningActivationOutcome{ActivationID: a.ActivationID, IdempotencyKey: "turn-1", Outcome: domain.LearningOutcomeHelpful, Source: domain.LearningOutcomeExplicit, Explanation: "used it"}
	first, created, err := client.RecordLearningActivationOutcome(ctx, in)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.LearningOutcomeExplicit, first.Source)
	second, created, err := client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ActivationID: a.ActivationID, IdempotencyKey: "turn-1", Outcome: domain.LearningOutcomeContradicted, Source: domain.LearningOutcomeInferred})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, domain.LearningOutcomeHelpful, second.Outcome, "dedup must preserve original metric")
	var activations, outcomes int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM learning_activations`).Scan(&activations))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM learning_activation_outcomes`).Scan(&outcomes))
	require.Equal(t, 1, activations)
	require.Equal(t, 1, outcomes)
}
