package issues

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestStateProductMigrationRejectsDirectSQLContradictions(t *testing.T) {
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "constraint target", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct{ name, query, want string }{
		{"invalid kind", `UPDATE issues SET issue_type='story' WHERE id=?`, "invalid issue_type"},
		{"review on backlog", `UPDATE issues SET disposition='backlog', engagement='review_requested', review_state='requested', lifecycle_state='backlog', status='backlog' WHERE id=?`, "non-ready issue requires idle engagement"},
		{"terminal without timestamp", `UPDATE issues SET disposition='completed', engagement='idle', lifecycle_state='closed', review_state='none', closed_outcome='completed', status='closed', closed_at=NULL WHERE id=?`, "terminal issue requires outcome and closed_at"},
	}
	var ownerColumnCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('issues') WHERE name IN ('owner_id','owner_kind','owner_claimed_at','owner_expires_at')`).Scan(&ownerColumnCount); err != nil {
		t.Fatal(err)
	}
	if ownerColumnCount != 0 {
		t.Fatalf("legacy owner columns=%d, want 0", ownerColumnCount)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, tt.query, issueID)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v, want %q", err, tt.want)
			}
		})
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,state,tmux_attached_count,updated_at) VALUES('p','s',?,'running',-1,?)`, issueID, now)
	if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("negative attachment err=%v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO daemon_worktree_projections(project_id,issue_id,path,branch,updated_at) VALUES('p',?,'','b',?)`, issueID, now)
	if err == nil || !strings.Contains(err.Error(), "must be nonempty") {
		t.Fatalf("empty worktree err=%v", err)
	}
}

func TestCanonicalStateMigrationUpgradesLegacyProjectionDeterministically(t *testing.T) {
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "upgrade", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	dropCanonicalStateMigrationGuards(t, db)
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE issues SET disposition=NULL,engagement=NULL,visibility=NULL WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := applyIssueStateRuntimeConstraintsMigration(ctx, db, "0045_issue_state_runtime_constraints"); err != nil {
		t.Fatal(err)
	}
	var disposition, engagement, visibility string
	if err := db.QueryRowContext(ctx, `SELECT disposition,engagement,visibility FROM issues WHERE id=?`, id).Scan(&disposition, &engagement, &visibility); err != nil {
		t.Fatal(err)
	}
	if disposition != "ready" || engagement != "review_requested" || visibility != "live" {
		t.Fatalf("canonical state=%s/%s/%s", disposition, engagement, visibility)
	}
}

func TestCanonicalStateMigrationRollsBackAmbiguousArchiveProduct(t *testing.T) {
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "ambiguous", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	dropCanonicalStateMigrationGuards(t, db)
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`); err != nil {
		t.Fatal(err)
	}
	archiveAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `UPDATE issues SET visibility='archived',archived_at=?,engagement='working' WHERE id=?`, archiveAt, id); err != nil {
		t.Fatal(err)
	}
	err = applyIssueStateRuntimeConstraintsMigration(ctx, db, "0045_issue_state_runtime_constraints")
	if err == nil || !strings.Contains(err.Error(), "constraint failed") {
		t.Fatalf("migration error=%v", err)
	}
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("migration marker survived rollback")
	}
	var engagement string
	if err := db.QueryRowContext(ctx, `SELECT engagement FROM issues WHERE id=?`, id).Scan(&engagement); err != nil {
		t.Fatal(err)
	}
	if engagement != "working" {
		t.Fatalf("failed migration mutated source row: %s", engagement)
	}
}

func dropCanonicalStateMigrationGuards(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}) {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE IF EXISTS issue_runtime_divergences`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"issue_state_product_guard_insert", "issue_state_product_guard_update", "issue_archive_aggregate_guard", "issue_lease_archived_guard", "issue_worktree_archived_guard", "issue_session_archived_guard", "daemon_session_state_product_guard_insert", "daemon_session_state_product_guard_update", "daemon_worktree_state_product_guard_insert", "daemon_worktree_state_product_guard_update"} {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRepairReadyIdleEngagementIsAtomicAndTerminalSafe(t *testing.T) {
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	readyID, err := client.Create(ctx, CreateTaskParams{Title: "ready", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := client.RepairReadyIdleEngagement(ctx, readyID)
	if err != nil || !repaired {
		t.Fatalf("repair=%t err=%v, want true", repaired, err)
	}
	task, err := client.GetWithRuntime(ctx, "project", readyID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Engagement != domain.IssueEngagementWorking {
		t.Fatalf("engagement=%s", task.State.Engagement)
	}

	terminalID, err := client.Create(ctx, CreateTaskParams{Title: "terminal", Type: domain.TypeBug, Status: domain.StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	repaired, err = client.RepairReadyIdleEngagement(ctx, terminalID)
	if err != nil || repaired {
		t.Fatalf("terminal repair=%t err=%v, want false", repaired, err)
	}
	task, err = client.GetWithRuntime(ctx, "project", terminalID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Disposition != domain.IssueDispositionCompleted {
		t.Fatalf("terminal disposition=%s", task.State.Disposition)
	}
}

func TestArchiveAggregateIsIdleUnclaimedAndResourceFree(t *testing.T) {
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "shelved", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Archive(ctx, id); err != nil {
		t.Fatal(err)
	}
	task, err := client.GetWithRuntimeArchiveMode(ctx, "project", id, ArchiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Visibility != domain.IssueVisibilityArchived || task.State.Engagement != domain.IssueEngagementIdle {
		t.Fatalf("state=%+v", task.State)
	}
	db, _ := client.dbHandle()
	var deleted any
	if err := db.QueryRowContext(ctx, `SELECT deleted_at FROM issues WHERE id=?`, id).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("deleted_at=%v, want nil", deleted)
	}

	claimed, err := client.Create(ctx, CreateTaskParams{Title: "claimed", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ClaimOwnershipWithRuntime(ctx, "project", claimed, OwnershipClaimParams{OwnerID: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err = client.Archive(ctx, claimed); err == nil || !strings.Contains(err.Error(), "resource-free") {
		t.Fatalf("archive claimed err=%v", err)
	}
}

func TestRuntimeDivergenceQuarantinesSelectorProjectionUntilRecovery(t *testing.T) {
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "diverged", Type: domain.TypeBug, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,state,updated_at) VALUES('project','s',?,'running',?)`, id, now); err != nil {
		t.Fatal(err)
	}
	if err = client.RecordRuntimeDivergence(ctx, id, "terminal/live"); err != nil {
		t.Fatal(err)
	}
	export, err := client.ExportProjection(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Tasks) != 1 || export.Tasks[0].Session != nil || export.Tasks[0].HasTmuxSession {
		t.Fatalf("quarantined export=%+v", export.Tasks)
	}
	if err = client.ClearRuntimeDivergence(ctx, id); err != nil {
		t.Fatal(err)
	}
	export, err = client.ExportProjection(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if export.Tasks[0].Session == nil {
		t.Fatal("resolved divergence remained hidden")
	}
}
