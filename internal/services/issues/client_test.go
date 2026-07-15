package issues

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/testutil/sqlitetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientSQLiteBusyPolicyDefaultsAndOverrides(t *testing.T) {
	parallelIssueStoreTest(t)
	defaultClient := NewClientAtPath(filepath.Join(t.TempDir(), "default.db"), nil)
	t.Cleanup(func() { require.NoError(t, defaultClient.CloseDB()) })
	assert.Equal(t, 5*time.Second, defaultClient.sqliteBusyTimeout)
	assert.Equal(t, 5*time.Second, defaultClient.sqliteBusyRetryBudget)
	assert.Equal(t, 100*time.Millisecond, defaultClient.sqliteBusyRetryDelay)
	require.NotNil(t, defaultClient.sqliteBusyWait)
	defaultDB, err := defaultClient.dbHandle()
	require.NoError(t, err)
	var defaultBusyTimeout int
	require.NoError(t, defaultDB.QueryRow(`PRAGMA busy_timeout`).Scan(&defaultBusyTimeout))
	assert.Equal(t, 100, defaultBusyTimeout)

	configured := NewClientAtPath(
		filepath.Join(t.TempDir(), "configured.db"),
		nil,
		WithSQLiteBusyPolicy(2*time.Millisecond, 3*time.Millisecond),
	)
	t.Cleanup(func() { require.NoError(t, configured.CloseDB()) })
	assert.Equal(t, 2*time.Millisecond, configured.sqliteBusyTimeout)
	assert.Equal(t, 2*time.Millisecond, configured.sqliteBusyRetryBudget)
	assert.Equal(t, 3*time.Millisecond, configured.sqliteBusyRetryDelay)
	configuredDB, err := configured.dbHandle()
	require.NoError(t, err)
	var configuredBusyTimeout int
	require.NoError(t, configuredDB.QueryRow(`PRAGMA busy_timeout`).Scan(&configuredBusyTimeout))
	assert.Equal(t, 2, configuredBusyTimeout)
}

func TestClientExistingDatabaseOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "issues.db")
	missing := NewClientAtPath(dbPath, nil, WithExistingDatabaseOnly())
	_, err := missing.dbHandle()
	require.ErrorContains(t, err, "require existing database")
	_, statErr := os.Stat(dbPath)
	require.True(t, os.IsNotExist(statErr), "existing-only open created database: %v", statErr)

	creating := NewClientAtPath(dbPath, nil)
	_, err = creating.dbHandle()
	require.NoError(t, err)
	require.NoError(t, creating.CloseDB())

	existing := NewClientAtPath(dbPath, nil, WithExistingDatabaseOnly())
	t.Cleanup(func() { require.NoError(t, existing.CloseDB()) })
	_, err = existing.dbHandle()
	require.NoError(t, err)
}

func TestClient_CRUDLifecycle(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	createdID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Create SQLite store client",
		Description: "Implement native Go sqlite path",
		Type:        domain.TypeFeature,
		Priority:    domain.P1,
		Assignee:    "sam",
		Labels:      []string{"cli", "form"},
		Design:      "Use sqlite-backed issue store",
		Notes:       "Initial note",
		Acceptance:  "CRUD works",
		Estimate:    intPtr(5),
	})
	require.NoError(t, err)
	require.NotEmpty(t, createdID)

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(createdID), tasks[0].ID)
	assert.Equal(t, "Create SQLite store client", tasks[0].Title)
	assert.Equal(t, "sam", tasks[0].Assignee)
	assert.Equal(t, []string{"cli", "form"}, tasks[0].Labels)
	assert.Equal(t, "Use sqlite-backed issue store", tasks[0].Design)
	assert.Equal(t, "Initial note", tasks[0].Notes)
	assert.Equal(t, "CRUD works", tasks[0].Acceptance)
	require.NotNil(t, tasks[0].Estimate)
	assert.Equal(t, 5, *tasks[0].Estimate)

	searchResults, err := client.Search(ctx, "SQLite")
	require.NoError(t, err)
	require.Len(t, searchResults, 1)
	assert.Equal(t, naming.IssueID(createdID), searchResults[0].ID)

	ready, err := client.Ready(ctx)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Equal(t, naming.IssueID(createdID), ready[0].ID)

	err = client.Update(ctx, createdID, domain.StatusInProgress)
	require.NoError(t, err)

	err = client.UpdateDetails(ctx, createdID, UpdateTaskParams{
		Title:       "Create native sqlite issue store",
		Description: "No bd shell calls",
		Type:        domain.TypeTask,
		Priority:    domain.P0,
	})
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, domain.StatusInProgress, tasks[0].Status)
	assert.Equal(t, "Create native sqlite issue store", tasks[0].Title)
	assert.Equal(t, domain.P0, tasks[0].Priority)
	assert.Equal(t, "Use sqlite-backed issue store", tasks[0].Design)
	assert.Equal(t, "Initial note", tasks[0].Notes)
	assert.Equal(t, "CRUD works", tasks[0].Acceptance)
	require.NotNil(t, tasks[0].Estimate)
	assert.Equal(t, 5, *tasks[0].Estimate)

	replacementDesign := "Replacement design"
	replacementNotes := "Replacement note"
	replacementAcceptance := "Replacement acceptance"
	replacementEstimate := 8
	err = client.UpdateDetails(ctx, createdID, UpdateTaskParams{
		Title:       "Create native sqlite issue store",
		Description: "No bd shell calls",
		Design:      &replacementDesign,
		Notes:       &replacementNotes,
		Acceptance:  &replacementAcceptance,
		Estimate:    &replacementEstimate,
		EstimateSet: true,
		Type:        domain.TypeTask,
		Priority:    domain.P0,
	})
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Replacement design", tasks[0].Design)
	assert.Equal(t, "Replacement note", tasks[0].Notes)
	assert.Equal(t, "Replacement acceptance", tasks[0].Acceptance)
	require.NotNil(t, tasks[0].Estimate)
	assert.Equal(t, 8, *tasks[0].Estimate)

	clearDesign := ""
	clearNotes := ""
	clearAcceptance := ""
	err = client.UpdateDetails(ctx, createdID, UpdateTaskParams{
		Title:       "Create native sqlite issue store",
		Description: "No bd shell calls",
		Design:      &clearDesign,
		Notes:       &clearNotes,
		Acceptance:  &clearAcceptance,
		EstimateSet: true,
		Type:        domain.TypeTask,
		Priority:    domain.P0,
	})
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "", tasks[0].Design)
	assert.Equal(t, "", tasks[0].Notes)
	assert.Equal(t, "", tasks[0].Acceptance)
	assert.Nil(t, tasks[0].Estimate)

	err = client.Close(ctx, createdID, "done")
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, domain.StatusDone, tasks[0].Status)

	err = client.Archive(ctx, createdID)
	require.NoError(t, err)

	tasks, err = client.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestClient_V2StateAuthorityDrivesLifecycleAndBacklogReadiness(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	backlogID, err := client.Create(ctx, CreateTaskParams{
		Title:     "Later backlog task",
		Type:      domain.TypeTask,
		Priority:  domain.P0,
		Status:    domain.StatusOpen,
		Lifecycle: domain.IssueWorkflowBacklog,
	})
	require.NoError(t, err)
	openP4ID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Low priority open task",
		Type:     domain.TypeTask,
		Priority: domain.P4,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	var lifecycle, closedOutcome, reviewState, status string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT lifecycle_state, closed_outcome, review_state, status
		FROM issues
		WHERE id = ?
	`, backlogID).Scan(&lifecycle, &closedOutcome, &reviewState, &status))
	assert.Equal(t, string(domain.IssueWorkflowBacklog), lifecycle)
	assert.Equal(t, string(domain.IssueCloseNone), closedOutcome)
	assert.Equal(t, string(domain.IssueReviewNone), reviewState)
	assert.Equal(t, string(domain.StatusOpen), status)
	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	tasksByID := taskByID(tasks)
	require.Contains(t, tasksByID, backlogID)
	require.Contains(t, tasksByID, openP4ID)
	assert.Equal(t, domain.IssueWorkflowBacklog, tasksByID[backlogID].State.Workflow())
	assert.Equal(t, domain.P0, tasksByID[backlogID].Priority)
	assert.Equal(t, domain.StatusOpen, tasksByID[backlogID].Status)
	assert.Equal(t, domain.IssueWorkflowOpen, tasksByID[openP4ID].State.Workflow())
	assert.Equal(t, domain.P4, tasksByID[openP4ID].Priority)
	assert.Equal(t, domain.StatusOpen, tasksByID[openP4ID].Status)

	ready, err := client.Ready(ctx)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Equal(t, openP4ID, ready[0].ID.String(), "P4 open issues must remain startable")

	require.NoError(t, client.UpdateDetails(ctx, backlogID, UpdateTaskParams{
		Title:    "Still backlog task",
		Type:     domain.TypeTask,
		Priority: domain.P4,
	}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state FROM issues WHERE id = ?`, backlogID).Scan(&lifecycle))
	assert.Equal(t, string(domain.IssueWorkflowBacklog), lifecycle, "priority edits must not change lifecycle")

	openLifecycle := domain.IssueWorkflowOpen
	require.NoError(t, client.UpdateDetails(ctx, backlogID, UpdateTaskParams{
		Title:     "Promoted backlog task",
		Type:      domain.TypeTask,
		Priority:  domain.P4,
		Lifecycle: &openLifecycle,
	}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state FROM issues WHERE id = ?`, backlogID).Scan(&lifecycle))
	assert.Equal(t, string(domain.IssueWorkflowOpen), lifecycle)
	ready, err = client.Ready(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{backlogID, openP4ID}, taskIDStrings(ready))

	require.NoError(t, client.Update(ctx, backlogID, domain.StatusInReview))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT lifecycle_state, closed_outcome, review_state, status
		FROM issues
		WHERE id = ?
	`, backlogID).Scan(&lifecycle, &closedOutcome, &reviewState, &status))
	assert.Equal(t, string(domain.IssueWorkflowActive), lifecycle)
	assert.Equal(t, string(domain.IssueCloseNone), closedOutcome)
	assert.Equal(t, string(domain.IssueReviewRequested), reviewState)
	assert.Equal(t, string(domain.StatusInReview), status)
	tasks, err = client.List(ctx)
	require.NoError(t, err)
	tasksByID = taskByID(tasks)
	require.Contains(t, tasksByID, backlogID)
	assert.Equal(t, domain.IssueWorkflowActive, tasksByID[backlogID].State.Workflow())
	assert.Equal(t, domain.IssueReviewRequested, tasksByID[backlogID].State.Review())
	assert.Equal(t, domain.StatusInReview, tasksByID[backlogID].Status)

	require.NoError(t, client.UpdateDetails(ctx, backlogID, UpdateTaskParams{
		Title:     "Reopened backlog task",
		Type:      domain.TypeTask,
		Priority:  domain.P0,
		Lifecycle: &openLifecycle,
	}))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT lifecycle_state, closed_outcome, review_state, status
		FROM issues
		WHERE id = ?
	`, backlogID).Scan(&lifecycle, &closedOutcome, &reviewState, &status))
	assert.Equal(t, string(domain.IssueWorkflowOpen), lifecycle)
	assert.Equal(t, string(domain.IssueCloseNone), closedOutcome)
	assert.Equal(t, string(domain.IssueReviewNone), reviewState)
	assert.Equal(t, string(domain.StatusOpen), status)
}

func TestClient_CancelledOutcomeCountsAsClosedForParentClosure(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{Title: "Parent", Type: domain.TypeEpic, Priority: domain.P2})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{Title: "Cancelled child", Type: domain.TypeTask, Priority: domain.P2})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, childID, parentID, string(domain.DependencyParentChild)))

	require.NoError(t, client.Update(ctx, childID, domain.StatusCancelled))

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	var lifecycle, outcome, status string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT lifecycle_state, closed_outcome, status
		FROM issues
		WHERE id = ?
	`, childID).Scan(&lifecycle, &outcome, &status))
	assert.Equal(t, string(domain.IssueWorkflowClosed), lifecycle)
	assert.Equal(t, string(domain.IssueCloseCancelled), outcome)
	assert.Equal(t, string(domain.StatusCancelled), status)

	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT lifecycle_state, closed_outcome
		FROM issues
		WHERE id = ?
	`, parentID).Scan(&lifecycle, &outcome))
	assert.Equal(t, string(domain.IssueWorkflowClosed), lifecycle)
	assert.Equal(t, string(domain.IssueCloseCompleted), outcome)
}

func TestClient_ArchiveVisibilityUsesArchivedAtAuthority(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-v2-archive"

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Archived by v2 state",
		Description: "archive visibility body",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Archive(ctx, issueID))

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	var archivedAt sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT archived_at FROM issues WHERE id = ?`, issueID).Scan(&archivedAt))
	require.True(t, archivedAt.Valid)
	require.NotEmpty(t, strings.TrimSpace(archivedAt.String))

	_, err = db.ExecContext(ctx, `UPDATE issues SET deleted_at = NULL WHERE id = ?`, issueID)
	require.NoError(t, err)

	active, err := client.ListWithRuntime(ctx, projectID)
	require.NoError(t, err)
	assert.NotContains(t, taskIDStrings(active), issueID)

	archivedOnly, err := client.ListSummariesWithRuntimeArchiveMode(ctx, projectID, ArchiveOnly)
	require.NoError(t, err)
	assert.Contains(t, taskIDStrings(archivedOnly), issueID)

	require.NoError(t, client.Unarchive(ctx, issueID))
	active, err = client.ListWithRuntime(ctx, projectID)
	require.NoError(t, err)
	assert.Contains(t, taskIDStrings(active), issueID)
}

func TestClient_UpdateDetailsPreservesImplementationsWhenUnset(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	createdID, err := client.Create(ctx, CreateTaskParams{
		Title:           "Implementation scoped task",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Implementations: []string{"default", "alt"},
	})
	require.NoError(t, err)

	require.NoError(t, client.UpdateDetails(ctx, createdID, UpdateTaskParams{
		Title:       "Implementation scoped task updated",
		Description: "metadata edit",
		Type:        domain.TypeTask,
		Priority:    domain.P1,
	}))

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	var implementationsJSON string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(implementations_json, '') FROM issues WHERE id = ?`, createdID).Scan(&implementationsJSON))
	assert.JSONEq(t, `["default","alt"]`, implementationsJSON)
}

func TestClient_ExternalIssueRefsAreBackendNeutralMetadata(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Imported provider task",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	ref, err := client.UpsertExternalIssueRef(ctx, UpsertExternalIssueRefParams{
		IssueID:       issueID,
		Provider:      "linear",
		ProviderScope: "team:CHE",
		RemoteKey:     "lin_opaque_key",
		DisplayKey:    "CHE-02091",
		URL:           "https://linear.app/acme/issue/CHE-02091",
		Metadata:      map[string]string{"status": "started"},
	})
	require.NoError(t, err)
	assert.Equal(t, issueID, ref.IssueID)
	assert.Equal(t, "linear", ref.Provider)
	assert.Equal(t, "team:CHE", ref.ProviderScope)
	assert.Equal(t, "lin_opaque_key", ref.RemoteKey)
	assert.Equal(t, "CHE-02091", ref.DisplayKey)
	assert.Equal(t, "started", ref.Metadata["status"])

	found, ok, err := client.GetExternalIssueRef(ctx, "linear", "team:CHE", "lin_opaque_key")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, naming.IssueID(issueID), naming.IssueID(found.IssueID))
	assert.Equal(t, "CHE-02091", found.DisplayKey)

	foundByDisplay, ok, err := client.GetExternalIssueRefByDisplayKey(ctx, "linear", "team:CHE", "CHE-02091")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, naming.IssueID(issueID), naming.IssueID(foundByDisplay.IssueID))
	assert.Equal(t, "lin_opaque_key", foundByDisplay.RemoteKey)

	refs, err := client.ListExternalIssueRefs(ctx, issueID)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "lin_opaque_key", refs[0].RemoteKey)

	task, err := client.GetWithRuntime(ctx, "proj", issueID)
	require.NoError(t, err)
	assert.Equal(t, naming.IssueID(issueID), task.ID, "runtime task id stays Az-owned, not provider-owned")
}

func TestClient_GitHubExternalRefHydratesPullRequestSummary(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Task with PR",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	_, err = client.UpsertExternalIssueRef(ctx, UpsertExternalIssueRefParams{
		IssueID:    issueID,
		Provider:   "github",
		RemoteKey:  "42",
		DisplayKey: "#42",
		URL:        "https://github.com/acme/repo/pull/42",
		Metadata: map[string]string{
			"state":         "open",
			"draft":         "true",
			"checks_status": "pending",
		},
	})
	require.NoError(t, err)

	task, err := client.GetWithRuntime(ctx, "proj", issueID)
	require.NoError(t, err)
	require.NotNil(t, task.PullRequest)
	assert.Equal(t, "github", task.Origin)
	assert.Equal(t, 42, task.PullRequest.Number)
	assert.Equal(t, "#42", task.PullRequest.DisplayKey)
	assert.Equal(t, "open", task.PullRequest.State)
	assert.True(t, task.PullRequest.Draft)
	assert.Equal(t, "pending", task.PullRequest.ChecksStatus)
}

func TestClient_GetManyWithRuntimeFiltersRequestedActiveIssues(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	firstID, err := client.Create(ctx, CreateTaskParams{
		Title:    "first",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{
		Title:    "second",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	thirdID, err := client.Create(ctx, CreateTaskParams{
		Title:    "third",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	archivedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "archived",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Archive(ctx, archivedID))

	tasks, err := client.GetManyWithRuntime(ctx, "proj", []string{secondID, "", firstID, secondID, archivedID})
	require.NoError(t, err)

	got := map[string]domain.Task{}
	for _, task := range tasks {
		got[task.ID.String()] = task
	}
	require.Len(t, got, 2)
	assert.Equal(t, "first", got[firstID].Title)
	assert.Equal(t, "second", got[secondID].Title)
	assert.NotContains(t, got, thirdID)
	assert.NotContains(t, got, archivedID)
}

func TestClient_ArchiveModeRuntimeReads(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-archive-mode"

	activeID, err := client.Create(ctx, CreateTaskParams{
		Title:       "active retained needle",
		Description: "active archive-mode search body",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	require.NoError(t, err)
	archivedID, err := client.Create(ctx, CreateTaskParams{
		Title:       "archived retained needle",
		Description: "archived archive-mode search body",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, archivedID, activeID, string(domain.DependencyBlocks)))
	require.NoError(t, client.Archive(ctx, archivedID))

	activeOnly, err := client.ListSummariesWithRuntimeArchiveMode(ctx, projectID, ArchiveExclude)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{activeID}, taskIDStrings(activeOnly))

	withArchived, err := client.ListSummariesWithRuntimeArchiveMode(ctx, projectID, ArchiveInclude)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{activeID, archivedID}, taskIDStrings(withArchived))

	archivedOnly, err := client.ListSummariesWithRuntimeArchiveMode(ctx, projectID, ArchiveOnly)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{archivedID}, taskIDStrings(archivedOnly))

	searchArchived, err := client.SearchWithRuntimeArchiveMode(ctx, projectID, "archived archive-mode", ArchiveOnly)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{archivedID}, taskIDStrings(searchArchived))

	_, err = client.GetWithDependencyContextRuntimeArchiveMode(ctx, projectID, archivedID, ArchiveExclude)
	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrNotFound)

	tasks, err := client.GetWithDependencyContextRuntimeArchiveMode(ctx, projectID, archivedID, ArchiveInclude)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{activeID, archivedID}, taskIDStrings(tasks))

	tasks, err = client.GetWithDependencyContextRuntimeArchiveMode(ctx, projectID, archivedID, ArchiveOnly)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{archivedID}, taskIDStrings(tasks))
}

func TestClient_UnarchiveRestoresActiveRuntimeReadsAndSearch(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-unarchive"

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:       "archived restore needle",
		Description: "restored issue search body",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusDone,
	})
	require.NoError(t, err)
	require.NoError(t, client.Archive(ctx, issueID))
	_, err = client.GetWithRuntime(ctx, projectID, issueID)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, client.Unarchive(ctx, issueID))

	task, err := client.GetWithRuntime(ctx, projectID, issueID)
	require.NoError(t, err)
	assert.Equal(t, "archived restore needle", task.Title)

	activeSearch, err := client.SearchWithRuntime(ctx, projectID, "restored issue search")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{issueID}, taskIDStrings(activeSearch))

	archivedOnly, err := client.ListSummariesWithRuntimeArchiveMode(ctx, projectID, ArchiveOnly)
	require.NoError(t, err)
	assert.NotContains(t, taskIDStrings(archivedOnly), issueID)
}

func TestClient_UnarchiveChildWithArchivedParentIsBlockedUnlessParentsIncluded(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Archived parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Archived child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))
	require.NoError(t, client.Archive(ctx, childID))
	require.NoError(t, client.Archive(ctx, parentID))

	err = client.Unarchive(ctx, childID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIssueHasArchivedParents)
	assert.Contains(t, err.Error(), "unarchive the parent first")

	result, err := client.UnarchiveWithOptions(ctx, childID, UnarchiveOptions{WithParents: true})
	require.NoError(t, err)
	assert.Equal(t, []string{parentID, childID}, result.UnarchivedIDs)

	active, err := client.ListSummariesWithRuntimeArchiveMode(ctx, "proj-unarchive-parents", ArchiveExclude)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{parentID, childID}, taskIDStrings(active))
}

func TestClient_UnarchiveCascadeChildrenRestoresArchivedSubtree(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	rootID, err := client.Create(ctx, CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Priority: domain.P1})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{Title: "Child", Type: domain.TypeTask, Priority: domain.P2})
	require.NoError(t, err)
	grandchildID, err := client.Create(ctx, CreateTaskParams{Title: "Grandchild", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, childID, rootID, "parent-child"))
	require.NoError(t, client.AddDependency(ctx, grandchildID, childID, "parent-child"))
	require.NoError(t, client.Archive(ctx, grandchildID))
	require.NoError(t, client.Archive(ctx, childID))
	require.NoError(t, client.Archive(ctx, rootID))

	result, err := client.UnarchiveWithOptions(ctx, rootID, UnarchiveOptions{CascadeChildren: true})
	require.NoError(t, err)
	assert.Equal(t, []string{rootID, childID, grandchildID}, result.UnarchivedIDs)

	active, err := client.ListSummariesWithRuntimeArchiveMode(ctx, "proj-unarchive-subtree", ArchiveExclude)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{rootID, childID, grandchildID}, taskIDStrings(active))
}

func TestClient_LinearSyncExternalRefsUseCanonicalOriginTable(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Synced Linear task",
		Description: "Body",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusInProgress,
	})
	require.NoError(t, err)

	syncedAt := time.Date(2026, time.June, 19, 10, 0, 0, 0, time.UTC)
	err = client.UpsertExternalRef(ctx, ExternalRef{
		Provider:           "linear",
		IssueID:            issueID,
		ExternalID:         "lin_opaque_key",
		ExternalIdentifier: "CHE-02091",
		ExternalURL:        "https://linear.app/acme/issue/CHE-02091",
		ExternalUpdatedAt:  syncedAt.Add(-time.Hour),
		LastSyncedAt:       syncedAt,
		LastSyncHash:       "hash-a",
		LastSyncPayload:    `{"title":"Synced Linear task","description":"Body","priority":3}`,
	})
	require.NoError(t, err)

	originRefs, err := client.ListExternalIssueRefs(ctx, issueID)
	require.NoError(t, err)
	require.Len(t, originRefs, 1)
	assert.Equal(t, "linear", originRefs[0].Provider)
	assert.Equal(t, "lin_opaque_key", originRefs[0].RemoteKey)
	assert.Equal(t, "CHE-02091", originRefs[0].DisplayKey)

	syncRefs, err := client.ListExternalRefs(ctx, "linear")
	require.NoError(t, err)
	require.Len(t, syncRefs, 1)
	assert.Equal(t, issueID, syncRefs[0].IssueID)
	assert.Equal(t, "lin_opaque_key", syncRefs[0].ExternalID)
	assert.Equal(t, "CHE-02091", syncRefs[0].ExternalIdentifier)
	assert.Equal(t, "hash-a", syncRefs[0].LastSyncHash)
	assert.Equal(t, `{"title":"Synced Linear task","description":"Body","priority":3}`, syncRefs[0].LastSyncPayload)
	assert.Equal(t, syncedAt, syncRefs[0].LastSyncedAt)

	task, err := client.GetWithRuntime(ctx, "proj", issueID)
	require.NoError(t, err)
	assert.Equal(t, "linear", task.Origin)
}

func TestClient_LinearSyncExternalRefsSynthesizeBaselineForLegacyOriginRows(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Legacy Linear task",
		Description: "Legacy body",
		Type:        domain.TypeTask,
		Priority:    domain.P1,
		Status:      domain.StatusOpen,
		Labels:      []string{"bug"},
	})
	require.NoError(t, err)

	_, err = client.UpsertExternalIssueRef(ctx, UpsertExternalIssueRefParams{
		IssueID:       issueID,
		Provider:      "linear",
		ProviderScope: "team:CHE",
		RemoteKey:     "lin_legacy_key",
		DisplayKey:    "CHE-02092",
	})
	require.NoError(t, err)

	syncRefs, err := client.ListExternalRefs(ctx, "linear")
	require.NoError(t, err)
	require.Len(t, syncRefs, 1)
	ref := syncRefs[0]
	assert.Equal(t, issueID, ref.IssueID)
	assert.Equal(t, "lin_legacy_key", ref.ExternalID)
	assert.Equal(t, "CHE-02092", ref.ExternalIdentifier)
	assert.NotEmpty(t, ref.LastSyncHash, "legacy origin rows should not trigger a blind first push")
	assert.JSONEq(t, `{"title":"Legacy Linear task","description":"Legacy body","priority":1}`, ref.LastSyncPayload)
}

func TestClient_MigratesLinearSyncRefsIntoCanonicalOriginTable(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Legacy sync row",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)
	now := time.Date(2026, time.June, 19, 11, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		INSERT INTO azedarach_external_issue_refs (
			provider,
			issue_id,
			external_id,
			external_identifier,
			external_url,
			last_synced_at,
			last_sync_hash,
			last_sync_payload
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, "linear", issueID, "lin_backfill", "CHE-02093", "https://linear.app/acme/issue/CHE-02093", now, "hash-backfill", `{"title":"Legacy sync row","description":"","priority":3}`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM issue_external_refs`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id = '0014_linear_sync_external_refs_backfill'`)
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	client = NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	originRefs, err := client.ListExternalIssueRefs(ctx, issueID)
	require.NoError(t, err)
	require.Len(t, originRefs, 1)
	assert.Equal(t, "linear", originRefs[0].Provider)
	assert.Equal(t, "lin_backfill", originRefs[0].RemoteKey)
	assert.Equal(t, "CHE-02093", originRefs[0].DisplayKey)

	syncRefs, err := client.ListExternalRefs(ctx, "linear")
	require.NoError(t, err)
	require.Len(t, syncRefs, 1)
	assert.Equal(t, "hash-backfill", syncRefs[0].LastSyncHash)
}

func TestClient_NormalizeProviderDisplayKeyIssueIDsMigratesDurableRefs(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	azID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Existing Az issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.Equal(t, "a", azID)

	db, err := client.dbHandle()
	require.NoError(t, err)

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		INSERT INTO issues (
			id,
			title,
			description,
			status,
			disposition,
			engagement,
			visibility,
			lifecycle_state,
			closed_outcome,
			review_state,
			priority,
			issue_type,
			created_at,
			updated_at,
			labels_json,
			implementations_json
		)
		VALUES (?, ?, ?, ?, 'ready', 'working', 'live', 'active', 'none', 'none', ?, ?, ?, ?, ?, ?)
	`, "CHE-02091", "Imported Linear issue", "legacy id", string(domain.StatusInProgress), int(domain.P1), string(domain.TypeTask), now, now, "[]", "[]")
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, ?, NULL), (?, ?, ?, NULL)
	`, "CHE-02091", "a", string(domain.DependencyBlocks), "a", "CHE-02091", string(domain.DependencyRelatedTo))
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO spec_requirements (local_id, title, issue_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "REQ-1", "Requirement", "CHE-02091", "draft", now, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO spec_links (issue_id, requirement_id, role, implementations_json, created_at, updated_at)
		VALUES (?, 1, ?, ?, ?, ?)
	`, "CHE-02091", "implements", "[]", now, now)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO issue_external_refs (
			issue_id,
			provider,
			provider_scope,
			remote_key,
			display_key,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "CHE-02091", "linear", "team:CHE", "lin_opaque_id", "CHE-02091", now, now)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "proj", "session-CHE-02091", "CHE-02091", "CHE-02091", "running", now, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, "proj", "CHE-02091", "/tmp/repo-CHE-02091", "riordan/che-02091/task", now)
	require.NoError(t, err)

	require.NoError(t, client.normalizeProviderDisplayKeyIssueIDs(ctx, db))

	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = 'CHE-02091'`).Scan(&count))
	assert.Equal(t, 0, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = 'b' AND title = 'Imported Linear issue'`).Scan(&count))
	assert.Equal(t, 1, count)

	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_dependencies WHERE issue_id = 'b' AND depends_on_id = 'a'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_dependencies WHERE issue_id = 'a' AND depends_on_id = 'b'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spec_requirements WHERE issue_id = 'b'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spec_links WHERE issue_id = 'b'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_projections WHERE issue_id = 'b'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_projections WHERE issue_id = 'b' AND scope_id = 'b'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_worktree_projections WHERE issue_id = 'b'`).Scan(&count))
	assert.Equal(t, 1, count)

	refs, err := client.ListExternalIssueRefs(ctx, "b")
	require.NoError(t, err)
	require.Len(t, refs, 2)
	remoteKeys := map[string]string{}
	for _, ref := range refs {
		remoteKeys[ref.RemoteKey] = ref.DisplayKey
		assert.Equal(t, "linear", ref.Provider)
		assert.Equal(t, "team:CHE", ref.ProviderScope)
	}
	assert.Equal(t, "CHE-02091", remoteKeys["lin_opaque_id"])
	assert.Equal(t, "CHE-02091", remoteKeys["CHE-02091"])

	var nextIndex string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, nextAlphaIssueIndexMetaKey).Scan(&nextIndex))
	assert.Equal(t, "2", nextIndex)
}

func intPtr(value int) *int {
	return &value
}

func TestClient_ListWithRuntimeReturnsJoinedProjectionFields(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-runtime"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime joined task",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	sessionID := "sess-runtime-1"
	startedAt := time.Date(2026, time.April, 4, 12, 0, 0, 0, time.UTC)
	updatedAt := startedAt.Add(2 * time.Minute)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, sessionID, taskID, taskID, "running", "no-agent", "session", startedAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)

	statusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:     true,
		GitAdditions:   7,
		GitDeletions:   3,
		GitAheadCount:  2,
		GitBehindCount: 1,
	})
	require.NoError(t, err)
	worktreePath := "/tmp/proj-runtime-" + taskID
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at, git_status_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, projectID, taskID, worktreePath, "riordan/"+taskID+"/task", updatedAt.Format(time.RFC3339Nano), string(statusRaw))
	require.NoError(t, err)

	tasks, err := client.ListWithRuntime(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	got := tasks[0]
	assert.Equal(t, naming.IssueID(taskID), got.ID)
	require.NotNil(t, got.Session)
	assert.Equal(t, domain.SessionBusy, got.Session.State)
	assert.Equal(t, "no-agent", got.Session.Activity)
	assert.Equal(t, "session", got.Session.ActivitySource)
	assert.Equal(t, worktreePath, got.Session.Worktree)
	require.NotNil(t, got.Session.StartedAt)
	assert.True(t, got.Session.StartedAt.Equal(startedAt))
	assert.True(t, got.HasWorktree)
	assert.True(t, got.HasUncommittedChanges)
	assert.Equal(t, 7, got.GitAdditions)
	assert.Equal(t, 3, got.GitDeletions)
	assert.Equal(t, 2, got.GitAheadCount)
	assert.Equal(t, 1, got.GitBehindCount)

	one, err := client.GetWithRuntime(ctx, projectID, taskID)
	require.NoError(t, err)
	assert.Equal(t, got.ID, one.ID)
	require.NotNil(t, one.Session)
	assert.Equal(t, "no-agent", one.Session.Activity)
	assert.Equal(t, "session", one.Session.ActivitySource)
	assert.Equal(t, worktreePath, one.Session.Worktree)
	assert.True(t, one.HasWorktree)
	assert.Equal(t, 7, one.GitAdditions)
}

func TestClient_SearchWithRuntimeUsesFTSIndexAndHydratesMatches(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-search-runtime"

	matchingID, err := client.Create(ctx, CreateTaskParams{
		Title:           "Runtime dashboard",
		Description:     "Cache evidence lives in the issue body",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Status:          domain.StatusInProgress,
		Labels:          []string{"observability"},
		Implementations: []string{"go-bubbletea"},
	})
	require.NoError(t, err)
	otherID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Runtime only",
		Description: "No matching storage term here",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	now := time.Date(2026, time.April, 4, 12, 0, 0, 0, time.UTC)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-search-runtime", matchingID, matchingID, "running", "busy", "session", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	require.NoError(t, err)

	tasks, err := client.SearchWithRuntime(ctx, projectID, "runtime cache")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(matchingID), tasks[0].ID)
	assert.Equal(t, "Cache evidence lives in the issue body", tasks[0].Description)
	require.NotNil(t, tasks[0].Session)
	assert.Equal(t, "busy", tasks[0].Session.Activity)

	tasks, err = client.SearchWithRuntime(ctx, projectID, "evidence")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(matchingID), tasks[0].ID)

	tasks, err = client.SearchWithRuntime(ctx, projectID, "go-bubbletea")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(matchingID), tasks[0].ID)

	tasks, err = client.SearchWithRuntime(ctx, projectID, "runtime missing")
	require.NoError(t, err)
	assert.Empty(t, tasks)

	tasks, err = client.SearchWithRuntime(ctx, projectID, otherID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(otherID), tasks[0].ID)
}

func TestClient_SearchWithRuntimeMaintainsFTSOnUpdateAndArchive(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-search-maintenance"

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Draft search task",
		Description: "initial text",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	})
	require.NoError(t, err)

	tasks, err := client.SearchWithRuntime(ctx, projectID, "needle")
	require.NoError(t, err)
	assert.Empty(t, tasks)

	err = client.UpdateDetails(ctx, issueID, UpdateTaskParams{
		Title:       "Draft search task",
		Description: "needle text",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	require.NoError(t, err)

	tasks, err = client.SearchWithRuntime(ctx, projectID, "needle")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(issueID), tasks[0].ID)

	require.NoError(t, client.Archive(ctx, issueID))

	tasks, err = client.SearchWithRuntime(ctx, projectID, "needle")
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestClient_ListWithRuntimeReadsSessionObservations(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-runtime-observation"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime observation task",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	startedAt := time.Date(2026, time.April, 4, 12, 30, 0, 0, time.UTC)
	updatedAt := startedAt.Add(time.Minute)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_observations (
			project_id, session_id, issue_id, scope_id, state, observed_state, activity, activity_source, started_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-runtime-observation.pane-535", taskID, taskID, "running", "running", "busy", "runtime", startedAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)

	tasks, err := client.ListWithRuntime(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NotNil(t, tasks[0].Session)
	assert.Equal(t, domain.SessionBusy, tasks[0].Session.State)
	assert.Equal(t, "busy", tasks[0].Session.Activity)
	assert.Equal(t, "runtime", tasks[0].Session.ActivitySource)
	assert.True(t, tasks[0].HasTmuxSession)
}

func TestClient_ListWithRuntimeReadsCanonicalHookActivityProjection(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-hook-pane-activity"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Hook pane activity task",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	startedAt := time.Date(2026, time.April, 4, 12, 30, 0, 0, time.UTC)
	hookUpdatedAt := startedAt.Add(5 * time.Minute)
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(client.dbPath, slog.Default())
	t.Cleanup(func() {
		_ = runtimeStore.Close()
	})
	started := startedAt
	err = runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        "az-" + taskID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateRunning,
		StartedAt: &started,
		UpdatedAt: hookUpdatedAt,
	})
	require.NoError(t, err)
	_, _, err = runtimeStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: "az-" + taskID, ObservedState: daemonstate.SessionStateRunning,
		Activity: "idle", ActivitySource: "hooks", UpdatedAt: hookUpdatedAt,
	})
	require.NoError(t, err)
	err = runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:             "az-" + taskID + ".pane-19",
		IssueID:        taskID,
		State:          daemonstate.SessionStatePaused,
		ObservedState:  daemonstate.SessionStatePaused,
		Activity:       "idle",
		ActivitySource: "hooks",
		StartedAt:      &started,
		UpdatedAt:      hookUpdatedAt,
	})
	require.NoError(t, err)

	tasks, err := client.ListWithRuntime(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NotNil(t, tasks[0].Session)
	assert.Equal(t, "idle", tasks[0].Session.Activity)
	assert.Equal(t, "hooks", tasks[0].Session.ActivitySource)
	assert.Equal(t, "idle", tasks[0].Session.DisplayLabel())

	got, err := client.GetWithRuntime(ctx, projectID, taskID)
	require.NoError(t, err)
	require.NotNil(t, got.Session)
	assert.Equal(t, "idle", got.Session.Activity)
	assert.Equal(t, "hooks", got.Session.ActivitySource)
	assert.Equal(t, "idle", got.Session.DisplayLabel())

	summaries, err := client.ListSummariesWithRuntime(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	require.NotNil(t, summaries[0].Session)
	assert.Equal(t, "idle", summaries[0].Session.Activity)
	assert.Equal(t, "hooks", summaries[0].Session.ActivitySource)
	assert.Equal(t, "idle", summaries[0].Session.DisplayLabel())

	metadata, err := client.GetManyMetadataWithRuntime(ctx, projectID, []string{taskID})
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	require.NotNil(t, metadata[0].Session)
	assert.Equal(t, "idle", metadata[0].Session.Activity)
	assert.Equal(t, "hooks", metadata[0].Session.ActivitySource)
	assert.Equal(t, "idle", metadata[0].Session.DisplayLabel())
}

func TestClient_ListSummariesWithRuntimeKeepsParentAndRuntimeProjection(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-summary-runtime"

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)
	blockerID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Child",
		Description: strings.Repeat("description ", 100),
		Design:      strings.Repeat("design ", 100),
		Notes:       strings.Repeat("notes ", 100),
		Acceptance:  strings.Repeat("acceptance ", 100),
		Type:        domain.TypeTask,
		Priority:    domain.P1,
		Status:      domain.StatusOpen,
		ParentID:    &parentID,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, childID, blockerID, string(domain.DependencyBlocks)))

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	now := time.Date(2026, time.April, 4, 12, 0, 0, 0, time.UTC)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-summary-runtime", childID, childID, "running", "idle", "hooks", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	require.NoError(t, err)

	statusRaw, err := json.Marshal(git.GitStatus{HasChanges: true, GitAdditions: 5, GitDeletions: 1})
	require.NoError(t, err)
	worktreePath := "/tmp/proj-summary-runtime-" + childID
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at, git_status_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, projectID, childID, worktreePath, "riordan/"+childID+"/task", now.Format(time.RFC3339Nano), string(statusRaw))
	require.NoError(t, err)

	tasks, err := client.ListSummariesWithRuntime(ctx, projectID)
	require.NoError(t, err)
	taskByID := map[string]domain.Task{}
	for _, task := range tasks {
		taskByID[task.ID.String()] = task
	}
	require.Contains(t, taskByID, childID)
	got := taskByID[childID]
	require.NotNil(t, got.ParentID)
	assert.Equal(t, parentID, got.ParentID.String())
	assert.Empty(t, got.Dependencies)
	require.NotNil(t, got.Session)
	assert.Equal(t, domain.SessionBusy, got.Session.State)
	assert.Equal(t, "idle", got.Session.Activity)
	assert.Equal(t, "hooks", got.Session.ActivitySource)
	assert.True(t, got.HasWorktree)
	assert.True(t, got.HasUncommittedChanges)
	assert.Equal(t, 5, got.GitAdditions)
	assert.Equal(t, 1, got.GitDeletions)
	assert.Empty(t, got.Description)
	assert.Empty(t, got.Design)
	assert.Empty(t, got.Notes)
	assert.Empty(t, got.Acceptance)

	tasks, err = client.ListSummariesWithRuntimeDependencies(ctx, projectID)
	require.NoError(t, err)
	taskByID = map[string]domain.Task{}
	for _, task := range tasks {
		taskByID[task.ID.String()] = task
	}
	got = taskByID[childID]
	require.NotNil(t, got.ParentID)
	assert.Equal(t, parentID, got.ParentID.String())
	require.Len(t, got.Dependencies, 1)
	assert.Equal(t, blockerID, got.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, got.Dependencies[0].Type)
}

func TestClient_HydrateRuntimePreservesDurableFieldsAndOverlaysProjection(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-hydrate-runtime"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "SQLite durable title",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)

	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(client.dbPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })

	updatedAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	require.NoError(t, runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      "/tmp/proj-hydrate-runtime-" + taskID,
		Branch:    "riordan/" + taskID + "/runtime",
		UpdatedAt: updatedAt,
	}))
	statusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:     true,
		GitAdditions:   8,
		GitDeletions:   3,
		GitAheadCount:  2,
		GitBehindCount: 1,
	})
	require.NoError(t, err)
	require.NoError(t, runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, statusRaw, updatedAt))

	cached := []domain.Task{{
		ID:               naming.IssueID(taskID),
		Title:            "cached durable title",
		Status:           domain.StatusInProgress,
		Type:             domain.TypeTask,
		Priority:         domain.P1,
		Labels:           []string{"cached"},
		Implementations:  []string{"worker-a"},
		Dependencies:     []domain.Dependency{{ID: "az-dep", Type: domain.DependencyBlocks}},
		RuntimeUpdatedAt: time.Now().UTC().Add(-time.Hour),
	}, {
		ID:                    "az-missing-runtime",
		Title:                 "cached missing task",
		Status:                domain.StatusOpen,
		Type:                  domain.TypeTask,
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          99,
		RuntimeUpdatedAt:      time.Now().UTC().Add(-time.Hour),
	}}

	hydrated, err := client.HydrateRuntime(ctx, projectID, cached)
	require.NoError(t, err)
	require.Len(t, hydrated, 2)

	got := hydrated[0]
	assert.Equal(t, "cached durable title", got.Title)
	assert.Equal(t, domain.StatusInProgress, got.Status)
	assert.Equal(t, domain.P1, got.Priority)
	assert.Equal(t, []string{"cached"}, got.Labels)
	require.Len(t, got.Dependencies, 1)
	assert.Equal(t, domain.DependencyBlocks, got.Dependencies[0].Type)
	assert.True(t, got.HasWorktree)
	assert.True(t, got.HasUncommittedChanges)
	assert.Equal(t, 8, got.GitAdditions)
	assert.Equal(t, 3, got.GitDeletions)
	assert.Equal(t, 2, got.GitAheadCount)
	assert.Equal(t, 1, got.GitBehindCount)
	assert.Truef(t, got.RuntimeUpdatedAt.Equal(updatedAt), "runtime updated_at = %v, want %v", got.RuntimeUpdatedAt, updatedAt)

	hydrated[0].Dependencies[0].Type = domain.DependencyRelatedTo
	assert.Equal(t, domain.DependencyBlocks, cached[0].Dependencies[0].Type)

	missing := hydrated[1]
	assert.Equal(t, "cached missing task", missing.Title)
	assert.False(t, missing.HasWorktree)
	assert.False(t, missing.HasUncommittedChanges)
	assert.Zero(t, missing.GitAdditions)
	assert.True(t, missing.RuntimeUpdatedAt.IsZero())
}

func TestClient_ListGraphReadinessWithRuntimeScopesToRootClosure(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-graph-readiness"

	rootID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Child",
		Description: strings.Repeat("description ", 100),
		Type:        domain.TypeTask,
		Priority:    domain.P1,
		Status:      domain.StatusOpen,
		ParentID:    &rootID,
	})
	require.NoError(t, err)
	grandchildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Grandchild",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &childID,
	})
	require.NoError(t, err)
	blockerID, err := client.Create(ctx, CreateTaskParams{
		Title:    "External blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, grandchildID, blockerID, string(domain.DependencyBlocks)))
	unrelatedRootID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Unrelated root",
		Type:     domain.TypeEpic,
		Priority: domain.P3,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	unrelatedChildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Unrelated child",
		Type:     domain.TypeTask,
		Priority: domain.P3,
		Status:   domain.StatusOpen,
		ParentID: &unrelatedRootID,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()
	now := time.Date(2026, time.June, 30, 11, 30, 0, 0, time.UTC)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-graph-readiness", childID, childID, "running", "busy", "hooks", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	require.NoError(t, err)

	tasks, err := client.ListGraphReadinessWithRuntime(ctx, projectID, rootID)
	require.NoError(t, err)
	taskByID := map[string]domain.Task{}
	for _, task := range tasks {
		taskByID[task.ID.String()] = task
	}

	for _, wantID := range []string{rootID, childID, grandchildID, blockerID} {
		require.Contains(t, taskByID, wantID)
	}
	require.NotContains(t, taskByID, unrelatedRootID)
	require.NotContains(t, taskByID, unrelatedChildID)
	require.NotNil(t, taskByID[childID].Session)
	assert.Equal(t, "busy", taskByID[childID].Session.Activity)
	assert.Empty(t, taskByID[childID].Description, "graph readiness should use summary rows")
	require.Len(t, taskByID[grandchildID].Dependencies, 1)
	assert.Equal(t, blockerID, taskByID[grandchildID].Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, taskByID[grandchildID].Dependencies[0].Type)
}

func TestClient_ListGraphReadinessWithRuntimeBoundsProjectCandidatesAndCountsAllOpen(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	for i := 0; i < 12; i++ {
		_, err := client.Create(ctx, CreateTaskParams{
			Title:    fmt.Sprintf("Candidate %02d", i),
			Type:     domain.TypeTask,
			Priority: domain.P1,
			Status:   domain.StatusOpen,
		})
		require.NoError(t, err)
	}

	tasks, err := client.ListGraphReadinessWithRuntime(ctx, "proj", "", 5)
	require.NoError(t, err)
	require.Len(t, tasks, 5)
	count, err := client.CountOpenOrchestrationIssues(ctx)
	require.NoError(t, err)
	assert.Equal(t, 12, count)
}

func TestClient_ListGraphReadinessWithRuntimeHydratesBoundedProjectContracts(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	_, err := client.Create(ctx, CreateTaskParams{
		Title:       "Candidate",
		Description: "Bounded scope",
		Acceptance:  "Focused test passes",
		Type:        domain.TypeTask,
		Priority:    domain.P1,
		Status:      domain.StatusOpen,
	})
	require.NoError(t, err)

	tasks, err := client.ListGraphReadinessWithRuntime(ctx, "project", "", 1)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Bounded scope", tasks[0].Description)
	assert.Equal(t, "Focused test passes", tasks[0].Acceptance)
}

func TestClient_ListParentChildSubtreeWithRuntimeScopesToTargetClosure(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-close-subtree"

	rootID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Root",
		Description: strings.Repeat("root detail ", 100),
		Type:        domain.TypeTask,
		Priority:    domain.P1,
		Status:      domain.StatusInReview,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	require.NoError(t, err)
	grandchildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Grandchild",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &childID,
	})
	require.NoError(t, err)
	blockerID, err := client.Create(ctx, CreateTaskParams{
		Title:    "External blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, grandchildID, blockerID, string(domain.DependencyBlocks)))
	unrelatedRootID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Unrelated root",
		Type:     domain.TypeTask,
		Priority: domain.P3,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	unrelatedChildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Unrelated child",
		Type:     domain.TypeTask,
		Priority: domain.P3,
		Status:   domain.StatusOpen,
		ParentID: &unrelatedRootID,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()
	now := time.Date(2026, time.June, 30, 12, 15, 0, 0, time.UTC)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-close-subtree", childID, childID, "running", "busy", "hooks", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	require.NoError(t, err)
	statusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:    true,
		GitAdditions:  3,
		GitAheadCount: 1,
	})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at, git_status_json, git_status_updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, projectID, grandchildID, "/tmp/proj-close-subtree-"+grandchildID, "riordan/"+grandchildID+"/task", now.Format(time.RFC3339Nano), string(statusRaw), now.Format(time.RFC3339Nano))
	require.NoError(t, err)

	tasks, err := client.ListParentChildSubtreeWithRuntime(ctx, projectID, rootID)
	require.NoError(t, err)
	taskByID := map[string]domain.Task{}
	for _, task := range tasks {
		taskByID[task.ID.String()] = task
	}

	for _, wantID := range []string{rootID, childID, grandchildID} {
		require.Contains(t, taskByID, wantID)
	}
	require.NotContains(t, taskByID, blockerID)
	require.NotContains(t, taskByID, unrelatedRootID)
	require.NotContains(t, taskByID, unrelatedChildID)
	assert.Empty(t, taskByID[rootID].Description, "close subtree read should use summary rows")
	require.NotNil(t, taskByID[childID].Session)
	assert.Equal(t, "busy", taskByID[childID].Session.Activity)
	assert.True(t, taskByID[grandchildID].HasWorktree)
	assert.True(t, taskByID[grandchildID].HasUncommittedChanges)
	assert.Equal(t, 3, taskByID[grandchildID].GitAdditions)
	assert.Equal(t, 1, taskByID[grandchildID].GitAheadCount)
	require.Len(t, taskByID[grandchildID].Dependencies, 1)
	assert.Equal(t, blockerID, taskByID[grandchildID].Dependencies[0].ID.String())
}

func TestGraphReadinessContextIDsQueryUsesClosureIndexes(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	query, args := graphReadinessContextIDsQuery("root")
	got := explainQueryPlan(t, ctx, db, query, args...)
	assert.Contains(t, got, "idx_issue_graph_closure_ancestor", got)
	assert.Contains(t, got, "idx_dependencies_issue_active_type", got)
	assert.NotContains(t, got, "SCAN child", got)
	assert.NotContains(t, got, "SCAN d", got)
}

func TestClient_ListWithRuntimeUsesObservedSessionState(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-runtime-observed"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Observed stopped session",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, observed_state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-runtime-stale", taskID, taskID, "running", "stopped", now, now)
	require.NoError(t, err)

	tasks, err := client.ListWithRuntime(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.False(t, tasks[0].HasTmuxSession)
	assert.Nil(t, tasks[0].Session)
}

func TestClient_GetManyWithDependencyContextRuntimeIncludesRequestedAndDirectContext(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-batch-context"

	firstID, err := client.Create(ctx, CreateTaskParams{
		Title:    "First",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Second",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)
	thirdID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Third",
		Type:     domain.TypeTask,
		Priority: domain.P3,
		Status:   domain.StatusInReview,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, secondID, firstID, string(domain.DependencyBlocks)))
	require.NoError(t, client.AddDependency(ctx, thirdID, secondID, string(domain.DependencyRelatedTo)))

	tasks, err := client.GetManyWithDependencyContextRuntime(ctx, projectID, []string{secondID, "missing", secondID})
	require.NoError(t, err)
	taskByID := map[string]domain.Task{}
	for _, task := range tasks {
		taskByID[task.ID.String()] = task
	}
	require.Contains(t, taskByID, firstID)
	require.Contains(t, taskByID, secondID)
	require.Contains(t, taskByID, thirdID)
	require.NotContains(t, taskByID, "missing")
	assert.Len(t, taskByID[secondID].Dependencies, 1)
	assert.Equal(t, firstID, taskByID[secondID].Dependencies[0].ID.String())
}

func TestClient_GetManyWithDependencyContextRuntimeIncludesAncestorContextWhenRequested(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-batch-ancestor-context"

	rootID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	directTasks, err := client.GetManyWithDependencyContextRuntime(ctx, projectID, []string{childID})
	require.NoError(t, err)
	directByID := map[string]domain.Task{}
	for _, task := range directTasks {
		directByID[task.ID.String()] = task
	}
	require.Contains(t, directByID, childID)
	require.Contains(t, directByID, parentID)
	require.NotContains(t, directByID, rootID)

	ancestorTasks, err := client.GetManyWithDependencyContextRuntime(ctx, projectID, []string{childID}, WithAncestorContext())
	require.NoError(t, err)
	ancestorByID := map[string]domain.Task{}
	for _, task := range ancestorTasks {
		ancestorByID[task.ID.String()] = task
	}
	require.Contains(t, ancestorByID, childID)
	require.Contains(t, ancestorByID, parentID)
	require.Contains(t, ancestorByID, rootID)
}

func TestClient_GetManyWithDependencyContextRuntimeCanOmitDependents(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-batch-no-dependents"

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	childIDs := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		childID, err := client.Create(ctx, CreateTaskParams{
			Title:    "Child",
			Type:     domain.TypeTask,
			Priority: domain.P2,
			Status:   domain.StatusOpen,
			ParentID: &parentID,
		})
		require.NoError(t, err)
		childIDs = append(childIDs, childID)
	}

	defaultTasks, err := client.GetManyWithDependencyContextRuntime(ctx, projectID, []string{parentID})
	require.NoError(t, err)
	defaultByID := map[string]domain.Task{}
	for _, task := range defaultTasks {
		defaultByID[task.ID.String()] = task
	}
	require.Contains(t, defaultByID, parentID)
	require.Contains(t, defaultByID, childIDs[0])

	leanTasks, err := client.GetManyWithDependencyContextRuntime(ctx, projectID, []string{parentID}, WithoutDependentContext())
	require.NoError(t, err)
	leanByID := map[string]domain.Task{}
	for _, task := range leanTasks {
		leanByID[task.ID.String()] = task
	}
	require.Contains(t, leanByID, parentID)
	for _, childID := range childIDs {
		require.NotContains(t, leanByID, childID)
	}
}

func TestClient_GetManyWithDependencyContextRuntimeCanLimitDependentsToParentChild(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-parent-child-dependents"

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	require.NoError(t, err)
	blockerID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, blockerID, parentID, string(domain.DependencyBlocks)))

	tasks, err := client.GetManyWithDependencyContextRuntime(ctx, projectID, []string{parentID}, WithParentChildDependentContext())
	require.NoError(t, err)
	taskByID := map[string]domain.Task{}
	for _, task := range tasks {
		taskByID[task.ID.String()] = task
	}

	require.Contains(t, taskByID, parentID)
	require.Contains(t, taskByID, childID)
	require.NotContains(t, taskByID, blockerID)
}

func TestClient_GetManyMetadataWithAncestorContextRuntimeIsLean(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-batch-metadata-ancestor"

	rootID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Root",
		Description: "root detail should not be loaded",
		Type:        domain.TypeEpic,
		Priority:    domain.P1,
		Status:      domain.StatusOpen,
		Labels:      []string{"expensive"},
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Child",
		Description: "child detail should not be loaded",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusInProgress,
		ParentID:    &rootID,
	})
	require.NoError(t, err)
	relatedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Related",
		Type:     domain.TypeTask,
		Priority: domain.P3,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, childID, relatedID, string(domain.DependencyRelatedTo)))

	tasks, err := client.GetManyMetadataWithAncestorContextRuntime(ctx, projectID, []string{childID})
	require.NoError(t, err)
	taskByID := map[string]domain.Task{}
	for _, task := range tasks {
		taskByID[task.ID.String()] = task
	}
	require.Contains(t, taskByID, childID)
	require.Contains(t, taskByID, rootID)
	require.NotContains(t, taskByID, relatedID)
	assert.Equal(t, "Child", taskByID[childID].Title)
	assert.Equal(t, domain.StatusInProgress, taskByID[childID].Status)
	assert.Empty(t, taskByID[childID].Description)
	assert.Empty(t, taskByID[childID].Labels)
	require.NotNil(t, taskByID[childID].ParentID)
	assert.Equal(t, rootID, taskByID[childID].ParentID.String())
}

func TestClient_GetManyMetadataWithRuntimeIncludesCachedGitProjection(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-metadata-runtime-git"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Metadata runtime git projection",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	statusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:     true,
		HasConflicts:   true,
		Conflicted:     []string{"conflict.go"},
		GitAdditions:   342,
		GitDeletions:   21,
		GitAheadCount:  1,
		GitBehindCount: 10,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	updatedAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-metadata-runtime", taskID, taskID, "running", "no-agent", "session", updatedAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at, git_status_json, git_status_updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, projectID, taskID, "/tmp/proj-metadata-runtime-git-"+taskID, "riordan/"+taskID+"/task", updatedAt.Format(time.RFC3339Nano), string(statusRaw), updatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)

	tasks, err := client.GetManyMetadataWithRuntime(ctx, projectID, []string{taskID})
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	got := tasks[0]
	require.NotNil(t, got.Session)
	assert.Equal(t, "no-agent", got.Session.Activity)
	assert.Equal(t, "session", got.Session.ActivitySource)
	assert.True(t, got.HasWorktree)
	assert.True(t, got.HasUncommittedChanges)
	assert.True(t, got.HasConflicts)
	assert.Equal(t, []string{"conflict.go"}, got.ConflictFiles)
	assert.Equal(t, 342, got.GitAdditions)
	assert.Equal(t, 21, got.GitDeletions)
	assert.Equal(t, 1, got.GitAheadCount)
	assert.Equal(t, 10, got.GitBehindCount)
	assert.Truef(t, got.Session.UpdatedAt.Equal(updatedAt), "session updated_at = %v, want %v", got.Session.UpdatedAt, updatedAt)
	assert.Truef(t, got.RuntimeUpdatedAt.Equal(updatedAt), "runtime updated_at = %v, want %v", got.RuntimeUpdatedAt, updatedAt)
}

func TestClient_MetadataRuntimeUpdatedAtIgnoresProjectionRefreshTimestamps(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-runtime-refresh"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Projection refresh should not look new",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	issueUpdatedAt := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Microsecond)
	projectionRefreshedAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	_, err = db.ExecContext(ctx, `
		UPDATE issues
		SET updated_at = ?
		WHERE id = ?
	`, issueUpdatedAt.Format(time.RFC3339Nano), taskID)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-runtime-refresh", taskID, taskID, "running", "idle", "hooks", projectionRefreshedAt.Format(time.RFC3339Nano), projectionRefreshedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, projectID, taskID, "/tmp/proj-runtime-refresh-"+taskID, "riordan/"+taskID+"/task", projectionRefreshedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)

	tasks, err := client.GetManyMetadataWithRuntime(ctx, projectID, []string{taskID})
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	got := tasks[0]
	require.NotNil(t, got.Session)
	assert.Truef(t, got.Session.UpdatedAt.Equal(projectionRefreshedAt), "session updated_at = %v, want raw projection %v", got.Session.UpdatedAt, projectionRefreshedAt)
	assert.Truef(t, got.RuntimeUpdatedAt.Equal(issueUpdatedAt), "runtime_updated_at = %v, want issue update %v", got.RuntimeUpdatedAt, issueUpdatedAt)
}

func TestTaskRuntimeProjectionQueryFiltersRuntimeCTEsForRequestedIDs(t *testing.T) {
	parallelIssueStoreTest(t)
	query, args := taskRuntimeProjectionQuery("proj-batch-context", true, " second ", "", "second", "third")

	assert.Equal(t, []any{
		"proj-batch-context",
		"second",
		"third",
		"second",
		"third",
		"proj-batch-context",
		"second",
		"third",
	}, args)
	assert.Equal(t, 2, strings.Count(query, "issue_id IN (?,?)"), query)
	assert.Contains(t, query, "i.id IN (?,?)")

	unfilteredQuery, unfilteredArgs := taskRuntimeProjectionQuery("proj-batch-context", false, ArchiveExclude)
	assert.Equal(t, []any{"proj-batch-context", "proj-batch-context"}, unfilteredArgs)
	assert.NotContains(t, unfilteredQuery, "issue_id IN")
	assert.NotContains(t, unfilteredQuery, "i.id IN")
}

func TestTaskRuntimeProjectionFilteredQueryUsesProjectionIndexes(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	query, args := taskRuntimeProjectionQuery("proj-batch-context", false, "second", "third")
	got := explainQueryPlan(t, ctx, db, query, args...)
	assert.Contains(t, got, "idx_daemon_session_projections_project_issue_updated", got)
	assert.Contains(t, got, "idx_daemon_session_observations_project_issue_updated", got)
	assert.Contains(t, got, "sqlite_autoindex_daemon_worktree_projections_1", got)
	assert.Contains(t, got, "idx_issue_external_refs_issue_active", got)
}

func TestTaskDependencyRowsParentOnlyQueryUsesActiveIssueTypeIndex(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	query, typeArgs := taskDependencyRowsQuery(2, taskDependencyLoadParentOnly)
	args := append([]any{"first", "second"}, typeArgs...)
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer rows.Close()

	plan := strings.Builder{}
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	require.NoError(t, rows.Err())

	got := plan.String()
	assert.Contains(t, query, "dependency_type IN (?, ?)")
	assert.Contains(t, got, "idx_dependencies_issue_active_type", got)
}

func TestClient_GetRuntimeWorktreeIssueContextScopesToRequestedIssuesAndAncestors(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	empty, err := client.GetRuntimeWorktreeIssueContext(ctx, "proj-runtime-context", nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	rootID, err := client.Create(ctx, CreateTaskParams{
		Title:  "Runtime root",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	require.NoError(t, err)
	rootIssueID := naming.IssueID(rootID)
	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime parent",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	require.NoError(t, err)
	parentIssueID := naming.IssueID(parentID)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	require.NoError(t, err)
	childIssueID := naming.IssueID(childID)
	unrelatedID, err := client.Create(ctx, CreateTaskParams{
		Title:  "Unrelated",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	require.NoError(t, err)
	unrelatedIssueID := naming.IssueID(unrelatedID)

	tasks, err := client.GetRuntimeWorktreeIssueContext(ctx, "proj-runtime-context", []string{childID})
	require.NoError(t, err)

	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	require.Len(t, byID, 3)
	assert.Contains(t, byID, rootIssueID)
	assert.Contains(t, byID, parentIssueID)
	assert.Contains(t, byID, childIssueID)
	assert.NotContains(t, byID, unrelatedIssueID)
	require.NotNil(t, byID[childIssueID].ParentID)
	assert.Equal(t, parentIssueID, *byID[childIssueID].ParentID)
	require.NotNil(t, byID[parentIssueID].ParentID)
	assert.Equal(t, rootIssueID, *byID[parentIssueID].ParentID)
}

func TestClient_GetRuntimeWorktreeIssueContextIncludesArchivedRequestedIssue(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:  "Archived worktree owner",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	require.NoError(t, err)
	require.NoError(t, client.Archive(ctx, issueID))

	tasks, err := client.GetRuntimeWorktreeIssueContext(ctx, "proj-runtime-context", []string{issueID})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(issueID), tasks[0].ID)
	assert.True(t, tasks[0].State.IsArchived())
}

func TestClient_OrdinaryMetadataRuntimeReadsExcludeArchivedIssues(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-runtime-context"

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:  "Archived ordinary metadata owner",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	require.NoError(t, err)
	require.NoError(t, client.Archive(ctx, issueID))

	metadata, err := client.GetManyMetadataWithRuntime(ctx, projectID, []string{issueID})
	require.NoError(t, err)
	assert.Empty(t, metadata)

	metadataWithAncestors, err := client.GetManyMetadataWithAncestorContextRuntime(ctx, projectID, []string{issueID})
	require.NoError(t, err)
	assert.Empty(t, metadataWithAncestors)

	archived, err := client.GetWithRuntimeArchiveMode(ctx, projectID, issueID, ArchiveOnly)
	require.NoError(t, err)
	archived.Origin = "cached-archived"
	hydrated, err := client.HydrateRuntime(ctx, projectID, []domain.Task{archived})
	require.NoError(t, err)
	require.Len(t, hydrated, 1)
	assert.Equal(t, "cached-archived", hydrated[0].Origin)
}

func TestSQLiteHotQueryPlansUseExpectedIndexes(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	tests := []struct {
		name        string
		build       func(t *testing.T) (string, []any)
		want        []string
		notWant     []string
		description string
	}{
		{
			name: "search candidate ids",
			build: func(t *testing.T) (string, []any) {
				t.Helper()
				return issueSearchIDsQuery(ArchiveExclude), []any{domain.ContentQueryFTSExpression("runtime cache")}
			},
			want: []string{
				"SCAN issue_search_fts VIRTUAL TABLE INDEX",
				"SEARCH i USING INTEGER PRIMARY KEY",
			},
			description: "search must use the FTS virtual table and rowid hydration instead of scanning issues",
		},
		{
			name: "dependency context ids",
			build: func(t *testing.T) (string, []any) {
				t.Helper()
				return dependencyContextIDsQuery([]string{"second", "third"}, dependencyContextOptions{includeDependents: true})
			},
			want: []string{
				"idx_dependencies_issue_active_type",
				"idx_dependencies_depends_on_active_type",
			},
			notWant: []string{
				"SCAN issue_dependencies",
			},
			description: "dependency context expansion must use both dependency-edge indexes",
		},
		{
			name: "parent ancestor ids",
			build: func(t *testing.T) (string, []any) {
				t.Helper()
				return parentAncestorIDsQuery([]string{"leaf"})
			},
			want: []string{
				"idx_issue_graph_closure_descendant",
			},
			notWant: []string{
				"SCAN closure",
			},
			description: "ancestor lookup must use the descendant-side graph closure index",
		},
		{
			name: "metadata runtime projection",
			build: func(t *testing.T) (string, []any) {
				t.Helper()
				return taskMetadataRuntimeProjectionQuery("project", ArchiveExclude, "second", "third")
			},
			want: []string{
				"idx_daemon_session_projections_project_issue_updated",
				"idx_daemon_session_observations_project_issue_updated",
				"sqlite_autoindex_daemon_worktree_projections_1",
				"idx_dependencies_issue_active_type",
			},
			description: "metadata runtime projection must stay lean and use runtime/dependency projection indexes",
		},
		{
			name: "unfiltered runtime projection",
			build: func(t *testing.T) (string, []any) {
				t.Helper()
				return taskRuntimeProjectionQuery("project", false, ArchiveExclude)
			},
			want: []string{
				"idx_daemon_session_projections_project_issue_updated",
				"idx_daemon_session_observations_project_issue_updated",
				"idx_issues_deleted_updated",
			},
			description: "board snapshot reads may scan active issues through the deleted/updated index, not the table",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, args := tt.build(t)
			require.NotEmpty(t, strings.TrimSpace(query))
			got := explainQueryPlan(t, ctx, db, query, args...)
			for _, want := range tt.want {
				assert.Containsf(t, got, want, "%s\nplan:\n%s", tt.description, got)
			}
			for _, notWant := range tt.notWant {
				assert.NotContainsf(t, got, notWant, "%s\nplan:\n%s", tt.description, got)
			}
		})
	}
}

func TestClient_SQLiteReadLogsIncludeStableAttribution(t *testing.T) {
	parallelIssueStoreTest(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClientWithLogger(t, logger)

	_, err := client.Create(context.Background(), CreateTaskParams{
		Title:    "Logged runtime read",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)

	_, err = client.ListWithRuntime(context.Background(), "project")
	require.NoError(t, err)

	got := logs.String()
	assert.Contains(t, got, `"event":"sqlite.query.completed"`)
	assert.Contains(t, got, `"service":"azedarach.issue_store"`)
	assert.Contains(t, got, `"dependency.name":"sqlite"`)
	assert.Contains(t, got, `"dependency.operation":"issue.runtime_projection"`)
	assert.Contains(t, got, `"dependency.duration_ms":`)
	assert.Contains(t, got, `"outcome":"success"`)
	assert.Contains(t, got, `"row_count":1`)
}

func TestClient_UpdateWithRuntimeReturnsChangedTask(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-update-runtime"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime update",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)

	task, err := client.UpdateWithRuntime(ctx, projectID, taskID, domain.StatusInProgress)
	require.NoError(t, err)
	assert.Equal(t, naming.IssueID(taskID), task.ID)
	assert.Equal(t, domain.StatusInProgress, task.Status)

	task, err = client.UpdateDetailsWithRuntime(ctx, projectID, taskID, UpdateTaskParams{
		Title:       "Runtime update changed",
		Description: "Changed through returning API",
		Type:        domain.TypeBug,
		Priority:    domain.P1,
	})
	require.NoError(t, err)
	assert.Equal(t, "Runtime update changed", task.Title)
	assert.Equal(t, domain.TypeBug, task.Type)
	assert.Equal(t, domain.P1, task.Priority)

	runtimeNotes := "Runtime replacement notes"
	task, err = client.UpdateDetailsWithRuntime(ctx, projectID, taskID, UpdateTaskParams{
		Title:       "Runtime update changed",
		Description: "Changed through returning API",
		Notes:       &runtimeNotes,
		Type:        domain.TypeBug,
		Priority:    domain.P1,
	})
	require.NoError(t, err)
	assert.Equal(t, "Runtime replacement notes", task.Notes)
}

func TestClientCloseWithRuntimeAtomicallyReleasesExecutionLeaseBeforeTerminalWrite(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-close-runtime"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Atomically close leased issue",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	require.NoError(t, err)
	_, err = client.ClaimOwnershipWithRuntime(ctx, projectID, taskID, OwnershipClaimParams{
		OwnerID:   "worker-a",
		OwnerKind: "agent",
		Purpose:   domain.CoordinationLeaseExecution,
	})
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TRIGGER fail_terminal_close
		BEFORE UPDATE ON issues
		WHEN NEW.lifecycle_state = 'closed'
		BEGIN
			SELECT RAISE(ABORT, 'injected terminal status write failure');
		END`)
	require.NoError(t, err)

	_, err = client.CloseWithRuntime(ctx, projectID, taskID, domain.StatusDone)
	require.ErrorContains(t, err, "injected terminal status write failure")
	afterFailure, err := client.GetWithRuntime(ctx, projectID, taskID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusInReview, afterFailure.Status)
	require.NotNil(t, afterFailure.Ownership, "failed terminal transaction must roll back execution lease release")
	assert.Equal(t, "worker-a", afterFailure.Ownership.OwnerID)

	_, err = db.ExecContext(ctx, `DROP TRIGGER fail_terminal_close`)
	require.NoError(t, err)
	closed, err := client.CloseWithRuntime(ctx, projectID, taskID, domain.StatusDone)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusDone, closed.Status)
	assert.Nil(t, closed.Ownership)
	assert.Empty(t, closed.CoordinationLeases)

	var releaseEventID, statusEventID int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM issue_observation_events
		WHERE issue_id = ? AND event_type = ? AND json_extract(payload_json, '$.reason') = 'terminal_close'`,
		taskID, domain.IssueEventIssueOwnershipChanged).Scan(&releaseEventID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT MAX(id) FROM issue_observation_events
		WHERE issue_id = ? AND event_type = ?`, taskID, domain.IssueEventIssueStatusChanged).Scan(&statusEventID))
	assert.Less(t, releaseEventID, statusEventID, "lease release event must precede terminal status event")
}

func TestClientUpdateDetailsReopensAtomicallyAndIdempotently(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-reopen-runtime"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Reopen terminal issue",
		Type:     domain.TypeBug,
		Priority: domain.P1,
		Status:   domain.StatusInReview,
	})
	require.NoError(t, err)
	_, err = client.ClaimOwnershipWithRuntime(ctx, projectID, taskID, OwnershipClaimParams{
		OwnerID:   "worker-a",
		OwnerKind: "agent",
		Purpose:   domain.CoordinationLeaseExecution,
	})
	require.NoError(t, err)
	closed, err := client.CloseWithRuntime(ctx, projectID, taskID, domain.StatusDone)
	require.NoError(t, err)
	assert.Nil(t, closed.Ownership)
	assert.Empty(t, closed.CoordinationLeases)

	db, err := client.dbHandle()
	require.NoError(t, err)
	var closedAt sql.NullString
	var closedOutcome, visibility string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT closed_at, closed_outcome, visibility FROM issues WHERE id = ?`, taskID).Scan(&closedAt, &closedOutcome, &visibility))
	require.True(t, closedAt.Valid)
	assert.Equal(t, string(domain.IssueCloseCompleted), closedOutcome)
	assert.Equal(t, string(domain.IssueVisibilityLive), visibility)

	_, err = db.ExecContext(ctx, `CREATE TRIGGER fail_reopen
		BEFORE INSERT ON issue_observation_events
		WHEN NEW.event_type = 'issue.status_changed'
			AND json_extract(NEW.payload_json, '$.from_status') = 'closed'
			AND json_extract(NEW.payload_json, '$.to_status') = 'open'
		BEGIN
			SELECT RAISE(ABORT, 'injected reopen failure');
		END`)
	require.NoError(t, err)
	openLifecycle := domain.IssueWorkflowOpen
	update := UpdateTaskParams{
		Title:     "Reopen terminal issue",
		Type:      domain.TypeBug,
		Priority:  domain.P1,
		Lifecycle: &openLifecycle,
	}
	require.ErrorContains(t, client.UpdateDetails(ctx, taskID, update), "injected reopen failure")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT closed_at, closed_outcome FROM issues WHERE id = ?`, taskID).Scan(&closedAt, &closedOutcome))
	require.True(t, closedAt.Valid, "failed reopen must preserve terminal timestamp")
	assert.Equal(t, string(domain.IssueCloseCompleted), closedOutcome)
	var reopenEvents int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events
		WHERE issue_id = ? AND event_type = ?
		AND json_extract(payload_json, '$.from_status') = ?
		AND json_extract(payload_json, '$.to_status') = ?`,
		taskID, domain.IssueEventIssueStatusChanged, domain.StatusDone, domain.StatusOpen).Scan(&reopenEvents))
	assert.Zero(t, reopenEvents, "failed reopen must roll back lifecycle history")

	_, err = db.ExecContext(ctx, `DROP TRIGGER fail_reopen`)
	require.NoError(t, err)
	reopened, err := client.UpdateDetailsWithRuntime(ctx, projectID, taskID, update)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusOpen, reopened.Status)
	assert.Equal(t, domain.IssueWorkflowOpen, reopened.State.Workflow())
	assert.Nil(t, reopened.Ownership, "reopen must not recreate terminally released ownership")
	assert.Empty(t, reopened.CoordinationLeases)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT closed_at, closed_outcome FROM issues WHERE id = ?`, taskID).Scan(&closedAt, &closedOutcome))
	assert.False(t, closedAt.Valid)
	assert.Equal(t, string(domain.IssueCloseNone), closedOutcome)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events
		WHERE issue_id = ? AND event_type = ?
		AND json_extract(payload_json, '$.from_status') = ?
		AND json_extract(payload_json, '$.to_status') = ?`,
		taskID, domain.IssueEventIssueStatusChanged, domain.StatusDone, domain.StatusOpen).Scan(&reopenEvents))
	assert.Equal(t, 1, reopenEvents)

	require.NoError(t, client.UpdateDetails(ctx, taskID, update), "idempotent reopen retry must succeed")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events
		WHERE issue_id = ? AND event_type = ?
		AND json_extract(payload_json, '$.from_status') = ?
		AND json_extract(payload_json, '$.to_status') = ?`,
		taskID, domain.IssueEventIssueStatusChanged, domain.StatusDone, domain.StatusOpen).Scan(&reopenEvents))
	assert.Equal(t, 1, reopenEvents, "idempotent retry must not duplicate lifecycle history")
}

func TestClient_AppendNotes(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Attachment notes",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AppendNotes(ctx, taskID, "📎 [one.png](.azedarach/images/axu/one.png)"))
	require.NoError(t, client.AppendNotes(ctx, taskID, "📎 [two.png](.azedarach/images/axu/two.png)"))

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	var notes string
	err = db.QueryRowContext(ctx, "SELECT COALESCE(notes, '') FROM issues WHERE id = ?", taskID).Scan(&notes)
	require.NoError(t, err)
	assert.Equal(t, "📎 [one.png](.azedarach/images/axu/one.png)\n📎 [two.png](.azedarach/images/axu/two.png)", notes)
}

func TestClient_IssueObservationEventsRecordIssueMutations(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Observable task",
		Type:     domain.TypeFeature,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, taskID, domain.StatusInProgress))
	require.NoError(t, client.AppendNotes(ctx, taskID, "first evidence"))
	require.NoError(t, client.UpdateDetails(ctx, taskID, UpdateTaskParams{
		Title:    "Observable task updated",
		Type:     domain.TypeBug,
		Priority: domain.P1,
	}))
	require.NoError(t, client.Archive(ctx, taskID))
	require.NoError(t, client.Unarchive(ctx, taskID))

	events, err := client.ListIssueObservationEvents(ctx, taskID, IssueObservationEventListOptions{})
	require.NoError(t, err)
	require.Len(t, events, 6)
	assert.Equal(t, domain.IssueEventIssueCreated, events[0].Type)
	assert.Equal(t, "issue-store", events[0].Source)
	assert.Equal(t, "Observable task", events[0].Payload["title"])
	assert.Equal(t, domain.IssueEventIssueStatusChanged, events[1].Type)
	assert.Equal(t, "open", events[1].Payload["from_status"])
	assert.Equal(t, "in_progress", events[1].Payload["to_status"])
	assert.Equal(t, domain.IssueEventIssueNotesAppended, events[2].Type)
	assert.Equal(t, "first evidence", events[2].Payload["line"])
	assert.Equal(t, domain.IssueEventIssueDetailsChanged, events[3].Type)
	assert.Contains(t, events[3].Payload["changed_fields"], "title")
	assert.Equal(t, domain.IssueEventIssueArchived, events[4].Type)
	assert.Equal(t, domain.IssueEventIssueUnarchived, events[5].Type)

	filtered, err := client.ListIssueObservationEvents(ctx, taskID, IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{domain.IssueEventIssueStatusChanged},
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, domain.IssueEventIssueStatusChanged, filtered[0].Type)
}

func TestClient_IssueOwnershipClaimConflictReleaseAndExpiredTakeover(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Owned work",
		Type:     domain.TypeFeature,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	claimed, err := client.ClaimOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID:   "agent-a",
		OwnerKind: "agent",
		TTL:       time.Hour,
	})
	require.NoError(t, err)
	require.NotNil(t, claimed.Ownership)
	assert.Equal(t, "agent-a", claimed.Ownership.OwnerID)
	assert.Equal(t, "agent", claimed.Ownership.OwnerKind)
	require.NotNil(t, claimed.Ownership.ExpiresAt)
	assert.True(t, claimed.Ownership.IsActive(time.Now().UTC()))

	_, err = client.ClaimOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "agent-b",
	})
	require.ErrorIs(t, err, domain.ErrConflict)

	_, err = client.ReleaseOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "agent-b",
	})
	require.ErrorIs(t, err, domain.ErrConflict)

	released, err := client.ReleaseOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "agent-a",
	})
	require.NoError(t, err)
	assert.Nil(t, released.Ownership)

	expiredClaim, err := client.ClaimOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "agent-a",
		TTL:     time.Hour,
	})
	require.NoError(t, err)
	require.NotNil(t, expiredClaim.Ownership)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `UPDATE issue_coordination_leases SET expires_at = ? WHERE issue_id = ? AND purpose = 'execution'`, past, taskID)
	require.NoError(t, err)

	takenOver, err := client.ClaimOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID:   "agent-b",
		OwnerKind: "orchestrator",
	})
	require.NoError(t, err)
	require.NotNil(t, takenOver.Ownership)
	assert.Equal(t, "agent-b", takenOver.Ownership.OwnerID)
	assert.Equal(t, "orchestrator", takenOver.Ownership.OwnerKind)
	assert.Nil(t, takenOver.Ownership.ExpiresAt)

	events, err := client.ListIssueObservationEvents(ctx, taskID, IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{domain.IssueEventIssueOwnershipChanged},
	})
	require.NoError(t, err)
	require.Len(t, events, 4)
	assert.Equal(t, "claimed", events[0].Payload["action"])
	assert.Equal(t, "agent-a", events[0].Payload["owner_id"])
	assert.Equal(t, "released", events[1].Payload["action"])
	assert.Equal(t, "agent-a", events[2].Payload["owner_id"])
	assert.Equal(t, "agent-b", events[3].Payload["owner_id"])
}

func TestClient_CoordinationLeasesArePurposeScopedAndPreserveExecutionOwner(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })
	taskID, err := client.Create(ctx, CreateTaskParams{Title: "scoped leases", Type: domain.TypeTask, Priority: domain.P1})
	require.NoError(t, err)

	worker, err := client.ClaimOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "worker-a", OwnerKind: "agent", Purpose: domain.CoordinationLeaseExecution,
	})
	require.NoError(t, err)
	require.NotNil(t, worker.Ownership)
	assert.Equal(t, "worker-a", worker.Ownership.OwnerID)
	require.NoError(t, client.Update(ctx, taskID, domain.StatusInReview))

	reviewed, err := client.ClaimOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "reviewer-a", OwnerKind: "agent", Purpose: domain.CoordinationLeaseReview,
	})
	require.NoError(t, err)
	require.NotNil(t, reviewed.Ownership)
	assert.Equal(t, "worker-a", reviewed.Ownership.OwnerID, "review must not overwrite execution ownership")
	require.Len(t, reviewed.CoordinationLeases, 2)

	_, err = client.ClaimOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "reviewer-b", Purpose: domain.CoordinationLeaseReview,
	})
	require.ErrorIs(t, err, domain.ErrConflict)

	handback, err := client.ReleaseOwnershipWithRuntime(ctx, "project-1", taskID, OwnershipClaimParams{
		OwnerID: "reviewer-a", Purpose: domain.CoordinationLeaseReview,
	})
	require.NoError(t, err)
	require.NotNil(t, handback.Ownership)
	assert.Equal(t, "worker-a", handback.Ownership.OwnerID)
	require.Len(t, handback.CoordinationLeases, 1)
	assert.Equal(t, domain.CoordinationLeaseExecution, handback.CoordinationLeases[0].Purpose)
}

func TestClient_AppendIssueObservationEventRecordsSourceMetadata(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "External observation",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	observedAt := time.Date(2026, 7, 6, 2, 30, 0, 0, time.UTC)
	event, err := client.AppendIssueObservationEvent(ctx, taskID, IssueObservationEventParams{
		Type:          domain.IssueEventValidationPassed,
		ObservedAt:    observedAt,
		Source:        "cli",
		SourceCommand: "az validate",
		OperationID:   "op-1",
		SessionID:     "sess-1",
		WorktreePath:  "/tmp/worktree",
		Payload: map[string]any{
			"command": "go test ./...",
			"status":  "passed",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, domain.IssueEventValidationPassed, event.Type)
	assert.Equal(t, observedAt, event.ObservedAt)
	assert.Equal(t, "cli", event.Source)
	assert.Equal(t, "az validate", event.SourceCommand)
	assert.Equal(t, "op-1", event.OperationID)
	assert.Equal(t, "sess-1", event.SessionID)
	assert.Equal(t, "/tmp/worktree", event.WorktreePath)
	assert.Equal(t, "go test ./...", event.Payload["command"])

	filtered, err := client.ListIssueObservationEvents(ctx, taskID, IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{domain.IssueEventValidationPassed},
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, event.ID, filtered[0].ID)
}

func TestClient_ReviewLeaseFenceIsScopedToCurrentReviewRequestEpochAcrossClients(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reviewer := newTestClientAtPath(t, path, slog.Default())
	competitor := newTestClientAtPath(t, path, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, reviewer.CloseDB())
		require.NoError(t, competitor.CloseDB())
	})
	issueID, err := reviewer.Create(ctx, CreateTaskParams{Title: "cross-daemon accepted review", Type: domain.TypeTask, Status: domain.StatusInReview})
	require.NoError(t, err)
	_, err = competitor.GetWithRuntime(ctx, "project", issueID)
	require.NoError(t, err)
	_, err = reviewer.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-a", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview})
	require.NoError(t, err)
	_, err = reviewer.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type:          domain.IssueEventReviewCompleted,
		Source:        "daemon-orchestration",
		SourceCommand: "review-accept",
		Payload:       map[string]any{"outcome": "accepted", "actor_id": "reviewer-a"},
	})
	require.NoError(t, err)
	_, err = reviewer.ReleaseOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-a", Purpose: domain.CoordinationLeaseReview})
	require.NoError(t, err)

	_, err = competitor.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-b", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview})
	require.ErrorIs(t, err, domain.ErrConflict)
	assert.Contains(t, err.Error(), "durable accepted review is awaiting authoritative close")
	_, err = reviewer.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type:          domain.IssueEventIssueStatusChanged,
		Source:        "az issue record",
		SourceCommand: "az issue record",
		Payload:       map[string]any{"to_status": "in_review"},
	})
	require.NoError(t, err)
	_, err = competitor.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-b", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview})
	require.ErrorIs(t, err, domain.ErrConflict)
	assert.Contains(t, err.Error(), "durable accepted review is awaiting authoritative close")

	require.NoError(t, reviewer.Update(ctx, issueID, domain.StatusDone))
	require.NoError(t, reviewer.Update(ctx, issueID, domain.StatusOpen))
	require.NoError(t, reviewer.Update(ctx, issueID, domain.StatusInReview))
	lease, err := competitor.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-b", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview})
	require.NoError(t, err)
	require.Len(t, lease.CoordinationLeases, 1)
	assert.Equal(t, domain.CoordinationLeaseReview, lease.CoordinationLeases[0].Purpose)

	_, err = reviewer.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type:          domain.IssueEventReviewCompleted,
		Source:        "daemon-orchestration",
		SourceCommand: "review-accept",
		Payload:       map[string]any{"outcome": "accepted", "actor_id": "reviewer-b"},
	})
	require.NoError(t, err)
	_, err = competitor.ReleaseOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-b", Purpose: domain.CoordinationLeaseReview})
	require.NoError(t, err)
	_, err = reviewer.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-a", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview})
	require.ErrorIs(t, err, domain.ErrConflict)

	_, err = reviewer.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type:          domain.IssueEventReviewCompleted,
		Source:        "daemon-orchestration",
		SourceCommand: "review-accept",
		Payload:       map[string]any{"outcome": "integration_failed", "actor_id": "reviewer-b"},
	})
	require.NoError(t, err)
	lease, err = reviewer.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "reviewer-a", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview})
	require.NoError(t, err)
	require.Len(t, lease.CoordinationLeases, 1)
	assert.Equal(t, domain.CoordinationLeaseReview, lease.CoordinationLeases[0].Purpose)
}

func TestClient_ListIssueObservationEventsNewestFirst(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Ordered observation",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	first, err := client.AppendIssueObservationEvent(ctx, taskID, IssueObservationEventParams{
		Type:       domain.IssueEventValidationPassed,
		ObservedAt: time.Date(2026, 7, 6, 1, 0, 0, 0, time.UTC),
		Source:     "test",
	})
	require.NoError(t, err)
	second, err := client.AppendIssueObservationEvent(ctx, taskID, IssueObservationEventParams{
		Type:       domain.IssueEventEvidenceSubmitted,
		ObservedAt: time.Date(2026, 7, 6, 2, 0, 0, 0, time.UTC),
		Source:     "test",
	})
	require.NoError(t, err)
	olderInsertedLast, err := client.AppendIssueObservationEvent(ctx, taskID, IssueObservationEventParams{
		Type:       domain.IssueEventReviewCompleted,
		ObservedAt: time.Date(2026, 7, 6, 0, 30, 0, 0, time.UTC),
		Source:     "test",
	})
	require.NoError(t, err)

	events, err := client.ListIssueObservationEvents(ctx, taskID, IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{
			domain.IssueEventValidationPassed,
			domain.IssueEventEvidenceSubmitted,
			domain.IssueEventReviewCompleted,
		},
		Limit:       3,
		NewestFirst: true,
	})
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, second.ID, events[0].ID)
	assert.Equal(t, first.ID, events[1].ID)
	assert.Equal(t, olderInsertedLast.ID, events[2].ID)
}

func TestClient_IssueObservationEventsSurviveHardDelete(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete audit",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.Delete(ctx, taskID))

	events, err := client.ListIssueObservationEvents(ctx, taskID, IssueObservationEventListOptions{})
	require.NoError(t, err)
	eventTypes := make([]domain.IssueObservationEventType, 0, len(events))
	for _, event := range events {
		eventTypes = append(eventTypes, event.Type)
	}
	assert.Contains(t, eventTypes, domain.IssueEventIssueCreated)
	assert.Contains(t, eventTypes, domain.IssueEventIssueDeleted)
}

func TestClient_CreateWithParentDependency(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent epic",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child task",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	tasks, err := client.Search(ctx, childID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NotNil(t, tasks[0].ParentID)
	assert.Equal(t, naming.IssueID(parentID), *tasks[0].ParentID)
}

func TestClient_CreateWithOpenChildReopensClosedParent(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent issue",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	_, err = client.Create(ctx, CreateTaskParams{
		Title:    "Open child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusInProgress, parentTasks[0].Status)
}

func TestClient_UpdatePreventsClosingParentWithOpenChildren(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent issue",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	_, err = client.Create(ctx, CreateTaskParams{
		Title:    "Open child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	err = client.Update(ctx, parentID, domain.StatusDone)
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "update", storeErr.Op)
	assert.Equal(t, parentID, storeErr.TaskID)
	assert.ErrorIs(t, storeErr.Err, domain.ErrConflict)

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusOpen, parentTasks[0].Status)
}

func TestClient_UpdateAllowsClosingParentWhenChildrenAreClosed(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent issue",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	require.NoError(t, client.Update(ctx, childID, domain.StatusDone))
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusDone, parentTasks[0].Status)
}

func TestClient_AddAndRemoveDependency(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	blockerID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	blockedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocked",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	err = client.AddDependency(ctx, blockedID, blockerID, "blocks")
	require.NoError(t, err)

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	var blockedTask *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found", blockedID)
	}
	require.Len(t, blockedTask.Dependencies, 1)
	assert.Equal(t, blockerID, blockedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, blockedTask.Dependencies[0].Type)

	err = client.RemoveDependency(ctx, blockedID, blockerID, "blocks")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDependencyRemovalConfirmationRequired)

	err = client.RemoveDependency(WithDependencyRemovalConfirmation(ctx), blockedID, blockerID, "blocks")
	require.NoError(t, err)

	tasks, err = client.List(ctx)
	require.NoError(t, err)
	blockedTask = nil
	for i := range tasks {
		if tasks[i].ID.String() == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found after remove", blockedID)
	}
	assert.Empty(t, blockedTask.Dependencies)
}

func TestClient_RemoveDependencyConfirmationIsNotRequiredForRelatedEdges(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	relatedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Related",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, relatedID, sourceID, "related"))
	require.NoError(t, client.RemoveDependency(ctx, relatedID, sourceID, "related"))

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var relatedTask *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == relatedID {
			relatedTask = &tasks[i]
			break
		}
	}
	if relatedTask == nil {
		t.Fatalf("related task %s not found after remove", relatedID)
	}
	assert.Empty(t, relatedTask.Dependencies)
}

func TestClient_RemoveParentChildActiveChildRequiresParentOrphanConfirmation(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Active child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))

	err = client.RemoveDependency(WithDependencyRemovalConfirmation(ctx), childID, parentID, "parent-child")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrParentChildOrphanConfirmationRequired)

	confirmedCtx := WithParentChildOrphanConfirmation(WithDependencyRemovalConfirmation(ctx))
	require.NoError(t, client.RemoveDependency(confirmedCtx, childID, parentID, "parent-child"))
}

func TestClient_RemoveParentChildRuntimeChildRequiresParentOrphanConfirmation(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-parent-orphan"

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, projectID, childID, "/tmp/"+childID, "riordan/"+childID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = client.RemoveDependencyWithRuntime(WithDependencyRemovalConfirmation(ctx), projectID, childID, parentID, "parent-child")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrParentChildOrphanConfirmationRequired)

	confirmedCtx := WithParentChildOrphanConfirmation(WithDependencyRemovalConfirmation(ctx))
	_, err = client.RemoveDependencyWithRuntime(confirmedCtx, projectID, childID, parentID, "parent-child")
	require.NoError(t, err)
}

func TestClient_AddDependencyCanonicalizesLegacyAliasesOnNonEpicTasks(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	blockedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocked",
		Type:     domain.TypeFeature,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	relatedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Related",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	createdID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Created",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, blockedID, sourceID, "blocked-by"))
	require.NoError(t, client.AddDependency(ctx, relatedID, sourceID, "related"))
	require.NoError(t, client.AddDependency(ctx, createdID, sourceID, "created-by"))

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var blockedTask, relatedTask, createdTask *domain.Task
	for i := range tasks {
		switch tasks[i].ID.String() {
		case blockedID:
			blockedTask = &tasks[i]
		case relatedID:
			relatedTask = &tasks[i]
		case createdID:
			createdTask = &tasks[i]
		}
	}

	if blockedTask == nil {
		t.Fatalf("blocked task %s not found", blockedID)
	}
	if relatedTask == nil {
		t.Fatalf("related task %s not found", relatedID)
	}

	require.Len(t, blockedTask.Dependencies, 1)
	assert.Equal(t, sourceID, blockedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, blockedTask.Dependencies[0].Type)

	require.Len(t, relatedTask.Dependencies, 1)
	assert.Equal(t, sourceID, relatedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyRelatedTo, relatedTask.Dependencies[0].Type)

	require.NotNil(t, createdTask)
	require.Len(t, createdTask.Dependencies, 1)
	assert.Equal(t, sourceID, createdTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyCreatedIn, createdTask.Dependencies[0].Type)
}

func TestClient_AddDependencyPreventsDuplicateEdges(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	blockedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocked",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, blockedID, sourceID, "blocks"))
	require.NoError(t, client.AddDependency(ctx, blockedID, sourceID, "blocks"))

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var blockedTask *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found", blockedID)
	}

	require.Len(t, blockedTask.Dependencies, 1)
	assert.Equal(t, sourceID, blockedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, blockedTask.Dependencies[0].Type)
}

func TestClient_AddParentChildDependencyReopensClosedParentForOpenChild(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusInProgress, parentTasks[0].Status)
}

func TestClient_AddParentChildDependencyKeepsClosedParentWhenChildClosed(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, childID, domain.StatusDone))

	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusDone, parentTasks[0].Status)
}

func TestClient_AddParentChildDependencyGuardsParentMoves(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	firstParentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "First parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	secondParentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Second parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, childID, firstParentID, "parent-child"))
	require.NoError(t, client.AddDependency(ctx, childID, firstParentID, "parent-child"))

	err = client.AddDependency(ctx, childID, secondParentID, "parent-child")
	require.Error(t, err)
	var parentErr ParentChangeRequiredError
	require.ErrorAs(t, err, &parentErr)
	assert.Equal(t, childID, parentErr.IssueID)
	assert.Equal(t, firstParentID, parentErr.CurrentParent)
	assert.Equal(t, secondParentID, parentErr.RequestedParent)

	child, err := client.GetWithRuntime(ctx, "default", childID)
	require.NoError(t, err)
	require.NotNil(t, child.ParentID)
	assert.Equal(t, firstParentID, child.ParentID.String())

	require.NoError(t, client.AddDependencyWithParentChange(ctx, childID, secondParentID, "parent-child", true))
	child, err = client.GetWithRuntime(ctx, "default", childID)
	require.NoError(t, err)
	require.NotNil(t, child.ParentID)
	assert.Equal(t, secondParentID, child.ParentID.String())

	var activeParentCount int
	db, err := client.dbHandle()
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM issue_dependencies
		WHERE issue_id = ? AND dependency_type = 'parent-child' AND tombstoned_at IS NULL
	`, childID).Scan(&activeParentCount))
	assert.Equal(t, 1, activeParentCount)
}

func TestClient_GraphClosureMaintainsParentChildMutations(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	rootID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	newRootID, err := client.Create(ctx, CreateTaskParams{
		Title:    "New root",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		ParentID: &rootID,
	})
	require.NoError(t, err)
	grandchildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Grandchild",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		ParentID: &childID,
	})
	require.NoError(t, err)

	descendants, err := client.ListGraphDescendantIDs(ctx, rootID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{childID, grandchildID}, descendants)
	ancestors, err := client.ListGraphAncestorIDs(ctx, grandchildID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{childID, rootID}, ancestors)

	require.NoError(t, client.AddDependencyWithParentChange(ctx, childID, newRootID, "parent-child", true))
	descendants, err = client.ListGraphDescendantIDs(ctx, rootID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Empty(t, descendants)
	descendants, err = client.ListGraphDescendantIDs(ctx, newRootID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{childID, grandchildID}, descendants)
	ancestors, err = client.ListGraphAncestorIDs(ctx, grandchildID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{childID, newRootID}, ancestors)

	confirmedCtx := WithParentChildOrphanConfirmation(WithDependencyRemovalConfirmation(ctx))
	require.NoError(t, client.RemoveDependency(confirmedCtx, grandchildID, childID, "parent-child"))
	descendants, err = client.ListGraphDescendantIDs(ctx, newRootID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{childID}, descendants)
	ancestors, err = client.ListGraphAncestorIDs(ctx, grandchildID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Empty(t, ancestors)

	leafID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Leaf",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		ParentID: &childID,
	})
	require.NoError(t, err)
	descendants, err = client.ListGraphDescendantIDs(ctx, childID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{leafID}, descendants)
	require.NoError(t, client.Delete(ctx, leafID))
	descendants, err = client.ListGraphDescendantIDs(ctx, childID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Empty(t, descendants)
}

func TestClient_GraphClosureCyclePreventionLeavesProjectionUnchanged(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	rootID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeTask,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		ParentID: &rootID,
	})
	require.NoError(t, err)
	grandchildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Grandchild",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		ParentID: &childID,
	})
	require.NoError(t, err)

	err = client.AddDependencyWithParentChange(ctx, rootID, grandchildID, "parent-child", true)
	require.Error(t, err)
	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.ErrorIs(t, storeErr.Err, domain.ErrConflict)

	descendants, err := client.ListGraphDescendantIDs(ctx, rootID, string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{childID, grandchildID}, descendants)

	db, err := client.dbHandle()
	require.NoError(t, err)
	var selfEdges int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM issue_graph_closure
		WHERE ancestor_id = descendant_id
	`).Scan(&selfEdges))
	assert.Equal(t, 0, selfEdges)
}

func TestClient_GraphClosureReadAPIsRejectUnsupportedDependencyTypes(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.ListGraphDescendantIDs(ctx, "root", string(domain.DependencyBlocks))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported graph closure dependency type")

	_, err = client.ListGraphAncestorIDs(ctx, "child", string(domain.DependencyRelatedTo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported graph closure dependency type")
}

func TestClient_ListHydratesParentChildAfterTaskSliceGrowth(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Hydration parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	// Create enough tasks to exceed the initial query slice capacity (32),
	// which previously exposed pointer invalidation when hydrating deps.
	var earlyChildID string
	for i := 0; i < 40; i++ {
		title := "Hydration task " + strconv.Itoa(i)
		params := CreateTaskParams{
			Title:    title,
			Type:     domain.TypeTask,
			Priority: domain.P2,
		}
		if i == 0 {
			params.Title = "Hydration early child"
			params.ParentID = &parentID
		}
		id, createErr := client.Create(ctx, params)
		require.NoError(t, createErr)
		if i == 0 {
			earlyChildID = id
		}
	}

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var earlyChild *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == earlyChildID {
			earlyChild = &tasks[i]
			break
		}
	}
	if earlyChild == nil {
		t.Fatalf("early child task %s not found", earlyChildID)
	}
	require.NotNil(t, earlyChild.ParentID)
	assert.Equal(t, parentID, earlyChild.ParentID.String())
}

func TestClient_AddDependencyRequiresExistingTargetIssue(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	err = client.AddDependency(ctx, sourceID, "missing-target", "blocks")
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "add-dependency", storeErr.Op)
	assert.Equal(t, sourceID, storeErr.TaskID)
	assert.ErrorIs(t, storeErr.Err, domain.ErrNotFound)

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Empty(t, tasks[0].Dependencies)
}

func TestClient_AddDependencyRejectsCycle(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	aID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Task A",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	bID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Task B",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	cID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Task C",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, bID, aID, "blocks"))
	require.NoError(t, client.AddDependency(ctx, cID, bID, "blocks"))

	err = client.AddDependency(ctx, aID, cID, "blocks")
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "add-dependency", storeErr.Op)
	assert.ErrorIs(t, storeErr.Err, domain.ErrConflict)
}

func TestClient_DeleteRemovesIssue(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete me",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	err = client.Delete(ctx, taskID)
	require.NoError(t, err)

	tasks, err := client.Search(ctx, taskID)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestClient_DeleteParentWithUndeletedDescendantsIsBlocked(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	grandchildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Grandchild",
		Type:     domain.TypeTask,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))
	require.NoError(t, client.AddDependency(ctx, grandchildID, childID, "parent-child"))

	err = client.Delete(ctx, parentID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIssueHasLiveChildren)
	assert.Contains(t, err.Error(), "cannot delete issue "+parentID)
	assert.Contains(t, err.Error(), "2 undeleted descendants")
	assert.Contains(t, err.Error(), "explicit recursive cleanup or supersede workflow")

	parent, findErr := client.Search(ctx, parentID)
	require.NoError(t, findErr)
	assertTaskPresent(t, parent, parentID)
	child, findErr := client.Search(ctx, childID)
	require.NoError(t, findErr)
	assertTaskPresent(t, child, childID)
}

func TestClient_DeleteBlockedWhenTaskHasWorktreeProjection(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-delete-worktree"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete blocked by worktree",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, projectID, taskID, "/tmp/"+taskID, "riordan/"+taskID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	err = client.Delete(ctx, taskID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleteBlockedByRuntimeAttachments)

	tasks, findErr := client.Search(ctx, taskID)
	require.NoError(t, findErr)
	require.Len(t, tasks, 1)
}

func TestClient_DeleteBlockedWhenTaskHasActiveSessionProjection(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-delete-session"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete blocked by active session",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-"+taskID, taskID, taskID, "running", time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	err = client.Delete(ctx, taskID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleteBlockedByRuntimeAttachments)

	tasks, findErr := client.Search(ctx, taskID)
	require.NoError(t, findErr)
	require.Len(t, tasks, 1)
}

func TestClient_DeleteAllowsStoppedSessionWithoutWorktreeProjection(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-delete-stopped"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete allowed with stopped session",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, projectID, "sess-"+taskID, taskID, taskID, "stopped", "", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	require.NoError(t, client.Delete(ctx, taskID))

	tasks, findErr := client.Search(ctx, taskID)
	require.NoError(t, findErr)
	assert.Empty(t, tasks)
}

func TestClient_ArchiveParentWithUndeletedDescendantsIsBlocked(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))

	err = client.Archive(ctx, parentID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIssueHasLiveChildren)
	assert.Contains(t, err.Error(), "cannot archive issue "+parentID)
	assert.Contains(t, err.Error(), "1 undeleted descendant")
	assert.Contains(t, err.Error(), "explicit recursive cleanup or supersede workflow")

	parent, findErr := client.Search(ctx, parentID)
	require.NoError(t, findErr)
	assertTaskPresent(t, parent, parentID)
	child, findErr := client.Search(ctx, childID)
	require.NoError(t, findErr)
	assertTaskPresent(t, child, childID)
}

func assertTaskPresent(t *testing.T, tasks []domain.Task, taskID string) {
	t.Helper()
	for _, task := range tasks {
		if task.ID.String() == taskID {
			return
		}
	}
	t.Fatalf("task %s not found in search results: %+v", taskID, tasks)
}

func TestClient_CreateDoesNotReuseDeletedLocalIssueIDs(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	firstID, err := client.Create(ctx, CreateTaskParams{Title: "first", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{Title: "second", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	thirdID, err := client.Create(ctx, CreateTaskParams{Title: "third", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.Equal(t, "a", firstID)
	require.Equal(t, "b", secondID)
	require.Equal(t, "c", thirdID)

	require.NoError(t, client.Delete(ctx, secondID))
	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `UPDATE meta SET value = '1' WHERE key = ?`, nextAlphaIssueIndexMetaKey)
	require.NoError(t, err)

	fourthID, err := client.Create(ctx, CreateTaskParams{Title: "fourth", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	assert.Equal(t, "d", fourthID)
}

func TestClient_CreateDoesNotReuseClosedLocalIssueIDsWhenMetaIndexDrifts(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	firstID, err := client.Create(ctx, CreateTaskParams{Title: "first", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.Equal(t, "a", firstID)
	require.NoError(t, client.Close(ctx, firstID, "done"))

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `UPDATE meta SET value = '0' WHERE key = ?`, nextAlphaIssueIndexMetaKey)
	require.NoError(t, err)

	secondID, err := client.Create(ctx, CreateTaskParams{Title: "second", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	assert.Equal(t, "b", secondID)
}

func TestClient_CreateSkipsHistoricallyUsedIDsWhenMetaIndexDrifts(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	firstID, err := client.Create(ctx, CreateTaskParams{Title: "first", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{Title: "second", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.Equal(t, "a", firstID)
	require.Equal(t, "b", secondID)

	// Simulate metadata/index drift pointing at an already-used historical ID.
	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`UPDATE meta SET value = '0' WHERE key = ?`, nextAlphaIssueIndexMetaKey)
	require.NoError(t, err)

	thirdID, err := client.Create(ctx, CreateTaskParams{Title: "third", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	assert.Equal(t, "c", thirdID)
}

func TestClient_CreateRepairsAllocatorFromDeletedIssueHistory(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())

	firstID, err := client.Create(ctx, CreateTaskParams{Title: "first", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{Title: "second", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.Equal(t, "a", firstID)
	require.Equal(t, "b", secondID)
	require.NoError(t, client.Delete(ctx, secondID))

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM issue_id_allocations WHERE id = ?`, secondID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE meta SET value = '1' WHERE key = ?`, nextAlphaIssueIndexMetaKey)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, client.CloseDB())

	reopened := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { require.NoError(t, reopened.CloseDB()) })
	thirdID, err := reopened.Create(ctx, CreateTaskParams{Title: "third", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	assert.Equal(t, "c", thirdID)
}

func TestClient_CreateRejectsOldOrphanWorktreeAndPreservesAllocator(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())

	firstID, err := client.Create(ctx, CreateTaskParams{Title: "first", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{Title: "second", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.Equal(t, "a", firstID)
	require.Equal(t, "b", secondID)
	require.NoError(t, client.Delete(ctx, secondID))

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, "default", secondID, filepath.Join(t.TempDir(), "repo-"+secondID), "riordan/"+secondID+"/old-branch", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "existing issue")
	_, err = db.ExecContext(ctx, `DELETE FROM issue_id_allocations WHERE id = ?`, secondID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE meta SET value = '1' WHERE key = ?`, nextAlphaIssueIndexMetaKey)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, client.CloseDB())

	reopened := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { require.NoError(t, reopened.CloseDB()) })
	thirdID, err := reopened.Create(ctx, CreateTaskParams{Title: "third", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	assert.Equal(t, "c", thirdID)
}

func TestClient_ErrorWrapping(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)

	err := client.Update(ctx, "does-not-exist", domain.StatusDone)
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "update", storeErr.Op)
	assert.Equal(t, "does-not-exist", storeErr.TaskID)
}

func TestClient_DBHandleReusedUntilExplicitClose(t *testing.T) {
	parallelIssueStoreTest(t)
	client := newTestClient(t)

	first, err := client.dbHandle()
	require.NoError(t, err)

	second, err := client.dbHandle()
	require.NoError(t, err)
	assert.Same(t, first, second)

	require.NoError(t, client.CloseDB())

	third, err := client.dbHandle()
	require.NoError(t, err)
	assert.NotSame(t, first, third)
}

func TestClient_DBHandleCreatesMissingParentDirectory(t *testing.T) {
	parallelIssueStoreTest(t)
	base := t.TempDir()
	dbPath := filepath.Join(base, "missing", "nested", "azedarach.db")
	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	_, err := client.dbHandle()
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Dir(dbPath))
	require.NoError(t, statErr)
}

func TestClient_ConfiguresSQLitePragmas(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	var foreignKeys int
	require.NoError(t, db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	assert.Equal(t, 1, foreignKeys)

	var journalMode string
	require.NoError(t, db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode))
	assert.Equal(t, "wal", strings.ToLower(journalMode))

	firstConn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer firstConn.Close()
	secondConn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer secondConn.Close()
	var secondForeignKeys int
	require.NoError(t, secondConn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&secondForeignKeys))
	assert.Equal(t, 1, secondForeignKeys)
}

func TestClient_EnsuresDependencyForeignKeysAndIndexes(t *testing.T) {
	parallelIssueStoreTest(t)
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	type fkRow struct {
		table string
		from  string
		to    string
	}
	fkRows, err := db.Query(`PRAGMA foreign_key_list('issue_dependencies')`)
	require.NoError(t, err)
	defer fkRows.Close()

	fks := make([]fkRow, 0, 2)
	for fkRows.Next() {
		var (
			id       int
			seq      int
			table    string
			from     string
			to       string
			onUpdate string
			onDelete string
			match    string
		)
		require.NoError(t, fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match))
		fks = append(fks, fkRow{table: table, from: from, to: to})
	}
	require.NoError(t, fkRows.Err())
	assert.Contains(t, fks, fkRow{table: "issues", from: "issue_id", to: "id"})
	assert.Contains(t, fks, fkRow{table: "issues", from: "depends_on_id", to: "id"})

	wantIndexes := []string{
		"idx_issues_deleted_updated",
		"idx_issues_status_deleted_priority_updated",
		"idx_dependencies_issue_active_type",
		"idx_dependencies_depends_on_active_type",
		"idx_dependencies_depends_on",
		"idx_issue_graph_closure_ancestor",
		"idx_issue_graph_closure_descendant",
		"idx_issue_graph_closure_guard",
	}
	indexRows, err := db.Query(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'index' AND name IN (?, ?, ?, ?, ?, ?, ?, ?)
		ORDER BY name
	`, wantIndexes[0], wantIndexes[1], wantIndexes[2], wantIndexes[3], wantIndexes[4], wantIndexes[5], wantIndexes[6], wantIndexes[7])
	require.NoError(t, err)
	defer indexRows.Close()

	gotIndexes := make([]string, 0, len(wantIndexes))
	for indexRows.Next() {
		var name string
		require.NoError(t, indexRows.Scan(&name))
		gotIndexes = append(gotIndexes, name)
	}
	require.NoError(t, indexRows.Err())
	assert.ElementsMatch(t, wantIndexes, gotIndexes)

	_, err = db.Exec(`
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES ('missing-source', 'missing-target', 'blocks', NULL)
	`)
	require.Error(t, err)
}

func TestClient_GraphClosureMigrationBackfillsParentChildEdges(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at)
		VALUES
			('root', 'Root', 'open', 1, 'epic', ?, ?),
			('child', 'Child', 'open', 1, 'task', ?, ?),
			('grandchild', 'Grandchild', 'open', 1, 'task', ?, ?),
			('deleted-child', 'Deleted child', 'open', 1, 'task', ?, ?);
		UPDATE issues SET deleted_at = ? WHERE id = 'deleted-child';
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES
			('child', 'root', 'parent_child', NULL),
			('grandchild', 'child', 'parent-child', NULL),
			('deleted-child', 'root', 'parent-child', NULL);
	`, now, now, now, now, now, now, now, now, now)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	descendants, err := client.ListGraphDescendantIDs(ctx, "root", string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{"child", "grandchild"}, descendants)
	ancestors, err := client.ListGraphAncestorIDs(ctx, "grandchild", string(domain.DependencyParentChild))
	require.NoError(t, err)
	assert.Equal(t, []string{"child", "root"}, ancestors)
}

func TestClient_MigratesLegacySchemaShape(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on
			ON issue_dependencies(depends_on_id, dependency_type, tombstoned_at);
	`)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	_, err = client.Create(ctx, CreateTaskParams{
		Title:    "legacy schema compatibility",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	rows, err := db.Query(`SELECT id FROM schema_migrations ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		got = append(got, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{
		"0001_bootstrap_tables",
		"0002_dependency_foreign_keys",
		"0003_issue_indexes",
		"0004_spec_tables",
		"0005_spec_audit_log",
		"0006_external_issue_sync",
		"0006_issue_external_refs",
		"0007_external_issue_sync_payload",
		"0008_decision_tables",
		"0009_decision_audit_log",
		"0010_decisions_refresh",
		"0011_decisions_consequences",
		"0012_blocked_status_to_open",
		"0013_closed_runtime_invariants",
		"0014_linear_sync_external_refs_backfill",
		"0015_issue_attachments",
		"0016_issue_search_fts",
		"0017_spec_requirement_search_fts",
		"0018_issue_graph_closure",
		"0019_agent_learnings",
		"0019_issue_observation_events",
		"0020_agent_learning_lifecycle",
		"0021_agent_learning_metadata",
		"0021_agent_learning_relations",
		"0021_agent_learning_target_state",
		"0025_agent_learning_privacy",
		"0026_decision_search_fts",
		"0026_issue_ownership",
		"0027_issue_id_allocations",
		"0028_runtime_projection_order_indexes",
		"0029_issue_state_model_v2",
		"0030_issue_closed_runtime_v2_triggers",
		"0031_board_views",
		"0032_coordination_leases",
		"0033_orchestrator_scope_leases",
		"0034_orchestration_start_attempts",
		"0034_orchestrator_lifecycle_clock",
		"0035_interaction_requests",
		"0036_advisor_sessions",
		"0037_learning_activation_feedback",
		"0037_projection_source_revision",
		"0038_learning_consolidation",
		"0039_contextual_learning_activation",
		"0040_typed_learning_observations",
		"0041_learning_activation_confirmation",
		"0042_learning_consolidation_scan_cursor",
		"0043_learning_activation_telemetry",
		"0044_learning_activation_abandonment",
		"0045_issue_state_runtime_constraints",
		"0046_repair_issue_state_runtime_constraints",
		"0047_human_authority_projection_revision",
		"0048_decision_propagation_outbox",
		"0049_managed_agent_incarnations",
	}, got)
}

func TestClient_RenumberedInteractionMigrationsUpgradeLegacyHistories(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	client := NewClientAtPath(dbPath, slog.Default())
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "migration seed",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		DELETE FROM schema_migrations
		WHERE id IN ('0035_interaction_requests', '0036_advisor_sessions');
		INSERT OR IGNORE INTO schema_migrations (id, applied_at) VALUES
			('0032_interaction_requests', ?),
			('0034_interaction_requests', ?),
			('0035_advisor_sessions', ?);
	`, now, now, now)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	upgraded := NewClientAtPath(dbPath, slog.Default())
	_, err = upgraded.List(ctx)
	require.NoError(t, err)
	require.NoError(t, upgraded.CloseDB())

	db, err = sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	defer db.Close()
	var applied int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM schema_migrations
		WHERE id IN ('0035_interaction_requests', '0036_advisor_sessions')
	`).Scan(&applied))
	assert.Equal(t, 2, applied)
}

func TestClient_RenumberedContextualLearningMigrationUpgradesHistoricalIdentity(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	client := NewClientAtPath(dbPath, slog.Default())
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "migration seed",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		DELETE FROM schema_migrations
		WHERE id = '0039_contextual_learning_activation';
		INSERT INTO schema_migrations (id, applied_at)
		VALUES ('0038_contextual_learning_activation', ?);
	`, now)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	upgraded := NewClientAtPath(dbPath, slog.Default())
	_, err = upgraded.List(ctx)
	require.NoError(t, err)
	require.NoError(t, upgraded.CloseDB())

	db, err = sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	defer db.Close()
	var applied int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM schema_migrations
		WHERE id = '0039_contextual_learning_activation'
	`).Scan(&applied))
	assert.Equal(t, 1, applied)
}

func TestClient_RenumberedContextualLearningMigrationCompletesHistoricalSchema(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	client := NewClientAtPath(dbPath, slog.Default())
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "migration seed",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		DELETE FROM schema_migrations
		WHERE id = '0039_contextual_learning_activation';
		INSERT INTO schema_migrations (id, applied_at)
		VALUES ('0038_contextual_learning_activation', '2026-07-12T00:00:00Z');
		DROP INDEX idx_learning_activations_session;
		DROP TABLE learning_activation_deliveries;
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	upgraded := NewClientAtPath(dbPath, slog.Default())
	_, err = upgraded.List(ctx)
	require.NoError(t, err)
	require.NoError(t, upgraded.CloseDB())

	db, err = sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	defer db.Close()
	var applied, repairedObjects int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM schema_migrations
		WHERE id = '0039_contextual_learning_activation'
	`).Scan(&applied))
	assert.Equal(t, 1, applied)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE (type = 'table' AND name = 'learning_activation_deliveries')
		   OR (type = 'index' AND name IN (
			'idx_learning_activations_session',
			'idx_learning_activation_deliveries_activation'
		   ))
	`).Scan(&repairedObjects))
	assert.Equal(t, 3, repairedObjects)
}

func TestClient_RepairsLegacyIssueColumnsBeforeSearchFTSMigration(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			notes TEXT,
			design TEXT,
			acceptance TEXT,
			assignee TEXT,
			labels_json TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		);
		CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		INSERT INTO issues (
			id, title, description, notes, design, acceptance, assignee, labels_json,
			status, priority, issue_type, created_at, updated_at
		)
		VALUES ('legacy-search', 'Legacy search fixture', '', '', '', '', '', '[]', 'open', 2, 'task', ?, ?);
	`, now, now)
	require.NoError(t, err)
	for _, id := range []string{
		"0001_bootstrap_tables",
		"0002_dependency_foreign_keys",
		"0003_issue_indexes",
		"0004_spec_tables",
		"0005_spec_audit_log",
		"0006_external_issue_sync",
		"0006_issue_external_refs",
		"0007_external_issue_sync_payload",
		"0008_decision_tables",
		"0009_decision_audit_log",
		"0010_decisions_refresh",
		"0011_decisions_consequences",
		"0012_blocked_status_to_open",
		"0013_closed_runtime_invariants",
		"0014_linear_sync_external_refs_backfill",
		"0015_issue_attachments",
	} {
		_, err = db.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, id, now)
		require.NoError(t, err)
	}

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	results, err := client.SearchWithRuntime(ctx, "proj", "legacy search")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, naming.IssueID("legacy-search"), results[0].ID)

	columns, err := tableColumns(db, "issues")
	require.NoError(t, err)
	for _, column := range []string{"closed_at", "implementations_json", "estimate"} {
		assert.Contains(t, columns, column)
	}

	var applied bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM schema_migrations WHERE id = '0016_issue_search_fts'
		)
	`).Scan(&applied)
	require.NoError(t, err)
	assert.True(t, applied)
}

func TestClient_ReplaysAgentLearningPrivacyMigrationWhenColumnAlreadyExists(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	client := NewClientAtPath(dbPath, slog.Default())
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "seed migrated schema",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id = '0025_agent_learning_privacy'`)
	require.NoError(t, err)

	client = NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	_, err = client.List(ctx)
	require.NoError(t, err)

	var applied bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM schema_migrations WHERE id = '0025_agent_learning_privacy'
		)
	`).Scan(&applied)
	require.NoError(t, err)
	assert.True(t, applied)

	var indexed bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_master
			WHERE type = 'index' AND name = 'idx_agent_learnings_active_privacy'
		)
	`).Scan(&indexed)
	require.NoError(t, err)
	assert.True(t, indexed)
}

func TestClient_ReplaysIssueOwnershipMigrationWhenColumnsAlreadyExist(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	client := NewClientAtPath(dbPath, slog.Default())
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "seed migrated ownership schema",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id = '0026_issue_ownership'`)
	require.NoError(t, err)

	client = NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	_, err = client.List(ctx)
	require.NoError(t, err)

	var applied bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM schema_migrations WHERE id = '0026_issue_ownership'
		)
	`).Scan(&applied)
	require.NoError(t, err)
	assert.True(t, applied)

	var indexed bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_master
			WHERE type = 'index' AND name = 'idx_issues_owner_active'
		)
	`).Scan(&indexed)
	require.NoError(t, err)
	assert.True(t, indexed)
}

func TestClient_RepairsAppliedAgentLearningMigrationMissingBaseColumns(t *testing.T) {
	parallelIssueStoreTest(t)
	for _, tc := range []struct {
		name         string
		seedLearning bool
	}{
		{name: "with learnings", seedLearning: true},
		{name: "without learnings", seedLearning: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "azedarach.db")

			client := NewClientAtPath(dbPath, slog.Default())
			_, err := client.List(ctx)
			require.NoError(t, err)
			var created Learning
			if tc.seedLearning {
				issueID, err := client.Create(ctx, CreateTaskParams{
					Title:    "learning schema repair",
					Type:     domain.TypeTask,
					Priority: domain.P3,
				})
				require.NoError(t, err)
				created, err = client.CreateLearning(ctx, CreateLearningParams{
					ProjectID: "proj",
					IssueID:   &issueID,
					Summary:   "Keep migration repair idempotent",
					Evidence:  "Affected databases have 0019 marked applied but are missing review columns.",
					Tags:      []string{"migration"},
				})
				require.NoError(t, err)
			}
			require.NoError(t, client.CloseDB())

			db, err := sql.Open("sqlite", "file:"+dbPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			for _, index := range []string{
				"idx_agent_learnings_active_policy",
				"idx_agent_learnings_target_state",
				"idx_agent_learnings_active_privacy",
			} {
				_, err = db.ExecContext(ctx, "DROP INDEX IF EXISTS "+index)
				require.NoError(t, err)
			}
			repairedColumns := []string{
				"evidence_private",
				"promotion_target",
				"promotion_target_id",
				"promotion_note",
				"promoted_at",
				"review_note",
				"reviewed_at",
				"expires_at",
				"stale_at",
				"last_recalled_at",
				"recall_count",
				"superseded_at",
				"target_retired_at",
				"target_state",
				"target_hash",
				"target_metadata_json",
				"target_drifted_at",
			}
			for _, column := range repairedColumns {
				_, err = db.ExecContext(ctx, "ALTER TABLE agent_learnings DROP COLUMN "+column)
				require.NoError(t, err)
			}

			var applied bool
			err = db.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM schema_migrations WHERE id = '0019_agent_learnings'
				)
			`).Scan(&applied)
			require.NoError(t, err)
			require.True(t, applied)

			client = NewClientAtPath(dbPath, slog.Default())
			t.Cleanup(func() {
				require.NoError(t, client.CloseDB())
			})
			rows, err := client.ListLearnings(ctx, LearningFilter{
				ProjectID: "proj",
				Query:     "idempotent",
				Limit:     5,
			})
			require.NoError(t, err)
			if !tc.seedLearning {
				assert.Empty(t, rows)
			} else {
				require.Len(t, rows, 1)
				assert.Equal(t, created.LocalID, rows[0].LocalID)
				assert.Empty(t, rows[0].ReviewNote)
				assert.Nil(t, rows[0].ReviewedAt)
			}

			columns, err := tableColumns(db, "agent_learnings")
			require.NoError(t, err)
			for _, column := range repairedColumns {
				_, exists := columns[column]
				assert.True(t, exists)
			}
		})
	}
}

func TestClient_MigratesClosedRuntimeInvariantViolationsToInReview(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE daemon_session_projections (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		);
		CREATE TABLE daemon_worktree_projections (
			project_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			path TEXT NOT NULL,
			branch TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			git_status_json TEXT,
			git_status_updated_at TEXT,
			PRIMARY KEY (project_id, issue_id)
		);
		INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at, closed_at)
		VALUES
			('az-direct', 'Direct dirty closed issue', 'closed', 2, 'task', ?, ?, ?),
			('az-parent', 'Closed parent with dirty child', 'closed', 2, 'task', ?, ?, ?),
			('az-child', 'Dirty closed child', 'closed', 2, 'task', ?, ?, ?),
			('az-open-parent', 'Closed parent with open child', 'closed', 2, 'task', ?, ?, ?),
			('az-open-child', 'Open child', 'open', 2, 'task', ?, ?, NULL),
			('az-clean', 'Clean closed issue', 'closed', 2, 'task', ?, ?, ?),
			('az-stopped', 'Stopped session closed issue', 'closed', 2, 'task', ?, ?, ?),
			('az-open', 'Open dirty issue', 'open', 2, 'task', ?, ?, NULL);
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES
			('az-child', 'az-parent', 'parent-child', NULL),
			('az-open-child', 'az-open-parent', 'parent-child', NULL);
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES
			('proj', 'az-direct', '/repo/az-direct', 'az-direct', ?),
			('proj', 'az-open', '/repo/az-open', 'az-open', ?);
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, started_at, updated_at)
		VALUES
			('proj', 'sess-child', 'az-child', 'running', ?, ?),
			('proj', 'sess-stopped', 'az-stopped', 'stopped', ?, ?);
	`, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	_, err = client.dbHandle()
	require.NoError(t, err)

	for _, id := range []string{"az-direct", "az-parent", "az-child", "az-open-parent"} {
		var status string
		var closedAt any
		require.NoError(t, db.QueryRowContext(ctx, `SELECT status, closed_at FROM issues WHERE id = ?`, id).Scan(&status, &closedAt))
		assert.Equal(t, "in_review", status, id)
		assert.Nil(t, closedAt, id)
	}
	for _, id := range []string{"az-clean", "az-stopped"} {
		var status string
		var closedAt string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT status, closed_at FROM issues WHERE id = ?`, id).Scan(&status, &closedAt))
		assert.Equal(t, "closed", status, id)
		assert.NotEmpty(t, closedAt, id)
	}
	var openStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM issues WHERE id = 'az-open'`).Scan(&openStatus))
	assert.Equal(t, "open", openStatus)
}

func TestClient_ClosedRuntimeInvariantTriggers(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	openID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Open issue with worktree",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES ('proj', ?, '/repo/open', 'open', ?)
	`, openID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	err = client.Update(ctx, openID, domain.StatusDone)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed issue cannot have active runtime attachments")

	closedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Closed issue",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, closedID, domain.StatusDone))
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES ('proj', ?, '/repo/closed', 'closed', ?)
	`, closedID, time.Now().UTC().Format(time.RFC3339Nano))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot attach worktree to closed issue or closed ancestor")

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Closed parent",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child issue",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, 'parent-child', NULL)
	`, childID, parentID)
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, childID, domain.StatusDone))
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, started_at, updated_at)
		VALUES ('proj', 'sess-child', ?, ?, 'running', ?, ?)
	`, childID, childID, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot attach active session to closed issue or closed ancestor")

	newParentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Another closed parent",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	dirtyChildID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Dirty child",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, newParentID, domain.StatusDone))
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, scope_id, state, started_at, updated_at)
		VALUES ('proj', 'sess-dirty-child', ?, ?, 'running', ?, ?)
	`, dirtyChildID, dirtyChildID, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, 'parent-child', NULL)
	`, dirtyChildID, newParentID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot place unresolved descendant under closed issue")
}

func TestClient_MigratesLegacyBlockedStatusToOpen(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at)
		VALUES ('az-legacy', 'Legacy blocked issue', '', 'blocked', 2, 'task', ?, ?);
	`, now, now)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	tasksByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		tasksByID[task.ID.String()] = task
	}

	legacy := tasksByID["az-legacy"]
	assert.Equal(t, domain.StatusOpen, legacy.Status)
	require.Len(t, legacy.Dependencies, 1)
	assert.Equal(t, "az-legacy-legacy-blocker", legacy.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, legacy.Dependencies[0].Type)

	blocker := tasksByID["az-legacy-legacy-blocker"]
	assert.Equal(t, "Resolve blocker for az-legacy", blocker.Title)
	assert.Equal(t, domain.StatusOpen, blocker.Status)

	ready, err := client.Ready(ctx)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Equal(t, "az-legacy-legacy-blocker", ready[0].ID.String())
}

func TestClient_MigratesLegacyBlockedStatusAllocatesUniqueBlockerID(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, closed_at)
		VALUES ('az-legacy-legacy-blocker', 'Existing unrelated issue', '', 'closed', 2, 'task', ?, ?, ?);
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at)
		VALUES ('az-legacy', 'Legacy blocked issue', '', 'blocked', 2, 'task', ?, ?);
	`, now, now, now, now, now)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	tasksByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		tasksByID[task.ID.String()] = task
	}

	legacy := tasksByID["az-legacy"]
	assert.Equal(t, domain.StatusOpen, legacy.Status)
	require.Len(t, legacy.Dependencies, 1)
	assert.Equal(t, "az-legacy-legacy-blocker-1", legacy.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, legacy.Dependencies[0].Type)

	collision := tasksByID["az-legacy-legacy-blocker"]
	assert.Equal(t, "Existing unrelated issue", collision.Title)
	assert.Equal(t, domain.StatusDone, collision.Status)

	blocker := tasksByID["az-legacy-legacy-blocker-1"]
	assert.Equal(t, "Resolve blocker for az-legacy", blocker.Title)
	assert.Equal(t, domain.StatusOpen, blocker.Status)

	ready, err := client.Ready(ctx)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Equal(t, "az-legacy-legacy-blocker-1", ready[0].ID.String())
}

func TestClient_CreateWaitsForWriteLockAndSucceedsWithinBusyTimeout(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "warmup",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	lockDB, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockDB.Close() })

	_, err = lockDB.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, createErr := client.Create(ctx, CreateTaskParams{
			Title:    "wait-for-lock",
			Type:     domain.TypeTask,
			Priority: domain.P3,
		})
		done <- createErr
	}()

	time.Sleep(250 * time.Millisecond)
	_, err = lockDB.Exec(`COMMIT`)
	require.NoError(t, err)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("create did not complete after releasing write lock")
	}

	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
}

func TestClient_ListWithRuntimeContinuesWhileSyncWriterHoldsConnection(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "local-first read",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)
	writerConn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() {
		_, _ = writerConn.ExecContext(context.Background(), `ROLLBACK`)
		_ = writerConn.Close()
	}()
	_, err = writerConn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	require.NoError(t, err)

	readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	tasks, err := client.ListWithRuntime(readCtx, "project-1")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(issueID), tasks[0].ID)
	assert.Equal(t, "local-first read", tasks[0].Title)
}

func TestClient_AddDependencyWaitsForIssueMutationLock(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
	})
	require.NoError(t, err)

	done := make(chan error, 1)
	err = client.WithMutationLock(ctx, func(context.Context) error {
		go func() {
			done <- client.AddDependency(ctx, childID, parentID, "parent-child")
		}()

		select {
		case addErr := <-done:
			t.Fatalf("AddDependency completed before mutation lock released: %v", addErr)
		case <-time.After(200 * time.Millisecond):
		}
		return nil
	})
	require.NoError(t, err)

	select {
	case addErr := <-done:
		require.NoError(t, addErr)
	case <-time.After(3 * time.Second):
		t.Fatal("AddDependency did not complete after releasing mutation lock")
	}
}

func TestClient_AddDependencyWithRuntimeWaitsForIssueMutationLock(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
	})
	require.NoError(t, err)
	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
	})
	require.NoError(t, err)

	done := make(chan error, 1)
	err = client.WithMutationLock(ctx, func(context.Context) error {
		go func() {
			_, addErr := client.AddDependencyWithRuntime(ctx, "project-1", childID, parentID, "parent-child")
			done <- addErr
		}()

		select {
		case addErr := <-done:
			t.Fatalf("AddDependencyWithRuntime completed before mutation lock released: %v", addErr)
		case <-time.After(200 * time.Millisecond):
		}
		return nil
	})
	require.NoError(t, err)

	select {
	case addErr := <-done:
		require.NoError(t, addErr)
	case <-time.After(3 * time.Second):
		t.Fatal("AddDependencyWithRuntime did not complete after releasing mutation lock")
	}
}

func TestClient_CreateRetriesAfterSQLiteBusyTimeout(t *testing.T) {
	ctx := context.Background()
	client, retryStarted, releaseRetry := newBusyRetryTestClient(t)
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "warmup",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	lockDB, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockDB.Close() })

	_, err = lockDB.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, createErr := client.Create(opCtx, CreateTaskParams{
			Title:    "retry-after-busy",
			Type:     domain.TypeTask,
			Priority: domain.P3,
		})
		done <- createErr
	}()

	select {
	case <-retryStarted:
	case <-opCtx.Done():
		t.Fatal("create did not reach SQLite busy retry")
	}
	_, err = lockDB.Exec(`COMMIT`)
	require.NoError(t, err)
	close(releaseRetry)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-opCtx.Done():
		t.Fatal("create did not complete after retrying past busy timeout")
	}
	var created int
	require.NoError(t, client.db.QueryRow(`SELECT COUNT(*) FROM issues WHERE title = 'retry-after-busy'`).Scan(&created))
	assert.Equal(t, 1, created, "transaction retry must not replay the create side effect")
}

func TestClient_CreateBusyRetryIsBoundedWithoutCallerDeadline(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t, WithSQLiteBusyPolicy(10*time.Millisecond, time.Millisecond))
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "warmup",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	lockDB, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockDB.Close() })
	require.NoError(t, func() error {
		_, err := lockDB.Exec(`BEGIN IMMEDIATE`)
		return err
	}())

	done := make(chan error, 1)
	go func() {
		_, createErr := client.Create(ctx, CreateTaskParams{
			Title:    "bounded-busy-retry",
			Type:     domain.TypeTask,
			Priority: domain.P3,
		})
		done <- createErr
	}()

	select {
	case createErr := <-done:
		require.Error(t, createErr)
		assert.True(t, IsSQLiteBusy(createErr), "error = %v, want preserved SQLite busy error", createErr)
	case <-time.After(250 * time.Millisecond):
		_, _ = lockDB.Exec(`ROLLBACK`)
		createErr := <-done
		t.Fatalf("Create exceeded configured busy policy without caller deadline; eventual error = %v", createErr)
	}
	_, _ = lockDB.Exec(`ROLLBACK`)
}

func TestClient_CreateBusyRetryStopsAtEarlierCallerDeadline(t *testing.T) {
	client := newTestClient(t, WithSQLiteBusyPolicy(time.Second, 10*time.Millisecond))
	_, err := client.Create(context.Background(), CreateTaskParams{Title: "warmup", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	lockDB, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockDB.Close() })
	_, err = lockDB.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	defer func() { _, _ = lockDB.Exec(`ROLLBACK`) }()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = client.Create(ctx, CreateTaskParams{Title: "cancelled-busy-retry", Type: domain.TypeTask, Priority: domain.P3})
	require.Error(t, err)
	assert.True(t, IsSQLiteBusy(err), "error = %v, want preserved SQLite busy error", err)
	assert.Less(t, time.Since(started), 500*time.Millisecond)
}

func TestIsSQLiteBusyRecognizesBusySnapshotExtendedCode(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "snapshot target", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	reader, err := db.Conn(ctx)
	require.NoError(t, err)
	defer reader.Close()
	writer, err := db.Conn(ctx)
	require.NoError(t, err)
	defer writer.Close()
	_, err = reader.ExecContext(ctx, `BEGIN`)
	require.NoError(t, err)
	defer func() { _, _ = reader.ExecContext(context.Background(), `ROLLBACK`) }()
	var title string
	require.NoError(t, reader.QueryRowContext(ctx, `SELECT title FROM issues WHERE id = ?`, issueID).Scan(&title))
	_, err = writer.ExecContext(ctx, `UPDATE issues SET notes = 'writer committed' WHERE id = ?`, issueID)
	require.NoError(t, err)
	_, err = reader.ExecContext(ctx, `UPDATE issues SET notes = 'stale snapshot' WHERE id = ?`, issueID)
	require.Error(t, err)
	assert.True(t, IsSQLiteBusy(err), "error = %v, want SQLITE_BUSY_SNAPSHOT classification", err)
}

func TestClient_UpdateRetriesAfterSQLiteBusyTimeout(t *testing.T) {
	ctx := context.Background()
	client, retryStarted, releaseRetry := newBusyRetryTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "update-retry-target",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	lockDB, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockDB.Close() })

	_, err = lockDB.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- client.Update(opCtx, issueID, domain.StatusInReview)
	}()

	select {
	case <-retryStarted:
	case <-opCtx.Done():
		t.Fatal("update did not reach SQLite busy retry")
	}
	_, err = lockDB.Exec(`COMMIT`)
	require.NoError(t, err)
	close(releaseRetry)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-opCtx.Done():
		t.Fatal("update did not complete after retrying past busy timeout")
	}

	task, err := client.GetWithRuntime(ctx, protocol.DefaultProjectID, issueID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusInReview, task.Status)
}

func TestClient_UpsertExternalSyncStateRetriesAfterSQLiteBusyTimeout(t *testing.T) {
	ctx := context.Background()
	client, retryStarted, releaseRetry := newBusyRetryTestClient(t)
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "sync-state-retry-warmup",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	lockDB, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockDB.Close() })

	_, err = lockDB.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- client.UpsertExternalSyncState(opCtx, ExternalSyncState{
			Provider:  "linear",
			ProjectID: "project-1",
			Cursor:    "cursor-1",
		})
	}()

	select {
	case <-retryStarted:
	case <-opCtx.Done():
		t.Fatal("external sync state upsert did not reach SQLite busy retry")
	}
	_, err = lockDB.Exec(`COMMIT`)
	require.NoError(t, err)
	close(releaseRetry)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-opCtx.Done():
		t.Fatal("external sync state upsert did not complete after retrying past busy timeout")
	}

	state, ok, err := client.GetExternalSyncState(ctx, "linear", "project-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "cursor-1", state.Cursor)
}

func TestClient_SQLiteWALDiagnosticsAndCheckpoint(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "wal diagnostics",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	diag, err := client.SQLiteWALDiagnostics(ctx)
	require.NoError(t, err)
	assert.Equal(t, client.dbPath, diag.DBPath)
	assert.Equal(t, client.dbPath+"-wal", diag.WALPath)
	assert.GreaterOrEqual(t, diag.WALBytes, int64(0))

	stats, err := client.CheckpointSQLiteWAL(ctx, SQLiteWALCheckpointPassive)
	require.NoError(t, err)
	assert.Equal(t, SQLiteWALCheckpointPassive, stats.Mode)
	assert.GreaterOrEqual(t, stats.LogFrames, 0)
	assert.GreaterOrEqual(t, stats.CheckpointedFrame, 0)
}

func TestClient_MutationMaintainsLargeSQLiteWAL(t *testing.T) {
	oldInterval := sqliteWALMaintenanceInterval
	oldCheckpointThreshold := sqliteWALCheckpointThreshold
	oldLargeThreshold := sqliteWALLargeThreshold
	sqliteWALMaintenanceInterval = 0
	sqliteWALCheckpointThreshold = 0
	sqliteWALLargeThreshold = 0
	t.Cleanup(func() {
		sqliteWALMaintenanceInterval = oldInterval
		sqliteWALCheckpointThreshold = oldCheckpointThreshold
		sqliteWALLargeThreshold = oldLargeThreshold
	})

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client := newTestClientWithLogger(t, logger)
	_, err := client.Create(context.Background(), CreateTaskParams{
		Title:    "wal maintenance",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	got := logs.String()
	assert.Contains(t, got, `"event":"sqlite.wal_checkpoint.completed"`)
	assert.Contains(t, got, `"wal_bytes_after"`)
}

func explainQueryPlan(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer rows.Close()

	plan := strings.Builder{}
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	require.NoError(t, rows.Err())
	return plan.String()
}

func newTestClient(t *testing.T, opts ...ClientOption) *Client {
	t.Helper()
	return newTestClientWithLogger(t, slog.Default(), opts...)
}

var (
	issueTestTemplateOnce sync.Once
	issueTestTemplate     *sqlitetest.Template
	issueTestTemplateErr  error
)

func newTestClientWithLogger(t *testing.T, logger *slog.Logger, opts ...ClientOption) *Client {
	t.Helper()
	return newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger, opts...)
}

func newTestClientAtPath(t *testing.T, dbPath string, logger *slog.Logger, opts ...ClientOption) *Client {
	t.Helper()
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		template := migratedIssueTestTemplate(t)
		_, err = template.Clone(dbPath)
		require.NoError(t, err)
	} else {
		require.NoError(t, err)
	}
	client := NewClientAtPath(dbPath, logger, opts...)
	_, err := client.ProjectionSourceVersion(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	return client
}

func migratedIssueTestTemplate(tb testing.TB) *sqlitetest.Template {
	tb.Helper()
	issueTestTemplateOnce.Do(func() {
		issueTestTemplate, issueTestTemplateErr = sqlitetest.NewTemplate(func(path string) error {
			client := NewClientAtPath(path, slog.New(slog.DiscardHandler))
			if _, err := client.ProjectionSourceVersion(tb.Context()); err != nil {
				_ = client.CloseDB()
				return err
			}
			return client.CloseDB()
		})
	})
	require.NoError(tb, issueTestTemplateErr)
	return issueTestTemplate
}

func TestMigratedIssueTestTemplateClonesAreIsolatedAndComplete(t *testing.T) {
	parallelIssueStoreTest(t)
	first := newTestClient(t)
	second := newTestClient(t)
	firstDB, err := first.dbHandle()
	require.NoError(t, err)
	secondDB, err := second.dbHandle()
	require.NoError(t, err)

	var migrationCount int
	require.NoError(t, firstDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount))
	assert.Equal(t, len(orderedMigrations), migrationCount)
	for _, object := range []struct{ kind, name string }{
		{"index", "idx_issues_status_deleted_priority_updated"},
		{"trigger", "projection_source_revision_issues_insert"},
	} {
		var count int
		require.NoError(t, firstDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&count))
		assert.Equalf(t, 1, count, "%s %s", object.kind, object.name)
	}
	var journalMode string
	require.NoError(t, firstDB.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode))
	assert.Equal(t, "wal", strings.ToLower(journalMode))

	_, err = firstDB.Exec(`INSERT INTO meta(key, value) VALUES('fixture-isolation', 'first')`)
	require.NoError(t, err)
	var secondCount int
	require.NoError(t, secondDB.QueryRow(`SELECT COUNT(*) FROM meta WHERE key='fixture-isolation'`).Scan(&secondCount))
	assert.Zero(t, secondCount)
}

func BenchmarkIssueStoreFixtureCosts(b *testing.B) {
	b.Run("template_creation", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			template, err := sqlitetest.NewTemplate(func(path string) error {
				client := NewClientAtPath(path, slog.New(slog.DiscardHandler))
				if _, err := client.ProjectionSourceVersion(b.Context()); err != nil {
					_ = client.CloseDB()
					return err
				}
				return client.CloseDB()
			})
			require.NoError(b, err)
			require.NoError(b, template.Close())
		}
	})

	template := migratedIssueTestTemplate(b)
	b.Run("clone", func(b *testing.B) {
		root := b.TempDir()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := template.Clone(filepath.Join(root, strconv.Itoa(i)+".db"))
			require.NoError(b, err)
		}
	})
	b.Run("open_migrated_clone", func(b *testing.B) {
		root := b.TempDir()
		paths := make([]string, b.N)
		for i := range paths {
			paths[i] = filepath.Join(root, strconv.Itoa(i)+".db")
			_, err := template.Clone(paths[i])
			require.NoError(b, err)
		}
		b.ResetTimer()
		for _, path := range paths {
			client := NewClientAtPath(path, slog.New(slog.DiscardHandler))
			_, err := client.ProjectionSourceVersion(b.Context())
			require.NoError(b, err)
			require.NoError(b, client.CloseDB())
		}
	})
}

// newMigratingTestClient retains the genuine legacy-to-current schema path for
// tests whose subject is migration behavior rather than ordinary store use.
func newMigratingTestClient(t *testing.T) *Client {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	schema := []string{
		`CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);`,
		`CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);`,
		`CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
	}
	for _, stmt := range schema {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	return client
}

func newBusyRetryTestClient(t *testing.T) (*Client, <-chan struct{}, chan struct{}) {
	t.Helper()
	retryStarted := make(chan struct{}, 1)
	releaseRetry := make(chan struct{})
	client := newTestClient(t,
		WithSQLiteBusyPolicy(time.Millisecond, time.Hour),
		withSQLiteBusyRetryBudget(5*time.Second),
		withSQLiteBusyWait(func(ctx context.Context, _ time.Duration) error {
			select {
			case retryStarted <- struct{}{}:
			default:
			}
			select {
			case <-releaseRetry:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
	)
	return client, retryStarted, releaseRetry
}

func TestResolveDBPathUsesEnvOverride(t *testing.T) {
	t.Setenv("AZEDARACH_DB_PATH", "/tmp/custom-azedarach.db")
	got, err := resolveDBPath(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "/tmp/custom-azedarach.db", got)
}

func TestClientRefusesConfiguredDBPathThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "azedarach.db")
	aliasPath := filepath.Join(dir, "azedarach-alias.db")
	require.NoError(t, os.WriteFile(dbPath, nil, 0o600))
	require.NoError(t, os.Symlink(dbPath, aliasPath))
	t.Setenv(refuseDBPathEnv, dbPath)

	client := NewClientAtPath(aliasPath, slog.Default())
	_, err := client.dbHandle()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing configured database path")
}

func TestResolveDBPathUsesBaseRepoForWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.MkdirAll(start, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))

	t.Setenv("PATH", "")
	got, err := resolveDBPath(start)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(repo, ".azedarach", "azedarach.db"), got)
}

func TestResolveDBPathFallsBackToGitMarkerWhenGitUnavailable(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.MkdirAll(start, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))

	t.Setenv("PATH", "")
	got, err := resolveDBPath(start)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(repo, ".azedarach", "azedarach.db"), got)
}

func BenchmarkClient_GetManyWithDependencyContextRuntimeLargeProject(b *testing.B) {
	ctx := context.Background()
	dbPath := filepath.Join(b.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())
	b.Cleanup(func() {
		require.NoError(b, client.CloseDB())
	})
	db, err := client.dbHandle()
	require.NoError(b, err)

	const (
		projectID = "proj-large"
		taskCount = 3500
	)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(b, err)
	issueStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, labels_json, implementations_json)
		VALUES (?, ?, '', ?, ?, ?, ?, ?, '[]', '[]')
	`)
	require.NoError(b, err)
	sessionStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	require.NoError(b, err)
	externalRefStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issue_external_refs (issue_id, provider, provider_scope, remote_key, display_key, created_at, updated_at)
		VALUES (?, 'linear', 'team:CKU', ?, ?, ?, ?)
	`)
	require.NoError(b, err)
	depStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type)
		VALUES (?, ?, ?)
	`)
	require.NoError(b, err)
	for i := 0; i < taskCount; i++ {
		id := "bench-" + strconv.Itoa(i)
		_, err = issueStmt.ExecContext(ctx, id, "Benchmark issue "+strconv.Itoa(i), string(domain.StatusOpen), int(domain.P2), string(domain.TypeTask), now, now)
		require.NoError(b, err)
		_, err = sessionStmt.ExecContext(ctx, projectID, "sess-"+strconv.Itoa(i), id, "stopped", now, now)
		require.NoError(b, err)
		_, err = externalRefStmt.ExecContext(ctx, id, "CKU-"+strconv.Itoa(i), "CKU-"+strconv.Itoa(i), now, now)
		require.NoError(b, err)
		if i > 0 {
			_, err = depStmt.ExecContext(ctx, id, "bench-"+strconv.Itoa(i-1), string(domain.DependencyRelatedTo))
			require.NoError(b, err)
		}
	}
	require.NoError(b, issueStmt.Close())
	require.NoError(b, sessionStmt.Close())
	require.NoError(b, externalRefStmt.Close())
	require.NoError(b, depStmt.Close())
	require.NoError(b, tx.Commit())
	require.NoError(b, client.rebuildIssueGraphClosure(ctx, db))

	ids := []string{"bench-101", "bench-501", "bench-1001", "bench-1501", "bench-2001", "bench-2501", "bench-3001", "bench-3201", "bench-3401"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := client.GetManyWithDependencyContextRuntime(ctx, projectID, ids)
		require.NoError(b, err)
		if len(tasks) == 0 {
			b.Fatal("expected context tasks")
		}
	}
}

func BenchmarkClient_RuntimeSummariesLargeProject(b *testing.B) {
	ctx := context.Background()
	dbPath := filepath.Join(b.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())
	b.Cleanup(func() {
		require.NoError(b, client.CloseDB())
	})
	db, err := client.dbHandle()
	require.NoError(b, err)

	const (
		projectID        = "proj-large-runtime-summaries"
		unrelatedCount   = 3500
		graphChildCount  = 40
		graphBlockerStep = 4
	)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(b, err)
	issueStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, labels_json, implementations_json)
		VALUES (?, ?, 'large details omitted by summary reads', ?, ?, ?, ?, ?, '[]', '[]')
	`)
	require.NoError(b, err)
	sessionStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, activity, activity_source, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	require.NoError(b, err)
	depStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type)
		VALUES (?, ?, ?)
	`)
	require.NoError(b, err)

	rootID := "bench-root"
	_, err = issueStmt.ExecContext(ctx, rootID, "Benchmark root", string(domain.StatusInProgress), int(domain.P1), string(domain.TypeEpic), now, now)
	require.NoError(b, err)
	for i := 0; i < graphChildCount; i++ {
		childID := "bench-child-" + strconv.Itoa(i)
		_, err = issueStmt.ExecContext(ctx, childID, "Benchmark child "+strconv.Itoa(i), string(domain.StatusOpen), int(domain.P2), string(domain.TypeTask), now, now)
		require.NoError(b, err)
		_, err = depStmt.ExecContext(ctx, childID, rootID, string(domain.DependencyParentChild))
		require.NoError(b, err)
		_, err = sessionStmt.ExecContext(ctx, projectID, "sess-child-"+strconv.Itoa(i), childID, "stopped", "", "", now, now)
		require.NoError(b, err)
		if i%graphBlockerStep == 0 {
			blockerID := "bench-blocker-" + strconv.Itoa(i)
			_, err = issueStmt.ExecContext(ctx, blockerID, "Benchmark blocker "+strconv.Itoa(i), string(domain.StatusInProgress), int(domain.P2), string(domain.TypeTask), now, now)
			require.NoError(b, err)
			_, err = depStmt.ExecContext(ctx, childID, blockerID, string(domain.DependencyBlocks))
			require.NoError(b, err)
		}
	}
	for i := 0; i < unrelatedCount; i++ {
		id := "bench-unrelated-" + strconv.Itoa(i)
		_, err = issueStmt.ExecContext(ctx, id, "Benchmark unrelated "+strconv.Itoa(i), string(domain.StatusOpen), int(domain.P2), string(domain.TypeTask), now, now)
		require.NoError(b, err)
		_, err = sessionStmt.ExecContext(ctx, projectID, "sess-unrelated-"+strconv.Itoa(i), id, "stopped", "", "", now, now)
		require.NoError(b, err)
		if i > 0 {
			_, err = depStmt.ExecContext(ctx, id, "bench-unrelated-"+strconv.Itoa(i-1), string(domain.DependencyRelatedTo))
			require.NoError(b, err)
		}
	}
	require.NoError(b, issueStmt.Close())
	require.NoError(b, sessionStmt.Close())
	require.NoError(b, depStmt.Close())
	require.NoError(b, tx.Commit())
	require.NoError(b, client.rebuildIssueGraphClosure(ctx, db))

	b.Run("full_project_summaries", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tasks, err := client.ListSummariesWithRuntime(ctx, projectID)
			require.NoError(b, err)
			if len(tasks) < unrelatedCount {
				b.Fatalf("full summary task count = %d, want at least %d", len(tasks), unrelatedCount)
			}
		}
	})
	b.Run("root_graph_readiness", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tasks, err := client.ListGraphReadinessWithRuntime(ctx, projectID, rootID)
			require.NoError(b, err)
			if len(tasks) >= unrelatedCount {
				b.Fatalf("graph summary task count = %d, want scoped graph", len(tasks))
			}
		}
	})
}

func BenchmarkClient_GetManyMetadataWithAncestorContextRuntimeLargeProject(b *testing.B) {
	ctx := context.Background()
	dbPath := filepath.Join(b.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())
	b.Cleanup(func() {
		require.NoError(b, client.CloseDB())
	})
	db, err := client.dbHandle()
	require.NoError(b, err)

	const (
		projectID = "proj-large"
		taskCount = 3500
	)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(b, err)
	issueStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, labels_json, implementations_json)
		VALUES (?, ?, 'large details that selector should not decode', ?, ?, ?, ?, ?, '["selector","ignored"]', '["impl"]')
	`)
	require.NoError(b, err)
	sessionStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	require.NoError(b, err)
	depStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type)
		VALUES (?, ?, ?)
	`)
	require.NoError(b, err)
	for i := 0; i < taskCount; i++ {
		id := "bench-" + strconv.Itoa(i)
		_, err = issueStmt.ExecContext(ctx, id, "Benchmark issue "+strconv.Itoa(i), string(domain.StatusOpen), int(domain.P2), string(domain.TypeTask), now, now)
		require.NoError(b, err)
		_, err = sessionStmt.ExecContext(ctx, projectID, "sess-"+strconv.Itoa(i), id, "stopped", now, now)
		require.NoError(b, err)
		if i > 0 && i%100 != 0 {
			_, err = depStmt.ExecContext(ctx, id, "bench-"+strconv.Itoa(i-(i%100)), string(domain.DependencyParentChild))
			require.NoError(b, err)
		}
	}
	require.NoError(b, issueStmt.Close())
	require.NoError(b, sessionStmt.Close())
	require.NoError(b, depStmt.Close())
	require.NoError(b, tx.Commit())
	require.NoError(b, client.rebuildIssueGraphClosure(ctx, db))

	ids := []string{"bench-100", "bench-500", "bench-1000", "bench-1500", "bench-2000", "bench-2500", "bench-3000", "bench-3200", "bench-3400"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := client.GetManyMetadataWithAncestorContextRuntime(ctx, projectID, ids)
		require.NoError(b, err)
		if len(tasks) == 0 {
			b.Fatal("expected metadata tasks")
		}
	}
}

func taskIDStrings(tasks []domain.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID.String())
	}
	return out
}

func taskByID(tasks []domain.Task) map[string]domain.Task {
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	return byID
}
