package state

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestAcquireAdvisorSessionIsSingletonAcrossStores(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	stores := []*RuntimeStateStore{NewRuntimeStateStoreAtPath(dbPath, nil), NewRuntimeStateStoreAtPath(dbPath, nil)}
	var wg sync.WaitGroup
	results := make(chan AdvisorSession, 2)
	errs := make(chan error, 2)
	for i, store := range stores {
		wg.Add(1)
		go func(index int, store *RuntimeStateStore) {
			defer wg.Done()
			got, _, err := store.AcquireAdvisorSession(ctx, "project", "request-1", "issue-1", []string{"advisor-a", "advisor-b"}[index])
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}(i, store)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("acquire advisor session: %v", err)
	}
	var sessionID string
	for result := range results {
		if sessionID == "" {
			sessionID = result.SessionID
		}
		if result.SessionID != sessionID {
			t.Fatalf("session ids differ: %q != %q", result.SessionID, sessionID)
		}
	}
}

func TestAdvisorSessionMetadataSurvivesProjectionRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), nil)
	want := Session{ID: "advisor-request-1", IssueID: "issue-1", Role: SessionRoleAdvisor, ScopeKind: SessionScopeInteraction, ScopeID: "request-1", State: SessionStateStarting}
	if err := store.UpsertSessionState(ctx, "project", want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetSessionState(ctx, "project", want.ID)
	if err != nil || !found {
		t.Fatalf("get projection: found=%v err=%v", found, err)
	}
	if got.Role != SessionRoleAdvisor || got.ScopeKind != SessionScopeInteraction || got.ScopeID != "request-1" {
		t.Fatalf("metadata = role=%q scope=%q/%q", got.Role, got.ScopeKind, got.ScopeID)
	}
	refresh := Session{ID: want.ID, IssueID: want.IssueID, State: SessionStateRunning}
	if err := store.UpsertSessionState(ctx, "project", refresh); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSessionStates(ctx, "project", []Session{refresh}); err != nil {
		t.Fatal(err)
	}
	got, found, err = store.GetSessionState(ctx, "project", want.ID)
	if err != nil || !found {
		t.Fatalf("get refreshed projection: found=%v err=%v", found, err)
	}
	if got.Role != SessionRoleAdvisor || got.ScopeKind != SessionScopeInteraction || got.ScopeID != "request-1" {
		t.Fatalf("metadata after untyped refresh = role=%q scope=%q/%q", got.Role, got.ScopeKind, got.ScopeID)
	}
}
