package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
	_ "modernc.org/sqlite"
)

func TestDoneTasksInArchiveOrderSortsDescendantsBeforeAncestors(t *testing.T) {
	rootID := naming.IssueID("az-root")
	childID := naming.IssueID("az-child")
	grandchildID := naming.IssueID("az-grandchild")
	openChildID := naming.IssueID("az-open-child")

	tasks := []domain.Task{
		{ID: rootID, Status: domain.StatusDone},
		{ID: childID, Status: domain.StatusDone, ParentID: &rootID},
		{ID: grandchildID, Status: domain.StatusCancelled, ParentID: &childID},
		{ID: openChildID, Status: domain.StatusOpen, ParentID: &rootID},
	}

	ordered := doneTasksInArchiveOrder(tasks)
	got := make([]naming.IssueID, 0, len(ordered))
	for _, task := range ordered {
		got = append(got, task.ID)
	}
	want := []naming.IssueID{grandchildID, childID, rootID}
	if len(got) != len(want) {
		t.Fatalf("ordered IDs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordered IDs = %+v, want %+v", got, want)
		}
	}
}

func TestProjectCleanupAdvisorSessionsStopsRuntimeBeforeProjectRemoval(t *testing.T) {
	ctx := withDaemonProjectIDContext(context.Background(), protocol.DefaultProjectID)
	repoDir := t.TempDir()
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "project removal", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "request-project-remove", IssueID: issueID, DecisionKey: "remove", OrchestrationScope: "project", Question: "Remove?", Why: "cleanup", Options: []domain.InteractionOption{{Key: "yes", Label: "Yes"}}, Significance: domain.InteractionSignificanceRoutine, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Remove"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := client.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issues: client, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(), hub: publish.NewHub(16, 8, slog.Default()), revision: map[string]uint64{}}
	discussed, err := (issueInteractionService{daemon: d}).MutateInteraction(ctx, protocol.CommandInteractionDiscuss, protocol.InteractionMutationRequestBody{ID: request.ID, ExpectedRevision: 1, Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(protocol.ProjectCleanupRequestBody{ProjectID: protocol.DefaultProjectID, Categories: []string{protocol.ProjectCleanupCategoryAdvisorSessions}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleProjectCleanup(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "cleanup-advisors", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandProjectCleanup, Meta: protocol.Metadata{ProjectID: protocol.DefaultProjectID}, Body: body})
	if err != nil || !resp.OK {
		t.Fatalf("cleanup response=%+v err=%v", resp, err)
	}
	var result protocol.ProjectCleanupResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.AdvisorSessionsCleaned != 1 || runner.sessions[discussed.Request.SessionID] {
		t.Fatalf("cleanup result=%+v sessions=%v", result, runner.sessions)
	}
	if _, found, err := d.sessionRuntimeStateStore(protocol.DefaultProjectID).GetAdvisorSession(ctx, protocol.DefaultProjectID, request.ID); err != nil || found {
		t.Fatalf("advisor reservation remains found=%t err=%v", found, err)
	}
	resp, err = d.handleProjectCleanup(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "cleanup-advisors-again", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandProjectCleanup, Meta: protocol.Metadata{ProjectID: protocol.DefaultProjectID}, Body: body})
	if err != nil || !resp.OK {
		t.Fatalf("repeated cleanup response=%+v err=%v", resp, err)
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.AdvisorSessionsCleaned != 0 {
		t.Fatalf("repeated cleanup count = %d, want 0", result.AdvisorSessionsCleaned)
	}
}

func TestProjectCleanupArchiveDoneArchivesCompletedSubgraphBottomUp(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-cleanup-archive-subgraph"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Closed root",
		Type:   domain.TypeEpic,
		Status: domain.StatusDone,
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
		t.Fatalf("create child: %v", err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed grandchild",
		Type:     domain.TypeTask,
		Status:   domain.StatusDone,
		ParentID: &childID,
	})
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(protocol.ProjectCleanupRequestBody{
		ProjectID:  naming.ProjectID(projectID),
		Categories: []string{projectCleanupCategoryArchiveDone},
	})
	if err != nil {
		t.Fatalf("marshal cleanup request: %v", err)
	}
	resp, err := d.handleProjectCleanup(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cleanup-archive-done-subgraph",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandProjectCleanup,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleProjectCleanup error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleProjectCleanup response = %+v", resp.Error)
	}
	var result protocol.ProjectCleanupResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal cleanup response: %v", err)
	}
	if result.Archived != 3 {
		t.Fatalf("archived = %d, want 3", result.Archived)
	}

	for _, issueID := range []string{rootID, childID, grandchildID} {
		tasks, err := issuesClient.Search(ctx, issueID)
		if err != nil {
			t.Fatalf("search %s: %v", issueID, err)
		}
		if len(tasks) != 0 {
			t.Fatalf("search %s returned hot tasks after archive: %+v", issueID, tasks)
		}
	}
}

func TestIssueAutoArchiveArchivesDoneIssuesOlderThanRetention(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-auto-archive"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	oldDoneID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Old done",
		Type:   domain.TypeTask,
		Status: domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("create old done: %v", err)
	}
	recentDoneID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Recent done",
		Type:   domain.TypeTask,
		Status: domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("create recent done: %v", err)
	}
	now := time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC)
	db, err := sql.Open("sqlite", filepath.Join(repoDir, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE id = ?`, now.AddDate(0, 0, -45).Format(time.RFC3339Nano), oldDoneID); err != nil {
		t.Fatalf("set old done updated_at: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE id = ?`, now.AddDate(0, 0, -5).Format(time.RFC3339Nano), recentDoneID); err != nil {
		t.Fatalf("set recent done updated_at: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			Logger:  slog.Default(),
			IssueAutoArchive: appconfig.IssueAutoArchiveConfig{
				Enabled:         true,
				ClosedAfterDays: 30,
				Interval:        "1h",
			},
		},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision:                map[string]uint64{},
		issueAutoArchiveLastRun: map[string]time.Time{},
	}

	archived, err := d.runIssueAutoArchiveOnce(ctx, projectID, now)
	if err != nil {
		t.Fatalf("runIssueAutoArchiveOnce error: %v", err)
	}
	if archived != 1 {
		t.Fatalf("archived = %d, want 1", archived)
	}

	if _, err := issuesClient.GetWithRuntime(ctx, projectID, oldDoneID); err == nil {
		t.Fatalf("old done issue still active after auto archive")
	}
	if _, err := issuesClient.GetWithRuntime(ctx, projectID, recentDoneID); err != nil {
		t.Fatalf("recent done issue should remain active: %v", err)
	}
	if _, err := issuesClient.GetWithRuntimeArchiveMode(ctx, projectID, oldDoneID, issues.ArchiveOnly); err != nil {
		t.Fatalf("old done issue not visible as archived: %v", err)
	}
}
