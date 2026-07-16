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
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND length(artifact_checksum)=64`, agentInputDeliveryMigrationID).Scan(&rows); err != nil || rows != 1 {
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

func TestAgentInputDeliveryIntentRetryKeepsFirstExpiry(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	firstExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "same", IntentKey: "retry", ExpiresAt: firstExpiry}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.ExpiresAt = firstExpiry.Add(time.Minute)
	intent, err := client.EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !intent.Request.ExpiresAt.Equal(firstExpiry) {
		t.Fatalf("expiry=%s, want first durable expiry %s", intent.Request.ExpiresAt, firstExpiry)
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
	if begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", "lease", second.LeaseToken, now.Add(2*time.Second)); err != nil || !begun {
		t.Fatalf("begin submission begun=%v err=%v", begun, err)
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

func TestAgentInputDeliveryMigrationRejectsIndexDefinitionDrift(t *testing.T) {
	tests := []struct {
		name       string
		index      string
		definition string
	}{
		{name: "pending column order", index: "idx_agent_input_delivery_pending", definition: `CREATE INDEX idx_agent_input_delivery_pending ON agent_input_delivery_intents(state, project_id, expires_at, created_at) WHERE state IN ('queued','leased')`},
		{name: "pending predicate", index: "idx_agent_input_delivery_pending", definition: `CREATE INDEX idx_agent_input_delivery_pending ON agent_input_delivery_intents(project_id, state, expires_at, created_at) WHERE state = 'queued'`},
		{name: "pending uniqueness", index: "idx_agent_input_delivery_pending", definition: `CREATE UNIQUE INDEX idx_agent_input_delivery_pending ON agent_input_delivery_intents(project_id, state, expires_at, created_at) WHERE state IN ('queued','leased')`},
		{name: "incarnation column order", index: "idx_agent_input_delivery_incarnation", definition: `CREATE INDEX idx_agent_input_delivery_incarnation ON agent_input_delivery_intents(project_id, session_id, agent_incarnation, logical_pane_id, state)`},
		{name: "incarnation uniqueness", index: "idx_agent_input_delivery_incarnation", definition: `CREATE UNIQUE INDEX idx_agent_input_delivery_incarnation ON agent_input_delivery_intents(project_id, session_id, logical_pane_id, agent_incarnation, state)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			client := NewClientAtPath(path, nil)
			if _, err := client.Create(context.Background(), CreateTaskParams{Title: "seed", Type: domain.TypeTask}); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseDB(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`DROP INDEX ` + tt.index); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(tt.definition); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := NewClientAtPath(path, nil)
			defer reopened.CloseDB()
			if _, err = reopened.List(context.Background()); err == nil || !strings.Contains(err.Error(), "non-canonical definition") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAgentInputDeliveryMigrationRejectsWeakenedStateConstraints(t *testing.T) {
	tests := []struct {
		name        string
		canonical   string
		replacement string
	}{
		{
			name:        "delivered acknowledgement equation",
			canonical:   `CHECK ((state = 'delivered') = (acknowledgement_token IS NOT NULL AND acknowledged_at IS NOT NULL))`,
			replacement: `CHECK (state != 'delivered' OR acknowledgement_token IS NOT NULL)`,
		},
		{
			name:        "leased ownership equation",
			canonical:   `CHECK ((state IN ('leased','ambiguous')) = (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))`,
			replacement: `CHECK (state != 'leased' OR lease_token IS NOT NULL)`,
		},
	}
	artifact, err := migrationFiles.ReadFile("migrations/0051_agent_input_delivery.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			client := NewClientAtPath(path, nil)
			if _, err := client.Create(context.Background(), CreateTaskParams{Title: "seed", Type: domain.TypeTask}); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseDB(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{
				`DROP INDEX idx_agent_input_delivery_pending`,
				`DROP INDEX idx_agent_input_delivery_incarnation`,
				`ALTER TABLE agent_input_delivery_intents RENAME TO agent_input_delivery_intents_old`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			weakened := strings.Replace(string(artifact), tt.canonical, tt.replacement, 1)
			if weakened == string(artifact) {
				t.Fatal("canonical constraint not found in artifact")
			}
			if _, err := db.Exec(weakened); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TABLE agent_input_delivery_intents_old`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := NewClientAtPath(path, nil)
			defer reopened.CloseDB()
			if _, err := reopened.List(context.Background()); err == nil || !strings.Contains(err.Error(), "missing constraint") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAgentInputDeliveryAmbiguousSubmissionDoesNotAutomaticallyRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: "ambiguous", ExpiresAt: now.Add(time.Hour)}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", "ambiguous", "daemon-a", now, time.Second)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	if begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", "ambiguous", claimed.LeaseToken, now); err != nil || !begun {
		t.Fatalf("begin=%v err=%v", begun, err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, nil)
	defer reopened.CloseDB()
	pending, err := reopened.ListPendingAgentInputDeliveryIntents(ctx, "p", now.Add(2*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("ambiguous intent was automatically retryable: %+v", pending)
	}
	if err := reopened.ReleaseAgentInputDeliveryIntent(ctx, "p", "ambiguous", claimed.LeaseToken, false, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	intent, err := reopened.EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil || intent.State != "ambiguous" {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	if err := reopened.ResolveAgentInputDeliverySubmissionRefusal(ctx, "p", "ambiguous", claimed.LeaseToken, false, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	intent, err = reopened.EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil || intent.State != "queued" {
		t.Fatalf("rejected intent=%+v err=%v", intent, err)
	}
}

func TestAgentInputDeliveryAmbiguousSubmissionStillExpires(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	now := time.Now().UTC()
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: "ambiguous-expiry", ExpiresAt: now.Add(100 * time.Millisecond)}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", request.IntentKey, "daemon-a", now, time.Second)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	if begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", request.IntentKey, claimed.LeaseToken, now); err != nil || !begun {
		t.Fatalf("begin=%v err=%v", begun, err)
	}
	time.Sleep(120 * time.Millisecond)
	intent, err := client.EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil || intent.State != "expired" {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
}

func TestAgentInputDeliveryMigrationHistoricalUpgradeRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, nil)
	seed.migrationCeiling = issueObservationEventSearchMigrationID
	issueID, err := seed.Create(ctx, CreateTaskParams{Title: "sentinel", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var previousMarker, currentMarker, currentTable int
	if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, issueObservationEventSearchMigrationID).Scan(&previousMarker); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryMigrationID).Scan(&currentMarker); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_input_delivery_intents'`).Scan(&currentTable); err != nil {
		t.Fatal(err)
	}
	if previousMarker != 1 || currentMarker != 0 || currentTable != 0 {
		t.Fatalf("previous production fixture marker0050=%d marker0051=%d table0051=%d", previousMarker, currentMarker, currentTable)
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
