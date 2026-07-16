package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/sqlitemigration"
	"github.com/stretchr/testify/require"
)

func TestProjectionDeltaChecksumCompatibilityCurrentMainOpenedDeltaFirst(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	require.NoError(t, seed.OpenProjectionDeltaStore())
	db, err := seed.dbHandle()
	require.NoError(t, err)
	for _, trigger := range humanAuthorityProjectionRevisionTriggers {
		_, err = db.Exec(`DROP TRIGGER ` + trigger)
		require.NoError(t, err)
	}
	_, err = db.Exec(`DELETE FROM schema_migrations WHERE id=?`, humanAuthorityProjectionMigrationID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE schema_migrations SET applied_at='2026-07-13T16:23:54.14364Z',artifact_checksum=NULL WHERE id=?`, projectionDeltaAuthorityMigrationID)
	require.NoError(t, err)
	currentMainArtifacts := make([]sqlitemigration.Artifact, 0, len(migrationArtifacts)-1)
	for _, artifact := range migrationArtifacts {
		if artifact.ID != projectionDeltaAuthorityMigrationID {
			currentMainArtifacts = append(currentMainArtifacts, artifact)
		}
	}
	require.NoError(t, sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, currentMainArtifacts))
	require.NoError(t, seed.applyHumanAuthorityProjectionMigration(ctx, db, humanAuthorityProjectionMigrationID))
	require.NoError(t, seed.CloseDB())

	candidate := NewClientAtPath(path, slog.Default())
	require.NoError(t, candidate.OpenProjectionDeltaStore())
	require.NoError(t, candidate.CloseDB())
	db = openProjectionMigrationFixture(t, path)
	var deltaChecksum, humanChecksum string
	require.NoError(t, db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, projectionDeltaAuthorityMigrationID).Scan(&deltaChecksum))
	require.NoError(t, db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, humanAuthorityProjectionMigrationID).Scan(&humanChecksum))
	require.Equal(t, projectionDeltaAuthorityChecksum, deltaChecksum)
	require.Equal(t, "ac3a48512b2e6e9c018d58a68db24a2465e9d172139d22f8378f69677073a0ab", humanChecksum)
	require.NoError(t, validateHumanAuthorityProjectionRevisionTriggers(ctx, db))
	require.NoError(t, db.Close())
}

func TestProjectionDeltaChecksumCompatibilityPreservesHistoricalOrders(t *testing.T) {
	orders := []struct {
		name, deltaAt, humanAt string
	}{
		{name: "delta_then_human", deltaAt: "2026-07-13T16:23:54.14364Z", humanAt: "2026-07-13T17:00:00Z"},
		{name: "human_then_delta", deltaAt: "2026-07-13T17:00:00Z", humanAt: "2026-07-13T12:59:54.578475Z"},
	}
	for _, tc := range orders {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			seed := NewClientAtPath(path, slog.Default())
			require.NoError(t, seed.OpenProjectionDeltaStore())
			require.NoError(t, seed.CloseDB())
			db := openProjectionMigrationFixture(t, path)
			_, err := db.Exec(`UPDATE schema_migrations SET applied_at=?, artifact_checksum=NULL WHERE id=?`, tc.deltaAt, projectionDeltaAuthorityMigrationID)
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE schema_migrations SET applied_at=? WHERE id=?`, tc.humanAt, humanAuthorityProjectionMigrationID)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			upgraded := NewClientAtPath(path, slog.Default())
			require.NoError(t, upgraded.OpenProjectionDeltaStore())
			require.NoError(t, upgraded.CloseDB())
			db = openProjectionMigrationFixture(t, path)
			var appliedAt, checksum string
			require.NoError(t, db.QueryRow(`SELECT applied_at,artifact_checksum FROM schema_migrations WHERE id=?`, projectionDeltaAuthorityMigrationID).Scan(&appliedAt, &checksum))
			require.Equal(t, tc.deltaAt, appliedAt)
			require.Equal(t, projectionDeltaAuthorityChecksum, checksum)
			require.NoError(t, db.Close())

			second := NewClientAtPath(path, slog.Default())
			second.projectionDeltaChecksumRepairHook = func(string) error { return errors.New("must not run on second open") }
			require.NoError(t, second.OpenProjectionDeltaStore())
			require.NoError(t, second.CloseDB())
		})
	}
}

func TestProjectionDeltaChecksumCompatibilityCurrentMainHumanFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	require.NoError(t, seed.OpenProjectionDeltaStore())
	require.NoError(t, seed.CloseDB())
	db := openProjectionMigrationFixture(t, path)
	_, err := db.Exec(`
		PRAGMA foreign_keys=OFF;
		DROP TABLE projection_consumer_cursors;
		DROP TABLE projection_deltas;
		DROP TABLE projection_streams;
		DELETE FROM schema_migrations WHERE id=?;
	`, projectionDeltaAuthorityMigrationID)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	merged := NewClientAtPath(path, slog.Default())
	require.NoError(t, merged.OpenProjectionDeltaStore())
	require.NoError(t, merged.CloseDB())
	db = openProjectionMigrationFixture(t, path)
	var checksum string
	require.NoError(t, db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, projectionDeltaAuthorityMigrationID).Scan(&checksum))
	require.Equal(t, projectionDeltaAuthorityChecksum, checksum)
	require.NoError(t, validateProjectionDeltaAuthoritySchema(context.Background(), db))
	require.NoError(t, db.Close())
}

func TestProjectionDeltaMigrationValidationFailureRollsBackSchemaAndLedger(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	db := openProjectionMigrationFixture(t, path)
	_, err := db.Exec(`
		CREATE TABLE schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL,
			artifact_checksum TEXT
		);
		CREATE TABLE legacy_authority (id TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO legacy_authority(id,value) VALUES ('kept','original');
	`)
	require.NoError(t, err)

	err = applyProjectionDeltaAuthorityMigrationWithValidator(ctx, db, projectionDeltaAuthorityMigrationID, func(ctx context.Context, reader projectionDeltaSchemaReader) error {
		require.NoError(t, validateProjectionDeltaAuthoritySchema(ctx, reader), "migration schema must be complete before the injected failure")
		return errors.New("injected in-transaction projection schema validation failure")
	})
	require.ErrorContains(t, err, "injected in-transaction projection schema validation failure")

	for _, object := range []string{"projection_streams", "projection_deltas", "projection_consumer_cursors", "idx_projection_deltas_key_history"} {
		var exists bool
		require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=?)`, object).Scan(&exists))
		require.False(t, exists, object+" must roll back")
	}
	var ledgerExists bool
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id=?)`, projectionDeltaAuthorityMigrationID).Scan(&ledgerExists))
	require.False(t, ledgerExists)
	var legacyValue string
	require.NoError(t, db.QueryRow(`SELECT value FROM legacy_authority WHERE id='kept'`).Scan(&legacyValue))
	require.Equal(t, "original", legacyValue)

	require.NoError(t, applyProjectionDeltaAuthorityMigration(ctx, db, projectionDeltaAuthorityMigrationID))
	require.NoError(t, validateProjectionDeltaAuthoritySchema(ctx, db))
	require.NoError(t, db.Close())
}

func TestProjectionDeltaChecksumCompatibilityRejectsStructuralDrift(t *testing.T) {
	mutations := map[string]string{
		"index_direction":     `DROP INDEX idx_projection_deltas_key_history; CREATE INDEX idx_projection_deltas_key_history ON projection_deltas(project_id,kind,key,cursor ASC);`,
		"missing_not_null":    malformedProjectionConsumerDDL("updated_at TEXT", "PRIMARY KEY (project_id,consumer)", "FOREIGN KEY(project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE"),
		"wrong_primary_key":   malformedProjectionConsumerDDL("updated_at TEXT NOT NULL", "PRIMARY KEY (consumer,project_id)", "FOREIGN KEY(project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE"),
		"missing_foreign_key": malformedProjectionConsumerDDL("updated_at TEXT NOT NULL", "PRIMARY KEY (project_id,consumer)", ""),
		"missing_unique": `
			PRAGMA foreign_keys=OFF;
			DROP INDEX idx_projection_deltas_key_history;
			ALTER TABLE projection_deltas RENAME TO projection_deltas_old;
			CREATE TABLE projection_deltas (
				project_id TEXT NOT NULL, cursor INTEGER NOT NULL CHECK (cursor > 0),
				kind TEXT NOT NULL CHECK (trim(kind) != ''), key TEXT NOT NULL CHECK (trim(key) != ''),
				operation TEXT NOT NULL CHECK (operation IN ('upsert', 'delete')),
				idempotency_key TEXT NOT NULL CHECK (trim(idempotency_key) != ''), payload_json TEXT NOT NULL,
				committed_at TEXT NOT NULL, PRIMARY KEY (project_id,cursor),
				FOREIGN KEY(project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE
			);
			DROP TABLE projection_deltas_old;
			CREATE INDEX idx_projection_deltas_key_history ON projection_deltas(project_id,kind,key,cursor DESC);
		`,
		"missing_check": `
			PRAGMA foreign_keys=OFF;
			DROP INDEX idx_projection_deltas_key_history;
			ALTER TABLE projection_deltas RENAME TO projection_deltas_old;
			CREATE TABLE projection_deltas (
				project_id TEXT NOT NULL, cursor INTEGER NOT NULL,
				kind TEXT NOT NULL CHECK (trim(kind) != ''), key TEXT NOT NULL CHECK (trim(key) != ''),
				operation TEXT NOT NULL CHECK (operation IN ('upsert', 'delete')),
				idempotency_key TEXT NOT NULL CHECK (trim(idempotency_key) != ''), payload_json TEXT NOT NULL,
				committed_at TEXT NOT NULL, PRIMARY KEY (project_id,cursor), UNIQUE(project_id,idempotency_key),
				FOREIGN KEY(project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE
			);
			DROP TABLE projection_deltas_old;
			CREATE INDEX idx_projection_deltas_key_history ON projection_deltas(project_id,kind,key,cursor DESC);
		`,
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			seed := NewClientAtPath(path, slog.Default())
			require.NoError(t, seed.OpenProjectionDeltaStore())
			require.NoError(t, seed.CloseDB())
			db := openProjectionMigrationFixture(t, path)
			_, err := db.Exec(mutation)
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE schema_migrations SET artifact_checksum=NULL WHERE id=?`, projectionDeltaAuthorityMigrationID)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			reopened := NewClientAtPath(path, slog.Default())
			err = reopened.OpenProjectionDeltaStore()
			require.Error(t, err)
			require.Contains(t, err.Error(), "refuse projection delta checksum compatibility conversion")
			_ = reopened.CloseDB()
			db = openProjectionMigrationFixture(t, path)
			var checksum sql.NullString
			require.NoError(t, db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, projectionDeltaAuthorityMigrationID).Scan(&checksum))
			require.False(t, checksum.Valid && strings.TrimSpace(checksum.String) != "")
			require.NoError(t, db.Close())
		})
	}
}

func TestProjectionDeltaChecksumCompatibilityRollsBackAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	require.NoError(t, seed.OpenProjectionDeltaStore())
	require.NoError(t, seed.CloseDB())
	db := openProjectionMigrationFixture(t, path)
	const originalAppliedAt = "2026-07-13T16:23:54.14364Z"
	_, err := db.Exec(`UPDATE schema_migrations SET applied_at=?,artifact_checksum=NULL WHERE id=?`, originalAppliedAt, projectionDeltaAuthorityMigrationID)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	failed := NewClientAtPath(path, slog.Default())
	failed.projectionDeltaChecksumRepairHook = func(string) error { return errors.New("injected conversion failure") }
	err = failed.OpenProjectionDeltaStore()
	require.ErrorContains(t, err, "injected conversion failure")
	_ = failed.CloseDB()
	db = openProjectionMigrationFixture(t, path)
	var appliedAt string
	var checksum sql.NullString
	require.NoError(t, db.QueryRow(`SELECT applied_at,artifact_checksum FROM schema_migrations WHERE id=?`, projectionDeltaAuthorityMigrationID).Scan(&appliedAt, &checksum))
	require.Equal(t, originalAppliedAt, appliedAt)
	require.False(t, checksum.Valid)
	require.NoError(t, db.Close())

	retried := NewClientAtPath(path, slog.Default())
	require.NoError(t, retried.OpenProjectionDeltaStore())
	require.NoError(t, retried.CloseDB())
}

func openProjectionMigrationFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	return db
}

func malformedProjectionConsumerDDL(updatedAt, primaryKey, foreignKey string) string {
	constraints := primaryKey
	if foreignKey != "" {
		constraints += "," + foreignKey
	}
	return `
		PRAGMA foreign_keys=OFF;
		ALTER TABLE projection_consumer_cursors RENAME TO projection_consumer_cursors_old;
		CREATE TABLE projection_consumer_cursors (
			project_id TEXT NOT NULL, consumer TEXT NOT NULL CHECK (trim(consumer) != ''),
			cursor INTEGER NOT NULL DEFAULT 0 CHECK (cursor >= 0), ` + updatedAt + `, ` + constraints + `
		);
		DROP TABLE projection_consumer_cursors_old;
	`
}
