package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const legacyAttachmentMigrationChecksum = "d402580910f62087210727ca7fdf814d2c4cce819429c5bf68fa77f76fb11a3d"

func TestLegacyAttachmentBlobForwardMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "azedarach.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyAttachmentBlobForwardSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedLegacyAttachmentBlobSchema(t, db, []byte("historical attachment"))
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	upgraded := NewClientAtPath(path, slog.Default())
	db, err = upgraded.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyAttachmentBlobForwardSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var filename, relativePath, mimeType, createdAt, checksum string
	var size int64
	if err := db.QueryRow(`SELECT filename,relative_path,mime_type,size,created_at FROM issue_attachments WHERE issue_id='drb' AND attachment_id='legacy-1'`).Scan(&filename, &relativePath, &mimeType, &size, &createdAt); err != nil {
		t.Fatal(err)
	}
	if filename != "2d5a1478daf2a158-notes.txt" || relativePath != ".azedarach/attachments/"+filename || mimeType != "text/plain" || size != 21 || createdAt != "2026-07-17T00:00:00Z" {
		t.Fatalf("migrated row filename=%q path=%q mime=%q size=%d created=%q", filename, relativePath, mimeType, size, createdAt)
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(path), "attachments", filename))
	if err != nil || string(content) != "historical attachment" {
		t.Fatalf("migrated content=%q err=%v", content, err)
	}
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, legacyAttachmentBlobForwardMigrationID).Scan(&checksum); err != nil || checksum != legacyAttachmentMigrationChecksum {
		t.Fatalf("checksum=%q err=%v", checksum, err)
	}
	if err := upgraded.CloseDB(); err != nil {
		t.Fatal(err)
	}

	reopened := NewClientAtPath(path, slog.Default())
	reopenedDB, err := reopened.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.CloseDB()
	var markers int
	if err := reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, legacyAttachmentBlobForwardMigrationID).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("idempotent marker count=%d err=%v", markers, err)
	}
}

func TestLegacyAttachmentBlobForwardMigrationCurrentSchemaNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE id=?`, legacyAttachmentBlobForwardMigrationID); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	reopened := NewClientAtPath(path, slog.Default())
	called := false
	reopened.legacyAttachmentMigrationFailureHook = func(stage string) error {
		called = true
		return nil
	}
	if _, err := reopened.dbHandle(); err != nil {
		t.Fatal(err)
	}
	defer reopened.CloseDB()
	if !called {
		t.Fatal("current-schema forward migration did not run on the normal startup path")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "attachments")); !os.IsNotExist(err) {
		t.Fatalf("current-schema no-op unexpectedly created attachment storage: %v", err)
	}
}

func TestLegacyAttachmentBlobForwardMigrationRollsBackFilesSchemaAndLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyAttachmentBlobSchema(t, db, []byte("historical attachment"))
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	failed := NewClientAtPath(path, slog.Default())
	failed.legacyAttachmentMigrationFailureHook = func(stage string) error {
		if stage == "before_record" {
			return errors.New("injected interruption")
		}
		return nil
	}
	if _, err := failed.dbHandle(); err == nil || !strings.Contains(err.Error(), "injected interruption") {
		t.Fatalf("migration error=%v", err)
	}
	_ = failed.CloseDB()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var blobColumns, rows, markers int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('issue_attachments') WHERE name='content_blob'`).Scan(&blobColumns)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM issue_attachments WHERE issue_id='drb' AND attachment_id='legacy-1'`).Scan(&rows)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, legacyAttachmentBlobForwardMigrationID).Scan(&markers)
	_ = raw.Close()
	if blobColumns != 1 || rows != 1 || markers != 0 {
		t.Fatalf("rollback blob_columns=%d rows=%d markers=%d", blobColumns, rows, markers)
	}
	if entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "attachments")); err != nil || len(entries) != 0 {
		t.Fatalf("rollback attachment entries=%v err=%v", entries, err)
	}

	retried := NewClientAtPath(path, slog.Default())
	if _, err := retried.dbHandle(); err != nil {
		t.Fatal(err)
	}
	defer retried.CloseDB()
}

func TestLegacyAttachmentBlobForwardMigrationHandlesPreexistingContent(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		content     []byte
		symlink     bool
		wantError   string
		wantMarkers int
	}{
		{name: "reuses complete crash leftover", content: []byte("historical attachment"), wantMarkers: 1},
		{name: "rejects conflicting destination", content: []byte("different content"), wantError: "different content"},
		{name: "rejects symlink destination", content: []byte("historical attachment"), symlink: true, wantError: "not a regular file"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "azedarach.db")
			seed := NewClientAtPath(path, slog.Default())
			db, err := seed.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			seedLegacyAttachmentBlobSchema(t, db, []byte("historical attachment"))
			if err := seed.CloseDB(); err != nil {
				t.Fatal(err)
			}
			attachmentDir := filepath.Join(filepath.Dir(path), "attachments")
			if err := os.MkdirAll(attachmentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(attachmentDir, "2d5a1478daf2a158-notes.txt")
			if testCase.symlink {
				backing := filepath.Join(filepath.Dir(path), "external-attachment")
				if err := os.WriteFile(backing, testCase.content, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(backing, destination); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(destination, testCase.content, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			candidate := NewClientAtPath(path, slog.Default())
			_, err = candidate.dbHandle()
			if testCase.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if testCase.wantError != "" && (err == nil || !strings.Contains(err.Error(), testCase.wantError)) {
				t.Fatalf("migration error=%v, want %q", err, testCase.wantError)
			}
			_ = candidate.CloseDB()

			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			var markers int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, legacyAttachmentBlobForwardMigrationID).Scan(&markers); err != nil {
				t.Fatal(err)
			}
			var blobColumns int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('issue_attachments') WHERE name='content_blob'`).Scan(&blobColumns); err != nil {
				t.Fatal(err)
			}
			_ = raw.Close()
			if markers != testCase.wantMarkers {
				t.Fatalf("migration markers=%d, want %d", markers, testCase.wantMarkers)
			}
			if testCase.wantError != "" && blobColumns != 1 {
				t.Fatalf("conflict changed legacy schema: blob columns=%d", blobColumns)
			}
			got, err := os.ReadFile(destination)
			if err != nil || string(got) != string(testCase.content) {
				t.Fatalf("destination content=%q err=%v", got, err)
			}
		})
	}
}

func TestLegacyAttachmentBlobForwardMigrationPreservesToleratedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyAttachmentBlobSchema(t, db, []byte("historical attachment"))
	if _, err := db.Exec(`UPDATE issue_attachments SET filename='', original_path='/legacy/fallback.bin', mime_type='', size_bytes=-7, created_at=''`); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	candidate := NewClientAtPath(path, slog.Default())
	db, err = candidate.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.CloseDB()
	var filename, mimeType, createdAt string
	var size int64
	if err := db.QueryRow(`SELECT filename,mime_type,size,created_at FROM issue_attachments WHERE issue_id='drb' AND attachment_id='legacy-1'`).Scan(&filename, &mimeType, &size, &createdAt); err != nil {
		t.Fatal(err)
	}
	if filename != "2d5a1478daf2a158-fallback.bin" || mimeType != "" || size != -7 || createdAt != "" {
		t.Fatalf("preserved metadata filename=%q mime=%q size=%d created=%q", filename, mimeType, size, createdAt)
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(path), "attachments", filename))
	if err != nil || string(content) != "historical attachment" {
		t.Fatalf("preserved content=%q err=%v", content, err)
	}
}

func TestLegacyAttachmentBlobForwardMigrationBoundsFilenames(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		filename string
	}{
		{name: "NAME_MAX ASCII basename", filename: strings.Repeat("a", 250)},
		{name: "multibyte UTF-8 basename", filename: strings.Repeat("界", 100)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "azedarach.db")
			seed := NewClientAtPath(path, slog.Default())
			db, err := seed.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			seedLegacyAttachmentBlobSchema(t, db, []byte("historical attachment"))
			if _, err := db.Exec(`UPDATE issue_attachments SET filename=?, original_path=?`, testCase.filename, "/legacy/"+testCase.filename); err != nil {
				t.Fatal(err)
			}
			if err := seed.CloseDB(); err != nil {
				t.Fatal(err)
			}

			candidate := NewClientAtPath(path, slog.Default())
			db, err = candidate.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			defer candidate.CloseDB()
			var filename string
			if err := db.QueryRow(`SELECT filename FROM issue_attachments WHERE issue_id='drb' AND attachment_id='legacy-1'`).Scan(&filename); err != nil {
				t.Fatal(err)
			}
			if len(filename) > legacyAttachmentFilenameMaxBytes || !utf8.ValidString(filename) {
				t.Fatalf("bounded filename bytes=%d valid_utf8=%v", len(filename), utf8.ValidString(filename))
			}
			if len(filename) != legacyAttachmentFilenamePrefixBytes+len(truncateUTF8Bytes(testCase.filename, legacyAttachmentFilenameMaxBytes-legacyAttachmentFilenamePrefixBytes)) {
				t.Fatalf("filename bytes=%d did not use deterministic budget", len(filename))
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "attachments", filename)); err != nil {
				t.Fatalf("bounded attachment path is inaccessible: %v", err)
			}
		})
	}
}

func TestLegacyAttachmentBlobForwardMigrationRejectsDrift(t *testing.T) {
	t.Run("applied target loses index", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "azedarach.db")
		client := NewClientAtPath(path, slog.Default())
		db, err := client.dbHandle()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP INDEX idx_issue_attachments_attachment_id`); err != nil {
			t.Fatal(err)
		}
		_ = client.CloseDB()
		reopened := NewClientAtPath(path, slog.Default())
		if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "target object drift") {
			t.Fatalf("drift error=%v", err)
		}
		_ = reopened.CloseDB()
	})

	t.Run("partial legacy shape", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "azedarach.db")
		client := NewClientAtPath(path, slog.Default())
		db, err := client.dbHandle()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM schema_migrations WHERE id=?; DROP TABLE issue_attachments; CREATE TABLE issue_attachments(issue_id TEXT NOT NULL, attachment_id TEXT NOT NULL, content_blob BLOB NOT NULL, PRIMARY KEY(issue_id,attachment_id))`, legacyAttachmentBlobForwardMigrationID); err != nil {
			t.Fatal(err)
		}
		_ = client.CloseDB()
		reopened := NewClientAtPath(path, slog.Default())
		if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "unknown or partial shape") {
			t.Fatalf("partial drift error=%v", err)
		}
		_ = reopened.CloseDB()
	})

	for _, testCase := range []struct {
		name string
		ddl  string
	}{
		{name: "table CHECK", ddl: `DROP TABLE issue_attachments;
			CREATE TABLE issue_attachments(issue_id TEXT NOT NULL,attachment_id TEXT NOT NULL,filename TEXT NOT NULL,relative_path TEXT NOT NULL,mime_type TEXT NOT NULL,size INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,PRIMARY KEY(issue_id,attachment_id),CHECK(size>=0));
			CREATE INDEX idx_issue_attachments_attachment_id ON issue_attachments(attachment_id)`},
		{name: "table foreign key", ddl: `DROP TABLE issue_attachments;
			CREATE TABLE issue_attachments(issue_id TEXT NOT NULL,attachment_id TEXT NOT NULL,filename TEXT NOT NULL,relative_path TEXT NOT NULL,mime_type TEXT NOT NULL,size INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,PRIMARY KEY(issue_id,attachment_id),FOREIGN KEY(issue_id) REFERENCES issues(id));
			CREATE INDEX idx_issue_attachments_attachment_id ON issue_attachments(attachment_id)`},
		{name: "WITHOUT ROWID", ddl: `DROP TABLE issue_attachments;
			CREATE TABLE issue_attachments(issue_id TEXT NOT NULL,attachment_id TEXT NOT NULL,filename TEXT NOT NULL,relative_path TEXT NOT NULL,mime_type TEXT NOT NULL,size INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,PRIMARY KEY(issue_id,attachment_id)) WITHOUT ROWID;
			CREATE INDEX idx_issue_attachments_attachment_id ON issue_attachments(attachment_id)`},
		{name: "extra trigger", ddl: `CREATE TRIGGER issue_attachments_drift AFTER INSERT ON issue_attachments BEGIN SELECT 1; END`},
		{name: "extra index", ddl: `CREATE INDEX issue_attachments_drift ON issue_attachments(issue_id)`},
		{name: "index collation and order", ddl: `DROP INDEX idx_issue_attachments_attachment_id; CREATE INDEX idx_issue_attachments_attachment_id ON issue_attachments(attachment_id COLLATE NOCASE DESC)`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "azedarach.db")
			client := NewClientAtPath(path, slog.Default())
			db, err := client.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(testCase.ddl); err != nil {
				t.Fatal(err)
			}
			_ = client.CloseDB()
			reopened := NewClientAtPath(path, slog.Default())
			if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "object drift") {
				t.Fatalf("object drift error=%v", err)
			}
			_ = reopened.CloseDB()
		})
	}

	t.Run("unexpected legacy source index", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "azedarach.db")
		client := NewClientAtPath(path, slog.Default())
		db, err := client.dbHandle()
		if err != nil {
			t.Fatal(err)
		}
		seedLegacyAttachmentBlobSchema(t, db, []byte("historical attachment"))
		if _, err := db.Exec(`CREATE INDEX issue_attachments_drift ON issue_attachments(filename)`); err != nil {
			t.Fatal(err)
		}
		_ = client.CloseDB()
		reopened := NewClientAtPath(path, slog.Default())
		if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "legacy issue attachment schema drift") {
			t.Fatalf("legacy object drift error=%v", err)
		}
		_ = reopened.CloseDB()
	})
}

func seedLegacyAttachmentBlobSchema(t *testing.T, db *sql.DB, data []byte) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE id=?`, legacyAttachmentBlobForwardMigrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		DROP TABLE issue_attachments;
		CREATE TABLE issue_attachments (
			issue_id TEXT NOT NULL,
			attachment_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			original_path TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			content_blob BLOB NOT NULL,
			PRIMARY KEY (issue_id, attachment_id)
		);
		CREATE INDEX idx_issue_attachments_attachment_id ON issue_attachments(attachment_id);
		CREATE INDEX idx_issue_attachments_issue ON issue_attachments(issue_id, created_at, attachment_id);
		INSERT INTO issue_attachments(issue_id,attachment_id,filename,original_path,mime_type,size_bytes,created_at,content_blob)
		VALUES('drb','legacy-1','notes.txt','/legacy/notes.txt','text/plain',?,'2026-07-17T00:00:00Z',?)
	`, len(data), data); err != nil {
		t.Fatal(err)
	}
}
