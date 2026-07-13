package sqlitemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
)

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

func EnsureLedgerChecksums(ctx context.Context, db LedgerDB, artifacts []Artifact) error {
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
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close migration ledger inspection: %w", err)
	}
	if !hasColumn {
		if _, err := db.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN artifact_checksum TEXT`); err != nil {
			return fmt.Errorf("add migration artifact checksum ledger: %w", err)
		}
	}
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
			if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET artifact_checksum=? WHERE id=? AND (artifact_checksum IS NULL OR trim(artifact_checksum)='')`, artifact.Checksum, artifact.ID); err != nil {
				return fmt.Errorf("backfill migration %s artifact checksum: %w", artifact.ID, err)
			}
			continue
		}
		if recorded.String != artifact.Checksum {
			return fmt.Errorf("migration %s historical artifact mutated: ledger has %s, binary has %s", artifact.ID, recorded.String, artifact.Checksum)
		}
	}
	return nil
}
