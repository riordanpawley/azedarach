package issues

import (
	"context"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCaptureLearningObservationTypedDuplicateAndPrivateIsolation(t *testing.T) {
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)
	ctx := context.Background()
	p := CaptureLearningObservationParams{ProjectID: "proj", ObservedBehavior: "Retries continued forever", PreferredBehavior: "Use bounded retries", Outcome: "request failed", Impact: "latency", Context: map[string]string{"surface": "cli"}, Provenance: LearningObservationProvenance{Source: "agent-hook", Actor: "worker", Ref: "turn-1"}, Sensitivity: domain.LearningSensitivityPublic, Tags: []string{"reliability"}, Files: []string{"retry.go"}}
	first, err := client.CaptureLearningObservation(ctx, p)
	require.NoError(t, err)
	require.NotEmpty(t, first.SafeFingerprint)
	require.Empty(t, first.DuplicateLearningIDs)
	second, err := client.CaptureLearningObservation(ctx, p)
	require.NoError(t, err)
	require.Equal(t, []string{first.LearningID}, second.DuplicateLearningIDs)
	private := p
	private.ObservedBehavior = "token=SECRETVALUE123"
	private.PreferredBehavior = "Never log the token"
	private.Context = map[string]string{"token": "PRIVATE-123"}
	private.Sensitivity = domain.LearningSensitivityPrivate
	secret, err := client.CaptureLearningObservation(ctx, private)
	require.NoError(t, err)
	require.Empty(t, secret.SafeFingerprint)
	require.Empty(t, secret.DuplicateLearningIDs)
	require.Equal(t, "Private learning observation", secret.Learning.Summary)
	require.Empty(t, secret.Learning.Tags)
	require.Empty(t, secret.Learning.Files)
	var indexed int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learning_search_fts WHERE agent_learning_search_fts MATCH 'SECRETVALUE123'`).Scan(&indexed))
	require.Zero(t, indexed)
	var stored string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT observed_behavior FROM learning_observations WHERE local_id=?`, secret.LocalID).Scan(&stored))
	require.Equal(t, "token=SECRETVALUE123", stored)
	var storedContext string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT context_json FROM learning_observations WHERE local_id=?`, secret.LocalID).Scan(&storedContext))
	require.Contains(t, storedContext, "PRIVATE-123")
}

func TestCaptureLearningObservationReusesLearningValidation(t *testing.T) {
	client := newTestClient(t)
	_, err := client.CaptureLearningObservation(context.Background(), CaptureLearningObservationParams{ProjectID: "proj", ObservedBehavior: strings.Repeat("x", maxLearningEvidenceRunes+1), PreferredBehavior: "safe", Provenance: LearningObservationProvenance{Source: "test"}, Sensitivity: domain.LearningSensitivityPublic})
	require.ErrorContains(t, err, "observed behavior exceeds")
}
