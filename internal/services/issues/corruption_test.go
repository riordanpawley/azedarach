package issues

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type codedCorruptionTestError struct{}

func (codedCorruptionTestError) Error() string { return "database disk image is malformed" }
func (codedCorruptionTestError) Code() int     { return sqliteCorruptPrimaryCode }

func TestClientQuarantinesRuntimeSQLiteCorruption(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())

	first := client.wrapError("list-mail-observation-events", "parent", codedCorruptionTestError{})
	if !IsSQLiteCorrupt(first) || !errors.Is(first, ErrSQLiteCorrupt) {
		t.Fatalf("first error = %v, want typed SQLite corruption", first)
	}
	if !strings.Contains(first.Error(), "Preserve the database and WAL") || !strings.Contains(first.Error(), "online-backup clone") {
		t.Fatalf("first error = %q, want preservation and clone recovery guidance", first)
	}

	_, second := client.dbHandle()
	var storeErr *domain.TaskStoreError
	if !errors.As(second, &storeErr) || storeErr.Op != "open-db" || !IsSQLiteCorrupt(second) {
		t.Fatalf("second error = %v, want quarantined open-db failure", second)
	}
}

func TestClientRejectsCorruptDatabaseBeforeStartupWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	client := NewClient(dir, slog.Default())
	if _, err := client.Create(ctx, CreateTaskParams{Title: "preserved", Type: domain.TypeBug, Status: domain.StatusOpen}); err != nil {
		t.Fatalf("create fixture issue: %v", err)
	}
	dbPath := filepath.Join(dir, ".azedarach", "azedarach.db")
	if err := client.CloseDB(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	corruptSQLiteRootPage(t, dbPath, "issue_observation_events")
	before := fileSHA256(t, dbPath)
	reopened := NewClientAtPath(dbPath, slog.Default(), WithExistingDatabaseOnly())
	_, err := reopened.ListIssueObservationMailEvents(ctx, "parent")
	if !IsSQLiteCorrupt(err) || !errors.Is(err, ErrSQLiteCorrupt) {
		t.Fatalf("open corrupt database error = %v, want typed corruption", err)
	}
	if got := fileSHA256(t, dbPath); got != before {
		t.Fatalf("corrupt database changed during rejected startup: got %x want %x", got, before)
	}
	if _, statErr := os.Stat(dbPath + "-wal"); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("inspect preserved WAL: %v", statErr)
	}

	_, err = reopened.ListIssueObservationMailEvents(ctx, "parent")
	var storeErr *domain.TaskStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "open-db" || !IsSQLiteCorrupt(err) {
		t.Fatalf("second read error = %v, want quarantined open-db failure", err)
	}
}

func TestClientRejectsCorruptWALDatabaseWithoutChangingAuthorityBytes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	client := NewClient(dir, slog.Default())
	if _, err := client.Create(ctx, CreateTaskParams{Title: "preserved", Type: domain.TypeBug, Status: domain.StatusOpen}); err != nil {
		t.Fatalf("create fixture issue: %v", err)
	}
	dbPath := filepath.Join(dir, ".azedarach", "azedarach.db")
	if err := client.CloseDB(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	rootPage, pageSize := sqliteObjectRootPage(t, dbPath, "issue_observation_events")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteWALCrashFixtureHelper$", "--", dbPath)
	cmd.Env = append(os.Environ(), "AZEDARACH_SQLITE_WAL_CRASH_FIXTURE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create crashed WAL fixture: %v\n%s", err, output)
	}
	walPath := dbPath + "-wal"
	walBefore := fileBytes(t, walPath)
	if len(walBefore) <= 32 {
		t.Fatalf("WAL fixture length = %d, want committed frames", len(walBefore))
	}
	corruptSQLitePage(t, dbPath, rootPage, pageSize)
	dbBefore := fileBytes(t, dbPath)

	reopened := NewClientAtPath(dbPath, slog.Default(), WithExistingDatabaseOnly())
	_, err := reopened.ListIssueObservationMailEvents(ctx, "parent")
	if !IsSQLiteCorrupt(err) || !errors.Is(err, ErrSQLiteCorrupt) {
		t.Fatalf("open corrupt WAL database error = %v, want typed corruption", err)
	}
	if got := fileBytes(t, dbPath); !bytes.Equal(got, dbBefore) {
		t.Fatal("database bytes changed during rejected read-only preflight")
	}
	if got := fileBytes(t, walPath); !bytes.Equal(got, walBefore) {
		t.Fatal("WAL bytes changed during rejected read-only preflight")
	}
}

func TestSQLiteWALCrashFixtureHelper(t *testing.T) {
	if os.Getenv("AZEDARACH_SQLITE_WAL_CRASH_FIXTURE") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		t.Fatal("missing crash fixture database path")
	}
	dbPath := os.Args[separator+1]
	writer, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=rw", filepath.ToSlash(dbPath)))
	if err != nil {
		t.Fatalf("open WAL fixture writer: %v", err)
	}
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := writer.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatalf("disable WAL autocheckpoint: %v", err)
	}
	if _, err := writer.Exec(`PRAGMA user_version=123`); err != nil {
		t.Fatalf("commit non-empty WAL fixture: %v", err)
	}
	os.Exit(0)
}

func corruptSQLiteRootPage(t *testing.T, dbPath, object string) {
	t.Helper()
	rootPage, pageSize := sqliteObjectRootPage(t, dbPath, object)
	corruptSQLitePage(t, dbPath, rootPage, pageSize)
}

func sqliteObjectRootPage(t *testing.T, dbPath, object string) (int64, int64) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=rw", filepath.ToSlash(dbPath)))
	if err != nil {
		t.Fatalf("open fixture for corruption: %v", err)
	}
	var pageSize, rootPage int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("read page size: %v", err)
	}
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = ?`, object).Scan(&rootPage); err != nil {
		t.Fatalf("read %s root page: %v", object, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture inspection handle: %v", err)
	}
	return rootPage, pageSize
}

func corruptSQLitePage(t *testing.T, dbPath string, rootPage, pageSize int64) {
	t.Helper()
	file, err := os.OpenFile(dbPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open fixture database bytes: %v", err)
	}
	zeroes := make([]byte, pageSize)
	if _, err := file.WriteAt(zeroes, (rootPage-1)*pageSize); err != nil {
		_ = file.Close()
		t.Fatalf("corrupt SQLite root page %d: %v", rootPage, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("sync corrupt fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupt fixture: %v", err)
	}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}

func fileSHA256(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(contents)
}
