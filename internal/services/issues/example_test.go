package issues_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/riordanpawley/azedarach/internal/services/issues"
)

// Example shows how to use the SQLite issue store client.
func Example() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	repoDir, err := os.Getwd()
	if err != nil {
		logger.Error("failed to determine working directory", "error", err)
		return
	}

	_ = filepath.Join(repoDir, ".azedarach", "azedarach.db")
	client := issues.NewClient(repoDir, logger)

	ctx := context.Background()
	tasks, err := client.List(ctx)
	if err != nil {
		logger.Error("failed to list tasks", "error", err)
		return
	}

	logger.Info("fetched tasks", "count", len(tasks))
}
