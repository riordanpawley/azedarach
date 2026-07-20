package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/sqlitemigration"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	id   string
	path string
}

var orderedMigrations = []migration{
	{id: "daemon_operations_0001", path: "migrations/0001_daemon_operations.sql"},
	{id: "daemon_operations_0002_progress", path: "migrations/0002_daemon_operation_progress.sql"},
	{id: "daemon_operations_0003_validation_leases", path: "migrations/0003_validation_leases.sql"},
	{id: "daemon_operations_0004_review_validation_assignment", path: "migrations/0004_review_validation_assignment.sql"},
	{id: "daemon_operations_0005_validation_scope_purpose", path: "migrations/0005_validation_scope_purpose.sql"},
	{id: "daemon_operations_0006_publication_validation_priority", path: "migrations/0006_publication_validation_priority.sql"},
	{id: "daemon_operations_0007_layered_publication_evidence", path: "migrations/0007_layered_publication_evidence.sql"},
	{id: "daemon_operations_0008_validation_priority_fairness", path: "migrations/0008_validation_priority_fairness.sql"},
	{id: "daemon_operations_0009_publication_review_authority", path: "migrations/0009_publication_review_authority.sql"},
	{id: "daemon_operations_0010_publication_evidence_authority", path: "migrations/0010_publication_evidence_authority.sql"},
}

var migrationArtifacts = []sqlitemigration.Artifact{
	{ID: "daemon_operations_0001", Path: "migrations/0001_daemon_operations.sql", Checksum: "573eaaeacb02741d90c12f4d0c747120dd7422dad46d65b0ca2e10fa77408321"},
	{ID: "daemon_operations_0002_progress", Path: "migrations/0002_daemon_operation_progress.sql", Checksum: "57fde6bd6348ea3925a7c027facbf23185bff81ebe659dfb6f5de1f2bb0c009a"},
	{ID: "daemon_operations_0003_validation_leases", Path: "migrations/0003_validation_leases.sql", Checksum: "317c9ea680d378637b417005ccfcf0d989e4c025cbbacabdd581f00dafac19df"},
	{ID: "daemon_operations_0004_review_validation_assignment", Path: "migrations/0004_review_validation_assignment.sql", Checksum: "6f5d54a3f27937ae9adcdd6a0b3f9b79ddd2814f32635eb2c3e5ca051c3268ca"},
	{ID: "daemon_operations_0005_validation_scope_purpose", Path: "migrations/0005_validation_scope_purpose.sql", Checksum: "6cb59febaf88ccc7948f5289cbdc040bfa041fbd639ea88eb77766dfff15a192"},
	{ID: "daemon_operations_0006_publication_validation_priority", Path: "migrations/0006_publication_validation_priority.sql", Checksum: "bbbf9fd51c2d9289a295a6aeb7427d65d04d3d3a897cc995d2d91ea4577713fd"},
	{ID: "daemon_operations_0007_layered_publication_evidence", Path: "migrations/0007_layered_publication_evidence.sql", Checksum: "59182365b3d9dd89464e1fdb2f0e5818d6d91bbcf0625bcfd4c3898f888a10ef"},
	{ID: "daemon_operations_0008_validation_priority_fairness", Path: "migrations/0008_validation_priority_fairness.sql", Checksum: "9f6a4ae4af768b433880a310b1d4c5bb79453224c1c93bd9c7b7696d4cf476bf"},
	{ID: "daemon_operations_0009_publication_review_authority", Path: "migrations/0009_publication_review_authority.sql", Checksum: "645af760b30317d26d72ddcccf7a3a5934c009923c8f77260a503e48a39565b2"},
	{ID: "daemon_operations_0010_publication_evidence_authority", Path: "migrations/0010_publication_evidence_authority.sql", Checksum: "69054f7d354374035f9012209b707a9b9ab629254f53f0fc12f0afa3eb5a7ff0"},
}

const migrationArtifactAuthority sqlitemigration.Authority = "project.daemon_operations"

func runMigrations(ctx context.Context, db *sql.DB) error {
	if err := sqlitemigration.Validate(migrationFiles, migrationArtifacts); err != nil {
		return fmt.Errorf("validate migration registry: %w", err)
	}
	registrations := make([]sqlitemigration.Artifact, 0, len(orderedMigrations))
	for _, migration := range orderedMigrations {
		registrations = append(registrations, sqlitemigration.Artifact{ID: migration.id, Path: migration.path})
	}
	if err := sqlitemigration.ValidateRegistrations(migrationArtifacts, registrations); err != nil {
		return fmt.Errorf("validate migration artifact coverage: %w", err)
	}
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}
	if err := sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, migrationArtifacts); err != nil {
		return err
	}
	for _, migration := range orderedMigrations {
		applied, err := isMigrationApplied(ctx, db, migration.id)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", migration.id, err)
		}
		if applied {
			continue
		}
		sqlText, err := loadMigrationSQL(migration.path)
		if err != nil {
			return fmt.Errorf("load migration %s: %w", migration.id, err)
		}
		if err := applyMigration(ctx, db, migration.id, sqlText); err != nil {
			return err
		}
	}
	if err := sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, migrationArtifacts); err != nil {
		return err
	}
	if err := validateValidationLeaseSchema(ctx, db); err != nil {
		return err
	}
	if err := validatePublicationEvidenceSchema(ctx, db); err != nil {
		return err
	}
	return validatePublicationQueueSchema(ctx, db)
}

func validatePublicationQueueSchema(ctx context.Context, db *sql.DB) error {
	artifact, err := loadMigrationSQL("migrations/0007_layered_publication_evidence.sql")
	if err != nil {
		return fmt.Errorf("load canonical publication queue schema: %w", err)
	}
	canonicalDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("open canonical publication queue schema: %w", err)
	}
	defer canonicalDB.Close()
	canonicalDB.SetMaxOpenConns(1)
	if _, err = canonicalDB.ExecContext(ctx, artifact); err != nil {
		return fmt.Errorf("build canonical publication queue schema: %w", err)
	}
	// Executed 0009 also expanded validation authority. The publication-only
	// canonical builder needs that sibling table shape solely so it can replay
	// the immutable artifact before applying the forward 0010 rename.
	if _, err = canonicalDB.ExecContext(ctx, `CREATE TABLE daemon_validation_requests(canonical_seed TEXT)`); err != nil {
		return fmt.Errorf("seed canonical validation authority schema: %w", err)
	}
	upgrade, err := loadMigrationSQL("migrations/0009_publication_review_authority.sql")
	if err != nil {
		return fmt.Errorf("load canonical publication review authority schema: %w", err)
	}
	if _, err = canonicalDB.ExecContext(ctx, upgrade); err != nil {
		return fmt.Errorf("build canonical publication review authority schema: %w", err)
	}
	evidenceAuthority, err := loadMigrationSQL("migrations/0010_publication_evidence_authority.sql")
	if err != nil {
		return fmt.Errorf("load canonical publication evidence authority schema: %w", err)
	}
	if _, err = canonicalDB.ExecContext(ctx, evidenceAuthority); err != nil {
		return fmt.Errorf("build canonical publication evidence authority schema: %w", err)
	}
	for _, object := range []struct{ typeName, name string }{
		{"table", "daemon_publication_operations"},
		{"index", "idx_daemon_publication_operations_queue"},
		{"index", "idx_daemon_publication_operations_issue"},
		{"index", "idx_daemon_publication_operations_claim"},
		{"trigger", "daemon_publication_operation_identity_immutable"},
	} {
		var expected, actual string
		if err = canonicalDB.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.typeName, object.name).Scan(&expected); err != nil {
			return fmt.Errorf("read canonical publication queue %s %s: %w", object.typeName, object.name, err)
		}
		if err = db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.typeName, object.name).Scan(&actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("publication queue schema drift: missing %s %s", object.typeName, object.name)
			}
			return fmt.Errorf("validate publication queue %s %s: %w", object.typeName, object.name, err)
		}
		if normalizeSQLiteDefinition(actual) != normalizeSQLiteDefinition(expected) {
			return fmt.Errorf("publication queue schema drift: %s %s differs from immutable artifact", object.typeName, object.name)
		}
	}
	return nil
}

func validatePublicationEvidenceSchema(ctx context.Context, db *sql.DB) error {
	artifact, err := loadMigrationSQL("migrations/0007_layered_publication_evidence.sql")
	if err != nil {
		return fmt.Errorf("load canonical publication evidence schema: %w", err)
	}
	canonicalDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("open canonical publication evidence schema: %w", err)
	}
	defer canonicalDB.Close()
	canonicalDB.SetMaxOpenConns(1)
	if _, err = canonicalDB.ExecContext(ctx, artifact); err != nil {
		return fmt.Errorf("build canonical publication evidence schema: %w", err)
	}
	for _, object := range []struct{ typeName, name string }{
		{"table", "daemon_publication_evidence"}, {"table", "daemon_publication_evidence_invalidations"}, {"table", "daemon_publication_evidence_state"},
		{"index", "idx_daemon_publication_evidence_issue_layer"}, {"index", "idx_daemon_publication_evidence_invalidations"},
		{"trigger", "daemon_publication_evidence_immutable_update"}, {"trigger", "daemon_publication_evidence_immutable_delete"},
		{"trigger", "daemon_publication_invalidation_immutable_update"}, {"trigger", "daemon_publication_invalidation_immutable_delete"},
		{"trigger", "daemon_publication_evidence_insert_revision"}, {"trigger", "daemon_publication_invalidation_insert_revision"},
	} {
		var expected, actual string
		if err = canonicalDB.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.typeName, object.name).Scan(&expected); err != nil {
			return fmt.Errorf("read canonical publication evidence %s %s: %w", object.typeName, object.name, err)
		}
		if err = db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.typeName, object.name).Scan(&actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("publication evidence schema drift: missing %s %s", object.typeName, object.name)
			}
			return fmt.Errorf("validate publication evidence %s %s: %w", object.typeName, object.name, err)
		}
		if normalizeSQLiteDefinition(actual) != normalizeSQLiteDefinition(expected) {
			return fmt.Errorf("publication evidence schema drift: %s %s differs from immutable artifact", object.typeName, object.name)
		}
	}
	return nil
}

func normalizeSQLiteDefinition(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func validateValidationLeaseSchema(ctx context.Context, db *sql.DB) error {
	required := []struct {
		typeName  string
		name      string
		fragments []string
	}{
		{typeName: "table", name: "daemon_validation_requests", fragments: []string{
			"create table daemon_validation_requests",
			"sequence integer primary key autoincrement",
			"request_id text not null unique",
			"lease_token_hash text not null check (length(lease_token_hash) = 64)",
			"project_id text not null",
			"issue_id text not null",
			"issue_priority integer not null default 2 check (issue_priority between 0 and 4)",
			"priority_bypass_count integer not null default 0 check (priority_bypass_count >= 0)",
			"class text not null check (class in ('aggregate','shared','safe'))",
			"profile text not null",
			"command text not null",
			"source_revision text not null",
			"reviewer_id text not null default ''",
			"review_epoch_event_id integer not null default 0 check (review_epoch_event_id >= 0)",
			"scope text not null default 'ticket' check (scope in ('repository','ticket'))",
			"purpose text not null default 'legacy' check (purpose in ('legacy','capacity','development','push_gate','review_evidence'))",
			"execution text not null default 'executed' check (execution in ('executed','joined','reused','skipped'))",
			"authoritative_request_id text not null default ''",
			"compatibility_key text not null default ''",
			"isolation_mode text not null default 'legacy'",
			"environment_fingerprint text not null default 'legacy'",
			"override_kind text not null default 'none' check (override_kind in ('none','no_reuse','force_rerun','emergency_skip'))",
			"override_actor text not null default ''",
			"override_reason text not null default ''",
			"state text not null check (state in ('queued','active','completed','cancelled','expired','failed'))",
			"queued_at text not null",
			"started_at text",
			"heartbeat_at text",
			"expires_at text",
			"finished_at text",
			"outcome text not null default ''",
			"evidence_json text not null default '{}'",
		}},
		{typeName: "table", name: "daemon_validation_state", fragments: []string{
			"create table daemon_validation_state",
			"project_id text primary key",
			"revision integer not null check (revision > 0)",
		}},
		{typeName: "index", name: "idx_daemon_validation_one_active_aggregate", fragments: []string{
			"create unique index idx_daemon_validation_one_active_aggregate",
			"on daemon_validation_requests(project_id)",
			"where state = 'active' and class = 'aggregate' and purpose = 'capacity'",
		}},
		{typeName: "index", name: "idx_daemon_validation_project_queue", fragments: []string{
			"create index idx_daemon_validation_project_queue",
			"on daemon_validation_requests(project_id, state, sequence)",
		}},
		{typeName: "index", name: "idx_daemon_validation_expiry", fragments: []string{
			"create index idx_daemon_validation_expiry",
			"on daemon_validation_requests(state, expires_at)",
		}},
		{typeName: "index", name: "idx_daemon_validation_review_evidence", fragments: []string{
			"create index idx_daemon_validation_review_evidence",
			"on daemon_validation_requests(project_id, issue_id, source_revision, sequence)",
			"where scope = 'ticket' and purpose = 'review_evidence' and class = 'aggregate'",
		}},
		{typeName: "index", name: "idx_daemon_validation_compatibility", fragments: []string{
			"create index idx_daemon_validation_compatibility",
			"on daemon_validation_requests(project_id, compatibility_key, state, sequence)",
		}},
		{typeName: "index", name: "idx_daemon_validation_priority_queue", fragments: []string{
			"create index idx_daemon_validation_priority_queue",
			"on daemon_validation_requests( project_id, state, purpose, priority_bypass_count, issue_priority, sequence )",
		}},
		{typeName: "trigger", name: "daemon_validation_requests_insert_revision", fragments: []string{
			"create trigger daemon_validation_requests_insert_revision",
			"after insert on daemon_validation_requests",
			"insert into daemon_validation_state(project_id, revision)",
			"values(new.project_id, 1)",
			"on conflict(project_id) do update set revision = revision + 1",
		}},
		{typeName: "trigger", name: "daemon_validation_requests_update_revision", fragments: []string{
			"create trigger daemon_validation_requests_update_revision",
			"after update on daemon_validation_requests",
			"insert into daemon_validation_state(project_id, revision)",
			"values(new.project_id, 1)",
			"on conflict(project_id) do update set revision = revision + 1",
		}},
	}
	for _, object := range required {
		var definition string
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.typeName, object.name).Scan(&definition); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("validation lease schema drift: missing %s %s", object.typeName, object.name)
			}
			return fmt.Errorf("validate validation lease %s %s: %w", object.typeName, object.name, err)
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(definition), " "))
		for _, fragment := range object.fragments {
			if !strings.Contains(normalized, fragment) {
				return fmt.Errorf("validation lease schema drift: %s %s is missing %q", object.typeName, object.name, fragment)
			}
		}
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            id TEXT PRIMARY KEY,
            applied_at TEXT NOT NULL,
            artifact_checksum TEXT
        )
    `)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	return nil
}

func isMigrationApplied(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM schema_migrations WHERE id = ?
        )
    `, id).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func loadMigrationSQL(path string) (string, error) {
	content, err := fs.ReadFile(migrationFiles, path)
	if err != nil {
		return "", err
	}
	sqlText := strings.TrimSpace(string(content))
	if sqlText == "" {
		return "", fmt.Errorf("empty migration sql")
	}
	return sqlText, nil
}

func applyMigration(ctx context.Context, db *sql.DB, id, sqlText string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if err := sqlitemigration.RecordApplied(ctx, tx, migrationArtifacts, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}
