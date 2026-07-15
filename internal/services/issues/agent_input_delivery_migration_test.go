package issues

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestAgentInputDeliveryMigrationFreshReopenAndDurableIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	ctx := context.Background()
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "private multiline\nbody", IntentKey: "intent", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, nil)
	defer reopened.CloseDB()
	intent, err := reopened.EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != "queued" || intent.Request.Payload != request.Payload {
		t.Fatalf("intent=%+v", intent)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='0050_agent_input_delivery' AND length(artifact_checksum)=64`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("ledger rows=%d err=%v", rows, err)
	}
}

func TestAgentInputDeliveryIntentConflictDoesNotReplacePayloadOrIncarnation(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	expires := time.Now().UTC().Add(time.Hour)
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "first", IntentKey: "same", ExpiresAt: expires}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.Payload = "second"
	request.Target.AgentIncarnation = "other"
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != ErrAgentInputIntentConflict {
		t.Fatalf("err=%v", err)
	}
}

func TestAgentInputDeliveryLeaseExpiryAndExactAcknowledgementFence(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	now := time.Now().UTC()
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: "lease", ExpiresAt: now.Add(time.Hour)}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	first, claimed, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", "lease", "daemon-a", now, time.Second)
	if err != nil || !claimed {
		t.Fatalf("first=%+v claimed=%v err=%v", first, claimed, err)
	}
	if _, claimed, err = client.ClaimAgentInputDeliveryIntent(ctx, "p", "lease", "daemon-b", now.Add(500*time.Millisecond), time.Second); err != nil || claimed {
		t.Fatalf("live lease claimed=%v err=%v", claimed, err)
	}
	second, claimed, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", "lease", "daemon-b", now.Add(2*time.Second), time.Second)
	if err != nil || !claimed || second.LeaseToken == first.LeaseToken {
		t.Fatalf("takeover=%+v claimed=%v err=%v", second, claimed, err)
	}
	if ok, err := client.AcknowledgeAgentInputDeliveryIntent(ctx, "p", "lease", "wrong-incarnation", second.LeaseToken, "ack", now.Add(2*time.Second)); err != nil || ok {
		t.Fatalf("wrong incarnation ok=%v err=%v", ok, err)
	}
	if ok, err := client.AcknowledgeAgentInputDeliveryIntent(ctx, "p", "lease", "inc", "wrong-lease", "ack", now.Add(2*time.Second)); err != nil || ok {
		t.Fatalf("wrong lease ok=%v err=%v", ok, err)
	}
	if ok, err := client.AcknowledgeAgentInputDeliveryIntent(ctx, "p", "lease", "inc", second.LeaseToken, "ack", now.Add(2*time.Second)); err != nil || !ok {
		t.Fatalf("exact ack ok=%v err=%v", ok, err)
	}
}

func TestAgentInputDeliveryExpiryIsDurablyTerminal(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	now := time.Now().UTC()
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: "expired", ExpiresAt: now.Add(-time.Second)}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListPendingAgentInputDeliveryIntents(ctx, "p", now, 10); err != nil {
		t.Fatal(err)
	}
	intent, err := client.EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != "expired" {
		t.Fatalf("state=%q", intent.State)
	}
}

func TestAgentInputDeliveryMigrationRejectsLedgerSchemaDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	ctx := context.Background()
	if _, err := client.Create(ctx, CreateTaskParams{Title: "seed", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP INDEX idx_agent_input_delivery_pending`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, nil)
	defer reopened.CloseDB()
	if _, err = reopened.List(ctx); err == nil {
		t.Fatal("expected applied-ledger schema drift to fail closed")
	}
}

func TestAgentInputDeliveryMigrationHistoricalUpgradeRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, nil)
	seed.migrationCeiling = "0049_managed_agent_incarnations"
	issueID, err := seed.Create(ctx, CreateTaskParams{Title: "sentinel", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var previousMarker, currentMarker, currentTable int
	if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='0049_managed_agent_incarnations'`).Scan(&previousMarker); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryMigrationID).Scan(&currentMarker); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_input_delivery_intents'`).Scan(&currentTable); err != nil {
		t.Fatal(err)
	}
	if previousMarker != 1 || currentMarker != 0 || currentTable != 0 {
		t.Fatalf("previous production fixture marker0049=%d marker0050=%d table0050=%d", previousMarker, currentMarker, currentTable)
	}
	if err = seed.CloseDB(); err != nil {
		t.Fatal(err)
	}
	failed := NewClientAtPath(path, nil)
	failed.agentInputMigrationFailureHook = func(string) error { return errors.New("interrupted") }
	if _, err = failed.dbHandle(); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err=%v", err)
	}
	_ = failed.CloseDB()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var tables, markers, issueCount int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_input_delivery_intents'`).Scan(&tables)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryMigrationID).Scan(&markers)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM issues WHERE id=?`, issueID).Scan(&issueCount)
	_ = raw.Close()
	if tables != 0 || markers != 0 || issueCount != 1 {
		t.Fatalf("rollback table=%d marker=%d issues=%d", tables, markers, issueCount)
	}
	retried := NewClientAtPath(path, nil)
	defer retried.CloseDB()
	retryDB, err := retried.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err = validateAgentInputDeliverySchema(ctx, retryDB); err != nil {
		t.Fatal(err)
	}
}
