package issues

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		Target:         LearningPromotionTargetDecision,
		TargetID:       decision.LocalID,
		Note:           "Decision recorded and linked separately.",
		TargetHash:     "sha256:decision-hash",
		TargetMetadata: map[string]string{"path": "docs/decisions/decision.md"},
	})
	require.NoError(t, err)
	assert.Equal(t, LearningStatusPromoted, promoted.Status)
	require.NotNil(t, promoted.Target)
	assert.Equal(t, LearningPromotionTargetDecision, *promoted.Target)
	assert.Equal(t, decision.LocalID, promoted.TargetID)
	assert.Equal(t, "Decision recorded and linked separately.", promoted.TargetNote)
	require.NotNil(t, promoted.PromotedAt)
	assert.Equal(t, LearningTargetStateActive, promoted.TargetState)
	assert.Equal(t, "sha256:decision-hash", promoted.TargetHash)
	assert.Equal(t, map[string]string{"path": "docs/decisions/decision.md"}, promoted.TargetMetadata)
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

func TestClient_ListLearningsActiveOnlyExcludesInactiveLifecycleRows(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	activeAccepted := createAcceptedLearning(t, ctx, client, "proj", "Active accepted learning")
	activePromoted := createAcceptedLearning(t, ctx, client, "proj", "Active promoted learning")
	decision, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "Promoted active target",
		Rationale: "The target is still live.",
	})
	require.NoError(t, err)
	_, err = client.PromoteLearning(ctx, activePromoted.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: decision.LocalID,
	})
	require.NoError(t, err)

	candidate, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Candidate learning",
		Evidence:  "Candidates are not reviewed.",
	})
	require.NoError(t, err)
	rejected := createAcceptedLearning(t, ctx, client, "proj", "Rejected learning")
	_, err = client.UpdateLearningStatus(ctx, rejected.LocalID, LearningStatusRejected, "Rejected.")
	require.NoError(t, err)
	staleStatus := createAcceptedLearning(t, ctx, client, "proj", "Stale status learning")
	_, err = client.UpdateLearningStatus(ctx, staleStatus.LocalID, LearningStatusStale, "Stale.")
	require.NoError(t, err)
	expired := createAcceptedLearning(t, ctx, client, "proj", "Expired learning")
	staleByTime := createAcceptedLearning(t, ctx, client, "proj", "Time-stale learning")
	superseded := createAcceptedLearning(t, ctx, client, "proj", "Superseded learning")
	retiredTarget := createAcceptedLearning(t, ctx, client, "proj", "Retired target learning")
	driftedTarget := createAcceptedLearning(t, ctx, client, "proj", "Drifted target learning")
	missingTarget := createAcceptedLearning(t, ctx, client, "proj", "Missing target learning")
	retiredDecision, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "Retired target",
		Rationale: "The target was later retired.",
	})
	require.NoError(t, err)
	_, err = client.PromoteLearning(ctx, retiredTarget.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: retiredDecision.LocalID,
	})
	require.NoError(t, err)
	driftedDecision, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "Drifted target",
		Rationale: "The target changed after promotion.",
	})
	require.NoError(t, err)
	_, err = client.PromoteLearning(ctx, driftedTarget.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: driftedDecision.LocalID,
	})
	require.NoError(t, err)
	missingDecision, err := client.RecordDecision(ctx, RecordDecisionParams{
		Title:     "Missing target",
		Rationale: "The target could not be found.",
	})
	require.NoError(t, err)
	_, err = client.PromoteLearning(ctx, missingTarget.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: missingDecision.LocalID,
	})
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)
	past := time.Now().UTC().Add(-time.Hour)
	setLearningLifecycleTime(t, ctx, db, expired.LocalID, "expires_at", past)
	setLearningLifecycleTime(t, ctx, db, staleByTime.LocalID, "stale_at", past)
	setLearningLifecycleTime(t, ctx, db, superseded.LocalID, "superseded_at", past)
	setLearningLifecycleTime(t, ctx, db, retiredTarget.LocalID, "target_retired_at", past)
	setLearningTargetState(t, ctx, db, retiredTarget.LocalID, LearningTargetStateRetired)
	setLearningTargetState(t, ctx, db, driftedTarget.LocalID, LearningTargetStateDrifted)
	setLearningTargetState(t, ctx, db, missingTarget.LocalID, LearningTargetStateMissing)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID: "proj",
		Statuses: []LearningStatus{
			LearningStatusCandidate,
			LearningStatusAccepted,
			LearningStatusRejected,
			LearningStatusPromoted,
			LearningStatusStale,
		},
		ActiveOnly: true,
	})
	require.NoError(t, err)

	got := make(map[string]Learning, len(rows))
	for _, row := range rows {
		got[row.LocalID] = row
	}
	assert.Contains(t, got, activeAccepted.LocalID)
	assert.Contains(t, got, activePromoted.LocalID)
	assert.NotContains(t, got, candidate.LocalID)
	assert.NotContains(t, got, rejected.LocalID)
	assert.NotContains(t, got, staleStatus.LocalID)
	assert.NotContains(t, got, expired.LocalID)
	assert.NotContains(t, got, staleByTime.LocalID)
	assert.NotContains(t, got, superseded.LocalID)
	assert.NotContains(t, got, retiredTarget.LocalID)
	assert.NotContains(t, got, driftedTarget.LocalID)
	assert.NotContains(t, got, missingTarget.LocalID)

	for _, id := range []string{activeAccepted.LocalID, activePromoted.LocalID} {
		assert.Equal(t, 1, got[id].RecallCount)
		assert.NotNil(t, got[id].LastRecalledAt)
		stored, err := client.GetLearning(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, 1, stored.RecallCount)
		assert.NotNil(t, stored.LastRecalledAt)
	}
	storedCandidate, err := client.GetLearning(ctx, candidate.LocalID)
	require.NoError(t, err)
	assert.Zero(t, storedCandidate.RecallCount)
	assert.Nil(t, storedCandidate.LastRecalledAt)
}

func TestClient_MigratesPromotedLearningTargetState(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	applyIssueMigrationsThrough(t, ctx, db, "0020_agent_learning_lifecycle")

	now := formatTimestamp(time.Now().UTC())
	retiredAt := formatTimestamp(time.Now().UTC().Add(-time.Hour))
	_, err = db.ExecContext(ctx, `
		INSERT INTO agent_learnings (
			local_id, project_id, summary, evidence, status, tags_json, files_json,
			promotion_target, promotion_target_id, promoted_at, target_retired_at,
			created_at, updated_at, deleted_at
		)
		VALUES
			('learn-active', 'proj', 'Active promoted', 'Evidence.', 'promoted', '[]', '[]', 'decision', 'dec-active', ?, NULL, ?, ?, NULL),
			('learn-retired', 'proj', 'Retired promoted', 'Evidence.', 'promoted', '[]', '[]', 'decision', 'dec-retired', ?, ?, ?, ?, NULL)
	`, now, now, now, now, retiredAt, now, now)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })

	active, err := client.GetLearning(ctx, "learn-active")
	require.NoError(t, err)
	assert.Equal(t, LearningTargetStateActive, active.TargetState)

	retired, err := client.GetLearning(ctx, "learn-retired")
	require.NoError(t, err)
	assert.Equal(t, LearningTargetStateRetired, retired.TargetState)
	require.NotNil(t, retired.TargetRetiredAt)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:  "proj",
		ActiveOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "learn-active", rows[0].LocalID)
}

func TestLearningActiveAtUsesParsedTimestampBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 2, 3, 45, 0, 500, time.UTC)
	expiresAtBoundary := now.Truncate(time.Second)
	staleAtBoundary := now.Truncate(time.Second)
	future := now.Add(time.Nanosecond)

	assert.False(t, learningActiveAt(Learning{
		Status:    LearningStatusAccepted,
		ExpiresAt: &expiresAtBoundary,
	}, now))
	assert.False(t, learningActiveAt(Learning{
		Status:  LearningStatusPromoted,
		StaleAt: &staleAtBoundary,
	}, now))
	assert.True(t, learningActiveAt(Learning{
		Status:    LearningStatusAccepted,
		ExpiresAt: &future,
	}, now))
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

func createAcceptedLearning(t *testing.T, ctx context.Context, client *Client, projectID, summary string) Learning {
	t.Helper()
	created, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: projectID,
		Summary:   summary,
		Evidence:  summary + " evidence.",
	})
	require.NoError(t, err)
	accepted, err := client.UpdateLearningStatus(ctx, created.LocalID, LearningStatusAccepted, "Accepted.")
	require.NoError(t, err)
	return accepted
}

func setLearningLifecycleTime(t *testing.T, ctx context.Context, db sqlIssueExecer, localID, column string, value time.Time) {
	t.Helper()
	switch column {
	case "expires_at", "stale_at", "superseded_at", "target_retired_at":
	default:
		t.Fatalf("unsupported lifecycle column %q", column)
	}
	_, err := db.ExecContext(ctx, "UPDATE agent_learnings SET "+column+" = ? WHERE local_id = ?", formatTimestamp(value), localID)
	require.NoError(t, err)
}

func setLearningTargetState(t *testing.T, ctx context.Context, db sqlIssueExecer, localID string, state LearningTargetState) {
	t.Helper()
	_, err := db.ExecContext(ctx, "UPDATE agent_learnings SET target_state = ? WHERE local_id = ?", string(state), localID)
	require.NoError(t, err)
}

func applyIssueMigrationsThrough(t *testing.T, ctx context.Context, db *sql.DB, throughID string) {
	t.Helper()
	require.NoError(t, ensureMigrationTable(ctx, db))
	for _, migration := range orderedMigrations {
		sqlText, err := loadMigrationSQL(migration.path)
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, sqlText)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (id, applied_at)
			VALUES (?, ?)
		`, migration.id, time.Now().UTC().Format(time.RFC3339Nano))
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		if migration.id == throughID {
			return
		}
	}
	t.Fatalf("migration %q not found", throughID)
}
