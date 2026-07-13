package sqlitemigration

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func TestValidateRejectsInvalidRegistries(t *testing.T) {
	files := fstest.MapFS{"one.sql": {Data: []byte("SELECT 1;")}}
	valid := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
	for name, artifacts := range map[string][]Artifact{
		"duplicate":     {valid, valid},
		"missing":       {{ID: "one", Path: "missing.sql", Checksum: valid.Checksum}},
		"callback-only": {{ID: "one"}},
		"mutated":       {{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 2;"))}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(files, artifacts); err == nil {
				t.Fatal("Validate unexpectedly succeeded")
			}
		})
	}
}

func TestValidateRegistrationsRejectsMissingAndDuplicateCoverage(t *testing.T) {
	catalog := []Artifact{{ID: "one", Path: "one.sql"}}
	for name, registrations := range map[string][]Artifact{
		"missing":       nil,
		"callback-only": {{ID: "one"}},
		"duplicate":     {{ID: "one", Path: "one.sql"}, {ID: "one", Path: "one.sql"}},
		"wrong path":    {{ID: "one", Path: "other.sql"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRegistrations(catalog, registrations); err == nil {
				t.Fatal("invalid registration accepted")
			}
		})
	}
}

func TestEnsureLedgerChecksumsBackfillsAndRejectsMutation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES('one','now')`); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
	if err := EnsureLedgerChecksums(context.Background(), db, []Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	artifact.Checksum = Sum([]byte("SELECT 2;"))
	if err := EnsureLedgerChecksums(context.Background(), db, []Artifact{artifact}); err == nil {
		t.Fatal("mutated artifact accepted")
	}
}

func TestEnsureLedgerChecksumsRejectsBlankChecksumInChecksumAwareLedger(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT); INSERT INTO schema_migrations(id,applied_at) VALUES('one','now')`); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
	if err := EnsureLedgerChecksums(context.Background(), db, []Artifact{artifact}); err == nil {
		t.Fatal("blank checksum in checksum-aware ledger was backfilled")
	}
}

func TestEnsureLedgerChecksumsAtomicRetriesInterruptedLegacyBackfill(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations VALUES('one','now'),('two','now');
	`); err != nil {
		t.Fatal(err)
	}
	artifacts := []Artifact{
		{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))},
		{ID: "two", Path: "two.sql", Checksum: Sum([]byte("SELECT 2;"))},
	}
	err = ensureLedgerChecksumsAtomic(context.Background(), db, artifacts, func(db LedgerDB) error {
		_, err := db.ExecContext(context.Background(), `
			CREATE TRIGGER fail_second_checksum_backfill
			BEFORE UPDATE OF artifact_checksum ON schema_migrations
			WHEN OLD.id='two'
			BEGIN
				SELECT RAISE(ABORT, 'injected second backfill update failure');
			END;
		`)
		return err
	})
	if err == nil {
		t.Fatal("interrupted legacy checksum conversion unexpectedly succeeded")
	}
	if hasColumn(t, db, "schema_migrations", "artifact_checksum") {
		t.Fatal("failed legacy conversion left artifact_checksum column behind")
	}

	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, artifacts); err != nil {
		t.Fatalf("retry legacy checksum conversion: %v", err)
	}
	for _, artifact := range artifacts {
		var checksum string
		if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, artifact.ID).Scan(&checksum); err != nil {
			t.Fatal(err)
		}
		if checksum != artifact.Checksum {
			t.Fatalf("migration %s checksum = %q, want %q", artifact.ID, checksum, artifact.Checksum)
		}
	}
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}

func TestRecordAppliedRequiresAndPersistsPinnedChecksum(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT)`); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
	if err := RecordApplied(context.Background(), db, []Artifact{artifact}, "missing", "now"); err == nil {
		t.Fatal("migration without pinned artifact checksum was recorded")
	}
	if err := RecordApplied(context.Background(), db, []Artifact{artifact}, "one", "now"); err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id='one'`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != artifact.Checksum {
		t.Fatalf("checksum = %q, want %q", checksum, artifact.Checksum)
	}
}
