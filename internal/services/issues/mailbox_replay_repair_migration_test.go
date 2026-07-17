package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMailboxObservationReplayRepairMigrationUpgradesRollsBackAndRejectsDrift(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(dbPath, slog.Default())
	root, err := seed.Create(ctx, CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	require.NoError(t, err)
	child, err := seed.Create(ctx, CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &root})
	require.NoError(t, err)
	observedAt := time.Date(2026, 7, 15, 7, 24, 25, 711794000, time.UTC)
	payload := map[string]any{
		"publication":                "review_ready_observation_replay.v1",
		"publication_key":            "project:7526",
		"worker_evidence":            map[string]any{"schema": "worker_evidence.v1"},
		"worker_evidence_validation": map[string]any{"found": true, "complete": true},
		"mail_event": map[string]any{
			"seq": 8, "parent_issue": root, "issue_id": child, "type": "worker-integration-ready",
			"body": "evidence", "created_at": observedAt.Format(time.RFC3339Nano),
		},
	}
	event, err := seed.AppendIssueObservationEvent(ctx, child, IssueObservationEventParams{
		Type: "worker-integration-ready", ObservedAt: observedAt, Source: "daemon-observation-replay",
		SourceCommand: "mailbox.cutover", WorktreePath: t.TempDir(), Payload: payload,
	})
	require.NoError(t, err)
	require.NoError(t, seed.CloseDB())

	removeMailboxReplayRepairLedger(t, dbPath)
	failed := NewClientAtPath(dbPath, slog.Default())
	failed.mailboxReplayRepairFailureHook = func(stage string) error {
		require.Equal(t, "after_repair", stage)
		return errors.New("injected repair failure")
	}
	_, err = failed.List(ctx)
	require.ErrorContains(t, err, "injected repair failure")
	require.NoError(t, failed.CloseDB())
	assertMailboxReplayRepairState(t, dbPath, event.ID, false, false)

	upgraded := NewClientAtPath(dbPath, slog.Default())
	_, err = upgraded.List(ctx)
	require.NoError(t, err)
	require.NoError(t, upgraded.CloseDB())
	assertMailboxReplayRepairState(t, dbPath, event.ID, true, true)

	reopened := NewClientAtPath(dbPath, slog.Default())
	_, err = reopened.List(ctx)
	require.NoError(t, err)
	require.NoError(t, reopened.CloseDB())
	assertMailboxReplayRepairState(t, dbPath, event.ID, true, true)

	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE issue_observation_events SET payload_json = json_remove(payload_json, '$.mail_event.payload') WHERE id = ?`, event.ID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	drifted := NewClientAtPath(dbPath, slog.Default())
	_, err = drifted.List(ctx)
	require.NoError(t, err)
	require.NoError(t, drifted.CloseDB())
	assertMailboxReplayRepairState(t, dbPath, event.ID, true, true)

	raw, err = sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE issue_observation_events SET payload_json = json_set(payload_json, '$.mail_event.payload', 'invalid') WHERE id = ?`, event.ID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	scalarDrift := NewClientAtPath(dbPath, slog.Default())
	_, err = scalarDrift.List(ctx)
	require.NoError(t, err)
	require.NoError(t, scalarDrift.CloseDB())
	assertMailboxReplayRepairState(t, dbPath, event.ID, true, true)

	raw, err = sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE issue_observation_events SET payload_json = json_remove(payload_json, '$.mail_event') WHERE id = ?`, event.ID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	malformed := NewClientAtPath(dbPath, slog.Default())
	defer malformed.CloseDB()
	_, err = malformed.List(ctx)
	require.ErrorContains(t, err, "mail_event payload is missing")
}

func TestMailboxObservationReplayRepairManifestDescribesImmutableContract(t *testing.T) {
	manifest, err := loadMigrationSQL("migrations/0053_mailbox_observation_replay_repair.manifest.sql")
	require.NoError(t, err)
	for _, required := range []string{
		"Schema effects:", "Data effects", "source_command=mailbox.cutover",
		"Validation effects:", "Rollback and idempotency effects:", "Ledger effects:",
		"immutable artifact checksum",
	} {
		require.Contains(t, manifest, required)
	}
}

func removeMailboxReplayRepairLedger(t *testing.T, dbPath string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	require.NoError(t, err)
	_, err = raw.Exec(`DELETE FROM schema_migrations WHERE id = ?`, mailboxObservationReplayRepairMigrationID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
}

func assertMailboxReplayRepairState(t *testing.T, dbPath string, eventID int64, wantPayload, wantLedger bool) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	require.NoError(t, err)
	defer raw.Close()
	var hasPayload bool
	require.NoError(t, raw.QueryRow(`SELECT COALESCE(json_type(payload_json, '$.mail_event.payload') = 'object', 0) FROM issue_observation_events WHERE id = ?`, eventID).Scan(&hasPayload))
	require.Equal(t, wantPayload, hasPayload)
	var ledger bool
	require.NoError(t, raw.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id = ?)`, mailboxObservationReplayRepairMigrationID).Scan(&ledger))
	require.Equal(t, wantLedger, ledger)
	if wantLedger {
		var checksum string
		require.NoError(t, raw.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id = ?`, mailboxObservationReplayRepairMigrationID).Scan(&checksum))
		require.Equal(t, "d2b70f9828f9e642d4ec6191205500713bf51c0378cf3c261bf54fadf976779e", checksum)
	}
	if wantPayload {
		var derivedCount int
		require.NoError(t, raw.QueryRow(`
			SELECT COUNT(*) FROM json_each((SELECT json_extract(payload_json, '$.mail_event.payload') FROM issue_observation_events WHERE id = ?))
			WHERE key IN ('worker_evidence', 'worker_evidence_validation')
		`, eventID).Scan(&derivedCount))
		require.Zero(t, derivedCount)
	}
}
