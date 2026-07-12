package issues

import (
	"context"
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
		{"review on idle", `UPDATE issues SET review_state='requested', lifecycle_state='open' WHERE id=?`, "review requires active"},
		{"terminal without timestamp", `UPDATE issues SET lifecycle_state='closed', closed_outcome='completed', closed_at=NULL WHERE id=?`, "requires outcome and closed_at"},
		{"partial owner", `UPDATE issues SET owner_id='agent', owner_kind=NULL, owner_claimed_at=NULL WHERE id=?`, "complete tuple"},
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
