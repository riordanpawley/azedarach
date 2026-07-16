package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testisolation"
)

func TestRealProjectDatabaseMigrationClones(t *testing.T) {
	rawPaths := strings.TrimSpace(os.Getenv("AZEDARACH_PROJECT_DB_CLONES"))
	if rawPaths == "" {
		t.Skip("AZEDARACH_PROJECT_DB_CLONES is not set")
	}
	for _, path := range filepath.SplitList(rawPaths) {
		path := path
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path))), func(t *testing.T) {
			if err := testisolation.CheckDatabaseClone(path, "."); err != nil {
				t.Fatalf("refuse unsafe project database clone before SQLite open: %v", err)
			}
			ctx := context.Background()
			beforeDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			var beforeIssues, beforeCustom, beforeDecisions, beforeDecisionAudits, beforeMigrations, beforeDecisionIdempotency, beforeAgentInput int
			if err = beforeDB.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&beforeIssues); err != nil {
				t.Fatal(err)
			}
			_ = beforeDB.QueryRow(`SELECT COUNT(*) FROM board_views WHERE built_in=0`).Scan(&beforeCustom)
			_ = beforeDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&beforeMigrations)
			_ = beforeDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, decisionIdempotencyMigrationID).Scan(&beforeDecisionIdempotency)
			_ = beforeDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryMigrationID).Scan(&beforeAgentInput)
			_ = beforeDB.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&beforeDecisions)
			_ = beforeDB.QueryRow(`SELECT COUNT(*) FROM decision_audit_log`).Scan(&beforeDecisionAudits)
			_ = beforeDB.Close()

			client := NewClientAtPath(path, slog.Default())
			db, err := client.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			if err = validateHumanAuthorityProjectionRevisionTriggers(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err = validateDecisionPropagationOutboxSchema(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err = validateAgentInputDeliverySchema(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err = validateProjectionDeltaAuthoritySchema(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err = validateDecisionIdempotencySchema(ctx, db); err != nil {
				t.Fatal(err)
			}
			var checksum string
			var ledgerRows int
			if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, projectionDeltaAuthorityMigrationID, projectionDeltaAuthorityChecksum).Scan(&ledgerRows); err != nil || ledgerRows != 1 {
				t.Fatalf("projection delta ledger rows=%d err=%v", ledgerRows, err)
			}
			if err = db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, humanAuthorityProjectionMigrationID).Scan(&checksum); err != nil || checksum != "ac3a48512b2e6e9c018d58a68db24a2465e9d172139d22f8378f69677073a0ab" {
				t.Fatalf("checksum=%q err=%v", checksum, err)
			}
			if err = db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, decisionPropagationOutboxMigrationID).Scan(&checksum); err != nil || checksum != "a12c44ba35156d71fbcd88a9d78e4cdb234e75e7e4aef5f896c8b1182ada858d" {
				t.Fatalf("decision outbox checksum=%q err=%v", checksum, err)
			}
			if err = db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, agentInputDeliveryMigrationID).Scan(&checksum); err != nil || checksum != "92d3be503bc193101944f1bc1ecee38656f04c3be7399a1b88356ae6add42f55" {
				t.Fatalf("agent input checksum=%q err=%v", checksum, err)
			}
			if err = db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, decisionIdempotencyMigrationID).Scan(&checksum); err != nil || checksum != "86d5400fe33bbc19e7e848bc232335809f76d85e4d45a6e45f6bc7ff77547f47" {
				t.Fatalf("decision idempotency checksum=%q err=%v", checksum, err)
			}
			if !rootedBootstrapTableExists(t, db) {
				t.Fatal("rooted bootstrap acknowledgement table missing after clone migration")
			}
			if err = db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, rootedBootstrapAcknowledgementMigrationID).Scan(&checksum); err != nil || checksum != "b54bdf5ec3f6af17c91e1625582ac58e66e47948cea68ee73db88d4e8df6f161" {
				t.Fatalf("rooted bootstrap acknowledgement checksum=%q err=%v", checksum, err)
			}
			projectIDs := []string{"default"}
			if rows, queryErr := db.Query(`SELECT DISTINCT project_id FROM board_views`); queryErr == nil {
				projectIDs = projectIDs[:0]
				for rows.Next() {
					var id string
					if err = rows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					projectIDs = append(projectIDs, id)
				}
				_ = rows.Close()
				if len(projectIDs) == 0 {
					projectIDs = []string{"default"}
				}
			}
			for _, projectID := range projectIDs {
				if _, err = client.ListBoardViews(ctx, projectID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = client.ExportProjection(ctx, projectIDs[0]); err != nil {
				t.Fatal(err)
			}
			if _, err = client.List(ctx); err != nil {
				t.Fatal(err)
			}
			var afterIssues, afterCustom, afterDecisions, afterDecisionAudits, afterMigrations, afterDecisionIdempotency, afterAgentInput, intentRows int
			if err = db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&afterIssues); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM board_views WHERE built_in=0`).Scan(&afterCustom); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&afterMigrations); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, decisionIdempotencyMigrationID).Scan(&afterDecisionIdempotency); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryMigrationID).Scan(&afterAgentInput); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM agent_input_delivery_intents`).Scan(&intentRows); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&afterDecisions); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM decision_audit_log`).Scan(&afterDecisionAudits); err != nil {
				t.Fatal(err)
			}
			if beforeIssues != afterIssues || beforeCustom != afterCustom || beforeDecisions != afterDecisions || beforeDecisionAudits != afterDecisionAudits {
				t.Fatalf("row preservation issues=%d/%d custom_views=%d/%d decisions=%d/%d decision_audits=%d/%d", beforeIssues, afterIssues, beforeCustom, afterCustom, beforeDecisions, afterDecisions, beforeDecisionAudits, afterDecisionAudits)
			}
			if afterDecisionIdempotency != 1 || afterAgentInput != 1 || intentRows != 0 || afterMigrations != beforeMigrations+(1-beforeDecisionIdempotency)+(1-beforeAgentInput) {
				t.Fatalf("migration summary before=%d/0051:%d/0052:%d after=%d/0051:%d/0052:%d intent_rows=%d", beforeMigrations, beforeDecisionIdempotency, beforeAgentInput, afterMigrations, afterDecisionIdempotency, afterAgentInput, intentRows)
			}
			t.Logf("real clone summary path=%s issues=%d custom_views=%d migrations=%d->%d decision_marker=%d->%d agent_input_marker=%d->%d intent_rows=%d", path, afterIssues, afterCustom, beforeMigrations, afterMigrations, beforeDecisionIdempotency, afterDecisionIdempotency, beforeAgentInput, afterAgentInput, intentRows)
			if err = client.CloseDB(); err != nil {
				t.Fatal(err)
			}
			reopened := NewClientAtPath(path, slog.Default())
			reopenedDB, err := reopened.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			if !rootedBootstrapTableExists(t, reopenedDB) {
				t.Fatal("rooted bootstrap acknowledgement table missing after clone reopen")
			}
			if err = reopenedDB.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, rootedBootstrapAcknowledgementMigrationID).Scan(&checksum); err != nil || checksum != "b54bdf5ec3f6af17c91e1625582ac58e66e47948cea68ee73db88d4e8df6f161" {
				t.Fatalf("reopened rooted bootstrap acknowledgement checksum=%q err=%v", checksum, err)
			}
			if _, err = reopened.ExportProjection(ctx, projectIDs[0]); err != nil {
				t.Fatal(err)
			}
			if err = reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, projectionDeltaAuthorityMigrationID, projectionDeltaAuthorityChecksum).Scan(&ledgerRows); err != nil || ledgerRows != 1 {
				t.Fatalf("projection delta ledger rows after reopen=%d err=%v", ledgerRows, err)
			}
			if err = reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, decisionIdempotencyMigrationID, "86d5400fe33bbc19e7e848bc232335809f76d85e4d45a6e45f6bc7ff77547f47").Scan(&ledgerRows); err != nil || ledgerRows != 1 {
				t.Fatalf("decision idempotency ledger rows after reopen=%d err=%v", ledgerRows, err)
			}
			if err = reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, agentInputDeliveryMigrationID, "92d3be503bc193101944f1bc1ecee38656f04c3be7399a1b88356ae6add42f55").Scan(&ledgerRows); err != nil || ledgerRows != 1 {
				t.Fatalf("agent input delivery ledger rows after reopen=%d err=%v", ledgerRows, err)
			}
			_ = reopened.CloseDB()
		})
	}
}

func TestHumanAuthorityProjectionMigrationRollsBackAndRetriesHistoricalUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO issues(id,title,status,disposition,engagement,visibility,lifecycle_state,closed_outcome,review_state,priority,issue_type,created_at,updated_at) VALUES('kept','Kept','open','ready','idle','live','open','none','none',2,'task','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, trigger := range humanAuthorityProjectionRevisionTriggers {
		if _, err = db.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`DELETE FROM schema_migrations WHERE id=?`, humanAuthorityProjectionMigrationID); err != nil {
		t.Fatal(err)
	}
	if err = seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected interruption")
	failed := NewClientAtPath(path, slog.Default())
	failed.humanAuthorityMigrationFailureHook = func(stage string) error {
		if stage == "after_schema" {
			return injected
		}
		return nil
	}
	if _, err = failed.dbHandle(); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("migration error = %v, want rollback", err)
	}
	_ = failed.CloseDB()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var marker, triggers, kept int
	if err = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, humanAuthorityProjectionMigrationID).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'projection_source_revision_issue_observations_%' OR type='trigger' AND name LIKE 'projection_source_revision_interactions_%'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM issues WHERE id='kept'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if marker != 0 || triggers != 0 || kept != 1 {
		t.Fatalf("rollback marker=%d triggers=%d kept=%d", marker, triggers, kept)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	retried := NewClientAtPath(path, slog.Default())
	retriedDB, err := retried.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer retried.CloseDB()
	if err = validateHumanAuthorityProjectionRevisionTriggers(ctx, retriedDB); err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err = retriedDB.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, humanAuthorityProjectionMigrationID).Scan(&checksum); err != nil || checksum != "ac3a48512b2e6e9c018d58a68db24a2465e9d172139d22f8378f69677073a0ab" {
		t.Fatalf("checksum=%q err=%v", checksum, err)
	}
}

func TestHumanAuthorityProjectionMigrationRejectsAppliedTriggerDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	missing := humanAuthorityProjectionRevisionTriggers[0]
	if _, err = db.Exec(`DROP TRIGGER ` + missing); err != nil {
		t.Fatal(err)
	}
	if err = seed.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	if _, err = reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("drift error=%v, want missing trigger %s", err, missing)
	}
	_ = reopened.CloseDB()
}
