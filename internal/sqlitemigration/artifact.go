package sqlitemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
)

type Authority string

const (
	checksumConversionV1FormatVersion = 1
	checksumConversionV1ManifestPath  = "artifacts/checksum_conversion_v1.manifest.sql"
	checksumConversionV1ManifestSum   = "f8782feba7752ee50fc1bc473629fc731934a3c78e7ef7530547ae3a2a9d654c"
	checksumConversionFormatVersion   = 2
	checksumConversionManifestPath    = "artifacts/checksum_conversion_v2.manifest.sql"
	checksumConversionManifestSum     = "790e0c3b2aa66ed787b10084937cd87ac12c4b1751d10e2fc3a8274b95d47b86"
)

//go:embed artifacts/checksum_conversion_v1.manifest.sql artifacts/checksum_conversion_v2.manifest.sql
var checksumConversionFiles embed.FS

var checksumConversionArtifacts = []Artifact{
	{
		ID:       "migration_checksum_conversion_v1",
		Path:     checksumConversionV1ManifestPath,
		Checksum: checksumConversionV1ManifestSum,
	},
	{
		ID:       "migration_checksum_conversion_v2",
		Path:     checksumConversionManifestPath,
		Checksum: checksumConversionManifestSum,
	},
}

// Artifact binds a durable migration ledger ID to one embedded, immutable file.
// Checksum is deliberately pinned outside the artifact so changing an historical
// file cannot silently redefine its identity.
type Artifact struct {
	ID       string
	Path     string
	Checksum string
}

func Validate(files fs.FS, artifacts []Artifact) error {
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.ID) == "" {
			return fmt.Errorf("migration has empty id")
		}
		if _, ok := seen[artifact.ID]; ok {
			return fmt.Errorf("duplicate migration id %q", artifact.ID)
		}
		seen[artifact.ID] = struct{}{}
		if strings.TrimSpace(artifact.Path) == "" {
			return fmt.Errorf("migration %s has no artifact", artifact.ID)
		}
		content, err := fs.ReadFile(files, artifact.Path)
		if err != nil {
			return fmt.Errorf("read migration %s artifact %q: %w", artifact.ID, artifact.Path, err)
		}
		if len(strings.TrimSpace(string(content))) == 0 {
			return fmt.Errorf("migration %s artifact %q is empty", artifact.ID, artifact.Path)
		}
		actual := Sum(content)
		if actual != artifact.Checksum {
			return fmt.Errorf("migration %s artifact checksum mismatch: got %s want %s", artifact.ID, actual, artifact.Checksum)
		}
	}
	return nil
}

// ValidateRegistrations proves the executable registry and pinned artifact
// catalog are a one-to-one mapping. Registration values only need ID and Path.
func ValidateRegistrations(artifacts, registrations []Artifact) error {
	catalog := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		catalog[artifact.ID] = artifact.Path
	}
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if _, exists := seen[registration.ID]; exists {
			return fmt.Errorf("duplicate migration id %q", registration.ID)
		}
		seen[registration.ID] = struct{}{}
		if strings.TrimSpace(registration.Path) == "" {
			return fmt.Errorf("migration %s has no artifact", registration.ID)
		}
		if path, ok := catalog[registration.ID]; !ok || path != registration.Path {
			return fmt.Errorf("migration %s artifact registry mismatch", registration.ID)
		}
	}
	if len(seen) != len(catalog) {
		return fmt.Errorf("migration registry has %d entries but artifact catalog has %d", len(seen), len(catalog))
	}
	return nil
}

func Sum(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// EnsureLedgerChecksums upgrades legacy ledgers in place, backfills checksums for
// known applied IDs, and rejects an artifact that differs from the value recorded
// on an earlier open. Unknown IDs are preserved for forward compatibility.
type LedgerDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type LedgerWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// RecordApplied writes a new migration's complete ledger identity atomically.
// Callers running a transactional migration must pass that transaction so the
// schema/data changes and checksum-bearing marker commit together.
func RecordApplied(ctx context.Context, db LedgerWriter, artifacts []Artifact, id, appliedAt string) error {
	return recordApplied(ctx, db, artifacts, id, appliedAt, false)
}

// RecordAppliedIfMissing is the idempotent form used by migration authorities
// whose schema ensure and migration execution share one transaction.
func RecordAppliedIfMissing(ctx context.Context, db LedgerWriter, artifacts []Artifact, id, appliedAt string) error {
	return recordApplied(ctx, db, artifacts, id, appliedAt, true)
}

func recordApplied(ctx context.Context, db LedgerWriter, artifacts []Artifact, id, appliedAt string, ifMissing bool) error {
	var checksum string
	for _, artifact := range artifacts {
		if artifact.ID == id {
			checksum = artifact.Checksum
			break
		}
	}
	if strings.TrimSpace(checksum) == "" {
		return fmt.Errorf("migration %s has no pinned artifact checksum", id)
	}
	insert := "INSERT INTO schema_migrations (id, applied_at, artifact_checksum) VALUES (?, ?, ?)"
	if ifMissing {
		insert = "INSERT OR IGNORE INTO schema_migrations (id, applied_at, artifact_checksum) VALUES (?, ?, ?)"
	}
	if _, err := db.ExecContext(ctx, insert, id, appliedAt, checksum); err != nil {
		return fmt.Errorf("record migration %s with artifact checksum: %w", id, err)
	}
	return nil
}

func EnsureLedgerChecksumsInTransaction(ctx context.Context, tx *sql.Tx, authority Authority, artifacts []Artifact) error {
	if tx == nil {
		return fmt.Errorf("migration artifact checksum transaction is nil")
	}
	return ensureLedgerChecksums(ctx, tx, authority, artifacts, conversionHooks{})
}

// EnsureLedgerChecksumsAtomic upgrades one authority in a shared legacy ledger
// in an immediate transaction. Callers already inside a migration transaction
// should use EnsureLedgerChecksumsInTransaction.
func EnsureLedgerChecksumsAtomic(ctx context.Context, db *sql.DB, authority Authority, artifacts []Artifact) error {
	return ensureLedgerChecksumsAtomic(ctx, db, authority, artifacts, conversionHooks{})
}

type conversionHooks struct {
	afterLedgerPrepared     func(LedgerDB) error
	afterArtifactBackfilled func(LedgerDB, Artifact) error
	beforeAuthorityMarked   func(LedgerDB) error
}

func ensureLedgerChecksumsAtomic(ctx context.Context, db *sql.DB, authority Authority, artifacts []Artifact, hooks conversionHooks) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration artifact checksum conversion connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin migration artifact checksum conversion: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if err := ensureLedgerChecksums(ctx, conn, authority, artifacts, hooks); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit migration artifact checksum conversion: %w", err)
	}
	committed = true
	return nil
}

func ensureLedgerChecksums(ctx context.Context, db LedgerDB, authority Authority, artifacts []Artifact, hooks conversionHooks) error {
	authorityID := strings.TrimSpace(string(authority))
	if authorityID == "" {
		return fmt.Errorf("migration artifact checksum authority is empty")
	}
	if err := Validate(checksumConversionFiles, checksumConversionArtifacts); err != nil {
		return fmt.Errorf("validate migration artifact checksum conversion manifest: %w", err)
	}
	conversionSQL, err := fs.ReadFile(checksumConversionFiles, checksumConversionManifestPath)
	if err != nil {
		return fmt.Errorf("read migration artifact checksum conversion manifest: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(conversionSQL)); err != nil {
		return fmt.Errorf("ensure migration artifact checksum authority ledger: %w", err)
	}
	if err := validateChecksumConversionTable(ctx, db); err != nil {
		return err
	}
	markerRows, err := db.QueryContext(ctx, `
		SELECT format_version, artifact_checksum
		FROM schema_migration_checksum_conversions
		WHERE authority_id = ?
		ORDER BY format_version
	`, authorityID)
	if err != nil {
		return fmt.Errorf("inspect migration artifact checksum authority %s: %w", authorityID, err)
	}
	authorityConverted := false
	hasPriorConversion := false
	for markerRows.Next() {
		hasPriorConversion = true
		var version int
		var recorded sql.NullString
		if err := markerRows.Scan(&version, &recorded); err != nil {
			markerRows.Close()
			return fmt.Errorf("scan migration artifact checksum authority %s: %w", authorityID, err)
		}
		expected, known := checksumConversionChecksum(version)
		if !known {
			markerRows.Close()
			return fmt.Errorf("migration artifact checksum authority %s has unsupported conversion format %d", authorityID, version)
		}
		if !recorded.Valid || strings.TrimSpace(recorded.String) == "" {
			markerRows.Close()
			return fmt.Errorf("migration artifact checksum authority %s format %d has empty conversion artifact checksum", authorityID, version)
		}
		if recorded.String != expected {
			markerRows.Close()
			return fmt.Errorf("migration artifact checksum authority %s format %d artifact mutated: ledger has %s, binary has %s", authorityID, version, recorded.String, expected)
		}
		authorityConverted = authorityConverted || version == checksumConversionFormatVersion
	}
	if err := markerRows.Err(); err != nil {
		markerRows.Close()
		return fmt.Errorf("iterate migration artifact checksum authority %s: %w", authorityID, err)
	}
	if err := markerRows.Close(); err != nil {
		return fmt.Errorf("close migration artifact checksum authority %s inspection: %w", authorityID, err)
	}
	var hasColumn bool
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(schema_migrations)`)
	if err != nil {
		return fmt.Errorf("inspect migration ledger: %w", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration ledger: %w", err)
		}
		hasColumn = hasColumn || name == "artifact_checksum"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate migration ledger inspection: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close migration ledger inspection: %w", err)
	}
	if !hasColumn {
		if _, err := db.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN artifact_checksum TEXT`); err != nil {
			return fmt.Errorf("add migration artifact checksum ledger: %w", err)
		}
	}
	if hooks.afterLedgerPrepared != nil {
		if err := hooks.afterLedgerPrepared(db); err != nil {
			return fmt.Errorf("prepare legacy migration artifact checksum backfill: %w", err)
		}
	}
	allowLegacyBackfill := !hasPriorConversion
	for _, artifact := range artifacts {
		var recorded sql.NullString
		err := db.QueryRowContext(ctx, `SELECT artifact_checksum FROM schema_migrations WHERE id=?`, artifact.ID).Scan(&recorded)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("read migration %s artifact checksum: %w", artifact.ID, err)
		}
		if !recorded.Valid || strings.TrimSpace(recorded.String) == "" {
			if !allowLegacyBackfill {
				return fmt.Errorf("migration %s has empty artifact checksum in checksum-aware ledger", artifact.ID)
			}
			result, err := db.ExecContext(ctx, `UPDATE schema_migrations SET artifact_checksum=? WHERE id=? AND (artifact_checksum IS NULL OR trim(artifact_checksum)='')`, artifact.Checksum, artifact.ID)
			if err != nil {
				return fmt.Errorf("backfill migration %s artifact checksum: %w", artifact.ID, err)
			}
			updated, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("count migration %s artifact checksum backfill: %w", artifact.ID, err)
			}
			if updated != 1 {
				return fmt.Errorf("backfill migration %s artifact checksum changed %d rows, want 1", artifact.ID, updated)
			}
			if hooks.afterArtifactBackfilled != nil {
				if err := hooks.afterArtifactBackfilled(db, artifact); err != nil {
					return fmt.Errorf("verify legacy migration %s artifact checksum backfill: %w", artifact.ID, err)
				}
			}
			continue
		}
		if recorded.String != artifact.Checksum {
			return fmt.Errorf("migration %s historical artifact mutated: ledger has %s, binary has %s", artifact.ID, recorded.String, artifact.Checksum)
		}
	}
	for _, artifact := range artifacts {
		var recorded sql.NullString
		err := db.QueryRowContext(ctx, `SELECT artifact_checksum FROM schema_migrations WHERE id=?`, artifact.ID).Scan(&recorded)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("verify migration %s artifact checksum: %w", artifact.ID, err)
		}
		if !recorded.Valid || recorded.String != artifact.Checksum {
			return fmt.Errorf("verify migration %s artifact checksum: got %q want %q", artifact.ID, recorded.String, artifact.Checksum)
		}
	}
	if !authorityConverted {
		if hooks.beforeAuthorityMarked != nil {
			if err := hooks.beforeAuthorityMarked(db); err != nil {
				return fmt.Errorf("prepare migration artifact checksum authority %s marker: %w", authorityID, err)
			}
		}
		result, err := db.ExecContext(ctx, `
			INSERT INTO schema_migration_checksum_conversions(
				authority_id, format_version, artifact_checksum, completed_at
			) VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		`, authorityID, checksumConversionFormatVersion, checksumConversionManifestSum)
		if err != nil {
			return fmt.Errorf("record migration artifact checksum authority %s: %w", authorityID, err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count migration artifact checksum authority %s marker: %w", authorityID, err)
		}
		if inserted != 1 {
			return fmt.Errorf("record migration artifact checksum authority %s changed %d rows, want 1", authorityID, inserted)
		}
		var insertedChecksum string
		if err := db.QueryRowContext(ctx, `
			SELECT artifact_checksum
			FROM schema_migration_checksum_conversions
			WHERE authority_id = ? AND format_version = ?
		`, authorityID, checksumConversionFormatVersion).Scan(&insertedChecksum); err != nil {
			return fmt.Errorf("verify migration artifact checksum authority %s marker: %w", authorityID, err)
		}
		if insertedChecksum != checksumConversionManifestSum {
			return fmt.Errorf("verify migration artifact checksum authority %s marker: got %q want %q", authorityID, insertedChecksum, checksumConversionManifestSum)
		}
	}
	return nil
}

func checksumConversionChecksum(version int) (string, bool) {
	switch version {
	case checksumConversionV1FormatVersion:
		return checksumConversionV1ManifestSum, true
	case checksumConversionFormatVersion:
		return checksumConversionManifestSum, true
	default:
		return "", false
	}
}

func validateChecksumConversionTable(ctx context.Context, db LedgerDB) error {
	type column struct {
		name    string
		typ     string
		notNull int
		pk      int
	}
	want := []column{
		{name: "authority_id", typ: "TEXT", notNull: 1, pk: 1},
		{name: "format_version", typ: "INTEGER", notNull: 1, pk: 2},
		{name: "artifact_checksum", typ: "TEXT", notNull: 1},
		{name: "completed_at", typ: "TEXT", notNull: 1},
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(schema_migration_checksum_conversions)`)
	if err != nil {
		return fmt.Errorf("inspect migration artifact checksum authority ledger: %w", err)
	}
	var got []column
	for rows.Next() {
		var cid int
		var current column
		var defaultValue any
		if err := rows.Scan(&cid, &current.name, &current.typ, &current.notNull, &defaultValue, &current.pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration artifact checksum authority ledger: %w", err)
		}
		got = append(got, current)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate migration artifact checksum authority ledger: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close migration artifact checksum authority ledger inspection: %w", err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("migration artifact checksum authority ledger has %d columns, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			return fmt.Errorf("migration artifact checksum authority ledger column %d = %+v, want %+v", index, got[index], want[index])
		}
	}
	var ddl string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='schema_migration_checksum_conversions'`).Scan(&ddl); err != nil {
		return fmt.Errorf("read migration artifact checksum authority ledger definition: %w", err)
	}
	normalizedDDL := strings.ToLower(strings.Join(strings.Fields(ddl), " "))
	for _, constraint := range []string{
		"check(trim(authority_id) <> '')",
		"check(format_version > 0)",
		"check(trim(artifact_checksum) <> '')",
	} {
		if !strings.Contains(normalizedDDL, constraint) {
			return fmt.Errorf("migration artifact checksum authority ledger missing constraint %q", constraint)
		}
	}
	var triggerCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type='trigger' AND tbl_name='schema_migration_checksum_conversions'
	`).Scan(&triggerCount); err != nil {
		return fmt.Errorf("inspect migration artifact checksum authority ledger triggers: %w", err)
	}
	if triggerCount != 0 {
		return fmt.Errorf("migration artifact checksum authority ledger has %d unexpected triggers", triggerCount)
	}
	return nil
}
