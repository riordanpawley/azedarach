package sqlitemigration

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
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
	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	artifact.Checksum = Sum([]byte("SELECT 2;"))
	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err == nil {
		t.Fatal("mutated artifact accepted")
	}
}

func TestEnsureLedgerChecksumsRejectsEmptyAuthorityBeforeMutation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, " ", nil); err == nil {
		t.Fatal("empty authority accepted")
	}
	if hasTable(t, db, "schema_migration_checksum_conversions") || hasColumn(t, db, "schema_migrations", "artifact_checksum") {
		t.Fatal("empty authority mutated the ledger")
	}
}

func TestEnsureLedgerChecksumsRejectsBlankChecksumInChecksumAwareLedger(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations(id,applied_at) VALUES('one','now')`); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_migrations SET artifact_checksum=NULL WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err == nil {
		t.Fatal("blank checksum in checksum-aware ledger was backfilled")
	}
}

func TestEnsureLedgerChecksumsConvertsSharedLedgerPerAuthority(t *testing.T) {
	authorities := []struct {
		id       Authority
		artifact Artifact
	}{
		{"project.issues", Artifact{ID: "issue_one", Path: "issue.sql", Checksum: Sum([]byte("SELECT 'issue';"))}},
		{"project.daemon_operations", Artifact{ID: "operation_one", Path: "operation.sql", Checksum: Sum([]byte("SELECT 'operation';"))}},
		{"project.daemon_notices", Artifact{ID: "notice_one", Path: "notice.sql", Checksum: Sum([]byte("SELECT 'notice';"))}},
	}
	for _, order := range permutations([]int{0, 1, 2}) {
		name := fmt.Sprintf("%d-%d-%d", order[0], order[1], order[2])
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`
				CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT);
				INSERT INTO schema_migrations(id,applied_at) VALUES('issue_one','now'),('operation_one','now'),('notice_one','now');
			`); err != nil {
				t.Fatal(err)
			}
			for position, index := range order {
				authority := authorities[index]
				if err := EnsureLedgerChecksumsAtomic(context.Background(), db, authority.id, []Artifact{authority.artifact}); err != nil {
					t.Fatalf("convert position %d authority %s: %v", position, authority.id, err)
				}
			}
			for _, authority := range authorities {
				var got string
				if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, authority.artifact.ID).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != authority.artifact.Checksum {
					t.Fatalf("%s checksum = %q, want %q", authority.id, got, authority.artifact.Checksum)
				}
			}
			var markerCount int
			if err := db.QueryRow(`SELECT count(*) FROM schema_migration_checksum_conversions`).Scan(&markerCount); err != nil {
				t.Fatal(err)
			}
			if markerCount != len(authorities) {
				t.Fatalf("authority marker count = %d, want %d", markerCount, len(authorities))
			}
		})
	}
}

func permutations(values []int) [][]int {
	if len(values) == 1 {
		return [][]int{{values[0]}}
	}
	var result [][]int
	for index, value := range values {
		rest := append(append([]int(nil), values[:index]...), values[index+1:]...)
		for _, suffix := range permutations(rest) {
			result = append(result, append([]int{value}, suffix...))
		}
	}
	return result
}

func TestEnsureLedgerChecksumsAtomicRollsBackExistingColumnConversion(t *testing.T) {
	for _, failurePoint := range []string{"after-first-backfill", "before-marker"} {
		t.Run(failurePoint, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`
				CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT);
				INSERT INTO schema_migrations(id,applied_at) VALUES('one','now'),('two','now');
			`); err != nil {
				t.Fatal(err)
			}
			artifacts := []Artifact{
				{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))},
				{ID: "two", Path: "two.sql", Checksum: Sum([]byte("SELECT 2;"))},
			}
			hooks := conversionHooks{}
			if failurePoint == "after-first-backfill" {
				hooks.afterArtifactBackfilled = func(_ LedgerDB, artifact Artifact) error {
					if artifact.ID == "one" {
						return fmt.Errorf("injected after first backfill")
					}
					return nil
				}
			} else {
				hooks.beforeAuthorityMarked = func(LedgerDB) error { return fmt.Errorf("injected before marker") }
			}
			if err := ensureLedgerChecksumsAtomic(context.Background(), db, "test", artifacts, hooks); err == nil {
				t.Fatal("injected conversion failure unexpectedly succeeded")
			}
			var blankCount int
			if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE artifact_checksum IS NULL`).Scan(&blankCount); err != nil {
				t.Fatal(err)
			}
			if blankCount != 2 {
				t.Fatalf("blank rows after rollback = %d, want 2", blankCount)
			}
			if hasTable(t, db, "schema_migration_checksum_conversions") {
				t.Fatal("failed shipped-shape conversion left marker table behind")
			}
			if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", artifacts); err != nil {
				t.Fatalf("retry conversion: %v", err)
			}
		})
	}
}

func TestEnsureLedgerChecksumsRejectsConversionMarkerDrift(t *testing.T) {
	for _, markerChecksum := range []string{"", "wrong"} {
		t.Run(fmt.Sprintf("checksum-%q", markerChecksum), func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			artifact := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
			if _, err := db.Exec(`
				CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT);
				INSERT INTO schema_migrations VALUES('one','now',?);
			`, artifact.Checksum); err != nil {
				t.Fatal(err)
			}
			if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err != nil {
				t.Fatal(err)
			}
			if markerChecksum == "" {
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE schema_migration_checksum_conversions SET artifact_checksum=? WHERE authority_id='test'`, markerChecksum); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
				t.Fatal(err)
			}
			if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err == nil {
				t.Fatal("conversion marker drift was accepted")
			}
		})
	}
}

func TestEnsureLedgerChecksumsPreservesExecutedV1AndAddsV2(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	artifact := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
	v1SQL, err := fs.ReadFile(checksumConversionFiles, checksumConversionV1ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(v1SQL)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES('one','now',?)`, artifact.Checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migration_checksum_conversions VALUES('test',1,?,'2026-07-13T14:23:14.490Z')`, checksumConversionV1ManifestSum); err != nil {
		t.Fatal(err)
	}

	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT format_version,artifact_checksum,completed_at FROM schema_migration_checksum_conversions WHERE authority_id='test' ORDER BY format_version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type marker struct {
		version     int
		checksum    string
		completedAt string
	}
	var markers []marker
	for rows.Next() {
		var current marker
		if err := rows.Scan(&current.version, &current.checksum, &current.completedAt); err != nil {
			t.Fatal(err)
		}
		markers = append(markers, current)
	}
	if len(markers) != 2 {
		t.Fatalf("marker count = %d, want 2", len(markers))
	}
	if markers[0] != (marker{version: 1, checksum: checksumConversionV1ManifestSum, completedAt: "2026-07-13T14:23:14.490Z"}) {
		t.Fatalf("v1 marker changed: %+v", markers[0])
	}
	if markers[1].version != 2 || markers[1].checksum != checksumConversionManifestSum {
		t.Fatalf("v2 marker = %+v", markers[1])
	}
}

func TestEnsureLedgerChecksumsV1MarkerRejectsLaterBlank(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	artifact := Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}
	v1SQL, err := fs.ReadFile(checksumConversionFiles, checksumConversionV1ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT); INSERT INTO schema_migrations(id,applied_at) VALUES('one','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(v1SQL)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migration_checksum_conversions VALUES('test',1,?,'now')`, checksumConversionV1ManifestSum); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err == nil {
		t.Fatal("v1-converted blank checksum was backfilled")
	}
	var v2Count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migration_checksum_conversions WHERE authority_id='test' AND format_version=2`).Scan(&v2Count); err != nil {
		t.Fatal(err)
	}
	if v2Count != 0 {
		t.Fatalf("failed v2 conversion left %d markers", v2Count)
	}
}

func TestEnsureLedgerChecksumsRejectsConversionTableDrift(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT);
		CREATE TABLE schema_migration_checksum_conversions(
			authority_id TEXT NOT NULL,
			format_version INTEGER NOT NULL,
			artifact_checksum TEXT NOT NULL,
			completed_at TEXT NOT NULL,
			PRIMARY KEY(authority_id, format_version)
		);
	`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", nil); err == nil {
		t.Fatal("conversion table without pinned constraints was accepted")
	}
}

func TestEnsureLedgerChecksumsRejectsConversionMarkerTriggerDrift(t *testing.T) {
	for _, trigger := range []struct {
		name string
		sql  string
	}{
		{
			name: "ignore-insert",
			sql:  `CREATE TRIGGER ignore_conversion_marker BEFORE INSERT ON schema_migration_checksum_conversions BEGIN SELECT RAISE(IGNORE); END`,
		},
		{
			name: "rewrite-checksum",
			sql:  `CREATE TRIGGER rewrite_conversion_marker AFTER INSERT ON schema_migration_checksum_conversions BEGIN UPDATE schema_migration_checksum_conversions SET artifact_checksum='wrong' WHERE authority_id=NEW.authority_id AND format_version=NEW.format_version; END`,
		},
	} {
		t.Run(trigger.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "first", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(trigger.sql); err != nil {
				t.Fatal(err)
			}
			if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "second", nil); err == nil {
				t.Fatal("marker trigger drift was accepted")
			}
			var secondMarkers int
			if err := db.QueryRow(`SELECT count(*) FROM schema_migration_checksum_conversions WHERE authority_id='second'`).Scan(&secondMarkers); err != nil {
				t.Fatal(err)
			}
			if secondMarkers != 0 {
				t.Fatalf("failed conversion left %d second-authority markers", secondMarkers)
			}
		})
	}
}

func TestEnsureLedgerChecksumsMarksVerifiedAuthorityAndPreservesUnknownRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	artifact := Artifact{ID: "known", Path: "known.sql", Checksum: Sum([]byte("SELECT 1;"))}
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT);
		INSERT INTO schema_migrations VALUES('known','now',?),('unknown','now',NULL);
	`, artifact.Checksum); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", []Artifact{artifact}); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	var markerCount int
	var markerChecksum string
	if err := db.QueryRow(`SELECT count(*),min(artifact_checksum) FROM schema_migration_checksum_conversions WHERE authority_id='test'`).Scan(&markerCount, &markerChecksum); err != nil {
		t.Fatal(err)
	}
	if markerCount != 1 || markerChecksum != checksumConversionManifestSum {
		t.Fatalf("marker count/checksum = %d/%q", markerCount, markerChecksum)
	}
	var unknownChecksum sql.NullString
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id='unknown'`).Scan(&unknownChecksum); err != nil {
		t.Fatal(err)
	}
	if unknownChecksum.Valid {
		t.Fatalf("unknown checksum changed to %q", unknownChecksum.String)
	}
}

func TestEnsureLedgerChecksumsSerializesConcurrentAuthorities(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT);
		INSERT INTO schema_migrations(id,applied_at) VALUES('one','now'),('two','now');
	`); err != nil {
		t.Fatal(err)
	}
	conversions := []struct {
		authority Authority
		artifact  Artifact
	}{
		{"one", Artifact{ID: "one", Path: "one.sql", Checksum: Sum([]byte("SELECT 1;"))}},
		{"two", Artifact{ID: "two", Path: "two.sql", Checksum: Sum([]byte("SELECT 2;"))}},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(conversions))
	for _, conversion := range conversions {
		conversion := conversion
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- EnsureLedgerChecksumsAtomic(context.Background(), db, conversion.authority, []Artifact{conversion.artifact})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var markers, checksummed int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migration_checksum_conversions`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE length(artifact_checksum)=64`).Scan(&checksummed); err != nil {
		t.Fatal(err)
	}
	if markers != 2 || checksummed != 2 {
		t.Fatalf("markers/checksummed = %d/%d, want 2/2", markers, checksummed)
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
	err = ensureLedgerChecksumsAtomic(context.Background(), db, "test", artifacts, conversionHooks{
		afterLedgerPrepared: func(db LedgerDB) error {
			_, err := db.ExecContext(context.Background(), `
			CREATE TRIGGER fail_second_checksum_backfill
			BEFORE UPDATE OF artifact_checksum ON schema_migrations
			WHEN OLD.id='two'
			BEGIN
				SELECT RAISE(ABORT, 'injected second backfill update failure');
			END;
		`)
			return err
		},
	})
	if err == nil {
		t.Fatal("interrupted legacy checksum conversion unexpectedly succeeded")
	}
	if hasColumn(t, db, "schema_migrations", "artifact_checksum") {
		t.Fatal("failed legacy conversion left artifact_checksum column behind")
	}
	if hasTable(t, db, "schema_migration_checksum_conversions") {
		t.Fatal("failed legacy conversion left authority marker table behind")
	}

	if err := EnsureLedgerChecksumsAtomic(context.Background(), db, "test", artifacts); err != nil {
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

func hasTable(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
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
