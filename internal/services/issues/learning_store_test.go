package issues

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

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

func TestClient_ListLearningsAppliesLimitAfterTagAndFileFilters(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	other, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Newer unrelated learning",
		Evidence:  "This row is newer but does not have the requested file.",
		Tags:      []string{"decision"},
		Files:     []string{"internal/daemon/other.go"},
	})
	require.NoError(t, err)
	_, err = client.UpdateLearningStatus(ctx, other.LocalID, LearningStatusAccepted, "Accepted unrelated row.")
	require.NoError(t, err)
	want, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Older decision learning",
		Evidence:  "This older row has the requested decision tag and file.",
		Tags:      []string{"decision", "guidance"},
		Files:     []string{"internal/daemon/handler_adapters.go"},
	})
	require.NoError(t, err)
	_, err = client.UpdateLearningStatus(ctx, want.LocalID, LearningStatusAccepted, "Accepted decision row.")
	require.NoError(t, err)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID: "proj",
		Statuses:  []LearningStatus{LearningStatusAccepted},
		Tags:      []string{" Decision "},
		Files:     []string{"INTERNAL/DAEMON/HANDLER_ADAPTERS.GO"},
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, want.LocalID, rows[0].LocalID)
}

func TestClient_ListLearningsTagAndFileFiltersUseIndexedMetadata(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	got := explainQueryPlan(t, ctx, db, `
		SELECT l.id
		FROM agent_learnings l
		WHERE l.id IN (SELECT learning_id FROM agent_learning_tags WHERE tag_key = ?)
			AND l.id IN (SELECT learning_id FROM agent_learning_files WHERE file_key = ?)
			AND l.deleted_at IS NULL
			AND l.project_id = ?
		ORDER BY l.updated_at DESC, l.local_id ASC
		LIMIT ?
	`, "decision", "internal/daemon/handler_adapters.go", "proj", 1)
	assert.Contains(t, got, "idx_agent_learning_tags_key_learning", got)
	assert.Contains(t, got, "idx_agent_learning_files_key_learning", got)
	assert.NotContains(t, got, "SCAN agent_learning_tags", got)
	assert.NotContains(t, got, "SCAN agent_learning_files", got)
}

func TestClient_ListLearningsIncludeDeletedUsesIndexedMetadata(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	created, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Deleted indexed learning",
		Evidence:  "Deleted rows remain queryable when explicitly included.",
		Tags:      []string{"decision"},
		Files:     []string{"internal/daemon/handler_adapters.go"},
	})
	require.NoError(t, err)
	_, err = client.UpdateLearningStatus(ctx, created.LocalID, LearningStatusAccepted, "Accepted before deletion.")
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		UPDATE agent_learnings SET deleted_at = ? WHERE local_id = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), created.LocalID)
	require.NoError(t, err)

	filter := LearningFilter{
		ProjectID: "proj",
		Statuses:  []LearningStatus{LearningStatusAccepted},
		Tags:      []string{"decision"},
		Files:     []string{"internal/daemon/handler_adapters.go"},
		Limit:     1,
	}
	rows, err := client.ListLearnings(ctx, filter)
	require.NoError(t, err)
	assert.Empty(t, rows)

	filter.IncludeDeleted = true
	rows, err = client.ListLearnings(ctx, filter)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, created.LocalID, rows[0].LocalID)
	require.NotNil(t, rows[0].DeletedAt)
}

func TestClient_MigratesLearningTagAndFileMetadataBackfill(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	legacy := NewClientAtPath(dbPath, slog.Default())
	require.NoError(t, ensureMigrationTable(ctx, db))
	for _, migration := range orderedMigrations {
		if migration.id == "0021_agent_learning_metadata" {
			break
		}
		shouldApply := true
		if migration.shouldApply != nil {
			shouldApply, err = migration.shouldApply(ctx, db)
			require.NoError(t, err)
		}
		if !shouldApply {
			require.NoError(t, recordAppliedMigration(ctx, db, migration.id))
			continue
		}
		sqlText, loadErr := loadMigrationSQL(migration.path)
		require.NoError(t, loadErr)
		require.NoError(t, legacy.applyMigration(ctx, db, migration.id, sqlText))
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		INSERT INTO agent_learnings (
			local_id, project_id, summary, evidence, status,
			tags_json, files_json, created_at, updated_at, deleted_at
		)
		VALUES (
			'learn-existing', 'proj', 'Existing learning', 'Existing evidence.', 'accepted',
			'["Decision", "guidance"]', '["internal/daemon/handler_adapters.go"]', ?, ?, NULL
		),
		(
			'learn-existing-deleted', 'proj', 'Existing deleted learning', 'Existing deleted evidence.', 'accepted',
			'["decision"]', '["internal/daemon/handler_adapters.go"]', ?, ?, ?
		),
		(
			'learn-existing-scalar-metadata', 'proj', 'Existing scalar metadata learning', 'Existing scalar metadata evidence.', 'accepted',
			'"decision"', '{"path":"internal/daemon/handler_adapters.go"}', ?, ?, NULL
		)
	`, now, now, now, now, now, now, now)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	migrated := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, migrated.CloseDB())
	})
	migratedDB, err := migrated.dbHandle()
	require.NoError(t, err)

	var tagCount, fileCount int
	require.NoError(t, migratedDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_learning_tags WHERE tag_key IN ('decision', 'guidance')
	`).Scan(&tagCount))
	require.NoError(t, migratedDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_learning_files WHERE file_key = 'internal/daemon/handler_adapters.go'
	`).Scan(&fileCount))
	assert.Equal(t, 3, tagCount)
	assert.Equal(t, 2, fileCount)

	rows, err := migrated.ListLearnings(ctx, LearningFilter{
		ProjectID: "proj",
		Statuses:  []LearningStatus{LearningStatusAccepted},
		Tags:      []string{"decision"},
		Files:     []string{"internal/daemon/handler_adapters.go"},
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "learn-existing", rows[0].LocalID)

	rows, err = migrated.ListLearnings(ctx, LearningFilter{
		ProjectID:      "proj",
		Statuses:       []LearningStatus{LearningStatusAccepted},
		Tags:           []string{"decision"},
		Files:          []string{"internal/daemon/handler_adapters.go"},
		Limit:          10,
		IncludeDeleted: true,
	})
	require.NoError(t, err)
	got := make(map[string]Learning, len(rows))
	for _, row := range rows {
		got[row.LocalID] = row
	}
	assert.Contains(t, got, "learn-existing")
	assert.Contains(t, got, "learn-existing-deleted")
	require.NotNil(t, got["learn-existing-deleted"].DeletedAt)
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

	db, err := client.dbHandle()
	require.NoError(t, err)
	past := time.Now().UTC().Add(-time.Hour)
	setLearningLifecycleTime(t, ctx, db, expired.LocalID, "expires_at", past)
	setLearningLifecycleTime(t, ctx, db, staleByTime.LocalID, "stale_at", past)
	setLearningLifecycleTime(t, ctx, db, superseded.LocalID, "superseded_at", past)
	setLearningLifecycleTime(t, ctx, db, retiredTarget.LocalID, "target_retired_at", past)

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
