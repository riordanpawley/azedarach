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
	parallelIssueStoreTest(t)
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
		{"status mirror bypass", `UPDATE issues SET status='closed' WHERE id=?`, "legacy status projection mismatch"},
		{"archive timestamp bypass", `UPDATE issues SET archived_at='2026-07-13T00:00:00Z' WHERE id=?`, "visibility/archive audit mismatch"},
		{"deletion timestamp bypass", `UPDATE issues SET deleted_at='2026-07-13T00:00:00Z' WHERE id=?`, "deletion timestamp is not canonical authority"},
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
	_, err = db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,scope_id,state,tmux_attached_count,updated_at) VALUES('p','s',?,?,'running',-1,?)`, issueID, issueID, now)
	if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("negative attachment err=%v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO daemon_worktree_projections(project_id,issue_id,path,branch,updated_at) VALUES('p',?,'','b',?)`, issueID, now)
	if err == nil || !strings.Contains(err.Error(), "nonempty identity") {
		t.Fatalf("empty worktree err=%v", err)
	}
}

func TestArchivedResourceGuardsRejectUpdatesAndStoppedRows(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	archivedID, err := client.Create(ctx, CreateTaskParams{Title: "archived", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Archive(ctx, archivedID); err != nil {
		t.Fatal(err)
	}
	liveID, err := client.Create(ctx, CreateTaskParams{Title: "live", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,scope_id,state,tmux_attached_count,updated_at) VALUES('p','s',?,?,'stopped',0,?)`, liveID, liveID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE daemon_session_projections SET issue_id=?,scope_id=? WHERE session_id='s'`, archivedID, archivedID); err == nil || !strings.Contains(err.Error(), "cannot attach session") {
		t.Fatalf("session retarget err=%v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO daemon_worktree_projections(project_id,issue_id,path,branch,updated_at) VALUES('p',?,'/tmp/live','b',?)`, liveID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE daemon_worktree_projections SET issue_id=? WHERE issue_id=?`, archivedID, liveID); err == nil || !strings.Contains(err.Error(), "cannot attach worktree") {
		t.Fatalf("worktree retarget err=%v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO issue_coordination_leases(issue_id,purpose,owner_id,owner_kind,claimed_at) VALUES(?,'execution','a','agent',?)`, liveID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE issue_coordination_leases SET issue_id=? WHERE issue_id=?`, archivedID, liveID); err == nil || !strings.Contains(err.Error(), "ineligible") {
		t.Fatalf("lease retarget err=%v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM issue_coordination_leases WHERE issue_id=?`, liveID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM daemon_worktree_projections WHERE issue_id=?`, liveID); err != nil {
		t.Fatal(err)
	}
	if err := client.Archive(ctx, liveID); err == nil || !strings.Contains(err.Error(), "resource-free") {
		t.Fatalf("archive with stopped session err=%v", err)
	}
}

func TestLeasePurposeEligibilityAcrossIssueStateProduct(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	readyID, _ := client.Create(ctx, CreateTaskParams{Title: "ready", Type: domain.TypeTask, Status: domain.StatusOpen})
	backlogID, _ := client.Create(ctx, CreateTaskParams{Title: "backlog", Type: domain.TypeTask, Status: domain.StatusOpen})
	backlog := domain.IssueWorkflowBacklog
	if err := client.UpdateDetails(ctx, backlogID, UpdateTaskParams{Title: "backlog", Type: domain.TypeTask, Priority: domain.P2, Lifecycle: &backlog}); err != nil {
		t.Fatal(err)
	}
	terminalID, _ := client.Create(ctx, CreateTaskParams{Title: "terminal", Type: domain.TypeTask, Status: domain.StatusDone})
	claim := func(id string, purpose domain.CoordinationLeasePurpose) error {
		return client.claimOwnership(ctx, id, OwnershipClaimParams{OwnerID: "agent", OwnerKind: "agent", Purpose: purpose})
	}
	if err := claim(readyID, domain.CoordinationLeaseExecution); err != nil {
		t.Fatalf("ready execution claim: %v", err)
	}
	if err := claim(readyID, domain.CoordinationLeaseReview); err == nil || !strings.Contains(err.Error(), "ineligible") {
		t.Fatalf("idle review claim err=%v", err)
	}
	if err := client.Update(ctx, readyID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if err := claim(readyID, domain.CoordinationLeaseReview); err == nil || !strings.Contains(err.Error(), "ineligible") {
		t.Fatalf("working review claim err=%v", err)
	}
	if err := client.Update(ctx, readyID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	if err := claim(readyID, domain.CoordinationLeaseReview); err != nil {
		t.Fatalf("review_requested review claim: %v", err)
	}
	if err := claim(backlogID, domain.CoordinationLeaseOrchestration); err != nil {
		t.Fatalf("backlog orchestration claim: %v", err)
	}
	for _, tc := range []struct {
		id      string
		purpose domain.CoordinationLeasePurpose
	}{{backlogID, domain.CoordinationLeaseExecution}, {backlogID, domain.CoordinationLeaseReview}, {terminalID, domain.CoordinationLeaseExecution}, {terminalID, domain.CoordinationLeaseOrchestration}} {
		if err := claim(tc.id, tc.purpose); err == nil || !strings.Contains(err.Error(), "ineligible") {
			t.Fatalf("claim %s/%s err=%v", tc.id, tc.purpose, err)
		}
	}
}

func TestCanonicalClaimMigrationPreflight(t *testing.T) {
	parallelIssueStoreTest(t)
	tests := []struct {
		name       string
		ownerID    string
		ownerKind  string
		claimedAt  string
		leaseOwner string
		wantErr    string
	}{
		{name: "owner only", ownerID: "owner", ownerKind: "agent", claimedAt: "2026-07-13T00:00:00Z"},
		{name: "lease only", leaseOwner: "lease"},
		{name: "matching", ownerID: "same", ownerKind: "agent", claimedAt: "2026-07-13T00:00:00Z", leaseOwner: "same"},
		{name: "conflicting", ownerID: "owner", ownerKind: "agent", claimedAt: "2026-07-13T00:00:00Z", leaseOwner: "lease", wantErr: "authority conflict"},
		{name: "partial", ownerID: "owner", ownerKind: "", claimedAt: "", wantErr: "partial tuples"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(t.TempDir(), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			ctx := context.Background()
			id, err := client.Create(ctx, CreateTaskParams{Title: tt.name, Type: domain.TypeTask, Status: domain.StatusOpen})
			if err != nil {
				t.Fatal(err)
			}
			db, _ := client.dbHandle()
			dropCanonicalStateMigrationGuards(t, db)
			for _, ddl := range []string{"ALTER TABLE issues ADD COLUMN owner_id TEXT", "ALTER TABLE issues ADD COLUMN owner_kind TEXT", "ALTER TABLE issues ADD COLUMN owner_claimed_at TEXT", "ALTER TABLE issues ADD COLUMN owner_expires_at TEXT"} {
				if _, err := db.ExecContext(ctx, ddl); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`); err != nil {
				t.Fatal(err)
			}
			if tt.ownerID != "" {
				if _, err := db.ExecContext(ctx, `UPDATE issues SET owner_id=?,owner_kind=?,owner_claimed_at=? WHERE id=?`, tt.ownerID, tt.ownerKind, tt.claimedAt, id); err != nil {
					t.Fatal(err)
				}
			}
			if tt.leaseOwner != "" {
				if _, err := db.ExecContext(ctx, `INSERT INTO issue_coordination_leases(issue_id,purpose,owner_id,owner_kind,claimed_at) VALUES(?,'execution',?,'agent','2026-07-13T00:00:00Z') ON CONFLICT(issue_id,purpose) DO UPDATE SET owner_id=excluded.owner_id,owner_kind=excluded.owner_kind,claimed_at=excluded.claimed_at`, id, tt.leaseOwner); err != nil {
					t.Fatal(err)
				}
			}
			err = applyIssueStateRuntimeConstraintsMigration(ctx, db, "0045_issue_state_runtime_constraints")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("migration err=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var owner string
			if err := db.QueryRowContext(ctx, `SELECT owner_id FROM issue_coordination_leases WHERE issue_id=? AND purpose='execution'`, id).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			want := tt.leaseOwner
			if want == "" {
				want = tt.ownerID
			}
			if owner != want {
				t.Fatalf("lease owner=%q want %q", owner, want)
			}
		})
	}
}

func TestCanonicalStateRepairRunsAfterRecordedBroken0045(t *testing.T) {
	parallelIssueStoreTest(t)
	dir := t.TempDir()
	client := NewClient(dir, slog.Default())
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "repair drift", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	archivedID, err := client.Create(ctx, CreateTaskParams{Title: "legacy archived active", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	dropCanonicalStateMigrationGuards(t, db)
	triggerRows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger'`)
	if err != nil {
		t.Fatal(err)
	}
	var triggerNames []string
	for triggerRows.Next() {
		var name string
		if err := triggerRows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggerNames = append(triggerNames, name)
	}
	if err := triggerRows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range triggerNames {
		if strings.HasSuffix(name, "_search_fts") {
			continue
		}
		if _, err := db.ExecContext(ctx, `DROP TRIGGER IF EXISTS "`+strings.ReplaceAll(name, `"`, `""`)+`"`); err != nil {
			t.Fatal(err)
		}
	}
	archiveAt := "2026-07-13T00:00:00Z"
	if _, err := db.ExecContext(ctx, `UPDATE issues SET archived_at=?,deleted_at=? WHERE id=?`, archiveAt, archiveAt, archivedID); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"visibility", "engagement", "disposition"} {
		if _, err := db.ExecContext(ctx, `ALTER TABLE issues DROP COLUMN `+column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	for _, ddl := range []string{"ALTER TABLE issues ADD COLUMN owner_id TEXT", "ALTER TABLE issues ADD COLUMN owner_kind TEXT", "ALTER TABLE issues ADD COLUMN owner_claimed_at TEXT", "ALTER TABLE issues ADD COLUMN owner_expires_at TEXT"} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE issues SET owner_id='legacy-owner',owner_kind='agent',owner_claimed_at='2026-07-13T00:00:00Z' WHERE id=?`, archivedID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER issue_state_product_guard_insert BEFORE INSERT ON issues BEGIN
		SELECT CASE WHEN (NEW.owner_id IS NULL)!=(NEW.owner_kind IS NULL) OR (NEW.owner_id IS NULL)!=(NEW.owner_claimed_at IS NULL)
		THEN RAISE(ABORT,'issue owner fields must form a complete tuple') END; END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id IN ('0046_repair_issue_state_runtime_constraints','0047_human_authority_projection_revision')`); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}

	repaired := NewClient(dir, slog.Default())
	t.Cleanup(func() { _ = repaired.CloseDB() })
	repairedDB, err := repaired.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var disposition, engagement, visibility, status string
	if err := repairedDB.QueryRowContext(ctx, `SELECT disposition,engagement,visibility,status FROM issues WHERE id=?`, id).Scan(&disposition, &engagement, &visibility, &status); err != nil {
		t.Fatal(err)
	}
	if disposition != "ready" || engagement != "working" || visibility != "live" || status != string(domain.StatusInProgress) {
		t.Fatalf("repaired state=%s/%s/%s status=%s", disposition, engagement, visibility, status)
	}
	var lifecycle, review, outcome string
	var deletedAt sql.NullString
	if err := repairedDB.QueryRowContext(ctx, `SELECT disposition,engagement,visibility,status,lifecycle_state,review_state,closed_outcome,deleted_at FROM issues WHERE id=?`, archivedID).Scan(&disposition, &engagement, &visibility, &status, &lifecycle, &review, &outcome, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if disposition != "ready" || engagement != "idle" || visibility != "archived" || status != string(domain.StatusOpen) || lifecycle != "open" || review != "none" || outcome != "none" || deletedAt.Valid {
		t.Fatalf("repaired archived state=%s/%s/%s status=%s legacy=%s/%s/%s deleted=%v", disposition, engagement, visibility, status, lifecycle, review, outcome, deletedAt)
	}
	var archivedLeases int
	if err := repairedDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_coordination_leases WHERE issue_id=?`, archivedID).Scan(&archivedLeases); err != nil || archivedLeases != 0 {
		t.Fatalf("archived legacy leases=%d err=%v", archivedLeases, err)
	}
	var applied int
	if err := repairedDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='0046_repair_issue_state_runtime_constraints'`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("repair migration applied=%d err=%v", applied, err)
	}
}

func TestCanonicalStateRepairRecordsNoOpOnCanonicalSchema(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='0046_repair_issue_state_runtime_constraints'`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("repair migration marker=%d err=%v", applied, err)
	}
}

func TestCanonicalMigrationRejectsHistoricalReviewLeaseWithoutReviewRequest(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "invalid review lease", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	dropCanonicalStateMigrationGuards(t, db)
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO issue_coordination_leases(issue_id,purpose,owner_id,owner_kind,claimed_at) VALUES(?,'review','r','agent','2026-07-13T00:00:00Z')`, id); err != nil {
		t.Fatal(err)
	}
	if err := applyIssueStateRuntimeConstraintsMigration(ctx, db, "0045_issue_state_runtime_constraints"); err == nil || !strings.Contains(err.Error(), "constraint failed") {
		t.Fatalf("migration err=%v, want invalid historical review lease rejection", err)
	}
}

func TestCanonicalMigrationRejectsOrphanAuthorityRows(t *testing.T) {
	parallelIssueStoreTest(t)
	for _, kind := range []string{"lease", "session", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			client := NewClient(t.TempDir(), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			ctx := context.Background()
			db, _ := client.dbHandle()
			db.SetMaxOpenConns(1)
			dropCanonicalStateMigrationGuards(t, db)
			if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			now := "2026-07-13T00:00:00Z"
			var err error
			switch kind {
			case "lease":
				_, err = db.ExecContext(ctx, `INSERT INTO issue_coordination_leases(issue_id,purpose,owner_id,owner_kind,claimed_at) VALUES('missing','execution','a','agent',?)`, now)
			case "session":
				_, err = db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,scope_id,state,tmux_attached_count,updated_at) VALUES('p','s','missing','missing','stopped',0,?)`, now)
			case "worktree":
				_, err = db.ExecContext(ctx, `INSERT INTO daemon_worktree_projections(project_id,issue_id,path,branch,updated_at) VALUES('p','missing','/tmp/orphan','b',?)`, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := applyIssueStateRuntimeConstraintsMigration(ctx, db, "0045_issue_state_runtime_constraints"); err == nil || !strings.Contains(err.Error(), "constraint failed") {
				t.Fatalf("migration err=%v, want orphan %s rejection", err, kind)
			}
		})
	}
}

func TestReviewLeaseDirectSQLCartesian(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "review cartesian", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	insert := func() error {
		_, err := db.ExecContext(ctx, `INSERT INTO issue_coordination_leases(issue_id,purpose,owner_id,owner_kind,claimed_at) VALUES(?,'review','r','agent','2026-07-13T00:00:00Z')`, id)
		return err
	}
	if err := insert(); err == nil || !strings.Contains(err.Error(), "ineligible") {
		t.Fatalf("idle insert err=%v", err)
	}
	if err := client.Update(ctx, id, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if err := insert(); err == nil || !strings.Contains(err.Error(), "ineligible") {
		t.Fatalf("working insert err=%v", err)
	}
	if err := client.Update(ctx, id, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	if err := insert(); err != nil {
		t.Fatalf("review_requested insert: %v", err)
	}
}

func TestWorkerSessionScopeDirectSQLRejectsEmptyAndMismatch(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "scope", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	now := "2026-07-13T00:00:00Z"
	insert := func(sessionID, scopeID string) error {
		_, err := db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,tmux_attached_count,updated_at) VALUES('p',?,?,'worker','issue',?,'stopped',0,?)`, sessionID, id, scopeID, now)
		return err
	}
	if err := insert("empty", ""); err == nil || !strings.Contains(err.Error(), "existing issue scope") {
		t.Fatalf("empty scope err=%v", err)
	}
	if err := insert("mismatch", "other"); err == nil || !strings.Contains(err.Error(), "existing issue scope") {
		t.Fatalf("mismatched scope err=%v", err)
	}
	if err := insert("valid", id); err != nil {
		t.Fatalf("matching scope: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,tmux_attached_count,updated_at) VALUES('p','project-orchestrator','','orchestrator','orchestration','project','running',0,?)`, now); err != nil {
		t.Fatalf("project orchestrator without issue identity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO interaction_requests(id,issue_id,decision_key,state,revision,request_json,created_at,updated_at) VALUES('request-scope',?,'decision','open',1,'{}',?,?)`, id, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','advisor-valid',?,'advisor','interaction','request-scope','running',?)`, id, now); err != nil {
		t.Fatalf("valid advisor: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE daemon_session_projections SET issue_id='missing' WHERE session_id='advisor-valid'`); err == nil {
		t.Fatal("advisor orphan/mismatch direct mutation accepted")
	}
	for _, statement := range []string{
		`INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','project-bad',?,'orchestrator','orchestration','project','running',?)`,
		`INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','root-bad',?,'orchestrator','orchestration','other','running',?)`,
		`INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','root-missing','missing','orchestrator','orchestration','missing','running',?)`,
	} {
		args := []any{now}
		if strings.Count(statement, "?") == 2 {
			args = []any{id, now}
		}
		if _, err := db.ExecContext(ctx, statement, args...); err == nil || !strings.Contains(err.Error(), "orchestrator session") {
			t.Fatalf("invalid orchestrator product accepted: statement=%s err=%v", statement, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','rooted',?,'orchestrator','orchestration',?,'running',?)`, id, id, now); err != nil {
		t.Fatalf("valid rooted orchestrator: %v", err)
	}
}

func TestCanonicalMigrationBackfillsPreColumnWorkerScope(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "legacy scope", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	dropCanonicalStateMigrationGuards(t, db)
	if _, err := db.ExecContext(ctx, `DROP TABLE daemon_session_projections; CREATE TABLE daemon_session_projections(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,state TEXT NOT NULL,tmux_attached_count INTEGER NOT NULL DEFAULT 0,observed_state TEXT,activity TEXT,activity_source TEXT,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id)); INSERT INTO daemon_session_projections(project_id,session_id,issue_id,state,updated_at) VALUES('p','legacy',?,'stopped','2026-07-13T00:00:00Z'); DELETE FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`, id); err != nil {
		t.Fatal(err)
	}
	if err := applyIssueStateRuntimeConstraintsMigration(ctx, db, "0045_issue_state_runtime_constraints"); err != nil {
		t.Fatal(err)
	}
	var role, scopeKind, scopeID string
	if err := db.QueryRowContext(ctx, `SELECT role,scope_kind,scope_id FROM daemon_session_projections WHERE session_id='legacy'`).Scan(&role, &scopeKind, &scopeID); err != nil {
		t.Fatal(err)
	}
	if role != "worker" || scopeKind != "issue" || scopeID != id {
		t.Fatalf("scope=%s/%s/%s want worker/issue/%s", role, scopeKind, scopeID, id)
	}
}

func TestCanonicalMigrationConvergesDuplicateLogicalSessions(t *testing.T) {
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	workerID, err := client.Create(ctx, CreateTaskParams{Title: "worker", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := client.Create(ctx, CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	dropCanonicalStateMigrationGuards(t, db)
	legacySessionTable := func(name string) string {
		return `CREATE TABLE ` + name + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE daemon_session_projections; DROP TABLE daemon_session_observations; `+legacySessionTable("daemon_session_projections")+legacySessionTable("daemon_session_observations")+` DELETE FROM schema_migrations WHERE id='0045_issue_state_runtime_constraints'`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`
	for _, row := range [][]any{
		{"p", "pr-" + workerID, workerID, "worker", "issue", workerID, "running", "running", "idle", "", 0, "2026-07-13T00:00:00Z", "2026-07-13T00:00:00Z"},
		{"p", "worker-new", workerID, "worker", "issue", workerID, "paused", "paused", "busy", "hooks", 0, "2026-07-13T00:01:00Z", "2026-07-13T00:02:00Z"},
		{"p", "pr-orchestrator-project", "", "orchestrator", "orchestration", "project", "running", "running", "idle", "", 0, "2026-07-13T00:00:00Z", "2026-07-13T00:00:00Z"},
		{"p", "project-new", "", "orchestrator", "orchestration", "project", "paused", "paused", "busy", "hooks", 0, "2026-07-13T00:01:00Z", "2026-07-13T00:02:00Z"},
		{"p", "pr-" + rootID, rootID, "orchestrator", "orchestration", rootID, "running", "running", "idle", "", 0, "2026-07-13T00:00:00Z", "2026-07-13T00:00:00Z"},
		{"p", "root-new", rootID, "orchestrator", "orchestration", rootID, "paused", "paused", "busy", "hooks", 0, "2026-07-13T00:01:00Z", "2026-07-13T00:02:00Z"},
	} {
		if _, err := db.ExecContext(ctx, insert, row...); err != nil {
			t.Fatal(err)
		}
	}
	observationInsert := strings.Replace(insert, "daemon_session_projections", "daemon_session_observations", 1)
	for _, row := range [][]any{
		{"p", "observation-old", workerID, "worker", "issue", workerID, "running", "running", "idle", "", 0, "2026-07-13T00:00:00Z", "2026-07-13T00:00:00Z"},
		{"p", "observation-new", workerID, "worker", "issue", workerID, "paused", "paused", "busy", "hooks", 0, "2026-07-13T00:01:00Z", "2026-07-13T00:02:00Z"},
	} {
		if _, err := db.ExecContext(ctx, observationInsert, row...); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyIssueStateRuntimeConstraintsMigration(ctx, db, "0045_issue_state_runtime_constraints"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_projections WHERE project_id='p'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("canonical rows=%d want 3", count)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_observations WHERE project_id='p'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("canonical observation rows=%d want 1", count)
	}
	var observedRuntimeID string
	if err := db.QueryRowContext(ctx, `SELECT session_id FROM daemon_session_observations WHERE project_id='p'`).Scan(&observedRuntimeID); err != nil {
		t.Fatal(err)
	}
	if observedRuntimeID != "pr-"+workerID {
		t.Fatalf("observation runtime association=%s want %s", observedRuntimeID, "pr-"+workerID)
	}
	for _, id := range []string{"pr-" + workerID, "pr-orchestrator-project", "pr-" + rootID} {
		var state, activity, started string
		if err := db.QueryRowContext(ctx, `SELECT state,activity,started_at FROM daemon_session_projections WHERE project_id='p' AND session_id=?`, id).Scan(&state, &activity, &started); err != nil {
			t.Fatal(err)
		}
		if state != "paused" || activity != "busy" || started != "2026-07-13T00:00:00Z" {
			t.Fatalf("%s merged=%s/%s/%s", id, state, activity, started)
		}
	}
}

func TestCanonicalStateMigrationUpgradesLegacyProjectionDeterministically(t *testing.T) {
	parallelIssueStoreTest(t)
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
	parallelIssueStoreTest(t)
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
	for _, name := range []string{"issue_state_product_guard_insert", "issue_state_product_guard_update", "issue_archive_aggregate_guard", "issue_lease_archived_guard", "issue_lease_state_guard_update", "issue_worktree_archived_guard", "issue_worktree_archived_guard_update", "issue_session_archived_guard", "issue_session_archived_guard_update", "daemon_session_state_product_guard_insert", "daemon_session_state_product_guard_update", "daemon_worktree_state_product_guard_insert", "daemon_worktree_state_product_guard_update"} {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRepairReadyIdleEngagementIsAtomicAndTerminalSafe(t *testing.T) {
	parallelIssueStoreTest(t)
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
	parallelIssueStoreTest(t)
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
	parallelIssueStoreTest(t)
	client := NewClient(t.TempDir(), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ctx := context.Background()
	id, err := client.Create(ctx, CreateTaskParams{Title: "diverged", Type: domain.TypeBug, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := client.dbHandle()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,scope_id,state,updated_at) VALUES('project','s',?,?,'running',?)`, id, id, now); err != nil {
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
