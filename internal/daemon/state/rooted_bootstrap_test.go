package state

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRootedBootstrapAcknowledgementAuthorityRefreshesAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	first := NewRuntimeStateStoreAtPath(path, slog.Default())
	second := NewRuntimeStateStoreAtPath(path, slog.Default())
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { _ = second.Close() })
	scope, err := domain.RootedOrchestrationScope("root")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity("project", scope)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ack := RootedBootstrapAcknowledgement{Identity: identity, SessionID: "az-root", PromptHash: "prompt-a", RuntimeNonce: "nonce-a", AcknowledgedAt: now, UpdatedAt: now}
	firstAuthority := NewRootedBootstrapAcknowledgementAuthority(first)
	secondAuthority := NewRootedBootstrapAcknowledgementAuthority(second)
	if err := firstAuthority.Acknowledge(ctx, ack); err != nil {
		t.Fatal(err)
	}
	if got, found, err := secondAuthority.Get(ctx, identity); err != nil || !found || got.RuntimeNonce != "nonce-a" {
		t.Fatalf("second authority acknowledgement = %+v found=%t err=%v", got, found, err)
	}
	if err := firstAuthority.Invalidate(ctx, identity, ack.SessionID); err != nil {
		t.Fatal(err)
	}
	if got, found, err := secondAuthority.Get(ctx, identity); err != nil || found {
		t.Fatalf("stale acknowledgement survived refresh = %+v found=%t err=%v", got, found, err)
	}
	ack.RuntimeNonce, ack.UpdatedAt = "nonce-b", now.Add(time.Second)
	if err := firstAuthority.Acknowledge(ctx, ack); err != nil {
		t.Fatal(err)
	}
	if got, found, err := secondAuthority.FindBySession(ctx, identity.ProjectID, ack.SessionID); err != nil || !found || got.RuntimeNonce != "nonce-b" {
		t.Fatalf("refreshed acknowledgement by session = %+v found=%t err=%v", got, found, err)
	}
}
