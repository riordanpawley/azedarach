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

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_BoardViewsMigrationSeedsDefaultsOnFreshDBAndRestartsIdempotently(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	client := NewClientAtPath(dbPath, slog.Default())
	db, err := client.dbHandle()
	require.NoError(t, err)

	views, err := client.ListBoardViews(ctx, "default")
	require.NoError(t, err)
	assertPromisedBuiltInBoardViews(t, views)
	assertBoardViewsMigrationApplied(t, ctx, db)
	assertBoardViewsDefaultCount(t, ctx, db, 6)
	var seededAt string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT updated_at FROM board_views WHERE project_id = 'default' AND id = ?`, domain.BoardViewDefaultID).Scan(&seededAt))
	require.NoError(t, client.CloseDB())

	reopened := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { require.NoError(t, reopened.CloseDB()) })
	reopenedDB, err := reopened.dbHandle()
	require.NoError(t, err)
	assertBoardViewsMigrationApplied(t, ctx, reopenedDB)
	assertBoardViewsDefaultCount(t, ctx, reopenedDB, 6)
	var reopenedAt string
	require.NoError(t, reopenedDB.QueryRowContext(ctx, `SELECT updated_at FROM board_views WHERE project_id = 'default' AND id = ?`, domain.BoardViewDefaultID).Scan(&reopenedAt))
	assert.Equal(t, seededAt, reopenedAt, "idempotent reopen must not rewrite canonical built-ins")
	var firstUpdatedAt, secondUpdatedAt string
	require.NoError(t, reopenedDB.QueryRowContext(ctx, `SELECT updated_at FROM board_views WHERE project_id='default' AND id=?`, domain.BoardViewOrchestrationID).Scan(&firstUpdatedAt))
	_, err = reopened.ListBoardViews(ctx, "default")
	require.NoError(t, err)
	require.NoError(t, reopenedDB.QueryRowContext(ctx, `SELECT updated_at FROM board_views WHERE project_id='default' AND id=?`, domain.BoardViewOrchestrationID).Scan(&secondUpdatedAt))
	assert.Equal(t, firstUpdatedAt, secondUpdatedAt, "idempotent reseed should not mutate canonical built-in")
}

func TestClient_BoardViewsMigrationSeedsDefaultsForExistingDBAndKeepsCustomViews(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seedBoardViewsPreMigrationDB(t, dbPath, true)

	client := NewClientAtPath(dbPath, slog.Default())
	db, err := client.dbHandle()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })

	views, err := client.ListBoardViews(ctx, "default")
	require.NoError(t, err)
	assertPromisedBuiltInBoardViews(t, views)
	assert.True(t, boardViewTestHasView(views, "custom-active"))
	assertBoardViewsMigrationApplied(t, ctx, db)
	assertBoardViewsDefaultCount(t, ctx, db, 7)
}

func TestClient_BoardViewsStartupRepairsDriftedBuiltInCatalog(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	client := NewClientAtPath(dbPath, slog.Default())
	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		UPDATE board_views
		SET name = 'Broken', definition_json = '{"schema_version":999}', deleted_at = '2026-07-09T00:00:00Z'
		WHERE project_id = 'default' AND id = ?
	`, domain.BoardViewDefaultID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO board_views (project_id, id, name, definition_json, built_in, created_at, updated_at, deleted_at)
		VALUES ('drifted-project', ?, 'Broken', '{"schema_version":999}', 1, '2026-07-09T00:00:00Z', '2026-07-09T00:00:00Z', NULL)
	`, domain.BoardViewDefaultID)
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	reopened := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { require.NoError(t, reopened.CloseDB()) })
	record, err := reopened.GetBoardView(ctx, "drifted-project", domain.DefaultBoardViewID)
	require.NoError(t, err)
	assert.True(t, record.BuiltIn)
	assert.Equal(t, domain.DefaultBoardView().Title, record.View.Title)
	views, err := reopened.ListBoardViews(ctx, "drifted-project")
	require.NoError(t, err)
	assertPromisedBuiltInBoardViews(t, views)
}

func TestClient_RefusesPartialBoardViewsSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seedBoardViewsPreMigrationDB(t, dbPath, false)
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE board_views (
			project_id TEXT NOT NULL,
			id TEXT NOT NULL,
			PRIMARY KEY (project_id, id)
		)
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	client := NewClientAtPath(dbPath, slog.Default())
	_, err = client.dbHandle()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing startup with partial board_views schema")
	assert.Contains(t, err.Error(), "definition_json")
	assert.Contains(t, err.Error(), "restore the database from backup before retrying")

	db, err = sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	defer db.Close()
	var title string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT title FROM issues WHERE id = 'az-board-open'`).Scan(&title))
	assert.Equal(t, "Open board issue", title)
	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id = ?)`, boardViewsMigrationID).Scan(&applied))
	assert.False(t, applied)
}

func TestClient_BoardViewsMigrationFailureRollsBackLiveSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seedBoardViewsPreMigrationDB(t, dbPath, false)

	client := NewClientAtPath(dbPath, slog.Default())
	client.boardViewsMigrationFailureHook = func(stage string) error {
		if stage == "after_schema" {
			return errors.New("boom")
		}
		return nil
	}
	_, err := client.dbHandle()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration 0031_board_views rolled back")
	assert.Contains(t, err.Error(), "injected board views migration failure at after_schema")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	defer db.Close()
	exists, err := tableExists(db, "board_views")
	require.NoError(t, err)
	assert.False(t, exists)
	var title string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT title FROM issues WHERE id = 'az-board-open'`).Scan(&title))
	assert.Equal(t, "Open board issue", title)
	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id = ?)`, boardViewsMigrationID).Scan(&applied))
	assert.False(t, applied)
}

func seedBoardViewsPreMigrationDB(t *testing.T, dbPath string, includeCustomBoardView bool) {
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
			deleted_at TEXT,
			lifecycle_state TEXT NOT NULL DEFAULT 'open',
			closed_outcome TEXT NOT NULL DEFAULT 'none',
			review_state TEXT NOT NULL DEFAULT 'none',
			archived_at TEXT
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
		INSERT INTO meta (key, value) VALUES (?, ?);
		INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at, lifecycle_state, closed_outcome, review_state)
		VALUES ('az-board-open', 'Open board issue', 'open', 2, 'task', ?, ?, 'open', 'none', 'none');
	`, issueStateModelVersionMetaKey, issueStateModelV2Version, now, now)
	require.NoError(t, err)

	for _, migration := range orderedMigrations {
		if migration.id == "0015_issue_attachments" || migration.id == "0019_issue_observation_events" || migration.id == boardViewsMigrationID || migration.id == "0035_interaction_requests" || migration.id == "0045_issue_state_runtime_constraints" || migration.id == humanAuthorityProjectionMigrationID || migration.id == projectionDeltaAuthorityMigrationID || migration.id == decisionPropagationOutboxMigrationID || migration.id == "0049_managed_agent_incarnations" || migration.id == issueObservationEventSearchMigrationID || migration.id == mailboxObservationProjectionCutoverMigrationID || migration.id == decisionIdempotencyMigrationID || migration.id == gitHookRefreshIntentsMigrationID || migration.id == rootedSessionRoleExclusivityMigrationID || migration.id == legacyAttachmentBlobForwardMigrationID || migration.id == agentInputDeliveryMigrationID || migration.id == orchestrationStartIntentsMigrationID || migration.id == taskCreationIntentsMigrationID || migration.id == managedAgentThreadIdentityMigrationID {
			continue
		}
		_, err := db.Exec(`INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, migration.id, now)
		require.NoError(t, err)
	}

	if !includeCustomBoardView {
		return
	}
	require.NoError(t, ensureBoardViewsSchema(db))
	definitionJSON, err := domain.EncodeBoardViewDefinitionJSON(boardViewTestCustomView("custom-active"))
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO board_views (project_id, id, name, definition_json, built_in, created_at, updated_at, deleted_at)
		VALUES ('default', 'custom-active', 'Custom Active', ?, 0, ?, ?, NULL)
	`, strings.TrimSpace(string(definitionJSON)), now, now)
	require.NoError(t, err)
}

func assertBoardViewsMigrationApplied(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id = ?)`, boardViewsMigrationID).Scan(&applied))
	assert.True(t, applied)
}

func assertBoardViewsDefaultCount(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM board_views
		WHERE project_id = 'default' AND deleted_at IS NULL
	`).Scan(&count))
	assert.Equal(t, want, count)
}

func assertPromisedBuiltInBoardViews(t *testing.T, views []domain.BoardViewRecord) {
	t.Helper()
	for _, id := range []domain.BoardViewID{domain.BoardViewDefaultID, domain.BoardViewPlanningID, domain.BoardViewOrchestrationID, domain.BoardViewCloseoutID, domain.BoardViewGridID, domain.BoardViewTreeID} {
		assert.True(t, boardViewTestHasView(views, string(id)), "missing built-in view %s", id)
	}
	assert.False(t, boardViewTestHasView(views, string(domain.BoardViewCurrentID)))
	assert.False(t, boardViewTestHasView(views, string(domain.BoardViewActivityID)))
}
