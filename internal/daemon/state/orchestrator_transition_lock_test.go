package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestOrchestratorScopeTransitionSerializesStoreInstances(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	first := NewRuntimeStateStoreAtPath(dbPath, nil)
	second := NewRuntimeStateStoreAtPath(dbPath, nil)
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})
	identity, err := domain.NewOrchestratorIdentity("project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.WithOrchestratorScopeTransition(context.Background(), identity, func(lockCtx context.Context) error {
			if err := first.WithOrchestratorScopeTransition(lockCtx, identity, func(context.Context) error { return nil }); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	secondRan := false
	err = second.WithOrchestratorScopeTransition(ctx, identity, func(context.Context) error {
		secondRan = true
		return nil
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("second transition error = %v, want deadline exceeded", err)
	}
	if secondRan {
		t.Fatal("second store entered the same scope transition concurrently")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first transition: %v", err)
	}

	otherScope, err := domain.NewOrchestratorIdentity("project", domain.OrchestrationScope{Kind: domain.OrchestrationScopeRooted, RootIssueID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.WithOrchestratorScopeTransition(context.Background(), otherScope, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("independent scope transition: %v", err)
	}
}
