package issues

import (
	"context"
	"testing"
	"time"

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
	public, err = client.UpdateLearningStatus(ctx, public.LocalID, LearningStatusAccepted, "reviewed")
	require.NoError(t, err)
	private, err = client.UpdateLearningStatus(ctx, private.LocalID, LearningStatusAccepted, "reviewed")
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	var tables int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('learning_activations','learning_activation_outcomes')`).Scan(&tables))
	require.Equal(t, 2, tables)
	_, err = client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "primer", ContextFingerprint: "sha256:test", LearningIDs: []string{private.LocalID}, TokenCost: 1})
	require.ErrorContains(t, err, "not canonically active")
	a, err := client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "primer", ContextFingerprint: "sha256:test", Purpose: "session_start", SessionID: "session-1", LearningIDs: []string{public.LocalID, public.LocalID}, TokenCost: 17, Explanation: "issue match"})
	require.NoError(t, err)
	require.Len(t, a.LearningIDs, 1)
	require.NotEmpty(t, a.ActivationID)
	delivered, err := client.DeliveredLearningIDs(ctx, "proj", "session-1")
	require.NoError(t, err)
	require.Contains(t, delivered, public.LocalID)
	otherSession, err := client.DeliveredLearningIDs(ctx, "proj", "session-2")
	require.NoError(t, err)
	require.Empty(t, otherSession)
	_, err = client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "transition", ContextFingerprint: "sha256:next", Purpose: "context_transition", SessionID: "session-1", LearningIDs: []string{public.LocalID}, TokenCost: 1})
	require.ErrorContains(t, err, "record session learning delivery")
	in := LearningActivationOutcome{ProjectID: "proj", ActivationID: a.ActivationID, IdempotencyKey: "turn-1", Outcome: domain.LearningOutcomeHelpful, Source: domain.LearningOutcomeExplicit, Explanation: "used it"}
	first, created, err := client.RecordLearningActivationOutcome(ctx, in)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.LearningOutcomeExplicit, first.Source)
	second, created, err := client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ProjectID: "proj", ActivationID: a.ActivationID, IdempotencyKey: "turn-1", Outcome: domain.LearningOutcomeContradicted, Source: domain.LearningOutcomeInferred})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, domain.LearningOutcomeHelpful, second.Outcome, "dedup must preserve original metric")
	var activations, outcomes int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM learning_activations`).Scan(&activations))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM learning_activation_outcomes`).Scan(&outcomes))
	require.Equal(t, 1, activations)
	require.Equal(t, 1, outcomes)
}

func TestLearningActivationProposalDoesNotSuppressUntilConfirmed(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "confirmed delivery only", Evidence: "safe"})
	require.NoError(t, err)
	learning, err = client.UpdateLearningStatus(ctx, learning.LocalID, LearningStatusAccepted, "reviewed")
	require.NoError(t, err)
	p := RecordLearningActivationParams{ProjectID: "proj", Surface: "prime", ContextFingerprint: "sha256:x", Purpose: "session_start", SessionID: "session-1", LearningIDs: []string{learning.LocalID}, Explanation: "match"}
	first, err := client.ProposeLearningActivation(ctx, p)
	require.NoError(t, err)
	delivered, err := client.DeliveredLearningIDs(ctx, "proj", "session-1")
	require.NoError(t, err)
	require.Empty(t, delivered, "lost response must not suppress")
	second, err := client.ProposeLearningActivation(ctx, p)
	require.NoError(t, err)
	require.NotEqual(t, first.ActivationID, second.ActivationID)
	_, err = client.ConfirmLearningActivation(ctx, "other-project", second.ActivationID, 23)
	require.Error(t, err, "cross-project confirmation must be refused")
	confirmed, err := client.ConfirmLearningActivation(ctx, "proj", second.ActivationID, 23)
	require.NoError(t, err)
	require.Equal(t, 23, confirmed.TokenCost)
	delivered, err = client.DeliveredLearningIDs(ctx, "proj", "session-1")
	require.NoError(t, err)
	require.Contains(t, delivered, learning.LocalID)
	retry, err := client.ConfirmLearningActivation(ctx, "proj", second.ActivationID, 99)
	require.NoError(t, err)
	require.Equal(t, 23, retry.TokenCost, "confirmation retry is idempotent")
}

func TestLearningActivationOutcomeResolvesOncePerActivationByReporterPriority(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "feedback", Evidence: "safe"})
	require.NoError(t, err)
	learning, err = client.UpdateLearningStatus(ctx, learning.LocalID, LearningStatusAccepted, "reviewed")
	require.NoError(t, err)
	a, err := client.RecordLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "manual", ContextFingerprint: "sha256:x", LearningIDs: []string{learning.LocalID}})
	require.NoError(t, err)
	inferred, _, err := client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ProjectID: "proj", ActivationID: a.ActivationID, IdempotencyKey: "auto:turn-1", Outcome: domain.LearningOutcomeHelpful, Source: domain.LearningOutcomeInferred})
	require.NoError(t, err)
	require.Equal(t, domain.LearningOutcomeHelpful, inferred.ResolvedOutcome)
	agent, _, err := client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ProjectID: "proj", ActivationID: a.ActivationID, IdempotencyKey: "agent:turn-1", Outcome: domain.LearningOutcomeFollowed, Source: domain.LearningOutcomeAgent})
	require.NoError(t, err)
	require.Equal(t, domain.LearningOutcomeFollowed, agent.ResolvedOutcome)
	human, _, err := client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ProjectID: "proj", ActivationID: a.ActivationID, IdempotencyKey: "human:1", Outcome: domain.LearningOutcomeContradicted, Source: domain.LearningOutcomeHuman})
	require.NoError(t, err)
	require.Equal(t, domain.LearningOutcomeContradicted, human.ResolvedOutcome)
	later, _, err := client.RecordLearningActivationOutcome(ctx, LearningActivationOutcome{ProjectID: "proj", ActivationID: a.ActivationID, IdempotencyKey: "agent:turn-2", Outcome: domain.LearningOutcomeHelpful, Source: domain.LearningOutcomeAgent})
	require.NoError(t, err)
	require.Equal(t, domain.LearningOutcomeContradicted, later.ResolvedOutcome)
}

func TestLearningActivationFailedConfirmationDoesNotSuppress(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "lifecycle", Evidence: "safe"})
	require.NoError(t, err)
	learning, err = client.UpdateLearningStatus(ctx, learning.LocalID, LearningStatusAccepted, "reviewed")
	require.NoError(t, err)
	proposal, err := client.ProposeLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "hook", ContextFingerprint: "sha256:x", Purpose: "context_transition", SessionID: "session-1", LearningIDs: []string{learning.LocalID}})
	require.NoError(t, err)
	_, err = client.UpdateLearningStatus(ctx, learning.LocalID, LearningStatusStale, "outdated")
	require.NoError(t, err)
	_, err = client.ConfirmLearningActivation(ctx, "proj", proposal.ActivationID, 8)
	require.ErrorContains(t, err, "no longer canonically active")
	delivered, err := client.DeliveredLearningIDs(ctx, "proj", "session-1")
	require.NoError(t, err)
	require.Empty(t, delivered)
}

func TestLearningActivationProposalExpiresLostResponses(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "expiry", Evidence: "safe"})
	require.NoError(t, err)
	learning, err = client.UpdateLearningStatus(ctx, learning.LocalID, LearningStatusAccepted, "reviewed")
	require.NoError(t, err)
	p, err := client.ProposeLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "prime", ContextFingerprint: "sha256:x", Purpose: "session_start", SessionID: "s", LearningIDs: []string{learning.LocalID}})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE learning_activation_proposals SET proposed_at=? WHERE activation_id=?`, formatTimestamp(time.Now().UTC().Add(-25*time.Hour)), p.ActivationID)
	require.NoError(t, err)
	_, err = client.ProposeLearningActivation(ctx, RecordLearningActivationParams{ProjectID: "proj", Surface: "prime", ContextFingerprint: "sha256:y", Purpose: "session_start", SessionID: "s", LearningIDs: []string{learning.LocalID}})
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM learning_activation_proposals WHERE activation_id=?`, p.ActivationID).Scan(&count))
	require.Zero(t, count)
}
