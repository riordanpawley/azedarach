package issues

import (
	"context"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_LearningLifecycleRecallReviewAndPromote(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Learning scope",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	req, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "learn-req-1",
		Title:   "Learning requirement",
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)

	issueID := taskID
	reqID := req.LocalID
	created, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID:     "proj",
		IssueID:       &issueID,
		RequirementID: &reqID,
		Summary:       "Use decisions for durable why records",
		Evidence:      "The existing az decision command already stores rationale and links to issues and requirements.",
		Tags:          []string{"decision", "guidance"},
		Files:         []string{"internal/daemon/handler_adapters.go"},
	})
	require.NoError(t, err)
	assert.Equal(t, LearningStatusCandidate, created.Status)
	assert.NotEmpty(t, created.LocalID)

	accepted, err := client.UpdateLearningStatus(ctx, created.LocalID, LearningStatusAccepted, "Validated against existing decision workflow.")
	require.NoError(t, err)
	assert.Equal(t, LearningStatusAccepted, accepted.Status)
	assert.Equal(t, "Validated against existing decision workflow.", accepted.ReviewNote)
	require.NotNil(t, accepted.ReviewedAt)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID: "proj",
		Query:     "durable decisions",
		Statuses:  []LearningStatus{LearningStatusAccepted},
		Tags:      []string{"decision"},
		Limit:     5,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, created.LocalID, rows[0].LocalID)
	assert.Empty(t, rows[0].Evidence, "recall should omit evidence unless requested")

	withEvidence, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:       "proj",
		IssueID:         issueID,
		RequirementID:   reqID,
		Query:           "decision",
		Statuses:        []LearningStatus{LearningStatusAccepted},
		IncludeEvidence: true,
	})
	require.NoError(t, err)
	require.Len(t, withEvidence, 1)
	assert.Contains(t, withEvidence[0].Evidence, "stores rationale")

	evidenceOnly, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID: "proj",
		Query:     "rationale",
		Statuses:  []LearningStatus{LearningStatusAccepted},
	})
	require.NoError(t, err)
	assert.Empty(t, evidenceOnly, "full evidence is stored but not indexed for recall")

	decision, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "Use decisions for durable why records",
		Rationale: "Decisions already store rationale and links.",
	})
	require.NoError(t, err)
	promoted, err := client.PromoteLearning(ctx, created.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: decision.LocalID,
		Note:     "Decision recorded and linked separately.",
	})
	require.NoError(t, err)
	assert.Equal(t, LearningStatusPromoted, promoted.Status)
	require.NotNil(t, promoted.Target)
	assert.Equal(t, LearningPromotionTargetDecision, *promoted.Target)
	assert.Equal(t, decision.LocalID, promoted.TargetID)
	assert.Equal(t, "Decision recorded and linked separately.", promoted.TargetNote)
	require.NotNil(t, promoted.PromotedAt)
}

func TestClient_ListLearningsAppliesLimitAfterTagFilter(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	other, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Newer unrelated learning",
		Evidence:  "This row is newer but does not have the requested tag.",
		Tags:      []string{"other"},
	})
	require.NoError(t, err)
	_, err = client.UpdateLearningStatus(ctx, other.LocalID, LearningStatusAccepted, "Accepted unrelated row.")
	require.NoError(t, err)
	want, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Older decision learning",
		Evidence:  "This older row has the requested decision tag.",
		Tags:      []string{"decision"},
	})
	require.NoError(t, err)
	_, err = client.UpdateLearningStatus(ctx, want.LocalID, LearningStatusAccepted, "Accepted decision row.")
	require.NoError(t, err)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID: "proj",
		Statuses:  []LearningStatus{LearningStatusAccepted},
		Tags:      []string{"decision"},
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, want.LocalID, rows[0].LocalID)
}

func TestClient_PromoteLearningRequiresAcceptedStatus(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	created, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Candidate learning",
		Evidence:  "Candidates need review before promotion.",
	})
	require.NoError(t, err)

	_, err = client.PromoteLearning(ctx, created.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: "dec-1",
	})
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestClient_LearningStatusRequiresReviewAndPromotionTarget(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Bad promoted learning",
		Evidence:  "Promoted rows need target evidence from the promotion path.",
		Status:    LearningStatusAccepted,
	})
	require.Error(t, err)

	created, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Candidate learning",
		Evidence:  "Promotion should go through the promote command.",
	})
	require.NoError(t, err)

	_, err = client.UpdateLearningStatus(ctx, created.LocalID, LearningStatusAccepted, "")
	require.Error(t, err)

	_, err = client.UpdateLearningStatus(ctx, created.LocalID, LearningStatusCandidate, "")
	require.Error(t, err)

	accepted, err := client.UpdateLearningStatus(ctx, created.LocalID, LearningStatusAccepted, "Reviewed.")
	require.NoError(t, err)
	assert.Equal(t, LearningStatusAccepted, accepted.Status)

	_, err = client.PromoteLearning(ctx, created.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: "missing-decision",
	})
	require.ErrorIs(t, err, domain.ErrNotFound)

	_, err = client.UpdateLearningStatus(ctx, created.LocalID, LearningStatusPromoted, "nope")
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestClient_CreateLearningRejectsOversizedEvidence(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Large evidence",
		Evidence:  strings.Repeat("x", maxLearningEvidenceRunes+1),
	})
	require.Error(t, err)
}

func TestClient_CreateLearningRejectsOversizedSummary(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   strings.Repeat("x", maxLearningSummaryRunes+1),
		Evidence:  "Short evidence remains stored separately.",
	})
	require.Error(t, err)
}
