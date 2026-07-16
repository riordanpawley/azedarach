package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const testValidationToken = "test-validation-secret"

func TestValidationLeaseSerializesAggregateAndPreservesSharedConcurrency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	storeA := NewAtPath(path, nil)
	storeB := NewAtPath(path, nil)
	t.Cleanup(func() { _ = storeA.Close(); _ = storeB.Close() })
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	ttl := time.Minute
	acquire := func(store *SQLiteStore, id string, class domain.ValidationClass) domain.ValidationRequest {
		t.Helper()
		profile, command := id, "go test"
		if class == domain.ValidationClassSafe {
			profile, command = "safe-git-diff", "git diff --check"
		}
		request, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: class, Profile: profile, Command: command, SourceRevision: "abc123", TTL: ttl}, now)
		require.NoError(t, err)
		return request
	}

	assert.Equal(t, domain.ValidationRequestActive, acquire(storeA, "shared-a", domain.ValidationClassShared).State)
	assert.Equal(t, domain.ValidationRequestActive, acquire(storeB, "shared-b", domain.ValidationClassShared).State)
	assert.Equal(t, domain.ValidationRequestQueued, acquire(storeA, "aggregate", domain.ValidationClassAggregate).State)
	assert.Equal(t, domain.ValidationRequestActive, acquire(storeB, "shared-late", domain.ValidationClassShared).State, "queued aggregate must not convoy later focused work")
	assert.Equal(t, domain.ValidationRequestQueued, acquire(storeA, "shared-bounded", domain.ValidationClassShared).State, "only one focused request may bypass the oldest aggregate")
	assert.Equal(t, domain.ValidationRequestActive, acquire(storeB, "safe", domain.ValidationClassSafe).State)

	_, err := storeA.FinishValidation(ctx, "shared-a", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(time.Second), ttl)
	require.NoError(t, err)
	_, err = storeB.FinishValidation(ctx, "shared-b", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(2*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err := storeA.ValidationSnapshot(ctx, "project", now.Add(2*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"shared-late", "safe"}, validationRequestIDs(snapshot.Active))
	assert.Equal(t, []string{"aggregate", "shared-bounded"}, validationRequestIDs(snapshot.Queued))

	_, err = storeB.FinishValidation(ctx, "shared-late", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(3*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err = storeB.ValidationSnapshot(ctx, "project", now.Add(3*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"aggregate", "safe"}, validationRequestIDs(snapshot.Active))
	assert.Equal(t, []string{"shared-bounded"}, validationRequestIDs(snapshot.Queued))

	_, err = storeA.FinishValidation(ctx, "aggregate", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(4*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err = storeA.ValidationSnapshot(ctx, "project", now.Add(4*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"shared-bounded", "safe"}, validationRequestIDs(snapshot.Active))
}

func TestValidationLeaseSharedBypassBoundSurvivesStoreReplacement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	ttl := time.Minute
	request := func(id string, class domain.ValidationClass) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: class, Profile: id, Command: "go test", SourceRevision: "abc123", TTL: ttl}
	}

	first := NewAtPath(path, nil)
	owner, err := first.AcquireValidation(ctx, request("owner", domain.ValidationClassShared), now)
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestActive, owner.State)
	waiter, err := first.AcquireValidation(ctx, request("aggregate", domain.ValidationClassAggregate), now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestQueued, waiter.State)
	bypass, err := first.AcquireValidation(ctx, request("bypass", domain.ValidationClassShared), now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestActive, bypass.State)
	require.NoError(t, first.Close())

	restarted := NewAtPath(path, nil)
	t.Cleanup(func() { _ = restarted.Close() })
	bounded, err := restarted.AcquireValidation(ctx, request("bounded", domain.ValidationClassShared), now.Add(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestQueued, bounded.State)

	_, err = restarted.FinishValidation(ctx, "owner", testValidationToken, domain.ValidationRequestCancelled, "cancelled", domain.ValidationEvidence{}, now.Add(4*time.Second), ttl)
	require.NoError(t, err)
	_, err = restarted.FinishValidation(ctx, "bypass", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(5*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err := restarted.ValidationSnapshot(ctx, "project", now.Add(5*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"aggregate"}, validationRequestIDs(snapshot.Active))
	assert.Equal(t, []string{"bounded"}, validationRequestIDs(snapshot.Queued))
}

func TestValidationLeaseCancellingQueuedAggregateReopensSharedAdmission(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	ttl := time.Minute
	request := func(id string, class domain.ValidationClass) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: class, Profile: id, Command: "go test", SourceRevision: "abc123", TTL: ttl}
	}

	_, err := store.AcquireValidation(ctx, request("owner", domain.ValidationClassShared), now)
	require.NoError(t, err)
	_, err = store.AcquireValidation(ctx, request("aggregate", domain.ValidationClassAggregate), now.Add(time.Second))
	require.NoError(t, err)
	_, err = store.AcquireValidation(ctx, request("bypass", domain.ValidationClassShared), now.Add(2*time.Second))
	require.NoError(t, err)
	bounded, err := store.AcquireValidation(ctx, request("bounded", domain.ValidationClassShared), now.Add(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestQueued, bounded.State)

	_, err = store.FinishValidation(ctx, "aggregate", testValidationToken, domain.ValidationRequestCancelled, "cancelled while queued", domain.ValidationEvidence{}, now.Add(4*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err := store.ValidationSnapshot(ctx, "project", now.Add(4*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner", "bypass", "bounded"}, validationRequestIDs(snapshot.Active))
	assert.Empty(t, snapshot.Queued)
}

func TestValidationLeaseConcurrentDaemonsActivateExactlyOneAggregate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	stores := []*SQLiteStore{NewAtPath(path, nil), NewAtPath(path, nil)}
	t.Cleanup(func() {
		for _, store := range stores {
			_ = store.Close()
		}
	})
	now := time.Now().UTC()
	for _, store := range stores {
		_, err := store.ValidationSnapshot(ctx, "project", now, time.Minute)
		require.NoError(t, err)
	}
	start := make(chan struct{})
	results := make(chan domain.ValidationRequest, len(stores))
	errs := make(chan error, len(stores))
	var ready sync.WaitGroup
	ready.Add(len(stores))
	for i, store := range stores {
		go func(i int, store *SQLiteStore) {
			ready.Done()
			<-start
			request, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: fmt.Sprintf("aggregate-%d", i), LeaseToken: testValidationToken, ProjectID: "project", IssueID: fmt.Sprintf("issue-%d", i), Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: time.Minute}, now)
			results <- request
			errs <- err
		}(i, store)
	}
	ready.Wait()
	close(start)
	for range stores {
		require.NoError(t, <-errs)
	}
	states := map[domain.ValidationRequestState]int{}
	for range stores {
		states[(<-results).State]++
	}
	assert.Equal(t, 1, states[domain.ValidationRequestActive])
	assert.Equal(t, 1, states[domain.ValidationRequestQueued])
}

func TestValidationLeaseConcurrentStoresAdmitExactlyOneSharedBypass(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	stores := []*SQLiteStore{NewAtPath(path, nil), NewAtPath(path, nil), NewAtPath(path, nil)}
	t.Cleanup(func() {
		for _, store := range stores {
			_ = store.Close()
		}
	})
	now := time.Now().UTC()
	request := func(id string, class domain.ValidationClass) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: class, Profile: id, Command: "go test", SourceRevision: "abc123", TTL: time.Minute}
	}
	_, err := stores[0].AcquireValidation(ctx, request("owner", domain.ValidationClassShared), now)
	require.NoError(t, err)
	queued, err := stores[0].AcquireValidation(ctx, request("aggregate", domain.ValidationClassAggregate), now)
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestQueued, queued.State)

	start := make(chan struct{})
	results := make(chan domain.ValidationRequest, len(stores))
	errs := make(chan error, len(stores))
	var ready sync.WaitGroup
	ready.Add(len(stores))
	for i, store := range stores {
		go func(i int, store *SQLiteStore) {
			ready.Done()
			<-start
			result, acquireErr := store.AcquireValidation(ctx, request(fmt.Sprintf("shared-%d", i), domain.ValidationClassShared), now.Add(time.Second))
			results <- result
			errs <- acquireErr
		}(i, store)
	}
	ready.Wait()
	close(start)
	for range stores {
		require.NoError(t, <-errs)
	}
	states := map[domain.ValidationRequestState]int{}
	for range stores {
		states[(<-results).State]++
	}
	assert.Equal(t, 1, states[domain.ValidationRequestActive])
	assert.Equal(t, len(stores)-1, states[domain.ValidationRequestQueued])
}

func TestValidationLeaseRejectsUnboundedSafeCommand(t *testing.T) {
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	_, err := store.AcquireValidation(context.Background(), domain.ValidationAcquire{RequestID: "unsafe", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassSafe, Profile: "cold", Command: "go test ./...", SourceRevision: "abc123", TTL: time.Minute}, time.Now().UTC())
	require.ErrorContains(t, err, "bounded non-compiling")
}

func TestValidationSnapshotRevisionAdvancesOnHeartbeatAndCompletion(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "shared", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassShared, Profile: "focused", Command: "go test ./internal/domain", SourceRevision: "abc123", TTL: time.Minute}, now)
	require.NoError(t, err)
	first, err := store.ValidationSnapshot(ctx, "project", now, time.Minute)
	require.NoError(t, err)
	_, err = store.HeartbeatValidation(ctx, "shared", testValidationToken, now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	second, err := store.ValidationSnapshot(ctx, "project", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	_, err = store.FinishValidation(ctx, "shared", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	third, err := store.ValidationSnapshot(ctx, "project", now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	assert.Greater(t, second.Revision, first.Revision)
	assert.Greater(t, third.Revision, second.Revision)
}

func TestValidationLeaseRestartExpiresStaleOwnerAndWakesQueue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	first := NewAtPath(path, nil)
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	ttl := 10 * time.Second
	request := func(id string) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: ttl}
	}
	active, err := first.AcquireValidation(ctx, request("owner"), now)
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestActive, active.State)
	queued, err := first.AcquireValidation(ctx, request("waiter"), now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestQueued, queued.State)
	require.NoError(t, first.Close())

	restarted := NewAtPath(path, nil)
	t.Cleanup(func() { _ = restarted.Close() })
	snapshot, err := restarted.ValidationSnapshot(ctx, "project", now.Add(ttl+500*time.Millisecond), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"waiter"}, validationRequestIDs(snapshot.Active))
	require.Len(t, snapshot.Recent, 1)
	assert.Equal(t, domain.ValidationRequestExpired, snapshot.Recent[0].State)
}

func TestValidationLeaseExpiresAbandonedQueuedRequest(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	ttl := 10 * time.Second
	request := func(id string) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: ttl}
	}
	_, err := store.AcquireValidation(ctx, request("owner"), now)
	require.NoError(t, err)
	_, err = store.AcquireValidation(ctx, request("abandoned"), now.Add(time.Second))
	require.NoError(t, err)
	_, err = store.HeartbeatValidation(ctx, "owner", testValidationToken, now.Add(9*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err := store.ValidationSnapshot(ctx, "project", now.Add(12*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner"}, validationRequestIDs(snapshot.Active))
	require.Len(t, snapshot.Recent, 1)
	assert.Equal(t, "abandoned", snapshot.Recent[0].RequestID)
	assert.Equal(t, domain.ValidationRequestExpired, snapshot.Recent[0].State)
}

func TestLatestAggregateValidationRetainsMachineEvidence(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "aggregate", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence, Profile: "cold", Command: "just test", SourceRevision: "abc123", ReviewerID: "reviewer", ReviewEpochEventID: 1, TTL: time.Minute}, now)
	require.NoError(t, err)
	want := domain.ValidationEvidence{Held: true, RequestID: "aggregate", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence, Profile: "cold", SourceRevision: "abc123", Present: true, ReportPath: ".tmp/report.json", OverlapDetected: true, ExternalGoProcesses: 3}
	_, err = store.FinishValidation(ctx, "aggregate", testValidationToken, domain.ValidationRequestCompleted, "passed", want, now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	got, err := store.LatestAggregateValidation(ctx, "project", "dkg", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want, got.Evidence)
}

func TestValidationLeaseRejectsSpoofedMachineEvidenceIdentity(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "aggregate", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: time.Minute}, now)
	require.NoError(t, err)
	_, err = store.FinishValidation(ctx, "aggregate", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{Held: true, RequestID: "different", Class: domain.ValidationClassAggregate, Profile: "cold", SourceRevision: "abc123", Present: true}, now.Add(time.Second), time.Minute)
	require.ErrorContains(t, err, "evidence identity does not match")
}

func TestValidationLeaseRejectsHeartbeatAndFinishWithWrongFencingToken(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "aggregate", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: time.Minute}, now)
	require.NoError(t, err)
	_, err = store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "aggregate", LeaseToken: "wrong", ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: time.Minute}, now)
	require.ErrorContains(t, err, "lease token rejected")
	_, err = store.HeartbeatValidation(ctx, "aggregate", "wrong", now.Add(time.Second), time.Minute)
	require.ErrorContains(t, err, "lease token rejected")
	_, err = store.FinishValidation(ctx, "aggregate", "wrong", domain.ValidationRequestCancelled, "stolen", domain.ValidationEvidence{}, now.Add(time.Second), time.Minute)
	require.ErrorContains(t, err, "lease token rejected")
}

func TestValidationLeaseAuthorizesNestedOnlyWithFencingTokenAndCompatibleClass(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "shared", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassShared, Profile: "focused", Command: "go test ./internal/domain", SourceRevision: "abc123", TTL: time.Minute}, now)
	require.NoError(t, err)

	_, err = store.AuthorizeNestedValidation(ctx, domain.ValidationNestedAuthorization{RequestID: "shared", LeaseToken: "wrong", Class: domain.ValidationClassShared}, now, time.Minute)
	require.ErrorContains(t, err, "lease token rejected")
	_, err = store.AuthorizeNestedValidation(ctx, domain.ValidationNestedAuthorization{RequestID: "shared", LeaseToken: testValidationToken, Class: domain.ValidationClassAggregate}, now, time.Minute)
	require.ErrorContains(t, err, "cannot join active shared")
	request, err := store.AuthorizeNestedValidation(ctx, domain.ValidationNestedAuthorization{RequestID: "shared", LeaseToken: testValidationToken, Class: domain.ValidationClassShared}, now, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestActive, request.State)
}

func TestValidationLeaseRejectsEvidenceFromDifferentSourceRevision(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "aggregate", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "candidate-a", TTL: time.Minute}, now)
	require.NoError(t, err)
	_, err = store.FinishValidation(ctx, "aggregate", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{Held: true, RequestID: "aggregate", Class: domain.ValidationClassAggregate, Profile: "cold", SourceRevision: "candidate-b", Present: true}, now.Add(time.Second), time.Minute)
	require.ErrorContains(t, err, "evidence identity does not match")
}

func validationRequestIDs(requests []domain.ValidationRequest) []string {
	ids := make([]string, 0, len(requests))
	for _, request := range requests {
		ids = append(ids, request.RequestID)
	}
	return ids
}
