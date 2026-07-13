package sqlitetest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Template is a closed, checkpointed SQLite database that can be copied into
// isolated test directories without sharing mutable database state.
type Template struct {
	root             string
	path             string
	creationDuration time.Duration
}

// NewTemplate creates and validates one process-local SQLite template.
// initialize must close every connection it opens before returning.
func NewTemplate(initialize func(path string) error) (*Template, error) {
	root, err := os.MkdirTemp("", "azedarach-sqlite-template-*")
	if err != nil {
		return nil, fmt.Errorf("create SQLite template directory: %w", err)
	}
	path := filepath.Join(root, "template.db")
	started := time.Now()
	if err := initialize(path); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("initialize SQLite template: %w", err)
	}
	if err := checkpointAndValidate(path); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("remove checkpointed SQLite template sidecar: %w", err)
		}
	}
	return &Template{root: root, path: path, creationDuration: time.Since(started)}, nil
}

// Close removes the process-local template directory.
func (t *Template) Close() error {
	return os.RemoveAll(t.root)
}

// CreationDuration reports the one-time migration and validation cost.
func (t *Template) CreationDuration() time.Duration {
	return t.creationDuration
}

// Clone copies the immutable template to path and reports only copy cost.
func (t *Template) Clone(path string) (time.Duration, error) {
	started := time.Now()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("create SQLite clone directory: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return 0, fmt.Errorf("refusing to overwrite SQLite clone artifact %s", path+suffix)
		} else if !os.IsNotExist(err) {
			return 0, fmt.Errorf("inspect SQLite clone artifact %s: %w", path+suffix, err)
		}
	}
	in, err := os.Open(t.path)
	if err != nil {
		return 0, fmt.Errorf("open SQLite template: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create SQLite clone: %w", err)
	}
	remove := true
	defer func() {
		_ = out.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return 0, fmt.Errorf("copy SQLite template: %w", err)
	}
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("close SQLite clone: %w", err)
	}
	remove = false
	return time.Since(started), nil
}

func checkpointAndValidate(path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open SQLite template for validation: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("checkpoint SQLite template: %w", err)
	}
	if busy != 0 || logFrames != checkpointedFrames {
		return fmt.Errorf("checkpoint SQLite template incomplete: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("integrity-check SQLite template: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite template integrity check: %s", integrity)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign-key-check SQLite template: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("SQLite template contains a foreign-key violation")
	}
	return rows.Err()
}
