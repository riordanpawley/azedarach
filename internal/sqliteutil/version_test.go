package sqliteutil

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func TestSQLiteEngineIncludesWALResetRaceFix(t *testing.T) {
	db, err := sql.Open("sqlite", "file:sqlite-version-check?mode=memory&cache=private")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var version string
	if err := db.QueryRowContext(context.Background(), `SELECT sqlite_version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatalf("parse SQLite version %q: %v", version, err)
	}
	if major < 3 || major == 3 && (minor < 51 || minor == 51 && patch < 3) {
		t.Fatalf("SQLite %s lacks the WAL-reset race fix; require 3.51.3 or newer", version)
	}
}
