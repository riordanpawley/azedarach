package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueObservationEventSearchMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	issueID, err := seed.Create(ctx, CreateTaskParams{Title: "preserved", Type: domain.TypeTask})
	require.NoError(t, err)
	want, err := seed.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, Source: "worker", Payload: map[string]any{"summary": "projection checkpoint retained"}})
	require.NoError(t, err)
	db, err := seed.dbHandle()
	require.NoError(t, err)
	require.NoError(t, downgradeBeforeIssueObservationEventSearchMigration(ctx, db))
	require.NoError(t, seed.CloseDB())

	upgraded := NewClientAtPath(path, slog.Default())
	db, err = upgraded.dbHandle()
	require.NoError(t, err)
	require.NoError(t, validateIssueObservationEventSearchSchema(ctx, db))
	require.NoError(t, validateIssueObservationEventSearchCoverage(ctx, db))
	page, err := upgraded.QueryIssueObservationEvents(ctx, issueID, IssueObservationEventQuery{Query: "projection checkpoint"})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, want.ID, page.Events[0].ID)
	var checksum, appliedAt string
	require.NoError(t, db.QueryRow(`SELECT artifact_checksum,applied_at FROM schema_migrations WHERE id=?`, issueObservationEventSearchMigrationID).Scan(&checksum, &appliedAt))
	assert.Equal(t, "e5a8efc20ddf313822576c4d6d42cd94e1837dfac810834957689d30b952005d", checksum)
	require.NoError(t, upgraded.CloseDB())

	reopened := NewClientAtPath(path, slog.Default())
	reopenedDB, err := reopened.dbHandle()
	require.NoError(t, err)
	defer reopened.CloseDB()
	var markerCount int
	var reopenedAppliedAt string
	require.NoError(t, reopenedDB.QueryRow(`SELECT COUNT(*),MAX(applied_at) FROM schema_migrations WHERE id=?`, issueObservationEventSearchMigrationID).Scan(&markerCount, &reopenedAppliedAt))
	assert.Equal(t, 1, markerCount)
	assert.Equal(t, appliedAt, reopenedAppliedAt, "idempotent reopen must not rewrite the ledger")
}

func TestIssueObservationEventSearchMigrationRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	issueID, err := seed.Create(ctx, CreateTaskParams{Title: "rollback sentinel", Type: domain.TypeTask})
	require.NoError(t, err)
	db, err := seed.dbHandle()
	require.NoError(t, err)
	require.NoError(t, downgradeBeforeIssueObservationEventSearchMigration(ctx, db))
	require.NoError(t, seed.CloseDB())

	failed := NewClientAtPath(path, slog.Default())
	failed.eventSearchMigrationFailureHook = func(stage string) error {
		if stage == "after_schema" {
			return errors.New("injected interruption")
		}
		return nil
	}
	_, err = failed.dbHandle()
	require.ErrorContains(t, err, "rolled back")
	_ = failed.CloseDB()
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var tableCount, markerCount, issueCount int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='issue_observation_event_search_fts'`).Scan(&tableCount)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, issueObservationEventSearchMigrationID).Scan(&markerCount)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM issues WHERE id=?`, issueID).Scan(&issueCount)
	require.NoError(t, raw.Close())
	assert.Equal(t, 0, tableCount)
	assert.Equal(t, 0, markerCount)
	assert.Equal(t, 1, issueCount)

	retried := NewClientAtPath(path, slog.Default())
	retryDB, err := retried.dbHandle()
	require.NoError(t, err)
	defer retried.CloseDB()
	require.NoError(t, validateIssueObservationEventSearchSchema(ctx, retryDB))
	require.NoError(t, validateIssueObservationEventSearchCoverage(ctx, retryDB))
}

func TestIssueObservationEventSearchMigrationRejectsAppliedSchemaDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate string
		want   string
	}{
		{name: "missing trigger", mutate: `DROP TRIGGER issue_observation_events_ai_search_fts`, want: "issue_observation_events_ai_search_fts"},
		{name: "wrong index", mutate: `DROP INDEX idx_issue_observation_events_issue_source_id; CREATE INDEX idx_issue_observation_events_issue_source_id ON issue_observation_events(source, id)`, want: "drifted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			seed := NewClientAtPath(path, slog.Default())
			db, err := seed.dbHandle()
			require.NoError(t, err)
			_, err = db.Exec(tc.mutate)
			require.NoError(t, err)
			require.NoError(t, seed.CloseDB())
			reopened := NewClientAtPath(path, slog.Default())
			_, err = reopened.dbHandle()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("drift error=%v, want %q", err, tc.want)
			}
			_ = reopened.CloseDB()
		})
	}
}

func downgradeBeforeIssueObservationEventSearchMigration(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS issue_observation_events_ai_search_fts`,
		`DROP TRIGGER IF EXISTS issue_observation_events_ad_search_fts`,
		`DROP TRIGGER IF EXISTS issue_observation_events_au_search_fts`,
		`DROP TABLE IF EXISTS issue_observation_event_search_fts`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_source_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_source_command_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_operation_id_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_session_id_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_worktree_path_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_payload_outcome_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_payload_disposition_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_payload_decision_id_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_payload_revision_id`,
		`DROP INDEX IF EXISTS idx_issue_observation_events_issue_payload_actor_id_id`,
		`DELETE FROM schema_migrations WHERE id='0050_issue_observation_event_search'`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
