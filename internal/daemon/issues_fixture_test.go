package daemon

import (
	"log/slog"
	"testing"

	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/testutil/issuestest"
)

func newMigratedIssueClient(tb testing.TB, repoDir string, logger *slog.Logger) *issues.Client {
	tb.Helper()
	return issuestest.NewClient(tb, repoDir, logger)
}

func newMigratedIssueClientAtPath(tb testing.TB, path string, logger *slog.Logger) *issues.Client {
	tb.Helper()
	return issuestest.NewClientAtPath(tb, path, logger)
}
