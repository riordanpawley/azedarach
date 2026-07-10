package state_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestOrchestratorLifecycleMigrationAfterRuntimeBootstrap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	identity, err := domain.NewOrchestratorIdentity("project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireOrchestratorScopeLease(context.Background(), identity, "orch", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	client := issues.NewClientAtPath(dbPath, slog.Default())
	if _, err := client.List(context.Background()); err != nil {
		t.Fatalf("issue migrations after runtime bootstrap: %v", err)
	}
}
