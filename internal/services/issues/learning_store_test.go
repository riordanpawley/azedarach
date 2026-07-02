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

func TestClient_PromoteLearningCreatesDecisionTargetAndLinksScopesIdempotently(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Decision promotion scope",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	req, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "promote-decision-req",
		Title:   "Decision promotion requirement",
		IssueID: &issueID,
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID:     "proj",
		IssueID:       &issueID,
		RequirementID: &req.LocalID,
		Summary:       "Promote durable decisions through the decision store",
		Evidence:      "Promotion should create, update, and link a structured decision record.",
	})
	require.NoError(t, err)
	learning, err = client.UpdateLearningStatus(ctx, learning.LocalID, LearningStatusAccepted, "Accepted.")
	require.NoError(t, err)

	promoted, err := client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:            LearningPromotionTargetDecision,
		CreateTarget:      true,
		TargetTitle:       "Use structured decision promotion",
		DecisionRationale: "Structured promotion keeps rationale in SQLite.",
		Note:              "Initial promotion.",
	})
	require.NoError(t, err)
	assert.Equal(t, LearningStatusPromoted, promoted.Status)
	require.NotEmpty(t, promoted.TargetID)

	decision, err := client.GetDecision(ctx, promoted.TargetID)
	require.NoError(t, err)
	assert.Equal(t, "Use structured decision promotion", decision.Title)
	assert.Equal(t, "Structured promotion keeps rationale in SQLite.", decision.Rationale)
	links, err := client.ListDecisionLinks(ctx, DecisionLinkFilter{DecisionID: promoted.TargetID})
	require.NoError(t, err)
	assert.ElementsMatch(t, []DecisionTargetKind{DecisionTargetIssue, DecisionTargetRequirement}, decisionLinkKinds(links))

	repeated, err := client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:               LearningPromotionTargetDecision,
		CreateTarget:         true,
		TargetTitle:          "Use structured decision promotion",
		DecisionRationale:    "Updated rationale remains on the existing decision.",
		DecisionConsequences: "Audit rows capture the structured update.",
		Note:                 "Repeat promotion updates the same target.",
	})
	require.NoError(t, err)
	assert.Equal(t, promoted.TargetID, repeated.TargetID)
	decision, err = client.GetDecision(ctx, repeated.TargetID)
	require.NoError(t, err)
	assert.Equal(t, "Updated rationale remains on the existing decision.", decision.Rationale)
	assert.Equal(t, "Audit rows capture the structured update.", decision.Consequences)
	decisions, err := client.ListDecisions(ctx, DecisionFilter{})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	links, err = client.ListDecisionLinks(ctx, DecisionLinkFilter{DecisionID: repeated.TargetID})
	require.NoError(t, err)
	require.Len(t, links, 2)
}

func TestClient_PromoteLearningCreatesAndUpdatesSpecTarget(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Spec promotion scope",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	learning, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		IssueID:   &issueID,
		Summary:   "Promote specs through the spec store",
		Evidence:  "Promotion should create or update a structured requirement and link it.",
	})
	require.NoError(t, err)
	learning, err = client.UpdateLearningStatus(ctx, learning.LocalID, LearningStatusAccepted, "Accepted.")
	require.NoError(t, err)

	promoted, err := client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:            LearningPromotionTargetSpec,
		TargetID:          "learn-structured-spec",
		CreateTarget:      true,
		TargetTitle:       "Structured spec promotion",
		TargetDescription: "Initial structured requirement.",
		Note:              "Create spec requirement.",
	})
	require.NoError(t, err)
	assert.Equal(t, "learn-structured-spec", promoted.TargetID)
	req, err := client.GetRequirement(ctx, promoted.TargetID)
	require.NoError(t, err)
	assert.Equal(t, "Structured spec promotion", req.Title)
	assert.Equal(t, "Initial structured requirement.", req.Description)
	links, err := client.ListSpecLinks(ctx, SpecLinkFilter{IssueID: issueID, RequirementID: promoted.TargetID})
	require.NoError(t, err)
	require.Len(t, links, 1)

	repeated, err := client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:            LearningPromotionTargetSpec,
		TargetID:          "learn-structured-spec",
		CreateTarget:      true,
		TargetDescription: "Updated structured requirement.",
		Note:              "Repeat promotion updates the same requirement.",
	})
	require.NoError(t, err)
	assert.Equal(t, promoted.TargetID, repeated.TargetID)
	req, err = client.GetRequirement(ctx, promoted.TargetID)
	require.NoError(t, err)
	assert.Equal(t, "Updated structured requirement.", req.Description)
	links, err = client.ListSpecLinks(ctx, SpecLinkFilter{IssueID: issueID, RequirementID: promoted.TargetID})
	require.NoError(t, err)
	require.Len(t, links, 1)
}

func TestClient_PromoteLearningMissingStructuredTargetRequiresCreateTarget(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	learning := createAcceptedLearning(t, ctx, client, "proj", "Missing target learning")

	_, err := client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:   LearningPromotionTargetDecision,
		TargetID: "missing-decision",
	})
	require.ErrorIs(t, err, domain.ErrNotFound)

	_, err = client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:       LearningPromotionTargetSpec,
		TargetID:     "missing-spec",
		CreateTarget: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target title")

	_, err = client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:       LearningPromotionTargetAgents,
		CreateTarget: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target id")
}

func TestClient_RetireLearningTargetPreservesStructuredTargetHistory(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	specLearning := createAcceptedLearning(t, ctx, client, "proj", "Retire spec target")
	promotedSpec, err := client.PromoteLearning(ctx, specLearning.LocalID, PromoteLearningParams{
		Target:            LearningPromotionTargetSpec,
		TargetID:          "retired-spec-target",
		CreateTarget:      true,
		TargetTitle:       "Retired spec target",
		TargetDescription: "History should remain after retirement.",
	})
	require.NoError(t, err)

	retiredSpec, err := client.RetireLearningTarget(ctx, promotedSpec.LocalID, "Structured spec target is retired.")
	require.NoError(t, err)
	assert.Equal(t, LearningTargetStateRetired, retiredSpec.TargetState)
	require.NotNil(t, retiredSpec.TargetRetiredAt)
	requirement, err := client.GetRequirement(ctx, promotedSpec.TargetID)
	require.NoError(t, err)
	assert.Equal(t, RequirementStatusSuperseded, requirement.Status)
	assert.Equal(t, "History should remain after retirement.", requirement.Description)

	decisionLearning := createAcceptedLearning(t, ctx, client, "proj", "Retire decision target")
	promotedDecision, err := client.PromoteLearning(ctx, decisionLearning.LocalID, PromoteLearningParams{
		Target:            LearningPromotionTargetDecision,
		CreateTarget:      true,
		TargetTitle:       "Retired decision target",
		DecisionRationale: "Decision rationale must remain after retirement.",
	})
	require.NoError(t, err)
	retiredDecision, err := client.RetireLearningTarget(ctx, promotedDecision.LocalID, "Structured decision target is retired.")
	require.NoError(t, err)
	assert.Equal(t, LearningTargetStateRetired, retiredDecision.TargetState)
	decision, err := client.GetDecision(ctx, promotedDecision.TargetID)
	require.NoError(t, err)
	assert.Equal(t, "Decision rationale must remain after retirement.", decision.Rationale)
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

func TestClient_ListLearningsRanksContextAndExplainsRecall(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Recall ranking scope",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	req, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "learn-ranking-req",
		Title:   "Recall ranking requirement",
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)

	generic := createAcceptedLearning(t, ctx, client, "proj", "Daemon config guidance")
	scopedIssueID := taskID
	scopedReqID := req.LocalID
	scoped, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID:     "proj",
		IssueID:       &scopedIssueID,
		RequirementID: &scopedReqID,
		Summary:       "Daemon config guidance for scoped issue",
		Evidence:      "Use the scoped daemon config guidance for this issue.",
		Tags:          []string{"daemon"},
		Files:         []string{"internal/config/config.go"},
	})
	require.NoError(t, err)
	scoped, err = client.UpdateLearningStatus(ctx, scoped.LocalID, LearningStatusAccepted, "Scoped guidance accepted.")
	require.NoError(t, err)
	newGeneric := createAcceptedLearning(t, ctx, client, "proj", "Newest generic daemon config guidance")

	db, err := client.dbHandle()
	require.NoError(t, err)
	setLearningUpdatedTime(t, ctx, db, generic.LocalID, time.Now().UTC().Add(-72*time.Hour))
	setLearningUpdatedTime(t, ctx, db, scoped.LocalID, time.Now().UTC().Add(-48*time.Hour))
	setLearningUpdatedTime(t, ctx, db, newGeneric.LocalID, time.Now().UTC())

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:      "proj",
		ContextIssueID: taskID,
		ContextReqID:   req.LocalID,
		ContextTags:    []string{"daemon"},
		ContextFiles:   []string{"internal/config/config.go"},
		Query:          "daemon config",
		Statuses:       []LearningStatus{LearningStatusAccepted},
		ActiveOnly:     true,
		Limit:          2,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, scoped.LocalID, rows[0].LocalID)
	assert.Contains(t, rows[0].RecallReason, "issue="+taskID)
	assert.Contains(t, rows[0].RecallReason, "req="+req.LocalID)
	assert.Contains(t, rows[0].RecallReason, "file=internal/config/config.go")
	assert.Contains(t, rows[0].RecallReason, "tag=daemon")
	assert.Greater(t, rows[0].RecallScore, rows[1].RecallScore)

	filtered, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:  "proj",
		IssueID:    taskID,
		Statuses:   []LearningStatus{LearningStatusAccepted},
		ActiveOnly: true,
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, scoped.LocalID, filtered[0].LocalID)
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

func TestMergeLearningFilterKeysDeduplicatesContextAndHardFilters(t *testing.T) {
	got := mergeLearningFilterKeys(
		[]string{"Daemon", "config"},
		[]string{"daemon", "review"},
	)
	assert.Equal(t, []string{"daemon", "config", "review"}, got)
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

func TestClient_UpdateLearningTargetStateRecordsLifecycle(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	learning := createAcceptedLearning(t, ctx, client, "proj", "Managed guidance target")
	promoted, err := client.PromoteLearning(ctx, learning.LocalID, PromoteLearningParams{
		Target:         LearningPromotionTargetAgents,
		TargetID:       "AGENTS.md",
		TargetHash:     "sha256:old",
		TargetMetadata: map[string]string{"path": "AGENTS.md"},
	})
	require.NoError(t, err)
	assert.Equal(t, LearningTargetStateActive, promoted.TargetState)

	drifted, err := client.UpdateLearningTargetState(ctx, learning.LocalID, UpdateLearningTargetStateParams{
		State: LearningTargetStateDrifted,
	})
	require.NoError(t, err)
	assert.Equal(t, LearningTargetStateDrifted, drifted.TargetState)
	require.NotNil(t, drifted.TargetDriftedAt)
	assert.Nil(t, drifted.TargetRetiredAt)
	assert.Equal(t, "sha256:old", drifted.TargetHash)
	assert.Equal(t, map[string]string{"path": "AGENTS.md"}, drifted.TargetMetadata)

	retired, err := client.UpdateLearningTargetState(ctx, learning.LocalID, UpdateLearningTargetStateParams{
		State:          LearningTargetStateRetired,
		TargetHash:     "sha256:retired",
		TargetMetadata: map[string]string{"path": "AGENTS.md", "managed_block": "azedarach-learning"},
	})
	require.NoError(t, err)
	assert.Equal(t, LearningTargetStateRetired, retired.TargetState)
	require.NotNil(t, retired.TargetRetiredAt)
	assert.Nil(t, retired.TargetDriftedAt)
	assert.Equal(t, "sha256:retired", retired.TargetHash)
	assert.Equal(t, map[string]string{"path": "AGENTS.md", "managed_block": "azedarach-learning"}, retired.TargetMetadata)
}

func TestClient_RelateLearningSupersedesScopedGuidanceAndExcludesTargetFromActiveRecall(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Scoped learning",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	req, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "learn-scope-req",
		Title:   "Scoped learning requirement",
		Status:  RequirementStatusOpen,
	})
	require.NoError(t, err)

	generic := createAcceptedLearning(t, ctx, client, "proj", "Use generic daemon config guidance")
	scoped := createAcceptedLearning(t, ctx, client, "proj", "Use file-specific daemon config guidance")
	scopeReqID := req.LocalID
	relation, err := client.RelateLearning(ctx, RelateLearningParams{
		Type:               LearningRelationSupersedes,
		SourceLearningID:   scoped.LocalID,
		TargetLearningID:   generic.LocalID,
		Note:               "Newer file guidance is verified against the daemon config path.",
		ScopeIssueID:       &taskID,
		ScopeRequirementID: &scopeReqID,
		ScopeTags:          []string{"daemon", "daemon"},
		ScopeFiles:         []string{"internal/config/config.go"},
	})
	require.NoError(t, err)
	assert.Equal(t, LearningRelationSupersedes, relation.Type)
	assert.Equal(t, scoped.LocalID, relation.SourceLearningID)
	assert.Equal(t, generic.LocalID, relation.TargetLearningID)
	require.NotNil(t, relation.ScopeIssueID)
	assert.Equal(t, taskID, *relation.ScopeIssueID)
	require.NotNil(t, relation.ScopeRequirementID)
	assert.Equal(t, req.LocalID, *relation.ScopeRequirementID)
	assert.Equal(t, []string{"daemon"}, relation.ScopeTags)
	assert.Equal(t, []string{"internal/config/config.go"}, relation.ScopeFiles)

	storedGeneric, err := client.GetLearning(ctx, generic.LocalID)
	require.NoError(t, err)
	require.NotNil(t, storedGeneric.SupersededAt)
	require.Len(t, storedGeneric.Relations, 1)
	assert.Equal(t, relation.LocalID, storedGeneric.Relations[0].LocalID)

	storedScoped, err := client.GetLearning(ctx, scoped.LocalID)
	require.NoError(t, err)
	require.Len(t, storedScoped.Relations, 1)
	assert.Equal(t, relation.LocalID, storedScoped.Relations[0].LocalID)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:  "proj",
		Query:      "daemon config guidance",
		Statuses:   []LearningStatus{LearningStatusAccepted},
		ActiveOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, scoped.LocalID, rows[0].LocalID)
}

func TestClient_RelateLearningConflictIsAuditableWithoutSuperseding(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	left := createAcceptedLearning(t, ctx, client, "proj", "Use daemon cache for config")
	right := createAcceptedLearning(t, ctx, client, "proj", "Avoid daemon cache for config")
	relation, err := client.RelateLearning(ctx, RelateLearningParams{
		Type:             LearningRelationConflicts,
		SourceLearningID: left.LocalID,
		TargetLearningID: right.LocalID,
		Note:             "The cache guidance conflicts and needs review before promotion.",
	})
	require.NoError(t, err)
	assert.Equal(t, LearningRelationConflicts, relation.Type)

	storedRight, err := client.GetLearning(ctx, right.LocalID)
	require.NoError(t, err)
	assert.Nil(t, storedRight.SupersededAt)
	require.Len(t, storedRight.Relations, 1)
	assert.Equal(t, relation.LocalID, storedRight.Relations[0].LocalID)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:  "proj",
		Query:      "daemon cache config",
		Statuses:   []LearningStatus{LearningStatusAccepted},
		ActiveOnly: true,
	})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestClient_RelateLearningSupersedesRequiresActiveSource(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	candidate, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Candidate replacement",
		Evidence:  "Candidate evidence.",
	})
	require.NoError(t, err)
	target := createAcceptedLearning(t, ctx, client, "proj", "Accepted target")

	_, err = client.RelateLearning(ctx, RelateLearningParams{
		Type:             LearningRelationSupersedes,
		SourceLearningID: candidate.LocalID,
		TargetLearningID: target.LocalID,
		Note:             "Candidate guidance cannot suppress active guidance.",
	})
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestClient_ListLearningsCanExcludePrivateEvidenceRows(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	public := createAcceptedLearning(t, ctx, client, "proj", "Public accepted learning")
	privateCreated, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID:       "proj",
		Summary:         "Private accepted learning",
		Evidence:        "Sensitive evidence must stay out of prime and default recall.",
		EvidencePrivate: true,
	})
	require.NoError(t, err)
	private, err := client.UpdateLearningStatus(ctx, privateCreated.LocalID, LearningStatusAccepted, "Accepted but private.")
	require.NoError(t, err)
	require.True(t, private.EvidencePrivate)

	rows, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:       "proj",
		Statuses:        []LearningStatus{LearningStatusAccepted},
		ActiveOnly:      true,
		ExcludePrivate:  true,
		IncludeEvidence: true,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, public.LocalID, rows[0].LocalID)
	assert.Contains(t, rows[0].Evidence, "Public accepted learning evidence.")

	storedPrivate, err := client.GetLearning(ctx, private.LocalID)
	require.NoError(t, err)
	assert.True(t, storedPrivate.EvidencePrivate)
	assert.Contains(t, storedPrivate.Evidence, "Sensitive evidence")
	assert.Zero(t, storedPrivate.RecallCount, "excluded private rows should not be counted as recalled")

	withPrivate, err := client.ListLearnings(ctx, LearningFilter{
		ProjectID:       "proj",
		Statuses:        []LearningStatus{LearningStatusAccepted},
		ActiveOnly:      true,
		IncludeEvidence: true,
	})
	require.NoError(t, err)
	require.Len(t, withPrivate, 2)

	got := make(map[string]Learning, len(withPrivate))
	for _, row := range withPrivate {
		got[row.LocalID] = row
	}
	require.True(t, got[private.LocalID].EvidencePrivate)
	assert.Contains(t, got[private.LocalID].Evidence, "Sensitive evidence")
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

func TestClient_CreateLearningRejectsControlCharacters(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Bad evidence",
		Evidence:  "contains\x00nul",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control characters")

	_, err = client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Bad edge evidence",
		Evidence:  "\vtrimmed control",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control characters")

	_, err = client.CreateLearning(ctx, CreateLearningParams{
		ProjectID: "proj",
		Summary:   "Allowed whitespace evidence",
		Evidence:  "line one\nline two\twith tab",
	})
	require.NoError(t, err)
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

func setLearningUpdatedTime(t *testing.T, ctx context.Context, db sqlIssueExecer, localID string, value time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, "UPDATE agent_learnings SET updated_at = ? WHERE local_id = ?", formatTimestamp(value), localID)
	require.NoError(t, err)
}

func setLearningTargetState(t *testing.T, ctx context.Context, db sqlIssueExecer, localID string, state LearningTargetState) {
	t.Helper()
	_, err := db.ExecContext(ctx, "UPDATE agent_learnings SET target_state = ? WHERE local_id = ?", string(state), localID)
	require.NoError(t, err)
}

func decisionLinkKinds(links []DecisionLink) []DecisionTargetKind {
	out := make([]DecisionTargetKind, 0, len(links))
	for _, link := range links {
		out = append(out, link.TargetKind)
	}
	return out
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
