package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMailboxObservationProjectionMigrationArtifactDescribesGoAssistedContract(t *testing.T) {
	manifest, err := loadMigrationSQL("migrations/0048_mailbox_observation_projection_cutover.sql")
	require.NoError(t, err)
	for _, required := range []string{
		"Schema effects:",
		"Data effects (transactional SQL phase):",
		"Go-assisted data completion:",
		"source_command=mailbox.cutover",
		"issue ID + event type + mail_event parent + sequence",
		"Failure rolls back both",
		"Validation effects:",
		"Ledger effects:",
		"pinned artifact checksum",
	} {
		require.Truef(t, strings.Contains(manifest, required), "migration manifest missing %q", required)
	}
}

func TestMailboxObservationProjectionCutoverUpgradesRetriesAndReopensIdempotently(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())
	root, err := client.Create(ctx, CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	require.NoError(t, err)
	child, err := client.Create(ctx, CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	require.NoError(t, err)
	marker, err := client.MailboxObservationProjectionCutoverState(ctx)
	require.NoError(t, err)
	require.Equal(t, MailboxObservationProjectionCutover{State: "pending", Version: 1}, marker)

	observedAt := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	payload := func(seq int64, body string) map[string]any {
		return map[string]any{"mail_event": map[string]any{
			"seq": seq, "parent_issue": root, "issue_id": child, "type": "worker-progress", "body": body,
			"created_at": observedAt.Format(time.RFC3339Nano),
		}}
	}
	_, err = client.AppendIssueObservationEvent(ctx, child, IssueObservationEventParams{
		Type: "worker-progress", ObservedAt: observedAt, SourceCommand: "mail.send", Payload: payload(1, "already mirrored"),
	})
	require.NoError(t, err)
	observations := []LegacyMailboxObservation{
		{IssueID: child, EventType: "worker-progress", ObservedAt: observedAt, ParentIssue: root, Sequence: 1, Payload: payload(1, "already mirrored")},
		{IssueID: child, EventType: "worker-progress", ObservedAt: observedAt.Add(time.Second), ParentIssue: root, Sequence: 2, Payload: payload(2, "legacy only")},
	}
	client.mailboxProjectionFailureHook = func(string) error { return errors.New("injected cutover failure") }
	_, err = client.CompleteMailboxObservationProjectionCutover(ctx, observations)
	require.ErrorContains(t, err, "injected cutover failure")
	marker, err = client.MailboxObservationProjectionCutoverState(ctx)
	require.NoError(t, err)
	require.Equal(t, "pending", marker.State)
	assertMailboxProjectionRows(t, client, child, 1)

	client.mailboxProjectionFailureHook = nil
	imported, err := client.CompleteMailboxObservationProjectionCutover(ctx, observations)
	require.NoError(t, err)
	require.Equal(t, 1, imported)
	marker, err = client.MailboxObservationProjectionCutoverState(ctx)
	require.NoError(t, err)
	require.Equal(t, "complete", marker.State)
	require.Equal(t, 1, marker.ImportedCount)
	assertMailboxProjectionRows(t, client, child, 2)
	require.NoError(t, client.CloseDB())

	reopened := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = reopened.CloseDB() })
	imported, err = reopened.CompleteMailboxObservationProjectionCutover(ctx, observations)
	require.NoError(t, err)
	require.Zero(t, imported)
	assertMailboxProjectionRows(t, reopened, child, 2)
}

func TestMailboxObservationProjectionMigrationUpgradesPreviousFixtureAndRejectsDrift(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(dbPath, slog.Default())
	_, err := client.List(ctx)
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	require.NoError(t, err)
	_, err = raw.Exec(`DELETE FROM schema_migrations WHERE id=?; DELETE FROM meta WHERE key=?`, mailboxObservationProjectionCutoverMigrationID, mailboxObservationProjectionCutoverMetaKey)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	upgraded := NewClientAtPath(dbPath, slog.Default())
	marker, err := upgraded.MailboxObservationProjectionCutoverState(ctx)
	require.NoError(t, err)
	require.Equal(t, "pending", marker.State)
	db, err := upgraded.dbHandle()
	require.NoError(t, err)
	var checksum string
	require.NoError(t, db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, mailboxObservationProjectionCutoverMigrationID).Scan(&checksum))
	require.Equal(t, "281f07694377b64c8ad2930add9238b7f397c49f4d0af0a402f804aeac367379", checksum)
	require.NoError(t, upgraded.CloseDB())

	raw, err = sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	require.NoError(t, err)
	_, err = raw.Exec(`DELETE FROM meta WHERE key=?`, mailboxObservationProjectionCutoverMetaKey)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	drifted := NewClientAtPath(dbPath, slog.Default())
	defer drifted.CloseDB()
	_, err = drifted.List(ctx)
	require.ErrorContains(t, err, "missing its cutover marker")
}

func assertMailboxProjectionRows(t *testing.T, client *Client, issueID string, want int) {
	t.Helper()
	events, err := client.ListIssueObservationEvents(context.Background(), issueID, IssueObservationEventListOptions{Limit: 20})
	require.NoError(t, err)
	count := 0
	for _, event := range events {
		if event.Type == "worker-progress" {
			count++
		}
	}
	require.Equal(t, want, count)
}
