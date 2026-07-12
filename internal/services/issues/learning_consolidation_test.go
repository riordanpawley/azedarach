package issues

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLearningConsolidationSuggestionsAreDeterministicPrivacySafeAndNonMutating(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	left, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Use bounded retries for daemon operations", Evidence: "PRIVATE-TOKEN-123", EvidencePrivate: true, Tags: []string{"daemon"}})
	require.NoError(t, err)
	right, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Use bounded retries for daemon operations", Evidence: "public", Tags: []string{"operations"}})
	require.NoError(t, err)
	first, err := client.SuggestLearningConsolidations(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, LearningSuggestionDuplicate, first[0].Kind)
	assert.Equal(t, 100, first[0].Score)
	assert.NotContains(t, first[0].Reason, "PRIVATE-TOKEN-123")
	second, err := client.SuggestLearningConsolidations(ctx, "proj")
	require.NoError(t, err)
	require.Equal(t, first, second)
	gotLeft, err := client.GetLearning(ctx, left.LocalID)
	require.NoError(t, err)
	gotRight, err := client.GetLearning(ctx, right.LocalID)
	require.NoError(t, err)
	assert.Equal(t, LearningStatusCandidate, gotLeft.Status)
	assert.Equal(t, LearningStatusCandidate, gotRight.Status)
}

func TestLearningConsolidationConfirmPreservesSourcesAndPromotionState(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	left, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Prefer deterministic ordering for review queues", Evidence: "left evidence", EvidencePrivate: true, Tags: []string{"ordering"}})
	require.NoError(t, err)
	right, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Prefer deterministic ordering for review queues", Evidence: "right evidence", Files: []string{"queue.go"}})
	require.NoError(t, err)
	accepted, err := client.UpdateLearningStatus(ctx, left.LocalID, LearningStatusAccepted, "reviewed")
	require.NoError(t, err)
	assert.Equal(t, LearningStatusAccepted, accepted.Status)
	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET status='promoted',promotion_target='agents',promotion_target_id='AGENTS.md',target_state='active' WHERE local_id=?`, left.LocalID)
	require.NoError(t, err)
	third, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Keep queue ownership in the daemon", Evidence: "third"})
	require.NoError(t, err)
	_, err = client.RelateLearning(ctx, RelateLearningParams{Type: LearningRelationConflicts, SourceLearningID: right.LocalID, TargetLearningID: third.LocalID, Note: "different queue policy"})
	require.NoError(t, err)
	suggestions, err := client.SuggestLearningConsolidations(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, suggestions, 1)
	resolved, err := client.ConfirmLearningConsolidation(ctx, ConfirmLearningConsolidationParams{SuggestionID: suggestions[0].LocalID, CanonicalLearningID: left.LocalID, Summary: "Use deterministic ordering for review queues", Note: "human confirmed duplicate"})
	require.NoError(t, err)
	assert.Equal(t, LearningSuggestionConfirmed, resolved.Status)
	canonical, err := client.GetLearning(ctx, left.LocalID)
	require.NoError(t, err)
	assert.Equal(t, LearningStatusPromoted, canonical.Status)
	require.NotNil(t, canonical.Target)
	assert.Equal(t, LearningPromotionTargetAgents, *canonical.Target)
	assert.Equal(t, LearningTargetStateActive, canonical.TargetState)
	assert.True(t, canonical.EvidencePrivate)
	assert.Equal(t, "left evidence", canonical.Evidence)
	assert.Contains(t, canonical.Tags, "ordering")
	assert.Contains(t, canonical.Files, "queue.go")
	source, err := client.GetLearning(ctx, right.LocalID)
	require.NoError(t, err)
	assert.Equal(t, "right evidence", source.Evidence)
	assert.Equal(t, LearningStatusCandidate, source.Status)
	rows, err := client.ListLearnings(ctx, LearningFilter{ProjectID: "proj", IncludeEvidence: true})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	listed := []string{rows[0].LocalID, rows[1].LocalID}
	assert.Contains(t, listed, left.LocalID)
	assert.Contains(t, listed, third.LocalID)
	assert.NotContains(t, listed, right.LocalID)
	var members, audit, relations int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learning_consolidation_members`).Scan(&members))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learning_consolidation_audit WHERE action='confirmed'`).Scan(&audit))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learning_relations WHERE deleted_at IS NULL`).Scan(&relations))
	assert.Equal(t, 2, members)
	assert.Equal(t, 1, audit)
	assert.Equal(t, 1, relations)
}

func TestLearningConsolidationAuditFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	left, _ := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Keep daemon events ordered deterministically", Evidence: "a"})
	right, _ := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Keep daemon events ordered deterministically", Evidence: "b"})
	suggestions, err := client.SuggestLearningConsolidations(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, suggestions, 1)
	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TRIGGER fail_consolidation_audit BEFORE INSERT ON agent_learning_consolidation_audit WHEN NEW.action='confirmed' BEGIN SELECT RAISE(ABORT,'audit failure'); END`)
	require.NoError(t, err)
	_, err = client.ConfirmLearningConsolidation(ctx, ConfirmLearningConsolidationParams{SuggestionID: suggestions[0].LocalID, CanonicalLearningID: left.LocalID, Note: "confirm"})
	require.Error(t, err)
	var consolidated int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learnings WHERE consolidated_into_id IS NOT NULL`).Scan(&consolidated))
	assert.Zero(t, consolidated)
	var status string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM agent_learning_suggestions WHERE local_id=?`, suggestions[0].LocalID).Scan(&status))
	assert.Equal(t, "pending", status)
	_, err = client.GetLearning(ctx, right.LocalID)
	require.NoError(t, err)
}

func TestLearningConflictSuggestionCanBeRejectedWithoutLifecycleMutation(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	left, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Use automatic promotion after human review", Evidence: "a"})
	require.NoError(t, err)
	right, err := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Never use automatic promotion after human review", Evidence: "b"})
	require.NoError(t, err)
	rows, err := client.SuggestLearningConsolidations(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, LearningSuggestionConflict, rows[0].Kind)
	rejected, err := client.RejectLearningSuggestion(ctx, rows[0].LocalID, "both rules are context-dependent")
	require.NoError(t, err)
	assert.Equal(t, LearningSuggestionRejected, rejected.Status)
	for _, id := range []string{left.LocalID, right.LocalID} {
		learning, err := client.GetLearning(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, LearningStatusCandidate, learning.Status)
	}
	db, err := client.dbHandle()
	require.NoError(t, err)
	var audit int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learning_consolidation_audit WHERE action='rejected'`).Scan(&audit))
	assert.Equal(t, 1, audit)
}

func TestLearningConsolidationRejectsStaleSuggestionMember(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	left, _ := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Use stable ordering for learning review", Evidence: "a"})
	right, _ := client.CreateLearning(ctx, CreateLearningParams{ProjectID: "proj", Summary: "Use stable ordering for learning review", Evidence: "b"})
	rows, err := client.SuggestLearningConsolidations(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_learnings SET consolidated_into_id=(SELECT id FROM agent_learnings WHERE local_id=?) WHERE local_id=?`, left.LocalID, right.LocalID)
	require.NoError(t, err)
	_, err = client.ConfirmLearningConsolidation(ctx, ConfirmLearningConsolidationParams{SuggestionID: rows[0].LocalID, CanonicalLearningID: left.LocalID, Note: "confirm"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already consolidated")
}
