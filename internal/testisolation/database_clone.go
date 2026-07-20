package testisolation

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// SnapshotDatabaseClone creates a transactionally consistent SQLite snapshot
// of an already-safe migration clone. The source is opened read-only and is
// checked against every configured production database before SQLite sees it.
func SnapshotDatabaseClone(ctx context.Context, source, destination, cwd string) error {
	if err := CheckDatabaseClone(source, cwd); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create database snapshot directory: %w", err)
	}
	sourcePath, err := filepath.Abs(filepath.Clean(source))
	if err != nil {
		return fmt.Errorf("canonicalize database snapshot source: %w", err)
	}
	destinationPath, err := filepath.Abs(filepath.Clean(destination))
	if err != nil {
		return fmt.Errorf("canonicalize database snapshot destination: %w", err)
	}
	if sourcePath == destinationPath {
		return fmt.Errorf("database snapshot source and destination are identical: %s", sourcePath)
	}
	sourceURL := url.URL{Scheme: "file", Path: sourcePath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", sourceURL.String())
	if err != nil {
		return fmt.Errorf("open database snapshot source read-only: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", destinationPath); err != nil {
		return fmt.Errorf("snapshot database %s: %w", sourcePath, err)
	}
	if err := os.Chmod(destinationPath, 0o600); err != nil {
		return fmt.Errorf("restrict database snapshot permissions: %w", err)
	}
	return nil
}
