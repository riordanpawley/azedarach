package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type corruptSQLiteDaemonTestError struct{}

func (corruptSQLiteDaemonTestError) Error() string { return "database disk image is malformed" }
func (corruptSQLiteDaemonTestError) Code() int     { return 11 }

func TestProjectIssueStoreCorruptionIsCachedAsUnavailableFromAnyStorePath(t *testing.T) {
	d := &Daemon{projectIssueStoreHealthByProject: map[string]projectIssueStoreHealthState{}}
	cause := &domain.TaskStoreError{Op: "list-mail-observation-events", TaskID: "root", Err: corruptSQLiteDaemonTestError{}}

	err := d.recordProjectIssueStoreFailure("project", cause)
	if !strings.Contains(err.Error(), "project issue store unhealthy") || !strings.Contains(err.Error(), "until daemon restart") {
		t.Fatalf("recorded health error = %q, want fail-closed recovery guidance", err)
	}
	if code := projectIssueStoreHealthErrorCode(cause); code != protocol.ErrorCodeUnavailable {
		t.Fatalf("corruption error code = %s, want unavailable", code)
	}
	cached, ok := d.projectIssueStoreHealthError("project")
	if !ok || !strings.Contains(cached.Error(), "project issue store unhealthy (cached)") {
		t.Fatalf("cached health = %v, %t; want cached unavailable state", cached, ok)
	}
	state := d.projectIssueStoreHealthByProject["project"]
	if !state.RequiresRestart {
		t.Fatal("corruption health did not require daemon restart")
	}
	state.RetryAfter = time.Time{}
	d.projectIssueStoreHealthByProject["project"] = state
	if _, ok := d.projectIssueStoreHealthError("project"); !ok {
		t.Fatal("elapsed backoff cleared corruption quarantine without daemon restart")
	}
	if !issues.IsSQLiteCorrupt(cause) || errors.Is(cause, issues.ErrSQLiteCorrupt) {
		t.Fatalf("raw SQLite code-11 classification changed: %v", cause)
	}
}

func TestLifecycleAndMailboxCorruptionFailProjectClosed(t *testing.T) {
	ctx := context.Background()
	projectID := "corrupt-project"
	repoDir := t.TempDir()
	fixture := issues.NewClient(repoDir, slog.Default())
	issueID, err := fixture.Create(ctx, issues.CreateTaskParams{Title: "corrupt target", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatalf("create fixture issue: %v", err)
	}
	if err := fixture.CloseDB(); err != nil {
		t.Fatalf("close fixture issue store: %v", err)
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	corruptDaemonSQLiteRootPage(t, dbPath, "issue_observation_events")
	corruptClient := issues.NewClientAtPath(dbPath, slog.Default(), issues.WithExistingDatabaseOnly())
	d := &Daemon{
		cfg:                              Config{Logger: slog.Default()},
		issues:                           corruptClient,
		issueClientsByProject:            map[string]*issues.Client{projectID: corruptClient},
		issueClientsByRoot:               map[string]*issues.Client{daemonStoreRootKey(repoDir): corruptClient},
		projectIssueStoreHealthByProject: map[string]projectIssueStoreHealthState{},
	}

	update, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		Command: "task.update_status",
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"task_id": issueID, "status": domain.StatusInProgress,
		}),
	})
	if err != nil || update.OK || update.Error == nil || update.Error.Code != protocol.ErrorCodeUnavailable {
		if update.Error != nil {
			t.Fatalf("corrupt lifecycle code=%s message=%q err=%v; want unavailable", update.Error.Code, update.Error.Message, err)
		}
		t.Fatalf("corrupt lifecycle response = %+v, err=%v; want unavailable", update, err)
	}
	if !strings.Contains(update.Error.Message, "project issue store unhealthy") {
		t.Fatalf("corrupt lifecycle message = %q, want project quarantine", update.Error.Message)
	}

	mail, err := d.handleMailSend(ctx, protocol.RequestEnvelope{
		Command: protocol.CommandMailSend,
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(protocol.MailSendCommandBody{
			RepoDir: repoDir, ParentIssue: issueID, IssueID: naming.IssueID(issueID), Type: "message", Body: "preserved",
		}),
	})
	if err != nil || mail.OK || mail.Error == nil || mail.Error.Code != protocol.ErrorCodeUnavailable {
		t.Fatalf("corrupt mailbox response = %+v, err=%v; want unavailable", mail, err)
	}
	if !strings.Contains(mail.Error.Message, "project issue store unhealthy") {
		t.Fatalf("corrupt mailbox message = %q, want project quarantine", mail.Error.Message)
	}
}

func TestCommandBoundaryQuarantinesFirstCorruptionFromEveryIssueMutationPath(t *testing.T) {
	tests := []struct {
		name    string
		command string
		body    func(string) []byte
	}{
		{
			name:    "create",
			command: "task.create",
			body: func(string) []byte {
				return marshalJSON(map[string]any{"title": "new issue", "type": domain.TypeTask, "priority": domain.P2, "status": domain.StatusOpen})
			},
		},
		{
			name:    "event append",
			command: "task.event.append",
			body: func(issueID string) []byte {
				return marshalJSON(map[string]any{"task_id": issueID, "event_type": domain.IssueEventProgressRecorded, "payload": map[string]any{"summary": "progress"}})
			},
		},
		{
			name:    "update details",
			command: "task.update_details",
			body: func(issueID string) []byte {
				return marshalJSON(map[string]any{"task_id": issueID, "title": "updated", "description": "details", "type": domain.TypeBug, "priority": domain.P1})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			projectID := "corrupt-" + strings.ReplaceAll(tt.name, " ", "-")
			repoDir := t.TempDir()
			fixture := issues.NewClient(repoDir, slog.Default())
			issueID, err := fixture.Create(ctx, issues.CreateTaskParams{Title: "corrupt target", Type: domain.TypeBug, Status: domain.StatusOpen})
			if err != nil {
				t.Fatalf("create fixture issue: %v", err)
			}
			if err := fixture.CloseDB(); err != nil {
				t.Fatalf("close fixture issue store: %v", err)
			}
			dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
			corruptDaemonSQLiteRootPage(t, dbPath, "issue_observation_events")
			corruptClient := issues.NewClientAtPath(dbPath, slog.Default(), issues.WithExistingDatabaseOnly())
			t.Cleanup(func() { _ = corruptClient.CloseDB() })
			d := &Daemon{
				cfg:                              Config{RepoDir: repoDir, Logger: slog.Default()},
				issues:                           corruptClient,
				issueClientsByProject:            map[string]*issues.Client{projectID: corruptClient},
				issueClientsByRoot:               map[string]*issues.Client{daemonStoreRootKey(repoDir): corruptClient},
				projectIssueStoreHealthByProject: map[string]projectIssueStoreHealthState{},
			}

			first, err := d.command(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       naming.RequestID("first-corruption-" + tt.command),
				Kind:            protocol.EnvelopeKindCommand,
				Command:         tt.command,
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Body:            tt.body(issueID),
			})
			if err != nil || first.OK || first.Error == nil || first.Error.Code != protocol.ErrorCodeUnavailable {
				t.Fatalf("first %s response=%+v err=%v, want unavailable", tt.command, first, err)
			}
			if !strings.Contains(first.Error.Message, "project issue store unhealthy") || !strings.Contains(first.Error.Message, "until daemon restart") {
				t.Fatalf("first %s message=%q, want corruption quarantine", tt.command, first.Error.Message)
			}

			cached, err := d.command(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       naming.RequestID("cached-unrelated-task-list"),
				Kind:            protocol.EnvelopeKindCommand,
				Command:         "task.list",
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			})
			if err != nil || cached.OK || cached.Error == nil || cached.Error.Code != protocol.ErrorCodeUnavailable {
				t.Fatalf("cached task.list response=%+v err=%v, want unavailable", cached, err)
			}
			if !strings.Contains(cached.Error.Message, "project issue store unhealthy (cached)") {
				t.Fatalf("cached task.list message=%q, want cached quarantine", cached.Error.Message)
			}
		})
	}
}

func corruptDaemonSQLiteRootPage(t *testing.T, dbPath, object string) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=rw", filepath.ToSlash(dbPath)))
	if err != nil {
		t.Fatalf("open fixture for corruption: %v", err)
	}
	var pageSize, rootPage int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("read page size: %v", err)
	}
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name=?`, object).Scan(&rootPage); err != nil {
		t.Fatalf("read %s root page: %v", object, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture inspection handle: %v", err)
	}
	file, err := os.OpenFile(dbPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open fixture database bytes: %v", err)
	}
	if _, err := file.WriteAt(make([]byte, pageSize), (rootPage-1)*pageSize); err != nil {
		_ = file.Close()
		t.Fatalf("corrupt %s root page: %v", object, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupt fixture: %v", err)
	}
}
