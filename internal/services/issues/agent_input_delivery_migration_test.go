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
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, agentInputDeliveryFencingMigrationID, agentInputDeliveryFencingMigrationChecksum).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("fencing ledger rows=%d err=%v", rows, err)
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
	sessionLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "daemon-b", now.Add(2*time.Second), time.Second)
	if err != nil || !acquired {
		t.Fatalf("session lease=%+v acquired=%v err=%v", sessionLease, acquired, err)
	}
	if _, begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", "lease", second.LeaseToken, "s", "inc", sessionLease.LeaseOwner, "wrong-session-fence", now.Add(2*time.Second), time.Second); err != nil || begun {
		t.Fatalf("wrong session fence begun=%v err=%v", begun, err)
	}
	if stillLeased, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil || stillLeased.State != "leased" {
		t.Fatalf("failed atomic fence changed intent=%+v err=%v", stillLeased, err)
	}
	if _, begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", "lease", second.LeaseToken, "s", "inc", sessionLease.LeaseOwner, sessionLease.LeaseToken, now.Add(2*time.Second), time.Second); err != nil || !begun {
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

func TestAgentInputDeliveryTimestampPredicatesCompareChronologically(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	base := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	now := base.Add(500 * time.Millisecond)
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body"}

	past := request
	past.IntentKey = "integral-expiry"
	past.ExpiresAt = base
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, past); err != nil {
		t.Fatal(err)
	}
	future := request
	future.IntentKey = "fractional-expiry"
	future.ExpiresAt = now.Add(100 * time.Microsecond)
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, future); err != nil {
		t.Fatal(err)
	}
	pending, err := client.ListPendingAgentInputDeliveryIntents(ctx, "p", now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Request.IntentKey != future.IntentKey {
		t.Fatalf("pending=%+v, want only fractional future expiry", pending)
	}
	expired, err := client.EnsureAgentInputDeliveryIntent(ctx, past)
	if err != nil || expired.State != "expired" {
		t.Fatalf("integral expiry intent=%+v err=%v", expired, err)
	}

	leaseBase := base.Add(time.Hour)
	for _, key := range []string{"integral-lease", "fractional-lease"} {
		leased := request
		leased.IntentKey = key
		leased.ExpiresAt = leaseBase.Add(time.Hour)
		if _, err := client.EnsureAgentInputDeliveryIntent(ctx, leased); err != nil {
			t.Fatal(err)
		}
	}
	first, claimed, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", "integral-lease", "daemon-a", leaseBase, time.Second)
	if err != nil || !claimed {
		t.Fatalf("integral first claim=%+v claimed=%v err=%v", first, claimed, err)
	}
	second, claimed, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", "integral-lease", "daemon-b", leaseBase.Add(1500*time.Millisecond), time.Second)
	if err != nil || !claimed || second.LeaseToken == first.LeaseToken {
		t.Fatalf("integral expired takeover=%+v claimed=%v err=%v", second, claimed, err)
	}
	first, claimed, err = client.ClaimAgentInputDeliveryIntent(ctx, "p", "fractional-lease", "daemon-a", leaseBase, time.Second+100*time.Microsecond)
	if err != nil || !claimed {
		t.Fatalf("fractional first claim=%+v claimed=%v err=%v", first, claimed, err)
	}
	if _, claimed, err = client.ClaimAgentInputDeliveryIntent(ctx, "p", "fractional-lease", "daemon-b", leaseBase.Add(time.Second), time.Second); err != nil || claimed {
		t.Fatalf("fractional live lease claimed=%v err=%v", claimed, err)
	}
}

func TestAgentInputDeliverySessionLeaseIsCrossClientAndSessionScoped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	clients := []*Client{NewClientAtPath(path, slog.Default()), NewClientAtPath(path, slog.Default())}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.CloseDB()
		}
	})
	// Open and migrate before the second client races the same durable table.
	if _, err := clients[0].dbHandle(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, acquired, err := clients[0].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "daemon-a", now, time.Second)
	if err != nil || !acquired || first.LeaseToken == "" {
		t.Fatalf("first lease=%+v acquired=%v err=%v", first, acquired, err)
	}
	if lease, acquired, err := clients[1].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "daemon-b", now.Add(500*time.Millisecond), time.Second); err != nil || acquired || lease.LeaseToken != "" {
		t.Fatalf("overlap lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	otherIncarnation, acquired, err := clients[1].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc-2", "daemon-b", now, time.Second)
	if err != nil || acquired || otherIncarnation.LeaseToken != "" {
		t.Fatalf("other incarnation overlapped session lease=%+v acquired=%v err=%v", otherIncarnation, acquired, err)
	}
	if _, renewed, err := clients[0].RenewAgentInputDeliverySessionLease(ctx, "p", "s", "inc", first.LeaseOwner, first.LeaseToken, now.Add(750*time.Millisecond), time.Second); err != nil || !renewed {
		t.Fatalf("renewed=%v err=%v", renewed, err)
	}
	if _, acquired, err := clients[1].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "daemon-b", now.Add(1250*time.Millisecond), time.Second); err != nil || acquired {
		t.Fatalf("renewed lease overlap acquired=%v err=%v", acquired, err)
	}
	if err := clients[0].ReleaseAgentInputDeliverySessionLease(ctx, "p", "s", "inc", first.LeaseOwner, "wrong"); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := clients[1].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "daemon-b", now.Add(1500*time.Millisecond), time.Second); err != nil || acquired {
		t.Fatalf("wrong-token release acquired=%v err=%v", acquired, err)
	}
	if err := clients[0].ReleaseAgentInputDeliverySessionLease(ctx, "p", "s", "inc", first.LeaseOwner, first.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if lease, acquired, err := clients[1].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc-2", "daemon-b", now.Add(1500*time.Millisecond), time.Second); err != nil || !acquired || lease.LeaseToken == "" || lease.AgentIncarnation != "inc-2" {
		t.Fatalf("post-release lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
}

func TestAgentInputDeliverySessionLeaseFencesUseExactRFC3339NanoBoundaries(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	base := time.Date(2026, 7, 17, 1, 2, 3, 123456789, time.UTC)
	window := 100 * time.Microsecond

	original, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "claim", "inc-old", "owner-old", base, window)
	if err != nil || !acquired {
		t.Fatalf("original claim=%+v acquired=%v err=%v", original, acquired, err)
	}
	if lease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "claim", "inc-new", "owner-new", original.LeaseExpires.Add(-time.Nanosecond), time.Second); err != nil || acquired || lease.LeaseToken != "" {
		t.Fatalf("sub-nanosecond-early takeover lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	takeover, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "claim", "inc-new", "owner-new", original.LeaseExpires, time.Second)
	if err != nil || !acquired || !takeover.TakeoverPending || takeover.AgentIncarnation != original.AgentIncarnation || takeover.LeaseToken != original.LeaseToken {
		t.Fatalf("exact-expiry takeover=%+v acquired=%v err=%v", takeover, acquired, err)
	}
	if _, renewed, err := client.RenewAgentInputDeliverySessionLease(ctx, "p", "claim", original.AgentIncarnation, original.LeaseOwner, original.LeaseToken, original.LeaseExpires, time.Second); err != nil || renewed {
		t.Fatalf("superseded owner renewed=%v err=%v", renewed, err)
	}
	if err := client.ReleaseAgentInputDeliverySessionLease(ctx, "p", "claim", original.AgentIncarnation, original.LeaseOwner, original.LeaseToken); err != nil {
		t.Fatalf("superseded owner release: %v", err)
	}
	if _, renewed, err := client.RenewAgentInputDeliverySessionLease(ctx, "p", "claim", takeover.AgentIncarnation, takeover.LeaseOwner, takeover.LeaseToken, original.LeaseExpires.Add(time.Nanosecond), time.Second); err != nil || !renewed {
		t.Fatalf("superseded owner deleted takeover fence renewed=%v err=%v", renewed, err)
	}
	completed, ok, err := client.CompleteAgentInputDeliverySessionLeaseTakeover(ctx, "p", "claim", takeover.AgentIncarnation, takeover.LeaseToken, "inc-new", "owner-new", original.LeaseExpires.Add(time.Nanosecond), time.Second)
	if err != nil || !ok || completed.AgentIncarnation != "inc-new" || completed.LeaseToken == takeover.LeaseToken {
		t.Fatalf("completed takeover=%+v ok=%v err=%v", completed, ok, err)
	}

	renewLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "renew", "inc", "owner", base, window)
	if err != nil || !acquired {
		t.Fatalf("renew fixture=%+v acquired=%v err=%v", renewLease, acquired, err)
	}
	renewedExpiry, renewed, err := client.RenewAgentInputDeliverySessionLease(ctx, "p", "renew", "inc", "owner", renewLease.LeaseToken, renewLease.LeaseExpires.Add(-time.Nanosecond), window)
	if err != nil || !renewed {
		t.Fatalf("sub-nanosecond-live renewal renewed=%v err=%v", renewed, err)
	}
	if _, renewed, err := client.RenewAgentInputDeliverySessionLease(ctx, "p", "renew", "inc", "owner", renewLease.LeaseToken, renewedExpiry, window); err != nil || renewed {
		t.Fatalf("exact-expiry renewal renewed=%v err=%v", renewed, err)
	}

	recoveryLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "recovery", "inc", "dead", base, window)
	if err != nil || !acquired {
		t.Fatalf("recovery fixture=%+v acquired=%v err=%v", recoveryLease, acquired, err)
	}
	if observed, acquired, err := client.ClaimAgentInputDeliverySessionLeaseRecovery(ctx, "p", "recovery", "inc", recoveryLease.LeaseToken, "recovery-owner", recoveryLease.LeaseExpires.Add(-time.Nanosecond), time.Second); err != nil || acquired || observed.LeaseToken != recoveryLease.LeaseToken {
		t.Fatalf("sub-nanosecond-early recovery=%+v acquired=%v err=%v", observed, acquired, err)
	}
	if recovered, acquired, err := client.ClaimAgentInputDeliverySessionLeaseRecovery(ctx, "p", "recovery", "inc", recoveryLease.LeaseToken, "recovery-owner", recoveryLease.LeaseExpires, time.Second); err != nil || !acquired || recovered.LeaseToken != recoveryLease.LeaseToken || recovered.LeaseOwner != "recovery-owner" {
		t.Fatalf("exact-expiry recovery=%+v acquired=%v err=%v", recovered, acquired, err)
	}

	ownerRequest := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "begin-owner", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc-old"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: "begin-old-owner"}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, ownerRequest); err != nil {
		t.Fatal(err)
	}
	ownerIntent, claimed, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", ownerRequest.IntentKey, "owner-old", base, time.Second)
	if err != nil || !claimed {
		t.Fatalf("old-owner intent claimed=%v err=%v", claimed, err)
	}
	ownerLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", ownerRequest.SessionID, "inc-old", "owner-old", base, window)
	if err != nil || !acquired {
		t.Fatalf("old-owner lease=%+v acquired=%v err=%v", ownerLease, acquired, err)
	}
	ownerTakeover, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", ownerRequest.SessionID, "inc-new", "owner-new", ownerLease.LeaseExpires, time.Second)
	if err != nil || !acquired || !ownerTakeover.TakeoverPending {
		t.Fatalf("owner takeover=%+v acquired=%v err=%v", ownerTakeover, acquired, err)
	}
	if _, begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", ownerRequest.IntentKey, ownerIntent.LeaseToken, ownerRequest.SessionID, "inc-old", "owner-old", ownerLease.LeaseToken, ownerLease.LeaseExpires.Add(time.Nanosecond), window); err != nil || begun {
		t.Fatalf("superseded owner began submission=%v err=%v", begun, err)
	}

	begin := func(sessionID, intentKey string, at time.Time) bool {
		request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: sessionID, Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: intentKey}
		if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
			t.Fatal(err)
		}
		intent, claimed, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", intentKey, "owner", base, time.Second)
		if err != nil || !claimed {
			t.Fatalf("intent %s claimed=%v err=%v", intentKey, claimed, err)
		}
		lease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", sessionID, "inc", "owner", base, window)
		if err != nil || !acquired {
			t.Fatalf("session %s acquired=%v err=%v", sessionID, acquired, err)
		}
		_, begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", intentKey, intent.LeaseToken, sessionID, "inc", lease.LeaseOwner, lease.LeaseToken, at, window)
		if err != nil {
			t.Fatalf("begin %s: %v", intentKey, err)
		}
		return begun
	}
	if !begin("begin-live", "begin-live", base.Add(window-time.Nanosecond)) {
		t.Fatal("sub-nanosecond-live begin submission was rejected")
	}
	if begin("begin-expired", "begin-expired", base.Add(window)) {
		t.Fatal("exact-expiry begin submission was accepted")
	}
}

func TestAgentInputDeliveryRecoveryLeaseDoesNotRecreateMissingAuthority(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	original, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "dead-daemon", now.Add(-2*time.Minute), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("original lease=%+v acquired=%v err=%v", original, acquired, err)
	}
	if err := client.ReleaseAgentInputDeliverySessionLease(ctx, "p", "s", "inc", original.LeaseOwner, original.LeaseToken); err != nil {
		t.Fatal(err)
	}
	recovery, acquired, err := client.ClaimAgentInputDeliverySessionLeaseRecovery(ctx, "p", "s", "inc", original.LeaseToken, "recovery-daemon", now, time.Minute)
	if err != nil || acquired || recovery.LeaseToken != "" {
		t.Fatalf("missing authority was recreated: lease=%+v acquired=%v err=%v", recovery, acquired, err)
	}
	ordinary, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "new-inc", "new-daemon", now, time.Minute)
	if err != nil || !acquired || ordinary.LeaseToken == "" {
		t.Fatalf("ordinary claim after missing recovery lease=%+v acquired=%v err=%v", ordinary, acquired, err)
	}
}

func TestAgentInputDeliverySubmissionFenceTimeoutRemainsAmbiguousAndAllowsExpiredTakeover(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	clients := []*Client{NewClientAtPath(path, slog.Default()), NewClientAtPath(path, slog.Default())}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.CloseDB()
		}
	})
	now := time.Now().UTC()
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc-old"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: "fenced-timeout", ExpiresAt: now.Add(time.Hour)}
	if _, err := clients[0].EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	intent, claimed, err := clients[0].ClaimAgentInputDeliveryIntent(ctx, "p", request.IntentKey, "daemon-old", now, time.Second)
	if err != nil || !claimed {
		t.Fatalf("intent=%+v claimed=%v err=%v", intent, claimed, err)
	}
	lease, acquired, err := clients[0].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc-old", "daemon-old", now, 100*time.Millisecond)
	if err != nil || !acquired {
		t.Fatalf("lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	if _, begun, err := clients[0].BeginAgentInputDeliverySubmission(ctx, "p", request.IntentKey, intent.LeaseToken, "s", "inc-old", lease.LeaseOwner, lease.LeaseToken, now.Add(10*time.Millisecond), 100*time.Millisecond); err != nil || !begun {
		t.Fatalf("begin=%v err=%v", begun, err)
	}
	db, err := clients[0].dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_delivery_session_leases SET updated_at=updated_at WHERE project_id='p' AND session_id='s'`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	busyObserved := false
	clients[1].sqliteBusyWait = func(context.Context, time.Duration) error {
		busyObserved = true
		return errors.New("stop after observed SQLite busy barrier")
	}
	_, renewed, renewErr := clients[1].RenewAgentInputDeliverySessionLease(ctx, "p", "s", "inc-old", lease.LeaseOwner, lease.LeaseToken, now.Add(20*time.Millisecond), 100*time.Millisecond)
	if renewErr == nil || renewed || !busyObserved || !IsSQLiteBusy(renewErr) {
		_ = tx.Rollback()
		t.Fatalf("blocked renewal renewed=%v busy_observed=%v err=%v", renewed, busyObserved, renewErr)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	loaded, err := clients[1].EnsureAgentInputDeliveryIntent(ctx, request)
	if err != nil || loaded.State != "ambiguous" {
		t.Fatalf("ambiguous intent=%+v err=%v", loaded, err)
	}
	newLease, acquired, err := clients[1].ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc-new", "daemon-new", now.Add(250*time.Millisecond), time.Second)
	if err != nil || !acquired || newLease.PreviousLeaseToken != lease.LeaseToken || newLease.PreviousAgentIncarnation != "inc-old" {
		t.Fatalf("takeover lease=%+v acquired=%v err=%v", newLease, acquired, err)
	}
	if _, renewed, err := clients[0].RenewAgentInputDeliverySessionLease(ctx, "p", "s", "inc-old", lease.LeaseOwner, lease.LeaseToken, now.Add(260*time.Millisecond), time.Second); err != nil || renewed {
		t.Fatalf("old fence renewed=%v err=%v", renewed, err)
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
		{name: "pending quoted literal bytes", index: "idx_agent_input_delivery_pending", definition: `CREATE INDEX idx_agent_input_delivery_pending ON agent_input_delivery_intents(project_id, state, expires_at, created_at) WHERE state IN ('QUEUED','leased')`},
		{name: "pending uniqueness", index: "idx_agent_input_delivery_pending", definition: `CREATE UNIQUE INDEX idx_agent_input_delivery_pending ON agent_input_delivery_intents(project_id, state, expires_at, created_at) WHERE state IN ('queued','leased')`},
		{name: "incarnation column order", index: "idx_agent_input_delivery_incarnation", definition: `CREATE INDEX idx_agent_input_delivery_incarnation ON agent_input_delivery_intents(project_id, session_id, agent_incarnation, logical_pane_id, state)`},
		{name: "incarnation uniqueness", index: "idx_agent_input_delivery_incarnation", definition: `CREATE UNIQUE INDEX idx_agent_input_delivery_incarnation ON agent_input_delivery_intents(project_id, session_id, logical_pane_id, agent_incarnation, state)`},
		{name: "session lease wrong column", index: "idx_agent_input_session_lease_expiry", definition: `CREATE INDEX idx_agent_input_session_lease_expiry ON agent_input_delivery_session_leases(updated_at)`},
		{name: "session lease uniqueness", index: "idx_agent_input_session_lease_expiry", definition: `CREATE UNIQUE INDEX idx_agent_input_session_lease_expiry ON agent_input_delivery_session_leases(lease_expires_at)`},
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

func TestAgentInputDeliveryMigrationRejectsIncarnationScopedSessionLeasePrimaryKey(t *testing.T) {
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
	statements := []string{
		`DROP INDEX idx_agent_input_session_lease_expiry`,
		`DROP TABLE agent_input_delivery_session_leases`,
		`CREATE TABLE agent_input_delivery_session_leases (
			project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
			session_id TEXT NOT NULL CHECK (trim(session_id) <> ''),
			agent_incarnation TEXT NOT NULL CHECK (trim(agent_incarnation) <> ''),
			lease_owner TEXT NOT NULL CHECK (trim(lease_owner) <> ''),
			lease_token TEXT NOT NULL CHECK (trim(lease_token) <> ''),
			lease_expires_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id, agent_incarnation)
		)`,
		`CREATE INDEX idx_agent_input_session_lease_expiry ON agent_input_delivery_session_leases(lease_expires_at)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, nil)
	defer reopened.CloseDB()
	if _, err := reopened.List(context.Background()); err == nil || !strings.Contains(err.Error(), "non-canonical definition") {
		t.Fatalf("err=%v", err)
	}
}

func TestNormalizeExactSQLiteDDLPreservesQuotedBytes(t *testing.T) {
	canonical := `CREATE INDEX "QuotedName" ON agent_input_delivery_intents(state) WHERE state IN ('queued','leased')`
	if got, want := normalizeExactSQLiteDDL("  create\nindex \"QuotedName\" on agent_input_delivery_intents ( state ) where state in ( 'queued' , 'leased' )  "), normalizeExactSQLiteDDL(canonical); got != want {
		t.Fatalf("unquoted formatting was not normalized: got=%q want=%q", got, want)
	}
	for _, drifted := range []string{
		`CREATE INDEX "quotedname" ON agent_input_delivery_intents(state) WHERE state IN ('queued','leased')`,
		`CREATE INDEX "QuotedName" ON agent_input_delivery_intents(state) WHERE state IN ('QUEUED','leased')`,
	} {
		if normalizeExactSQLiteDDL(drifted) == normalizeExactSQLiteDDL(canonical) {
			t.Fatalf("quoted-byte drift normalized away: %s", drifted)
		}
	}
}

func TestExactSQLiteDDLReferencesIdentifierAcrossSQLiteQuoting(t *testing.T) {
	const target = "agent_input_delivery_intents"
	for _, test := range []struct {
		name string
		ddl  string
		want bool
	}{
		{name: "bare", ddl: `CREATE VIEW v AS SELECT * FROM agent_input_delivery_intents`, want: true},
		{name: "double quoted", ddl: `CREATE VIEW v AS SELECT * FROM "agent_input_delivery_intents"`, want: true},
		{name: "backtick quoted", ddl: "CREATE VIEW v AS SELECT * FROM `agent_input_delivery_intents`", want: true},
		{name: "bracket quoted", ddl: `CREATE VIEW v AS SELECT * FROM [agent_input_delivery_intents]`, want: true},
		{name: "single quoted compatibility identifier", ddl: `CREATE VIEW v AS SELECT * FROM 'agent_input_delivery_intents'`, want: true},
		{name: "case insensitive identifier", ddl: `CREATE VIEW v AS SELECT * FROM AGENT_INPUT_DELIVERY_INTENTS`, want: true},
		{name: "line comment", ddl: "CREATE VIEW v AS SELECT 1 -- agent_input_delivery_intents\n", want: false},
		{name: "block comment", ddl: `CREATE VIEW v AS SELECT 1 /* agent_input_delivery_intents */`, want: false},
		{name: "non-exact string literal", ddl: `CREATE VIEW v AS SELECT 'prefix agent_input_delivery_intents suffix'`, want: false},
		{name: "different identifier", ddl: `CREATE VIEW v AS SELECT * FROM agent_input_delivery_intents_archive`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := exactSQLiteDDLReferencesIdentifier(test.ddl, target); got != test.want {
				t.Fatalf("exactSQLiteDDLReferencesIdentifier(%q)=%v, want %v", test.ddl, got, test.want)
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
	artifact, err := migrationFiles.ReadFile("migrations/0053_agent_input_delivery_fencing.sql")
	if err != nil {
		t.Fatal(err)
	}
	artifactText := string(artifact)
	createStart := strings.Index(artifactText, "CREATE TABLE agent_input_delivery_intents (")
	copyStart := strings.Index(artifactText, "INSERT INTO agent_input_delivery_intents(")
	finishStart := strings.Index(artifactText, "DROP TABLE agent_input_delivery_intents_0052;")
	if createStart < 0 || copyStart <= createStart || finishStart <= copyStart {
		t.Fatal("0053 artifact does not contain the expected rebuild phases")
	}
	canonicalTable := artifactText[createStart:copyStart]
	finishSchema := artifactText[finishStart:]
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
				`DROP INDEX idx_agent_input_session_lease_expiry`,
				`DROP TABLE agent_input_delivery_session_leases`,
				`ALTER TABLE agent_input_delivery_intents RENAME TO agent_input_delivery_intents_0052`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			weakened := strings.Replace(canonicalTable, tt.canonical, tt.replacement, 1)
			if weakened == canonicalTable {
				t.Fatal("canonical constraint not found in artifact")
			}
			if _, err := db.Exec(weakened + "\n" + finishSchema); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := NewClientAtPath(path, nil)
			defer reopened.CloseDB()
			if _, err := reopened.List(context.Background()); err == nil || !strings.Contains(err.Error(), "non-canonical definition") {
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
	sessionLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "daemon-a", now, time.Second)
	if err != nil || !acquired {
		t.Fatalf("session lease=%+v acquired=%v err=%v", sessionLease, acquired, err)
	}
	if _, begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", "ambiguous", claimed.LeaseToken, "s", "inc", sessionLease.LeaseOwner, sessionLease.LeaseToken, now, time.Second); err != nil || !begun {
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
	if _, err := client.List(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "body", IntentKey: "ambiguous-expiry", ExpiresAt: now.Add(time.Minute)}
	if _, err := client.EnsureAgentInputDeliveryIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := client.ClaimAgentInputDeliveryIntent(ctx, "p", request.IntentKey, "daemon-a", now, time.Second)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	sessionLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "s", "inc", "daemon-a", now, time.Second)
	if err != nil || !acquired {
		t.Fatalf("session lease=%+v acquired=%v err=%v", sessionLease, acquired, err)
	}
	if _, begun, err := client.BeginAgentInputDeliverySubmission(ctx, "p", request.IntentKey, claimed.LeaseToken, "s", "inc", sessionLease.LeaseOwner, sessionLease.LeaseToken, now, time.Second); err != nil || !begun {
		t.Fatalf("begin=%v err=%v", begun, err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_input_delivery_intents SET expires_at=? WHERE project_id=? AND intent_key=?`, formatTimestamp(now.Add(-time.Second)), request.ProjectID, request.IntentKey); err != nil {
		t.Fatal(err)
	}
	if pending, err := client.ListPendingAgentInputDeliveryIntents(ctx, "p", now, 10); err != nil || len(pending) != 0 {
		t.Fatalf("expired pending=%+v err=%v", pending, err)
	}
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
		t.Fatalf("previous production fixture marker0050=%d marker0052=%d table0052=%d", previousMarker, currentMarker, currentTable)
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

func TestAgentInputDeliveryFencingMigrationUpgradesImmutable0052AndRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, nil)
	seed.migrationCeiling = agentInputDeliveryMigrationID
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	now := formatTimestamp(time.Now().UTC())
	if _, err := db.Exec(`INSERT INTO agent_input_delivery_intents(project_id,intent_key,session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,tool,message_kind,payload,state,created_at,updated_at) VALUES('p','sentinel','s','agent','7',42,'inc','codex','session_message','payload','queued',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, agentInputDeliveryMigrationID).Scan(&checksum); err != nil || checksum != agentInputDeliveryMigrationChecksum {
		t.Fatalf("immutable 0052 checksum=%q err=%v", checksum, err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	failed := NewClientAtPath(path, nil)
	failed.agentInputMigrationFailureHook = func(stage string) error {
		if stage == "after_fencing_schema" {
			return errors.New("interrupted")
		}
		return nil
	}
	if _, err := failed.dbHandle(); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err=%v", err)
	}
	_ = failed.CloseDB()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var marker, sentinel, sessionLeaseTable int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryFencingMigrationID).Scan(&marker)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM agent_input_delivery_intents WHERE intent_key='sentinel' AND payload='payload'`).Scan(&sentinel)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_input_delivery_session_leases'`).Scan(&sessionLeaseTable)
	_ = raw.Close()
	if marker != 0 || sentinel != 1 || sessionLeaseTable != 0 {
		t.Fatalf("rollback marker0053=%d sentinel=%d lease_table=%d", marker, sentinel, sessionLeaseTable)
	}

	retried := NewClientAtPath(path, nil)
	defer retried.CloseDB()
	retryDB, err := retried.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAgentInputDeliverySchema(ctx, retryDB); err != nil {
		t.Fatal(err)
	}
	if err := retryDB.QueryRow(`SELECT COUNT(*) FROM agent_input_delivery_intents WHERE intent_key='sentinel' AND payload='payload'`).Scan(&sentinel); err != nil || sentinel != 1 {
		t.Fatalf("upgraded sentinel=%d err=%v", sentinel, err)
	}
}

func TestAgentInputDeliveryFencingMigrationRejectsPredecessorDriftBeforeRebuild(t *testing.T) {
	const canonicalLeasedConstraint = `CHECK ((state = 'leased') = (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))`
	const weakenedLeasedConstraint = `CHECK (state != 'leased' OR lease_token IS NOT NULL)`
	for _, test := range []struct {
		name              string
		mutate            func(*testing.T, *sql.DB)
		preservedType     string
		preservedName     string
		preservedSQL      string
		preservedValueSQL string
		preservedValue    string
	}{
		{
			name: "table constraint",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`PRAGMA writable_schema=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE sqlite_master SET sql=replace(sql, ?, ?) WHERE type='table' AND name='agent_input_delivery_intents'`, canonicalLeasedConstraint, weakenedLeasedConstraint); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA writable_schema=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA schema_version=999`); err != nil {
					t.Fatal(err)
				}
			},
			preservedSQL: "state != 'leased' or lease_token is not null",
		},
		{
			name: "pending index",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`DROP INDEX idx_agent_input_delivery_pending`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE INDEX idx_agent_input_delivery_pending ON agent_input_delivery_intents(state, project_id)`); err != nil {
					t.Fatal(err)
				}
			},
			preservedType: "index",
			preservedName: "idx_agent_input_delivery_pending",
			preservedSQL:  "(state, project_id)",
		},
		{
			name: "pending quoted literal bytes",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`DROP INDEX idx_agent_input_delivery_pending`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE INDEX idx_agent_input_delivery_pending ON agent_input_delivery_intents(project_id, state, expires_at, created_at) WHERE state IN ('QUEUED','leased')`); err != nil {
					t.Fatal(err)
				}
			},
			preservedType: "index",
			preservedName: "idx_agent_input_delivery_pending",
			preservedSQL:  "where state in ('queued','leased')",
		},
		{
			name: "extra sentinel index",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`CREATE INDEX review_sentinel_index ON agent_input_delivery_intents(updated_at)`); err != nil {
					t.Fatal(err)
				}
			},
			preservedType: "index",
			preservedName: "review_sentinel_index",
			preservedSQL:  "review_sentinel_index on agent_input_delivery_intents(updated_at)",
		},
		{
			name: "extra sentinel trigger",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`CREATE TRIGGER review_sentinel_trigger AFTER UPDATE ON agent_input_delivery_intents BEGIN SELECT 1; END`); err != nil {
					t.Fatal(err)
				}
			},
			preservedType: "trigger",
			preservedName: "review_sentinel_trigger",
			preservedSQL:  "review_sentinel_trigger after update on agent_input_delivery_intents",
		},
		{
			name: "extra sentinel column",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`ALTER TABLE agent_input_delivery_intents ADD COLUMN review_sentinel TEXT NOT NULL DEFAULT 'default'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE agent_input_delivery_intents SET review_sentinel='preserve-me' WHERE intent_key='sentinel'`); err != nil {
					t.Fatal(err)
				}
			},
			preservedSQL:      "review_sentinel text not null default 'default'",
			preservedValueSQL: `SELECT review_sentinel FROM agent_input_delivery_intents WHERE intent_key='sentinel'`,
			preservedValue:    "preserve-me",
		},
		{
			name: "external view dependency",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`CREATE VIEW review_delivery_view AS SELECT payload FROM "agent_input_delivery_intents"`); err != nil {
					t.Fatal(err)
				}
			},
			preservedType:     "view",
			preservedName:     "review_delivery_view",
			preservedSQL:      `select payload from "agent_input_delivery_intents"`,
			preservedValueSQL: `SELECT payload FROM review_delivery_view WHERE payload='payload'`,
			preservedValue:    "payload",
		},
		{
			name: "external trigger dependency",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`CREATE TABLE review_delivery_audit(id INTEGER PRIMARY KEY)`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TRIGGER review_delivery_trigger AFTER INSERT ON review_delivery_audit BEGIN SELECT COUNT(*) FROM [agent_input_delivery_intents]; END`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO review_delivery_audit(id) VALUES(7)`); err != nil {
					t.Fatal(err)
				}
			},
			preservedType:     "trigger",
			preservedName:     "review_delivery_trigger",
			preservedSQL:      "select count(*) from [agent_input_delivery_intents]",
			preservedValueSQL: `SELECT CAST(id AS TEXT) FROM review_delivery_audit`,
			preservedValue:    "7",
		},
		{
			name: "external foreign key dependency",
			mutate: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`CREATE TABLE review_delivery_reference(
					project_id TEXT NOT NULL,
					intent_key TEXT NOT NULL,
					FOREIGN KEY(project_id, intent_key) REFERENCES "agent_input_delivery_intents"(project_id, intent_key)
				)`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO review_delivery_reference(project_id,intent_key) VALUES('p','sentinel')`); err != nil {
					t.Fatal(err)
				}
			},
			preservedType:     "table",
			preservedName:     "review_delivery_reference",
			preservedSQL:      `references "agent_input_delivery_intents"(project_id, intent_key)`,
			preservedValueSQL: `SELECT intent_key FROM review_delivery_reference`,
			preservedValue:    "sentinel",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			seed := NewClientAtPath(path, nil)
			seed.migrationCeiling = agentInputDeliveryMigrationID
			db, err := seed.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			now := formatTimestamp(time.Now().UTC())
			if _, err := db.Exec(`INSERT INTO agent_input_delivery_intents(project_id,intent_key,session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,tool,message_kind,payload,state,created_at,updated_at) VALUES('p','sentinel','s','agent','7',42,'inc','codex','session_message','payload','queued',?,?)`, now, now); err != nil {
				t.Fatal(err)
			}
			if err := seed.CloseDB(); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, raw)
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			candidate := NewClientAtPath(path, nil)
			if _, err := candidate.dbHandle(); err == nil || !strings.Contains(err.Error(), "validate migration 0053_agent_input_delivery_fencing predecessor") {
				t.Fatalf("candidate open error=%v, want predecessor drift refusal", err)
			}
			_ = candidate.CloseDB()

			raw, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var marker, sentinel int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryFencingMigrationID).Scan(&marker); err != nil {
				t.Fatal(err)
			}
			if err := raw.QueryRow(`SELECT COUNT(*) FROM agent_input_delivery_intents WHERE intent_key='sentinel' AND payload='payload'`).Scan(&sentinel); err != nil {
				t.Fatal(err)
			}
			var schemaSQL string
			objectType := test.preservedType
			objectName := test.preservedName
			if objectType == "" {
				objectType = "table"
				objectName = "agent_input_delivery_intents"
			}
			if err := raw.QueryRow(`SELECT lower(sql) FROM sqlite_master WHERE type=? AND name=?`, objectType, objectName).Scan(&schemaSQL); err != nil {
				t.Fatal(err)
			}
			if marker != 0 || sentinel != 1 || !strings.Contains(schemaSQL, test.preservedSQL) {
				t.Fatalf("rollback marker0053=%d sentinel=%d schema=%q", marker, sentinel, schemaSQL)
			}
			if test.preservedValueSQL != "" {
				var value string
				if err := raw.QueryRow(test.preservedValueSQL).Scan(&value); err != nil {
					t.Fatal(err)
				}
				if value != test.preservedValue {
					t.Fatalf("preserved value=%q, want %q", value, test.preservedValue)
				}
			}
		})
	}
}

func TestAgentInputDeliveryFencingMigrationRejectsSameConnectionTempDependenciesBeforeDDL(t *testing.T) {
	for _, test := range []struct {
		name       string
		createSQL  string
		objectType string
		objectName string
	}{
		{
			name:       "temp view",
			createSQL:  `CREATE TEMP VIEW review_temp_delivery_view AS SELECT payload FROM main.agent_input_delivery_intents`,
			objectType: "view",
			objectName: "review_temp_delivery_view",
		},
		{
			name:       "temp trigger",
			createSQL:  `CREATE TEMP TRIGGER review_temp_delivery_trigger AFTER INSERT ON agent_input_delivery_intents BEGIN SELECT count(*) FROM agent_input_delivery_intents; END`,
			objectType: "trigger",
			objectName: "review_temp_delivery_trigger",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			seed := NewClientAtPath(path, nil)
			seed.migrationCeiling = agentInputDeliveryMigrationID
			db, err := seed.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			now := formatTimestamp(time.Now().UTC())
			if _, err := db.Exec(`INSERT INTO agent_input_delivery_intents(project_id,intent_key,session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,tool,message_kind,payload,state,created_at,updated_at) VALUES('p','sentinel','s','agent','7',42,'inc','codex','session_message','payload','queued',?,?)`, now, now); err != nil {
				t.Fatal(err)
			}
			if err := seed.CloseDB(); err != nil {
				t.Fatal(err)
			}

			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			raw.SetMaxOpenConns(1)
			if _, err := raw.Exec(test.createSQL); err != nil {
				t.Fatal(err)
			}
			candidate := NewClientAtPath(path, nil)
			err = candidate.applyAgentInputDeliveryFencingMigration(context.Background(), raw, agentInputDeliveryFencingMigrationID)
			if err == nil || !strings.Contains(err.Error(), "predecessor dependencies") || !strings.Contains(err.Error(), "temp."+test.objectName) {
				t.Fatalf("migration error=%v, want exact TEMP dependency refusal", err)
			}

			var marker, sentinel, tempObject int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, agentInputDeliveryFencingMigrationID).Scan(&marker); err != nil {
				t.Fatal(err)
			}
			if err := raw.QueryRow(`SELECT COUNT(*) FROM agent_input_delivery_intents WHERE intent_key='sentinel' AND payload='payload'`).Scan(&sentinel); err != nil {
				t.Fatal(err)
			}
			if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_temp_master WHERE type=? AND name=?`, test.objectType, test.objectName).Scan(&tempObject); err != nil {
				t.Fatal(err)
			}
			if marker != 0 || sentinel != 1 || tempObject != 1 {
				t.Fatalf("preflight mutated state: marker=%d sentinel=%d temp_object=%d", marker, sentinel, tempObject)
			}
		})
	}
}

func TestAgentInputDeliveryMigrationRejectsAppliedExtraColumnDrift(t *testing.T) {
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
	if _, err := db.Exec(`ALTER TABLE agent_input_delivery_session_leases ADD COLUMN review_sentinel TEXT`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, nil)
	defer reopened.CloseDB()
	if _, err := reopened.List(context.Background()); err == nil || !strings.Contains(err.Error(), "non-canonical definition") {
		t.Fatalf("reopen error=%v, want final-schema drift refusal", err)
	}
}

func TestAgentInputDeliveryMigrationRejectsAppliedSchemaObjectInventoryDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		create string
	}{
		{
			name:   "extra index",
			create: `CREATE INDEX review_sentinel_index ON agent_input_delivery_session_leases(updated_at)`,
		},
		{
			name:   "extra trigger",
			create: `CREATE TRIGGER review_sentinel_trigger AFTER UPDATE ON agent_input_delivery_session_leases BEGIN SELECT 1; END`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			if _, err := db.Exec(test.create); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := NewClientAtPath(path, nil)
			defer reopened.CloseDB()
			if _, err := reopened.List(context.Background()); err == nil || !strings.Contains(err.Error(), "schema object inventory") {
				t.Fatalf("reopen error=%v, want final object-inventory drift refusal", err)
			}
		})
	}
}
