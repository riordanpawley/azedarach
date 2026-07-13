package issuefixture_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/testutil/issuefixture"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSeedPathPreservesProductionQuerySemanticsAndSchemaTriggers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := issues.NewClientAtPath(path, slog.Default())
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })
	_, err := client.ProjectionSourceVersion(ctx)
	require.NoError(t, err)

	result, err := issuefixture.SeedPath(ctx, path, issuefixture.Fixture{
		Issues: []issuefixture.Issue{
			{ID: "fixture-root", Title: "Fixture root", Type: domain.TypeEpic, Status: domain.StatusInProgress, Priority: domain.P1},
			{ID: "fixture-child", Title: "Fixture child", Type: domain.TypeTask, Status: domain.StatusOpen, Priority: domain.P2},
		},
		Dependencies: []issuefixture.Dependency{{IssueID: "fixture-child", DependsOnID: "fixture-root", Type: domain.DependencyParentChild}},
		Learnings: []issuefixture.Learning{
			{LocalID: "learn-fixture-1", ProjectID: "proj", Summary: "Use bounded fixture transactions", Evidence: "one"},
			{LocalID: "learn-fixture-2", ProjectID: "proj", Summary: "Use bounded fixture transactions", Evidence: "two"},
		},
		Sessions: []issuefixture.SessionProjection{{ProjectID: "proj", SessionID: "session-child", IssueID: "fixture-child", State: "running", Activity: "busy", Source: "hooks"}},
	})
	require.NoError(t, err)
	require.Equal(t, 6, result.Rows)
	require.Positive(t, result.Duration)

	tasks, err := client.ListGraphReadinessWithRuntime(ctx, "proj", "fixture-root")
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	byID := map[string]domain.Task{tasks[0].ID.String(): tasks[0], tasks[1].ID.String(): tasks[1]}
	require.Contains(t, byID, "fixture-root")
	require.Contains(t, byID, "fixture-child")
	require.NotNil(t, byID["fixture-child"].Session)
	require.Equal(t, domain.SessionBusy, byID["fixture-child"].Session.State)

	suggestions, err := client.SuggestLearningConsolidations(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, suggestions, 1)
	require.Equal(t, issues.LearningSuggestionDuplicate, suggestions[0].Kind)

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var indexed int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_learning_search_fts WHERE agent_learning_search_fts MATCH 'bounded'`).Scan(&indexed))
	require.Equal(t, 2, indexed)
}

func TestSeedDBRejectsUnboundedFixtureBeforeWriting(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := issues.NewClientAtPath(path, slog.Default())
	_, err := client.ProjectionSourceVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rows := make([]issuefixture.Issue, issuefixture.MaxRows+1)
	for i := range rows {
		rows[i] = issuefixture.Issue{ID: fmt.Sprintf("fixture-%05d", i), Title: "bounded"}
	}
	_, err = issuefixture.SeedDB(ctx, db, issuefixture.Fixture{Issues: rows})
	require.ErrorContains(t, err, "maximum")
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues`).Scan(&count))
	require.Zero(t, count)
}

func TestSeedDBRollsBackFailingBatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := issues.NewClientAtPath(path, slog.Default())
	_, err := client.ProjectionSourceVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = issuefixture.SeedDB(ctx, db, issuefixture.Fixture{Issues: []issuefixture.Issue{
		{ID: "duplicate", Title: "first", Priority: domain.P2},
		{ID: "duplicate", Title: "second", Priority: domain.P2},
	}})
	require.ErrorContains(t, err, "seed issue duplicate")
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues`).Scan(&count))
	require.Zero(t, count)
}
