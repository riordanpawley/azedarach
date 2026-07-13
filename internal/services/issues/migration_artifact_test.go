package issues

import (
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestMigrationRegistryRequiresPinnedArtifactForEveryID(t *testing.T) {
	if err := validateMigrationRegistry(); err != nil {
		t.Fatal(err)
	}
	for _, migration := range orderedMigrations {
		if migration.apply != nil && migration.path == "" {
			t.Fatalf("Go-assisted migration %s has no artifact", migration.id)
		}
	}
}

func TestMigrationRegistryRejectsCallbackOnlyAndDuplicateIDs(t *testing.T) {
	original := orderedMigrations
	t.Cleanup(func() { orderedMigrations = original })

	orderedMigrations = append([]migration(nil), original...)
	orderedMigrations[len(orderedMigrations)-1].path = ""
	if err := validateMigrationRegistry(); err == nil {
		t.Fatal("callback-only migration accepted")
	}

	orderedMigrations = append(append([]migration(nil), original...), original[0])
	if err := validateMigrationRegistry(); err == nil {
		t.Fatal("duplicate migration ID accepted")
	}
}

func TestMigrationRunRecordsArtifactChecksums(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range migrationArtifacts {
		var recorded string
		if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, artifact.ID).Scan(&recorded); err != nil {
			t.Fatalf("read migration %s checksum: %v", artifact.ID, err)
		}
		if recorded != artifact.Checksum {
			t.Fatalf("migration %s checksum = %q, want %q", artifact.ID, recorded, artifact.Checksum)
		}
	}
}

func TestMigrationFailureLeavesEarlierCommittedRowsChecksummed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())
	client.boardViewsMigrationFailureHook = func(stage string) error {
		if stage == "after_schema" {
			return errors.New("injected later migration failure")
		}
		return nil
	}
	if _, err := client.dbHandle(); err == nil {
		t.Fatal("migration run unexpectedly succeeded")
	}
	_ = client.CloseDB()

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, artifact := range migrationArtifacts {
		if artifact.ID == boardViewsMigrationID {
			break
		}
		var recorded string
		if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, artifact.ID).Scan(&recorded); err != nil {
			t.Fatalf("read earlier migration %s checksum after later failure: %v", artifact.ID, err)
		}
		if recorded != artifact.Checksum {
			t.Fatalf("earlier migration %s checksum = %q, want %q", artifact.ID, recorded, artifact.Checksum)
		}
	}
}
