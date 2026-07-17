package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_MigratesIssueStateModelV2WithBackup(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seedIssueStateModelV1DB(t, dbPath)

	client := NewClientAtPath(dbPath, slog.Default())
	db, err := client.dbHandle()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })

	rows, err := db.QueryContext(ctx, `
		SELECT id, lifecycle_state, closed_outcome, review_state, archived_at
		FROM issues
		ORDER BY id
	`)
	require.NoError(t, err)
	defer rows.Close()

	got := map[string][]sql.NullString{}
	for rows.Next() {
		var (
			id            string
			lifecycle     sql.NullString
			closedOutcome sql.NullString
			reviewState   sql.NullString
			archivedAt    sql.NullString
		)
		require.NoError(t, rows.Scan(&id, &lifecycle, &closedOutcome, &reviewState, &archivedAt))
		got[id] = []sql.NullString{lifecycle, closedOutcome, reviewState, archivedAt}
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, "open", got["az-open"][0].String)
	assert.Equal(t, "none", got["az-open"][1].String)
	assert.Equal(t, "none", got["az-open"][2].String)
	assert.False(t, got["az-open"][3].Valid)

	assert.Equal(t, "active", got["az-active"][0].String)
	assert.Equal(t, "none", got["az-active"][1].String)
	assert.Equal(t, "none", got["az-active"][2].String)

	assert.Equal(t, "active", got["az-review"][0].String)
	assert.Equal(t, "none", got["az-review"][1].String)
	assert.Equal(t, "requested", got["az-review"][2].String)

	assert.Equal(t, "closed", got["az-closed"][0].String)
	assert.Equal(t, "completed", got["az-closed"][1].String)
	assert.Equal(t, "none", got["az-closed"][2].String)

	assert.Equal(t, "open", got["az-archived"][0].String)
	assert.Equal(t, "none", got["az-archived"][1].String)
	assert.Equal(t, "none", got["az-archived"][2].String)
	assert.Equal(t, "2026-07-01T00:00:00Z", got["az-archived"][3].String)

	var version string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, issueStateModelVersionMetaKey).Scan(&version))
	assert.Equal(t, issueStateModelV2Version, version)

	marker, ok, err := readIssueStateModelV2CutoverMarker(ctx, db)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "complete", marker.State)
	require.FileExists(t, marker.BackupPath)

	backupDB, err := sql.Open("sqlite", "file:"+marker.BackupPath)
	require.NoError(t, err)
	defer backupDB.Close()
	backupColumns, err := tableColumns(backupDB, "issues")
	require.NoError(t, err)
	assert.NotContains(t, backupColumns, "lifecycle_state")
	assert.Contains(t, backupColumns, "status")

	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id = ?)
	`, issueStateModelV2MigrationID).Scan(&applied))
	assert.True(t, applied)
}

func TestClient_IssueStateModelV2MigratesResidualBlockedStatus(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seedIssueStateModelV1DB(t, dbPath)

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at)
		VALUES ('az-blocked', 'Residual blocked issue', 'blocked', 2, 'task', ?, ?)
	`, now, now)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	client := NewClientAtPath(dbPath, slog.Default())
	db, err = client.dbHandle()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })

	var (
		status        string
		lifecycle     string
		closedOutcome string
		reviewState   string
	)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status, lifecycle_state, closed_outcome, review_state
		FROM issues
		WHERE id = 'az-blocked'
	`).Scan(&status, &lifecycle, &closedOutcome, &reviewState))
	assert.Equal(t, "open", status)
	assert.Equal(t, "open", lifecycle)
	assert.Equal(t, "none", closedOutcome)
	assert.Equal(t, "none", reviewState)

	var blockerStatus, blockerLifecycle string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status, lifecycle_state
		FROM issues
		WHERE id = 'az-blocked-legacy-blocker'
	`).Scan(&blockerStatus, &blockerLifecycle))
	assert.Equal(t, "open", blockerStatus)
	assert.Equal(t, "open", blockerLifecycle)

	var depCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM issue_dependencies
		WHERE issue_id = 'az-blocked'
			AND depends_on_id = 'az-blocked-legacy-blocker'
			AND dependency_type = 'blocks'
			AND tombstoned_at IS NULL
	`).Scan(&depCount))
	assert.Equal(t, 1, depCount)
}

func TestClient_IssueStateModelV2MigrationFailureRollsBackLiveSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seedIssueStateModelV1DB(t, dbPath)

	client := NewClientAtPath(dbPath, slog.Default())
	client.stateModelV2MigrationFailureHook = func(stage string) error {
		if stage == "after_columns" {
			return errors.New("boom")
		}
		return nil
	}
	_, err := client.dbHandle()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration 0029_issue_state_model_v2 rolled back")
	assert.Contains(t, err.Error(), "backup=")
	assert.Contains(t, err.Error(), "restore the backup before retrying")

	db, openErr := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, openErr)
	defer db.Close()

	columns, err := tableColumns(db, "issues")
	require.NoError(t, err)
	assert.NotContains(t, columns, "lifecycle_state")
	assert.NotContains(t, columns, "closed_outcome")
	assert.NotContains(t, columns, "review_state")
	assert.NotContains(t, columns, "archived_at")

	var status string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM issues WHERE id = 'az-review'`).Scan(&status))
	assert.Equal(t, "in_review", status)

	marker, ok, err := readIssueStateModelV2CutoverMarker(ctx, db)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "failed", marker.State)
	require.FileExists(t, marker.BackupPath)

	retry := NewClientAtPath(dbPath, slog.Default())
	_, retryErr := retry.dbHandle()
	require.Error(t, retryErr)
	assert.Contains(t, retryErr.Error(), "refusing startup after partial issue state-model v2 cutover")
	assert.Contains(t, retryErr.Error(), marker.BackupPath)
}

func TestClient_RefusesPartialIssueStateModelV2MigrationMarker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seedIssueStateModelV1DB(t, dbPath)

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	require.NoError(t, writeIssueStateModelV2CutoverMarker(context.Background(), db, issueStateModelV2CutoverMarker{
		State:      "in_progress",
		StartedAt:  startedAt,
		BackupPath: filepath.Join(filepath.Dir(dbPath), "azedarach.db.state-model-v1.test.bak"),
	}))
	require.NoError(t, db.Close())

	client := NewClientAtPath(dbPath, slog.Default())
	_, err = client.dbHandle()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing startup after partial issue state-model v2 cutover")
	assert.Contains(t, err.Error(), "restore the backup before retrying")

	db, err = sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	defer db.Close()
	columns, err := tableColumns(db, "issues")
	require.NoError(t, err)
	assert.NotContains(t, columns, "lifecycle_state")
}

func TestValidateIssueStateModelV2RowsRejectsImpossibleStateCombinations(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		lifecycle     string
		closedOutcome any
		reviewState   string
		wantErr       string
	}{
		{
			name:          "closed lifecycle rejects null outcome",
			lifecycle:     "closed",
			closedOutcome: nil,
			reviewState:   "none",
			wantErr:       "invalid closed_outcome for closed lifecycle",
		},
		{
			name:          "non-closed lifecycle rejects null outcome",
			lifecycle:     "open",
			closedOutcome: nil,
			reviewState:   "none",
			wantErr:       "invalid closed_outcome for non-closed lifecycle",
		},
		{
			name:          "closed lifecycle requires terminal outcome",
			lifecycle:     "closed",
			closedOutcome: "none",
			reviewState:   "none",
			wantErr:       "invalid closed_outcome for closed lifecycle",
		},
		{
			name:          "non-closed lifecycle cannot carry terminal outcome",
			lifecycle:     "open",
			closedOutcome: "completed",
			reviewState:   "none",
			wantErr:       "invalid closed_outcome for non-closed lifecycle",
		},
		{
			name:          "review requested requires active lifecycle",
			lifecycle:     "open",
			closedOutcome: "none",
			reviewState:   "requested",
			wantErr:       "review requested for non-active lifecycle",
		},
		{
			name:          "closed lifecycle cannot carry review request",
			lifecycle:     "closed",
			closedOutcome: "completed",
			reviewState:   "requested",
			wantErr:       "review requested for non-active lifecycle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			defer db.Close()

			_, err = db.ExecContext(ctx, `
				CREATE TABLE issues (
					id TEXT PRIMARY KEY,
					lifecycle_state TEXT,
					closed_outcome TEXT,
					review_state TEXT,
					archived_at TEXT,
					deleted_at TEXT
				);
				INSERT INTO issues (id, lifecycle_state, closed_outcome, review_state, archived_at, deleted_at)
				VALUES ('az-invalid', ?, ?, ?, NULL, NULL);
			`, tt.lifecycle, tt.closedOutcome, tt.reviewState)
			require.NoError(t, err)

			err = validateIssueStateModelV2Rows(ctx, db)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func seedIssueStateModelV1DB(t *testing.T, dbPath string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	defer db.Close()

	now := "2026-07-09T00:00:00Z"
	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE projection_source_revision (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			revision INTEGER NOT NULL CHECK (revision >= 0)
		);
		INSERT INTO projection_source_revision(singleton, revision) VALUES (1, 4611686018427387904);
		CREATE TABLE decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			local_id TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL,
			rationale TEXT,
			context TEXT,
			consequences TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		);
	`)
	require.NoError(t, err)

	for _, migration := range orderedMigrations {
		if migration.id == "0019_issue_observation_events" || migration.id == issueStateModelV2MigrationID || migration.id == "0035_interaction_requests" || migration.id == "0045_issue_state_runtime_constraints" || migration.id == humanAuthorityProjectionMigrationID || migration.id == projectionDeltaAuthorityMigrationID || migration.id == decisionPropagationOutboxMigrationID || migration.id == "0049_managed_agent_incarnations" || migration.id == issueObservationEventSearchMigrationID || migration.id == mailboxObservationProjectionCutoverMigrationID || migration.id == decisionIdempotencyMigrationID || migration.id == agentInputDeliveryMigrationID || migration.id == agentInputDeliveryFencingMigrationID {
			continue
		}
		_, err := db.Exec(`INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, migration.id, now)
		require.NoError(t, err)
	}

	_, err = db.Exec(`
		INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at, closed_at, deleted_at)
		VALUES
			('az-open', 'Open', 'open', 2, 'task', ?, ?, NULL, NULL),
			('az-active', 'Active', 'in_progress', 2, 'task', ?, ?, NULL, NULL),
			('az-review', 'Review ready', 'in_review', 2, 'task', ?, ?, NULL, NULL),
			('az-closed', 'Closed', 'closed', 2, 'task', ?, ?, ?, NULL),
			('az-archived', 'Archived', 'open', 2, 'task', ?, ?, NULL, '2026-07-01T00:00:00Z')
	`, now, now, now, now, now, now, now, now, now, now, now)
	require.NoError(t, err)

	files, err := filepath.Glob(dbPath + ".state-model-v1.*.bak")
	require.NoError(t, err)
	require.Empty(t, files, strings.Join(files, ","))
}
