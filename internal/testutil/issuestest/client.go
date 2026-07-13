package issuestest

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/testutil/sqlitetest"
)

var (
	templateOnce sync.Once
	templateDB   *sqlitetest.Template
	templateErr  error
)

// NewClient returns an issue client backed by an isolated clone of the one
// process-local, fully migrated template.
func NewClient(tb testing.TB, repoDir string, logger *slog.Logger) *issues.Client {
	tb.Helper()
	return NewClientAtPath(tb, filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
}

// NewClientAtPath clones the migrated template when path does not exist, then
// opens and registers cleanup for the resulting isolated issue database.
// A pre-existing path is intentionally preserved for migration and repair tests.
func NewClientAtPath(tb testing.TB, path string, logger *slog.Logger) *issues.Client {
	tb.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		template := migratedTemplate(tb)
		if _, err := template.Clone(path); err != nil {
			tb.Fatalf("clone migrated issue-store template: %v", err)
		}
	} else if err != nil {
		tb.Fatalf("inspect issue-store test database: %v", err)
	}
	client := issues.NewClientAtPath(path, logger)
	if _, err := client.ProjectionSourceVersion(tb.Context()); err != nil {
		tb.Fatalf("open cloned issue-store database: %v", err)
	}
	tb.Cleanup(func() {
		if err := client.CloseDB(); err != nil {
			tb.Errorf("close issue-store test database: %v", err)
		}
	})
	return client
}

// TemplateCreationDuration exposes the one-time migration cost for benchmarks.
func TemplateCreationDuration(tb testing.TB) time.Duration {
	tb.Helper()
	return migratedTemplate(tb).CreationDuration()
}

func migratedTemplate(tb testing.TB) *sqlitetest.Template {
	tb.Helper()
	templateOnce.Do(func() {
		templateDB, templateErr = sqlitetest.NewTemplate(func(path string) error {
			client := issues.NewClientAtPath(path, slog.New(slog.DiscardHandler))
			if _, err := client.ProjectionSourceVersion(tb.Context()); err != nil {
				_ = client.CloseDB()
				return err
			}
			return client.CloseDB()
		})
	})
	if templateErr != nil {
		tb.Fatalf("create migrated issue-store template: %v", templateErr)
	}
	return templateDB
}
