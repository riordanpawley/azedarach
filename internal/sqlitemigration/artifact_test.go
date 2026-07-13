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
