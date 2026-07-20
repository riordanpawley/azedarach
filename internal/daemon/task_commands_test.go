package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	opstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
	"github.com/riordanpawley/azedarach/internal/testutil/issuefixture"
)

func TestBuildTaskSnapshotExportBodyIsDeterministic(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC)
	}
	wantCapturedAt := time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC).UnixMilli()

	parentID := naming.IssueID("parent-1")
	body := buildTaskSnapshotExportBody(
		"proj",
		17,
		[]domain.Task{
			{
				ID:           "b-task",
				Title:        "Bravo",
				Status:       domain.StatusOpen,
				Priority:     domain.P2,
				Type:         domain.TypeTask,
				UpdatedAt:    time.Now(),
				Dependencies: []domain.Dependency{{ID: "dep-1", Type: domain.DependencyBlocks}},
			},
			{
				ID:           "a-task",
				Title:        "Alpha",
				Status:       domain.StatusInProgress,
				Priority:     domain.P1,
				Type:         domain.TypeBug,
				ParentID:     &parentID,
				Dependencies: []domain.Dependency{{ID: "dep-2", Type: domain.DependencyRelatedTo}, {ID: "dep-3", Type: domain.DependencyBlocks}},
			},
		},
		[]string{"b-task", "a-task"},
		"/tmp/proj",
	)

	if got, want := body.SchemaVersion, uint16(1); got != want {
		t.Fatalf("SchemaVersion = %d, want %d", got, want)
	}
	if got, want := body.SnapshotRevision, uint64(17); got != want {
		t.Fatalf("SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := body.CapturedAtMs, wantCapturedAt; got != want {
		t.Fatalf("CapturedAtMs = %d, want %d", got, want)
	}
	if got, want := body.ProjectID, "proj"; got != want {
		t.Fatalf("ProjectID = %q, want %q", got, want)
	}
	if got, want := body.TaskCount, 2; got != want {
		t.Fatalf("TaskCount = %d, want %d", got, want)
	}
	if got, want := body.SessionCount, 2; got != want {
		t.Fatalf("SessionCount = %d, want %d", got, want)
	}

	if got, want := len(body.Tasks), 2; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].ID, "a-task"; got != want {
		t.Fatalf("Tasks[0].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[0].SessionAttached, true; got != want {
		t.Fatalf("Tasks[0].SessionAttached = %v, want %v", got, want)
	}
	if got, want := body.Tasks[0].DependencyCount, 2; got != want {
		t.Fatalf("Tasks[0].DependencyCount = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].Critical, false; got != want {
		t.Fatalf("Tasks[0].Critical = %v, want %v", got, want)
	}
	if body.Tasks[0].ParentID == nil || *body.Tasks[0].ParentID != parentID.String() {
		t.Fatalf("Tasks[0].ParentID = %+v, want %q", body.Tasks[0].ParentID, parentID.String())
	}
	if got, want := body.Tasks[1].ID, "b-task"; got != want {
		t.Fatalf("Tasks[1].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[1].Critical, false; got != want {
		t.Fatalf("Tasks[1].Critical = %v, want %v", got, want)
	}

	if got, want := len(body.Sessions), 2; got != want {
		t.Fatalf("len(Sessions) = %d, want %d", got, want)
	}
	if got, want := body.Sessions[0].Name, "a-task"; got != want {
		t.Fatalf("Sessions[0].Name = %q, want %q", got, want)
	}
	if got, want := body.Sessions[1].Name, "b-task"; got != want {
		t.Fatalf("Sessions[1].Name = %q, want %q", got, want)
	}

	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal snapshot body: %v", err)
	}
}

func TestWorkerObservationDecisionWaitingIsDistinctFromRuntimePrompt(t *testing.T) {
	taskID, err := naming.ParseIssueID("abc")
	if err != nil {
		t.Fatal(err)
	}
	got := daemonWorkerObservationFromInputs(workerObservationInputs{Task: domain.Task{ID: taskID, Status: domain.StatusOpen, Priority: domain.P2}, Runnable: true, DecisionWaiting: true})
	if got.State != domain.WorkerObservationWaitingHuman || got.WaitingHumanSource != "interaction_request" {
		t.Fatalf("observation = %+v", got)
	}
}

func TestProjectIssueStoreMigrationFailureSuppressesRepeatedPolls(t *testing.T) {
	originalNow := timeNow
	now := time.Date(2026, 7, 7, 6, 0, 0, 0, time.UTC)
	timeNow = func() time.Time {
		return now
	}
	t.Cleanup(func() {
		timeNow = originalNow
	})

	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	seedIssueStoreInvalidGraphClosureObject(t, dbPath)
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := issues.NewClientAtPath(dbPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		panes:       map[string][]string{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		tmux:   tmux.NewClient(tmuxRunner, slog.New(slog.NewTextHandler(io.Discard, nil))),
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		issueClientsByRoot: map[string]*issues.Client{
			daemonStoreRootKey(repoDir): issuesClient,
		},
		projectIssueStoreHealthByProject: map[string]projectIssueStoreHealthState{},
		revision:                         map[string]uint64{projectID: 1},
	}

	first, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-broken-task-list",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleTaskList returned error: %v", err)
	}
	if first.OK || first.Error == nil {
		t.Fatalf("first task.list response OK = %v, error = %+v; want migration error", first.OK, first.Error)
	}
	if got := first.Error.Message; !strings.Contains(got, "apply migration 0018_issue_graph_closure") || !strings.Contains(got, "issue_graph_closure") {
		t.Fatalf("first task.list error = %q, want issue_graph_closure migration context", got)
	}

	removeSQLiteFiles(t, dbPath)
	board, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cached-board-fetch",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandBoardFetch,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleBoardFetch returned error: %v", err)
	}
	if board.OK || board.Error == nil {
		t.Fatalf("cached board.fetch response OK = %v, error = %+v; want cached health error", board.OK, board.Error)
	}
	if got := board.Error.Message; !strings.Contains(got, "project issue store unhealthy (cached)") {
		t.Fatalf("cached board.fetch error = %q, want cached health message", got)
	}

	second, err := d.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cached-session-status",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if second.OK || second.Error == nil {
		t.Fatalf("cached session.status response OK = %v, error = %+v; want cached health error", second.OK, second.Error)
	}
	if got := second.Error.Message; !strings.Contains(got, "project issue store unhealthy (cached)") || !strings.Contains(got, "suppressing repeated polling") {
		t.Fatalf("cached session.status error = %q, want cached health message", got)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 0 {
		t.Fatalf("tmux list-sessions calls after cached health = %d, want 0", got)
	}

	now = now.Add(projectIssueStoreHealthBackoff + time.Second)
	third, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-retry-after-backoff",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("retry task.list returned error: %v", err)
	}
	if !third.OK {
		t.Fatalf("retry task.list response error = %+v, want success after backoff expiry", third.Error)
	}
}

func seedIssueStoreInvalidGraphClosureObject(t *testing.T, dbPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s", filepath.ToSlash(dbPath)))
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
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
		CREATE VIEW issue_graph_closure AS
			SELECT
				'default' AS project_id,
				'' AS ancestor_id,
				'' AS descendant_id,
				'parent-child' AS dependency_type,
				0 AS depth,
				'1970-01-01T00:00:00Z' AS updated_at
			WHERE 0;
	`); err != nil {
		t.Fatalf("create legacy issue store fixture: %v", err)
	}
	applied := []string{
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
	}
	for _, id := range applied {
		if _, err := db.Exec(`INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, id, "2026-07-07T00:00:00Z"); err != nil {
			t.Fatalf("seed migration %s: %v", id, err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO issues (
			id, title, description, notes, design, acceptance, assignee, labels_json,
			status, priority, issue_type, created_at, updated_at
		)
		VALUES ('cvn-fixture', 'Broken migration fixture', '', '', '', '', '', '[]', 'open', 1, 'bug', '2026-07-07T00:00:00Z', '2026-07-07T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed legacy issue row: %v", err)
	}
}

func removeSQLiteFiles(t *testing.T, dbPath string) {
	t.Helper()
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
}

func TestValidateDependencyEndpointProjectsRejectsCrossProjectRefs(t *testing.T) {
	tests := []struct {
		name             string
		requestProject   string
		issueProject     string
		dependsOnProject string
		wantErr          string
	}{
		{
			name:             "same declared project passes",
			requestProject:   "chefy",
			issueProject:     "chefy",
			dependsOnProject: "chefy",
		},
		{
			name:             "mixed endpoint projects rejected",
			requestProject:   "chefy",
			issueProject:     "chefy",
			dependsOnProject: "azedarach",
			wantErr:          "dependency endpoints must be in the same project",
		},
		{
			name:             "issue endpoint project must match request project",
			requestProject:   "azedarach",
			issueProject:     "chefy",
			dependsOnProject: "chefy",
			wantErr:          `issue_project_id "chefy" does not match request project "azedarach"`,
		},
		{
			name:             "depends endpoint project must match request project",
			requestProject:   "azedarach",
			dependsOnProject: "chefy",
			wantErr:          `depends_on_project_id "chefy" does not match request project "azedarach"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDependencyEndpointProjects(tt.requestProject, tt.issueProject, tt.dependsOnProject)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateDependencyEndpointProjects() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateDependencyEndpointProjects() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestHandleTaskDependencyAddRejectsCrossProjectEndpointMetadata(t *testing.T) {
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			"chefy": issuesClient,
		},
		revision: map[string]uint64{"chefy": 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]any{
		"task_id":               "az-1",
		"depends_on_id":         "az-2",
		"dependency_type":       "blocks",
		"issue_project_id":      "chefy",
		"depends_on_project_id": "azedarach",
	})
	if err != nil {
		t.Fatalf("marshal dependency add body: %v", err)
	}

	resp, err := d.handleTaskDependencyAdd(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cross-project-dep",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID("chefy")},
		Command:         "task.dependency.add",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDependencyAdd error: %v", err)
	}
	if resp.OK {
		t.Fatalf("handleTaskDependencyAdd OK = true, want rejection")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest || !strings.Contains(resp.Error.Message, "dependency endpoints must be in the same project") {
		t.Fatalf("response error = %+v, want invalid cross-project dependency", resp.Error)
	}
}

func TestBuildTaskSnapshotExportBody_ProjectScopedSessionPrefixMatchesIssue(t *testing.T) {
	body := buildTaskSnapshotExportBody(
		"Chefy",
		1,
		[]domain.Task{
			{
				ID:       "em",
				Title:    "Issue with tmux session",
				Status:   domain.StatusInProgress,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		},
		[]string{"ch-em"},
		"Chefy",
	)

	if got, want := len(body.Tasks), 1; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if !body.Tasks[0].SessionAttached {
		t.Fatalf("SessionAttached = false, want true")
	}
}

func TestBuildBoardSnapshotPayloadOmitsDetailFields(t *testing.T) {
	payload, err := buildBoardSnapshotPayload(
		"proj-board",
		12,
		time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC),
		protocol.TaskListFreshnessFresh,
		[]domain.Task{{
			ID:          "az-board",
			Title:       "Board task",
			Description: "large description",
			Notes:       "large notes",
			Design:      "large design",
			Acceptance:  "large acceptance",
			Status:      domain.StatusInProgress,
			Priority:    domain.P1,
			Type:        domain.TypeTask,
		}},
		domain.DefaultBoardView(),
	)
	if err != nil {
		t.Fatalf("build board payload: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal board payload: %v", err)
	}
	for _, field := range []string{"description", "notes", "design", "acceptance"} {
		if bytes.Contains(body, []byte(field)) {
			t.Fatalf("board payload contains %q field: %s", field, string(body))
		}
	}

	decoded, err := protocol.DecodeBoardSnapshotPayload(body)
	if err != nil {
		t.Fatalf("decode board payload: %v", err)
	}
	if got, want := decoded.SchemaVersion, uint16(protocol.BoardSnapshotSchemaVersion); got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	tasks := decoded.Projection.TaskSummaries()
	columns := decoded.Projection.ColumnSnapshots()
	if got, want := len(tasks), 1; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}
	if got, want := string(decoded.Projection.View.ID), domain.DefaultBoardViewID; got != want {
		t.Fatalf("view id = %q, want %q", got, want)
	}
	if got := len(columns); got == 0 {
		t.Fatal("expected grouped board columns")
	}
	if got, want := tasks[0].Title, "Board task"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestBuildBoardSnapshotPayloadAppliesViewSortPolicy(t *testing.T) {
	payload, err := buildBoardSnapshotPayload(
		"proj-board",
		13,
		time.Now().UTC(),
		protocol.TaskListFreshnessFresh,
		[]domain.Task{
			{ID: "ordinary", Status: domain.StatusInProgress, Priority: domain.P0},
			{ID: "waiting", Status: domain.StatusInProgress, Priority: domain.P4, Session: &domain.Session{Activity: "waiting-for-human"}},
		},
		domain.DefaultBoardView(),
	)
	if err != nil {
		t.Fatalf("build board payload: %v", err)
	}
	for _, column := range payload.Projection.ColumnSnapshots() {
		if column.Definition.ID != domain.BoardColumnActive {
			continue
		}
		if len(column.Tasks) != 2 || column.Tasks[0].ID != "waiting" || column.Tasks[1].ID != "ordinary" {
			t.Fatalf("active snapshot order = %+v, want waiting then ordinary", column.Tasks)
		}
		return
	}
	t.Fatal("active board column not found")
}

func TestHandleBoardFetchGroupsBySelectedView(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("CloseDB error: %v", err)
		}
	})
	projectID := "proj-board-selected"
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Open issue",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		Priority: domain.P2,
	}); err != nil {
		t.Fatalf("create open issue: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Active issue",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		Priority: domain.P1,
	}); err != nil {
		t.Fatalf("create active issue: %v", err)
	}
	customView := domain.BoardView{
		ID:    "active-only",
		Title: "Active Only",
		Columns: []domain.BoardColumn{{
			ID:    domain.BoardColumnActive,
			Title: "Active",
			Predicates: []domain.BoardColumnPredicate{{
				Kind:          domain.BoardPredicateDisplayPhase,
				DisplayPhases: []domain.IssueDisplayPhase{domain.IssueDisplayActive},
			}},
		}},
	}
	if _, err := issuesClient.SaveBoardView(ctx, projectID, customView); err != nil {
		t.Fatalf("SaveBoardView error: %v", err)
	}
	repoDir := t.TempDir()
	d := &Daemon{
		cfg: Config{
			Logger:  logger,
			RepoDir: repoDir,
		},
		issues:                issuesClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		uiState:               map[string]string{},
		revision:              map[string]uint64{},
	}
	if err := d.setSelectedBoardViewID(projectID, "active-only"); err != nil {
		t.Fatalf("setSelectedBoardViewID error: %v", err)
	}
	d = &Daemon{
		cfg: Config{
			Logger:  logger,
			RepoDir: repoDir,
		},
		issues:                issuesClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		uiState:               map[string]string{},
		revision:              map[string]uint64{},
	}

	resp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("board-fetch-selected"),
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandBoardFetch,
		SentAt:          time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("handleBoardFetch error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleBoardFetch response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeBoardSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("DecodeBoardSnapshotPayload error: %v", err)
	}
	if got, want := string(payload.Projection.View.ID), "active-only"; got != want {
		t.Fatalf("payload view id = %q, want %q", got, want)
	}
	columns := payload.Projection.ColumnSnapshots()
	tasks := payload.Projection.TaskSummaries()
	if got, want := len(columns), 1; got != want {
		t.Fatalf("len(columns) = %d, want %d", got, want)
	}
	if got, want := len(columns[0].Tasks), 1; got != want {
		t.Fatalf("len(active tasks) = %d, want %d", got, want)
	}
	if got, want := columns[0].Tasks[0].Title, "Active issue"; got != want {
		t.Fatalf("active task title = %q, want %q", got, want)
	}
	if tasks[0].Facts.DisplayPhase != domain.IssueDisplayActive {
		t.Fatalf("issue facts display phase = %s, want %s", tasks[0].Facts.DisplayPhase, domain.IssueDisplayActive)
	}
	if !slices.Contains(tasks[0].Facts.ReasonMessages(), "lifecycle is active") {
		t.Fatalf("issue facts reasons = %#v, want lifecycle reason", tasks[0].Facts.ReasonMessages())
	}
}

func TestHandleBoardFetchFallsBackToDefaultWhenSelectedViewPreferenceIsCorrupt(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("CloseDB error: %v", err)
		}
	})
	projectID := "proj-board-corrupt-pref"
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Open issue",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		Priority: domain.P2,
	}); err != nil {
		t.Fatalf("create open issue: %v", err)
	}
	repoDir := t.TempDir()
	prefPath := filepath.Join(repoDir, ".azedarach", boardViewPreferenceFileName)
	if err := os.MkdirAll(filepath.Dir(prefPath), 0o755); err != nil {
		t.Fatalf("mkdir pref dir: %v", err)
	}
	if err := os.WriteFile(prefPath, []byte(`{"selected_view_by_project":`), 0o644); err != nil {
		t.Fatalf("write corrupt preference: %v", err)
	}
	d := &Daemon{
		cfg: Config{
			Logger:  logger,
			RepoDir: repoDir,
		},
		issues:                issuesClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		uiState:               map[string]string{},
		revision:              map[string]uint64{},
	}

	resp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("board-fetch-corrupt-pref"),
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandBoardFetch,
		SentAt:          time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("handleBoardFetch error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleBoardFetch response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeBoardSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("DecodeBoardSnapshotPayload error: %v", err)
	}
	if got, want := string(payload.Projection.View.ID), domain.DefaultBoardViewID; got != want {
		t.Fatalf("payload view id = %q, want %q", got, want)
	}
	if got, want := len(payload.Projection.Groups), len(domain.DefaultBoardView().Columns); got != want {
		t.Fatalf("len(columns) = %d, want default view column count %d", got, want)
	}
}

func TestHandleTaskListIsReadOnlyAndUsesProjectionData(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 11, 2, 5, 0, time.UTC)
	}

	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	projectID := "proj-read-only"
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "Read-only task list",
		Description: "Long description should stay out of task.list summaries",
		Design:      "Long design should stay out of task.list summaries",
		Notes:       "Long notes should stay out of task.list summaries",
		Acceptance:  "Long acceptance should stay out of task.list summaries",
		Type:        domain.TypeTask,
		Priority:    domain.P1,
		Status:      domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sessionID := naming.CanonicalSessionID(projectID, taskID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	sessionStartedAt := time.Date(2026, time.April, 2, 10, 59, 0, 0, time.UTC)
	sessionUpdatedAt := time.Date(2026, time.April, 2, 11, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &sessionStartedAt,
		UpdatedAt: sessionUpdatedAt,
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	rawStatus, err := json.Marshal(git.GitStatus{
		Added:          []string{"new.go"},
		Modified:       []string{"changed.go"},
		Staged:         []string{"staged.go"},
		Deleted:        []string{"removed.go"},
		HasChanges:     true,
		GitAdditions:   9,
		GitDeletions:   2,
		GitAheadCount:  4,
		GitBehindCount: 1,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	worktreePath := "/tmp/proj-read-only-" + taskID
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    "riordan/" + taskID + "/read-only",
		UpdatedAt: time.Date(2026, time.April, 2, 11, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed projected worktree: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, rawStatus, time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed projected worktree status: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  logger,
		},
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		revision: map[string]uint64{projectID: 7},
		tmux:     nil,
		git:      &git.Client{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			".": &git.WorktreeManager{},
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: &git.WorktreeManager{},
		},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}
	if got, want := resp.Revision, uint64(7); got != want {
		t.Fatalf("response revision = %d, want %d", got, want)
	}
	if got, want := d.currentRevision(projectID), uint64(7); got != want {
		t.Fatalf("daemon revision = %d, want %d", got, want)
	}

	var payload protocol.TaskListSnapshotPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal task list body: %v", err)
	}
	if got, want := payload.SchemaVersion, protocol.TaskListSnapshotSchemaVersion; got != want {
		t.Fatalf("payload.SchemaVersion = %d, want %d", got, want)
	}
	if got, want := payload.ProjectID.String(), projectID; got != want {
		t.Fatalf("payload.ProjectID = %q, want %q", got, want)
	}
	if got, want := payload.SnapshotRevision, uint64(7); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	wantLastCheckedAt := time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	if got, want := payload.LastCheckedAt, wantLastCheckedAt; !got.Equal(want) {
		t.Fatalf("payload.LastCheckedAt = %v, want %v", got, want)
	}
	if got, want := payload.Freshness, protocol.TaskListFreshnessFresh; got != want {
		t.Fatalf("payload.Freshness = %q, want %q", got, want)
	}
	if !payload.SummariesOnly {
		t.Fatal("payload.SummariesOnly = false, want true")
	}
	tasks := payload.Tasks
	if got, want := len(tasks), 1; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}

	task := tasks[0]
	if task.ID.String() != taskID {
		t.Fatalf("task.ID = %q, want %q", task.ID, taskID)
	}
	if task.Title != "Read-only task list" {
		t.Fatalf("task.Title = %q, want %q", task.Title, "Read-only task list")
	}
	if task.Description != "" || task.Design != "" || task.Notes != "" || task.Acceptance != "" {
		t.Fatalf("task.list returned full detail fields: description=%q design=%q notes=%q acceptance=%q", task.Description, task.Design, task.Notes, task.Acceptance)
	}
	if task.Session == nil {
		t.Fatal("expected task session projection")
	}
	if task.Session.State != domain.SessionBusy {
		t.Fatalf("task.Session.State = %q, want %q", task.Session.State, domain.SessionBusy)
	}
	if task.Session.StartedAt == nil || !task.Session.StartedAt.Equal(sessionStartedAt.UTC()) {
		t.Fatalf("task.Session.StartedAt = %v, want %v", task.Session.StartedAt, sessionStartedAt.UTC())
	}
	if task.Session.Worktree != worktreePath {
		t.Fatalf("task.Session.Worktree = %q, want %q", task.Session.Worktree, worktreePath)
	}
	if !task.HasWorktree {
		t.Fatal("task.HasWorktree = false, want true")
	}
	if !task.HasUncommittedChanges {
		t.Fatal("task.HasUncommittedChanges = false, want true")
	}
	if got, want := task.GitAdditions, 9; got != want {
		t.Fatalf("task.GitAdditions = %d, want %d", got, want)
	}
	if got, want := task.GitDeletions, 2; got != want {
		t.Fatalf("task.GitDeletions = %d, want %d", got, want)
	}
	if got, want := task.GitAheadCount, 4; got != want {
		t.Fatalf("task.GitAheadCount = %d, want %d", got, want)
	}
	if got, want := task.GitBehindCount, 1; got != want {
		t.Fatalf("task.GitBehindCount = %d, want %d", got, want)
	}

	sessions, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list projected sessions: %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("projected session count = %d, want %d", got, want)
	}
	if sessions[0].State != daemonstate.SessionStateAttached {
		t.Fatalf("projected session state = %q, want %q", sessions[0].State, daemonstate.SessionStateAttached)
	}
	if !sessions[0].UpdatedAt.Equal(sessionUpdatedAt) {
		t.Fatalf("projected session updated_at = %v, want %v", sessions[0].UpdatedAt, sessionUpdatedAt)
	}

	worktrees, err := runtimeStateStore.ListWorktreeStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list projected worktrees: %v", err)
	}
	if got, want := len(worktrees), 1; got != want {
		t.Fatalf("projected worktree count = %d, want %d", got, want)
	}
	if worktrees[0].IssueID != taskID {
		t.Fatalf("projected worktree issue_id = %q, want %q", worktrees[0].IssueID, taskID)
	}
	if !bytes.Equal(worktrees[0].GitStatusRaw, rawStatus) {
		t.Fatalf("projected worktree git status was mutated")
	}

	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	getResp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-after-summary-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !getResp.OK {
		t.Fatalf("task.get response = %+v", getResp.Error)
	}
	getPayload, err := protocol.DecodeTaskListSnapshotPayload(getResp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if len(getPayload.Tasks) == 0 {
		t.Fatal("task.get returned no tasks")
	}
	if getPayload.SummariesOnly {
		t.Fatal("task.get summaries_only = true, want false")
	}
	if got, want := getPayload.Tasks[0].Description, "Long description should stay out of task.list summaries"; got != want {
		t.Fatalf("task.get description = %q, want %q", got, want)
	}
	if got, want := getPayload.Tasks[0].Design, "Long design should stay out of task.list summaries"; got != want {
		t.Fatalf("task.get design = %q, want %q", got, want)
	}
}

func TestHandleTaskListAndGetSupportArchivedOnly(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	projectID := "proj-archived-only"
	activeID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "active issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create active issue: %v", err)
	}
	archivedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "archived issue",
		Description: "archived details",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("create archived issue: %v", err)
	}
	if err := issuesClient.Archive(ctx, archivedID); err != nil {
		t.Fatalf("archive issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  logger,
		},
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 3},
		git:          &git.Client{},
	}

	listBody, err := json.Marshal(protocol.TaskListRequestBody{Archived: string(protocol.ArchiveModeOnly)})
	if err != nil {
		t.Fatalf("marshal task.list request: %v", err)
	}
	listResp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-archived-only",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
		Body:            listBody,
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !listResp.OK {
		t.Fatalf("task.list response = %+v", listResp.Error)
	}
	listPayload, err := protocol.DecodeTaskListSnapshotPayload(listResp.Body)
	if err != nil {
		t.Fatalf("decode task.list body: %v", err)
	}
	if got := taskIDStrings(listPayload.Tasks); len(got) != 1 || got[0] != archivedID {
		t.Fatalf("task.list ids = %v, want [%s]; active id was %s", got, archivedID, activeID)
	}

	getBody, err := json.Marshal(map[string]string{"task_id": archivedID, "archived": string(protocol.ArchiveModeOnly)})
	if err != nil {
		t.Fatalf("marshal task.get request: %v", err)
	}
	getResp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-archived-only",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            getBody,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !getResp.OK {
		t.Fatalf("task.get response = %+v", getResp.Error)
	}
	getPayload, err := protocol.DecodeTaskListSnapshotPayload(getResp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if got := taskIDStrings(getPayload.Tasks); len(got) != 1 || got[0] != archivedID {
		t.Fatalf("task.get ids = %v, want [%s]", got, archivedID)
	}
	if got := getPayload.Tasks[0].Description; got != "archived details" {
		t.Fatalf("task.get description = %q, want archived details", got)
	}
}

func TestHandleTaskUnarchiveRestoresArchivedIssue(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	projectID := "proj-unarchive"
	archivedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "archived issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("create archived issue: %v", err)
	}
	if err := issuesClient.Archive(ctx, archivedID); err != nil {
		t.Fatalf("archive issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  logger,
		},
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		hub:          publish.NewHub(16, 8, logger),
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 9},
		git:          &git.Client{},
	}
	events, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	body, err := json.Marshal(map[string]string{"task_id": archivedID})
	if err != nil {
		t.Fatalf("marshal task.unarchive request: %v", err)
	}
	resp, err := d.handleTaskUnarchive(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-unarchive",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.unarchive",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUnarchive error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.unarchive response = %+v", resp.Error)
	}
	if resp.Revision != 10 {
		t.Fatalf("revision = %d, want 10", resp.Revision)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, archivedID)
	if err != nil {
		t.Fatalf("unarchived issue not active: %v", err)
	}
	if task.Title != "archived issue" {
		t.Fatalf("title = %q, want archived issue", task.Title)
	}
	select {
	case evt := <-events:
		if evt.Event != protocol.EventTaskRestored || evt.Revision != 10 {
			t.Fatalf("published event = %+v, want %s revision 10", evt, protocol.EventTaskRestored)
		}
		var taskBody protocol.TaskEventBody
		if err := json.Unmarshal(evt.Body, &taskBody); err != nil {
			t.Fatalf("unmarshal task event body: %v", err)
		}
		if taskBody.TaskID.String() != archivedID || taskBody.Task == nil || taskBody.Task.ID.String() != archivedID {
			t.Fatalf("task event body = %+v, want restored task %s", taskBody, archivedID)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s event", protocol.EventTaskRestored)
	}
}

func TestHandleTaskUnarchiveRejectsArchivedParentWithoutFlag(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-unarchive-parent-guard"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Archived parent",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Archived child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, "parent-child"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	if err := issuesClient.Archive(ctx, childID); err != nil {
		t.Fatalf("archive child: %v", err)
	}
	if err := issuesClient.Archive(ctx, parentID); err != nil {
		t.Fatalf("archive parent: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		hub: publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]string{"task_id": childID})
	if err != nil {
		t.Fatalf("marshal task.unarchive request: %v", err)
	}
	resp, err := d.handleTaskUnarchive(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-unarchive-parent-guard",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.unarchive",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUnarchive error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("task.unarchive response = %+v, want conflict", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
	if !strings.Contains(resp.Error.Message, "unarchive the parent first") {
		t.Fatalf("error message = %q", resp.Error.Message)
	}
}

func TestHandleTaskListConsumesAsynchronousMissingTmuxObservation(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-task-stale-session"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Task list stale session",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: logger},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)
	if err := d.observeTmuxProject(ctx, projectID, newTmuxRuntimeLiveness(nil, nil), domain.CurrentTmuxObservationProvenance(time.Now().UTC().Add(time.Second))); err != nil {
		t.Fatalf("observe missing tmux session: %v", err)
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-stale-session",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}

	var payload protocol.TaskListSnapshotPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal task list body: %v", err)
	}
	if len(payload.Tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(payload.Tasks))
	}
	if payload.Tasks[0].HasTmuxSession || payload.Tasks[0].Session != nil {
		t.Fatalf("task runtime session = %+v has_tmux=%v, want inactive after live tmux refresh", payload.Tasks[0].Session, payload.Tasks[0].HasTmuxSession)
	}

	row, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get projected session: %v", err)
	}
	if !found {
		t.Fatal("expected projected session row")
	}
	if row.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("observed state = %s, want stopped", row.ObservedState)
	}
}

func TestTaskRuntimeCleanupRepairsPurgeManagedIdentityForMissingSession(t *testing.T) {
	for _, tt := range []struct {
		name   string
		repair func(*Daemon, context.Context, string, string) error
	}{
		{name: "close preflight", repair: (*Daemon).repairStaleSessionRuntimeProjections},
		{name: "post cleanup", repair: (*Daemon).repairStaleRuntimeProjections},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			const projectID, taskID, sessionID = "cleanup-project", "az-1", "cleanup-session"
			store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() { _ = store.Close() })
			observedAt := time.Date(2026, time.January, 1, 12, 30, 0, 0, time.UTC)
			if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
				ID: sessionID, IssueID: taskID, State: daemonstate.SessionStateRunning,
				ObservedState: daemonstate.SessionStateRunning, UpdatedAt: observedAt,
			}); err != nil {
				t.Fatal(err)
			}
			d := &Daemon{
				cfg: Config{Logger: slog.Default()}, tmux: tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, slog.Default()),
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
			}
			d.recordManagedAgentIdentityProjection(daemonstate.ManagedAgentIdentity{
				ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "7",
				PanePID: 123, AgentIncarnation: "cleanup-incarnation", ObservedAt: observedAt,
			}, true)
			if err := tt.repair(d, ctx, projectID, taskID); err != nil {
				t.Fatalf("repair missing task session: %v", err)
			}
			projected, found, err := store.GetSessionState(ctx, projectID, sessionID)
			if err != nil || !found || projected.ObservedState != daemonstate.SessionStateStopped {
				t.Fatalf("terminal task session projection = %+v found=%t err=%v", projected, found, err)
			}
			if _, found := d.projectedManagedAgentIdentity(projectID, sessionID, "agent"); found {
				t.Fatal("task runtime cleanup retained managed-agent identity projection")
			}
		})
	}
}

func TestHandleTaskListThrottlesSessionRuntimeRefresh(t *testing.T) {
	originalNow := timeNow
	now := time.Date(2026, time.July, 7, 3, 30, 0, 0, time.UTC)
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = originalNow })

	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-task-list-refresh-throttle"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Task list refresh throttle",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	tmuxRunner := &testTmuxRunner{sessions: map[string]bool{}}
	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: logger},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-refresh-throttle",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	}
	if resp, err := d.handleTaskList(ctx, req); err != nil {
		t.Fatalf("first handleTaskList error: %v", err)
	} else if !resp.OK {
		t.Fatalf("first task.list response = %+v", resp.Error)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 0 {
		t.Fatalf("tmux list-sessions calls after first task.list = %d, want 0", got)
	}

	now = now.Add(time.Second)
	req.RequestID = "req-task-list-refresh-throttle-second"
	if resp, err := d.handleTaskList(ctx, req); err != nil {
		t.Fatalf("second handleTaskList error: %v", err)
	} else if !resp.OK {
		t.Fatalf("second task.list response = %+v", resp.Error)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 0 {
		t.Fatalf("tmux list-sessions calls after second task.list = %d, want 0", got)
	}

	now = now.Add(taskListRuntimeRefreshTTL + time.Millisecond)
	req.RequestID = "req-task-list-refresh-throttle-third"
	if resp, err := d.handleTaskList(ctx, req); err != nil {
		t.Fatalf("third handleTaskList error: %v", err)
	} else if !resp.OK {
		t.Fatalf("third task.list response = %+v", resp.Error)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 0 {
		t.Fatalf("tmux list-sessions calls after observation age expiry = %d, want 0", got)
	}
}

func TestTaskListSessionRuntimeRefreshReadsObserverFreshnessOnly(t *testing.T) {
	now := time.Date(2026, time.July, 7, 3, 45, 0, 0, time.UTC)
	d := &Daemon{taskListRuntimeLastRefresh: map[string]time.Time{"proj": now}}
	runtimeAt, refreshed, err := d.refreshTaskListSessionRuntimeState(context.Background(), "proj")
	if err != nil || refreshed || !runtimeAt.Equal(now) {
		t.Fatalf("runtime freshness = %v refreshed=%v err=%v, want observer time without refresh", runtimeAt, refreshed, err)
	}
}

type countingRuntimeProjectionWriter struct {
	*daemonRuntimeProjectionWriter

	mu             sync.Mutex
	sessionPersist int
}

func (w *countingRuntimeProjectionWriter) PersistSessionProjection(ctx context.Context, projectID string, session daemonstate.Session) error {
	w.mu.Lock()
	w.sessionPersist++
	w.mu.Unlock()
	return w.daemonRuntimeProjectionWriter.PersistSessionProjection(ctx, projectID, session)
}

func (w *countingRuntimeProjectionWriter) sessionPersistCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionPersist
}

func TestRefreshExistingSessionRuntimeStateSkipsUnchangedProjectionWrites(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-session-refresh-skip"
	liveIssueID := "az-live"
	staleIssueID := "az-stale"
	liveSessionID := naming.CanonicalSessionID(projectID, liveIssueID)
	staleSessionID := naming.CanonicalSessionID(projectID, staleIssueID)
	seededAt := time.Date(2026, time.April, 2, 11, 0, 0, 0, time.UTC)

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	for _, session := range []daemonstate.Session{
		{
			ID:            liveSessionID,
			IssueID:       liveIssueID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     seededAt,
		},
		{
			ID:            staleSessionID,
			IssueID:       staleIssueID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     seededAt,
		},
	} {
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, session); err != nil {
			t.Fatalf("seed session %s: %v", session.ID, err)
		}
		if _, _, err := runtimeStateStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
			ProjectID: projectID, SessionID: session.ID, ObservedState: daemonstate.SessionStateRunning, UpdatedAt: seededAt,
		}); err != nil {
			t.Fatalf("seed physical observation %s: %v", session.ID, err)
		}
	}

	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			liveSessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}
	writer := &countingRuntimeProjectionWriter{daemonRuntimeProjectionWriter: newRuntimeProjectionWriter(d)}
	d.runtimeProjectionWriter = writer

	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil {
		t.Fatalf("refreshExistingSessionRuntimeState: %v", err)
	}
	if got := writer.sessionPersistCount(); got != 0 {
		t.Fatalf("logical session persists = %d, runtime observations must use physical authority", got)
	}

	live, found, err := runtimeStateStore.GetSessionState(ctx, projectID, liveSessionID)
	if err != nil {
		t.Fatalf("get live session: %v", err)
	}
	if !found {
		t.Fatal("live session missing")
	}
	if !live.UpdatedAt.Equal(seededAt) {
		t.Fatalf("live updated_at = %v, want unchanged %v", live.UpdatedAt, seededAt)
	}

	stale, found, err := runtimeStateStore.GetSessionState(ctx, projectID, staleSessionID)
	if err != nil {
		t.Fatalf("get stale session: %v", err)
	}
	if !found {
		t.Fatal("stale session missing")
	}
	if stale.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("stale observed state = %s, want stopped", stale.ObservedState)
	}
	if !stale.UpdatedAt.After(seededAt) {
		t.Fatalf("stale updated_at = %v, want after %v", stale.UpdatedAt, seededAt)
	}
}

func TestHandleTaskListKeepsFreshResponseWhenRuntimeRefreshSkipsUnchangedRows(t *testing.T) {
	originalNow := timeNow
	now := time.Date(2026, time.April, 2, 11, 5, 0, 0, time.UTC)
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = originalNow })

	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-session-refresh-fresh"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "unchanged live session stays fresh",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	seededAt := now.Add(-time.Minute)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     seededAt,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			sessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: logger},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)
	d.taskListRuntimeLastRefresh = map[string]time.Time{projectID: now}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-refresh-fresh",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Freshness != protocol.TaskListFreshnessFresh {
		t.Fatalf("freshness = %s, want fresh after successful runtime check", payload.Freshness)
	}
	if !payload.LastCheckedAt.Equal(now) {
		t.Fatalf("last_checked_at = %v, want runtime refresh time %v", payload.LastCheckedAt, now)
	}
	session, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !found {
		t.Fatal("session missing")
	}
	if !session.UpdatedAt.Equal(seededAt) {
		t.Fatalf("session updated_at = %v, want unchanged %v", session.UpdatedAt, seededAt)
	}
}

func TestHandleTaskListIgnoresAgentPaneStatusForTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-agent-task-list"
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "paused agent in live tmux",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	startedAt := time.Date(2026, time.May, 29, 8, 0, 0, 0, time.UTC)
	containerID := naming.CanonicalSessionID(projectID, taskID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:                containerID,
		IssueID:           taskID,
		State:             daemonstate.SessionStateAttached,
		ObservedState:     daemonstate.SessionStateAttached,
		TmuxAttachedCount: 1,
		StartedAt:         &startedAt,
		UpdatedAt:         startedAt,
	}); err != nil {
		t.Fatalf("seed container session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            containerID + ".pane-190",
		IssueID:       taskID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		StartedAt:     &startedAt,
		UpdatedAt:     startedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed paused pane session: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  logger,
		},
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		revision: map[string]uint64{projectID: 9},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-agent-status",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}
	var payload protocol.TaskListSnapshotPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal task list body: %v", err)
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].Session == nil {
		t.Fatalf("tasks = %+v, want one task with session", payload.Tasks)
	}
	session := payload.Tasks[0].Session
	if session.State != domain.SessionPaused {
		t.Fatalf("session state = %s, want %s", session.State, domain.SessionPaused)
	}
	if session.TotalCount != 1 || session.ActiveCount != 0 || session.PausedCount != 1 {
		t.Fatalf("session counts = %d/%d/%d, want 1/0/1", session.TotalCount, session.ActiveCount, session.PausedCount)
	}
	if !session.TmuxAttached || session.TmuxAttachedCount != 1 {
		t.Fatalf("tmux attachment = %v/%d, want true/1", session.TmuxAttached, session.TmuxAttachedCount)
	}
}

func TestHandleTaskGetUsesFreshTaskListSnapshotCache(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-get"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "cached issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 3},
		hub:      publish.NewHub(16, 8, logger),
	}

	d.storeTaskListSnapshotCache(projectID, 3, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{{
		ID:       naming.IssueID(taskID),
		Title:    "cached issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	}}, false)

	if err := issuesClient.Update(ctx, taskID, domain.StatusInReview); err != nil {
		t.Fatalf("update issue behind cache: %v", err)
	}

	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	getResp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-cache-hit",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !getResp.OK {
		t.Fatalf("task.get response = %+v", getResp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(getResp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if got, want := payload.SnapshotRevision, uint64(3); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	if got, want := payload.Tasks[0].Status, domain.StatusOpen; got != want {
		t.Fatalf("payload task status = %q, want cached %q", got, want)
	}
}

func TestHandleTaskGetComposesRuntimeProjectionOverCachedTaskSnapshot(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-get-worktree-refresh"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "stale dirty worktree issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "Chefy-"+taskID)
	branch := "riordanpawley/" + taskID + "/stale-dirty"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	staleDirtyStatus := git.GitStatus{
		HasChanges:     true,
		GitAdditions:   2154,
		GitDeletions:   1400,
		GitAheadCount:  4,
		GitBehindCount: 25,
	}
	staleDirtyStatusRaw, err := json.Marshal(staleDirtyStatus)
	if err != nil {
		t.Fatalf("marshal stale status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, staleDirtyStatusRaw, time.Now().Add(-time.Hour).UTC()); err != nil {
		t.Fatalf("seed stale dirty git status: %v", err)
	}

	d := &Daemon{
		cfg: Config{BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 12},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.storeTaskListSnapshotCache(projectID, 12, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{{
		ID:                    naming.IssueID(taskID),
		Title:                 "cached durable issue",
		Type:                  domain.TypeTask,
		Priority:              domain.P2,
		Status:                domain.StatusInProgress,
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          2154,
		GitDeletions:          1400,
		GitAheadCount:         4,
		GitBehindCount:        25,
	}}, false)
	if cached, ok := d.readFreshTaskListSnapshotCache(projectID); !ok {
		t.Fatal("task snapshot cache should be fresh")
	} else if cached.Tasks[0].HasUncommittedChanges || cached.Tasks[0].GitAdditions != 0 || cached.Tasks[0].GitDeletions != 0 {
		t.Fatalf("cached task retained runtime fields: %+v", cached.Tasks[0])
	}
	cleanStatusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:     false,
		GitAdditions:   12,
		GitDeletions:   4,
		GitAheadCount:  4,
		GitBehindCount: 25,
	})
	if err != nil {
		t.Fatalf("marshal clean status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, cleanStatusRaw, time.Now().UTC()); err != nil {
		t.Fatalf("persist refreshed clean git status: %v", err)
	}

	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	resp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-refresh-stale-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get response = %+v", resp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	task := payload.Tasks[0]
	if !task.HasWorktree {
		t.Fatal("task.HasWorktree = false, want true")
	}
	if got, want := task.Title, "cached durable issue"; got != want {
		t.Fatalf("task.Title = %q, want cached durable title %q", got, want)
	}
	if task.HasUncommittedChanges {
		t.Fatalf("task.HasUncommittedChanges = true, want false: %+v", task)
	}
	if got, want := task.GitAdditions, 12; got != want {
		t.Fatalf("task.GitAdditions = %d, want refreshed %d", got, want)
	}
	if got, want := task.GitDeletions, 4; got != want {
		t.Fatalf("task.GitDeletions = %d, want refreshed %d", got, want)
	}
	if got, want := task.GitAheadCount, 4; got != want {
		t.Fatalf("task.GitAheadCount = %d, want refreshed %d", got, want)
	}
	if got, want := task.GitBehindCount, 25; got != want {
		t.Fatalf("task.GitBehindCount = %d, want refreshed %d", got, want)
	}
}

func TestHandleTaskListIgnoresFreshCacheAndReadsSQLiteProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "current sqlite list",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeAdapter: &worktreeServiceAdapter{},
		revision:        map[string]uint64{projectID: 11},
		hub:             publish.NewHub(16, 8, logger),
	}
	cachedTask := domain.Task{
		ID:       naming.IssueID(taskID),
		Title:    "stale cached list",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusDone,
	}
	d.storeTaskListSnapshotCache(projectID, 11, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{cachedTask}, false)

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-cache-hit",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.list body: %v", err)
	}
	if got, want := payload.SnapshotRevision, uint64(11); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	if got, want := payload.Tasks[0].Title, "current sqlite list"; got != want {
		t.Fatalf("payload task title = %q, want sqlite %q", got, want)
	}
	if got, want := payload.Tasks[0].Status, domain.StatusOpen; got != want {
		t.Fatalf("payload task status = %q, want sqlite %q", got, want)
	}

	d.worktreeStateRefreshMu.Lock()
	defer d.worktreeStateRefreshMu.Unlock()
	if got := d.worktreeStateLastRefresh[projectID]; got.IsZero() {
		t.Fatal("worktree refresh was not triggered on task.list sqlite projection read")
	}
}

func TestHandleBoardFetchComposesRuntimeProjectionOverCachedTaskSnapshot(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-board-runtime-overlay"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "sqlite title should not replace durable cache",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "Chefy-"+taskID)
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    "az/" + taskID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	statusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:    true,
		GitAdditions:  7,
		GitDeletions:  2,
		GitAheadCount: 3,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, statusRaw, time.Now().UTC()); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeAdapter: &worktreeServiceAdapter{},
		revision:        map[string]uint64{projectID: 11},
		hub:             publish.NewHub(16, 8, logger),
	}
	d.storeTaskListSnapshotCache(projectID, 11, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{{
		ID:                    naming.IssueID(taskID),
		Title:                 "cached durable title",
		Type:                  domain.TypeTask,
		Priority:              domain.P2,
		Status:                domain.StatusOpen,
		HasWorktree:           false,
		HasUncommittedChanges: false,
	}}, true)

	resp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-board-fetch-runtime-overlay",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "board.fetch",
	})
	if err != nil {
		t.Fatalf("handleBoardFetch error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("board.fetch response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeBoardSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode board.fetch body: %v", err)
	}
	tasks := payload.Projection.TaskSummaries()
	if got, want := len(tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	task := tasks[0]
	if got, want := task.Title, "cached durable title"; got != want {
		t.Fatalf("task.Title = %q, want cached durable title %q", got, want)
	}
	if !task.HasWorktree || !task.HasUncommittedChanges || task.GitAdditions != 7 || task.GitDeletions != 2 || task.GitAheadCount != 3 {
		t.Fatalf("task runtime overlay = %+v, want dirty projected worktree", task)
	}
}

func TestHandleBoardFetchDerivesInReviewPhaseFromSessionActivity(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-board-derived-review"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	busyID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Busy handoff",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create busy issue: %v", err)
	}
	idleID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Idle handoff",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create idle issue: %v", err)
	}
	for _, seed := range []struct {
		issueID  string
		activity string
	}{
		{issueID: busyID, activity: "busy"},
		{issueID: idleID, activity: "idle"},
	} {
		if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
			ID:             naming.CanonicalSessionID(projectID, seed.issueID),
			IssueID:        seed.issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       seed.activity,
			ActivitySource: "hooks",
			UpdatedAt:      time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed %s session projection: %v", seed.issueID, err)
		}
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 11},
		hub:      publish.NewHub(16, 8, logger),
	}

	resp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-board-fetch-derived-review",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "board.fetch",
	})
	if err != nil {
		t.Fatalf("handleBoardFetch error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("board.fetch response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeBoardSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode board.fetch body: %v", err)
	}
	statusByID := map[string]domain.Status{}
	factsByID := map[string]domain.IssueFacts{}
	for _, task := range payload.Projection.TaskSummaries() {
		statusByID[task.ID.String()] = task.Status
		factsByID[task.ID.String()] = task.Facts
	}
	if got := statusByID[busyID]; got != domain.StatusInProgress {
		t.Fatalf("busy handoff board status = %s, want %s", got, domain.StatusInProgress)
	}
	if got := statusByID[idleID]; got != domain.StatusInReview {
		t.Fatalf("idle handoff board status = %s, want %s", got, domain.StatusInReview)
	}
	if got := factsByID[busyID]; got.DisplayPhase != domain.IssueDisplayActive || got.ReviewReadyVisible {
		t.Fatalf("busy handoff facts = %+v, want active and not review-ready", got)
	}
	if got := factsByID[idleID]; got.DisplayPhase != domain.IssueDisplayReview || !got.ReviewReadyVisible {
		t.Fatalf("idle handoff facts = %+v, want review-ready", got)
	}
}

func TestHandleTaskListReadsSQLiteProjection(t *testing.T) {
	logger := slog.Default()
	projectID := "proj-local-first-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	taskID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title:    "foreground reads local projection",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	// The deadline covers the list operation under test, not lazy,
	// race-instrumented schema setup performed by the fixture create.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 17},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-local-first-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.list body: %v", err)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}
	if payload.Tasks[0].ID.String() != taskID || payload.Tasks[0].Title != "foreground reads local projection" {
		t.Fatalf("payload task = %+v, want local issue %s", payload.Tasks[0], taskID)
	}
}

func TestLoadTaskListSnapshotSharesInFlightProjectLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	projectID := "proj-shared-list"
	done := make(chan struct{})
	d := &Daemon{
		taskListSnapshotLoads: map[string]*taskListSnapshotLoad{
			projectID: {done: done},
		},
	}

	resultCh := make(chan taskListSnapshotLoadResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, shared, err := d.loadTaskListSnapshot(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-shared-list",
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         "task.list",
		}, projectID, "", false, protocol.ArchiveModeExclude)
		if !shared {
			errCh <- errors.New("load was not shared")
			return
		}
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	d.taskListSnapshotLoadMu.Lock()
	inflight := d.taskListSnapshotLoads[projectID]
	inflight.result = taskListSnapshotLoadResult{
		Revision:      17,
		LastCheckedAt: time.Now().UTC(),
		Freshness:     protocol.TaskListFreshnessFresh,
		Tasks: []domain.Task{{
			ID:     "az-shared",
			Title:  "shared result",
			Status: domain.StatusOpen,
		}},
	}
	close(done)
	d.taskListSnapshotLoadMu.Unlock()

	select {
	case err := <-errCh:
		t.Fatalf("shared load error: %v", err)
	case result := <-resultCh:
		if got, want := result.Revision, uint64(17); got != want {
			t.Fatalf("result.Revision = %d, want %d", got, want)
		}
		if got, want := len(result.Tasks), 1; got != want {
			t.Fatalf("result.Tasks len = %d, want %d", got, want)
		}
		result.Tasks[0].Title = "mutated"
		if got := d.taskListSnapshotLoads[projectID].result.Tasks[0].Title; got != "shared result" {
			t.Fatalf("shared result was not cloned; title = %q", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for shared task-list load")
	}
}

func TestLoadTaskListSnapshotUsesDetachedBuildContext(t *testing.T) {
	logger := slog.Default()
	projectID := "proj-canceled-owner-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	taskID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title:    "canceled owner should not poison load",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 23},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, shared, err := d.loadTaskListSnapshot(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-canceled-owner-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	}, projectID, "", false, protocol.ArchiveModeExclude)
	if err != nil {
		t.Fatalf("loadTaskListSnapshot error = %v, want detached load to succeed", err)
	}
	if shared {
		t.Fatal("shared = true, want owner load")
	}
	if got, want := result.Revision, uint64(23); got != want {
		t.Fatalf("result.Revision = %d, want %d", got, want)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID.String() != taskID {
		t.Fatalf("result.Tasks = %+v, want task %s", result.Tasks, taskID)
	}
}

func TestHandleTaskListAppliesContentQueryInDaemon(t *testing.T) {
	logger := slog.Default()
	projectID := "proj-query-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	matchID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title:       "Alpha",
		Description: "Contains runtime cache evidence",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create matching issue: %v", err)
	}
	if _, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title:       "Beta",
		Description: "Unrelated",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	}); err != nil {
		t.Fatalf("create nonmatching issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 23},
	}
	body, err := json.Marshal(protocol.TaskListRequestBody{Query: "RUNTIME cache"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := d.handleTaskList(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-query-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskList response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task list body: %v", err)
	}
	if payload.SummariesOnly {
		t.Fatal("query task list should return full task payloads, got summaries_only")
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].ID.String() != matchID {
		t.Fatalf("payload tasks = %+v, want only %s", payload.Tasks, matchID)
	}
	if payload.Tasks[0].Description == "" {
		t.Fatalf("query task list lost full issue content: %+v", payload.Tasks[0])
	}
}

func TestHandleTaskListQuerySkipsLiveRuntimeRefresh(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-query-skip-refresh"
	issuesDBPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	matchID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "RPC cache invalidation",
		Description: "Track task snapshot timeout while searching rpc cache invalidation",
		Type:        domain.TypeBug,
		Priority:    domain.P1,
		Status:      domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create matching issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            naming.CanonicalSessionID(projectID, matchID),
		IssueID:       matchID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	tmuxRunner := &testTmuxRunner{sessions: map[string]bool{}}
	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: logger},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 31},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)

	body, err := json.Marshal(protocol.TaskListRequestBody{Query: "rpc cache invalidation"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-query-skip-runtime-refresh",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskList response = %+v", resp.Error)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 0 {
		t.Fatalf("tmux list-sessions calls = %d, want 0 for query snapshot", got)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task list body: %v", err)
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].ID.String() != matchID {
		t.Fatalf("payload tasks = %+v, want only %s", payload.Tasks, matchID)
	}
}

func TestHandleTaskListIncludesDependenciesOnlyWhenRequested(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-list-deps"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create blocker issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 4},
	}

	decodeChild := func(t *testing.T, body []byte) domain.Task {
		t.Helper()
		payload, err := protocol.DecodeTaskListSnapshotPayload(body)
		if err != nil {
			t.Fatalf("decode task list body: %v", err)
		}
		for _, task := range payload.Tasks {
			if task.ID.String() == childID {
				return task
			}
		}
		t.Fatalf("child %s missing from payload: %+v", childID, payload.Tasks)
		return domain.Task{}
	}

	defaultResp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list-deps-default",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handle default task.list error: %v", err)
	}
	if !defaultResp.OK {
		t.Fatalf("default task.list response = %+v", defaultResp.Error)
	}
	defaultTask := decodeChild(t, defaultResp.Body)
	if defaultTask.ParentID == nil || defaultTask.ParentID.String() != parentID {
		t.Fatalf("default parent = %v, want %s", defaultTask.ParentID, parentID)
	}
	if len(defaultTask.Dependencies) != 0 {
		t.Fatalf("default dependencies = %+v, want none", defaultTask.Dependencies)
	}

	body, err := json.Marshal(protocol.TaskListRequestBody{IncludeDependencies: true})
	if err != nil {
		t.Fatalf("marshal dependency request: %v", err)
	}
	fullResp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list-deps-full",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handle full-dependency task.list error: %v", err)
	}
	if !fullResp.OK {
		t.Fatalf("full-dependency task.list response = %+v", fullResp.Error)
	}
	fullTask := decodeChild(t, fullResp.Body)
	if fullTask.ParentID == nil || fullTask.ParentID.String() != parentID {
		t.Fatalf("full parent = %v, want %s", fullTask.ParentID, parentID)
	}
	if len(fullTask.Dependencies) != 1 || fullTask.Dependencies[0].ID.String() != blockerID {
		t.Fatalf("full dependencies = %+v, want blocker %s", fullTask.Dependencies, blockerID)
	}

	showChildren := true
	boardBody, err := json.Marshal(protocol.BoardSnapshotRequestBody{ShowChildren: &showChildren})
	if err != nil {
		t.Fatalf("marshal board child-visibility override: %v", err)
	}
	boardResp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list-deps-board",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "board.fetch",
		Body:            boardBody,
	})
	if err != nil {
		t.Fatalf("handle board.fetch error: %v", err)
	}
	if !boardResp.OK {
		t.Fatalf("board.fetch response = %+v", boardResp.Error)
	}
	boardPayload, err := protocol.DecodeBoardSnapshotPayload(boardResp.Body)
	if err != nil {
		t.Fatalf("decode board body: %v", err)
	}
	foundChild := false
	boardTasks := boardPayload.Projection.TaskSummaries()
	for _, task := range boardTasks {
		if task.ID.String() != childID {
			continue
		}
		foundChild = true
		if len(task.Dependencies) != 0 {
			t.Fatalf("board child dependencies = %+v, want none", task.Dependencies)
		}
	}
	if !foundChild {
		t.Fatalf("board payload missing child %s: %+v", childID, boardTasks)
	}
}

func TestHandleTaskGetInvalidatesTaskListSnapshotCacheAfterIssueUpdate(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-invalidation"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "cache invalidates",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 3},
		hub:      publish.NewHub(16, 8, logger),
	}

	listResp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-cache-prime",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !listResp.OK {
		t.Fatalf("task.list response = %+v", listResp.Error)
	}

	updateBody, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	updateResp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-update-cache-invalidate",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            updateBody,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if !updateResp.OK {
		t.Fatalf("task.update_status response = %+v", updateResp.Error)
	}

	getBody, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	getResp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-after-update",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            getBody,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !getResp.OK {
		t.Fatalf("task.get response = %+v", getResp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(getResp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if got, want := payload.SnapshotRevision, uint64(4); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	if got, want := payload.Tasks[0].Status, domain.StatusInReview; got != want {
		t.Fatalf("payload task status = %q, want %q", got, want)
	}
}

func TestTaskUpdateStatusRejectsRawCloseRuntimeAttachments(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-runtime"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Runtime guard",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      "/tmp/" + taskID,
		Branch:    "riordan/" + taskID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-runtime",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get guarded issue after failed close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("guarded issue status = %s, want %s", task.Status, domain.StatusInReview)
	}

}

func TestTaskClosePreflightBlocksDirtyWorktreeInDaemonPolicy(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-dirty"
	repoDir := filepath.Join(t.TempDir(), "repo")
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Dirty preflight",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	worktreePath := filepath.Join(t.TempDir(), "repo-"+taskID)
	branchName := "riordan/" + taskID + "/work"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return " M main.go\n", nil
		default:
			return "", nil
		}
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, logger),
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: runtimeStore,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(map[string]any{
		"task_id":               taskID,
		"allow_target_worktree": true,
	})
	if err != nil {
		t.Fatalf("marshal close preflight request: %v", err)
	}
	resp, err := d.handleTaskClosePreflight(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close_preflight",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClosePreflight error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "worktree has local changes: main.go") || !strings.Contains(resp.Error.Message, "commit, discard, or merge the worktree changes first") {
		t.Fatalf("task.close_preflight response = %+v, want dirty worktree guard", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get guarded issue after failed preflight: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("guarded issue status = %s, want %s", task.Status, domain.StatusInReview)
	}
}

func TestTaskClosePreflightConvergesStaleBusyHookAtIdlePrompt(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	const projectID = "proj-close-preflight-stale-hook"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Preflight after missed idle hook", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	staleAt := time.Now().UTC().Add(-time.Minute)
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: taskID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: staleAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtimeStore.UpsertSessionActivityEvidence(ctx, daemonstate.SessionActivityEvidence{
		ProjectID: projectID, SessionID: sessionID, IssueID: taskID,
		Activity: "busy", ActivitySource: "hooks", SourceSessionID: sessionID,
		ObservedAt: staleAt, UpdatedAt: staleAt,
	}); err != nil {
		t.Fatal(err)
	}
	captures := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return sessionID, nil
		case "capture-pane":
			captures++
			return "Validation complete.\n› Continue", nil
		default:
			return "", nil
		}
	}}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger}, tmux: tmux.NewClient(runner, logger),
		issueClientsByProject:  map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		revision:               map[string]uint64{projectID: 1},
		hub:                    publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]any{"task_id": taskID, "allow_target_session": true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleTaskClosePreflight(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion, RequestID: "req-close-preflight-stale-hook",
		Kind: protocol.EnvelopeKindCommand, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command: "task.close_preflight", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || captures != 1 {
		t.Fatalf("close preflight response=%+v captures=%d, want one idle convergence and success", resp, captures)
	}
	evidence, found, err := runtimeStore.GetSessionActivityEvidence(ctx, projectID, sessionID)
	if err != nil || !found || evidence.Activity != "idle" || evidence.ActivitySource != "terminal" {
		t.Fatalf("activity evidence=%+v found=%t err=%v, want materialized terminal idle", evidence, found, err)
	}
}

func TestTaskClosePreflightEnforcesInvestigationDispositionAcceptance(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-investigation-acceptance"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: logger}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}}

	tests := []struct {
		name       string
		events     []issues.IssueObservationEventParams
		newEpoch   bool
		wantReason string
	}{
		{name: "human facing remains gated", wantReason: "human-facing investigation lacks explicit issue-specific findings acceptance"},
		{name: "internal accepted", events: []issues.IssueObservationEventParams{
			{Type: domain.IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "internal_review"}},
			{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"outcome": "accepted", "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator}},
		}},
		{name: "internal returned findings remain blocked", events: []issues.IssueObservationEventParams{
			{Type: domain.IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "internal_review"}},
			{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"outcome": "accepted", "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator}},
			{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return", Payload: map[string]any{"outcome": "returned", "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator}},
		}, wantReason: "unresolved returned findings"},
		{name: "new review epoch rejects stale human acceptance", events: []issues.IssueObservationEventParams{
			{Type: domain.IssueEventHumanInputProvided, Source: "human", Payload: map[string]any{"investigation_findings_accepted": true}},
		}, newEpoch: true, wantReason: "human-facing investigation lacks explicit issue-specific findings acceptance"},
		{name: "new review epoch rejects stale internal acceptance", events: []issues.IssueObservationEventParams{
			{Type: domain.IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "internal_review"}},
			{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"outcome": "accepted", "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator}},
		}, newEpoch: true, wantReason: "internal review lacks durable accepted reviewer outcome"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: tt.name, Type: domain.TypeInvestigation, Status: domain.StatusInReview})
			if err != nil {
				t.Fatalf("create investigation: %v", err)
			}
			for _, event := range tt.events {
				if _, err := issuesClient.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
					t.Fatalf("append event: %v", err)
				}
			}
			if tt.newEpoch {
				if err := issuesClient.Update(ctx, issueID, domain.StatusOpen); err != nil {
					t.Fatalf("reopen investigation: %v", err)
				}
				if err := issuesClient.Update(ctx, issueID, domain.StatusInReview); err != nil {
					t.Fatalf("start new review epoch: %v", err)
				}
			}
			_, err = d.validateTaskClosePreflight(ctx, projectID, issueID, taskClosePreflightOptions{}, protocol.RequestEnvelope{})
			if tt.wantReason == "" && err != nil {
				t.Fatalf("accepted internal review blocked: %v", err)
			}
			if tt.wantReason != "" && (err == nil || !strings.Contains(err.Error(), tt.wantReason)) {
				t.Fatalf("error = %v, want reason %q", err, tt.wantReason)
			}
		})
	}
}

func TestTaskCloseBlocksActiveSessionActivity(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	tests := []struct {
		name           string
		activity       string
		activitySource string
		wantActivity   string
	}{
		{name: "busy", activity: "busy", activitySource: "hooks", wantActivity: "busy"},
		{name: "waiting", activity: "waiting", activitySource: "hooks", wantActivity: "waiting"},
		{name: "working", activity: "working", activitySource: "hooks", wantActivity: "working"},
		{name: "unknown", activity: "unknown", activitySource: "none", wantActivity: "unknown"},
		{name: "no agent", activity: "no-agent", activitySource: "session", wantActivity: "no-agent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID := "proj-close-active-" + strings.ReplaceAll(tt.name, " ", "-")
			issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
			issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
			t.Cleanup(func() { _ = runtimeStore.Close() })

			taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
				Title:    "Close active session",
				Type:     domain.TypeTask,
				Priority: domain.P2,
				Status:   domain.StatusInReview,
			})
			if err != nil {
				t.Fatalf("create issue: %v", err)
			}
			sessionID := naming.CanonicalSessionID(projectID, taskID)
			if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
				ID:             sessionID,
				IssueID:        taskID,
				State:          daemonstate.SessionStateRunning,
				ObservedState:  daemonstate.SessionStateRunning,
				Activity:       tt.activity,
				ActivitySource: tt.activitySource,
				UpdatedAt:      time.Now().UTC(),
			}); err != nil {
				t.Fatalf("seed session projection: %v", err)
			}

			d := &Daemon{
				cfg:          Config{RepoDir: ".", Logger: logger},
				sessionStore: daemonstate.NewStore(),
				issueClientsByProject: map[string]*issues.Client{
					projectID: issuesClient,
				},
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
					projectID: runtimeStore,
				},
				revision: map[string]uint64{projectID: 1},
				hub:      publish.NewHub(16, 8, logger),
			}

			body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
			if err != nil {
				t.Fatalf("marshal close request: %v", err)
			}
			resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-close-active-session",
				Kind:            protocol.EnvelopeKindCommand,
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Command:         "task.close",
				Body:            body,
			})
			if err != nil {
				t.Fatalf("handleTaskClose error: %v", err)
			}
			if resp.OK || resp.Error == nil {
				t.Fatalf("task.close response = %+v, want session activity guard", resp)
			}
			for _, want := range []string{
				"session activity is " + tt.wantActivity,
				"wait for the session projection to report idle/done/terminal activity or intentionally stop the session",
			} {
				if !strings.Contains(resp.Error.Message, want) {
					t.Fatalf("task.close error = %q, want %q", resp.Error.Message, want)
				}
			}
			task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
			if err != nil {
				t.Fatalf("get issue after blocked close: %v", err)
			}
			if task.Status != domain.StatusInReview {
				t.Fatalf("task status = %s, want %s", task.Status, domain.StatusInReview)
			}
			session, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
			if err != nil {
				t.Fatalf("get session projection: %v", err)
			}
			if !found || session.State != daemonstate.SessionStateRunning {
				t.Fatalf("session projection = %+v found=%t, want running row preserved", session, found)
			}
		})
	}
}

func TestTaskCloseAllowsExplicitActiveSessionOverride(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-active-override"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close active session with override",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        taskID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID, AllowActiveSession: true})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-active-session-override",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v, want success", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get closed issue: %v", err)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
	}
	session, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get session projection: %v", err)
	}
	if !found || session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("session projection = %+v found=%t, want stopped row", session, found)
	}
}

func TestTaskCloseAllowsTerminalOrIdleSessionActivity(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	tests := []struct {
		name           string
		activity       string
		activitySource string
	}{
		{name: "idle", activity: "idle", activitySource: "hooks"},
		{name: "done", activity: "done", activitySource: "hooks"},
		{name: "error", activity: "error", activitySource: "hooks"},
		{name: "paused", activity: "paused", activitySource: "session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID := "proj-close-inactive-" + tt.name
			issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
			issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
			t.Cleanup(func() { _ = runtimeStore.Close() })

			taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
				Title:    "Close inactive session",
				Type:     domain.TypeTask,
				Priority: domain.P2,
				Status:   domain.StatusInReview,
			})
			if err != nil {
				t.Fatalf("create issue: %v", err)
			}
			sessionID := naming.CanonicalSessionID(projectID, taskID)
			if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
				ID:             sessionID,
				IssueID:        taskID,
				State:          daemonstate.SessionStateRunning,
				ObservedState:  daemonstate.SessionStateRunning,
				Activity:       tt.activity,
				ActivitySource: tt.activitySource,
				UpdatedAt:      time.Now().UTC(),
			}); err != nil {
				t.Fatalf("seed session projection: %v", err)
			}

			d := &Daemon{
				cfg:          Config{RepoDir: ".", Logger: logger},
				sessionStore: daemonstate.NewStore(),
				tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, logger),
				issueClientsByProject: map[string]*issues.Client{
					projectID: issuesClient,
				},
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
					projectID: runtimeStore,
				},
				revision: map[string]uint64{projectID: 1},
				hub:      publish.NewHub(16, 8, logger),
			}

			body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
			if err != nil {
				t.Fatalf("marshal close request: %v", err)
			}
			resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-close-inactive-session",
				Kind:            protocol.EnvelopeKindCommand,
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Command:         "task.close",
				Body:            body,
			})
			if err != nil {
				t.Fatalf("handleTaskClose error: %v", err)
			}
			if !resp.OK {
				t.Fatalf("task.close response = %+v, want success", resp)
			}
			task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
			if err != nil {
				t.Fatalf("get closed issue: %v", err)
			}
			if task.Status != domain.StatusDone {
				t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
			}
			session, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
			if err != nil {
				t.Fatalf("get session projection: %v", err)
			}
			if !found || session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped {
				t.Fatalf("session projection = %+v found=%t, want stopped row", session, found)
			}
		})
	}
}

func TestTaskCloseCommandUpdatesStatusThroughDaemon(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Close me",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := issuesClient.Update(ctx, taskID, domain.StatusInReview); err != nil {
		t.Fatalf("update task: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close response: %v", err)
	}
	if result.TaskID != taskID || result.Status != string(domain.StatusDone) || result.Revision == 0 {
		t.Fatalf("close result = %+v", result)
	}
	if got := taskClosePhaseNames(result.Phases); !slices.Contains(got, "preflight") || !slices.Contains(got, "status_write") {
		t.Fatalf("close phases = %v, want preflight and status_write", got)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var task domain.Task
	found := false
	for _, candidate := range tasks {
		if candidate.ID.String() == taskID {
			task = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("task not found after close: %s", taskID)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want done", task.Status)
	}
}

func TestTaskCloseCommandReleasesExecutionLeaseBeforeTerminalStatusWrite(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close-execution-lease"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close leased issue",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{
		OwnerID:   "worker-a",
		OwnerKind: "agent",
		Purpose:   domain.CoordinationLeaseExecution,
	}); err != nil {
		t.Fatalf("claim execution lease: %v", err)
	}
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-execution-lease",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskClose response = %+v, want success without manual lease release", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get closed task: %v", err)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
	}
	if task.Ownership != nil || len(task.CoordinationLeases) != 0 {
		t.Fatalf("closed task leases = %+v ownership = %+v, want none", task.CoordinationLeases, task.Ownership)
	}
}

func TestTaskCloseCommandCancelledSkipsIntegrationAndWritesOutcome(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-cancelled"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Cancel me",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		hub: publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
		CloseOutcome:         string(domain.IssueCloseCancelled),
	})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-cancelled",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close response: %v", err)
	}
	if result.Status != string(domain.StatusCancelled) || result.IntegrationRequested || result.Integrated {
		t.Fatalf("close result = %+v, want cancelled without integration", result)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get cancelled task: %v", err)
	}
	if task.Status != domain.StatusCancelled {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusCancelled)
	}
	db, err := sql.Open("sqlite", issuesDBPath)
	if err != nil {
		t.Fatalf("open issues db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var lifecycle, outcome string
	if err := db.QueryRowContext(ctx, `SELECT lifecycle_state, closed_outcome FROM issues WHERE id = ?`, taskID).Scan(&lifecycle, &outcome); err != nil {
		t.Fatalf("read issue state columns: %v", err)
	}
	if lifecycle != string(domain.IssueWorkflowClosed) || outcome != string(domain.IssueCloseCancelled) {
		t.Fatalf("issue state = lifecycle %q outcome %q, want closed/cancelled", lifecycle, outcome)
	}
}

func TestTaskCloseBlocksUnresolvedChildrenByDefault(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close-child-default"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	for _, tc := range []struct {
		name    string
		outcome string
	}{
		{name: "completed"},
		{name: "cancelled", outcome: string(domain.IssueCloseCancelled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(taskCloseRequest{TaskID: parentID, CloseOutcome: tc.outcome})
			if err != nil {
				t.Fatalf("marshal close request: %v", err)
			}
			resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       naming.RequestID("req-close-child-default-" + tc.name),
				Kind:            protocol.EnvelopeKindCommand,
				Command:         "task.close",
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Body:            body,
			})
			if err != nil {
				t.Fatalf("handleTaskClose error: %v", err)
			}
			if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "unresolved child issues remain: "+childID+" (open)") {
				t.Fatalf("handleTaskClose response = %+v, want unresolved child guard", resp)
			}
		})
	}
}

func TestTaskClosePreflightBlocksFiveOpenDescendantsBeforeIntegration(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-atomic-preflight"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root with unresolved descendants",
		Type:   domain.TypeEpic,
		Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childIDs := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
			Title:    fmt.Sprintf("Open descendant %d", i+1),
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &rootID,
		})
		if err != nil {
			t.Fatalf("create child %d: %v", i+1, err)
		}
		childIDs = append(childIDs, childID)
	}

	sourceWorktree := filepath.Join(repoDir, "wt-"+rootID)
	sourceBranch := "riordan/" + rootID + "/atomic-preflight"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   rootID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed source worktree: %v", err)
	}

	targetHead := "target-head-before-close"
	integrationAdapterCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		integrationAdapterCalls++
		targetHead = "target-head-mutated"
		return "", fmt.Errorf("integration adapter must not be invoked before close preflight: %s", strings.Join(args, " "))
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		hub: publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               rootID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-atomic-preflight",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "unresolved child issues remain") {
		t.Fatalf("handleTaskClose response = %+v, want descendant preflight failure", resp)
	}
	for _, childID := range childIDs {
		if !strings.Contains(resp.Error.Message, childID+" (open)") {
			t.Fatalf("close error = %q, missing open descendant %s", resp.Error.Message, childID)
		}
	}
	if integrationAdapterCalls != 0 {
		t.Fatalf("integration adapter calls = %d, want zero", integrationAdapterCalls)
	}
	if targetHead != "target-head-before-close" {
		t.Fatalf("target HEAD = %q, want unchanged", targetHead)
	}

	root, err := issuesClient.GetWithRuntime(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("get root after failed close: %v", err)
	}
	if root.Status != domain.StatusInReview {
		t.Fatalf("root status = %s, want unchanged %s", root.Status, domain.StatusInReview)
	}
	for _, childID := range childIDs {
		child, err := issuesClient.GetWithRuntime(ctx, projectID, childID)
		if err != nil {
			t.Fatalf("get child %s after failed close: %v", childID, err)
		}
		if child.Status != domain.StatusOpen {
			t.Fatalf("child %s status = %s, want unchanged %s", childID, child.Status, domain.StatusOpen)
		}
	}
	worktrees, err := runtimeStore.ListWorktreeStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list source worktrees after failed close: %v", err)
	}
	if len(worktrees) != 1 || worktrees[0].IssueID != rootID || worktrees[0].Path != sourceWorktree || worktrees[0].Branch != sourceBranch {
		t.Fatalf("source worktrees = %+v, want unchanged", worktrees)
	}
}

func TestTaskCloseRejectsCleanUnresolvedChildrenWithoutAutoTerminalizing(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close-clean-children"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{TaskID: parentID, CloseCleanChildren: true})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-clean-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "unresolved child issues remain") {
		t.Fatalf("handleTaskClose response = %+v, want unresolved child rejection", resp)
	}
	for _, issueID := range []string{parentID, childID} {
		task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
		if err != nil {
			t.Fatalf("get %s after close: %v", issueID, err)
		}
		if task.Status == domain.StatusDone {
			t.Fatalf("%s status = %s, want nonterminal unchanged state", issueID, task.Status)
		}
	}
}

func TestTaskCloseNeverAutoTerminalizesDescendants(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close-clean-backlog-descendants"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Backlog child", Type: domain.TypeTask, Status: domain.StatusOpen, Lifecycle: domain.IssueWorkflowBacklog, ParentID: &parentID})
	if err != nil {
		t.Fatal(err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Backlog grandchild", Type: domain.TypeTask, Status: domain.StatusOpen, Lifecycle: domain.IssueWorkflowBacklog, ParentID: &childID})
	if err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject:  map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}, revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{TaskID: parentID, CloseCleanChildren: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "req-close-clean-backlog-descendants", Kind: protocol.EnvelopeKindCommand, Command: "task.close", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "unresolved child issues remain") {
		t.Fatalf("handleTaskClose response = %+v, want unresolved descendant rejection", resp)
	}
	for _, issueID := range []string{childID, grandchildID} {
		task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != domain.StatusOpen {
			t.Fatalf("%s status = %s, want open", issueID, task.Status)
		}
	}
}

func TestTaskCloseCleanChildrenDoesNotForceDirtyChildWorktree(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-clean-child-dirty"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen, Lifecycle: domain.IssueWorkflowBacklog, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	childWorktree := filepath.Join(repoDir, "wt-"+childID)
	childBranch := "riordan/" + childID + "/work"
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      childWorktree,
		Branch:    childBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed child worktree: %v", err)
	}
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", childWorktree, childBranch), nil
		case strings.Contains(joined, "status --porcelain"):
			return " M child.go\n", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		hub: publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: store,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{
		TaskID:             parentID,
		ForceWorktree:      true,
		CloseCleanChildren: true,
	})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-clean-child-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "unresolved child issues remain") {
		t.Fatalf("handleTaskClose response = %+v, want unresolved descendant rejection", resp)
	}
	child, err := issuesClient.GetWithRuntime(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("get child after blocked close: %v", err)
	}
	if child.Status != domain.StatusOpen {
		t.Fatalf("child status = %s, want %s", child.Status, domain.StatusOpen)
	}
	if child.IssueFacts().LifecycleState != domain.IssueWorkflowOpen {
		t.Fatalf("child lifecycle = %s, want open", child.IssueFacts().LifecycleState)
	}
}

func taskClosePhaseNames(phases []taskClosePhaseTiming) []string {
	names := make([]string, 0, len(phases))
	for _, phase := range phases {
		names = append(names, phase.Name)
	}
	return names
}

func TestTaskCloseIntegrationContextReservesCleanupBudget(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), domain.IntegrationCloseReserve+2*time.Second)
	defer parentCancel()

	ctx, cancel, err := taskCloseIntegrationContext(parent)
	if err != nil {
		t.Fatalf("taskCloseIntegrationContext() error = %v", err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("integration context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < time.Second || remaining > 2*time.Second {
		t.Fatalf("integration budget = %v, want close to 2s after cleanup reserve", remaining)
	}
}

func TestTaskCloseTimeoutBoundsNonIntegratingCancellation(t *testing.T) {
	if got := taskCloseTimeout(domain.IssueCloseCancelled); got != domain.LifecycleCleanupTimeout {
		t.Fatalf("cancel timeout = %v, want %v", got, domain.LifecycleCleanupTimeout)
	}
	if got := taskCloseTimeout(domain.IssueCloseCompleted); got != domain.IntegrationCloseTimeout {
		t.Fatalf("completed timeout = %v, want %v", got, domain.IntegrationCloseTimeout)
	}
}

func TestTaskCloseIntegrationContextRejectsExhaustedCleanupReserve(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), domain.IntegrationCloseReserve-time.Second)
	defer parentCancel()

	_, cancel, err := taskCloseIntegrationContext(parent)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "cleanup reserve is required") {
		t.Fatalf("taskCloseIntegrationContext() error = %v, want exhausted cleanup reserve", err)
	}
}

func taskClosePhaseByName(phases []taskClosePhaseTiming, name string) (taskClosePhaseTiming, bool) {
	for _, phase := range phases {
		if phase.Name == name {
			return phase, true
		}
	}
	return taskClosePhaseTiming{}, false
}

func TestRecordTaskCloseHookPhasesLogsSlowHookWithSanitizedContext(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	result := taskCloseResult{TaskID: "cyi", Status: string(domain.StatusDone)}
	recordTaskCloseHookPhases(context.Background(), &result, logger, protocol.RequestEnvelope{
		RequestID: "task.close-test",
		Command:   "task.close",
	}, "proj-test", "cyi", []git.GitHookDiagnostic{{
		Hook:       "commit-msg",
		Command:    "git merge --...",
		ElapsedMS:  int64((taskCloseSlowGitHookThreshold + time.Second) / time.Millisecond),
		ExitStatus: 0,
		Blocking:   true,
	}})

	phase, ok := taskClosePhaseByName(result.Phases, "githook.commit-msg")
	if !ok {
		t.Fatalf("phases = %+v, want githook.commit-msg", result.Phases)
	}
	if phase.Command != "git merge --..." || phase.Hook != "commit-msg" {
		t.Fatalf("phase = %+v, want sanitized hook command shape", phase)
	}
	logOutput := buf.String()
	for _, want := range []string{
		"event=task.close.githook.slow",
		"operation=task.close",
		"hook=commit-msg",
		"hook_command_shape=\"git merge --...\"",
		"blocking=true",
		"timed_out=false",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("slow hook log missing %q:\n%s", want, logOutput)
		}
	}
	for _, forbidden := range []string{
		"feature",
		"/Users/",
		"token=",
		"authorization",
	} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("slow hook log contains forbidden value %q:\n%s", forbidden, logOutput)
		}
	}
}

func TestTaskCloseRepairsLegacyProjectRuntimeProjectionBeforeFinalStatusUpdate(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with legacy projection alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      filepath.Join(t.TempDir(), "missing", taskID),
		Branch:    "riordan/" + taskID + "/legacy",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-legacy-projection",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v, want success", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get closed issue: %v", err)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
	}
	if _, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if found {
		t.Fatalf("legacy worktree projection still present for %s", taskID)
	}
}

func TestCommittedCloseAdvancesMaterializedTaskReadsWhileRuntimeEnrichmentIsBlocked(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectID := "proj-committed-close-read-floor"
	issuesClient, repoDir := newTestIssueClient(t)
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "committed close read floor", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 2 {
			close(hydrateEntered)
			<-releaseHydrate
		}
		return tasks, nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- materializer.refreshRuntime(ctx, []string{taskID}) }()
	<-hydrateEntered

	d := &Daemon{
		cfg:                   Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		materializers:        map[string]*projectReadMaterializer{projectID: materializer},
		materializersStarted: true,
		revision:             map[string]uint64{},
		hub:                  publish.NewHub(16, 8, logger),
	}
	closeBody, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	closeResp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-close-read-floor",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            closeBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !closeResp.OK {
		t.Fatalf("task.close response = %+v", closeResp.Error)
	}

	assertRead := func(name string, resp protocol.ResponseEnvelope) {
		t.Helper()
		if !resp.OK {
			t.Fatalf("%s response = %+v", name, resp.Error)
		}
		payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		found := false
		for _, task := range payload.Tasks {
			if task.ID.String() == taskID {
				found = true
				if task.Status != domain.StatusDone {
					t.Fatalf("%s task status = %s, want %s", name, task.Status, domain.StatusDone)
				}
			}
		}
		if !found {
			t.Fatalf("%s omitted committed issue %s", name, taskID)
		}
	}
	request := func(command string, body any) protocol.RequestEnvelope {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("req-" + command), Kind: protocol.EnvelopeKindCommand, Command: command, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: encoded}
	}
	getResp, err := d.handleTaskGet(ctx, request("task.get", map[string]any{"task_id": taskID}))
	if err != nil {
		t.Fatal(err)
	}
	assertRead("task.get", getResp)
	getManyResp, err := d.handleTaskGetMany(ctx, request("task.get-many", map[string]any{"task_ids": []string{taskID}, "metadata_only": true}))
	if err != nil {
		t.Fatal(err)
	}
	assertRead("task.get-many", getManyResp)
	listResp, err := d.handleTaskList(ctx, request("task.list", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertRead("task.list", listResp)
	searchResp, err := d.handleTaskList(ctx, request("task.search", map[string]any{"query": "committed close"}))
	if err != nil {
		t.Fatal(err)
	}
	assertRead("task.search", searchResp)
	projectTasks, _, err := d.projectReadSnapshot(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projectTasks) != 1 || projectTasks[0].Status != domain.StatusDone {
		t.Fatalf("orchestration project snapshot source = %+v, want committed closed lifecycle", projectTasks)
	}

	close(releaseHydrate)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
}

func TestTaskCreateIntentReplaysCanonicalChildAndRejectsConflictingReuse(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = client.CloseDB() })
	parent, err := client.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: logger}, issues: client, revision: map[string]uint64{}, hub: publish.NewHub(8, 4, logger)}
	request := func(intentKey, title string) protocol.RequestEnvelope {
		body, marshalErr := json.Marshal(map[string]any{"intent_key": intentKey, "title": title, "type": domain.TypeTask, "parent_id": parent, "created_from_id": parent})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "split-create", Kind: protocol.EnvelopeKindCommand, Command: daemonclient.CommandTaskCreate, Meta: protocol.Metadata{ProjectID: "project"}, Body: body}
	}
	first, err := d.handleTaskCreate(ctx, request("split-1", "child"))
	if err != nil || !first.OK {
		t.Fatalf("first response=%+v err=%v", first, err)
	}
	var firstBody daemonclient.TaskIDResponse
	if err := json.Unmarshal(first.Body, &firstBody); err != nil || !firstBody.Created {
		t.Fatalf("first body=%+v err=%v", firstBody, err)
	}
	replay, err := d.handleTaskCreate(ctx, request("split-1", "child"))
	if err != nil || !replay.OK {
		t.Fatalf("replay response=%+v err=%v", replay, err)
	}
	var replayBody daemonclient.TaskIDResponse
	if err := json.Unmarshal(replay.Body, &replayBody); err != nil || replayBody.Created || replayBody.TaskID != firstBody.TaskID || replay.Revision != 0 {
		t.Fatalf("replay body=%+v revision=%d err=%v", replayBody, replay.Revision, err)
	}
	conflict, err := d.handleTaskCreate(ctx, request("split-1", "different"))
	if err != nil || conflict.OK || conflict.Error == nil || conflict.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
	distinct, err := d.handleTaskCreate(ctx, request("split-2", "child"))
	if err != nil || !distinct.OK {
		t.Fatalf("distinct response=%+v err=%v", distinct, err)
	}
	var distinctBody daemonclient.TaskIDResponse
	if err := json.Unmarshal(distinct.Body, &distinctBody); err != nil || !distinctBody.Created || distinctBody.TaskID == firstBody.TaskID {
		t.Fatalf("byte-identical distinct invocation body=%+v first=%+v err=%v", distinctBody, firstBody, err)
	}
}

func TestCommittedCreateSurvivesConcurrentWorktreeRefreshAndAdvancesMaterializedReads(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectID := "proj-committed-create-read-floor"
	issuesClient, repoDir := newTestIssueClient(t)
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "create read floor parent", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatal(err)
	}

	var hydrateCalls atomic.Int32
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	var releaseHydrateOnce sync.Once
	release := func() { releaseHydrateOnce.Do(func() { close(releaseHydrate) }) }
	t.Cleanup(release)
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 2 {
			close(hydrateEntered)
			<-releaseHydrate
		}
		return tasks, nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:                   Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		materializers:        map[string]*projectReadMaterializer{projectID: materializer},
		materializersStarted: true,
		revision:             map[string]uint64{},
		hub:                  publish.NewHub(16, 8, logger),
	}

	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- newRuntimeProjectionWriter(d).ReplaceWorktreeProjectionSnapshot(ctx, projectID, []daemonstate.WorktreeState{{
			ProjectID: projectID,
			IssueID:   parentID,
			Path:      filepath.Join(repoDir, "parent-worktree"),
			Branch:    "issue/" + parentID,
			UpdatedAt: time.Date(2026, time.July, 18, 1, 0, 0, 0, time.UTC),
		}})
	}()
	<-hydrateEntered

	request := func(command string, body any) protocol.RequestEnvelope {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID("req-" + command),
			Kind:            protocol.EnvelopeKindCommand,
			Command:         command,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Body:            encoded,
		}
	}
	createResp, err := d.handleTaskCreate(ctx, request("task.create", map[string]any{
		"title": "created during worktree refresh", "type": domain.TypeBug, "priority": domain.P1, "parent_id": parentID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !createResp.OK {
		t.Fatalf("task.create response = %+v", createResp.Error)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(createResp.Body, &created); err != nil {
		t.Fatal(err)
	}
	dependencyResp, err := d.handleTaskDependencyAdd(ctx, request("task.dependency.add", map[string]any{
		"task_id": created.TaskID, "depends_on_id": parentID, "dependency_type": domain.DependencyCreatedIn,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !dependencyResp.OK {
		t.Fatalf("task.dependency.add response = %+v", dependencyResp.Error)
	}

	independentDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = independentDB.Close() })
	var issueCount, dependencyCount, eventCount int
	if err := independentDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id=?`, created.TaskID).Scan(&issueCount); err != nil {
		t.Fatal(err)
	}
	if err := independentDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_dependencies WHERE issue_id=?`, created.TaskID).Scan(&dependencyCount); err != nil {
		t.Fatal(err)
	}
	if err := independentDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events WHERE issue_id=?`, created.TaskID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if issueCount != 1 || dependencyCount != 2 || eventCount < 3 {
		t.Fatalf("independent durable read = issue:%d dependencies:%d events:%d, want 1/2/at least 3", issueCount, dependencyCount, eventCount)
	}

	assertCreatedRead := func(name string, resp protocol.ResponseEnvelope) {
		t.Helper()
		if !resp.OK {
			t.Fatalf("%s response = %+v", name, resp.Error)
		}
		payload, decodeErr := protocol.DecodeTaskListSnapshotPayload(resp.Body)
		if decodeErr != nil {
			t.Fatalf("decode %s: %v", name, decodeErr)
		}
		for _, task := range payload.Tasks {
			if task.ID.String() == created.TaskID {
				return
			}
		}
		t.Fatalf("%s omitted committed create %s", name, created.TaskID)
	}
	getResp, err := d.handleTaskGet(ctx, request("task.get", map[string]any{"task_id": created.TaskID}))
	if err != nil {
		t.Fatal(err)
	}
	assertCreatedRead("task.get", getResp)
	getManyResp, err := d.handleTaskGetMany(ctx, request("task.get-many", map[string]any{"task_ids": []string{created.TaskID}, "metadata_only": true}))
	if err != nil {
		t.Fatal(err)
	}
	assertCreatedRead("task.get-many", getManyResp)
	listResp, err := d.handleTaskList(ctx, request("task.list", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertCreatedRead("task.list", listResp)
	projectTasks, _, err := d.projectReadSnapshot(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := findDaemonTaskByID(projectTasks, created.TaskID); !found {
		t.Fatalf("orchestration project snapshot source omitted committed create %s", created.TaskID)
	}

	release()
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
}

func TestCrossDaemonReadsConvergeCommittedLifecycleWithoutRuntimeRefresh(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const projectID = "proj-cross-daemon-close-preflight"
	issuesClient, repoDir := newTestIssueClient(t)
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "parent ready to close", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "terminal child", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusInReview, ParentID: &parentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(projectID, childID)
	if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: childID, State: daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached, Activity: "busy", ActivitySource: "hooks",
		UpdatedAt: time.Date(2026, time.July, 19, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	reader := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		issueClientsByProject:  map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		materializers:          map[string]*projectReadMaterializer{},
		materializersStarted:   true,
		revision:               map[string]uint64{projectID: 1},
		hub:                    publish.NewHub(16, 8, logger),
	}
	var readerHydrateCalls atomic.Int32
	readerMaterializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
		readerHydrateCalls.Add(1)
		return reader.hydrateProjectReadTasks(hydrateCtx, projectID, issuesClient, tasks)
	})
	reader.configureProjectReadMaterializer(readerMaterializer, projectID, issuesClient)
	if err := readerMaterializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	reader.materializers[projectID] = readerMaterializer
	readerHydrateCalls.Store(0)
	var readerUserSyncCalls atomic.Int32
	reader.projectReadUserProjectionSync = func(context.Context, string, []string) error {
		readerUserSyncCalls.Add(1)
		return nil
	}

	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.ForceUpsertSession(projectID, sessionID, childID, daemonstate.SessionStateAttached); err != nil {
		t.Fatal(err)
	}
	writer := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		tmux:                   tmux.NewClient(tmuxRunner, logger),
		issues:                 issuesClient,
		issueClientsByProject:  map[string]*issues.Client{projectID: issuesClient},
		session:                daemonhandlers.NewSessionHandler(sessionStore),
		sessionStore:           sessionStore,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		revision:               map[string]uint64{projectID: 1},
		hub:                    publish.NewHub(16, 8, logger),
	}

	request := func(command string, body any) protocol.RequestEnvelope {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("req-" + command),
			Kind: protocol.EnvelopeKindCommand, Command: command,
			Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: encoded,
		}
	}
	stopResp, err := writer.handleSessionStopDirect(ctx, request("session.stop", map[string]string{
		"project_id": projectID, "session_id": childID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !stopResp.OK {
		t.Fatalf("session.stop response = %+v", stopResp.Error)
	}
	removeResp, err := writer.handleTaskDependencyRemove(ctx, request("task.dependency.remove", map[string]any{
		"task_id": childID, "depends_on_id": parentID, "dependency_type": domain.DependencyParentChild,
		"confirm": true, "confirm_parent_orphan": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !removeResp.OK {
		t.Fatalf("task.dependency.remove response = %+v", removeResp.Error)
	}

	assertDetachedTerminalChild := func(name string, resp protocol.ResponseEnvelope, callErr error) {
		t.Helper()
		if callErr != nil {
			t.Fatal(callErr)
		}
		if !resp.OK {
			t.Fatalf("cross-daemon %s response = %+v", name, resp.Error)
		}
		payload, decodeErr := protocol.DecodeTaskListSnapshotPayload(resp.Body)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		child, found := findDaemonTaskByID(payload.Tasks, childID)
		if !found {
			t.Fatalf("cross-daemon %s omitted %s", name, childID)
		}
		if child.ParentID != nil {
			t.Fatalf("cross-daemon %s child parent = %v, want committed parent removal from canonical delta", name, child.ParentID)
		}
	}
	getResp, getErr := reader.handleTaskGet(ctx, request("task.get", map[string]any{"task_id": childID}))
	assertDetachedTerminalChild("task.get", getResp, getErr)
	getManyResp, getManyErr := reader.handleTaskGetMany(ctx, request("task.get-many", map[string]any{"task_ids": []string{childID}, "metadata_only": true}))
	assertDetachedTerminalChild("task.get-many", getManyResp, getManyErr)
	listResp, listErr := reader.handleTaskList(ctx, request("task.list", map[string]any{}))
	assertDetachedTerminalChild("task.list", listResp, listErr)
	searchResp, searchErr := reader.handleTaskList(ctx, request("task.search", map[string]any{"query": "terminal child"}))
	assertDetachedTerminalChild("task.search", searchResp, searchErr)
	if got := readerHydrateCalls.Load(); got != 0 {
		t.Fatalf("ordinary cross-daemon reads invoked runtime hydration %d times", got)
	}
	if got := readerUserSyncCalls.Load(); got != 0 {
		t.Fatalf("ordinary cross-daemon reads invoked user projection sync %d times", got)
	}

	preflightResp, err := reader.handleTaskClosePreflight(ctx, request("task.close_preflight", map[string]any{"task_id": parentID}))
	if err != nil {
		t.Fatal(err)
	}
	if !preflightResp.OK {
		t.Fatalf("cross-daemon close preflight response = %+v, want removed child/session facts refreshed", preflightResp.Error)
	}
	if readerHydrateCalls.Load() == 0 || readerUserSyncCalls.Load() == 0 {
		t.Fatalf("strict close preflight did not refresh runtime/user projections: hydration=%d user_sync=%d", readerHydrateCalls.Load(), readerUserSyncCalls.Load())
	}
}

func TestCommittedSessionStopFailsUnavailableWhenTaskRuntimeRefreshFails(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const projectID = "proj-session-stop-refresh-failure"
	issuesClient, repoDir := newTestIssueClient(t)
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "stop refresh failure", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached, UpdatedAt: time.Date(2026, time.July, 19, 2, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.ForceUpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatal(err)
	}
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: logger},
		tmux:                   tmux.NewClient(tmuxRunner, logger),
		issues:                 issuesClient,
		issueClientsByProject:  map[string]*issues.Client{projectID: issuesClient},
		session:                daemonhandlers.NewSessionHandler(sessionStore),
		sessionStore:           sessionStore,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		materializers:          map[string]*projectReadMaterializer{},
		materializersStarted:   true,
		projectReadRuntimeHydrate: func(_ context.Context, _ string, tasks []domain.Task) ([]domain.Task, error) {
			return nil, errors.New("injected runtime refresh failure")
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]string{"project_id": projectID, "session_id": issueID})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleSessionStopDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion, RequestID: "req-stop-refresh-failure",
		Kind: protocol.EnvelopeKindCommand, Command: "session.stop",
		Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnavailable || !resp.Error.Retryable {
		t.Fatalf("session.stop response = %+v, want retryable unavailable after committed stop", resp)
	}
	if !strings.Contains(resp.Error.Message, "injected runtime refresh failure") || len(resp.Body) != 0 {
		t.Fatalf("session.stop error/body = %+v/%q, want injected refresh failure and no success payload", resp.Error, resp.Body)
	}
	row, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || row.State != daemonstate.SessionStateStopped || row.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("durable session after unavailable = %+v found=%t err=%v, want committed stopped state", row, found, err)
	}
}

func TestMaterializedTaskReadsFailUnavailableWithoutStalePayload(t *testing.T) {
	ctx := context.Background()
	const (
		projectID = "proj-materializer-unavailable"
	)
	issuesClient, _ := newTestIssueClient(t)
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "stale payload must not escape", Status: domain.StatusInProgress, Type: domain.TypeTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) { return tasks, nil })
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	materializer.markUnhealthy(errors.New("injected structural failure"), false)
	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		materializersStarted:  true,
		materializers:         map[string]*projectReadMaterializer{projectID: materializer},
		revision:              map[string]uint64{projectID: 7},
	}
	request := func(command string, body any) protocol.RequestEnvelope {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID("req-unavailable-" + command),
			Kind:            protocol.EnvelopeKindCommand,
			Command:         command,
			Meta:            protocol.Metadata{ProjectID: projectID},
			Body:            encoded,
		}
	}
	tests := []struct {
		name   string
		invoke func() (protocol.ResponseEnvelope, error)
	}{
		{name: "list", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskList(ctx, request("task.list", map[string]any{}))
		}},
		{name: "search", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskList(ctx, request("task.search", map[string]any{"query": "stale"}))
		}},
		{name: "get", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskGet(ctx, request("task.get", map[string]any{"task_id": taskID}))
		}},
		{name: "get-many", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskGetMany(ctx, request("task.get-many", map[string]any{"task_ids": []string{taskID}, "metadata_only": true}))
		}},
		{name: "close-preflight", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskClosePreflight(ctx, request("task.close_preflight", map[string]any{"task_id": taskID}))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp, err := test.invoke()
			if err != nil {
				t.Fatal(err)
			}
			if resp.OK || resp.Error == nil {
				t.Fatalf("response = %+v, want materializer-unavailable error", resp)
			}
			if resp.Error.Code != protocol.ErrorCodeUnavailable || !resp.Error.Retryable {
				t.Fatalf("error = %+v, want retryable unavailable", resp.Error)
			}
			if !strings.Contains(resp.Error.Message, "injected structural failure") {
				t.Fatalf("error message = %q, want injected structural failure", resp.Error.Message)
			}
			if len(resp.Body) != 0 {
				t.Fatalf("body = %q, want no stale payload", resp.Body)
			}
		})
	}
}

func TestProductionRuntimeHydrationFailureDoesNotBlockOrdinaryReads(t *testing.T) {
	ctx := context.Background()
	const projectID = "proj-production-runtime-unavailable"
	issuesClient, _ := newTestIssueClient(t)
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "runtime failure must fail closed", Status: domain.StatusInReview, Type: domain.TypeTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	var failHydration atomic.Bool
	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                issuesClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		materializersStarted:  true,
		materializers:         map[string]*projectReadMaterializer{},
		projectReadRuntimeHydrate: func(_ context.Context, _ string, tasks []domain.Task) ([]domain.Task, error) {
			if failHydration.Load() {
				return nil, errors.New("injected production runtime hydration failure")
			}
			return tasks, nil
		},
		revision: map[string]uint64{projectID: 7},
	}
	if _, err := d.ensureProjectReadMaterializer(ctx, projectID, issuesClient); err != nil {
		t.Fatalf("bootstrap production materializer: %v", err)
	}
	failHydration.Store(true)
	request := func(command string, body any) protocol.RequestEnvelope {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("req-production-unavailable-" + command),
			Kind: protocol.EnvelopeKindCommand, Command: command,
			Meta: protocol.Metadata{ProjectID: projectID}, Body: encoded,
		}
	}
	ordinaryReads := []struct {
		name   string
		invoke func() (protocol.ResponseEnvelope, error)
	}{
		{name: "list", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskList(ctx, request("task.list", map[string]any{}))
		}},
		{name: "search", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskList(ctx, request("task.search", map[string]any{"query": "runtime failure"}))
		}},
		{name: "get", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskGet(ctx, request("task.get", map[string]any{"task_id": taskID}))
		}},
		{name: "get-many", invoke: func() (protocol.ResponseEnvelope, error) {
			return d.handleTaskGetMany(ctx, request("task.get-many", map[string]any{"task_ids": []string{taskID}, "metadata_only": true}))
		}},
	}
	for _, test := range ordinaryReads {
		t.Run(test.name, func(t *testing.T) {
			resp, err := test.invoke()
			if err != nil {
				t.Fatal(err)
			}
			if !resp.OK || resp.Error != nil || len(resp.Body) == 0 {
				t.Fatalf("response = %+v, want last verified projected payload without synchronous hydration", resp)
			}
		})
	}
	preflight, err := d.handleTaskClosePreflight(ctx, request("task.close_preflight", map[string]any{"task_id": taskID}))
	if err != nil {
		t.Fatal(err)
	}
	if preflight.OK || preflight.Error == nil || preflight.Error.Code != protocol.ErrorCodeUnavailable || !preflight.Error.Retryable {
		t.Fatalf("close preflight response = %+v, want strict invariant refresh failure", preflight)
	}
	if !strings.Contains(preflight.Error.Message, "injected production runtime hydration failure") || len(preflight.Body) != 0 {
		t.Fatalf("close preflight error/body = %+v/%q, want strict hydration failure", preflight.Error, preflight.Body)
	}
	deletePreflight, err := d.handleTaskDeletePreflight(ctx, request("task.delete_preflight", map[string]any{"task_id": taskID}))
	if err != nil {
		t.Fatal(err)
	}
	if deletePreflight.OK || deletePreflight.Error == nil || deletePreflight.Error.Code != protocol.ErrorCodeUnavailable || !deletePreflight.Error.Retryable || len(deletePreflight.Body) != 0 {
		t.Fatalf("delete preflight response = %+v, want retryable unavailable without payload", deletePreflight)
	}
	deleteResp, err := d.handleTaskDelete(ctx, request("task.delete", taskDeleteRequest{TaskID: taskID}))
	if err != nil {
		t.Fatal(err)
	}
	if deleteResp.OK || deleteResp.Error == nil || deleteResp.Error.Code != protocol.ErrorCodeUnavailable || !deleteResp.Error.Retryable || len(deleteResp.Body) != 0 {
		t.Fatalf("task.delete response = %+v, want retryable unavailable without payload", deleteResp)
	}
	if _, err := issuesClient.GetWithRuntime(ctx, projectID, taskID); err != nil {
		t.Fatalf("task mutated after unavailable delete preflight: %v", err)
	}
}

func TestEmbeddedOrdinaryReadUsesCanonicalBootstrapOnly(t *testing.T) {
	ctx := context.Background()
	const projectID = "proj-embedded-canonical-only"
	issuesClient, repoDir := newTestIssueClient(t)
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "embedded canonical row", Status: domain.StatusInProgress, Type: domain.TypeTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "archived canonical child", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &parentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := issuesClient.Archive(ctx, childID); err != nil {
		t.Fatal(err)
	}
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID: naming.CanonicalSessionID(projectID, parentID), IssueID: parentID,
		State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateAttached, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                issuesClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		materializers:         map[string]*projectReadMaterializer{},
		projectReadRuntimeHydrate: func(_ context.Context, _ string, tasks []domain.Task) ([]domain.Task, error) {
			hydrateCalls.Add(1)
			return nil, errors.New("ordinary embedded read invoked runtime hydration")
		},
		revision: map[string]uint64{projectID: 1},
	}
	tasks, _, err := d.convergedProjectReadSnapshot(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if got := hydrateCalls.Load(); got != 0 {
		t.Fatalf("embedded ordinary read hydration calls = %d, want 0", got)
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	parent, parentFound := byID[parentID]
	child, childFound := byID[childID]
	if !parentFound || !childFound || !child.State.IsArchived() || child.ParentID == nil || child.ParentID.String() != parentID {
		t.Fatalf("canonical bootstrap tasks = %+v, want active parent plus archived dependent child", tasks)
	}
	if parent.Session != nil || parent.HasTmuxSession || parent.HasWorktree {
		t.Fatalf("canonical bootstrap leaked runtime projection: %+v", parent)
	}
}

func TestOrdinaryReadDoesNotInitializeMissingProductionMaterializer(t *testing.T) {
	ctx := context.Background()
	const projectID = "proj-missing-production-materializer"
	issuesClient, _ := newTestIssueClient(t)
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "durable but not initialized", Status: domain.StatusInProgress, Type: domain.TypeTask,
	}); err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		materializersStarted:  true,
		materializers:         map[string]*projectReadMaterializer{},
		projectReadRuntimeHydrate: func(_ context.Context, _ string, tasks []domain.Task) ([]domain.Task, error) {
			hydrateCalls.Add(1)
			return tasks, nil
		},
		revision: map[string]uint64{projectID: 1},
	}
	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-missing-production-materializer",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnavailable {
		t.Fatalf("task.list response = %+v, want unavailable materializer", resp)
	}
	if got := hydrateCalls.Load(); got != 0 {
		t.Fatalf("ordinary read initialized runtime projection %d times, want 0", got)
	}
}

func TestEmbeddedInvariantReadUsesStrictHydrationAfterBootstrap(t *testing.T) {
	ctx := context.Background()
	const projectID = "proj-embedded-runtime-unavailable"
	issuesClient, _ := newTestIssueClient(t)
	_, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "embedded strict runtime", Status: domain.StatusInProgress, Type: domain.TypeTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                issuesClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		materializers:         map[string]*projectReadMaterializer{},
		projectReadRuntimeHydrate: func(_ context.Context, _ string, tasks []domain.Task) ([]domain.Task, error) {
			if hydrateCalls.Add(1) > 1 {
				return nil, errors.New("injected embedded strict hydration failure")
			}
			return tasks, nil
		},
		revision: map[string]uint64{projectID: 1},
	}
	tasks, _, err := d.convergedProjectReadSnapshotForInvariant(ctx, projectID)
	if err == nil || !isProjectReadUnavailableError(err) {
		t.Fatalf("tasks/error = %+v/%v, want project-read unavailable", tasks, err)
	}
	if !strings.Contains(err.Error(), "injected embedded strict hydration failure") {
		t.Fatalf("error = %v, want embedded strict failure", err)
	}
}

func TestTaskCloseRepairsVerifiedStaleLegacyProjectSessionProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy-session-stale"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	preservedWrongProjectWorktree := filepath.Join(t.TempDir(), "wrong-project-worktree")
	if err := os.MkdirAll(preservedWrongProjectWorktree, 0o755); err != nil {
		t.Fatalf("create preserved wrong-project worktree: %v", err)
	}

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with stale legacy session alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	sessionID := "legacy-" + taskID
	if err := upsertSessionStateFixture(runtimeStore, ctx, legacyProjectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale legacy session projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		tmux:     tmux.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) { return "", nil }}, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID, CloseOutcome: string(domain.IssueCloseCancelled)})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-stale-legacy-session",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v, want success", resp)
	}
	session, found, err := runtimeStore.GetSessionState(ctx, legacyProjectID, sessionID)
	if err != nil {
		t.Fatalf("get stale legacy session projection: %v", err)
	}
	if !found {
		t.Fatalf("stale legacy session projection missing for %s", sessionID)
	}
	if session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped || session.TmuxAttachedCount != 0 {
		t.Fatalf("legacy session projection = %+v, want stopped repair marker", session)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get cancelled issue: %v", err)
	}
	if task.Status != domain.StatusCancelled {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusCancelled)
	}
	if _, err := os.Stat(preservedWrongProjectWorktree); err != nil {
		t.Fatalf("preserved wrong-project worktree: %v", err)
	}
	resp, err = d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-stale-legacy-session-retry",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("retry handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("retry task.close response = %+v, want idempotent success", resp)
	}
}

func TestTaskCloseRepairsStaleBusyHookProjectionBeforePreflight(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-stale-hook-preflight"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close after close-session with stale hook activity",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	sessionID := "ch-" + taskID
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        taskID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale busy hook session projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		tmux:     tmux.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) { return "", nil }}, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-stale-hook-preflight",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v, want stale hook projection repaired before preflight", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get closed issue: %v", err)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
	}
	session, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get repaired session projection: %v", err)
	}
	if !found {
		t.Fatalf("repaired session projection missing for %s", sessionID)
	}
	if session.State != daemonstate.SessionStateStopped ||
		session.ObservedState != daemonstate.SessionStateStopped ||
		session.Activity != "" ||
		session.ActivitySource != "" {
		t.Fatalf("session projection = %+v, want stopped with cleared hook activity", session)
	}
}

func TestTaskCloseBlocksLiveLegacyProjectRuntimeProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy-live"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with live legacy projection alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	worktreePath := filepath.Join(t.TempDir(), "legacy-live-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir legacy worktree: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    "riordan/" + taskID + "/legacy-live",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed live legacy worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-live-legacy-projection",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "active runtime projection aliases remain") {
		t.Fatalf("task.close response = %+v, want live alias blocker", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get issue after blocked close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusInReview)
	}
	if _, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get live legacy worktree projection: %v", err)
	} else if !found {
		t.Fatalf("live legacy worktree projection was removed for %s", taskID)
	}
}

func TestTaskCloseBlocksUnverifiedLegacyProjectSessionProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy-session"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with unverified legacy session alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStore, ctx, legacyProjectID, daemonstate.Session{
		ID:        "legacy-" + taskID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy session projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-unverified-legacy-session",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "active runtime projection aliases remain") {
		t.Fatalf("task.close response = %+v, want unverified session alias blocker", resp)
	}
	session, found, err := runtimeStore.GetSessionState(ctx, legacyProjectID, "legacy-"+taskID)
	if err != nil {
		t.Fatalf("get legacy session projection: %v", err)
	}
	if !found || session.State != daemonstate.SessionStateRunning {
		t.Fatalf("legacy session projection = %+v found=%t, want running row preserved", session, found)
	}
}

func TestTaskCloseRunsIssueResourceCleanupWithoutSessionBeforeWorktreeRemoval(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-resource-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Close with resource cleanup",
		Type:   domain.TypeTask,
		Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	branchName := "riordan/" + taskID + "/resource-cleanup"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "cleanup-marker")
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			if _, err := os.Stat(cleanupMarker); err != nil {
				return "", fmt.Errorf("cleanup marker missing before remove: %w", err)
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" > %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: runtimeStore,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	want := taskID + "|" + worktreePath + "|" + branchName
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	if !strings.Contains(strings.Join(commands, "\n"), "worktree remove "+worktreePath) {
		t.Fatalf("commands = %v, want worktree remove", commands)
	}
}

func TestTaskCloseRunsIssueResourceCleanupWithSessionBeforeWorktreeRemoval(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-session-resource-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Close with session resource cleanup",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	branchName := "riordan/" + taskID + "/session-resource-cleanup"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        taskID,
		State:          daemonstate.SessionStateAttached,
		ObservedState:  daemonstate.SessionStateAttached,
		Activity:       "idle",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "cleanup-marker")
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			if _, err := os.Stat(cleanupMarker); err != nil {
				return "", fmt.Errorf("cleanup marker missing before remove: %w", err)
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	sessionStore := daemonstate.NewStore()
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				Env: map[string]string{
					"LOCAL_POSTGRES_DEV_DATABASE": "chefy_$AZEDARACH_ISSUE_ID",
				},
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" \"$LOCAL_POSTGRES_DEV_DATABASE\" >> %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		sessionStore: sessionStore,
		session:      daemonhandlers.NewSessionHandler(sessionStore),
		tmux:         tmux.NewClient(tmuxRunner, logger),
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: runtimeStore,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-session-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	want := taskID + "|" + worktreePath + "|" + branchName + "|chefy_" + taskID
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	if !strings.Contains(strings.Join(commands, "\n"), "worktree remove "+worktreePath) {
		t.Fatalf("commands = %v, want worktree remove", commands)
	}
}

func TestCleanupTaskIssueResourcesRunsReconcileAbsentBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	worktreePath := filepath.Join(repoDir, "wt-az-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	marker := filepath.Join(repoDir, "resource-order")
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				ReconcileCommand: fmt.Sprintf("printf '%%s\\n' \"$AZEDARACH_RESOURCE_DESIRED_STATE\" >> %q", marker),
				CleanupCommands: []string{
					fmt.Sprintf("printf 'cleanup\\n' >> %q", marker),
				},
			},
			Logger: slog.Default(),
		},
	}

	if err := d.cleanupTaskIssueResourcesForClose(ctx, protocol.DefaultProjectID, "az-1", worktreePath); err != nil {
		t.Fatalf("cleanupTaskIssueResourcesForClose error: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), "absent\ncleanup"; got != want {
		t.Fatalf("resource order = %q, want %q", got, want)
	}
}

func TestCleanupTaskIssueResourcesReconcileAbsentFailureSkipsCleanup(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	worktreePath := filepath.Join(repoDir, "wt-az-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	marker := filepath.Join(repoDir, "resource-order")
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				ReconcileCommand: "printf 'absent failed'; exit 7",
				CleanupCommands: []string{
					fmt.Sprintf("printf 'cleanup\\n' >> %q", marker),
				},
			},
			Logger: slog.Default(),
		},
	}

	err := d.cleanupTaskIssueResourcesForClose(ctx, protocol.DefaultProjectID, "az-1", worktreePath)
	if err == nil {
		t.Fatal("cleanupTaskIssueResourcesForClose error = nil, want reconcile failure")
	}
	if !strings.Contains(err.Error(), "absent failed") {
		t.Fatalf("cleanup error = %q, want reconcile command output", err.Error())
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cleanup marker stat = %v, want not exist", statErr)
	}
}

func TestTaskUpdateStatusRejectsRawCloseActiveRuntime(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-reopen-target"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed with live session",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID:            "sess-" + taskID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-reopen-target",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get guarded issue after failed close: %v", err)
	}
	if task.Status != domain.StatusInProgress {
		t.Fatalf("guarded issue status = %s, want %s", task.Status, domain.StatusInProgress)
	}
	if strings.Contains(resp.Error.Message, "Moved closed blockers back for cleanup") {
		t.Fatalf("task.update_status response = %q, did not expect status repair for reachable active issue", resp.Error.Message)
	}

	body, err = json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusCancelled,
	})
	if err != nil {
		t.Fatalf("marshal cancelled task update request: %v", err)
	}
	resp, err = d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cancel-guard-reopen-target",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus cancelled error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status cancelled must be applied with task.close") {
		t.Fatalf("task.update_status cancelled response = %+v, want raw cancel rejection", resp)
	}
}

func TestTaskUpdateStatusRejectsRawCloseUnresolvedChildrenAndApplyPath(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-children"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	}); err != nil {
		t.Fatalf("create child: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(map[string]any{
		"task_id": parentID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-children",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}

	body, err = json.Marshal(map[string]any{
		"task_id": parentID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal skip task update request: %v", err)
	}
	resp, err = d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-children-skip",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus skip error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status skip response = %+v, want raw close rejection", resp)
	}

	_, err = d.Update(withDaemonProjectIDContext(ctx, projectID), parentID, domain.StatusDone)
	if err == nil || !strings.Contains(err.Error(), "status closed must be applied with task.close") {
		t.Fatalf("apply Update error = %v, want raw close rejection", err)
	}
}

func TestTaskCloseCommandIntegratesThroughDaemon(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Integrate me",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{
		OwnerID:   "worker-a",
		OwnerKind: "agent",
		Purpose:   domain.CoordinationLeaseExecution,
	}); err != nil {
		t.Fatalf("claim execution lease: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/integrate"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	var scratchWorktree string
	var mergeBudget time.Duration
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case integrationTestIsWorktreeList(args):
			return integrationTestWorktreeList(worktreeListOutput, scratchWorktree, "merged-sha"), nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "branch" && args[3] == "--show-current":
			return "main", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == sourceBranch+"^{commit}":
			return "source-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "main^{commit}":
			return "merged-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) == 4 && args[0] == "-C" && args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(repoDir, ".git", "worktrees", filepath.Base(scratchWorktree)), nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			scratchHook := filepath.Join(scratchWorktree, ".git", "hooks", "commit-msg")
			if err := os.MkdirAll(filepath.Dir(scratchHook), 0o755); err != nil {
				t.Fatalf("mkdir scratch hooks: %v", err)
			}
			if err := os.WriteFile(scratchHook, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
				t.Fatalf("write scratch hook: %v", err)
			}
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			time.Sleep(20 * time.Millisecond)
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}
	runner.runWithEnvFn = func(runCtx context.Context, extraEnv []string, args ...string) (string, error) {
		tracePath := ""
		for _, entry := range extraEnv {
			if value, ok := strings.CutPrefix(entry, "GIT_TRACE2_EVENT="); ok {
				tracePath = value
				break
			}
		}
		startedAt := time.Now().UTC()
		if len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge" {
			if deadline, ok := runCtx.Deadline(); ok {
				mergeBudget = time.Until(deadline)
			}
		}
		output, err := runner.Run(runCtx, args...)
		endedAt := time.Now().UTC()
		if tracePath != "" && len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge" {
			elapsed := endedAt.Sub(startedAt).Seconds()
			trace := fmt.Sprintf(
				"{\"event\":\"child_start\",\"time\":%q,\"child_id\":1,\"child_class\":\"hook\",\"hook_name\":\"commit-msg\",\"argv\":[\".git/hooks/commit-msg\"]}\n"+
					"{\"event\":\"child_exit\",\"time\":%q,\"child_id\":1,\"code\":0,\"t_rel\":%.6f}\n",
				startedAt.Format(time.RFC3339Nano),
				endedAt.Format(time.RFC3339Nano),
				elapsed,
			)
			if writeErr := os.WriteFile(tracePath, []byte(trace), 0o644); writeErr != nil {
				t.Fatalf("write trace2 hook events: %v", writeErr)
			}
		}
		return output, err
	}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskClose response = %+v error=%q", resp, responseErrorMessage(resp))
	}
	if mergeBudget < domain.IntegrationMergeTimeout-time.Second || mergeBudget > domain.IntegrationMergeTimeout {
		t.Fatalf("daemon close merge budget = %v, want close to %v with cleanup reserve retained", mergeBudget, domain.IntegrationMergeTimeout)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || !result.Integrated || !result.WorktreeRemoved || result.IntegratedSourceBranch != sourceBranch || result.IntegratedTargetBranch != "main" {
		t.Fatalf("close integration result = %+v", result)
	}
	hookPhase, ok := taskClosePhaseByName(result.Phases, "githook.commit-msg")
	if !ok {
		t.Fatalf("close phases = %+v, want githook.commit-msg", result.Phases)
	}
	if hookPhase.Hook != "commit-msg" || hookPhase.Command != "git merge --..." || hookPhase.ElapsedMS < 15 {
		t.Fatalf("hook phase = %+v, want commit-msg merge timing", hookPhase)
	}
	if hookPhase.ExitStatus == nil || *hookPhase.ExitStatus != 0 || hookPhase.Blocking == nil || !*hookPhase.Blocking {
		t.Fatalf("hook phase = %+v, want exit_status=0 blocking=true", hookPhase)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	if closed.Ownership != nil || len(closed.CoordinationLeases) != 0 {
		t.Fatalf("closed task leases = %+v ownership = %+v, want none", closed.CoordinationLeases, closed.Ownership)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"-C " + sourceWorktree + " status --porcelain",
		"-C " + repoDir + " merge-tree --write-tree main " + sourceBranch,
		"-C " + repoDir + " checkout main",
		"-C " + repoDir + " worktree add --detach ",
		"merge --no-edit " + sourceBranch,
		"-C " + repoDir + " reset --hard merged-sha",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-C "+repoDir+" fetch origin") {
		t.Fatalf("git commands should not fetch during local close integration:\n%s", joined)
	}
}

func TestTaskCloseCommandIntegrationIgnoresDuplicateIssueTargetWorktreeFromOtherProject(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-target"
	repoDir := t.TempDir()
	otherRepo := filepath.Join(t.TempDir(), "Chefy-production")
	if err := os.MkdirAll(otherRepo, 0o755); err != nil {
		t.Fatalf("mkdir other repo: %v", err)
	}
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Duplicate issue id integration",
		Type:     domain.TypeBug,
		Priority: domain.P0,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/integrate"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := strings.Join([]string{
		fmt.Sprintf("worktree %s\nbranch refs/heads/main\n", otherRepo),
		fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n", sourceWorktree, sourceBranch),
	}, "\n")
	commands := make([]string, 0, 24)
	var scratchWorktree string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case integrationTestIsWorktreeList(args):
			return integrationTestWorktreeList(worktreeListOutput, scratchWorktree, "merged-sha"), nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == otherRepo && args[2] == "status":
			return "A  domain/commerce/tsconfig.json\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "branch" && args[3] == "--show-current":
			return "main", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == sourceBranch+"^{commit}":
			return "source-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "main^{commit}":
			return "merged-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(repoDir, ".git", "worktrees", filepath.Base(scratchWorktree)), nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-duplicate-project",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || !result.Integrated || result.IntegratedTargetBranch != "main" {
		t.Fatalf("close integration result = %+v, want project-scoped integration to main", result)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "-C "+otherRepo+" ") {
		t.Fatalf("git commands used non-target project worktree:\n%s", joined)
	}
	if !strings.Contains(joined, "-C "+repoDir+" status --porcelain") {
		t.Fatalf("git commands missing requested project target status:\n%s", joined)
	}
}

func TestTaskCloseCommandRetryRepairsProjectionAndReleasesLeaseAfterIntegratedWorktreeWasRemoved(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-cleanup-fail-retry"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Integrated cleanup retry",
		Type:     domain.TypeBug,
		Priority: domain.P1,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{
		OwnerID:   "worker-a",
		OwnerKind: "agent",
		Purpose:   domain.CoordinationLeaseExecution,
	}); err != nil {
		t.Fatalf("claim execution lease: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/integrated-cleanup-retry"
	projectionDB, err := sql.Open("sqlite", issuesDBPath)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	defer projectionDB.Close()
	lockConn, err := projectionDB.Conn(ctx)
	if err != nil {
		t.Fatalf("open cleanup lock connection: %v", err)
	}
	defer lockConn.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := projectionDB.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, projectID, taskID, sourceWorktree, sourceBranch, now); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	preflightTasks, err := issuesClient.ListParentChildSubtreeWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("precondition list runtime subtree: %v", err)
	}
	if len(preflightTasks) != 1 || !preflightTasks[0].HasWorktree {
		t.Fatalf("precondition task runtime = %+v, want worktree projection", preflightTasks)
	}

	commands := make([]string, 0, 32)
	var scratchWorktree string
	sourceUniqueReads := 0
	removeAttempts := 0
	branchRemoved := false
	cleanupLocked := false
	var cancelFirst context.CancelFunc
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case integrationTestIsWorktreeList(args):
			if removeAttempts >= 1 {
				return integrationTestWorktreeList(fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), scratchWorktree, "merged-sha"), nil
			}
			return integrationTestWorktreeList(fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\nworktree %s\nbranch refs/heads/%s\n\n", repoDir, sourceWorktree, sourceBranch), scratchWorktree, "merged-sha"), nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status" && removeAttempts > 0:
			return "", fmt.Errorf("cannot change to %s: no such file or directory", sourceWorktree)
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "branch" && args[3] == "--show-current":
			return "main", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == sourceBranch+"^{commit}":
			if branchRemoved {
				return "", fmt.Errorf("unknown revision %s", sourceBranch)
			}
			return "source-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "main^{commit}":
			return "merged-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(repoDir, ".git", "worktrees", filepath.Base(scratchWorktree)), nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			sourceUniqueReads++
			if sourceUniqueReads == 1 {
				return "1", nil
			}
			return "", fmt.Errorf("fatal: ambiguous argument %s: unknown revision", sourceBranch)
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "branch" && args[3] == "--list":
			if branchRemoved {
				return "", nil
			}
			return sourceBranch, nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "log":
			return "merged-sha\x00" + taskID + ": integrated cleanup retry\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			removedPath := args[len(args)-1]
			if removedPath == scratchWorktree {
				return "", nil
			}
			if removedPath != sourceWorktree {
				return "", fmt.Errorf("unexpected worktree remove path: %s", removedPath)
			}
			removeAttempts++
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			removedPath := args[len(args)-1]
			if removedPath != sourceWorktree {
				return "", fmt.Errorf("unexpected worktree remove path: %s", removedPath)
			}
			removeAttempts++
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			if branchRemoved {
				return "", nil
			}
			branchRemoved = true
			if _, err := lockConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
				return "", fmt.Errorf("lock projection cleanup: %w", err)
			}
			cleanupLocked = true
			if cancelFirst != nil {
				cancelFirst()
			}
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	firstCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	cancelFirst = cancel
	defer cancelFirst()
	resp, err := d.handleTaskClose(firstCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrated-cleanup-fail",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose first error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("first handleTaskClose response = %+v, want cleanup failure", resp)
	}
	for _, want := range []string{"phase worktree_cleanup", "Integration already completed", sourceBranch, "landed on main", "cleanup/status remains", "Next:"} {
		if !strings.Contains(resp.Error.Message, want) {
			t.Fatalf("first close error = %q, missing %q", resp.Error.Message, want)
		}
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after failed close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status after failed close = %s, want %s", task.Status, domain.StatusInReview)
	}
	if task.Ownership == nil || task.Ownership.OwnerID != "worker-a" {
		t.Fatalf("task ownership after failed close = %+v, want execution lease preserved", task.Ownership)
	}
	receipts, err := issuesClient.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted},
	})
	if err != nil {
		t.Fatalf("list exact integration receipts: %v", err)
	}
	if len(receipts) != 1 || observationPayloadString(receipts[0].Payload, "source_oid") != "source-sha" || observationPayloadString(receipts[0].Payload, "target_oid") != "merged-sha" {
		t.Fatalf("exact integration receipts = %+v, want one source-sha/merged-sha receipt before destructive cleanup", receipts)
	}
	originalReceipt := receipts[0]
	if _, err := os.Stat(sourceWorktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source worktree after failed close stat error = %v, want removed path", err)
	}
	if !cleanupLocked {
		t.Fatal("first close did not hold the projection cleanup lock")
	}
	if _, err := lockConn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatalf("release projection cleanup lock: %v", err)
	}
	if err := lockConn.Close(); err != nil {
		t.Fatalf("close projection cleanup lock connection: %v", err)
	}
	cleanupLocked = false

	resp, err = d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrated-cleanup-retry",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose retry error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("retry handleTaskClose error = %s\ncommands:\n%s", resp.Error.Message, strings.Join(commands, "\n"))
		}
		t.Fatalf("retry handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal retry close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated || !result.WorktreeRemoved {
		t.Fatalf("retry close result = %+v, want no-op integration plus worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after retry close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status after retry = %s, want %s", closed.Status, domain.StatusDone)
	}
	if closed.Ownership != nil || len(closed.CoordinationLeases) != 0 {
		t.Fatalf("closed task leases = %+v ownership = %+v, want terminal retry to release execution lease", closed.CoordinationLeases, closed.Ownership)
	}
	receipts, err = issuesClient.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted},
	})
	if err != nil {
		t.Fatalf("list exact integration receipts after retry: %v", err)
	}
	if len(receipts) != 1 || receipts[0].ID != originalReceipt.ID || !reflect.DeepEqual(receipts[0].Payload, originalReceipt.Payload) {
		t.Fatalf("exact integration receipts after retry = %+v, want only original receipt id=%d payload=%+v", receipts, originalReceipt.ID, originalReceipt.Payload)
	}
	joined := strings.Join(commands, "\n")
	if got := strings.Count(joined, "merge --no-edit "+sourceBranch); got != 1 {
		t.Fatalf("merge count = %d, want one initial merge only:\n%s", got, joined)
	}
	if sourceUniqueReads != 2 {
		t.Fatalf("main..source containment reads = %d, want first merge check plus retry no-op check", sourceUniqueReads)
	}
	if removeAttempts != 1 {
		t.Fatalf("worktree remove attempts = %d, want physical cleanup only on the first close", removeAttempts)
	}
}

type codedSQLiteDaemonTestError struct{ code int }

func (e codedSQLiteDaemonTestError) Error() string { return "injected sqlite contention" }
func (e codedSQLiteDaemonTestError) Code() int     { return e.code }

func TestTaskCloseSameCallRetriesBusySnapshotWithoutRepeatingIntegrationOrCleanup(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-same-call-busy-snapshot"
	repoDir := t.TempDir()
	runDaemonTestGit(t, repoDir, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repoDir, "config", "user.name", "Azedarach Test")
	runDaemonTestGit(t, repoDir, "config", "user.email", "azedarach@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repoDir, "add", "tracked.txt")
	runDaemonTestGit(t, repoDir, "commit", "-q", "-m", "chore: seed")

	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Same-call projection retry", Type: domain.TypeBug, Priority: domain.P1,
		Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceBranch := "riordan/" + taskID + "/same-call-projection-retry"
	sourceWorktree := filepath.Join(t.TempDir(), "wt-"+taskID)
	runDaemonTestGit(t, repoDir, "worktree", "add", "-q", "-b", sourceBranch, sourceWorktree)
	if err := os.WriteFile(filepath.Join(sourceWorktree, "tracked.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, sourceWorktree, "commit", "-q", "-am", "fix("+taskID+"): exercise same-call retry")
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID, IssueID: taskID, Path: sourceWorktree, Branch: sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var commandsMu sync.Mutex
	commands := make([]string, 0, 64)
	execRunner := git.NewExecRunner(repoDir)
	runner := &recordingGitRunner{runWithContextFn: func(runCtx context.Context, args ...string) (string, error) {
		commandsMu.Lock()
		commands = append(commands, strings.Join(args, " "))
		commandsMu.Unlock()
		return execRunner.Run(runCtx, args...)
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg:                       Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject:     map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
		git:                       git.NewClient(runner, logger), revision: map[string]uint64{projectID: 1},
		hub: publish.NewHub(16, 8, logger),
	}
	d.gitStatusAdapter = &gitServiceAdapter{
		client: git.NewClient(runner, logger), runtimeStateStore: runtimeStore,
		logger: logger, baseBranch: "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject:           func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore { return runtimeStore },
		runtimeProjectionWriter:     d.runtimeProjectionStateWriter(), logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	var projectionAttempts atomic.Int32
	closeCtx := daemonstate.WithSQLiteWriteAttemptHookForTest(ctx, func(operation string, attempt int) error {
		if operation != "delete_worktree_state" {
			return nil
		}
		projectionAttempts.Store(int32(attempt))
		if attempt <= 2 {
			return codedSQLiteDaemonTestError{code: 517}
		}
		return nil
	})
	body, err := json.Marshal(taskCloseRequest{TaskID: taskID, IntegrateBeforeClose: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleTaskClose(closeCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion, RequestID: "req-close-same-call-busy-snapshot",
		Kind: protocol.EnvelopeKindCommand, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command: "task.close", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v", resp)
	}
	if got := projectionAttempts.Load(); got != 3 {
		t.Fatalf("delete projection attempts = %d, want 3", got)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil || closed.Status != domain.StatusDone {
		t.Fatalf("closed task status=%s err=%v", closed.Status, err)
	}
	if _, err := os.Stat(sourceWorktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source worktree still present: %v", err)
	}
	receipts, err := issuesClient.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted},
	})
	if err != nil || len(receipts) != 1 {
		t.Fatalf("integration receipts=%+v err=%v, want exactly one", receipts, err)
	}
	commandsMu.Lock()
	commandsSnapshot := append([]string(nil), commands...)
	commandsMu.Unlock()
	joined := strings.Join(commandsSnapshot, "\n")
	if got := strings.Count(joined, "merge --no-edit "+sourceBranch); got != 1 {
		t.Fatalf("merge count = %d, want 1:\n%s", got, joined)
	}
	cleanupCount := 0
	for _, command := range commandsSnapshot {
		fields := strings.Fields(command)
		if len(fields) >= 3 && strings.Contains(command, "worktree remove") && filepath.Base(fields[len(fields)-1]) == filepath.Base(sourceWorktree) {
			cleanupCount++
		}
	}
	if cleanupCount != 1 {
		t.Fatalf("source cleanup count = %d, want 1:\n%s", cleanupCount, joined)
	}
}

func TestTaskClosePostIntegrationPhaseErrorTreatsNoChangesAsIntegratedEvidence(t *testing.T) {
	err := taskClosePostIntegrationPhaseError("az-1", "status_write", taskCloseIntegrationResult{
		Requested:    true,
		NoChanges:    true,
		SourceBranch: "riordan/az-1/already-landed",
		TargetBranch: "main",
	}, fmt.Errorf("status write failed"))

	for _, want := range []string{"Integration already completed", "riordan/az-1/already-landed", "landed on main", "retry close"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, missing %q", err.Error(), want)
		}
	}
}

func TestTaskCloseIntegrationReceiptAcceptsConventionalCommitOnRealTarget(t *testing.T) {
	repoDir := t.TempDir()
	runDaemonTestGit(t, repoDir, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repoDir, "config", "user.name", "Azedarach Test")
	runDaemonTestGit(t, repoDir, "config", "user.email", "azedarach@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runDaemonTestGit(t, repoDir, "add", "tracked.txt")
	runDaemonTestGit(t, repoDir, "commit", "-q", "-m", "chore: seed")
	sourceBranch := "riordan/djb/conventional"
	runDaemonTestGit(t, repoDir, "checkout", "-q", "-b", sourceBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	runDaemonTestGit(t, repoDir, "commit", "-q", "-am", "fix(djb): tolerate cleanup retry")
	sourceOID := runDaemonTestGitOutput(t, repoDir, "rev-parse", "HEAD")
	runDaemonTestGit(t, repoDir, "checkout", "-q", "main")
	runDaemonTestGit(t, repoDir, "merge", "-q", "--ff-only", sourceBranch)
	targetOID := runDaemonTestGitOutput(t, repoDir, "rev-parse", "main")
	runDaemonTestGit(t, repoDir, "branch", "-D", sourceBranch)

	receipt := taskCloseIntegrationReceipt{
		ProjectID:    "proj-real",
		SourceBranch: sourceBranch,
		TargetBranch: "main",
		SourceOID:    sourceOID,
		TargetOID:    targetOID,
	}
	client := git.NewClient(git.NewExecRunner(repoDir), slog.Default())
	if err := verifyTaskCloseIntegrationReceipt(context.Background(), client, repoDir, receipt, "proj-real", sourceBranch, "main"); err != nil {
		t.Fatalf("verify exact conventional-commit receipt: %v", err)
	}
}

func TestTaskCloseIntegrationReceiptRejectsOlderSubjectEvidenceForDeletedUnintegratedTip(t *testing.T) {
	repoDir := t.TempDir()
	runDaemonTestGit(t, repoDir, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repoDir, "config", "user.name", "Azedarach Test")
	runDaemonTestGit(t, repoDir, "config", "user.email", "azedarach@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write old evidence: %v", err)
	}
	runDaemonTestGit(t, repoDir, "add", "tracked.txt")
	runDaemonTestGit(t, repoDir, "commit", "-q", "-m", "djb: older integrated work")
	sourceBranch := "riordan/djb/unintegrated"
	runDaemonTestGit(t, repoDir, "checkout", "-q", "-b", sourceBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("new unintegrated\n"), 0o644); err != nil {
		t.Fatalf("write unintegrated source: %v", err)
	}
	runDaemonTestGit(t, repoDir, "commit", "-q", "-am", "fix(djb): newer unintegrated work")
	deletedSourceOID := runDaemonTestGitOutput(t, repoDir, "rev-parse", "HEAD")
	runDaemonTestGit(t, repoDir, "checkout", "-q", "main")
	runDaemonTestGit(t, repoDir, "branch", "-D", sourceBranch)

	client := git.NewClient(git.NewExecRunner(repoDir), slog.Default())
	older, err := client.IssueEvidenceCommits(context.Background(), repoDir, "main", "djb")
	if err != nil || len(older) == 0 {
		t.Fatalf("precondition older issue subject evidence = %+v, err=%v", older, err)
	}
	receipt := taskCloseIntegrationReceipt{
		ProjectID:    "proj-real",
		SourceBranch: sourceBranch,
		TargetBranch: "main",
		SourceOID:    deletedSourceOID,
		TargetOID:    runDaemonTestGitOutput(t, repoDir, "rev-parse", "main"),
	}
	err = verifyTaskCloseIntegrationReceipt(context.Background(), client, repoDir, receipt, "proj-real", sourceBranch, "main")
	if err == nil || !strings.Contains(err.Error(), "recorded source OID is not reachable from main") {
		t.Fatalf("verify deleted unintegrated tip error = %v, want exact source reachability refusal", err)
	}
}

func TestTaskCloseExactReceiptAcceptsAndRepairsMissingProjectionIdempotentlyInRealRepo(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	runDaemonTestGit(t, repoDir, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repoDir, "config", "user.name", "Azedarach Test")
	runDaemonTestGit(t, repoDir, "config", "user.email", "azedarach@example.com")
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".azedarach", "config.json"), []byte(`{"publicationEvidence":{"policyVersion":"portable-v1"}}`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runDaemonTestGit(t, repoDir, "add", "tracked.txt")
	runDaemonTestGit(t, repoDir, "commit", "-q", "-m", "chore: seed")
	baseOID := runDaemonTestGitOutput(t, repoDir, "rev-parse", "HEAD")

	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("resolve project id: %v", err)
	}
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Repair exact receipt retry", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/exact-receipt"
	runDaemonTestGit(t, repoDir, "checkout", "-q", "-b", sourceBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	runDaemonTestGit(t, repoDir, "commit", "-q", "-am", "fix("+taskID+"): exact receipt")
	sourceOID := runDaemonTestGitOutput(t, repoDir, "rev-parse", "HEAD")
	runDaemonTestGit(t, repoDir, "checkout", "-q", "main")
	runDaemonTestGit(t, repoDir, "merge", "-q", "--ff-only", sourceBranch)
	targetOID := runDaemonTestGitOutput(t, repoDir, "rev-parse", "main")
	runDaemonTestGit(t, repoDir, "branch", "-D", sourceBranch)
	missingWorktree := filepath.Join(t.TempDir(), "removed-worktree")
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID, IssueID: taskID, Path: missingWorktree, Branch: sourceBranch, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale worktree projection: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": sourceBranch, "target_branch": "main",
			"integrated": true, "configured_base_target": true, "target_id": "base",
			"base_oid": baseOID, "source_oid": sourceOID, "target_oid": targetOID, "publication_operation_id": "",
		},
	}); err != nil {
		t.Fatalf("seed exact integration receipt: %v", err)
	}

	runner := git.NewExecRunner(repoDir)
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg:                       Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		git:                       git.NewClient(runner, logger),
		issueClientsByProject:     map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject:           func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore { return runtimeStore },
		logger:                      logger,
	}
	recovered, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true, true, "", "")
	if err != nil {
		t.Fatalf("recover configured-base receipt with removed source: %v", err)
	}
	if !recovered.ReceiptRecovered || recovered.TargetOID != targetOID || recovered.PublicationOperationID != "" {
		t.Fatalf("recovered configured-base receipt = %+v, want exact empty-operation receipt", recovered)
	}
	receipt, found, err := d.latestTaskCloseIntegrationReceipt(ctx, projectID, taskID, sourceBranch)
	if err != nil || !found {
		t.Fatalf("load exact integration receipt found=%v err=%v", found, err)
	}
	if err := verifyTaskCloseIntegrationReceipt(ctx, d.git, repoDir, receipt, projectID, sourceBranch, "main"); err != nil {
		t.Fatalf("verify exact integration receipt: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := d.repairStaleRuntimeProjections(ctx, projectID, taskID); err != nil {
			t.Fatalf("repair stale projection attempt %d: %v", attempt, err)
		}
	}
	if _, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, projectID, taskID); err != nil || found {
		t.Fatalf("worktree projection after idempotent repair found=%v err=%v", found, err)
	}
}

func TestTaskCloseIntegrationRetriesRepeatedlyWhenTargetHeadMovesAfterScratchValidation(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-retry-stale-target"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Retry stale target",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/retry-stale-target"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 24)
	var scratchWorktree string
	scratchDesiredHeads := map[string]string{}
	targetHeadReads := 0
	scratchAdds := 0
	resets := make([]string, 0, 1)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case integrationTestIsWorktreeList(args):
			return integrationTestWorktreeList(worktreeListOutput, scratchWorktree, scratchDesiredHeads[scratchWorktree]), nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "branch" && args[3] == "--show-current":
			return "main", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			targetHeadReads++
			switch targetHeadReads {
			case 1:
				return "target-sha-1", nil
			case 2, 3:
				return "target-sha-2", nil
			default:
				return "target-sha-3", nil
			}
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return scratchDesiredHeads[scratchWorktree], nil
		case len(args) == 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(repoDir, ".git", "worktrees", filepath.Base(scratchWorktree)), nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchAdds++
			scratchWorktree = args[5]
			scratchDesiredHeads[scratchWorktree] = fmt.Sprintf("merged-sha-%d", scratchAdds)
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			resets = append(resets, args[4])
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true, false, "", "")
	if err != nil {
		t.Fatalf("integrateTaskBeforeClose error: %v", err)
	}
	if !result.Integrated {
		t.Fatalf("integration result = %+v, want integrated after retry", result)
	}
	if scratchAdds != 3 {
		t.Fatalf("scratch merge attempts = %d, want 3", scratchAdds)
	}
	if !slices.Equal(resets, []string{"merged-sha-3"}) {
		t.Fatalf("reset refs = %v, want retry final apply only", resets)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "worktree add --detach ") || !strings.Contains(joined, " target-sha-3") {
		t.Fatalf("git commands missing retry scratch from moved target:\n%s", joined)
	}
}

func TestTaskCloseExpectedBaseFenceRejectsPostCheckMovement(t *testing.T) {
	repo := t.TempDir()
	runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "add", "tracked.txt")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
	expectedBase := runDaemonTestGitOutput(t, repo, "rev-parse", "main")
	runDaemonTestGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "commit", "-q", "-am", "feature")
	runDaemonTestGit(t, repo, "checkout", "-q", "main")
	baseChecked := make(chan struct{})
	baseMoved := make(chan struct{})
	moveErr := make(chan error, 1)
	go func() {
		<-baseChecked
		output, err := exec.Command("git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "move base after publication check").CombinedOutput()
		if err != nil {
			err = fmt.Errorf("move base: %w: %s", err, strings.TrimSpace(string(output)))
		}
		moveErr <- err
		close(baseMoved)
	}()
	close(baseChecked)
	<-baseMoved
	if err := <-moveErr; err != nil {
		t.Fatal(err)
	}
	d := &Daemon{git: git.NewClient(git.NewExecRunner(repo), slog.Default())}
	_, err := d.mergeTaskBranchBeforeClose(context.Background(), "project", "issue", repo, "main", "feature", true, expectedBase)
	var stale *taskCloseExpectedBaseStaleError
	if !errors.As(err, &stale) || stale.Expected != expectedBase || stale.Actual == expectedBase {
		t.Fatalf("expected-base fence error = %#v, want typed stale after post-check movement", err)
	}
}

func TestTaskCloseExpectedBaseFenceRejectsMovementDuringCandidateValidation(t *testing.T) {
	repo := t.TempDir()
	runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "add", "tracked.txt")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
	expectedBase := runDaemonTestGitOutput(t, repo, "rev-parse", "main")
	runDaemonTestGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "commit", "-q", "-am", "feature")
	runDaemonTestGit(t, repo, "checkout", "-q", "main")
	d := &Daemon{git: git.NewClient(git.NewExecRunner(repo), slog.Default())}
	var moveOnce sync.Once
	ctx := git.WithCandidateValidationCommand(context.Background(), "true")
	ctx = git.WithCandidateValidationObserver(ctx, func(attempt git.CandidateValidationAttempt) {
		if attempt.Status == git.CandidateValidationPassed {
			moveOnce.Do(func() {
				runDaemonTestGit(t, repo, "commit", "-q", "--allow-empty", "-m", "move base during validation")
			})
		}
	})
	_, err := d.mergeTaskBranchBeforeClose(ctx, "project", "issue", repo, "main", "feature", true, expectedBase)
	var stale *taskCloseExpectedBaseStaleError
	if !errors.As(err, &stale) || stale.Expected != expectedBase || stale.Actual == expectedBase {
		t.Fatalf("merge error = %#v, want typed stale after during-validation movement", err)
	}
	if runDaemonTestGitOutput(t, repo, "rev-parse", "main") != stale.Actual {
		t.Fatalf("typed stale actual = %s, want current target", stale.Actual)
	}
	if count := runDaemonTestGitOutput(t, repo, "rev-list", "--count", "main..feature"); count != "1" {
		t.Fatalf("feature commits remaining after fenced apply = %s, want 1", count)
	}
}

func TestTaskCloseIntegrationBaseFallbackUsesProjectRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	logger := slog.Default()
	bootstrapRepo := t.TempDir()
	projectRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bootstrapRepo, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir bootstrap .azedarach: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRepo, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir project .azedarach: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRepo, ".azedarach", "config.json"), []byte(`{"publicationEvidence":{"policyVersion":"portable-v1"}}`), 0o644); err != nil {
		t.Fatalf("write project publication config: %v", err)
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{{Name: "other", Path: projectRepo}},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}
	projectID, err := appconfig.ProjectIDForRoot(projectRepo)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(project): %v", err)
	}

	issuesClient := newMigratedIssueClient(t, projectRepo, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Integrate me",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	sourceWorktree := filepath.Join(projectRepo, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/integrate"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	var scratchWorktree string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case integrationTestIsWorktreeList(args):
			return integrationTestWorktreeList(worktreeListOutput, scratchWorktree, "merged-sha"), nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == projectRepo && args[2] == "branch" && args[3] == "--show-current":
			return "main", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == projectRepo && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(projectRepo, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == projectRepo && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(projectRepo, ".git", "worktrees", filepath.Base(scratchWorktree)), nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == projectRepo && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == projectRepo && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == projectRepo && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == projectRepo && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == projectRepo && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, projectRepo, logger)
	d := &Daemon{
		cfg: Config{RepoDir: bootstrapRepo, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			protocol.NormalizeProjectID(projectID): issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			protocol.NormalizeProjectID(projectID): runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			protocol.NormalizeProjectID(projectID): manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{protocol.NormalizeProjectID(projectID): 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true, false, "", "")
	if err == nil || !strings.Contains(err.Error(), "requires an accepted publication operation before synthetic merge or apply") {
		t.Fatalf("unbound integrateTaskBeforeClose = (%+v, %v), want pre-integration publication authority failure", result, err)
	}
	if joined := strings.Join(commands, "\n"); strings.Contains(joined, "worktree add --detach") || strings.Contains(joined, "reset --hard") {
		t.Fatalf("unbound configured-base close reached synthetic merge or apply:\n%s", joined)
	}
	ctx = withTaskClosePublicationBinding(ctx, "publication-test", "claim-test")
	result, err = d.integrateTaskBeforeClose(ctx, projectID, taskID, true, false, "", "")
	if err != nil {
		t.Fatalf("integrateTaskBeforeClose error: %v", err)
	}
	if !result.Integrated {
		t.Fatalf("integration result = %+v, want integrated", result)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "-C "+bootstrapRepo+" ") {
		t.Fatalf("git commands used bootstrap repo, want project repo:\n%s", joined)
	}
	if !strings.Contains(joined, "-C "+projectRepo+" worktree add --detach ") {
		t.Fatalf("git commands missing project repo worktree add:\n%s", joined)
	}
}

func TestTaskCloseIntegrationOriginBaseSkipsLocalMergeWhenRemoteTreeMatches(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Already remote integrated",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/remote-integrated"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 8)
	fetchedOrigin := false
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "fetch" && args[3] == "origin":
			fetchedOrigin = true
			return "", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == repoDir && args[2] == "diff" && args[3] == "--name-only" && args[4] == "-z" && args[5] == "origin/preview" && args[6] == sourceBranch:
			if !fetchedOrigin {
				t.Fatal("origin close diff ran before fetching origin")
			}
			return "", nil
		case slices.Contains(args, "merge") || slices.Contains(args, "checkout") || slices.Contains(args, "reset"):
			t.Fatalf("origin close attempted local mutation command: %s", strings.Join(args, " "))
		default:
			return "", nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:         repoDir,
			BaseBranch:      "preview",
			GitWorkflowMode: "origin",
			Logger:          logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true, false, "", "")
	if err != nil {
		t.Fatalf("integrateTaskBeforeClose error: %v", err)
	}
	if !result.NoChanges || result.Integrated {
		t.Fatalf("integration result = %+v, want origin-base no-changes without local integration", result)
	}
	if result.TargetBranch != "origin/preview" {
		t.Fatalf("target branch = %q, want origin/preview", result.TargetBranch)
	}
	if !fetchedOrigin {
		t.Fatal("origin close did not fetch origin before integration evidence")
	}
	joined := strings.Join(commands, "\n")
	for _, forbidden := range []string{" checkout ", " merge ", " reset ", " worktree add "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("origin close attempted local mutation %q:\n%s", forbidden, joined)
		}
	}
}

func TestTaskCloseIntegrationOriginBaseAllowsRemoteAheadWhenSourceContained(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Remote ahead after integration",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/remote-contained"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 8)
	fetchedOrigin := false
	checkedContainment := false
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "fetch" && args[3] == "origin":
			fetchedOrigin = true
			return "", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == repoDir && args[2] == "diff" && args[3] == "--name-only" && args[4] == "-z" && args[5] == "origin/preview" && args[6] == sourceBranch:
			if !fetchedOrigin {
				t.Fatal("origin close diff ran before fetching origin")
			}
			return "docs/alchemy-infra.md\x00ops/infra/src/stacks/chefy-fly.test.ts\x00ops/infra/src/stacks/fly-configs.ts\x00", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "merge-base" && args[3] == "--is-ancestor" && args[4] == sourceBranch && args[5] == "origin/preview":
			checkedContainment = true
			return "", nil
		case slices.Contains(args, "merge") || slices.Contains(args, "checkout") || slices.Contains(args, "reset"):
			t.Fatalf("origin close attempted local mutation command: %s", strings.Join(args, " "))
		default:
			return "", fmt.Errorf("unexpected git command: %s", strings.Join(args, " "))
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:         repoDir,
			BaseBranch:      "preview",
			GitWorkflowMode: "origin",
			Logger:          logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true, false, "", "")
	if err != nil {
		t.Fatalf("integrateTaskBeforeClose error: %v", err)
	}
	if !result.NoChanges || result.Integrated {
		t.Fatalf("integration result = %+v, want origin-base no-changes when source is contained", result)
	}
	if result.TargetBranch != "origin/preview" {
		t.Fatalf("target branch = %q, want origin/preview", result.TargetBranch)
	}
	if !checkedContainment {
		t.Fatal("origin close did not check source containment after tree diff")
	}
	joined := strings.Join(commands, "\n")
	for _, forbidden := range []string{" checkout ", " merge ", " reset ", " worktree add "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("origin close attempted local mutation %q:\n%s", forbidden, joined)
		}
	}
}

func TestTaskCloseIntegrationOriginBaseRetryUsesExactReceiptAfterSourceRemoval(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-origin-retry"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Retry removed origin worktree", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/origin-retry"
	receipt := taskCloseIntegrationReceipt{
		ProjectID: projectID, SourceBranch: sourceBranch, TargetBranch: "origin/preview", TargetID: "base", ConfiguredBaseTarget: true, SourceOID: "source-oid", TargetOID: "target-oid",
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close",
		Payload: map[string]any{"project_id": receipt.ProjectID, "source_branch": receipt.SourceBranch, "target_branch": receipt.TargetBranch, "target_id": receipt.TargetID, "configured_base_target": receipt.ConfiguredBaseTarget, "source_oid": receipt.SourceOID, "target_oid": receipt.TargetOID},
	}); err != nil {
		t.Fatalf("seed integration receipt: %v", err)
	}

	fetched := false
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "fetch" && args[3] == "origin":
			fetched = true
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == sourceBranch+"^{commit}":
			return "", fmt.Errorf("unknown revision")
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "merge-base" && args[3] == "--is-ancestor" && (args[4] == receipt.SourceOID || args[4] == receipt.TargetOID) && args[5] == receipt.TargetBranch:
			return "", nil
		case len(args) >= 3 && args[0] == "-C" && args[2] == "status":
			t.Fatalf("origin retry read removed source status: %s", strings.Join(args, " "))
		default:
			return "", fmt.Errorf("unexpected git command: %s", strings.Join(args, " "))
		}
		return "", nil
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "preview", GitWorkflowMode: "origin", Logger: logger},
		git: git.NewClient(runner, logger), issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
	}
	result, err := d.integrateTaskBeforeCloseOriginBase(ctx, projectID, taskID, git.Worktree{
		IssueID: taskID, Path: filepath.Join(repoDir, "removed-worktree"), Branch: sourceBranch,
	}, repoDir, "preview", true, "", "")
	if err != nil {
		t.Fatalf("origin retry exact receipt error: %v", err)
	}
	if !fetched || !result.NoChanges || result.SourceOID != receipt.SourceOID || result.TargetOID != receipt.TargetOID {
		t.Fatalf("origin retry result = %+v fetched=%v, want exact receipt no-op", result, fetched)
	}
}

func TestTaskCloseIntegrationOriginBaseRefusesLocalMergeWhenRemoteDiffRemains(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Not remote integrated",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/not-remote-integrated"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 8)
	fetchedOrigin := false
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "fetch" && args[3] == "origin":
			fetchedOrigin = true
			return "", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == repoDir && args[2] == "diff" && args[3] == "--name-only" && args[4] == "-z" && args[5] == "origin/preview" && args[6] == sourceBranch:
			if !fetchedOrigin {
				t.Fatal("origin close diff ran before fetching origin")
			}
			return "main.go\x00", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "merge-base" && args[3] == "--is-ancestor" && args[4] == sourceBranch && args[5] == "origin/preview":
			return "", fmt.Errorf("exit status 1")
		case slices.Contains(args, "merge") || slices.Contains(args, "checkout") || slices.Contains(args, "reset"):
			t.Fatalf("origin close attempted local mutation command: %s", strings.Join(args, " "))
		default:
			return "", fmt.Errorf("unexpected git command: %s", strings.Join(args, " "))
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:         repoDir,
			BaseBranch:      "preview",
			GitWorkflowMode: "origin",
			Logger:          logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true, false, "", "")
	if err == nil {
		t.Fatalf("integrateTaskBeforeClose error = nil, result = %+v; want origin-mode refusal", result)
	}
	if !strings.Contains(err.Error(), "origin workflow close will not merge") || !strings.Contains(err.Error(), "main.go") {
		t.Fatalf("integrateTaskBeforeClose error = %v, want origin-mode recovery with changed file", err)
	}
	for _, want := range []string{"git push -u origin HEAD", "az pr create --issue " + taskID, "az pr status --issue " + taskID, "az pr merge --issue " + taskID + " --confirm", "fetch origin/preview", "az ticket close --id " + taskID} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("origin close guidance %q missing %q", err, want)
		}
	}
	if !fetchedOrigin {
		t.Fatal("origin close did not fetch origin before integration evidence")
	}
	joined := strings.Join(commands, "\n")
	for _, forbidden := range []string{" checkout ", " merge ", " reset ", " worktree add "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("origin close attempted local mutation %q:\n%s", forbidden, joined)
		}
	}
}

func TestDaemonRemoteTrackingBaseRef(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain branch", in: "preview", want: "origin/preview"},
		{name: "branch with slash", in: "release/2026-07", want: "origin/release/2026-07"},
		{name: "origin ref", in: "origin/preview", want: "origin/preview"},
		{name: "full remote ref", in: "refs/remotes/origin/preview", want: "refs/remotes/origin/preview"},
		{name: "empty defaults main", in: "", want: "origin/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := daemonRemoteTrackingBaseRef(tt.in); got != tt.want {
				t.Fatalf("daemonRemoteTrackingBaseRef(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTaskCloseCommandKeepsTargetCleanWhenScratchMergeDirties(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-post-merge-dirty"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Dirty post-merge target",
		Type:     domain.TypeBug,
		Priority: domain.P1,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/dirty-post-merge"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 16)
	var scratchWorktree string
	scratchStatusReads := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-parse" && args[3] == "--verify" && strings.HasSuffix(args[4], "^{commit}"):
			return "test-oid-" + strings.TrimSuffix(args[4], "^{commit}"), nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "status":
			return "", nil
		case len(args) == 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "branch" && args[3] == "--show-current":
			return "main", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "status":
			scratchStatusReads++
			if scratchStatusReads >= 2 {
				return "A  hook-created.txt\n", nil
			}
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "-q" && args[4] == "--verify":
			return "", fmt.Errorf("no merge head")
		case len(args) == 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--git-dir":
			return filepath.Join(repoDir, ".git", "worktrees", filepath.Base(scratchWorktree)), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "fetch":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "Merge made by the 'ort' strategy.", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "restore":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "clean":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate-post-merge-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskClose response = %+v, want integration failure", resp)
	}
	if !strings.Contains(resp.Error.Message, "post-merge hooks") || !strings.Contains(resp.Error.Message, "hook-created.txt") {
		t.Fatalf("handleTaskClose error = %q, want post-merge dirty detail", resp.Error.Message)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after failed close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusInReview)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"-C " + repoDir + " worktree add --detach ",
		"merge --no-edit " + sourceBranch,
		"restore --staged --worktree .",
		"clean -fd",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-C "+repoDir+" reset --hard") {
		t.Fatalf("target should not be reset after scratch merge failure:\n%s", joined)
	}
}

func TestTaskCloseCommandSkipsIntegrationWhenSourceHasNoChangesEvenIfTargetDirty(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-no-changes"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close no-op branch",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/no-changes"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "status":
			return " M README.md\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "HEAD..main":
			return "0", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "main..HEAD":
			return "2", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	beforeCloseTasks, err := issuesClient.ListWithRuntime(ctx, projectID)
	if err != nil {
		t.Fatalf("list tasks before close: %v", err)
	}
	var beforeClose domain.Task
	for _, task := range beforeCloseTasks {
		if task.ID.String() == taskID {
			beforeClose = task
			break
		}
	}
	if beforeClose.ID.IsZero() {
		t.Fatalf("task %s missing before close", taskID)
	}
	if !beforeClose.HasWorktree {
		t.Fatalf("task before close HasWorktree = false, want seeded runtime projection; task = %+v", beforeClose)
	}

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate-no-changes",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated {
		t.Fatalf("close integration result = %+v, want requested no-op integration", result)
	}
	if !result.WorktreeRemoved {
		t.Fatalf("close integration result = %+v, want worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	joined := strings.Join(commands, "\n")
	for _, blocked := range []string{
		"-C " + repoDir + " checkout main",
		"-C " + repoDir + " merge --no-edit " + sourceBranch,
		"-C " + repoDir + " merge-tree --write-tree main " + sourceBranch,
	} {
		if strings.Contains(joined, blocked) {
			t.Fatalf("git commands should skip no-op integration but included %q:\n%s", blocked, joined)
		}
	}
	for _, want := range []string{
		"-C " + sourceWorktree + " rev-list --count main..HEAD",
		"worktree remove " + sourceWorktree,
		"branch -D " + sourceBranch,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
}

func TestTaskCloseNoChangesIntegrationResultCarriesRecoveredCanonicalValidation(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repo, "config", "user.name", "Test User")
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	gatePath := filepath.Join(repo, "scripts", "git-merge-rebase-gate.sh")
	if err := os.WriteFile(gatePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runDaemonTestGit(t, repo, "add", ".")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
	runDaemonTestGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	runDaemonTestGit(t, repo, "add", "feature.txt")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "feature")
	sourceOID := runDaemonTestGitOutput(t, repo, "rev-parse", "HEAD")
	runDaemonTestGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}
	runDaemonTestGit(t, repo, "add", "main.txt")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "main")

	client := git.NewClient(git.NewExecRunner(repo), slog.Default())
	merge, err := client.MergeCleanlyTransactional(ctx, repo, "feature")
	if err != nil || merge == nil || !merge.Success {
		t.Fatalf("MergeCleanlyTransactional() = (%+v, %v), want success", merge, err)
	}
	targetOID := runDaemonTestGitOutput(t, repo, "rev-parse", "HEAD")
	d := &Daemon{git: client}
	result, err := d.taskCloseNoChangesIntegrationResult(ctx, repo, "base", "feature", "main", sourceOID, targetOID, true)
	if err != nil {
		t.Fatalf("taskCloseNoChangesIntegrationResult() error = %v", err)
	}
	if !result.NoChanges || len(result.ValidationAttempts) != 1 {
		t.Fatalf("no-change result = %+v, want one durable validation attempt", result)
	}
	attempt := result.ValidationAttempts[0]
	if attempt.CandidateHead != targetOID || attempt.Status != domain.IntegrationCandidateValidationPassed || !attempt.Canonical {
		t.Fatalf("validation attempt = %+v, want canonical exact target %s", attempt, targetOID)
	}
}

func TestTaskCloseIntegrationReceiptRequiresExactFreshTypedTarget(t *testing.T) {
	tests := []struct {
		name                  string
		receipt               taskCloseIntegrationReceipt
		currentProject        string
		currentTargetID       string
		currentTargetBranch   string
		currentConfiguredBase bool
		wantError             string
	}{
		{
			name:                "matching non-base target",
			receipt:             taskCloseIntegrationReceipt{ProjectID: "project", TargetID: "parent-a", TargetBranch: "shared", ConfiguredBaseTarget: false},
			currentProject:      "project",
			currentTargetID:     "parent-a",
			currentTargetBranch: "shared",
		},
		{
			name:                "same branch different non-base target",
			receipt:             taskCloseIntegrationReceipt{ProjectID: "project", TargetID: "parent-a", TargetBranch: "shared", ConfiguredBaseTarget: false},
			currentProject:      "project",
			currentTargetID:     "parent-b",
			currentTargetBranch: "shared",
			wantError:           "target identity changed",
		},
		{
			name:                "base retargeted to non-base with reused branch",
			receipt:             taskCloseIntegrationReceipt{ProjectID: "project", TargetID: "base", TargetBranch: "shared", ConfiguredBaseTarget: true},
			currentProject:      "project",
			currentTargetID:     "parent-a",
			currentTargetBranch: "shared",
			wantError:           "target identity changed",
		},
		{
			name:                  "non-base retargeted to base with reused branch",
			receipt:               taskCloseIntegrationReceipt{ProjectID: "project", TargetID: "parent-a", TargetBranch: "shared", ConfiguredBaseTarget: false},
			currentProject:        "project",
			currentTargetID:       "base",
			currentTargetBranch:   "shared",
			currentConfiguredBase: true,
			wantError:             "target identity changed",
		},
		{
			name:                  "legacy receipt missing target identity",
			receipt:               taskCloseIntegrationReceipt{ProjectID: "project", TargetBranch: "shared", ConfiguredBaseTarget: true},
			currentProject:        "project",
			currentTargetID:       "base",
			currentTargetBranch:   "shared",
			currentConfiguredBase: true,
			wantError:             "missing authoritative typed target identity",
		},
		{
			name:                  "configured base branch retargeted",
			receipt:               taskCloseIntegrationReceipt{ProjectID: "project", TargetID: "base", TargetBranch: "main", ConfiguredBaseTarget: true},
			currentProject:        "project",
			currentTargetID:       "base",
			currentTargetBranch:   "release",
			currentConfiguredBase: true,
			wantError:             "target branch changed",
		},
		{
			name:                  "receipt project retargeted",
			receipt:               taskCloseIntegrationReceipt{ProjectID: "project-a", TargetID: "base", TargetBranch: "main", ConfiguredBaseTarget: true},
			currentProject:        "project-b",
			currentTargetID:       "base",
			currentTargetBranch:   "main",
			currentConfiguredBase: true,
			wantError:             "project identity changed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTaskCloseIntegrationReceiptIdentity(tt.receipt, tt.currentProject, tt.currentTargetID, tt.currentTargetBranch, tt.currentConfiguredBase)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateTaskCloseIntegrationReceiptIdentity() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateTaskCloseIntegrationReceiptIdentity() error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestTaskCloseRejectsConfiguredBaseBranchRetargetBeforeNoChangeFallback(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	runDaemonTestGit(t, repoDir, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repoDir, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repoDir, "add", "base.txt")
	runDaemonTestGit(t, repoDir, "commit", "-q", "-m", "base")

	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	projectID := "proj-configured-base-retarget"
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Retargeted configured base", Type: domain.TypeBug, Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceBranch := "riordan/" + taskID + "/retarget-source"
	sourceWorktree := filepath.Join(t.TempDir(), "source")
	runDaemonTestGit(t, repoDir, "worktree", "add", "-q", "-b", sourceBranch, sourceWorktree)
	if err := os.WriteFile(filepath.Join(sourceWorktree, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, sourceWorktree, "add", "feature.txt")
	runDaemonTestGit(t, sourceWorktree, "commit", "-q", "-m", "feature")
	sourceOID := runDaemonTestGitOutput(t, sourceWorktree, "rev-parse", "HEAD")
	runDaemonTestGit(t, repoDir, "merge", "-q", "--ff-only", sourceBranch)
	mainOID := runDaemonTestGitOutput(t, repoDir, "rev-parse", "main")
	runDaemonTestGit(t, repoDir, "branch", "release", mainOID)
	runDaemonTestGit(t, repoDir, "checkout", "-q", "release")

	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID, IssueID: taskID, Path: sourceWorktree, Branch: sourceBranch, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": sourceBranch, "target_branch": "main",
			"target_id": "base", "configured_base_target": true, "integrated": true,
			"base_oid": mainOID, "source_oid": sourceOID, "target_oid": mainOID,
			"publication_operation_id": "publication-main",
		},
	}); err != nil {
		t.Fatal(err)
	}

	runner := git.NewExecRunner(repoDir)
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "release", Logger: logger}, git: git.NewClient(runner, logger),
		issueClientsByProject:     map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject:           func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore { return runtimeStore },
		logger:                      logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true, false, "", "")
	if err == nil || !strings.Contains(err.Error(), "target branch changed: recorded=main current=release") {
		t.Fatalf("integrateTaskBeforeClose() = (%+v, %v), want configured-base branch retarget rejection", result, err)
	}
	events, listErr := issuesClient.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10,
	})
	if listErr != nil || len(events) != 1 {
		t.Fatalf("integration receipts after rejection = %+v, err=%v; want only original main receipt", events, listErr)
	}
}

func TestTaskCloseRemovedWorktreeRejectsStaleReceiptAfterSameBranchAncestorRetarget(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-retarget"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentA, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Original ancestor", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create original ancestor: %v", err)
	}
	parentB, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Current ancestor", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create current ancestor: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Retargeted child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentB})
	if err != nil {
		t.Fatalf("create retargeted child: %v", err)
	}
	sharedBranch := "riordan/" + parentB + "/shared-parent-branch"
	childBranch := "riordan/" + childID + "/retargeted"
	missingChildWorktree := filepath.Join(t.TempDir(), "removed-child")
	currentParentWorktree := filepath.Join(t.TempDir(), "parent-b")
	for _, projection := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: childID, Path: missingChildWorktree, Branch: childBranch, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: parentB, Path: currentParentWorktree, Branch: sharedBranch, UpdatedAt: time.Now().UTC()},
	} {
		if err := runtimeStore.UpsertWorktreeState(ctx, projection); err != nil {
			t.Fatalf("seed worktree projection for %s: %v", projection.IssueID, err)
		}
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": childBranch, "target_branch": sharedBranch,
			"target_id": parentA, "configured_base_target": false, "integrated": true,
			"base_oid": "parent-a-before", "source_oid": "child-source", "target_oid": "parent-a-result",
		},
	}); err != nil {
		t.Fatalf("seed stale ancestor integration receipt: %v", err)
	}

	var ancestryReuseReached atomic.Bool
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if slices.Contains(args, "merge-base") {
			ancestryReuseReached.Store(true)
		}
		switch {
		case len(args) >= 5 && args[0] == "-C" && args[1] == currentParentWorktree && args[2] == "merge-base" && args[3] == "--is-ancestor":
			return "", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return fmt.Sprintf("worktree %s\nHEAD parent-b-head\nbranch refs/heads/%s\n\n", currentParentWorktree, sharedBranch), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == currentParentWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == childBranch+"^{commit}":
			return "", fmt.Errorf("unknown revision")
		case len(args) >= 5 && args[0] == "-C" && args[1] == currentParentWorktree && args[2] == "rev-list":
			return "", fmt.Errorf("unknown revision")
		default:
			return "", fmt.Errorf("unexpected git command: %s", strings.Join(args, " "))
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg:                       Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		git:                       git.NewClient(runner, logger),
		issueClientsByProject:     map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject:           func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore { return runtimeStore },
		logger:                      logger,
		pollInterval:                time.Hour,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, childID, true, true, "", "")
	if err == nil || !strings.Contains(err.Error(), "target identity changed: recorded="+parentA+" current="+parentB) {
		t.Fatalf("integrateTaskBeforeClose() = (%+v, %v), want stale same-branch target identity rejection", result, err)
	}
	if ancestryReuseReached.Load() {
		t.Fatal("stale receipt reached ancestry reuse before typed target rejection")
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": childBranch, "target_branch": sharedBranch,
			"target_id": parentB, "configured_base_target": false, "integrated": true,
			"base_oid": "parent-b-before", "source_oid": "child-source", "target_oid": "parent-b-result",
			"publication_operation_id": "publication-parent-b",
		},
	}); err != nil {
		t.Fatalf("seed current ancestor integration receipt: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID, IssueID: childID, Path: missingChildWorktree, Branch: childBranch, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("restore removed child projection for receipt retry: %v", err)
	}
	recovered, err := d.integrateTaskBeforeClose(ctx, projectID, childID, true, true, "", "")
	if err != nil {
		t.Fatalf("integrateTaskBeforeClose() current receipt error = %v", err)
	}
	if !recovered.ReceiptRecovered || recovered.PublicationOperationID != "publication-parent-b" {
		t.Fatalf("recovered integration = %+v, want exact publication operation identity", recovered)
	}
}

func TestFailedCandidateValidationAttemptSelectsLatestTypedFailure(t *testing.T) {
	attempt, ok := failedCandidateValidationAttempt([]domain.IntegrationCandidateValidationAttempt{
		{CandidateHead: "old", Status: domain.IntegrationCandidateValidationFailed, Message: "old failure"},
		{CandidateHead: "cancelled", Status: domain.IntegrationCandidateValidationCancelled},
		{CandidateHead: "exact", Status: domain.IntegrationCandidateValidationFailed, Message: "actionable failure"},
	})
	if !ok || attempt.CandidateHead != "exact" || attempt.Message != "actionable failure" {
		t.Fatalf("failedCandidateValidationAttempt() = (%+v, %t), want latest typed failure", attempt, ok)
	}
	message, ok := candidateValidationFailureMessage([]domain.IntegrationCandidateValidationAttempt{attempt})
	if !ok || !strings.Contains(message, "candidate validation for revision exact failed") || !strings.Contains(message, "actionable failure") || strings.Contains(message, "merge failed") {
		t.Fatalf("candidateValidationFailureMessage() = (%q, %t), want typed actionable classification", message, ok)
	}
}

func TestTaskCloseCommandDirtyChildTargetNamesPathsAndRecovery(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-child-dirty-target"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent integration target",
		Type:     domain.TypeFeature,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child ready to close",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child task: %v", err)
	}

	parentWorktree := filepath.Join(repoDir, "wt-"+parentID)
	childWorktree := filepath.Join(repoDir, "wt-"+childID)
	parentBranch := "riordan/" + parentID + "/parent"
	childBranch := "riordan/" + childID + "/child"
	for _, state := range []daemonstate.WorktreeState{
		{
			ProjectID: projectID,
			IssueID:   parentID,
			Path:      parentWorktree,
			Branch:    parentBranch,
			UpdatedAt: time.Now().UTC(),
		},
		{
			ProjectID: projectID,
			IssueID:   childID,
			Path:      childWorktree,
			Branch:    childBranch,
			UpdatedAt: time.Now().UTC(),
		},
	} {
		if err := runtimeStore.UpsertWorktreeState(ctx, state); err != nil {
			t.Fatalf("seed worktree projection: %v", err)
		}
	}

	worktreeListOutput := strings.Join([]string{
		fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n", parentWorktree, parentBranch),
		fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n", childWorktree, childBranch),
	}, "\n")
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == parentWorktree && args[2] == "rev-list" && args[4] == parentBranch+".."+childBranch:
			return "1", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == childWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == parentWorktree && args[2] == "status":
			return "A  staged-target.go\n M modified-target.go\n?? scratch-target.log\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktree && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tchild-change.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktree && args[2] == "diff":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               childID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-child-dirty-target",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskClose response = %+v, want dirty target failure", resp)
	}
	message := resp.Error.Message
	for _, want := range []string{
		"target branch worktree is not clean",
		"1 staged, 1 modified, 1 untracked",
		"staged: staged-target.go",
		"modified: modified-target.go",
		"untracked: scratch-target.log",
		"target-side dirt, not source worker dirt",
		"child " + childID + " evidence may still be valid",
		"git -C " + fmt.Sprintf("%q", parentWorktree) + " status --short",
		"stash push -u -m \"az issue close target drift before " + childID + "\"",
		"commit it intentionally",
		"abort and leave the child open/in_review",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("handleTaskClose error = %q, missing %q", message, want)
		}
	}
	if strings.Contains(strings.Join(commands, "\n"), "merge --no-edit") {
		t.Fatalf("dirty target preflight should block before merge, commands:\n%s", strings.Join(commands, "\n"))
	}
}

func TestGitStatusSummaryWithDetailsBoundsPathOutput(t *testing.T) {
	modified := make([]string, 0, gitStatusDetailPathLimit+2)
	for i := 0; i < gitStatusDetailPathLimit+2; i++ {
		modified = append(modified, fmt.Sprintf("file-%02d.go", i))
	}

	got := gitStatusSummaryWithDetails(&git.GitStatus{
		HasChanges: true,
		Modified:   modified,
	})

	if !strings.Contains(got, fmt.Sprintf("%d modified", gitStatusDetailPathLimit+2)) {
		t.Fatalf("summary = %q, want total modified count", got)
	}
	if !strings.Contains(got, "file-11.go") {
		t.Fatalf("summary = %q, want last in-bounds path", got)
	}
	if strings.Contains(got, "file-12.go") || strings.Contains(got, "file-13.go") {
		t.Fatalf("summary = %q, want paths beyond bounded output omitted", got)
	}
	if !strings.Contains(got, "2 more omitted") {
		t.Fatalf("summary = %q, want omitted count", got)
	}
}

func TestTaskCloseCommandSkipsIntegrationWhenSourceAlreadyReachableFromTarget(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-already-reachable"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close already integrated branch",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{
		OwnerID:   "worker-a",
		OwnerKind: "agent",
		Purpose:   domain.CoordinationLeaseExecution,
	}); err != nil {
		t.Fatalf("claim execution lease: %v", err)
	}
	evidenceEvent, err := issuesClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
		Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t),
	})
	if err != nil {
		t.Fatalf("append reviewed evidence: %v", err)
	}
	evidence := domain.ReduceReviewReadyEvidence([]domain.IssueObservationEvent{evidenceEvent}).LatestEvidence
	if evidence == nil || !evidence.Validation.Complete {
		t.Fatalf("reviewed evidence = %+v, want complete", evidence)
	}
	evidenceBody, err := json.Marshal(evidence.Evidence)
	if err != nil {
		t.Fatalf("marshal reviewed evidence: %v", err)
	}
	evidencePin := &issues.ReviewEvidencePin{Source: "issue_event", EventID: evidenceEvent.ID, Digest: fmt.Sprintf("%x", sha256.Sum256(evidenceBody))}
	admission, err := issuesClient.CaptureReviewAdmissionPin(ctx, taskID)
	if err != nil {
		t.Fatalf("capture review admission: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{
		OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview, ExpectedReviewAdmission: &admission,
	}); err != nil {
		t.Fatalf("claim reviewer lease: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
		Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
			"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator,
			"reviewed_evidence_source": evidencePin.Source, "reviewed_evidence_event_id": evidencePin.EventID,
			"reviewed_evidence_seq": evidencePin.Seq, "reviewed_evidence_digest": evidencePin.Digest,
		},
	}); err != nil {
		t.Fatalf("record accepted review: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/already-integrated"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	var lateEvidenceErr error
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		if lateEvidenceErr == nil && len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" {
			mutated := mustWorkerEvidencePayload(t)
			mutated["summary"] = "late evidence from git runner hook"
			_, lateEvidenceErr = issuesClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
				Type: domain.IssueEventEvidenceSubmitted, Source: "late-worker", Payload: mutated,
			})
		}
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			return "0", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "status":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "merge-base":
			return "stale-base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\talready-integrated.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "HEAD..main":
			return "3", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "main..HEAD":
			return "2", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID: taskID, IntegrateBeforeClose: true, ExpectedReviewEvidence: evidencePin, ExpectedReviewerID: "reviewer",
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate-already-reachable",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated {
		t.Fatalf("close integration result = %+v, want requested no-op integration", result)
	}
	if lateEvidenceErr == nil || !strings.Contains(lateEvidenceErr.Error(), "active review admission lease fences evidence replacement") {
		t.Fatalf("late git-runner evidence error = %v, want reviewer-owned durable close-fence conflict", lateEvidenceErr)
	}
	if !result.WorktreeRemoved {
		t.Fatalf("close integration result = %+v, want worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	if closed.Ownership != nil || len(closed.CoordinationLeases) != 0 {
		t.Fatalf("closed task leases = %+v ownership = %+v, want already-contained close to release execution lease", closed.CoordinationLeases, closed.Ownership)
	}
	joined := strings.Join(commands, "\n")
	for _, blocked := range []string{
		"-C " + repoDir + " merge-tree --write-tree main " + sourceBranch,
		"merge --no-edit " + sourceBranch,
		"-C " + sourceWorktree + " diff --name-status",
	} {
		if strings.Contains(joined, blocked) {
			t.Fatalf("git commands should skip already-integrated source but included %q:\n%s", blocked, joined)
		}
	}
	for _, want := range []string{
		"-C " + repoDir + " rev-list --count main.." + sourceBranch,
		"worktree remove " + sourceWorktree,
		"branch -D " + sourceBranch,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
}

func TestVerifyExternalTaskIntegrationRequiresAncestryAndCandidateTreeIdentity(t *testing.T) {
	operation := domain.PublicationOperation{
		OperationID: "publication", TargetID: "base", TargetBranch: "main", BaseRevision: "base-oid",
		SourceRevision: "source-oid", CandidateRevision: "candidate-oid",
	}
	tests := []struct {
		name            string
		integrationTree string
		failAncestry    string
		wantErr         string
	}{
		{name: "success", integrationTree: "tree-exact"},
		{name: "candidate tree mismatch", integrationTree: "tree-other", wantErr: "does not match accepted candidate tree"},
		{name: "reviewed source missing", integrationTree: "tree-exact", failAncestry: "source-oid", wantErr: "reviewed source source-oid is not reachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
				joined := strings.Join(args, " ")
				switch {
				case strings.Contains(joined, "rev-parse --verify integrated^{commit}"):
					return "integrated-oid", nil
				case strings.Contains(joined, "rev-parse --verify main^{commit}"):
					return "main-oid", nil
				case strings.Contains(joined, "merge-base --is-ancestor"):
					if tt.failAncestry != "" && strings.Contains(joined, tt.failAncestry) {
						return "", fmt.Errorf("exit status 1")
					}
					return "", nil
				case strings.Contains(joined, "rev-parse --verify candidate-oid^{tree}"):
					return "tree-exact", nil
				case strings.Contains(joined, "rev-parse --verify integrated-oid^{tree}"):
					return tt.integrationTree, nil
				default:
					return "", fmt.Errorf("unexpected git args: %s", joined)
				}
			}}
			d := &Daemon{git: git.NewClient(runner, slog.Default())}
			got, err := d.verifyExternalTaskIntegration(context.Background(), t.TempDir(), "issue-1", "integrated", operation)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("verify error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("verify external integration: %v", err)
			}
			if !got.ExternalReconciled || got.TargetOID != "integrated-oid" || got.SourceOID != operation.SourceRevision || got.PublicationOperationID != operation.OperationID {
				t.Fatalf("integration = %+v, want exact reconciled identity", got)
			}
		})
	}
}

func TestTaskCloseCommandReplaysCompletedExternalIntegrationAndRepublishesDurableState(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.Default()
	client := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = client.CloseDB() })
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, logger: logger})
	t.Cleanup(func() { _ = runtime.Close() })
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	projectID = protocol.NormalizeProjectID(projectID)
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	admission, err := client.CaptureReviewAdmissionPin(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview, ExpectedReviewAdmission: &admission, ReviewSourceOID: "source-oid"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	operation := domain.PublicationOperation{
		OperationID: "publication-replayed", ProjectID: projectID, IssueID: issueID, IntentKey: "accepted-external", RequestFingerprint: "fingerprint",
		ActorID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator, ReviewEpochEventID: admission.ReviewEpochEventID,
		PatchEvidenceID: "publication-replayed", AcceptedPublicationOperationID: "publication-replayed", TargetID: "base", TargetBranch: "main",
		SourceRevision: "source-oid", BaseRevision: "base-oid", CandidateRevision: "candidate-oid", PolicyVersion: "policy", EnvironmentFingerprint: "test", ValidationCommand: "just test", CreatedAt: now,
	}
	patchEvidence := domain.PublicationEvidence{EvidenceID: operation.PatchEvidenceID, ProjectID: projectID, IssueID: issueID, Layer: domain.PublicationEvidencePatchReview, PatchDigest: "patch", SourceRevision: operation.SourceRevision, BaseRevision: operation.BaseRevision, Producer: "reviewer:reviewer", PolicyVersion: operation.PolicyVersion, EnvironmentFingerprint: operation.EnvironmentFingerprint, CreatedAt: now}
	receipt, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
	}}, operation, patchEvidence, "accepted-external", admission, "", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	operation.AcceptedReviewEventID = receipt.EventID
	storedRoot, found, err := runtime.store.PublicationOperation(ctx, operation.OperationID)
	if err != nil || !found {
		t.Fatalf("accepted publication root found=%t err=%v", found, err)
	}
	if storedRoot.ProjectID != projectID || !naming.IssueIDsEqual(storedRoot.IssueID, issueID) || storedRoot.AcceptedReviewEventID != receipt.EventID {
		t.Fatalf("accepted publication root identity = %+v receipt=%+v project=%s issue=%s", storedRoot, receipt, projectID, issueID)
	}
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "rev-parse --verify integrated^{commit}"):
			return "integrated-oid", nil
		case strings.Contains(joined, "rev-parse --verify main^{commit}"):
			return "main-oid", nil
		case strings.Contains(joined, "merge-base --is-ancestor"):
			return "", nil
		case strings.Contains(joined, "rev-parse --verify candidate-oid^{tree}"), strings.Contains(joined, "rev-parse --verify integrated-oid^{tree}"):
			return "tree-exact", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	published := 0
	d := &Daemon{cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger}, git: git.NewClient(runner, logger), operationRuntime: runtime, hub: publish.NewHub(16, 8, logger), revision: map[string]uint64{},
		issueClientsByProject: map[string]*issues.Client{projectID: client}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore}, publicationStateChanged: func(got domain.PublicationOperation) {
			published++
			if got.OperationID != operation.OperationID || got.State != domain.PublicationOperationMerged {
				t.Fatalf("published operation = %+v", got)
			}
		}}
	body, err := json.Marshal(taskCloseRequest{TaskID: issueID, ExternalIntegratedRevision: "integrated"})
	if err != nil {
		t.Fatal(err)
	}
	var replayEvents <-chan protocol.EventEnvelope
	var cancelReplayEvents func()
	for attempt := 1; attempt <= 2; attempt++ {
		resp, closeErr := d.handleTaskClose(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID(fmt.Sprintf("external-close-%d", attempt)), Kind: protocol.EnvelopeKindCommand, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Command: "task.close", Body: body})
		if closeErr != nil || !resp.OK {
			message := ""
			if resp.Error != nil {
				message = resp.Error.Message
			}
			t.Fatalf("close %d: response=%+v message=%q err=%v", attempt, resp, message, closeErr)
		}
		var result taskCloseResult
		if err := json.Unmarshal(resp.Body, &result); err != nil {
			t.Fatal(err)
		}
		if !result.Integrated || !result.IntegrationRequested || result.IntegratedTargetBranch != "main" || result.Revision == 0 {
			t.Fatalf("close %d result = %+v", attempt, result)
		}
		if attempt == 1 {
			replayEvents, cancelReplayEvents = d.hub.Subscribe(projectID, d.currentRevision(projectID))
			defer cancelReplayEvents()
		}
	}
	if published != 2 {
		t.Fatalf("publication convergence notifications = %d, want 2", published)
	}
	gotEvents := map[string]bool{}
	for len(gotEvents) < 2 {
		event := <-replayEvents
		gotEvents[event.Event] = true
	}
	for _, eventType := range []string{protocol.EventPublicationOperationUpdated, protocol.EventTaskUpdated} {
		if !gotEvents[eventType] {
			t.Fatalf("replay events = %+v, missing %s", gotEvents, eventType)
		}
	}
	stored, found, err := runtime.store.PublicationOperation(ctx, operation.OperationID)
	if err != nil || !found || stored.State != domain.PublicationOperationMerged {
		t.Fatalf("terminal publication = %+v found=%t err=%v", stored, found, err)
	}
	closed, err := client.GetWithRuntime(ctx, projectID, issueID)
	if err != nil || closed.Status != domain.StatusDone || len(closed.CoordinationLeases) != 0 {
		t.Fatalf("terminal issue = %+v err=%v", closed, err)
	}
}

func TestReconcileExternalTaskIntegrationRejectsStaleAcceptedRootIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.PublicationOperation)
	}{
		{name: "project", mutate: func(op *domain.PublicationOperation) { op.ProjectID = "other-project" }},
		{name: "issue", mutate: func(op *domain.PublicationOperation) { op.IssueID = "other-issue" }},
		{name: "accepted event", mutate: func(op *domain.PublicationOperation) { op.AcceptedReviewEventID = 999999 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repoDir := t.TempDir()
			client := newMigratedIssueClient(t, repoDir, slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			projectID := protocol.NormalizeProjectID(filepath.Base(repoDir))
			issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "authority mismatch", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInReview})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
				"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": "reviewer", "actor_kind": "orchestrator", "reviewer_kind": "orchestrator",
				"intent_key": "stale-root", "request_fingerprint": "fingerprint", "review_epoch_event_id": int64(1), "publication_operation_id": "stale-root",
			}})
			if err != nil {
				t.Fatal(err)
			}
			op := domain.PublicationOperation{OperationID: "stale-root", ProjectID: projectID, IssueID: issueID, IntentKey: "stale-root", RequestFingerprint: "fingerprint", ActorID: "reviewer", ReviewerKind: "orchestrator", ReviewEpochEventID: 1, AcceptedReviewEventID: accepted.ID, PatchEvidenceID: "patch", AcceptedPublicationOperationID: "stale-root", TargetID: "base", TargetBranch: "main", SourceRevision: "source", BaseRevision: "base", CandidateRevision: "candidate", PolicyVersion: "policy", EnvironmentFingerprint: "test", ValidationCommand: "just test", CreatedAt: time.Now().UTC()}
			tc.mutate(&op)
			runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
			t.Cleanup(func() { _ = runtime.Close() })
			if _, _, err := runtime.store.EnqueuePublication(ctx, op, "stale-root-"+tc.name); err != nil {
				t.Fatal(err)
			}
			d := &Daemon{cfg: Config{RepoDir: repoDir}, git: git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) { return "", fmt.Errorf("git must not run") }}, slog.Default()), operationRuntime: runtime, issueClientsByProject: map[string]*issues.Client{projectID: client}}
			if _, _, err := d.reconcileExternalTaskIntegration(ctx, projectID, issueID, "integrated"); err == nil || !strings.Contains(err.Error(), "stale project, issue, or review identity") {
				t.Fatalf("reconcile error = %v", err)
			}
		})
	}
}

func TestTaskCloseCommandForceRemovesDirtyAlreadyIntegratedWorktree(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-force-dirty-already-reachable"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Force close dirty stale worktree",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/already-integrated-dirty"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	cacheRoot := filepath.Join(repoDir, ".azedarach", "go")
	for _, kind := range []string{"normal", "race", "coverage"} {
		entry := filepath.Join(cacheRoot, "caches", "v1", kind, "issue-"+taskID, "entry")
		if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
			t.Fatalf("seed %s cache: %v", kind, err)
		}
		if err := os.WriteFile(entry, []byte("cache"), 0o644); err != nil {
			t.Fatalf("write %s cache: %v", kind, err)
		}
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	removeStarted := make(chan struct{})
	releaseRemove := make(chan struct{})
	removeDone := make(chan struct{})
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			return "0", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "?? generated.txt\n", fmt.Errorf("forced close should not inspect dirty source status")
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" && slices.Contains(args, "--force") && len(args) >= 5:
			close(removeStarted)
			<-releaseRemove
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			close(removeDone)
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		ForceWorktree:        true,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	type closeResponse struct {
		resp protocol.ResponseEnvelope
		err  error
	}
	closeDone := make(chan closeResponse, 1)
	go func() {
		resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-close-force-dirty-already-reachable",
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         "task.close",
			Body:            body,
		})
		closeDone <- closeResponse{resp: resp, err: err}
	}()
	var closeResult closeResponse
	select {
	case closeResult = <-closeDone:
	case <-time.After(500 * time.Millisecond):
		close(releaseRemove)
		t.Fatal("handleTaskClose blocked on forced physical worktree removal")
	}
	resp := closeResult.resp
	if closeResult.err != nil {
		t.Fatalf("handleTaskClose error: %v", closeResult.err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated || !result.WorktreeForced || !result.WorktreeCleanupDeferred || result.WorktreeRemoved || result.WorktreeCleanupOperationID == "" {
		t.Fatalf("close result = %+v, want requested no-op integration with forced worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone || closed.HasWorktree {
		t.Fatalf("closed task status=%s has_worktree=%v, want closed without worktree", closed.Status, closed.HasWorktree)
	}
	select {
	case <-removeStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deferred physical worktree cleanup did not start")
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "-C "+sourceWorktree+" status") {
		t.Fatalf("forced close should skip dirty source status, commands:\n%s", joined)
	}
	if !strings.Contains(joined, "worktree remove --force --force "+sourceWorktree) {
		t.Fatalf("git commands missing double-force worktree removal:\n%s", joined)
	}
	close(releaseRemove)
	select {
	case <-removeDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deferred physical worktree cleanup did not finish after release")
	}
	record := waitForRuntimeState(t, d.operationRuntime, result.WorktreeCleanupOperationID, daemonops.StateDone)
	if record.Kind != taskDeferredWorktreeCleanupOperationKind {
		t.Fatalf("cleanup operation kind = %s, want %s", record.Kind, taskDeferredWorktreeCleanupOperationKind)
	}
	for _, kind := range []string{"normal", "race", "coverage"} {
		_, err := os.Stat(filepath.Join(cacheRoot, "caches", "v1", kind, "issue-"+taskID))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s cache remains after deferred close cleanup: %v", kind, err)
		}
	}
}

type fakeDeferredCleanupOperationManager struct {
	listFn   func(context.Context, daemonops.Query) ([]daemonops.Record, error)
	getFn    func(context.Context, string) (daemonops.Record, error)
	cancelFn func(context.Context, string, string) (daemonops.Record, error)
}

func (f fakeDeferredCleanupOperationManager) List(ctx context.Context, query daemonops.Query) ([]daemonops.Record, error) {
	return f.listFn(ctx, query)
}

func (f fakeDeferredCleanupOperationManager) Get(ctx context.Context, id string) (daemonops.Record, error) {
	if f.getFn == nil {
		return daemonops.Record{}, daemonops.ErrNotFound
	}
	return f.getFn(ctx, id)
}

func (f fakeDeferredCleanupOperationManager) Cancel(ctx context.Context, id, reason string) (daemonops.Record, error) {
	if f.cancelFn == nil {
		return daemonops.Record{}, daemonops.ErrNotFound
	}
	return f.cancelFn(ctx, id, reason)
}

func TestTaskUpdateDetailsFailsClosedWhenDeferredCleanupBarrierFails(t *testing.T) {
	tests := []struct {
		name    string
		manager fakeDeferredCleanupOperationManager
		wantErr string
	}{
		{
			name: "list failure",
			manager: fakeDeferredCleanupOperationManager{listFn: func(context.Context, daemonops.Query) ([]daemonops.Record, error) {
				return nil, errors.New("injected cleanup list failure")
			}},
			wantErr: "list deferred worktree cleanup before lifecycle change: injected cleanup list failure",
		},
		{
			name: "cancel failure",
			manager: fakeDeferredCleanupOperationManager{
				listFn: func(context.Context, daemonops.Query) ([]daemonops.Record, error) {
					return []daemonops.Record{{ID: "cleanup-1", State: daemonops.StateQueued, ResourceKeys: []string{"issue:project:a", "worktree:/tmp/a", "branch:issue/a"}}}, nil
				},
				cancelFn: func(context.Context, string, string) (daemonops.Record, error) {
					return daemonops.Record{}, errors.New("injected cleanup cancel failure")
				},
			},
			wantErr: "cancel deferred worktree cleanup cleanup-1 before lifecycle change: injected cleanup cancel failure",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			projectID := "project"
			issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
				Title: "closed issue", Type: domain.TypeBug, Priority: domain.P2, Status: domain.StatusDone,
			})
			if err != nil {
				t.Fatalf("create closed issue: %v", err)
			}
			d := &Daemon{
				cfg:                             Config{Logger: slog.Default()},
				issueClientsByProject:           map[string]*issues.Client{projectID: issuesClient},
				deferredCleanupOperationManager: tt.manager,
			}
			openLifecycle := domain.IssueWorkflowOpen
			body, err := json.Marshal(map[string]any{
				"task_id": taskID, "title": "closed issue", "type": domain.TypeBug, "priority": domain.P2, "lifecycle_state": openLifecycle,
			})
			if err != nil {
				t.Fatalf("marshal update: %v", err)
			}
			resp, err := d.handleTaskUpdateDetails(ctx, protocol.RequestEnvelope{
				Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body,
			})
			if err != nil {
				t.Fatalf("handle update: %v", err)
			}
			if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, tt.wantErr) {
				t.Fatalf("response = %+v, want error containing %q", resp, tt.wantErr)
			}
			task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
			if err != nil {
				t.Fatalf("get closed issue: %v", err)
			}
			if task.Status != domain.StatusDone || task.State.Workflow() != domain.IssueWorkflowClosed {
				t.Fatalf("issue status=%s workflow=%s, want closed", task.Status, task.State.Workflow())
			}
		})
	}
}

func TestTaskCloseDeferredWorktreeCleanupCancelledWhenIssueReopens(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-deferred-reopen"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Reopen before deferred cleanup",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/already-integrated-dirty"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			return "0", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			return "", fmt.Errorf("cleanup should remain queued until reopened")
		default:
			return "", fmt.Errorf("unexpected git args: %s", strings.Join(args, " "))
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	blocker, err := d.operationRuntime.manager.Submit(ctx, daemonops.SubmitRequest{
		ProjectID:    normalizedProjectID(projectID),
		IssueID:      taskID,
		Kind:         "test.blocker",
		ResourceKeys: []string{"issue:" + normalizedProjectID(projectID) + ":" + taskID},
	}, func(runCtx context.Context) ([]byte, error) {
		close(blockerStarted)
		select {
		case <-releaseBlocker:
			return []byte(`{}`), nil
		case <-runCtx.Done():
			return nil, runCtx.Err()
		}
	})
	if err != nil {
		t.Fatalf("submit blocker operation: %v", err)
	}
	select {
	case <-blockerStarted:
	case <-time.After(time.Second):
		t.Fatal("blocker operation did not start")
	}

	closeBody, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		ForceWorktree:        true,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	closeResp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-before-reopen",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            closeBody,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !closeResp.OK {
		if closeResp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", closeResp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", closeResp)
	}
	var closeResult taskCloseResult
	if err := json.Unmarshal(closeResp.Body, &closeResult); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !closeResult.WorktreeCleanupDeferred || closeResult.WorktreeCleanupOperationID == "" {
		t.Fatalf("close result = %+v, want deferred cleanup operation", closeResult)
	}
	queued := waitForRuntimeState(t, d.operationRuntime, closeResult.WorktreeCleanupOperationID, daemonops.StateQueued)
	if queued.Kind != taskDeferredWorktreeCleanupOperationKind {
		t.Fatalf("queued operation kind = %s, want %s", queued.Kind, taskDeferredWorktreeCleanupOperationKind)
	}
	db, err := sql.Open("sqlite", filepath.Join(repoDir, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatalf("open issue database: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_reopen_history
		BEFORE INSERT ON issue_observation_events
		WHEN NEW.event_type = 'issue.status_changed'
			AND json_extract(NEW.payload_json, '$.from_status') = 'closed'
			AND json_extract(NEW.payload_json, '$.to_status') = 'open'
		BEGIN
			SELECT RAISE(ABORT, 'injected reopen history failure');
		END`); err != nil {
		t.Fatalf("create reopen failure trigger: %v", err)
	}

	openLifecycle := domain.IssueWorkflowOpen
	updateBody, err := json.Marshal(map[string]any{
		"task_id":         taskID,
		"title":           "Reopen before deferred cleanup",
		"type":            domain.TypeBug,
		"priority":        domain.P2,
		"lifecycle_state": openLifecycle,
	})
	if err != nil {
		t.Fatalf("marshal update request: %v", err)
	}
	updateResp, err := d.handleTaskUpdateDetails(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-reopen-after-close",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_details",
		Body:            updateBody,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateDetails error: %v", err)
	}
	if updateResp.OK || updateResp.Error == nil || !strings.Contains(updateResp.Error.Message, "injected reopen history failure") {
		t.Fatalf("failed reopen response = %+v, want injected history failure", updateResp)
	}
	failedReopen, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get issue after failed reopen: %v", err)
	}
	if failedReopen.Status != domain.StatusDone {
		t.Fatalf("issue status after failed reopen = %s, want closed", failedReopen.Status)
	}
	cancelled := waitForRuntimeState(t, d.operationRuntime, closeResult.WorktreeCleanupOperationID, daemonops.StateCancelled)
	if cancelled.ErrorMessage == "" {
		t.Fatalf("cancelled cleanup error message empty")
	}
	activeCleanup, err := d.operationRuntime.manager.List(ctx, daemonops.Query{
		ProjectID: normalizedProjectID(projectID), IssueID: taskID, Kind: taskDeferredWorktreeCleanupOperationKind,
		States: []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
	})
	if err != nil {
		t.Fatalf("list compensated cleanup: %v", err)
	}
	if len(activeCleanup) != 1 || activeCleanup[0].ID == closeResult.WorktreeCleanupOperationID {
		t.Fatalf("compensated cleanup = %+v, want one requeued operation", activeCleanup)
	}
	compensatedCleanupID := activeCleanup[0].ID
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fail_reopen_history`); err != nil {
		t.Fatalf("drop reopen failure trigger: %v", err)
	}

	updateResp, err = d.handleTaskUpdateDetails(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-reopen-after-close-retry",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_details",
		Body:            updateBody,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateDetails retry error: %v", err)
	}
	if !updateResp.OK {
		if updateResp.Error != nil {
			t.Fatalf("handleTaskUpdateDetails error = %s", updateResp.Error.Message)
		}
		t.Fatalf("handleTaskUpdateDetails response = %+v", updateResp)
	}
	_ = waitForRuntimeState(t, d.operationRuntime, compensatedCleanupID, daemonops.StateCancelled)
	restored, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("read restored worktree projection: %v", err)
	}
	if !found || restored.Path != sourceWorktree || restored.Branch != sourceBranch {
		t.Fatalf("restored projection = %+v found=%v, want %s %s", restored, found, sourceWorktree, sourceBranch)
	}
	reopened, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get reopened issue: %v", err)
	}
	if reopened.Status != domain.StatusOpen || reopened.State.Workflow() != domain.IssueWorkflowOpen {
		t.Fatalf("reopened issue status=%s workflow=%s, want open", reopened.Status, reopened.State.Workflow())
	}
	if strings.Contains(strings.Join(commands, "\n"), "worktree remove") {
		t.Fatalf("cleanup should not remove worktree after reopen, commands:\n%s", strings.Join(commands, "\n"))
	}
	close(releaseBlocker)
	_ = waitForRuntimeState(t, d.operationRuntime, blocker.Record.ID, daemonops.StateDone)
}

func TestRecoverInterruptedDeferredWorktreeCleanupRemovesClosedIssueWorktree(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-recover-deferred-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Recover deferred cleanup",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/recover-deferred-cleanup"
	cacheRoot := filepath.Join(repoDir, ".azedarach", "go")
	for _, kind := range []string{"normal", "race", "coverage"} {
		entry := filepath.Join(cacheRoot, "caches", "v1", kind, "issue-"+taskID, "entry")
		if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
			t.Fatalf("seed %s cache: %v", kind, err)
		}
		if err := os.WriteFile(entry, []byte("cache"), 0o644); err != nil {
			t.Fatalf("write %s cache: %v", kind, err)
		}
	}
	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" && slices.Contains(args, "--force"):
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D" && args[2] == sourceBranch:
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", strings.Join(args, " "))
		}
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, logger),
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
		ID:        "op-recover-cleanup",
		ProjectID: projectID,
		IssueID:   taskID,
		Kind:      taskDeferredWorktreeCleanupOperationKind,
		ResourceKeys: []string{
			"issue:" + normalizedProjectID(projectID) + ":" + taskID,
			"worktree:" + sourceWorktree,
			"branch:" + sourceBranch,
		},
	})
	if !ok {
		t.Fatal("recoverInterruptedOperation ok = false, want true")
	}
	if recovery.State != daemonops.StateDone {
		t.Fatalf("recovery state = %s, want done: %s", recovery.State, recovery.ErrorMessage)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "worktree remove --force --force "+sourceWorktree) {
		t.Fatalf("commands missing forced worktree remove:\n%s", joined)
	}
	if !strings.Contains(joined, "branch -D "+sourceBranch) {
		t.Fatalf("commands missing branch cleanup:\n%s", joined)
	}
	for _, kind := range []string{"normal", "race", "coverage"} {
		_, err := os.Stat(filepath.Join(cacheRoot, "caches", "v1", kind, "issue-"+taskID))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s cache remains after interrupted recovery cleanup: %v", kind, err)
		}
	}
}

func TestTaskCloseCommandRefusesChildIntegrationToBaseWithoutAncestorTarget(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-child-base"
	repoDir := t.TempDir()
	sourceWorktree := filepath.Join(repoDir, "wt-child")
	sourceBranch := "riordan/az-2/child-base"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent without active worktree",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child refuses base integration",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed child worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               childID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-child-base",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskClose response = %+v, want child base refusal", resp)
	}
	if !strings.Contains(resp.Error.Message, "no active ancestor worktree branch was found") ||
		!strings.Contains(resp.Error.Message, "az worktree create "+parentID) ||
		strings.Contains(resp.Error.Message, "--allow-base-for-child") {
		t.Fatalf("handleTaskClose error = %q, want parent target guidance without override suggestion", resp.Error.Message)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("get child after close: %v", err)
	}
	if closed.Status != domain.StatusInReview {
		t.Fatalf("child status = %s, want %s", closed.Status, domain.StatusInReview)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, " merge --no-edit ") || strings.Contains(joined, " merge-tree ") {
		t.Fatalf("git commands should not attempt child-to-base merge:\n%s", joined)
	}
}

func TestTaskUpdateStatusRejectsRawCloseActiveChildRuntime(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-reopen-child"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed child with runtime",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      "/tmp/" + childID,
		Branch:    "riordan/" + childID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed child worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]any{
		"task_id": parentID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-reopen-child",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}
	child, err := issuesClient.GetWithRuntime(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("get child after failed close: %v", err)
	}
	if child.Status != domain.StatusInReview {
		t.Fatalf("child status = %s, want %s", child.Status, domain.StatusInReview)
	}
	if strings.Contains(resp.Error.Message, "Moved closed blockers back for cleanup") {
		t.Fatalf("task.update_status response = %q, did not expect status repair for reachable active child", resp.Error.Message)
	}
}

func TestTaskGraphReadinessDependencyGating(t *testing.T) {
	root := naming.IssueID("az-root")
	a := naming.IssueID("az-a")
	b := naming.IssueID("az-b")
	aParent := root
	bParent := root

	base := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: a, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &aParent},
		{
			ID:       b,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &bParent,
			Dependencies: []domain.Dependency{
				{ID: a, Type: domain.DependencyBlocks},
			},
		},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), base)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	before, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes before error: %v", err)
	}
	if len(before.Runnable) != 1 || before.Runnable[0] != a.String() {
		t.Fatalf("before runnable = %v, want [%s]", before.Runnable, a.String())
	}
	if got := before.Blocked[b.String()]; !strings.Contains(got, a.String()) {
		t.Fatalf("before blocked[%s] = %q, want blocker %s", b.String(), got, a.String())
	}

	after := append([]domain.Task(nil), base...)
	after[1].Status = domain.StatusDone
	rootID, byID, children, err = daemonTaskGraphIndexes(root.String(), after)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes after error: %v", err)
	}
	gotAfter, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes after error: %v", err)
	}
	if len(gotAfter.Runnable) != 1 || gotAfter.Runnable[0] != b.String() {
		t.Fatalf("after runnable = %v, want [%s]", gotAfter.Runnable, b.String())
	}
}

func TestTaskGraphReadinessPropagatesRootBlockersToDescendantsAndNestedRoots(t *testing.T) {
	root := naming.IssueID("az-root")
	blocker := naming.IssueID("az-blocker")
	leaf := naming.IssueID("az-leaf")
	nested := naming.IssueID("az-nested")
	leafParent := root
	nestedParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress, Dependencies: []domain.Dependency{{ID: blocker, Type: domain.DependencyBlocks}}},
		{ID: blocker, Type: domain.TypeTask, Status: domain.StatusInProgress},
		{ID: leaf, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &leafParent, Dependencies: []domain.Dependency{{ID: "az-missing", Type: domain.DependencyBlocks}}},
		{ID: nested, Type: domain.TypeEpic, Status: domain.StatusOpen, ParentID: &nestedParent},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Runnable) != 0 || !slices.Equal(blocked.RootBlockers, []string{blocker.String()}) {
		t.Fatalf("blocked readiness = %+v", blocked)
	}
	if got := blocked.Blocked[leaf.String()]; !strings.Contains(got, "root waiting on "+blocker.String()) {
		t.Fatalf("leaf blocker = %q", got)
	} else if !strings.Contains(got, "az-missing(missing)") {
		t.Fatalf("leaf-local blocker was hidden by root gate: %q", got)
	}
	if len(blocked.NestedRoots) != 1 || blocked.NestedRoots[0].Status != "blocked_root_dependency" || blocked.NestedRoots[0].Classification != string(domain.OrchestrationCandidateBlocked) {
		t.Fatalf("nested roots = %+v", blocked.NestedRoots)
	}

	byID[blocker] = domain.Task{ID: blocker, Type: domain.TypeTask, Status: domain.StatusDone}
	ready, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready.Runnable) != 0 || !strings.Contains(ready.Blocked[leaf.String()], "az-missing(missing)") || len(ready.RootBlockers) != 0 || ready.NestedRoots[0].Status != "startable" {
		t.Fatalf("settled readiness = %+v", ready)
	}
}

func TestCloneTaskGraphReadinessResultCopiesRootBlockers(t *testing.T) {
	original := taskGraphReadinessResult{RootBlockers: []string{"upstream-blocker"}}
	cloned := cloneTaskGraphReadinessResult(original)
	cloned.RootBlockers[0] = "changed"
	if original.RootBlockers[0] != "upstream-blocker" {
		t.Fatalf("clone mutated cached root blockers: %v", original.RootBlockers)
	}
}

func TestTaskGraphReadinessSkipsForeignOwnedRunnableIssues(t *testing.T) {
	root := naming.IssueID("az-root")
	leaf := naming.IssueID("az-owned")
	leafParent := root
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)

	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{
			ID:       leaf,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &leafParent,
			Ownership: &domain.IssueOwnership{
				OwnerID:   "agent-a",
				OwnerKind: "agent",
				ClaimedAt: now.Add(-time.Minute),
				ExpiresAt: &expiresAt,
			},
		},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	foreignActor, err := daemonTaskGraphReadinessFromIndexesForActor(rootID, byID, children, "agent-b", now)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexesForActor foreign actor error: %v", err)
	}
	if len(foreignActor.Runnable) != 0 {
		t.Fatalf("foreign actor runnable = %v, want empty", foreignActor.Runnable)
	}
	if got := foreignActor.Blocked[leaf.String()]; !strings.Contains(got, "owned by agent-a") {
		t.Fatalf("foreign actor blocked[%s] = %q, want ownership blocker", leaf.String(), got)
	}

	ownerActor, err := daemonTaskGraphReadinessFromIndexesForActor(rootID, byID, children, "agent-a", now)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexesForActor owner actor error: %v", err)
	}
	if len(ownerActor.Runnable) != 1 || ownerActor.Runnable[0] != leaf.String() {
		t.Fatalf("owner actor runnable = %v, want [%s]", ownerActor.Runnable, leaf.String())
	}

	expiredActor, err := daemonTaskGraphReadinessFromIndexesForActor(rootID, byID, children, "agent-b", expiresAt.Add(time.Second))
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexesForActor expired actor error: %v", err)
	}
	if len(expiredActor.Runnable) != 1 || expiredActor.Runnable[0] != leaf.String() {
		t.Fatalf("expired actor runnable = %v, want [%s]", expiredActor.Runnable, leaf.String())
	}
}

func TestTaskOwnershipClaimConflictReturnsProtocolConflict(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-ownership-conflict"
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Owned worker",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{OwnerID: "agent-a"}); err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	d := &Daemon{
		cfg: Config{Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskOwnershipRequest{TaskID: taskID, OwnerID: "agent-b"})
	if err != nil {
		t.Fatalf("marshal ownership request: %v", err)
	}

	resp, err := d.handleTaskOwnershipClaim(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-ownership-conflict",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.ownership.claim",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskOwnershipClaim error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskOwnershipClaim response = %+v, want conflict error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
}

func TestTaskOwnershipClaimCommandKeepsReviewSeparateFromExecution(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-scoped-ownership"
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "worker under review", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{OwnerID: "worker-a"}); err != nil {
		t.Fatalf("seed execution ownership: %v", err)
	}
	if err := issuesClient.Update(ctx, taskID, domain.StatusInReview); err != nil {
		t.Fatalf("request review: %v", err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, hub: publish.NewHub(16, 8, slog.Default()), issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, revision: map[string]uint64{}}
	body, err := json.Marshal(taskOwnershipRequest{TaskID: taskID, OwnerID: "reviewer-a", Purpose: domain.CoordinationLeaseReview})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := d.handleTaskOwnershipClaim(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "req-review-lease", Kind: protocol.EnvelopeKindCommand, Command: "task.ownership.claim", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil {
		t.Fatalf("handle claim: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response = %+v, want success", resp)
	}
	var task domain.Task
	if err := json.Unmarshal(resp.Body, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.Ownership == nil || task.Ownership.OwnerID != "worker-a" {
		t.Fatalf("execution ownership = %+v, want worker-a", task.Ownership)
	}
	if len(task.CoordinationLeases) != 2 {
		t.Fatalf("coordination leases = %+v, want execution and review", task.CoordinationLeases)
	}
}

func TestTaskGraphReadinessReportsMissingDependencyAndActiveSession(t *testing.T) {
	root := naming.IssueID("az-root")
	missingLeaf := naming.IssueID("az-leaf")
	activeLeaf := naming.IssueID("az-active")
	missingParent := root
	activeParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{
			ID:       missingLeaf,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &missingParent,
			Dependencies: []domain.Dependency{
				{ID: naming.IssueID("az-missing"), Type: domain.DependencyBlocks},
			},
		},
		{ID: activeLeaf, Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &activeParent, HasTmuxSession: true},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if len(result.Runnable) != 0 {
		t.Fatalf("runnable = %v, want empty", result.Runnable)
	}
	if got := result.Blocked[missingLeaf.String()]; !strings.Contains(got, "missing") {
		t.Fatalf("blocked[%s] = %q, want missing marker", missingLeaf.String(), got)
	}
	if len(result.Active) != 1 || result.Active[0] != activeLeaf.String() {
		t.Fatalf("active = %v, want [%s]", result.Active, activeLeaf.String())
	}
}

func TestTaskGraphReadinessStopsAtNestedRoots(t *testing.T) {
	root := naming.IssueID("az-root")
	nested := naming.IssueID("az-nested")
	grandchild := naming.IssueID("az-grandchild")
	direct := naming.IssueID("az-direct")
	nestedParent := root
	grandchildParent := nested
	directParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: nested, Type: domain.TypeEpic, Status: domain.StatusOpen, ParentID: &nestedParent},
		{ID: grandchild, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &grandchildParent},
		{ID: direct, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &directParent},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if len(result.Runnable) != 1 || result.Runnable[0] != direct.String() {
		t.Fatalf("runnable = %v, want direct child only %s", result.Runnable, direct.String())
	}
	if len(result.NestedRoots) != 1 || result.NestedRoots[0].IssueID != nested.String() {
		t.Fatalf("nested roots = %v, want %s", result.NestedRoots, nested.String())
	}
	if result.NestedRoots[0].Status != "startable" || result.NestedRoots[0].IssueStatus != string(domain.StatusOpen) {
		t.Fatalf("nested root status = %+v, want startable with issue_status=open", result.NestedRoots[0])
	}
	if result.Capacity.DirectRunnableCount != 1 || result.Capacity.NestedStartableCount != 1 {
		t.Fatalf("capacity = %+v, want direct runnable and nested startable counts", result.Capacity)
	}
	if slices.Contains(result.Runnable, grandchild.String()) {
		t.Fatalf("runnable = %v, must not flatten nested root descendant %s", result.Runnable, grandchild.String())
	}
	observations := daemonTaskGraphWorkerObservations(rootID, byID, children, result, taskGraphReadinessContext{})
	observedDirect := false
	for _, observation := range observations {
		if observation.IssueID == direct.String() {
			observedDirect = true
		}
		if observation.IssueID == grandchild.String() {
			t.Fatalf("worker observations included nested root descendant: %+v", observations)
		}
	}
	if !observedDirect {
		t.Fatalf("worker observations = %+v, want direct child %s", observations, direct.String())
	}
}

func TestTaskGraphReadinessExcludesBacklogNestedRootsDespiteOpenCompatibilityStatus(t *testing.T) {
	root := naming.IssueID("az-root")
	ada := naming.IssueID("ADA")
	cif := naming.IssueID("CIF")
	adaChild := naming.IssueID("ada-child")
	cifChild := naming.IssueID("cif-child")
	backlog, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: ada, Type: domain.TypeEpic, Status: domain.StatusOpen, State: backlog, ParentID: &root},
		{ID: cif, Type: domain.TypeEpic, Status: domain.StatusOpen, State: backlog, ParentID: &root},
		{ID: adaChild, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &ada},
		{ID: cifChild, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &cif},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NestedRoots) != 2 {
		t.Fatalf("nested roots = %+v, want ADA and CIF", result.NestedRoots)
	}
	for _, nested := range result.NestedRoots {
		if nested.Status != "not_counting_capacity" || nested.IssueStatus != string(domain.StatusOpen) || nested.Classification != string(domain.OrchestrationCandidateBacklog) {
			t.Fatalf("nested root = %+v, want backlog exclusion despite compatibility status open", nested)
		}
		if !slices.Contains(nested.ExclusionReasons, "lifecycle-backlog") {
			t.Fatalf("nested root exclusions = %v, want lifecycle-backlog", nested.ExclusionReasons)
		}
	}
	if result.Capacity.NestedStartableCount != 0 || result.Capacity.NotCountingCapacityCount != 2 {
		t.Fatalf("capacity = %+v, want both backlog roots outside startable capacity", result.Capacity)
	}
}

func TestTaskGraphReadinessExcludesBacklogLeafDespiteOpenCompatibilityStatus(t *testing.T) {
	root := naming.IssueID("az-root")
	backlogLeaf := naming.IssueID("paused-leaf")
	activeLeaf := naming.IssueID("active-leaf")
	reviewLeaf := naming.IssueID("review-leaf")
	liveBlockedLeaf := naming.IssueID("live-blocked-leaf")
	openLeaf := naming.IssueID("ready-leaf")
	backlog, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	active, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowActive})
	if err != nil {
		t.Fatal(err)
	}
	review, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowActive, Review: domain.IssueReviewRequested})
	if err != nil {
		t.Fatal(err)
	}
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: backlogLeaf, Title: "Paused", Description: "Executable", Type: domain.TypeTask, Status: domain.StatusOpen, State: backlog, ParentID: &root},
		{ID: activeLeaf, Title: "Active", Description: "Executable", Type: domain.TypeTask, Status: domain.StatusInProgress, State: active, ParentID: &root},
		{ID: reviewLeaf, Title: "Review", Description: "Executable", Type: domain.TypeTask, Status: domain.StatusInReview, State: review, ParentID: &root},
		{ID: liveBlockedLeaf, Title: "Live blocked", Description: "Executable", Type: domain.TypeTask, Status: domain.StatusInProgress, State: active, ParentID: &root, HasTmuxSession: true, Dependencies: []domain.Dependency{{ID: backlogLeaf, Type: domain.DependencyBlocks}}},
		{ID: openLeaf, Title: "Ready", Description: "Executable", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &root},
	}
	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Runnable, []string{openLeaf.String()}) {
		t.Fatalf("runnable = %v, want only ready leaf", result.Runnable)
	}
	if got := result.Blocked[backlogLeaf.String()]; got != "lifecycle-backlog" {
		t.Fatalf("blocked backlog reason = %q, want lifecycle-backlog", got)
	}
	if got := result.Blocked[activeLeaf.String()]; got != "active-work-present" {
		t.Fatalf("blocked active reason = %q, want active-work-present", got)
	}
	if !slices.Equal(result.Active, []string{liveBlockedLeaf.String()}) {
		t.Fatalf("active = %v, want live runtime retained despite dependency", result.Active)
	}
	if result.Capacity.DirectRunnableCount != 1 || result.Capacity.DirectActiveCount != 1 || result.Capacity.TotalCountingCapacityCount != 1 {
		t.Fatalf("capacity = %+v, backlog leaf must not count and live runtime must count active", result.Capacity)
	}
}

func TestTaskGraphReadinessBacklogRootContainsOpenDescendants(t *testing.T) {
	root := naming.IssueID("paused-root")
	direct := naming.IssueID("open-direct")
	nested := naming.IssueID("open-nested")
	nestedChild := naming.IssueID("open-nested-child")
	backlog, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusOpen, State: backlog},
		{ID: direct, Title: "Direct", Description: "Executable", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &root},
		{ID: nested, Title: "Nested", Type: domain.TypeEpic, Status: domain.StatusOpen, ParentID: &root},
		{ID: nestedChild, Title: "Nested child", Description: "Executable", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &nested},
	}
	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Runnable) != 0 || result.Blocked[direct.String()] != "lifecycle-backlog" {
		t.Fatalf("readiness = %+v, want direct child contained by backlog root", result)
	}
	if len(result.NestedRoots) != 1 || result.NestedRoots[0].Status != "not_counting_capacity" || result.NestedRoots[0].Classification != string(domain.OrchestrationCandidateBacklog) || !slices.Contains(result.NestedRoots[0].ExclusionReasons, "lifecycle-backlog") || result.Blocked[nested.String()] != "lifecycle-backlog" {
		t.Fatalf("nested roots = %+v, want inherited backlog containment", result.NestedRoots)
	}
	if result.Capacity.DirectRunnableCount != 0 || result.Capacity.NestedStartableCount != 0 || result.Capacity.TotalCountingCapacityCount != 0 {
		t.Fatalf("capacity = %+v, want no backlog-contained start capacity", result.Capacity)
	}
}

func TestTaskGraphReadinessRefreshesBacklogRootContainmentAcrossDaemonClients(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	rootID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Active root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Open child", Description: "Executable", Acceptance: "Worker completes scoped change", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	materializerCtx, cancelMaterializer := context.WithCancel(ctx)
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}, hub: publish.NewHub(16, 8, slog.Default()), revision: map[string]uint64{}}
	if err := d.startProjectReadMaterializers(materializerCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelMaterializer()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		d.stopAllProjectReadMaterializers(shutdownCtx)
	})
	before, err := d.taskGraphReadiness(ctx, "proj", rootID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before.Runnable, []string{childID}) {
		t.Fatalf("before runnable = %v, want open child", before.Runnable)
	}
	backlog := domain.IssueWorkflowBacklog
	if err := writer.UpdateDetails(ctx, rootID, issues.UpdateTaskParams{Title: "Active root", Type: domain.TypeEpic, Priority: domain.P2, Lifecycle: &backlog}); err != nil {
		t.Fatal(err)
	}
	waitForProjectMaterializerRevision(t, d, "proj", 1)
	after, err := d.taskGraphReadiness(ctx, "proj", rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Runnable) != 0 || after.Blocked[childID] != "lifecycle-backlog" {
		t.Fatalf("after readiness = %+v, want refreshed backlog-root containment", after)
	}
}

func TestTaskGraphReadinessRefreshesNestedRootLifecycleAcrossDaemonClients(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	rootID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Outer root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	nestedID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "ADA", Type: domain.TypeEpic, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Create(ctx, issues.CreateTaskParams{Title: "ADA child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &nestedID}); err != nil {
		t.Fatal(err)
	}
	materializerCtx, cancelMaterializer := context.WithCancel(ctx)
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}, hub: publish.NewHub(16, 8, slog.Default()), revision: map[string]uint64{}}
	if err := d.startProjectReadMaterializers(materializerCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelMaterializer()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		d.stopAllProjectReadMaterializers(shutdownCtx)
	})
	before, err := d.taskGraphReadiness(ctx, "proj", rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.NestedRoots) != 1 || before.NestedRoots[0].Status != "startable" {
		t.Fatalf("before nested roots = %+v, want open root startable", before.NestedRoots)
	}
	backlog := domain.IssueWorkflowBacklog
	if err := writer.UpdateDetails(ctx, nestedID, issues.UpdateTaskParams{Title: "ADA", Type: domain.TypeEpic, Priority: domain.P2, Lifecycle: &backlog}); err == nil || !strings.Contains(err.Error(), "cannot regress to backlog") {
		t.Fatalf("update nested root = %v, want ancestor lifecycle floor rejection", err)
	}
	after, err := d.taskGraphReadiness(ctx, "proj", rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.NestedRoots) != 1 || after.NestedRoots[0].Status != "startable" {
		t.Fatalf("after nested roots = %+v, want invariant-preserved startable root", after.NestedRoots)
	}
}

func waitForProjectMaterializerRevision(t *testing.T, d *Daemon, projectID string, minimum uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d.currentRevision(projectID) >= minimum {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("project materializer revision = %d, want at least %d", d.currentRevision(projectID), minimum)
}

func TestTaskGraphReadinessLoadsRootScopedTasksWithLargeUnrelatedProject(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-root-scoped-readiness"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Watched root",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "External blocker",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Watched child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}
	unrelated := make([]issuefixture.Issue, 250)
	for i := range unrelated {
		unrelated[i] = issuefixture.Issue{ID: fmt.Sprintf("fixture-readiness-unrelated-%03d", i), Title: fmt.Sprintf("Unrelated %03d", i), Type: domain.TypeTask, Priority: domain.P3, Status: domain.StatusOpen}
	}
	setup, err := issuefixture.SeedPath(ctx, dbPath, issuefixture.Fixture{Issues: unrelated})
	if err != nil {
		t.Fatalf("seed unrelated issues: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	queryStarted := time.Now()
	tasks, err := d.loadTaskGraphReadinessDomainTasks(ctx, projectID, rootID)
	queryDuration := time.Since(queryStarted)
	if err != nil {
		t.Fatalf("loadTaskGraphReadinessDomainTasks error: %v", err)
	}
	t.Logf("graph readiness scale timing: setup=%s query=%s rows=%d", setup.Duration, queryDuration, setup.Rows)
	byID := map[string]domain.Task{}
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if got, want := len(byID), 3; got != want {
		t.Fatalf("root-scoped task count = %d, want %d (%s, %s, %s); tasks=%v", got, want, rootID, childID, blockerID, byID)
	}
	for _, wantID := range []string{rootID, childID, blockerID} {
		if _, ok := byID[wantID]; !ok {
			t.Fatalf("root-scoped tasks missing %s: tasks=%v", wantID, byID)
		}
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if got := ready.Blocked[childID]; !strings.Contains(got, blockerID) {
		t.Fatalf("blocked[%s] = %q, want blocker %s", childID, got, blockerID)
	}
	if len(ready.Runnable) != 0 {
		t.Fatalf("runnable = %v, want none while blocker is open", ready.Runnable)
	}
}

func TestTaskGraphReadinessRefreshesOnlyRootScopedSessions(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-root-scoped-session-refresh"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Watched root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Watched child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	unrelatedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Unrelated active session",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create unrelated: %v", err)
	}

	childSessionID := naming.CanonicalSessionID(projectID, childID)
	unrelatedSessionID := naming.CanonicalSessionID(projectID, unrelatedID)
	seededAt := time.Date(2026, time.July, 7, 2, 0, 0, 0, time.UTC)
	for _, session := range []daemonstate.Session{
		{
			ID:            childSessionID,
			IssueID:       childID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     seededAt,
		},
		{
			ID:            unrelatedSessionID,
			IssueID:       unrelatedID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     seededAt,
		},
	} {
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, session); err != nil {
			t.Fatalf("seed session %s: %v", session.ID, err)
		}
	}

	tmuxRunner := newTestTmuxRunner("foreign-live-session")
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	tasks, err := d.loadTaskGraphReadinessDomainTasks(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("loadTaskGraphReadinessDomainTasks error: %v", err)
	}
	byID := map[string]domain.Task{}
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if got, want := len(byID), 2; got != want {
		t.Fatalf("root-scoped task count = %d, want %d; tasks=%v", got, want, byID)
	}

	childRow, found, err := runtimeStateStore.GetSessionState(ctx, projectID, childSessionID)
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	if !found {
		t.Fatal("child session row not found")
	}
	if childRow.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("child observed state = %s, want %s", childRow.ObservedState, daemonstate.SessionStateStopped)
	}

	unrelatedRow, found, err := runtimeStateStore.GetSessionState(ctx, projectID, unrelatedSessionID)
	if err != nil {
		t.Fatalf("get unrelated session: %v", err)
	}
	if !found {
		t.Fatal("unrelated session row not found")
	}
	if unrelatedRow.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("unrelated observed state = %s, want unchanged %s", unrelatedRow.ObservedState, daemonstate.SessionStateRunning)
	}
	if !unrelatedRow.UpdatedAt.Equal(seededAt) {
		t.Fatalf("unrelated updated_at = %v, want unchanged %v", unrelatedRow.UpdatedAt, seededAt)
	}
}

func TestTaskGraphRuntimeValidationCoalescesMultiWatchAndTUIP95(t *testing.T) {
	projectID := "proj-graph-shared-load"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	setupCtx := context.Background()
	rootID, err := issuesClient.Create(setupCtx, issues.CreateTaskParams{
		Title:  "Shared readiness root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(setupCtx, issues.CreateTaskParams{
		Title:    "Shared readiness child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, childID)
	if err := upsertSessionStateFixture(runtimeStateStore, setupCtx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       childID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session state: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	tmuxRunner := &testTmuxRunner{
		sessions:            map[string]bool{sessionID: true},
		listSessionsEntered: entered,
		listSessionsRelease: release,
	}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		revision: map[string]uint64{projectID: 1},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)
	// Keep fixture initialization outside the operation deadline. Under -race,
	// lazy migrations can exceed the intended three-second readiness assertion.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type readinessResult struct {
		ready taskGraphReadinessResult
		err   error
	}
	firstCh := make(chan readinessResult, 1)
	go func() {
		ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
		firstCh <- readinessResult{ready: ready, err: err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for first readiness load to enter tmux: %v", ctx.Err())
	}

	secondCh := make(chan readinessResult, 1)
	go func() {
		ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
		secondCh <- readinessResult{ready: ready, err: err}
	}()
	close(release)

	var first, second readinessResult
	select {
	case first = <-firstCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for first readiness result: %v", ctx.Err())
	}
	select {
	case second = <-secondCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for second readiness result: %v", ctx.Err())
	}
	if first.err != nil {
		t.Fatalf("first readiness error: %v", first.err)
	}
	if second.err != nil {
		t.Fatalf("second readiness error: %v", second.err)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 1 {
		t.Fatalf("tmux list-sessions calls = %d, want one shared graph readiness load", got)
	}
	if len(first.ready.Active) != 1 || first.ready.Active[0] != childID {
		t.Fatalf("first active = %v, want [%s]", first.ready.Active, childID)
	}
	if len(second.ready.Active) != 1 || second.ready.Active[0] != childID {
		t.Fatalf("second active = %v, want [%s]", second.ready.Active, childID)
	}

	third, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("cached readiness error: %v", err)
	}
	if len(third.Active) != 1 || third.Active[0] != childID {
		t.Fatalf("cached active = %v, want [%s]", third.Active, childID)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 1 {
		t.Fatalf("tmux list-sessions calls after sequential duplicate = %d, want cached hybrid runtime observation", got)
	}

	d.nextRevision(projectID)
	if _, err := d.taskGraphReadiness(ctx, projectID, rootID); err != nil {
		t.Fatalf("readiness after revision change: %v", err)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 2 {
		t.Fatalf("tmux list-sessions calls after revision change = %d, want cache invalidation", got)
	}

	rootedScope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.OrchestrationSnapshotRequest{Scope: rootedScope}
	build := func(_ context.Context, _ string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		return protocol.OrchestrationSnapshot{Scope: request.Scope, Active: []string{childID}, Blocked: map[string]string{}}, nil
	}
	if _, _, stable, err := d.loadOrchestrationSnapshot(ctx, projectID, request, build); err != nil || !stable {
		t.Fatalf("warm orchestration snapshot stable=%v err=%v", stable, err)
	}

	const (
		watchers   = 5
		iterations = 20
		p95Budget  = 25 * time.Millisecond
	)
	durations := make([]time.Duration, 0, (watchers+1)*iterations)
	for range iterations {
		d.taskGraphRuntimeValidationMu.Lock()
		d.taskGraphRuntimeValidations = map[string]taskGraphRuntimeValidationEntry{}
		d.taskGraphRuntimeValidationMu.Unlock()
		entered := make(chan struct{})
		release := make(chan struct{})
		tmuxRunner.mu.Lock()
		tmuxRunner.listSessionsEntered = entered
		tmuxRunner.listSessionsRelease = release
		tmuxRunner.mu.Unlock()

		type timedResult struct {
			duration time.Duration
			err      error
		}
		results := make(chan timedResult, watchers+1)
		go func() {
			started := time.Now()
			_, err := d.taskGraphReadiness(ctx, projectID, rootID)
			results <- timedResult{duration: time.Since(started), err: err}
		}()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for coalesced runtime validation: %v", ctx.Err())
		}
		for range watchers - 1 {
			go func() {
				started := time.Now()
				_, err := d.taskGraphReadiness(ctx, projectID, rootID)
				results <- timedResult{duration: time.Since(started), err: err}
			}()
		}
		go func() {
			started := time.Now()
			_, _, _, err := d.loadOrchestrationSnapshot(ctx, projectID, request, build)
			results <- timedResult{duration: time.Since(started), err: err}
		}()
		close(release)
		for range watchers + 1 {
			result := <-results
			if result.err != nil {
				t.Fatalf("multi-watch/TUI cached read: %v", result.err)
			}
			durations = append(durations, result.duration)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(len(durations)*95+99)/100-1]
	if p95 > p95Budget {
		t.Fatalf("multi-watch plus TUI hybrid p95 = %s, budget %s", p95, p95Budget)
	}
	wantRuntimeCalls := 2 + iterations
	if got := tmuxRunner.listSessionCallCount(); got != wantRuntimeCalls {
		t.Fatalf("list-sessions calls = %d, want %d (one per forced observation, not per reader)", got, wantRuntimeCalls)
	}
	if got := tmuxRunner.listPaneCallCount(); got != wantRuntimeCalls {
		t.Fatalf("list-panes calls = %d, want %d (one per forced observation, not per reader)", got, wantRuntimeCalls)
	}
	t.Logf("multi-watch plus TUI hybrid p95=%s budget=%s samples=%d runtime_validations=%d", p95, p95Budget, len(durations), iterations)
}

func TestTaskGraphReadinessOwnershipExpiryBoundsCache(t *testing.T) {
	now := time.Date(2026, time.July, 13, 5, 30, 0, 0, time.UTC)
	early := now.Add(2 * time.Second)
	late := now.Add(time.Minute)
	expired := now.Add(-time.Second)
	tasks := []domain.Task{
		{Ownership: &domain.IssueOwnership{ExpiresAt: &late}},
		{Ownership: &domain.IssueOwnership{ExpiresAt: &early}},
		{Ownership: &domain.IssueOwnership{ExpiresAt: &expired}},
	}
	if got := taskGraphReadinessOwnershipExpiry(tasks, now); !got.Equal(early) {
		t.Fatalf("cache expiry = %s, want earliest active ownership expiry %s", got, early)
	}
}

func TestTaskClosePreflightLoadsRootScopedSubtreeWithLargeUnrelatedProject(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-close-scoped-subtree"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close root",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close grandchild",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &childID,
	})
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}
	unrelated := make([]issuefixture.Issue, 250)
	for i := range unrelated {
		unrelated[i] = issuefixture.Issue{ID: fmt.Sprintf("fixture-close-unrelated-%03d", i), Title: fmt.Sprintf("Unrelated %03d", i), Type: domain.TypeTask, Priority: domain.P3, Status: domain.StatusOpen}
	}
	setup, err := issuefixture.SeedPath(ctx, dbPath, issuefixture.Fixture{Issues: unrelated})
	if err != nil {
		t.Fatalf("seed unrelated issues: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	queryStarted := time.Now()
	tasks, err := d.loadTaskClosePreflightDomainTasks(ctx, projectID, rootID)
	queryDuration := time.Since(queryStarted)
	if err != nil {
		t.Fatalf("loadTaskClosePreflightDomainTasks error: %v", err)
	}
	t.Logf("close preflight scale timing: setup=%s query=%s rows=%d", setup.Duration, queryDuration, setup.Rows)
	byID := map[string]domain.Task{}
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if got, want := len(byID), 3; got != want {
		t.Fatalf("close preflight scoped task count = %d, want %d (%s, %s, %s); tasks=%v", got, want, rootID, childID, grandchildID, byID)
	}
	for _, wantID := range []string{rootID, childID, grandchildID} {
		if _, ok := byID[wantID]; !ok {
			t.Fatalf("close preflight scoped tasks missing %s: tasks=%v", wantID, byID)
		}
	}

	guard := daemonCloseGuardChildBlockers(naming.IssueID(rootID), tasks, taskClosePreflightOptions{})
	if len(guard) != 1 || !strings.Contains(guard[0], childID+" (open)") || !strings.Contains(guard[0], grandchildID+" (open)") {
		t.Fatalf("close child guard = %+v, want root descendants only", guard)
	}
}

func TestTaskGraphReadinessSurfacesPendingSessionStartProgress(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Worker",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, nextRevision: sequentialRevision()})
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	if _, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ID:           "op-session-start",
		ProjectID:    projectID,
		IssueID:      childID,
		Kind:         daemonhandlers.CommandSessionStart,
		ResourceKeys: []string{"issue:" + projectID + ":" + childID},
	}, func(ctx context.Context) ([]byte, error) {
		_ = daemonops.ReportProgress(ctx, daemonops.Progress{
			Phase:   "worktree_preflight",
			Message: "creating or reusing worktree",
			Current: 25,
			Total:   100,
			Unit:    "percent",
			Percent: 25,
		})
		close(started)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatalf("submit session start operation: %v", err)
	}
	<-started
	waitForRuntimeProgress(t, runtime, "op-session-start", "worktree_preflight")

	daemon := &Daemon{
		cfg:              Config{RepoDir: repoDir, Logger: slog.Default()},
		operationRuntime: runtime,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	ready, err := daemon.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.Runnable) != 0 {
		t.Fatalf("runnable = %+v, want pending session start removed", ready.Runnable)
	}
	if len(ready.SessionStartProgress) != 1 {
		t.Fatalf("session_start_progress = %+v, want one entry", ready.SessionStartProgress)
	}
	progress := ready.SessionStartProgress[0]
	if progress.IssueID != childID || progress.OperationID != "op-session-start" || progress.Phase != "worktree_preflight" || progress.Percent != 25 {
		t.Fatalf("session start progress = %+v", progress)
	}
}

func TestSessionStartStatusKeepsHistoricalFailureDistinctFromCurrentRetry(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-retry-status"
	issueID := "az-retry"
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: t.TempDir(),
		logger:  slog.Default(),
		hub:     publish.NewHub(16, 8, slog.Default()),
	})
	t.Cleanup(func() { _ = runtime.Close() })

	failedAt := time.Date(2026, time.July, 14, 1, 0, 0, 0, time.UTC)
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID: "op-historical", ProjectID: projectID, IssueID: issueID,
		Kind: daemonhandlers.CommandSessionStart, DedupeKey: "session.start:az-retry:old",
		ResourceKeys: []string{"issue:" + projectID + ":" + issueID}, State: opstore.StateFailed,
		SubmittedAt: failedAt.Add(-time.Second), FinishedAt: &failedAt,
		ErrorJSON: json.RawMessage(`{"message":"obsolete generation failed"}`),
	}); err != nil {
		t.Fatalf("seed historical failure: %v", err)
	}
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID: "op-current", ProjectID: projectID, IssueID: issueID,
		Kind: daemonhandlers.CommandSessionStart, DedupeKey: "session.start:az-retry:new",
		ResourceKeys: []string{"issue:" + projectID + ":" + issueID}, State: opstore.StateQueued,
		SubmittedAt: failedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed current retry: %v", err)
	}

	d := &Daemon{cfg: Config{Logger: slog.Default()}, operationRuntime: runtime}
	progress := d.sessionStartProgressByIssue(ctx, projectID)
	current, ok := progress[sessionKey(issueID)]
	if !ok || current.OperationID != "op-current" || current.OperationState != string(daemonops.StateQueued) {
		t.Fatalf("current start progress = %+v, want op-current queued", progress)
	}
	history := d.failedSessionStartByIssue(ctx, projectID)
	if got := history[issueID]; got.OperationID != "op-historical" || got.OperationState != string(daemonops.StateFailed) {
		t.Fatalf("historical start failure = %+v, want distinct op-historical failed", got)
	}
}

func TestTaskGraphReadinessPendingStartOverridesStaleCloseableProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-pending-start-overrides-stale"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Starting child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "daemon-task-close",
		SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": "riordan/" + childID + "/task", "target_branch": "main",
			"source_oid": "source-sha", "target_oid": "target-sha",
		},
	}); err != nil {
		t.Fatalf("seed durable integration evidence: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      filepath.Join(t.TempDir(), "repo-"+childID),
		Branch:    "riordan/" + childID + "/task",
		UpdatedAt: time.Date(2026, time.July, 7, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed stale worktree projection: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: filepath.Dir(dbPath),
		logger:  logger,
		hub:     publish.NewHub(16, 8, logger),
	})
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID:  "op-session-start",
		ProjectID:    projectID,
		IssueID:      childID,
		Kind:         daemonhandlers.CommandSessionStart,
		DedupeKey:    "session.start:" + childID,
		ResourceKeys: []string{"issue:" + projectID + ":" + childID},
		State:        opstore.StateRunning,
		SubmittedAt:  time.Date(2026, time.July, 7, 9, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed running session.start operation: %v", err)
	}

	d := &Daemon{
		cfg:              Config{RepoDir: ".", Logger: logger},
		operationRuntime: runtime,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if slices.Contains(ready.Runnable, childID) {
		t.Fatalf("pending child still runnable: %+v", ready)
	}
	if len(ready.StaleCloseableChildren) != 0 {
		t.Fatalf("stale closeable children = %+v, want none while start is running", ready.StaleCloseableChildren)
	}
	if len(ready.Pending) != 1 || ready.Pending[0].IssueID != childID || ready.Pending[0].OperationID != "op-session-start" {
		t.Fatalf("pending = %+v, want running start for %s", ready.Pending, childID)
	}
}

func TestTaskGraphReadinessStoppedProjectionSuppressesNewerSnapshotAfterClose(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-stopped-projection-wins"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed child",
		Type:     domain.TypeTask,
		Status:   domain.StatusDone,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create closed child: %v", err)
	}

	sessionID := naming.CanonicalSessionID(projectID, childID)
	stoppedAt := time.Date(2026, time.July, 7, 9, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       childID,
		State:         daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     stoppedAt,
	}); err != nil {
		t.Fatalf("seed stopped projection: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.UpsertSession(projectID, sessionID, childID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed in-memory session: %v", err)
	}
	snapshot := sessionStore.ReadSnapshot(projectID)
	session := snapshot.Sessions[sessionID]
	session.UpdatedAt = stoppedAt.Add(time.Minute)
	sessionStore.ReplaceProjectSessions(projectID, []daemonstate.Session{session})

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		sessionStore: sessionStore,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.Runnable) != 0 {
		t.Fatalf("runnable = %+v, want none for closed child", ready.Runnable)
	}
	if len(ready.Active) != 0 || len(ready.ActiveSessions) != 0 {
		t.Fatalf("closed child resurrected as active: active=%+v active_sessions=%+v", ready.Active, ready.ActiveSessions)
	}
	if len(ready.StaleCloseableChildren) != 0 {
		t.Fatalf("stale closeable children = %+v, want none for closed child", ready.StaleCloseableChildren)
	}
}

func TestTaskCompletionAdviceIncludesDomainNextSteps(t *testing.T) {
	advice := daemonTaskCompletionAdvice("az-1", []string{"az-2"}, []taskGraphNestedRoot{{IssueID: "az-5"}}, []string{"az-3"}, []string{"az-4"}, nil)
	joined := strings.Join(advice, "\n")
	for _, want := range []string{
		"az orchestrate close-session --issue az-4",
		"az orchestrator-session start --root az-5",
		"az issue close --id az-3",
		"az orchestrate start --root az-1 --issue az-2 --json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("advice = %+v, missing %q", advice, want)
		}
	}
}

func TestTaskGraphCapacitySummaryDedupesNestedStartProgress(t *testing.T) {
	ready := taskGraphReadinessResult{
		Active:               []string{"az-direct"},
		SessionStartProgress: []taskGraphSessionStartProgress{{IssueID: "az-nested", OperationID: "op-1"}},
		NestedRoots:          []taskGraphNestedRoot{{IssueID: "az-nested", Status: "active"}},
	}
	capacity := daemonTaskGraphCapacitySummary(ready)
	if capacity.DirectActiveCount != 1 || capacity.PendingStartsCount != 1 || capacity.NestedActiveCount != 1 {
		t.Fatalf("capacity counts = %+v, want direct/pending/nested each reported separately", capacity)
	}
	if capacity.TotalCountingCapacityCount != 2 {
		t.Fatalf("total counting capacity = %d, want unique issue count 2", capacity.TotalCountingCapacityCount)
	}
}

func TestTaskGraphReadinessKeepsOpenChildWithPreservedCleanWorktreeRunnable(t *testing.T) {
	root := naming.IssueID("az-root")
	child := naming.IssueID("az-child")
	childParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{
			ID:            child,
			Type:          domain.TypeTask,
			Status:        domain.StatusOpen,
			ParentID:      &childParent,
			HasWorktree:   true,
			GitAheadCount: 0,
		},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if !slices.Contains(result.Runnable, child.String()) {
		t.Fatalf("runnable = %+v, want preserved unfinished child %s", result.Runnable, child)
	}
	if len(result.StaleCloseableChildren) != 0 {
		t.Fatalf("stale_closeable_children = %+v, want none without completion evidence", result.StaleCloseableChildren)
	}
}

func TestTaskGraphReadinessKeepsOpenRootWithPreservedCleanWorktreeRunnable(t *testing.T) {
	root := naming.IssueID("az-root")
	tasks := []domain.Task{{
		ID:            root,
		Type:          domain.TypeBug,
		Status:        domain.StatusOpen,
		HasWorktree:   true,
		GitAheadCount: 0,
	}}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if !slices.Contains(result.Runnable, root.String()) {
		t.Fatalf("runnable = %+v, want preserved unfinished root %s", result.Runnable, root)
	}
	if len(result.StaleCloseableChildren) != 0 {
		t.Fatalf("stale_closeable_children = %+v, want no close suggestion for unfinished root", result.StaleCloseableChildren)
	}
}

func TestTaskGraphReadinessReopensStoppedRootWithPreservedCleanWorktree(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-reopened-root"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Stopped root", Type: domain.TypeBug, Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, rootID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "daemon-task-close",
		SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": "riordan/" + rootID + "/preserved", "target_branch": "main",
			"source_oid": "completed-source", "target_oid": "completed-target",
		},
	}); err != nil {
		t.Fatalf("seed prior trusted integration receipt: %v", err)
	}
	if err := issuesClient.Update(ctx, rootID, domain.StatusOpen); err != nil {
		t.Fatalf("return stopped root to open: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, rootID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "daemon-task-close",
		SourceCommand: "integrate-before-close",
		Payload:       map[string]any{"project_id": projectID, "source_oid": "incomplete-source"},
	}); err != nil {
		t.Fatalf("seed incomplete authority integration claim: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, rootID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "agent",
		SourceCommand: "integrate-before-close",
		Payload:       map[string]any{"source_oid": "forged-source", "target_oid": "forged-target"},
	}); err != nil {
		t.Fatalf("seed untrusted integration claim: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   rootID,
		Path:      filepath.Join(t.TempDir(), "preserved-root"),
		Branch:    "riordan/" + rootID + "/preserved",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed preserved worktree: %v", err)
	}
	cleanStatus, err := json.Marshal(git.GitStatus{HasChanges: false, GitAheadCount: 0})
	if err != nil {
		t.Fatalf("marshal clean git status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, rootID, cleanStatus, time.Now().UTC()); err != nil {
		t.Fatalf("seed clean worktree status: %v", err)
	}

	d := &Daemon{
		cfg:                   Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByRoot:   map[string]*daemonstate.RuntimeStateStore{".": runtimeStore},
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness: %v", err)
	}
	if !slices.Contains(ready.Runnable, rootID) {
		t.Fatalf("runnable = %+v, want reopened stopped root %s", ready.Runnable, rootID)
	}
	if len(ready.StaleCloseableChildren) != 0 {
		t.Fatalf("stale_closeable_children = %+v, want none after return to open", ready.StaleCloseableChildren)
	}
	complete, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck: %v", err)
	}
	if len(complete.StaleCloseableChildren) != 0 || !strings.Contains(strings.Join(complete.Reasons, "\n"), "runnable leaves remain: "+rootID) {
		t.Fatalf("complete check = %+v, want reopened root runnable rather than stale-closeable", complete)
	}
}

func TestTaskGraphReadinessKeepsRootReceiptWithoutLaterReopenStaleCloseable(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-integrated-root"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Integrated root", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, rootID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "daemon-task-close",
		SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": "riordan/" + rootID + "/integrated", "target_branch": "main",
			"source_oid": "completed-source", "target_oid": "completed-target",
		},
	}); err != nil {
		t.Fatalf("seed trusted integration receipt: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID, IssueID: rootID, Path: filepath.Join(t.TempDir(), "integrated-root"), Branch: "riordan/" + rootID + "/integrated", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed preserved worktree: %v", err)
	}
	cleanStatus, err := json.Marshal(git.GitStatus{HasChanges: false, GitAheadCount: 0})
	if err != nil {
		t.Fatalf("marshal clean status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, rootID, cleanStatus, time.Now().UTC()); err != nil {
		t.Fatalf("seed clean worktree status: %v", err)
	}

	d := &Daemon{
		cfg:                   Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByRoot:   map[string]*daemonstate.RuntimeStateStore{".": runtimeStore},
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness: %v", err)
	}
	if len(ready.StaleCloseableChildren) != 1 || ready.StaleCloseableChildren[0].IssueID != rootID || slices.Contains(ready.Runnable, rootID) {
		t.Fatalf("readiness = %+v, want integrated root stale-closeable", ready)
	}
	complete, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck: %v", err)
	}
	if len(complete.StaleCloseableChildren) != 1 || complete.StaleCloseableChildren[0].IssueID != rootID {
		t.Fatalf("complete check = %+v, want integrated root stale-closeable", complete)
	}
}

func TestTaskGraphReadinessReportsStaleChildBranchContainmentRisk(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-containment-risk"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootIDRaw, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	rootID := naming.IssueID(rootIDRaw)
	closedIDRaw, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Closed child", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &rootIDRaw})
	if err != nil {
		t.Fatalf("create closed child: %v", err)
	}
	closedID := naming.IssueID(closedIDRaw)
	activeIDRaw, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Active reconciliation", Type: domain.TypeBug, Status: domain.StatusInProgress, ParentID: &rootIDRaw})
	if err != nil {
		t.Fatalf("create active child: %v", err)
	}
	activeID := naming.IssueID(activeIDRaw)
	rootBranch := "riordan/" + rootID.String() + "/profile-and-worker-mater-cif-merge"
	activeBranch := "riordan/" + activeID.String() + "/reconcile"

	repoDir := t.TempDir()
	tasks, err := issuesClient.GetManyWithRuntime(ctx, projectID, []string{rootID.String(), closedID.String(), activeID.String()})
	if err != nil {
		t.Fatalf("load projected tasks: %v", err)
	}
	materializer := newProjectReadMaterializer(projectID, nil, nil)
	materializer.authority = NewProjectionDeltaAuthority(listErrorProjectionStore{list: func(context.Context, string, uint64, int) ([]domain.ProjectionDelta, uint64, error) {
		return nil, 0, nil
	}})
	for i := range tasks {
		task := tasks[i]
		switch task.ID {
		case rootID:
			task.HasWorktree = true
		case activeID:
			task.HasWorktree = true
			task.GitBehindCount = 1
		}
		materializer.canonical[task.ID.String()] = task
		materializer.tasks[task.ID.String()] = task
	}
	materializer.metadata.Health = "healthy"
	materializer.replaceWorktrees(map[string]git.Worktree{
		rootID.String():   {IssueID: rootID.String(), Path: repoDir, Branch: rootBranch},
		activeID.String(): {IssueID: activeID.String(), Path: repoDir, Branch: activeBranch},
	})
	gitCalls := 0
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		git: git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) {
			gitCalls++
			return "", errors.New("Git must not run from graph readiness")
		}}, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		materializersStarted: true,
		materializers:        map[string]*projectReadMaterializer{projectID: materializer},
	}
	d.taskGraphWorktrees = func(context.Context, string) ([]git.Worktree, error) {
		t.Fatal("graph readiness must consume projected worktrees")
		return nil, nil
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID.String())
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.ContainmentRisks) != 1 {
		t.Fatalf("containment risks = %+v, want one stale child risk", ready.ContainmentRisks)
	}
	risk := ready.ContainmentRisks[0]
	if risk.Classification != "stale_child_branch" || risk.IssueID != activeID.String() || risk.RootBranch != rootBranch {
		t.Fatalf("risk identity = %+v, want projected stale child risk for active %s", risk, activeID)
	}
	if risk.ClosedChildIssueID != "" || risk.EvidenceCommit != "" || risk.RootContainsEvidence || risk.ActiveContainsEvidence {
		t.Fatalf("risk = %+v, must not fabricate exact Git containment evidence", risk)
	}
	if !strings.Contains(risk.Message, "behind ancestor branch") || !strings.Contains(risk.Message, "mutation preflight") {
		t.Fatalf("risk message = %q, want projected authority wording", risk.Message)
	}
	if gitCalls != 0 {
		t.Fatalf("Git calls = %d, want zero from graph readiness", gitCalls)
	}
}

func runDaemonTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func runDaemonTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func TestTaskGraphReadinessDoesNotMisreportIncompleteChildAsCloseable(t *testing.T) {
	root := naming.IssueID("az-root")
	child := naming.IssueID("az-child")
	childParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: child, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &childParent},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if len(result.StaleCloseableChildren) != 0 {
		t.Fatalf("stale_closeable_children = %+v, want none", result.StaleCloseableChildren)
	}
	if !slices.Contains(result.Runnable, child.String()) {
		t.Fatalf("runnable = %+v, want incomplete child runnable", result.Runnable)
	}
}

func TestTaskGraphReadinessReportsNestedTrackerRootInsteadOfFlatteningDescendants(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-nested-tracker-readiness"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Outer root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	directID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Direct worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create direct worker: %v", err)
	}
	trackerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Nested tracker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create tracker: %v", err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Tracker child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &trackerID,
	})
	if err != nil {
		t.Fatalf("create tracker child: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if !slices.Contains(ready.Runnable, directID) {
		t.Fatalf("runnable = %+v, want direct worker %s", ready.Runnable, directID)
	}
	if slices.Contains(ready.Runnable, grandchildID) {
		t.Fatalf("runnable = %+v, did not expect nested child %s under outer root", ready.Runnable, grandchildID)
	}
	if len(ready.NestedRoots) != 1 {
		t.Fatalf("nested_roots = %+v, want one nested tracker", ready.NestedRoots)
	}
	nested := ready.NestedRoots[0]
	if nested.IssueID != trackerID || nested.Status != "startable" || nested.IssueStatus != string(domain.StatusOpen) || nested.Type != string(domain.TypeTask) || nested.ChildCount != 1 {
		t.Fatalf("nested root = %+v, want tracker %s", nested, trackerID)
	}
	if ready.Capacity.DirectRunnableCount != 1 || ready.Capacity.NestedStartableCount != 1 || ready.Capacity.TotalCountingCapacityCount != 0 {
		t.Fatalf("capacity = %+v, want direct runnable and nested startable outside active capacity", ready.Capacity)
	}
	if !strings.Contains(nested.Advice, "az orchestrator-session start --root "+trackerID) {
		t.Fatalf("nested advice = %q, want session start guidance", nested.Advice)
	}
	for _, observation := range ready.WorkerObservations {
		if observation.IssueID == grandchildID {
			t.Fatalf("worker observations included nested child under outer root: %+v", ready.WorkerObservations)
		}
	}
}

func TestTaskGraphReadinessMarksFailedNestedRootStartAsBlockedCapacity(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-nested-start-failed"
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Outer root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	nestedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Nested root",
		Type:     domain.TypeEpic,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create nested root: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Nested child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &nestedID,
	}); err != nil {
		t.Fatalf("create nested child: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		hub: publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{},
	}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, hub: d.hub, nextRevision: d.nextRevision})
	d.operationRuntime = runtime
	if _, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ID:           "op-nested-start",
		ProjectID:    projectID,
		IssueID:      nestedID,
		Kind:         daemonhandlers.CommandSessionStart,
		ResourceKeys: []string{"issue:" + projectID + ":" + nestedID},
	}, func(context.Context) ([]byte, error) {
		return nil, errors.New("worktree setup failed")
	}); err != nil {
		t.Fatalf("submit failed session start operation: %v", err)
	}
	_ = waitForRuntimeState(t, runtime, "op-nested-start", daemonops.StateFailed)

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.NestedRoots) != 1 {
		t.Fatalf("nested_roots = %+v, want one nested root", ready.NestedRoots)
	}
	nested := ready.NestedRoots[0]
	if nested.Status != "blocked_start_failed" || nested.StartFailure == nil || nested.StartFailure.OperationID != "op-nested-start" {
		t.Fatalf("nested root = %+v, want blocked_start_failed with operation", nested)
	}
	if nested.FallbackPolicy != "keep_children_blocked_or_create_replacement_direct_work" {
		t.Fatalf("fallback policy = %q", nested.FallbackPolicy)
	}
	if !strings.Contains(nested.Advice, "retry `az orchestrator-session start --root "+nestedID+"`") || !strings.Contains(nested.Advice, "replacement direct work") {
		t.Fatalf("advice = %q, want retry and replacement guidance", nested.Advice)
	}
	if ready.Capacity.BlockedNestedRootsCount != 1 || ready.Capacity.NestedStartableCount != 0 {
		t.Fatalf("capacity = %+v, want blocked nested root count", ready.Capacity)
	}

	if _, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ID:           "op-nested-start-retry",
		ProjectID:    projectID,
		IssueID:      nestedID,
		Kind:         daemonhandlers.CommandSessionStart,
		ResourceKeys: []string{"issue:" + projectID + ":" + nestedID},
	}, func(context.Context) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	}); err != nil {
		t.Fatalf("submit successful retry session start operation: %v", err)
	}
	_ = waitForRuntimeState(t, runtime, "op-nested-start-retry", daemonops.StateDone)
	ready, err = d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness after retry error: %v", err)
	}
	nested = ready.NestedRoots[0]
	if nested.Status != "startable" || nested.StartFailure != nil {
		t.Fatalf("nested root after successful retry = %+v, want startable without stale failure", nested)
	}
}

func TestTaskCompleteCheckIgnoresClosedNestedRootWithClosedDescendants(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-complete-closed-nested-root"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	trackerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Closed tracker", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create tracker: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Closed child", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &trackerID}); err != nil {
		t.Fatalf("create tracker child: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.NestedRoots) != 0 {
		t.Fatalf("nested_roots = %+v, want none for closed nested subtree", ready.NestedRoots)
	}
	result, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck error: %v", err)
	}
	if !result.Pass {
		t.Fatalf("complete check = %+v, want pass for closed nested subtree", result)
	}
}

func TestWorkerObservationStateDerivationPrecedence(t *testing.T) {
	rootID := "az-root"
	baseTask := func(id string, status domain.Status) domain.Task {
		return domain.Task{
			ID:       naming.IssueID(id),
			Type:     domain.TypeTask,
			Status:   status,
			Priority: domain.P2,
		}
	}

	tests := []struct {
		name string
		in   workerObservationInputs
		want domain.WorkerObservationState
	}{
		{
			name: "done with runtime cleanup pending wins over active",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task: func() domain.Task {
					task := baseTask("az-cleanup", domain.StatusDone)
					task.HasTmuxSession = true
					return task
				}(),
				Active: &taskGraphActiveSession{IssueID: "az-cleanup", Activity: "busy", Status: "active"},
			},
			want: domain.WorkerObservationCleanupPending,
		},
		{
			name: "plain done",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-done", domain.StatusDone),
			},
			want: domain.WorkerObservationDone,
		},
		{
			name: "cancelled with runtime cleanup pending wins over active",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task: func() domain.Task {
					task := baseTask("az-cancelled-cleanup", domain.StatusCancelled)
					task.HasTmuxSession = true
					return task
				}(),
				Active: &taskGraphActiveSession{IssueID: "az-cancelled-cleanup", Activity: "busy", Status: "active"},
			},
			want: domain.WorkerObservationCleanupPending,
		},
		{
			name: "plain cancelled is terminal done observation",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-cancelled", domain.StatusCancelled),
			},
			want: domain.WorkerObservationDone,
		},
		{
			name: "failed active session wins over review",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-failed", domain.StatusInReview),
				Active:      &taskGraphActiveSession{IssueID: "az-failed", Activity: "failed", Status: "active"},
			},
			want: domain.WorkerObservationFailed,
		},
		{
			name: "waiting active session",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-waiting", domain.StatusInProgress),
				Active:      &taskGraphActiveSession{IssueID: "az-waiting", Activity: "waiting", Status: "active"},
			},
			want: domain.WorkerObservationWaitingHuman,
		},
		{
			name: "working active session",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-working", domain.StatusInProgress),
				Active:      &taskGraphActiveSession{IssueID: "az-working", Activity: "busy", Status: "active"},
			},
			want: domain.WorkerObservationWorking,
		},
		{
			name: "review beats blocked",
			in: workerObservationInputs{
				RootIssueID:   rootID,
				Task:          baseTask("az-review", domain.StatusInReview),
				BlockedReason: "waiting on az-blocker",
			},
			want: domain.WorkerObservationReviewReady,
		},
		{
			name: "busy review handoff remains working",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-review-busy", domain.StatusInReview),
				Active:      &taskGraphActiveSession{IssueID: "az-review-busy", Activity: "busy", Status: "active"},
			},
			want: domain.WorkerObservationWorking,
		},
		{
			name: "waiting review handoff remains waiting",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-review-waiting", domain.StatusInReview),
				Active:      &taskGraphActiveSession{IssueID: "az-review-waiting", Activity: "waiting_tool", Status: "active"},
			},
			want: domain.WorkerObservationWaitingHuman,
		},
		{
			name: "idle review handoff is review ready",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-review-idle", domain.StatusInReview),
				Active:      &taskGraphActiveSession{IssueID: "az-review-idle", Activity: "idle", Status: "active"},
			},
			want: domain.WorkerObservationReviewReady,
		},
		{
			name: "blocked",
			in: workerObservationInputs{
				RootIssueID:   rootID,
				Task:          baseTask("az-blocked", domain.StatusOpen),
				BlockedReason: "waiting on az-blocker",
			},
			want: domain.WorkerObservationBlocked,
		},
		{
			name: "stale closeable",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-stale", domain.StatusOpen),
				Stale:       &taskStaleCloseableCandidate{IssueID: "az-stale", Evidence: []string{"clean worktree"}},
			},
			want: domain.WorkerObservationStale,
		},
		{
			name: "pending start",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-pending", domain.StatusOpen),
				Pending:     &taskGraphPendingStart{IssueID: "az-pending", OperationID: "op-1", OperationState: "queued"},
			},
			want: domain.WorkerObservationWorking,
		},
		{
			name: "runnable",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-runnable", domain.StatusOpen),
				Runnable:    true,
			},
			want: domain.WorkerObservationRunnable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := daemonWorkerObservationFromInputs(tt.in)
			if got.State != tt.want {
				t.Fatalf("state = %q, want %q (observation=%+v)", got.State, tt.want, got)
			}
			if got.SourceTruthPolicy.IssueGraph != string(daemonInvariantSourceProjection) ||
				got.SourceTruthPolicy.SessionRuntime != string(daemonInvariantSourceHybrid) ||
				got.SourceTruthPolicy.MailboxEvidence != string(daemonInvariantSourceProjection) {
				t.Fatalf("source policy = %+v", got.SourceTruthPolicy)
			}
			if got.Reason == "" && got.State != domain.WorkerObservationDone {
				t.Fatalf("reason missing for %+v", got)
			}
		})
	}
}

func TestLatestWorkerObservationIssueEventFiltersStructuralDependencies(t *testing.T) {
	blocking := domain.IssueObservationEvent{
		ID:         1,
		Type:       domain.IssueEventIssueDependencyAdded,
		ObservedAt: time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC),
		Source:     "issue-store",
		Payload:    map[string]any{"dependency_type": string(domain.DependencyBlocks)},
	}
	events := []domain.IssueObservationEvent{
		blocking,
		{
			ID:         2,
			Type:       domain.IssueEventIssueDependencyAdded,
			ObservedAt: blocking.ObservedAt.Add(time.Minute),
			Source:     "issue-store",
			Payload:    map[string]any{"dependency_type": string(domain.DependencyParentChild)},
		},
		{
			ID:         3,
			Type:       domain.IssueEventIssueDependencyAdded,
			ObservedAt: blocking.ObservedAt.Add(2 * time.Minute),
			Source:     "issue-store",
			Payload:    map[string]any{"dependency_type": string(domain.DependencyCreatedIn)},
		},
	}

	got := latestWorkerObservationIssueEvent(events)
	if got == nil || got.ID != blocking.ID {
		t.Fatalf("latestWorkerObservationIssueEvent() = %+v, want blocking dependency event", got)
	}
}

func TestTaskGraphReadinessWorkerObservationsIncludeEvidenceAndMissingProjectionStates(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-worker-observation"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	runnableID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Runnable worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create runnable: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	blockedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Blocked worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, blockedID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}
	reviewID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Review worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusInReview,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusDone,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create closed: %v", err)
	}

	if _, err := issuesClient.AppendIssueObservationEvent(ctx, reviewID, issues.IssueObservationEventParams{
		Type:       domain.IssueEventEvidenceSubmitted,
		ObservedAt: time.Date(2026, time.June, 17, 9, 0, 0, 0, time.UTC),
		Source:     "test",
		Payload:    map[string]any{"summary": "focused validation passed"},
	}); err != nil {
		t.Fatalf("append review event: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         7,
		ParentIssue: rootID,
		IssueID:     reviewID,
		Type:        "worker-integration-ready",
		Body:        "ready with commands and files changed",
		CreatedAt:   time.Date(2026, time.July, 6, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	closedSessionID := naming.CanonicalSessionID(projectID, closedID)
	if _, err := sessionStore.UpsertSession(projectID, closedSessionID, closedID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed closed session: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: sessionStore,
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	observations := map[string]domain.WorkerObservation{}
	for _, observation := range ready.WorkerObservations {
		observations[observation.IssueID] = observation
	}
	for _, wantID := range []string{runnableID, blockerID, blockedID, reviewID, closedID} {
		if _, ok := observations[wantID]; !ok {
			t.Fatalf("worker observations missing %s: %+v", wantID, ready.WorkerObservations)
		}
	}
	if got := observations[runnableID]; got.State != domain.WorkerObservationRunnable {
		t.Fatalf("runnable observation = %+v", got)
	}
	if got := observations[blockedID]; got.State != domain.WorkerObservationBlocked || !strings.Contains(got.Reason, blockerID) {
		t.Fatalf("blocked observation = %+v", got)
	}
	if got := observations[reviewID]; got.State != domain.WorkerObservationReviewReady || got.LastEvent == nil {
		t.Fatalf("review observation = %+v", got)
	}
	if got := strings.Join(observations[reviewID].EvidenceSummary, "\n"); !strings.Contains(got, "worker-integration-ready") || !strings.Contains(got, "evidence.submitted") {
		t.Fatalf("review evidence = %q", got)
	} else if strings.Contains(got, "issue.dependency_added") || strings.Contains(got, "parent-child") {
		t.Fatalf("review evidence includes structural dependency event: %q", got)
	}
	if got := observations[closedID]; got.State != domain.WorkerObservationCleanupPending {
		t.Fatalf("closed observation = %+v", got)
	}
}

func TestTaskGraphReadinessWorkerObservationsIncludeOpenNonEpicRootLeaf(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-root-leaf-observation"
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Ordinary open work",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create root task: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.WorkerObservations) != 1 {
		t.Fatalf("worker observations = %+v, want root leaf observation", ready.WorkerObservations)
	}
	observation := ready.WorkerObservations[0]
	if observation.IssueID != rootID || observation.State != domain.WorkerObservationRunnable {
		t.Fatalf("root observation = %+v", observation)
	}
	if !slices.Contains(ready.Runnable, rootID) {
		t.Fatalf("runnable = %+v, want root leaf", ready.Runnable)
	}
}

func TestTaskCompleteCheckReportsMixedStaleCloseableAndIncompleteChildren(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-complete-check-stale-closeable"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	staleID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Stale child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create stale child: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, staleID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "daemon-task-close",
		SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id":    projectID,
			"source_branch": "riordan/" + staleID + "/stale",
			"target_branch": "main",
			"source_oid":    "source-sha",
			"target_oid":    "target-sha",
		},
	}); err != nil {
		t.Fatalf("seed durable integration evidence: %v", err)
	}
	incompleteID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Incomplete child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create incomplete child: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   staleID,
		Path:      filepath.Join(repoDir, "wt-"+staleID),
		Branch:    "riordan/" + staleID + "/stale",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale child worktree projection: %v", err)
	}
	cleanStatus, err := json.Marshal(git.GitStatus{HasChanges: false, GitAheadCount: 0})
	if err != nil {
		t.Fatalf("marshal clean git status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, staleID, cleanStatus, time.Now().UTC()); err != nil {
		t.Fatalf("seed stale child git status: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.StaleCloseableChildren) != 1 || ready.StaleCloseableChildren[0].IssueID != staleID {
		t.Fatalf("readiness stale_closeable_children = %+v, want trusted receipt child %s", ready.StaleCloseableChildren, staleID)
	}

	result, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck error: %v", err)
	}
	if result.Pass {
		t.Fatalf("complete check unexpectedly passed: %+v", result)
	}
	if len(result.StaleCloseableChildren) != 1 || result.StaleCloseableChildren[0].IssueID != staleID {
		t.Fatalf("stale_closeable_children = %+v, want %s", result.StaleCloseableChildren, staleID)
	}
	if evidence := strings.Join(result.StaleCloseableChildren[0].Evidence, "\n"); !strings.Contains(evidence, "durable task.integration_completed event=") {
		t.Fatalf("stale closeable evidence = %q, want durable integration event", evidence)
	}
	reasons := strings.Join(result.Reasons, "\n")
	if !strings.Contains(reasons, "stale-closeable child candidates remain: "+staleID) {
		t.Fatalf("reasons = %+v, missing stale-closeable reason", result.Reasons)
	}
	if !strings.Contains(reasons, "required descendants not closed: "+incompleteID) || strings.Contains(reasons, "required descendants not closed: "+staleID) {
		t.Fatalf("reasons = %+v, want incomplete only in required descendants", result.Reasons)
	}
	advice := strings.Join(result.Advice, "\n")
	if !strings.Contains(advice, "az issue close --id "+rootID+" --close-clean-children") {
		t.Fatalf("advice = %+v, missing close-clean-children parent command", result.Advice)
	}
	if !strings.Contains(advice, "az orchestrate start --root "+rootID+" --issue "+incompleteID+" --json") {
		t.Fatalf("advice = %+v, missing runnable incomplete child start command", result.Advice)
	}
}

func TestTaskCompleteCheckTreatsReceiptBeforeLaterReopenAsIncomplete(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-complete-check-reopened-receipt"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Reopened child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "daemon-task-close",
		SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": projectID, "source_branch": "riordan/" + childID + "/reopened", "target_branch": "main",
			"source_oid": "completed-source", "target_oid": "completed-target",
		},
	}); err != nil {
		t.Fatalf("seed trusted integration receipt: %v", err)
	}
	if err := issuesClient.Update(ctx, childID, domain.StatusInProgress); err != nil {
		t.Fatalf("start new child lifecycle epoch: %v", err)
	}
	if err := issuesClient.Update(ctx, childID, domain.StatusOpen); err != nil {
		t.Fatalf("return child to open: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID, IssueID: childID, Path: filepath.Join(repoDir, "wt-"+childID), Branch: "riordan/" + childID + "/reopened", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed preserved worktree: %v", err)
	}
	cleanStatus, err := json.Marshal(git.GitStatus{HasChanges: false, GitAheadCount: 0})
	if err != nil {
		t.Fatalf("marshal clean status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, childID, cleanStatus, time.Now().UTC()); err != nil {
		t.Fatalf("seed clean worktree status: %v", err)
	}

	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: logger}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness: %v", err)
	}
	if !slices.Contains(ready.Runnable, childID) || len(ready.StaleCloseableChildren) != 0 {
		t.Fatalf("readiness = %+v, want reopened child runnable and not stale", ready)
	}
	result, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck: %v", err)
	}
	if len(result.StaleCloseableChildren) != 0 || !strings.Contains(strings.Join(result.Reasons, "\n"), "required descendants not closed: "+childID) {
		t.Fatalf("complete check = %+v, want reopened child incomplete rather than stale-closeable", result)
	}
}

func TestTaskCompleteCheckClassifiesInvestigationChildrenByAcceptance(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-complete-check-investigations"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	humanID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Human findings", Type: domain.TypeInvestigation, Status: domain.StatusInReview, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create human investigation: %v", err)
	}
	internalID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Internal review", Type: domain.TypeInvestigation, Status: domain.StatusInReview, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create internal investigation: %v", err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"outcome": "accepted", "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator}},
	} {
		if _, err := issuesClient.AppendIssueObservationEvent(ctx, internalID, event); err != nil {
			t.Fatalf("append internal review event: %v", err)
		}
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: logger}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, revision: map[string]uint64{projectID: 1}}
	result, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck: %v", err)
	}
	if len(result.StaleCloseableChildren) != 1 || result.StaleCloseableChildren[0].IssueID != internalID {
		t.Fatalf("stale closeable = %+v, want accepted internal review %s only", result.StaleCloseableChildren, internalID)
	}
	evidence := strings.Join(result.StaleCloseableChildren[0].Evidence, "\n")
	if !strings.Contains(evidence, "investigation disposition=internal_review") || !strings.Contains(evidence, "durable acceptance satisfied") {
		t.Fatalf("internal review evidence = %q", evidence)
	}
	reasons := strings.Join(result.Reasons, "\n")
	if !strings.Contains(reasons, "investigation "+humanID+" acceptance blocked") || strings.Contains(reasons, "investigation "+internalID+" acceptance blocked") {
		t.Fatalf("reasons = %q", reasons)
	}
}

func TestTaskIntegrationReadinessAcceptsLegacyMailboxAliases(t *testing.T) {
	tests := map[string]bool{
		"worker-integration-ready":       true,
		" worker-integration-ready ":     true,
		"WORKER-INTEGRATION-READY":       true,
		"worker-ready":                   true,
		" worker-ready ":                 true,
		"worker-complete":                true,
		" worker-complete ":              true,
		"worker-progress":                false,
		"worker-blocked":                 false,
		"dependency-ready":               false,
		"worker-integration-ready-later": false,
		"worker-ready-later":             false,
		"worker-completed":               false,
	}
	for eventType, want := range tests {
		t.Run(eventType, func(t *testing.T) {
			if got := daemonWorkerIntegrationReadyMailType(eventType); got != want {
				t.Fatalf("daemonWorkerIntegrationReadyMailType(%q) = %v, want %v", eventType, got, want)
			}
		})
	}
}

func TestTaskIntegrationReadinessRequiresCompleteWorkerEvidencePacket(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         1,
		ParentIssue: parentID,
		IssueID:     childID,
		Type:        "worker-integration-ready",
		Body: `{
			"schema": "worker_evidence.v1",
			"summary": "Ready for integration.",
			"commands_run": ["go test ./internal/daemon"],
			"key_assertions": ["integration readiness accepts complete worker evidence"],
			"files_changed": ["internal/daemon/task_commands.go"],
			"review": {"status": "clean", "findings": []},
			"risks": ["none"]
		}`,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if !result.Ready || result.EvidenceEventSeq != 1 || result.EvidencePacket == nil {
		t.Fatalf("result = %+v, want ready with evidence packet", result)
	}
	if result.EvidencePacket.Schema != domain.WorkerEvidenceSchemaV1 {
		t.Fatalf("evidence packet = %+v", result.EvidencePacket)
	}
}

func reviewValidationAcquire(requestID, token, projectID, issueID, revision string) domain.ValidationAcquire {
	return domain.ValidationAcquire{
		RequestID: requestID, LeaseToken: token, ProjectID: projectID, IssueID: issueID,
		Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence,
		IsolationMode: "repository-family", EnvironmentFingerprint: "test-toolchain", Override: domain.ValidationOverrideNone,
		Profile: "cold", Command: "just test", SourceRevision: revision, ReviewerID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator, ReviewEpochEventID: 1, PublicationOperationID: "publication", AcceptedReviewEventID: 2, AcceptedPublicationOperationID: "publication", TTL: time.Minute,
	}
}

func reviewValidationEvidence(requestID, revision string, overlap bool, external int) domain.ValidationEvidence {
	return domain.ValidationEvidence{
		Held: true, RequestID: requestID, Class: domain.ValidationClassAggregate,
		Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence,
		Profile: "cold", SourceRevision: revision, Present: true,
		OverlapDetected: overlap, ExternalGoProcesses: external,
	}
}

func TestTaskIntegrationReadinessTreatsPublicationOverlapAsDiagnostic(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-overlapped-aggregate"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	_, err = issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: map[string]any{
		"schema": domain.WorkerEvidenceSchemaV1, "summary": "ready", "commands_run": []string{"just test"}, "key_assertions": []string{"tests pass"}, "files_changed": []string{"justfile"}, "review": map[string]any{"status": "clean", "findings": []string{}}, "risks": []string{"none"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	now := time.Now().UTC()
	_, err = runtime.store.AcquireValidation(ctx, reviewValidationAcquire("aggregate-overlap", "test-secret", projectID, childID, "abc123"), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.store.FinishValidation(ctx, "aggregate-overlap", "test-secret", domain.ValidationRequestCompleted, "passed", reviewValidationEvidence("aggregate-overlap", "abc123", true, 2), now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, operationRuntime: runtime, revision: map[string]uint64{projectID: 1}}
	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.AggregateValidation == nil || strings.Contains(strings.Join(result.Reasons, "\n"), "overlapped") || !strings.Contains(strings.Join(result.Reasons, "\n"), "candidate worktree is required") {
		t.Fatalf("result = %+v, want overlap accepted before exact-worktree binding", result)
	}
}

func TestTaskIntegrationReadinessRejectsUnleasedAggregateEvidencePacket(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-unleased-aggregate"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	_, err = issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: map[string]any{
		"schema": domain.WorkerEvidenceSchemaV1, "summary": "ready", "commands_run": []string{"just test"}, "key_assertions": []string{"tests pass"}, "files_changed": []string{"justfile"}, "review": map[string]any{"status": "clean", "findings": []string{}}, "risks": []string{"none"},
		"aggregate_validation": map[string]any{"held": true, "request_id": "fabricated", "class": "aggregate", "profile": "cold", "source_revision": "abc123", "present": true, "overlap_detected": false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, operationRuntime: runtime, revision: map[string]uint64{projectID: 1}}
	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || !strings.Contains(strings.Join(result.Reasons, "\n"), "no aggregate validation is present in the daemon validation projection") {
		t.Fatalf("result = %+v, want rejected unleased aggregate evidence", result)
	}
}

func TestTaskIntegrationReadinessBindsAuthoritativeAggregateToExactCandidateRevision(t *testing.T) {
	for _, tc := range []struct {
		name              string
		candidateRevision string
		includeAggregate  bool
		dirtyCandidate    bool
		wantReady         bool
		wantReason        string
	}{
		{name: "authoritative aggregate need not be duplicated in packet", candidateRevision: "abc123", wantReady: true},
		{name: "candidate advanced after gate", candidateRevision: "def456", includeAggregate: true, wantReason: "does not match exact candidate revision"},
		{name: "candidate is dirty after gate", candidateRevision: "abc123", includeAggregate: true, dirtyCandidate: true, wantReason: "dirty candidate tree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			projectID := "proj-exact-" + strings.ReplaceAll(tc.name, " ", "-")
			repoDir := t.TempDir()
			issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview})
			if err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"schema": domain.WorkerEvidenceSchemaV1, "summary": "ready", "commands_run": []string{"just test"}, "key_assertions": []string{"tests pass"}, "files_changed": []string{"justfile"}, "review": map[string]any{"status": "clean", "findings": []string{}}, "risks": []string{"none"}}
			if tc.includeAggregate {
				payload["aggregate_validation"] = map[string]any{"held": true, "request_id": "aggregate", "class": "aggregate", "profile": "cold", "source_revision": "abc123", "present": true, "overlap_detected": false}
			}
			_, err = issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
			t.Cleanup(func() { _ = runtime.Close() })
			now := time.Now().UTC()
			_, err = runtime.store.AcquireValidation(ctx, reviewValidationAcquire("aggregate", "test-secret", projectID, childID, "abc123"), now)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.store.FinishValidation(ctx, "aggregate", "test-secret", domain.ValidationRequestCompleted, "passed", reviewValidationEvidence("aggregate", "abc123", false, 0), now.Add(time.Second), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			runner := &recordingGitRunner{runWithContextFn: func(_ context.Context, args ...string) (string, error) {
				if len(args) >= 4 && args[0] == "-C" && args[2] == "rev-parse" && args[3] == "--verify" {
					return tc.candidateRevision, nil
				}
				if len(args) >= 3 && args[0] == "-C" && args[2] == "status" {
					if tc.dirtyCandidate {
						return " M internal/daemon/task_commands.go\n", nil
					}
					return "", nil
				}
				return "", fmt.Errorf("unexpected git command: %v", args)
			}}
			d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, operationRuntime: runtime, git: git.NewClient(runner, slog.Default()), revision: map[string]uint64{projectID: 1}}
			result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantReady {
				if !result.Ready || result.EvidencePacket == nil || result.EvidenceSource != "issue_event" {
					t.Fatalf("result = %+v, want structurally complete issue evidence bound by authoritative aggregate", result)
				}
			} else if result.Ready || !strings.Contains(strings.Join(result.Reasons, "\n"), tc.wantReason) {
				t.Fatalf("result = %+v, want rejection containing %q", result, tc.wantReason)
			}
		})
	}
}

func TestTaskIntegrationReadinessReadsMailboxFromProjectRootForIssueWorktreeCandidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	bootstrapRoot := t.TempDir()
	projectRoot := t.TempDir()
	candidateWorktree := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: projectID, Name: "review-evidence-project", Path: projectRoot}}}); err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, projectRoot, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatal(err)
	}
	if err := appendMailboxEvent(projectRoot, daemonMailEvent{
		Seq: 1, ParentIssue: parentID, IssueID: childID, Type: "worker-integration-ready", CreatedAt: time.Now().UTC(),
		Body: `{"schema":"worker_evidence.v1","summary":"Ready from issue worktree.","commands_run":["just test"],"key_assertions":["authoritative project mailbox replayed"],"files_changed":["internal/daemon/task_commands.go"],"review":{"status":"clean","findings":[]},"risks":["none"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: bootstrapRoot, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, revision: map[string]uint64{projectID: 1}}
	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, candidateWorktree)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.EvidenceEventSeq != 1 || result.EvidencePacket == nil {
		t.Fatalf("result = %+v, want project-root mailbox evidence independent of candidate path %s", result, candidateWorktree)
	}
}

func TestTaskIntegrationReadinessRejectsUnknownProjectMailboxRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	projectID := "unknown-review-evidence-project"
	bootstrapRoot := t.TempDir()
	projectRoot := t.TempDir()
	candidateWorktree := t.TempDir()
	issuesClient := newMigratedIssueClient(t, projectRoot, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: bootstrapRoot, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, revision: map[string]uint64{projectID: 1}}
	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, candidateWorktree)
	if err == nil || !strings.Contains(err.Error(), "resolve authoritative project mailbox root") {
		t.Fatalf("result=%+v err=%v, want unknown project mailbox route rejected", result, err)
	}
}

func TestTaskIntegrationReadinessSkipsReviewReadyReplayNotification(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatal(err)
	}
	events := []daemonMailEvent{
		{
			Seq: 1, ParentIssue: parentID, IssueID: childID, Type: "worker-integration-ready", CreatedAt: time.Now().UTC(),
			Body: `{"schema":"worker_evidence.v1","summary":"Ready","commands_run":["go test ./internal/daemon"],"key_assertions":["replay notification cannot mask evidence"],"files_changed":["internal/daemon/task_commands.go"],"review":{"status":"clean","findings":[]},"risks":["none"]}`,
		},
		{
			Seq: 2, ParentIssue: parentID, IssueID: childID, Type: "worker-integration-ready", From: "daemon-observation-replay", Body: `{"summary":"issue is review-ready"}`, CreatedAt: time.Now().UTC(),
			Payload: map[string]interface{}{"publication": reviewReadyReplayPublication, "publication_key": "project:42"},
		},
	}
	for _, event := range events {
		if err := appendMailboxEvent(repoDir, event); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, revision: map[string]uint64{projectID: 1}}
	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.EvidenceEventSeq != 1 || result.EvidencePacket == nil {
		t.Fatalf("result = %+v, want earlier structured evidence selected", result)
	}
}

func TestTaskIntegrationReadinessAcceptsIssueRecordWorkerEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worker-issue-evidence-ready"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	event, err := issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{
		Type:   domain.IssueEventEvidenceSubmitted,
		Source: "az issue record",
		Payload: map[string]any{
			"schema":         "worker_evidence.v1",
			"summary":        "Ready for integration.",
			"commands_run":   []string{"go test ./internal/daemon"},
			"key_assertions": []string{"integration readiness accepts issue-recorded worker evidence"},
			"files_changed":  []string{"internal/daemon/task_commands.go"},
			"review": map[string]any{
				"status":   "clean",
				"findings": []string{},
			},
			"risks": []string{"none"},
		},
	})
	if err != nil {
		t.Fatalf("append issue evidence: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, "")
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if !result.Ready || result.EvidenceEventID != event.ID || result.EvidenceSource != "issue_event" || result.EvidencePacket == nil {
		t.Fatalf("result = %+v, want ready with issue evidence packet", result)
	}
}

func TestHandleTaskEventAppendPublishesTaskUpdate(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-event-append"
	repoDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "append event", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		hub: publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 3},
	}
	events, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	resp, err := d.handleTaskEventAppend(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("task-event-append-req"),
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.event.append",
		SentAt:          time.Now().UTC(),
		Body: mustJSON(t, map[string]any{
			"task_id":    taskID,
			"event_type": string(domain.IssueEventProgressRecorded),
			"payload": map[string]any{
				"summary": "recorded durable progress",
			},
		}),
	})
	if err != nil {
		t.Fatalf("handleTaskEventAppend error: %v", err)
	}
	if !resp.OK || resp.Revision != 4 {
		t.Fatalf("response = %+v, want ok revision 4", resp)
	}
	var body struct {
		Event domain.IssueObservationEvent `json:"event"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if body.Event.ID == 0 || body.Event.Type != domain.IssueEventProgressRecorded {
		t.Fatalf("event = %+v, want recorded progress event", body.Event)
	}

	select {
	case evt := <-events:
		if evt.Event != protocol.EventTaskUpdated || evt.Revision != 4 {
			t.Fatalf("published event = %+v, want task.updated revision 4", evt)
		}
		var taskBody protocol.TaskEventBody
		if err := json.Unmarshal(evt.Body, &taskBody); err != nil {
			t.Fatalf("unmarshal task event body: %v", err)
		}
		if taskBody.ProjectID.String() != projectID || taskBody.TaskID.String() != taskID {
			t.Fatalf("task event body = %+v, want project %s task %s", taskBody, projectID, taskID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task update event")
	}
}

func TestHandleTaskEventAppendCanonicalizesIntegrationReadyEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-event-worker-evidence"
	repoDir := t.TempDir()
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	taskID, err := client.Create(ctx, issues.CreateTaskParams{Title: "worker", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	packet := mustWorkerEvidencePayload(t)
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, hub: publish.NewHub(16, 8, slog.Default()), issueClientsByProject: map[string]*issues.Client{projectID: client}, revision: map[string]uint64{projectID: 1}}
	resp, err := d.handleTaskEventAppend(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: mustJSON(t, map[string]any{
		"task_id": taskID, "event_type": "worker-integration-ready", "payload": map[string]any{"worker_evidence": packet},
	})})
	if err != nil || !resp.OK {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	events, err := client.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{"worker-integration-ready"}})
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if _, nested := events[0].Payload["worker_evidence"]; nested {
		t.Fatalf("payload=%+v, want canonical direct storage", events[0].Payload)
	}
	if parsed, validation := domain.ParseWorkerEvidenceIssueEvent(events[0]); !validation.Complete || parsed.Schema != domain.WorkerEvidenceSchemaV1 {
		t.Fatalf("parsed=%+v validation=%+v", parsed, validation)
	}
}

func TestHandleTaskEventAppendRejectsIncompleteIntegrationReadyEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-event-worker-evidence-invalid"
	repoDir := t.TempDir()
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	taskID, err := client.Create(ctx, issues.CreateTaskParams{Title: "worker", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: client}}
	resp, err := d.handleTaskEventAppend(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: mustJSON(t, map[string]any{
		"task_id": taskID, "event_type": "worker-integration-ready", "payload": map[string]any{"schema": domain.WorkerEvidenceSchemaV1, "summary": "not complete"},
	})})
	if err != nil || resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "commands_run") {
		t.Fatalf("response=%+v err=%v, want packet diagnostics", resp, err)
	}
	events, err := client.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{"worker-integration-ready"}})
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%+v err=%v, want invalid readiness rejected before storage", events, err)
	}
}

func TestHandleTaskEventAppendRejectsCallerForgedAuthorityEvents(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-event-authority-spoof"
	repoDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "reject event spoof", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: logger}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}, revision: map[string]uint64{projectID: 1}}

	authorityEvents := []domain.IssueObservationEventType{domain.IssueEventIssueStatusChanged, domain.IssueEventReviewCompleted, domain.IssueEventReviewCloseFailed, domain.IssueEventTaskIntegrationCompleted, domain.IssueEventDecisionChanged, domain.IssueEventDecisionAcknowledged}
	for _, eventType := range authorityEvents {
		resp, err := d.command(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID("task-event-authority-spoof-" + string(eventType)),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         "task.event.append",
			Body: mustJSON(t, map[string]any{
				"task_id":        taskID,
				"event_type":     string(eventType),
				"source":         "daemon-decision",
				"source_command": protocol.CommandDecisionAcknowledge,
				"payload": map[string]any{
					"to_status": "in_review", "outcome": "integration_failed", "actor_id": "attacker",
					"decision_id": "dec-forged", "revision": int64(48), "disposition": domain.DecisionAcknowledgementCompatible,
				},
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest || !strings.Contains(resp.Error.Message, "authority-only") {
			t.Fatalf("event type %s response = %+v, want authority-only invalid request", eventType, resp)
		}
	}
	events, err := issuesClient.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{Types: authorityEvents})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("forged authority events persisted: %+v", events)
	}
}

func TestTaskIntegrationReadinessLatestIssueEvidenceEventWins(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worker-issue-evidence-latest"
	repoDir := t.TempDir()
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	observedAt := time.Now().UTC()
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{
		Type:       domain.IssueEventEvidenceSubmitted,
		ObservedAt: observedAt,
		Source:     "az issue record",
		Payload: map[string]any{
			"schema":         "worker_evidence.v1",
			"summary":        "Earlier complete packet.",
			"commands_run":   []string{"go test ./internal/daemon"},
			"key_assertions": []string{"older complete evidence should not hide latest malformed issue evidence"},
			"files_changed":  []string{"internal/daemon/task_commands.go"},
			"review": map[string]any{
				"status":   "clean",
				"findings": []string{},
			},
			"risks": []string{"none"},
		},
	}); err != nil {
		t.Fatalf("append complete issue evidence: %v", err)
	}
	latest, err := issuesClient.AppendIssueObservationEvent(ctx, childID, issues.IssueObservationEventParams{
		Type:       domain.IssueEventEvidenceSubmitted,
		ObservedAt: observedAt.Add(time.Second),
		Source:     "az issue record",
		Payload: map[string]any{
			"summary": "Latest evidence is not a worker_evidence.v1 packet.",
		},
	})
	if err != nil {
		t.Fatalf("append malformed issue evidence: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, "")
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if result.Ready || !result.EvidenceIncomplete || result.EvidenceEventID != latest.ID || result.EvidenceSource != "issue_event" {
		t.Fatalf("result = %+v, want latest malformed issue evidence to block readiness", result)
	}
	reasons := strings.Join(result.Reasons, "\n")
	if !strings.Contains(reasons, fmt.Sprintf("issue evidence event %d does not contain a structured worker_evidence.v1 packet", latest.ID)) {
		t.Fatalf("reasons = %+v, want malformed latest issue evidence reason", result.Reasons)
	}
}

func TestTaskIntegrationReadinessReportsIncompleteWorkerEvidencePacket(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         1,
		ParentIssue: parentID,
		IssueID:     childID,
		Type:        "worker-integration-ready",
		Body:        `{"schema":"worker_evidence.v1","summary":"Ready","review":{"status":"clean","findings":[]}}`,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if result.Ready || !result.EvidenceIncomplete || result.EvidenceEventSeq != 1 || result.EvidenceSource != "mailbox" {
		t.Fatalf("result = %+v, want incomplete evidence", result)
	}
	reasons := strings.Join(result.Reasons, "\n")
	for _, want := range []string{"worker evidence packet in mailbox event seq 1 is incomplete", "missing commands_run", "missing files_changed", "missing key_assertions", "missing risks"} {
		if !strings.Contains(reasons, want) {
			t.Fatalf("reasons = %+v, missing %q", result.Reasons, want)
		}
	}
}

func TestTaskIntegrationReadinessLatestWorkerEvidenceEventWins(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	events := []daemonMailEvent{
		{
			Seq:         1,
			ParentIssue: parentID,
			IssueID:     childID,
			Type:        "worker-integration-ready",
			Body: `{
				"schema": "worker_evidence.v1",
				"summary": "Earlier complete packet.",
				"commands_run": ["go test ./internal/daemon"],
				"key_assertions": ["older complete evidence should not hide latest incomplete evidence"],
				"files_changed": ["internal/daemon/task_commands.go"],
				"review": {"status": "clean", "findings": []},
				"risks": ["none"]
			}`,
			CreatedAt: time.Now().UTC(),
		},
		{
			Seq:         2,
			ParentIssue: parentID,
			IssueID:     childID,
			Type:        "worker-integration-ready",
			Body:        `{"schema":"worker_evidence.v1","summary":"Latest packet is incomplete","review":{"status":"clean","findings":[]}}`,
			CreatedAt:   time.Now().UTC(),
		},
	}
	for _, event := range events {
		if err := appendMailboxEvent(repoDir, event); err != nil {
			t.Fatalf("append mailbox event seq %d: %v", event.Seq, err)
		}
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if result.Ready || !result.EvidenceIncomplete || result.EvidenceEventSeq != 2 {
		t.Fatalf("result = %+v, want latest incomplete evidence at seq 2", result)
	}
}

func TestTaskIntegrationReadinessAcceptsLegacyAliasOnlyWithStructuredEvidence(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         1,
		ParentIssue: parentID,
		IssueID:     childID,
		Type:        "worker-complete",
		Body: `{
			"schema": "worker_evidence.v1",
			"summary": "Ready through a legacy alias.",
			"commands_run": ["go test ./internal/daemon"],
			"key_assertions": ["legacy aliases still require structured evidence"],
			"files_changed": ["internal/daemon/task_commands.go"],
			"review": {"status": "clean", "findings": []},
			"risks": ["none"]
		}`,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if !result.Ready || result.EvidencePacket == nil {
		t.Fatalf("result = %+v, want ready with structured legacy alias evidence", result)
	}
}

func TestTaskMergeBaseTargetSelectsNearestAncestorWorktree(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-merge-base-target"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "parent",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "child",
		Type:     domain.TypeTask,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	parentWorktree := filepath.Join(repoDir, "wt-parent")
	childWorktree := filepath.Join(repoDir, "wt-child")
	parentBranch := "riordan/" + parentID + "/parent"
	childBranch := "riordan/" + childID + "/child"
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: parentID, Path: parentWorktree, Branch: parentBranch, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: childID, Path: childWorktree, Branch: childBranch, UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + parentWorktree,
				"branch refs/heads/" + parentBranch,
				"",
				"worktree " + childWorktree,
				"branch refs/heads/" + childBranch,
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(projectID string) *git.WorktreeManager { return d.worktreeManagerForProject(projectID) },
		runtimeStateStore: store,
		logger:            slog.Default(),
		pollers:           map[string]context.CancelFunc{normalizedProjectID(projectID): func() {}},
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	got, err := d.taskMergeBaseTarget(ctx, projectID, childID, "main", false)
	if err != nil {
		t.Fatalf("taskMergeBaseTarget error: %v", err)
	}
	if got.TargetID != parentID || got.Branch != parentBranch || got.WorktreePath != parentWorktree || !got.BranchAttached {
		t.Fatalf("merge target = %+v, want parent %s branch %s worktree %s", got, parentID, parentBranch, parentWorktree)
	}
}

func TestTaskMergeBaseTargetDefaultUsesProjectBaseBranch(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configJSON := `{
		"$version": 8,
		"git": {
			"baseBranch": "main"
		}
	}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := New(Config{
		RepoDir:    repoDir,
		BaseBranch: "preview",
		Logger:     slog.Default(),
	})
	got, err := d.taskMergeBaseTarget(ctx, filepath.Base(repoDir), "", "", false)
	if err != nil {
		t.Fatalf("taskMergeBaseTarget error: %v", err)
	}
	if got.Branch != "main" {
		t.Fatalf("Branch = %q, want project base branch main", got.Branch)
	}
}

func TestDurableBaseIntegrationAcceptanceRequiresIssueScopedHumanInput(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj-base-acceptance"
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: issuesClient}}
	accepted, err := d.hasDurableBaseIntegrationAcceptance(ctx, projectID, issueID)
	if err != nil || accepted {
		t.Fatalf("acceptance before event = %v, %v; want false, nil", accepted, err)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type:    domain.IssueEventHumanInputProvided,
		Payload: map[string]any{"base_integration_accepted": true},
	}); err != nil {
		t.Fatalf("append acceptance: %v", err)
	}
	accepted, err = d.hasDurableBaseIntegrationAcceptance(ctx, projectID, issueID)
	if err != nil || !accepted {
		t.Fatalf("acceptance after event = %v, %v; want true, nil", accepted, err)
	}
}

func TestTaskMergeBaseTargetHandlerGatesExplicitBaseOnDurableHumanAcceptance(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-handler-base-acceptance"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return "worktree " + repoDir + "\nbranch refs/heads/main\n", nil
		}
		return "", fmt.Errorf("unexpected git args: %v", args)
	}}
	d := &Daemon{
		cfg:                       Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject:     map[string]*issues.Client{projectID: issuesClient},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: store},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(runner, repoDir, slog.Default())},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return d.worktreeManagerForProject(projectID) },
		runtimeStateStore: store,
		logger:            slog.Default(),
		pollers:           map[string]context.CancelFunc{normalizedProjectID(projectID): func() {}},
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	request := testRequest("task.merge_base_target", map[string]any{
		"task_id": issueID, "base_branch": "main", "require_human_acceptance": true,
	})
	request.Meta.ProjectID = naming.ProjectID(projectID)
	blocked, err := d.handleTaskMergeBaseTarget(ctx, request)
	if err != nil {
		t.Fatalf("blocked handler error: %v", err)
	}
	if blocked.OK || blocked.Error == nil || blocked.Error.Code != protocol.ErrorCodeInvalidRequest || !strings.Contains(blocked.Error.Message, "without durable human acceptance") {
		t.Fatalf("blocked response = %+v, want acceptance refusal", blocked)
	}
	if _, err := issuesClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventHumanInputProvided, Payload: map[string]any{"base_integration_accepted": true},
	}); err != nil {
		t.Fatalf("append acceptance: %v", err)
	}
	allowed, err := d.handleTaskMergeBaseTarget(ctx, request)
	if err != nil || !allowed.OK {
		t.Fatalf("allowed response = %+v, err = %v", allowed, err)
	}
	var result taskMergeBaseTargetResult
	if err := json.Unmarshal(allowed.Body, &result); err != nil {
		t.Fatalf("decode allowed response: %v", err)
	}
	if result.TargetID != "base" || result.Branch != "main" {
		t.Fatalf("allowed target = %+v, want base/main", result)
	}

	d.projectConfigMu.Lock()
	d.workflowModeByProject[projectID] = "origin"
	d.projectConfigMu.Unlock()
	originBlocked, err := d.handleTaskMergeBaseTarget(ctx, request)
	if err != nil {
		t.Fatalf("origin handler error: %v", err)
	}
	if originBlocked.OK || originBlocked.Error == nil || originBlocked.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("origin response = %+v, want direct base refusal", originBlocked)
	}
	for _, want := range []string{"workflow mode is origin", "git push -u origin HEAD", "az pr create --issue " + issueID, "az pr status --issue " + issueID, "az pr merge --issue " + issueID + " --confirm", "az ticket close --id " + issueID} {
		if !strings.Contains(originBlocked.Error.Message, want) {
			t.Fatalf("origin refusal %q missing %q", originBlocked.Error.Message, want)
		}
	}
}

func TestTaskGraphReadinessReportsPendingStartupAndCleanupTranscriptStates(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-graph-transcript"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	pendingID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Pending child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create pending child: %v", err)
	}
	startingID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Starting child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create starting child: %v", err)
	}
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed child",
		Type:     domain.TypeTask,
		Status:   domain.StatusDone,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create closed child: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: repoDir,
		logger:  logger,
		hub:     publish.NewHub(16, 8, logger),
	})
	t.Cleanup(func() { _ = runtime.Close() })
	submittedAt := time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC)
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID:  "op-session-start",
		ProjectID:    projectID,
		IssueID:      pendingID,
		Kind:         daemonhandlers.CommandSessionStart,
		DedupeKey:    "session.start:" + pendingID,
		ResourceKeys: []string{"issue:" + projectID + ":" + pendingID},
		State:        opstore.StateQueued,
		SubmittedAt:  submittedAt,
	}); err != nil {
		t.Fatalf("seed pending session.start operation: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	startingSessionID := naming.CanonicalSessionID(projectID, startingID)
	if _, err := sessionStore.UpsertSession(projectID, startingSessionID, startingID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	closedSessionID := naming.CanonicalSessionID(projectID, closedID)
	if _, err := sessionStore.UpsertSession(projectID, closedSessionID, closedID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed stale closed session: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore:     sessionStore,
		operationRuntime: runtime,
		revision:         map[string]uint64{projectID: 1},
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if slices.Contains(ready.Runnable, pendingID) {
		t.Fatalf("pending child still runnable: %+v", ready)
	}
	if len(ready.Pending) != 1 || ready.Pending[0].IssueID != pendingID || ready.Pending[0].OperationID != "op-session-start" || ready.Pending[0].OperationState != string(opstore.StateQueued) {
		t.Fatalf("pending = %+v", ready.Pending)
	}
	if len(ready.Active) != 1 || ready.Active[0] != startingID {
		t.Fatalf("active = %+v, want starting child only", ready.Active)
	}
	byID := map[string]taskGraphActiveSession{}
	for _, active := range ready.ActiveSessions {
		byID[active.IssueID] = active
	}
	if got := byID[startingID]; got.Status != "active" || got.Activity != "starting" || got.ActivitySource != "startup-grace" || got.Advice != "" {
		t.Fatalf("starting active session = %+v", got)
	}
	if got := byID[closedID]; got.Status != "cleanup-pending" || !strings.Contains(got.Advice, "cleanup is pending") {
		t.Fatalf("closed active session = %+v", got)
	}
}

func TestTaskGraphPendingSessionStartsCanonicalizesProjectID(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	canonicalProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: repoDir,
		logger:  logger,
		hub:     publish.NewHub(16, 8, logger),
	})
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID:  "op-session-start",
		ProjectID:    canonicalProjectID,
		IssueID:      "az-2",
		Kind:         daemonhandlers.CommandSessionStart,
		DedupeKey:    "session.start:az-2",
		ResourceKeys: []string{"issue:" + canonicalProjectID + ":az-2"},
		State:        opstore.StateQueued,
		SubmittedAt:  time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed pending session.start operation: %v", err)
	}
	d := &Daemon{
		cfg:              Config{RepoDir: repoDir, Logger: logger},
		operationRuntime: runtime,
	}

	pending, err := d.taskGraphPendingSessionStarts(ctx, protocol.DefaultProjectID)
	if err != nil {
		t.Fatalf("taskGraphPendingSessionStarts error: %v", err)
	}
	got, ok := pending["az-2"]
	if !ok || got.OperationID != "op-session-start" || got.OperationState != string(opstore.StateQueued) {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestTaskFollowOnMergeCandidatesAreDaemonOwned(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-follow-on-candidates"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent epic", Type: domain.TypeEpic})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := issuesClient.Update(ctx, parentID, domain.StatusInProgress); err != nil {
		t.Fatalf("update parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child task", Type: domain.TypeTask, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.Update(ctx, childID, domain.StatusInProgress); err != nil {
		t.Fatalf("update child: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Ready blocker", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := issuesClient.Update(ctx, blockerID, domain.StatusInProgress); err != nil {
		t.Fatalf("update blocker: %v", err)
	}
	openID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Open blocker", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create open blocker: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add ready blocker dependency: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, openID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add open blocker dependency: %v", err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: parentID, Path: filepath.Join(repoDir, "wt-parent"), Branch: "az/" + parentID, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: blockerID, Path: filepath.Join(repoDir, "wt-blocker"), Branch: "az/" + blockerID, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: openID, Path: filepath.Join(repoDir, "wt-open"), Branch: "az/" + openID, UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}

	got, err := d.taskFollowOnMergeCandidates(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("taskFollowOnMergeCandidates error: %v", err)
	}
	if got.MergeTargetToBase {
		t.Fatal("MergeTargetToBase = true, want false for child")
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2: %+v", len(got.Candidates), got.Candidates)
	}
	if got.Candidates[0].IssueID != parentID || got.Candidates[0].Relation != string(domain.DependencyParentChild) {
		t.Fatalf("first candidate = %+v, want parent", got.Candidates[0])
	}
	if got.Candidates[1].IssueID != blockerID || got.Candidates[1].Relation != string(domain.DependencyBlocks) {
		t.Fatalf("second candidate = %+v, want ready blocker", got.Candidates[1])
	}
	for _, candidate := range got.Candidates {
		if candidate.IssueID == openID {
			t.Fatalf("open blocker should not be eligible: %+v", got.Candidates)
		}
		if !candidate.HasWorktree {
			t.Fatalf("candidate missing worktree signal: %+v", candidate)
		}
	}
}

func TestTaskFollowOnMergeCandidatesAllowsTopLevelBaseFallback(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-follow-on-top"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	topID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Top task", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create top task: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}

	got, err := d.taskFollowOnMergeCandidates(ctx, projectID, topID)
	if err != nil {
		t.Fatalf("taskFollowOnMergeCandidates error: %v", err)
	}
	if !got.MergeTargetToBase {
		t.Fatal("MergeTargetToBase = false, want true for top-level task")
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", got.Candidates)
	}
}

func TestTaskDeleteRuntimeBlockersAreDaemonOwned(t *testing.T) {
	task := domain.Task{
		ID:             naming.IssueID("az-1"),
		HasTmuxSession: true,
		HasWorktree:    true,
	}
	blockers := daemonTaskDeleteRuntimeBlockers(task)
	if strings.Join(blockers, ",") != "session,worktree" {
		t.Fatalf("blockers = %v, want session/worktree", blockers)
	}
}

func TestTaskDeleteCommandDeletesThroughDaemon(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-delete"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete me",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskDeleteRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}
	var result taskDeleteResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal delete response: %v", err)
	}
	if result.TaskID != taskID || !result.Deleted || result.Revision == 0 {
		t.Fatalf("delete result = %+v", result)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID.String() == taskID {
			t.Fatalf("task %s still present after delete", taskID)
		}
	}
}

func TestTaskDeleteCommandRejectsParentWithUndeletedChildren(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-delete-parent-children"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Parent",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, "parent-child"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskDeleteRequest{TaskID: parentID})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-parent-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskDelete response = %+v, want guard error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
	if !strings.Contains(resp.Error.Message, "cannot delete issue "+parentID) || !strings.Contains(resp.Error.Message, "1 undeleted descendant") {
		t.Fatalf("error message = %q", resp.Error.Message)
	}
	tasks, err := issuesClient.Search(ctx, parentID)
	if err != nil {
		t.Fatalf("search parent: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("parent search len = %d, want 1", len(tasks))
	}
}

func TestTaskArchiveCommandRejectsParentWithUndeletedChildren(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-archive-parent-children"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Parent",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, "parent-child"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(struct {
		TaskID string `json:"task_id"`
	}{TaskID: parentID})
	if err != nil {
		t.Fatalf("marshal archive request: %v", err)
	}
	resp, err := d.handleTaskArchive(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-archive-parent-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.archive",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskArchive error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskArchive response = %+v, want guard error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
	if !strings.Contains(resp.Error.Message, "cannot archive issue "+parentID) || !strings.Contains(resp.Error.Message, "1 undeleted descendant") {
		t.Fatalf("error message = %q", resp.Error.Message)
	}
	tasks, err := issuesClient.Search(ctx, parentID)
	if err != nil {
		t.Fatalf("search parent: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("parent search len = %d, want 1", len(tasks))
	}
}

func TestTaskDeleteCommandRejectsParentWithChildrenBeforeRuntimeCleanup(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-parent-children-runtime"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Parent with runtime",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, "parent-child"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+parentID)
	branchName := "riordan/" + parentID + "/parent-runtime"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   parentID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "delete-parent-cleanup-marker")
	worktreeDeleteCalled := false
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			worktreeDeleteCalled = true
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("touch %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         parentID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-parent-runtime-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskDelete response = %+v, want guard error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
	if _, err := os.Stat(cleanupMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup marker stat err = %v, want not exist", err)
	}
	if worktreeDeleteCalled {
		t.Fatal("worktree delete called before live-child guard")
	}
}

func TestTaskDeleteRunsIssueResourceCleanupWithoutSessionBeforeWorktreeRemoval(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-resource-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete with resource cleanup",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	branchName := "riordan/" + taskID + "/resource-cleanup"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "delete-cleanup-marker")
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			if _, err := os.Stat(cleanupMarker); err != nil {
				return "", fmt.Errorf("cleanup marker missing before remove: %w", err)
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" > %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskDeleteRequest{TaskID: taskID, RemoveWorktree: true})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	want := taskID + "|" + worktreePath + "|" + branchName
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID.String() == taskID {
			t.Fatalf("task %s still present after delete", taskID)
		}
	}
	if !strings.Contains(strings.Join(commands, "\n"), "worktree remove "+worktreePath) {
		t.Fatalf("commands = %v, want worktree remove", commands)
	}
}

func TestTaskDeleteCleanupRepairsStaleMissingWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-stale-runtime"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete stale runtime",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := issuesClient.UpdateWithRuntime(ctx, projectID, taskID, domain.StatusInProgress); err != nil {
		t.Fatalf("mark task in progress: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}
	staleWorktreePath := filepath.Join(repoDir, "missing-"+taskID)
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      staleWorktreePath,
		Branch:    "riordan/" + taskID + "/stale-runtime",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		StopSession:    true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-stale-runtime",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}

	tasks, err := issuesClient.Search(ctx, taskID)
	if err != nil {
		t.Fatalf("search task: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("task %s still present after cleanup delete", taskID)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, projectID, taskID); err != nil {
		t.Fatalf("get worktree projection: %v", err)
	} else if ok {
		t.Fatalf("stale worktree projection still present for %s", taskID)
	}
	session, ok, err := runtimeStore.GetSessionStateByIssueID(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get session projection: %v", err)
	}
	if !ok {
		t.Fatalf("session projection missing for %s, want stopped repair marker", taskID)
	}
	if session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("session projection = %+v, want desired/observed stopped", session)
	}
}

func TestTaskDeleteCleanupRepairsStaleLegacyProjectWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-current-runtime"
	legacyProjectID := "proj-task-delete-legacy-runtime"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete stale legacy runtime",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	staleWorktreePath := filepath.Join(repoDir, "missing-legacy-"+taskID)
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      staleWorktreePath,
		Branch:    "riordan/" + taskID + "/legacy-stale-runtime",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-stale-legacy-runtime",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}

	tasks, err := issuesClient.Search(ctx, taskID)
	if err != nil {
		t.Fatalf("search task: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("task %s still present after cleanup delete", taskID)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if ok {
		t.Fatalf("stale legacy worktree projection still present for %s", taskID)
	}
}

func TestTaskDeleteCleanupRepairsLegacyProjectWorktreeProjectionMissingFromGitList(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-current-git-list"
	legacyProjectID := "proj-task-delete-legacy-git-list"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete stale legacy runtime absent from git list",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	staleWorktreePath := filepath.Join(repoDir, "existing-but-unlisted-"+taskID)
	if err := os.MkdirAll(staleWorktreePath, 0o755); err != nil {
		t.Fatalf("mkdir stale worktree path: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      staleWorktreePath,
		Branch:    "riordan/" + taskID + "/legacy-unlisted-runtime",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-stale-legacy-runtime-git-list",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}

	tasks, err := issuesClient.Search(ctx, taskID)
	if err != nil {
		t.Fatalf("search task: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("task %s still present after cleanup delete", taskID)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if ok {
		t.Fatalf("git-unlisted legacy worktree projection still present for %s", taskID)
	}
}

func TestTaskDeleteCleanupBlocksLiveLegacyProjectWorktreeProjectionFromGitList(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-current-live-git-list"
	legacyProjectID := "proj-task-delete-legacy-live-git-list"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete live legacy runtime still in git list",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	liveWorktreePath := filepath.Join(repoDir, "existing-live-"+taskID)
	if err := os.MkdirAll(liveWorktreePath, 0o755); err != nil {
		t.Fatalf("mkdir live worktree path: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      liveWorktreePath,
		Branch:    "feature/unparseable",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\nworktree %s\nbranch refs/heads/feature/unparseable\n\n", repoDir, liveWorktreePath), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-live-legacy-runtime-git-list",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "active runtime projection aliases remain") {
		t.Fatalf("handleTaskDelete response = %+v, want live alias blocker", resp)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if !ok {
		t.Fatalf("live legacy worktree projection was removed for %s", taskID)
	}
}

func TestTaskDeleteRunsIssueResourceCleanupWithoutRuntimeAttachments(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-resource-cleanup-root"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete root cleanup",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	cleanupMarker := filepath.Join(repoDir, "delete-root-cleanup-marker")
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" \"$(pwd)\" > %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskDeleteRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-root-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatalf("eval root symlink: %v", err)
	}
	want := taskID + "|||" + wantRoot
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID.String() == taskID {
			t.Fatalf("task %s still present after delete", taskID)
		}
	}
}

func TestTaskUpdateStatusRejectsInReviewWithUnreadyChildren(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-children"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "all live descendants must be terminal") || !strings.Contains(resp.Error.Message, childID+" (in_progress)") {
		t.Fatalf("task.update_status response = %+v, want terminal descendant guard", resp)
	}
	parent, err := issuesClient.GetWithRuntime(ctx, projectID, parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Status != domain.StatusOpen {
		t.Fatalf("parent status = %s, want open", parent.Status)
	}
}

func TestTaskUpdateStatusRejectsInReviewWithBusyReviewChild(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-busy-review-child"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Busy review child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInReview,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	seedReviewGuardSessionProjection(t, d, projectID, childID, daemonstate.Session{
		ID:             naming.CanonicalSessionID(projectID, childID),
		IssueID:        childID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	})

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "all live descendants must be terminal") || !strings.Contains(resp.Error.Message, childID+" (in_progress)") {
		t.Fatalf("task.update_status response = %+v, want terminal descendant guard", resp)
	}
	parent, err := issuesClient.GetWithRuntime(ctx, projectID, parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Status != domain.StatusOpen {
		t.Fatalf("parent status = %s, want open", parent.Status)
	}
}

func TestTaskUpdateStatusRejectsInReviewWithBusyActivity(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-busy"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Busy handoff",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	seedReviewGuardSessionProjection(t, d, projectID, taskID, daemonstate.Session{
		ID:             naming.CanonicalSessionID(projectID, taskID),
		IssueID:        taskID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	})

	resp := updateStatusForTest(t, d, projectID, taskID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "session activity is busy (source: hooks)") || !strings.Contains(resp.Error.Message, "leave it in_progress") {
		t.Fatalf("task.update_status response = %+v, want busy activity guard", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != domain.StatusInProgress {
		t.Fatalf("task status = %s, want in_progress", task.Status)
	}
}

func TestTaskUpdateStatusRejectsInReviewWithWaitingActivity(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-waiting"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Waiting handoff",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	seedReviewGuardSessionProjection(t, d, projectID, taskID, daemonstate.Session{
		ID:             naming.CanonicalSessionID(projectID, taskID),
		IssueID:        taskID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "waiting_tool",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	})

	resp := updateStatusForTest(t, d, projectID, taskID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "session activity is waiting_tool (source: hooks)") || !strings.Contains(resp.Error.Message, "leave it in_progress") {
		t.Fatalf("task.update_status response = %+v, want waiting activity guard", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != domain.StatusInProgress {
		t.Fatalf("task status = %s, want in_progress", task.Status)
	}
}

func TestTaskUpdateStatusAllowsInReviewWithBusyActivityForActiveIssueSelfHandoff(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-busy-self"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Busy self handoff",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	seedReviewGuardSessionProjection(t, d, projectID, taskID, daemonstate.Session{
		ID:             naming.CanonicalSessionID(projectID, taskID),
		IssueID:        taskID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	})

	resp := updateStatusForTestWithActiveIssue(t, d, projectID, taskID, domain.StatusInReview, false, taskID)
	if !resp.OK || resp.Error != nil {
		t.Fatalf("task.update_status response = %+v, want active issue self-handoff success", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status = %s, want in_review", task.Status)
	}
}

func TestTaskUpdateStatusAllowsInReviewWithIdleActivity(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-idle"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Idle handoff",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	seedReviewGuardSessionProjection(t, d, projectID, taskID, daemonstate.Session{
		ID:             naming.CanonicalSessionID(projectID, taskID),
		IssueID:        taskID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "idle",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	})

	resp := updateStatusForTest(t, d, projectID, taskID, domain.StatusInReview, false)
	if !resp.OK || resp.Error != nil {
		t.Fatalf("task.update_status response = %+v, want success", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status = %s, want in_review", task.Status)
	}
}

func TestTaskUpdateStatusCascadeChildrenCannotBypassTerminalDescendantGate(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-cascade-children"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Grandchild",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &childID,
	})
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, true)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "all live descendants must be terminal") {
		t.Fatalf("task.update_status response = %+v, want terminal descendant rejection", resp)
	}
	for _, id := range []string{parentID, childID, grandchildID} {
		task, err := issuesClient.GetWithRuntime(ctx, projectID, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if task.Status == domain.StatusInReview {
			t.Fatalf("%s status = %s, want unchanged non-review state", id, task.Status)
		}
	}
}

func TestTaskUpdateStatusCascadeChildrenRejectsBusyChildActivity(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-cascade-busy-child"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Busy child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	seedReviewGuardSessionProjection(t, d, projectID, childID, daemonstate.Session{
		ID:             naming.CanonicalSessionID(projectID, childID),
		IssueID:        childID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "working",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	})

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, true)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "all live descendants must be terminal") {
		t.Fatalf("task.update_status response = %+v, want terminal descendant guard", resp)
	}
	for _, id := range []string{parentID, childID} {
		task, err := issuesClient.GetWithRuntime(ctx, projectID, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if task.Status == domain.StatusInReview {
			t.Fatalf("%s status = %s, want not in_review", id, task.Status)
		}
	}
}

func TestTaskUpdateStatusReviewGuardHonorsLegacyParentChildDependency(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-legacy-edge"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, string(domain.DependencyParentChild)); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, childID+" (open)") {
		t.Fatalf("task.update_status response = %+v, want legacy child guard", resp)
	}
}

func TestTaskUpdateStatusRejectsInReviewWhenChildOnlyReviewReady(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-ready"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	for _, status := range []domain.Status{domain.StatusInReview, domain.StatusDone} {
		if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
			Title:    "Child",
			Type:     domain.TypeTask,
			Status:   status,
			ParentID: &parentID,
		}); err != nil {
			t.Fatalf("create child %s: %v", status, err)
		}
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "all live descendants must be terminal") {
		t.Fatalf("task.update_status response = %+v, want review-ready child rejection", resp)
	}
	parent, err := issuesClient.GetWithRuntime(ctx, projectID, parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Status == domain.StatusInReview {
		t.Fatalf("parent status = %s, want unchanged", parent.Status)
	}
}

func TestTaskUpdateStatusRejectsCascadeChildrenForNonReviewStatus(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-cascade-non-review"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Task", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, taskID, domain.StatusInProgress, true)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "cascade_children is only supported with status in_review") {
		t.Fatalf("task.update_status response = %+v, want cascade non-review rejection", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != domain.StatusOpen {
		t.Fatalf("task status = %s, want open", task.Status)
	}
}

func newTaskStatusReviewGuardDaemon(t *testing.T, projectID string) (*Daemon, *issues.Client) {
	t.Helper()
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	return &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, slog.Default()),
	}, issuesClient
}

func seedReviewGuardSessionProjection(t *testing.T, d *Daemon, projectID, taskID string, session daemonstate.Session) {
	t.Helper()
	if session.ID == "" {
		session.ID = naming.CanonicalSessionID(projectID, taskID)
	}
	if session.IssueID == "" {
		session.IssueID = taskID
	}
	if session.State == "" {
		session.State = daemonstate.SessionStateRunning
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = time.Now().UTC()
	}
	store := d.sessionRuntimeStateStore(projectID)
	if store == nil {
		t.Fatal("missing runtime store")
	}
	if err := upsertSessionStateFixture(store, context.Background(), projectID, session); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}
}

func updateStatusForTest(t *testing.T, d *Daemon, projectID, taskID string, status domain.Status, cascadeChildren bool) protocol.ResponseEnvelope {
	t.Helper()
	return updateStatusForTestWithActiveIssue(t, d, projectID, taskID, status, cascadeChildren, "")
}

func updateStatusForTestWithActiveIssue(t *testing.T, d *Daemon, projectID, taskID string, status domain.Status, cascadeChildren bool, activeIssue string) protocol.ResponseEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"task_id":          taskID,
		"status":           status,
		"cascade_children": cascadeChildren,
	})
	if err != nil {
		t.Fatalf("marshal status request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-review-guard-" + taskID),
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID:         naming.ProjectID(projectID),
			ClientActiveIssue: activeIssue,
		},
		Command: "task.update_status",
		Body:    body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	return resp
}

func assertNextTaskUpdatedEvent(t *testing.T, events <-chan protocol.EventEnvelope, taskID string, status domain.Status) {
	t.Helper()
	select {
	case evt := <-events:
		if evt.Event != protocol.EventTaskUpdated {
			t.Fatalf("event = %s, want %s", evt.Event, protocol.EventTaskUpdated)
		}
		var body protocol.TaskEventBody
		if err := json.Unmarshal(evt.Body, &body); err != nil {
			t.Fatalf("unmarshal task event body: %v", err)
		}
		if body.TaskID.String() != taskID || body.Task == nil || body.Task.Status != status {
			t.Fatalf("task event body = %+v, want %s -> %s", body, taskID, status)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s event", protocol.EventTaskUpdated)
	}
}

func TestTaskListSnapshotFreshnessMarksStaleProjection(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 11, 5, 0, 0, time.UTC)
	}

	ctx := context.Background()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	checkedAt := time.Date(2026, time.April, 2, 11, 4, 0, 0, time.UTC)
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: "proj-stale",
		IssueID:   "az-1",
		Path:      "/tmp/proj-stale-az-1",
		UpdatedAt: checkedAt,
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: slog.Default()},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": store,
		},
	}
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, "proj-stale")
	if !lastCheckedAt.Equal(checkedAt) {
		t.Fatalf("lastCheckedAt = %v, want %v", lastCheckedAt, checkedAt)
	}
	if freshness != protocol.TaskListFreshnessStale {
		t.Fatalf("freshness = %q, want %q", freshness, protocol.TaskListFreshnessStale)
	}
}

func TestHandleTaskGetManyReturnsBatchDependencyContextWithPartialMiss(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	}

	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := newMigratedIssueClientAtPath(t, issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many"
	firstID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "First",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create first issue: %v", err)
	}
	secondID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Second",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create second issue: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, secondID, firstID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	firstSessionID := naming.CanonicalSessionID(projectID, firstID)
	secondSessionID := naming.CanonicalSessionID(projectID, secondID)
	staleUpdatedAt := time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		name      string
		sessionID string
		issueID   string
	}{
		{name: "requested", sessionID: secondSessionID, issueID: secondID},
		{name: "context", sessionID: firstSessionID, issueID: firstID},
	} {
		if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
			ID:        row.sessionID,
			IssueID:   row.issueID,
			State:     daemonstate.SessionStateRunning,
			UpdatedAt: staleUpdatedAt,
		}); err != nil {
			t.Fatalf("seed %s session state: %v", row.name, err)
		}
		if _, _, err := runtimeStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
			ProjectID: projectID, SessionID: row.sessionID, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: staleUpdatedAt,
		}); err != nil {
			t.Fatalf("seed %s stale physical observation: %v", row.name, err)
		}
	}
	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			firstSessionID:  true,
			secondSessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}

	const initialRevision uint64 = 9
	d := &Daemon{
		cfg:  Config{RepoDir: ".", Logger: logger},
		tmux: tmux.NewClient(tmuxRunner, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore:           daemonstate.NewStore(),
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		revision:               map[string]uint64{projectID: initialRevision},
	}
	if err := d.observeTmuxProject(ctx, projectID, newTmuxRuntimeLiveness([]tmux.SessionInfo{{Name: firstSessionID}, {Name: secondSessionID}}, nil), domain.CurrentTmuxObservationProvenance(timeNow())); err != nil {
		t.Fatalf("apply asynchronous tmux observation: %v", err)
	}

	body, err := json.Marshal(map[string][]string{
		"task_ids": []string{secondID, "az-missing", firstID},
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}
	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	if got, want := payload.SnapshotRevision, initialRevision+2; got != want {
		t.Fatalf("snapshot revision = %d, want %d", got, want)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	if _, ok := taskByID["az-missing"]; ok {
		t.Fatalf("missing issue appeared in payload: %+v", payload.Tasks)
	}
	second := taskByID[secondID]
	if got, want := len(second.Dependencies), 1; got != want {
		t.Fatalf("second dependencies = %+v, want one", second.Dependencies)
	}
	if second.Dependencies[0].ID.String() != firstID {
		t.Fatalf("second dependency id = %q, want %q", second.Dependencies[0].ID, firstID)
	}
	if _, ok := taskByID[firstID]; !ok {
		t.Fatalf("dependency context missing first issue: %+v", payload.Tasks)
	}
	for _, row := range []struct {
		name      string
		sessionID string
	}{
		{name: "requested", sessionID: secondSessionID},
		{name: "context", sessionID: firstSessionID},
	} {
		session, found, err := runtimeStore.GetSessionState(ctx, projectID, row.sessionID)
		if err != nil {
			t.Fatalf("load %s session state: %v", row.name, err)
		}
		if !found {
			t.Fatalf("%s session state missing", row.name)
		}
		if session.ObservedState != daemonstate.SessionStateRunning {
			t.Fatalf("%s observed state = %s, want running", row.name, session.ObservedState)
		}
		if !session.UpdatedAt.After(staleUpdatedAt) {
			t.Fatalf("%s updated_at = %s, want after %s", row.name, session.UpdatedAt, staleUpdatedAt)
		}
	}
}

func TestRefreshWorktreeRuntimeStateForIssuesDoesNotPublishUnchangedGitStatus(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	const (
		projectID = "proj-refresh-issue"
		issueID   = "az-1"
		worktree  = "/tmp/proj-refresh-issue-az-1"
		branch    = "riordan/az-1/refresh"
	)
	status := cleanGitStatus()
	rawStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 2, 11, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	runner := &recordingGitRunner{
		runFn: func(args ...string) (string, error) {
			switch {
			case len(args) == 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
				return "worktree " + worktree + "\nbranch refs/heads/" + branch + "\n\n", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
				return "", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "symbolic-ref":
				return "origin/main\n", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
				return "merge-base-sha\n", nil
			case len(args) >= 7 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
				return "", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "rev-list":
				return "0\n", nil
			default:
				t.Fatalf("unexpected git args: %v", args)
				return "", nil
			}
		},
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", BaseBranch: "main", Logger: logger},
		hub: publish.NewHub(16, 8, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, ".", logger),
		},
		git: git.NewClient(runner, logger),
	}

	ch, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	if _, err := d.refreshWorktreeRuntimeStateForIssues(ctx, projectID, []string{issueID}); err != nil {
		t.Fatalf("refreshWorktreeRuntimeStateForIssues: %v", err)
	}
	cancel()
	for evt := range ch {
		if evt.Event == protocol.EventGitStatusUpdated {
			t.Fatalf("unexpected unchanged git status event: %+v", evt)
		}
	}
}

func TestRefreshExactReviewWorktreeGitFactsConvergesStaleDirtyAndStaleCleanBoundedly(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	const projectID = "proj-finite-refresh"
	cleanID, dirtyID := "az-clean", "az-dirty"
	cleanPath, dirtyPath := t.TempDir(), t.TempDir()
	cleanBranch, dirtyBranch := "riordan/az-clean/work", "riordan/az-dirty/work"

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	for _, fixture := range []struct {
		issueID, path, branch string
		status                git.GitStatus
	}{
		{cleanID, cleanPath, cleanBranch, git.GitStatus{HasChanges: true, Modified: []string{"stale.go"}}},
		{dirtyID, dirtyPath, dirtyBranch, git.GitStatus{}},
	} {
		if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: fixture.issueID, Path: fixture.path, Branch: fixture.branch, UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(fixture.status)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertWorktreeStateGitStatus(ctx, projectID, fixture.issueID, raw, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) == 3 && args[0] == "worktree" && args[1] == "list":
			return "worktree " + cleanPath + "\nbranch refs/heads/" + cleanBranch + "\n\nworktree " + dirtyPath + "\nbranch refs/heads/" + dirtyBranch + "\n\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			if args[1] == dirtyPath {
				return " M live.go\n", nil
			}
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "symbolic-ref":
			return "origin/main\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "merge-base":
			return "merge-base-sha\n", nil
		case len(args) >= 7 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "rev-list":
			return "0\n", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}
	d := &Daemon{
		cfg:                       Config{RepoDir: ".", BaseBranch: "main", Logger: logger},
		hub:                       publish.NewHub(16, 8, logger),
		runtimeStoresByRoot:       map[string]*daemonstate.RuntimeStateStore{".": store},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(runner, ".", logger)},
		git:                       git.NewClient(runner, logger),
	}

	assertConverged := func(issueID string, wantDirty bool) {
		t.Helper()
		row, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, issueID)
		if err != nil || !found {
			t.Fatalf("load %s projection: found=%v err=%v", issueID, found, err)
		}
		var status git.GitStatus
		if err := json.Unmarshal(row.GitStatusRaw, &status); err != nil {
			t.Fatal(err)
		}
		if status.HasChanges != wantDirty {
			t.Fatalf("%s dirty = %v, want %v", issueID, status.HasChanges, wantDirty)
		}
	}

	initialRevision := d.currentRevision(projectID)
	if err := d.refreshExactReviewWorktreeGitFacts(ctx, projectID, []string{cleanID, dirtyID}); err != nil {
		t.Fatal(err)
	}
	assertConverged(cleanID, false)
	assertConverged(dirtyID, true)
	firstRevision := d.currentRevision(projectID)
	if got := firstRevision - initialRevision; got != 2 {
		t.Fatalf("first refresh revision delta = %d, want one changed status update per worktree", got)
	}

	if err := d.refreshExactReviewWorktreeGitFacts(ctx, projectID, []string{cleanID, dirtyID}); err != nil {
		t.Fatal(err)
	}
	if got := d.currentRevision(projectID) - firstRevision; got != 0 {
		t.Fatalf("unchanged finite refresh revision delta = %d, want no duplicate projection updates", got)
	}

	// A fresh daemon instance must derive the same facts from Git rather than
	// treating either persisted direction as authoritative after restart.
	restarted := &Daemon{
		cfg:                       d.cfg,
		hub:                       publish.NewHub(16, 8, logger),
		runtimeStoresByRoot:       map[string]*daemonstate.RuntimeStateStore{".": store},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(runner, ".", logger)},
		git:                       git.NewClient(runner, logger),
	}
	if err := restarted.refreshExactReviewWorktreeGitFacts(ctx, projectID, []string{cleanID, dirtyID}); err != nil {
		t.Fatal(err)
	}
	assertConverged(cleanID, false)
	assertConverged(dirtyID, true)
	if got := restarted.currentRevision(projectID); got != 0 {
		t.Fatalf("restart refresh revision = %d, want no inverted or duplicate updates", got)
	}
}

func TestTaskListSnapshotFreshnessRefreshesSessionCacheBeforeEvaluation(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 11, 5, 0, 0, time.UTC)
	}

	ctx := context.Background()
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })

	const (
		projectID = "proj-fresh"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	durableUpdatedAt := time.Date(2026, time.April, 2, 11, 4, 45, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: durableUpdatedAt,
	}); err != nil {
		t.Fatalf("seed durable session projection: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	sessionStore.ReplaceProjectSessions(projectID, []daemonstate.Session{{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC),
	}})

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: slog.Default()},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStore,
		},
		sessionStore: sessionStore,
	}

	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	if !lastCheckedAt.Equal(durableUpdatedAt) {
		t.Fatalf("lastCheckedAt = %v, want %v from durable projection", lastCheckedAt, durableUpdatedAt)
	}
	if freshness != protocol.TaskListFreshnessFresh {
		t.Fatalf("freshness = %q, want %q", freshness, protocol.TaskListFreshnessFresh)
	}
}

func TestHandleTaskListDoesNotPersistSessionProjectionSnapshot(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "task list no projection writes",
		Type:  domain.TypeTask,
	}); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	projectID := "proj-tasklist-no-write"
	sessionID := naming.CanonicalSessionID(projectID, "az-1")
	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			sessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-no-write",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	}
	resp, err := d.handleTaskList(ctx, req)
	if err != nil {
		t.Fatalf("handleTaskList returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response not OK: %+v", resp)
	}

	rows, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("projection rows = %d, want 0 (task.list read path must not persist)", len(rows))
	}
}

func TestHandleTaskGetManyIncludesAncestorContextWhenRequested(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many-ancestors"
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create root issue: %v", err)
	}
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 11},
	}
	body, err := json.Marshal(map[string]any{
		"task_ids":          []string{childID},
		"include_ancestors": true,
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}

	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many-ancestors",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	for _, issueID := range []string{childID, parentID, rootID} {
		if _, ok := taskByID[issueID]; !ok {
			t.Fatalf("task %s missing from ancestor-context payload: %+v", issueID, payload.Tasks)
		}
	}
}

func TestHandleTaskGetManyCanExcludeDependents(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many-no-dependents"
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 12},
	}
	body, err := json.Marshal(map[string]any{
		"task_ids":           []string{parentID},
		"exclude_dependents": true,
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}

	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many-no-dependents",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	if _, ok := taskByID[parentID]; !ok {
		t.Fatalf("parent task missing from payload: %+v", payload.Tasks)
	}
	if _, ok := taskByID[childID]; ok {
		t.Fatalf("dependent child task should be omitted from payload: %+v", payload.Tasks)
	}
}

func TestHandleTaskGetManyDirectDependentsOnlyUsesParentChildEdges(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many-direct-dependents"
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create blocker issue: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, blockerID, parentID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 13},
	}
	body, err := json.Marshal(map[string]any{
		"task_ids":          []string{parentID},
		"direct_dependents": true,
		"include_ancestors": true,
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}

	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many-direct-dependents",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	if _, ok := taskByID[parentID]; !ok {
		t.Fatalf("parent task missing from payload: %+v", payload.Tasks)
	}
	if _, ok := taskByID[childID]; !ok {
		t.Fatalf("child task missing from payload: %+v", payload.Tasks)
	}
	if _, ok := taskByID[blockerID]; ok {
		t.Fatalf("non-parent-child dependent should be omitted from payload: %+v", payload.Tasks)
	}
}

func TestHandleTaskGetManyMetadataOnlyPreservesContextShape(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many-metadata-only"
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "Root",
		Description: "root details should stay out of metadata-only payload",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create root issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "Child",
		Description: "child details should stay out of metadata-only payload",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
		ParentID:    &rootID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	relatedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Related dependency",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create related issue: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, relatedID, string(domain.DependencyRelatedTo)); err != nil {
		t.Fatalf("add related dependency: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 12},
	}
	body, err := json.Marshal(map[string]any{
		"task_ids":           []string{childID},
		"include_ancestors":  true,
		"exclude_dependents": true,
		"metadata_only":      true,
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}

	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many-metadata-only",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	for _, issueID := range []string{childID, rootID} {
		if _, ok := taskByID[issueID]; !ok {
			t.Fatalf("task %s missing from metadata-only payload: %+v", issueID, payload.Tasks)
		}
	}
	if _, ok := taskByID[relatedID]; ok {
		t.Fatalf("metadata-only payload included unrelated dependency %s: %+v", relatedID, payload.Tasks)
	}
	if taskByID[childID].Description != "" {
		t.Fatalf("metadata-only child description = %q, want empty", taskByID[childID].Description)
	}
	if taskByID[childID].ParentID == nil || taskByID[childID].ParentID.String() != rootID {
		t.Fatalf("metadata-only child parent = %+v, want %s", taskByID[childID].ParentID, rootID)
	}
}

func TestHandleTaskSnapshotExportUsesProjectionSessions(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "snapshot export projection sessions",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	projectID := "proj-snapshot-export"
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	// Keep tmux empty; export should still include the projected session.
	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	resp, err := d.handleTaskSnapshotExport(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-snapshot-export",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.snapshot.export",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleTaskSnapshotExport error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.snapshot.export response not OK: %+v", resp.Error)
	}

	var out taskSnapshotExportBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal snapshot export body: %v", err)
	}
	if out.ProjectID != projectID {
		t.Fatalf("project id = %q, want %q", out.ProjectID, projectID)
	}
	if out.TaskCount != 1 {
		t.Fatalf("task count = %d, want 1", out.TaskCount)
	}
	if out.SessionCount != 1 {
		t.Fatalf("session count = %d, want 1", out.SessionCount)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].Name != sessionID {
		t.Fatalf("sessions = %+v, want [%s]", out.Sessions, sessionID)
	}
	if len(out.Tasks) != 1 || !out.Tasks[0].SessionAttached {
		t.Fatalf("tasks = %+v, expected exported task with SessionAttached=true", out.Tasks)
	}
}

func TestHandleTaskReadsDoNotEnqueueWorktreeRefresh(t *testing.T) {
	ctx := context.Background()
	projectID := protocol.DefaultProjectID
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "target issue",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create target issue: %v", err)
	}
	otherID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "other issue",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create other issue: %v", err)
	}

	targetWorktree := filepath.Join(repoDir, "target-worktree")
	otherWorktree := filepath.Join(repoDir, "other-worktree")
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: targetID, Path: targetWorktree, Branch: "az/" + targetID, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: otherID, Path: otherWorktree, Branch: "az/" + otherID, UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain" {
			statusCalls++
			return " M changed.go\n", nil
		}
		return "", nil
	}}
	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "test_git_status_refresh",
		Workers: 1,
		Logger:  slog.Default(),
	})
	t.Cleanup(func() { _ = queue.Close() })
	gitAdapter := &gitServiceAdapter{
		client:             git.NewClient(runner, slog.Default()),
		runtimeStateStore:  store,
		statusRefreshQueue: queue,
		logger:             slog.Default(),
		baseBranch:         "main",
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return store
		},
	}
	d := &Daemon{
		cfg:              Config{BaseBranch: "main", Logger: slog.Default()},
		issues:           issuesClient,
		gitStatusAdapter: gitAdapter,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}
	reads := []struct {
		name    string
		command string
		body    any
		handle  func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	}{
		{name: "get", command: "task.get", body: map[string]string{"task_id": targetID}, handle: d.handleTaskGet},
		{name: "get many", command: "task.get_many", body: map[string]any{"task_ids": []string{targetID}}, handle: d.handleTaskGetMany},
	}
	for _, read := range reads {
		t.Run(read.name, func(t *testing.T) {
			reqBody, err := json.Marshal(read.body)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := read.handle(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-task-read-projection-only",
				Kind:            protocol.EnvelopeKindCommand,
				Command:         read.command,
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Body:            reqBody,
			})
			if err != nil || !resp.OK {
				t.Fatalf("%s response = %+v, err = %v", read.command, resp.Error, err)
			}
			payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if len(payload.Tasks) == 0 || payload.Tasks[0].ID.String() != targetID {
				t.Fatalf("%s tasks = %+v, want target %s", read.command, payload.Tasks, targetID)
			}
		})
	}
	if counters := queue.snapshotCounters(); counters.Enqueued != 0 {
		t.Fatalf("task reads enqueued worktree refresh: %+v", counters)
	}
	if statusCalls != 0 {
		t.Fatalf("task reads invoked git status %d times", statusCalls)
	}
}

func TestHandleTaskGetMaterializedReadDoesNotRefreshRuntimeOrGit(t *testing.T) {
	ctx := context.Background()
	const (
		projectID = "proj-detail-read"
	)
	issuesClient, repoDir := newTestIssueClient(t)
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "Durable detail",
		Description: "Return without external Git",
		Status:      domain.StatusInProgress,
		Type:        domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create durable detail: %v", err)
	}
	worktree := filepath.Join(repoDir, "worktrees", issueID)
	if err := os.WriteFile(filepath.Join(repoDir, ".azedarach", "config.json"), []byte(`{
		"publicationEvidence": {"policyVersion":"portable-v1","activePathProfiles":["consumer-integration"]}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    "az/az-1",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree state: %v", err)
	}

	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{Name: "detail_read_git_status", Workers: 1, Logger: slog.Default()})
	t.Cleanup(func() { _ = queue.Close() })
	gitAdapter := &gitServiceAdapter{
		client:             git.NewClient(&recordingGitRunner{}, slog.Default()),
		runtimeStateStore:  store,
		statusRefreshQueue: queue,
		logger:             slog.Default(),
		baseBranch:         "main",
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return store
		},
	}
	hydrateCalls := 0
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		hydrateCalls++
		return tasks, nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap materializer: %v", err)
	}
	hydrateCalls = 0
	operationRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = operationRuntime.Close() })
	evidence := domain.PublicationEvidence{
		EvidenceID: "merge-detail", ProjectID: projectID, IssueID: issueID, Layer: domain.PublicationEvidenceMergeResult,
		SourceRevision: "source", BaseRevision: "base", ResultRevision: "result", Producer: "reviewer",
		PolicyVersion: "portable-v1", EnvironmentFingerprint: "env", CreatedAt: time.Unix(1, 0).UTC(),
	}
	if _, err := operationRuntime.store.RecordPublicationEvidence(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	beforeEvidence, err := operationRuntime.store.PublicationEvidenceSnapshot(ctx, projectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	var publicationGitCalls atomic.Int32
	d := &Daemon{
		cfg:              Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		operationRuntime: operationRuntime,
		git: git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) {
			publicationGitCalls.Add(1)
			return "", errors.New("task.get publication diagnostic must not invoke Git")
		}}, slog.Default()),
		gitStatusAdapter: gitAdapter,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		materializersStarted: true,
		materializers: map[string]*projectReadMaterializer{
			projectID: materializer,
		},
		revision: map[string]uint64{projectID: 7},
	}
	body, err := json.Marshal(map[string]string{"task_id": issueID})
	if err != nil {
		t.Fatalf("marshal task get: %v", err)
	}
	resp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-detail-projection-only",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.get",
		Meta:            protocol.Metadata{ProjectID: projectID},
		Body:            body,
	})
	if err != nil || !resp.OK {
		t.Fatalf("task.get response = %+v, err = %v", resp.Error, err)
	}
	if counters := queue.snapshotCounters(); counters.Enqueued != 0 {
		t.Fatalf("task.get enqueued external Git refreshes: %+v", counters)
	}
	if hydrateCalls != 0 {
		t.Fatalf("task.get synchronously refreshed runtime enrichment %d times, want projection-only read", hydrateCalls)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get response: %v", err)
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].Description != "Return without external Git" {
		t.Fatalf("task.get payload = %+v, want durable detail", payload.Tasks)
	}
	if payload.Tasks[0].PublicationEvidence == nil || payload.Tasks[0].PublicationEvidence.State != "recorded" {
		t.Fatalf("task.get publication diagnostic = %+v, want recorded projection", payload.Tasks[0].PublicationEvidence)
	}
	if got := publicationGitCalls.Load(); got != 0 {
		t.Fatalf("task.get publication diagnostic Git calls = %d, want 0", got)
	}
	afterEvidence, err := operationRuntime.store.PublicationEvidenceSnapshot(ctx, projectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterEvidence, beforeEvidence) {
		t.Fatalf("task.get wrote publication evidence: before=%+v after=%+v", beforeEvidence, afterEvidence)
	}
}

func TestHandleTaskListMaterializedReadDoesNotRefreshRuntime(t *testing.T) {
	ctx := context.Background()
	const projectID = "proj-list-projection-only"
	issuesClient, _ := newTestIssueClient(t)
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "durable list row", Status: domain.StatusInProgress, Type: domain.TypeTask,
	}); err != nil {
		t.Fatal(err)
	}

	hydrateCalls := 0
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		hydrateCalls++
		return tasks, nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	hydrateCalls = 0
	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		materializersStarted:  true,
		materializers:         map[string]*projectReadMaterializer{projectID: materializer},
		revision:              map[string]uint64{projectID: 1},
	}
	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list-projection-only",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            []byte(`{}`),
	})
	if err != nil || !resp.OK {
		t.Fatalf("task.list response = %+v, err = %v", resp.Error, err)
	}
	if hydrateCalls != 0 {
		t.Fatalf("task.list synchronously refreshed runtime enrichment %d times, want projection-only read", hydrateCalls)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].Title != "durable list row" {
		t.Fatalf("task.list tasks = %+v, want durable projected row", payload.Tasks)
	}
}

func TestHandleBoardFetchMaterializedReadBypassesLegacyRuntimeCache(t *testing.T) {
	ctx := context.Background()
	const projectID = "proj-board-projection-only"
	issuesClient, _ := newTestIssueClient(t)
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "canonical board row", Status: domain.StatusInProgress, Type: domain.TypeTask,
	})
	if err != nil {
		t.Fatal(err)
	}

	hydrateCalls := 0
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		hydrateCalls++
		return tasks, nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	hydrateCalls = 0
	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
		materializersStarted:  true,
		materializers:         map[string]*projectReadMaterializer{projectID: materializer},
		revision:              map[string]uint64{projectID: 1},
	}
	d.storeTaskListSnapshotCache(projectID, 1, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{{
		ID: naming.IssueID(issueID), Title: "legacy cached row", Status: domain.StatusOpen, Type: domain.TypeTask,
	}}, false)

	resp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-board-projection-only",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "board.fetch",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil || !resp.OK {
		t.Fatalf("board.fetch response = %+v, err = %v", resp.Error, err)
	}
	if hydrateCalls != 0 {
		t.Fatalf("board.fetch synchronously refreshed runtime enrichment %d times, want projection-only read", hydrateCalls)
	}
	payload, err := protocol.DecodeBoardSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tasks := payload.Projection.TaskSummaries()
	if len(tasks) != 1 || tasks[0].Title != "canonical board row" {
		t.Fatalf("board.fetch tasks = %+v, want canonical materialized row", tasks)
	}
}

func TestOrdinaryTaskReadsBypassBlockedRuntimeAndUserProjection(t *testing.T) {
	for _, command := range []string{"task.list", "task.get"} {
		for _, blockedPhase := range []string{"runtime hydration", "user projection sync"} {
			t.Run(command+"/"+blockedPhase, func(t *testing.T) {
				ctx := context.Background()
				projectID := "proj-blocked-read-" + strings.NewReplacer(".", "-", " ", "-").Replace(command+"-"+blockedPhase)
				issuesClient, _ := newTestIssueClient(t)
				issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
					Title: "projected while writers are blocked", Status: domain.StatusInProgress, Type: domain.TypeTask,
				})
				if err != nil {
					t.Fatal(err)
				}

				var blockRuntime atomic.Bool
				runtimeEntered, releaseRuntime := make(chan struct{}, 1), make(chan struct{})
				materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(issuesClient), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
					if blockRuntime.Load() {
						runtimeEntered <- struct{}{}
						<-releaseRuntime
					}
					return tasks, nil
				})
				if err := materializer.bootstrap(ctx); err != nil {
					t.Fatal(err)
				}
				userSyncEntered, releaseUserSync := make(chan struct{}, 1), make(chan struct{})
				d := &Daemon{
					cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
					issueClientsByProject: map[string]*issues.Client{projectID: issuesClient},
					materializersStarted:  true,
					materializers:         map[string]*projectReadMaterializer{projectID: materializer},
					projectReadUserProjectionSync: func(context.Context, string, []string) error {
						userSyncEntered <- struct{}{}
						<-releaseUserSync
						return nil
					},
					revision: map[string]uint64{projectID: 1},
				}
				if blockedPhase == "runtime hydration" {
					blockRuntime.Store(true)
				}
				body := []byte(`{}`)
				if command == "task.get" {
					body, err = json.Marshal(map[string]string{"task_id": issueID})
					if err != nil {
						t.Fatal(err)
					}
				}
				type readResult struct {
					resp protocol.ResponseEnvelope
					err  error
				}
				result := make(chan readResult, 1)
				go func() {
					req := protocol.RequestEnvelope{
						ProtocolVersion: protocol.CurrentVersion, RequestID: "req-blocked-projection-read",
						Kind: protocol.EnvelopeKindCommand, Command: command,
						Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body,
					}
					var resp protocol.ResponseEnvelope
					var callErr error
					if command == "task.list" {
						resp, callErr = d.handleTaskList(ctx, req)
					} else {
						resp, callErr = d.handleTaskGet(ctx, req)
					}
					result <- readResult{resp: resp, err: callErr}
				}()

				var got readResult
				if blockedPhase == "runtime hydration" {
					select {
					case got = <-result:
					case <-runtimeEntered:
						close(releaseRuntime)
						<-result
						t.Fatal("ordinary read entered blocked runtime hydration")
					}
				} else {
					select {
					case got = <-result:
					case <-userSyncEntered:
						close(releaseUserSync)
						<-result
						t.Fatal("ordinary read entered blocked user projection sync")
					}
				}
				if got.err != nil || !got.resp.OK {
					t.Fatalf("%s response = %+v, err = %v", command, got.resp.Error, got.err)
				}
			})
		}
	}
}

func TestHandleTaskGetManyMaterializedReadDoesNotInvokeGit(t *testing.T) {
	ctx := context.Background()
	const projectID = "portable-go-consumer"
	issueID := naming.IssueID("go-1")
	materializer := newProjectReadMaterializer(projectID, nil, nil)
	materializer.authority = NewProjectionDeltaAuthority(listErrorProjectionStore{
		list: func(_ context.Context, _ string, after uint64, _ int) ([]domain.ProjectionDelta, uint64, error) {
			return nil, after, nil
		},
	})
	for i := 0; i < 800; i++ {
		id := naming.IssueID(fmt.Sprintf("go-%d", i+1))
		task := domain.Task{ID: id, Title: "portable durable task " + id.String(), Status: domain.StatusOpen, Type: domain.TypeTask}
		materializer.canonical[id.String()] = task
		materializer.tasks[id.String()] = task
	}
	materializer.metadata.Health = "healthy"

	gitCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		gitCalls++
		return "", errors.New("Git must not run from task.get_many")
	}}
	d := &Daemon{
		cfg:                  Config{Logger: slog.Default()},
		git:                  git.NewClient(runner, slog.Default()),
		materializersStarted: true,
		materializers:        map[string]*projectReadMaterializer{projectID: materializer},
		revision:             map[string]uint64{projectID: 4},
	}
	body, err := json.Marshal(map[string]any{"task_ids": []string{issueID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-projection-only-get-many",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.get_many",
		Meta:            protocol.Metadata{ProjectID: projectID},
		Body:            body,
	})
	if err != nil || !resp.OK {
		t.Fatalf("task.get_many response = %+v, err = %v", resp.Error, err)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].Title != "portable durable task go-1" {
		t.Fatalf("tasks = %+v, want durable portable task", payload.Tasks)
	}
	if gitCalls != 0 {
		t.Fatalf("Git calls = %d, want 0", gitCalls)
	}
}

func TestHandleTaskGetRefreshesOnlyRequestedIssueSession(t *testing.T) {
	ctx := context.Background()
	projectID := "azedarach"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	_, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "schema seed",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create schema seed issue: %v", err)
	}

	targetID := "bra"
	contextID := "brb"
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range []struct {
		id    string
		title string
	}{
		{id: targetID, title: "target issue"},
		{id: contextID, title: "context issue"},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO issues (
				id, title, description, status, disposition, engagement, visibility, lifecycle_state, closed_outcome, review_state, priority, issue_type,
				created_at, updated_at, closed_at, assignee, labels_json,
				implementations_json, design, notes, acceptance, estimate, deleted_at
			)
			VALUES (?, ?, NULL, ?, 'ready', 'idle', 'live', 'open', 'none', 'none', ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)
		`, row.id, row.title, string(domain.StatusOpen), int(domain.P3), string(domain.TypeTask), now, now); err != nil {
			t.Fatalf("insert issue %s: %v", row.id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, ?, NULL)
	`, contextID, targetID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("insert context dependency: %v", err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	targetSessionID := naming.CanonicalSessionID(projectID, targetID)
	contextSessionID := naming.CanonicalSessionID(projectID, contextID)
	targetUpdatedAt := time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		name      string
		sessionID string
		issueID   string
	}{
		{name: "target", sessionID: targetSessionID, issueID: targetID},
		{name: "context", sessionID: contextSessionID, issueID: contextID},
	} {
		if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
			ID:            row.sessionID,
			IssueID:       row.issueID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateStopped,
			UpdatedAt:     targetUpdatedAt,
		}); err != nil {
			t.Fatalf("seed %s session state: %v", row.name, err)
		}
	}

	otherUpdatedAt := time.Date(2026, time.April, 2, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		issueID := fmt.Sprintf("other-%02d", i)
		if _, err := db.ExecContext(ctx, `INSERT INTO issues(id,title,status,disposition,engagement,visibility,lifecycle_state,closed_outcome,review_state,priority,issue_type,created_at,updated_at) VALUES(?,?,?,'ready','idle','live','open','none','none',?,?,?,?)`, issueID, issueID, string(domain.StatusOpen), int(domain.P3), string(domain.TypeTask), now, now); err != nil {
			t.Fatalf("seed unrelated issue %s: %v", issueID, err)
		}
		if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
			ID:            naming.CanonicalSessionID(projectID, issueID),
			IssueID:       issueID,
			State:         daemonstate.SessionStateStopped,
			ObservedState: daemonstate.SessionStateStopped,
			UpdatedAt:     otherUpdatedAt,
		}); err != nil {
			t.Fatalf("seed other session state %d: %v", i, err)
		}
	}

	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			targetSessionID:  true,
			contextSessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg:    Config{Logger: slog.Default()},
		issues: issuesClient,
		tmux:   tmux.NewClient(tmuxRunner, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}
	if err := d.observeTmuxProject(ctx, projectID, newTmuxRuntimeLiveness([]tmux.SessionInfo{{Name: targetSessionID}, {Name: contextSessionID}}, nil), domain.CurrentTmuxObservationProvenance(time.Now().UTC())); err != nil {
		t.Fatalf("apply asynchronous tmux observation: %v", err)
	}

	reqBody, err := json.Marshal(map[string]string{"task_id": targetID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	resp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-session-refresh",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.get",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            reqBody,
	})
	if err != nil {
		t.Fatalf("handleTaskGet returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get response not OK: %+v", resp.Error)
	}

	for _, row := range []struct {
		name      string
		sessionID string
	}{
		{name: "target", sessionID: targetSessionID},
		{name: "context", sessionID: contextSessionID},
	} {
		session, found, err := store.GetSessionState(ctx, projectID, row.sessionID)
		if err != nil {
			t.Fatalf("load %s session state: %v", row.name, err)
		}
		if !found {
			t.Fatalf("%s session state missing", row.name)
		}
		if session.ObservedState != daemonstate.SessionStateRunning {
			t.Fatalf("%s observed state = %s, want running", row.name, session.ObservedState)
		}
		if !session.UpdatedAt.After(targetUpdatedAt) {
			t.Fatalf("%s updated_at = %s, want after %s", row.name, session.UpdatedAt, targetUpdatedAt)
		}
	}

	sessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list session states: %v", err)
	}
	for _, session := range sessions {
		if session.ID == targetSessionID || session.ID == contextSessionID {
			continue
		}
		if !session.UpdatedAt.Equal(otherUpdatedAt) {
			t.Fatalf("unrelated session %s updated_at = %s, want unchanged %s", session.ID, session.UpdatedAt, otherUpdatedAt)
		}
	}
}

func TestRefreshWorktreeRuntimeStatePersistsGitMetricsFromWorktreeList(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-worktrees"
	worktreePath := "/tmp/repo-az-1"
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return "worktree /tmp/repo-root\nbranch refs/heads/main\n\nworktree /tmp/repo-az-1\nbranch refs/heads/az/az-1\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktreePath && args[2] == "status" && args[3] == "--porcelain":
			return " M changed.go\n?? new.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktreePath && args[2] == "merge-base" && args[3] == "main" && args[4] == "HEAD":
			return "abc123\n", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == worktreePath && args[2] == "diff" && args[3] == "--shortstat" && args[4] == "abc123" && args[5] == "HEAD":
			return " 2 files changed, 7 insertions(+), 3 deletions(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktreePath && args[2] == "rev-list" && args[3] == "--count" && args[4] == "HEAD..main":
			return "5\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktreePath && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main..HEAD":
			return "2\n", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	d := &Daemon{
		cfg: Config{RepoDir: "/tmp/repo-root", BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			"/tmp/repo-root": store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			"/tmp/repo-root": git.NewWorktreeManager(runner, "/tmp/repo-root", slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, "/tmp/repo-root", slog.Default()),
		},
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 1 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 1", count)
	}

	worktrees, err := store.ListWorktreeStates(ctx, projectID)
	if err != nil {
		t.Fatalf("ListWorktrees error: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("ListWorktrees len = %d, want 1", len(worktrees))
	}

	var status git.GitStatus
	if err := json.Unmarshal(worktrees[0].GitStatusRaw, &status); err != nil {
		t.Fatalf("unmarshal persisted git status: %v", err)
	}
	if !status.HasChanges {
		t.Fatal("persisted git status should be dirty")
	}
	if status.GitAdditions != 7 || status.GitDeletions != 3 {
		t.Fatalf("persisted git diff totals = %d/%d, want 7/3", status.GitAdditions, status.GitDeletions)
	}
	if status.GitAheadCount != 2 || status.GitBehindCount != 5 {
		t.Fatalf("persisted git ahead/behind = %d/%d, want 2/5", status.GitAheadCount, status.GitBehindCount)
	}
}

func TestRefreshWorktreeRuntimeStateSkipsClosedIssueWorktrees(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-closed-worktrees"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "closed with stale live worktree",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := issuesClient.Update(ctx, closedID, domain.StatusDone); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	worktreePath := filepath.Join(repoDir, "wt-"+closedID)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + worktreePath,
				"branch refs/heads/az/" + closedID,
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 0 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 0", count)
	}
	if _, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, closedID); err != nil {
		t.Fatalf("get worktree state: %v", err)
	} else if found {
		t.Fatalf("closed issue worktree projection persisted for %s", closedID)
	}
}

func TestRefreshWorktreeRuntimeStateSuppressesMissingWorktreeFromGitList(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-missing-worktree"
	repoDir := t.TempDir()
	missingID := "az-missing"
	missingWorktree := filepath.Join(repoDir, "missing-"+missingID)

	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + missingWorktree,
				"branch refs/heads/az/" + missingID,
				"",
			}, "\n"), nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain":
			statusCalls++
			return "", nil
		default:
			return "", nil
		}
	}}

	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY); INSERT INTO issues(id) VALUES(?)`, missingID); err != nil {
		_ = db.Close()
		t.Fatalf("seed durable issue authority: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   missingID,
		Path:      missingWorktree,
		Branch:    "az/" + missingID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale worktree projection: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		hub: publish.NewHub(16, 8, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}
	ch, cancel := d.hub.Subscribe(projectID, 0)
	t.Cleanup(cancel)

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 0 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 0", count)
	}
	if statusCalls != 0 {
		t.Fatalf("status calls = %d, want 0 for missing stale worktree", statusCalls)
	}
	if _, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, missingID); err != nil {
		t.Fatalf("get worktree state: %v", err)
	} else if found {
		t.Fatalf("missing worktree projection still present for %s", missingID)
	}
	select {
	case evt := <-ch:
		if evt.Event != protocol.EventWorktreeProjectionUpdated {
			t.Fatalf("event = %s, want %s", evt.Event, protocol.EventWorktreeProjectionUpdated)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for missing worktree projection delete event")
	}
}

func TestRefreshWorktreeRuntimeStateForIssuesDeletesClosedIssueWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-closed-target"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "closed targeted worktree",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := issuesClient.Update(ctx, closedID, domain.StatusDone); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	worktreePath := filepath.Join(repoDir, "wt-"+closedID)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + worktreePath,
				"branch refs/heads/az/" + closedID,
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	count, err := d.refreshWorktreeRuntimeStateForIssues(ctx, projectID, []string{closedID})
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeStateForIssues error: %v", err)
	}
	if count != 0 {
		t.Fatalf("refreshWorktreeRuntimeStateForIssues count = %d, want 0", count)
	}
	if _, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, closedID); err != nil {
		t.Fatalf("get worktree state: %v", err)
	} else if found {
		t.Fatalf("closed issue worktree projection persisted for %s", closedID)
	}
}

func TestRuntimeWorktreeIssueEligibleRejectsClosedAncestor(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	tests := []struct {
		name         string
		parentStatus domain.Status
	}{
		{name: "completed", parentStatus: domain.StatusDone},
		{name: "cancelled", parentStatus: domain.StatusCancelled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskByIssue := map[string]domain.Task{
				parentID.String(): {
					ID:     parentID,
					Status: tt.parentStatus,
					Type:   domain.TypeTask,
				},
				childID.String(): {
					ID:       childID,
					Status:   domain.StatusInProgress,
					Type:     domain.TypeTask,
					ParentID: &parentID,
				},
			}

			if runtimeWorktreeIssueEligible(childID.String(), taskByIssue) {
				t.Fatal("child under closed ancestor should not be runtime-worktree eligible")
			}
		})
	}
}

func TestRefreshWorktreeRuntimeStateUsesClosestNonDoneAncestorBranch(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-ancestor-base"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "root",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "parent",
		Type:     domain.TypeTask,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	childWorktreePath := filepath.Join(repoDir, "wt-child")
	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + filepath.Join(repoDir, "wt-root"),
				"branch refs/heads/az/" + rootID,
				"",
				"worktree " + filepath.Join(repoDir, "wt-parent"),
				"branch refs/heads/az/" + parentID,
				"",
				"worktree " + childWorktreePath,
				"branch refs/heads/az/" + childID,
				"",
			}, "\n"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "merge-base":
			mergeBaseRef = args[3]
			return "abc123\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "status" && args[3] == "--porcelain":
			return " M changed.go\n", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "diff" && args[3] == "--shortstat":
			return " 1 file changed, 2 insertions(+), 1 deletion(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "rev-list" && args[3] == "--count":
			return "0\n", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	d := &Daemon{
		cfg:    Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git:    git.NewClient(runner, slog.Default()),
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	_, err = d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if got, want := strings.TrimSpace(mergeBaseRef), "az/"+parentID; got != want {
		t.Fatalf("merge-base base ref = %q, want %q", got, want)
	}
}

func TestRuntimeDiffBaseBranchForIssueUsesNearestAncestorWorktreeEvenWhenClosed(t *testing.T) {
	childID := "az-child"
	parentID := "az-parent"
	rootID := "az-root"
	parentIssueID := naming.IssueID(parentID)
	rootIssueID := naming.IssueID(rootID)

	taskByIssue := map[string]domain.Task{
		childID: {
			ID:       naming.IssueID(childID),
			Status:   domain.StatusInProgress,
			ParentID: &parentIssueID,
		},
		parentID: {
			ID:       parentIssueID,
			Status:   domain.StatusDone,
			ParentID: &rootIssueID,
		},
		rootID: {
			ID:     rootIssueID,
			Status: domain.StatusInProgress,
		},
	}
	worktreeByIssue := map[string]git.Worktree{
		parentID: {
			IssueID: parentID,
			Branch:  "riordan/" + parentID + "/parent-branch",
		},
		rootID: {
			IssueID: rootID,
			Branch:  "riordan/" + rootID + "/root-branch",
		},
	}

	d := &Daemon{}
	branch := d.runtimeDiffBaseBranchForIssue(childID, "preview", taskByIssue, worktreeByIssue)
	if branch != "riordan/"+parentID+"/parent-branch" {
		t.Fatalf("runtimeDiffBaseBranchForIssue(...) = %q, want nearest ancestor worktree branch", branch)
	}
}

func TestRuntimeDiffBaseBranchForIssueSkipsAncestorWithoutWorktree(t *testing.T) {
	childID := "az-child"
	parentID := "az-parent"
	rootID := "az-root"
	parentIssueID := naming.IssueID(parentID)
	rootIssueID := naming.IssueID(rootID)

	taskByIssue := map[string]domain.Task{
		childID: {
			ID:       naming.IssueID(childID),
			Status:   domain.StatusInProgress,
			ParentID: &parentIssueID,
		},
		parentID: {
			ID:       parentIssueID,
			Status:   domain.StatusInProgress,
			ParentID: &rootIssueID,
		},
		rootID: {
			ID:     rootIssueID,
			Status: domain.StatusInProgress,
		},
	}
	worktreeByIssue := map[string]git.Worktree{
		rootID: {
			IssueID: rootID,
			Branch:  "riordan/" + rootID + "/root-branch",
		},
	}

	d := &Daemon{}
	branch := d.runtimeDiffBaseBranchForIssue(childID, "preview", taskByIssue, worktreeByIssue)
	if branch != "riordan/"+rootID+"/root-branch" {
		t.Fatalf("runtimeDiffBaseBranchForIssue(...) = %q, want closest ancestor worktree branch", branch)
	}
}

func TestRuntimeDiffBaseBranchForWorktreeUsesClosestAncestorWorktree(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worktree-base"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "parent",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "child",
		Type:     domain.TypeTask,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	parentWorktree := filepath.Join(repoDir, "wt-parent")
	childWorktree := filepath.Join(repoDir, "wt-child")
	parentBranch := "riordan/" + parentID + "/parent-branch"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: parentID, Path: parentWorktree, Branch: parentBranch, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: childID, Path: childWorktree, Branch: "riordan/" + childID + "/child-branch", UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/preview",
				"",
				"worktree " + parentWorktree,
				"branch refs/heads/" + parentBranch,
				"",
				"worktree " + childWorktree,
				"branch refs/heads/riordan/" + childID + "/child-branch",
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "preview", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	branch := d.runtimeDiffBaseBranchForWorktree(ctx, projectID, childWorktree)
	if branch != parentBranch {
		t.Fatalf("runtimeDiffBaseBranchForWorktree(...) = %q, want closest ancestor worktree branch %q", branch, parentBranch)
	}
}

func TestRefreshWorktreeRuntimeStateFallsBackToAncestorWorktreeBranchWhenAncestorTaskMissing(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-ancestor-worktree-fallback"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	ancestorID := "az-ancestor"
	childWorktreePath := filepath.Join(repoDir, "wt-child")
	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + filepath.Join(repoDir, "wt-ancestor"),
				"branch refs/heads/riordan/" + ancestorID + "/ancestor-branch",
				"",
				"worktree " + childWorktreePath,
				"branch refs/heads/riordan/" + childID + "/child-branch",
				"",
			}, "\n"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "merge-base":
			mergeBaseRef = args[3]
			return "abc123\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "status" && args[3] == "--porcelain":
			return " M changed.go\n", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "diff" && args[3] == "--shortstat":
			return " 1 file changed, 1 insertion(+), 1 deletion(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "rev-list" && args[3] == "--count":
			return "0\n", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	d := &Daemon{
		cfg:    Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git:    git.NewClient(runner, slog.Default()),
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	// Simulate sparse task projection where child references an ancestor not in taskByIssue.
	taskByIssue := map[string]domain.Task{
		childID: {
			ID:       naming.IssueID(childID),
			ParentID: func() *naming.IssueID { v := naming.IssueID(ancestorID); return &v }(),
		},
	}
	branch := d.runtimeDiffBaseBranchForIssue(childID, "main", taskByIssue, map[string]git.Worktree{
		ancestorID: {
			IssueID: ancestorID,
			Branch:  "riordan/" + ancestorID + "/ancestor-branch",
		},
	})
	if branch != "riordan/"+ancestorID+"/ancestor-branch" {
		t.Fatalf("runtimeDiffBaseBranchForIssue(...) = %q, want ancestor worktree branch", branch)
	}

	_, err = d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if got, want := strings.TrimSpace(mergeBaseRef), "main"; got != want {
		// Full reconcile has no persisted parent relation in this test DB shape,
		// so Az ancestry correctly falls back to configured base branch there.
		t.Fatalf("merge-base base ref = %q, want %q", got, want)
	}
}

func TestRefreshWorktreeRuntimeStateMutationDoesNotBypassGitProbeBudget(t *testing.T) {
	ctx := context.WithValue(context.Background(), runtimeReconcileRequestContextKey{}, runtimeReconcileRequestContext{
		Priority: reconcilePriorityManual,
		Reason:   "mutation:session.stop",
	})
	projectID := "proj-refresh-budget"
	repoDir := t.TempDir()
	worktreeOne := filepath.Join(repoDir, "repo-az-1")
	worktreeTwo := filepath.Join(repoDir, "repo-az-2")
	for _, path := range []string{worktreeOne, worktreeTwo} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir worktree %s: %v", path, err)
		}
	}
	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return "worktree " + repoDir + "\nbranch refs/heads/main\n\n" +
				"worktree " + worktreeOne + "\nbranch refs/heads/az/az-1\n\n" +
				"worktree " + worktreeTwo + "\nbranch refs/heads/az/az-2\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain":
			statusCalls++
			return "", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeGitProbeThrottle: newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "worktree_git_probe_test",
			Budget:               1,
			Cadence:              time.Hour,
			UnchangedBackoffBase: time.Hour,
			UnchangedBackoffMax:  time.Hour,
			FailureBackoffBase:   time.Hour,
			FailureBackoffMax:    time.Hour,
			Now:                  func() time.Time { return now },
		}),
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 2 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 2", count)
	}
	if statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1", statusCalls)
	}
}

func TestRefreshWorktreeRuntimeStateTransientGitFailureKeepsProjectionAndBacksOff(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-transient"
	repoDir := t.TempDir()
	worktreeOne := filepath.Join(repoDir, "repo-az-1")
	worktreeTwo := filepath.Join(repoDir, "repo-az-2")
	for _, path := range []string{worktreeOne, worktreeTwo} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir worktree %s: %v", path, err)
		}
	}

	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return "worktree " + repoDir + "\nbranch refs/heads/main\n\n" +
				"worktree " + worktreeOne + "\nbranch refs/heads/az/az-1\n\n" +
				"worktree " + worktreeTwo + "\nbranch refs/heads/az/az-2\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain":
			statusCalls++
			return "", errors.New("git status failed: exit status 128: index.lock busy")
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeGitProbeThrottle: newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "worktree_git_probe_test",
			Budget:               1,
			Cadence:              time.Hour,
			UnchangedBackoffBase: time.Hour,
			UnchangedBackoffMax:  time.Hour,
			FailureBackoffBase:   time.Hour,
			FailureBackoffMax:    time.Hour,
			Now:                  func() time.Time { return now },
		}),
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("first refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 2 {
		t.Fatalf("first refreshWorktreeRuntimeState count = %d, want 2", count)
	}
	count, err = d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("second refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 2 {
		t.Fatalf("second refreshWorktreeRuntimeState count = %d, want 2", count)
	}
	if statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1 after transient failure backoff/budget", statusCalls)
	}
	worktrees, err := store.ListWorktreeStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list worktree projections: %v", err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("worktree projection count = %d, want 2", len(worktrees))
	}
}

func TestWorktreeGitProbeThrottleKeyIncludesResolvedBaseBranch(t *testing.T) {
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	throttle := newReconcileThrottle(reconcileThrottleConfig{
		Name:                 "worktree_git_probe_test",
		Cadence:              time.Hour,
		UnchangedBackoffBase: time.Hour,
		UnchangedBackoffMax:  time.Hour,
		FailureBackoffBase:   time.Hour,
		FailureBackoffMax:    time.Hour,
		Now:                  func() time.Time { return now },
	})

	oldBaseKey := worktreeGitProbeThrottleKey("proj", "/repo-dox", "preview")
	if !throttle.Admit(oldBaseKey, false).Allowed() {
		t.Fatal("initial preview-base probe was not admitted")
	}
	throttle.Record(oldBaseKey, "preview-large-diff", nil)
	if throttle.Admit(oldBaseKey, false).Allowed() {
		t.Fatal("same preview-base probe should be throttled after unchanged record")
	}

	parentBaseKey := worktreeGitProbeThrottleKey("proj", "/repo-dox", "riordan/cif/parent")
	if !throttle.Admit(parentBaseKey, false).Allowed() {
		t.Fatal("parent-base probe should not be suppressed by previous preview-base record")
	}
}

func taskIDStrings(tasks []domain.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID.String())
	}
	return out
}

func integrationTestWorktreeList(base, scratch, head string) string {
	if strings.TrimSpace(scratch) == "" {
		return base
	}
	return strings.TrimRight(base, "\n") + fmt.Sprintf("\n\nworktree %s\nHEAD %s\ndetached\n\n", scratch, head)
}

func integrationTestIsWorktreeList(args []string) bool {
	return len(args) >= 3 && args[0] == "worktree" && args[1] == "list" ||
		len(args) >= 5 && args[0] == "-C" && args[2] == "worktree" && args[3] == "list"
}

func TestTaskDetailAttachesPublicationEvidenceDiagnostic(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	var gitCalls atomic.Int32
	d := &Daemon{
		cfg:              Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		operationRuntime: runtime,
		git: git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) {
			gitCalls.Add(1)
			return "", errors.New("publication diagnostic must not invoke Git")
		}}, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	evidence := domain.PublicationEvidence{
		EvidenceID: "merge-1", ProjectID: "project", IssueID: "issue", Layer: domain.PublicationEvidenceMergeResult,
		SourceRevision: "source", BaseRevision: "base", ResultRevision: "result", Producer: "reviewer", PolicyVersion: "policy", EnvironmentFingerprint: "env", CreatedAt: time.Unix(1, 0).UTC(),
	}
	if _, err := runtime.store.RecordPublicationEvidence(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	before, err := runtime.store.PublicationEvidenceSnapshot(ctx, "project", "issue")
	if err != nil {
		t.Fatal(err)
	}
	tasks := d.attachPublicationEvidenceDiagnostic(ctx, "project", "issue", []domain.Task{{ID: "issue"}, {ID: "related"}})
	if tasks[0].PublicationEvidence == nil || tasks[0].PublicationEvidence.State != "recorded" || tasks[0].PublicationEvidence.PatchReview != 0 || !strings.Contains(tasks[0].PublicationEvidence.Detail, "authoritative current assessment unavailable") {
		t.Fatalf("publication diagnostic = %+v", tasks[0].PublicationEvidence)
	}
	if tasks[1].PublicationEvidence != nil {
		t.Fatalf("related task received selected-task publication diagnostic: %+v", tasks[1].PublicationEvidence)
	}
	if got := gitCalls.Load(); got != 0 {
		t.Fatalf("publication diagnostic Git calls = %d, want 0", got)
	}
	after, err := runtime.store.PublicationEvidenceSnapshot(ctx, "project", "issue")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("publication evidence changed during task detail read: before=%+v after=%+v", before, after)
	}
}
