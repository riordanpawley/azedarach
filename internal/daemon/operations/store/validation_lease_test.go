package store

import (
	"context"
	"database/sql"
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
	assert.Equal(t, "azedarach.validation_lease_status.v3", snapshot.Schema)
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

func TestValidationLeasePriorityOrdersP0FocusedAheadOfNewAggregate(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	ttl := time.Minute
	request := func(id string, class domain.ValidationClass, priority domain.Priority) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: class, IssuePriority: priority, IssuePriorityResolved: true, Profile: id, Command: "go test", SourceRevision: "abc123", TTL: ttl}
	}

	owner, err := store.AcquireValidation(ctx, request("owner", domain.ValidationClassAggregate, domain.P2), now)
	require.NoError(t, err)
	require.Equal(t, domain.ValidationRequestActive, owner.State)
	queuedAggregate, err := store.AcquireValidation(ctx, request("new-cold", domain.ValidationClassAggregate, domain.P2), now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, domain.ValidationRequestQueued, queuedAggregate.State)
	urgent, err := store.AcquireValidation(ctx, request("p0-focused-retry", domain.ValidationClassShared, domain.P0), now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, domain.ValidationRequestQueued, urgent.State)

	snapshot, err := store.ValidationSnapshot(ctx, "project", now.Add(2*time.Second), ttl)
	require.NoError(t, err)
	require.Equal(t, []string{"p0-focused-retry", "new-cold"}, validationRequestIDs(snapshot.Queued))
	assert.Equal(t, 1, snapshot.Queued[0].QueuePosition)
	assert.Equal(t, domain.ValidationOrderingPriorityFIFO, snapshot.Queued[0].OrderingReason)
	assert.Equal(t, domain.P0, snapshot.Queued[0].IssuePriority)

	_, err = store.FinishValidation(ctx, "owner", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(3*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err = store.ValidationSnapshot(ctx, "project", now.Add(3*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"p0-focused-retry"}, validationRequestIDs(snapshot.Active))
	assert.Equal(t, []string{"new-cold"}, validationRequestIDs(snapshot.Queued))
}

func TestValidationLeaseDefaultsUnresolvedInternalPriorityToNeutral(t *testing.T) {
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	request, err := store.AcquireValidation(context.Background(), domain.ValidationAcquire{RequestID: "legacy-caller", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "consumer-ticket", Class: domain.ValidationClassShared, Profile: "consumer-focused", Command: "make verify", SourceRevision: "abc123", TTL: time.Minute}, time.Date(2026, 7, 18, 0, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, domain.P2, request.IssuePriority)
}

func TestValidationLeasePriorityFIFOAndBoundedFairnessSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	ttl := time.Minute
	request := func(id string, class domain.ValidationClass, priority domain.Priority) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: class, IssuePriority: priority, IssuePriorityResolved: true, Profile: id, Command: "go test", SourceRevision: "abc123", TTL: ttl}
	}

	first := NewAtPath(path, nil)
	_, err := first.AcquireValidation(ctx, request("owner", domain.ValidationClassAggregate, domain.P2), now)
	require.NoError(t, err)
	_, err = first.AcquireValidation(ctx, request("older-p4", domain.ValidationClassShared, domain.P4), now.Add(time.Second))
	require.NoError(t, err)
	_, err = first.AcquireValidation(ctx, request("first-p0", domain.ValidationClassAggregate, domain.P0), now.Add(2*time.Second))
	require.NoError(t, err)
	_, err = first.AcquireValidation(ctx, request("second-p0", domain.ValidationClassAggregate, domain.P0), now.Add(3*time.Second))
	require.NoError(t, err)

	_, err = first.FinishValidation(ctx, "owner", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(4*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err := first.ValidationSnapshot(ctx, "project", now.Add(4*time.Second), ttl)
	require.NoError(t, err)
	require.Equal(t, []string{"first-p0"}, validationRequestIDs(snapshot.Active), "equal-priority requests must remain FIFO")
	require.Equal(t, []string{"older-p4", "second-p0"}, validationRequestIDs(snapshot.Queued), "a request bypassed once becomes the bounded-fairness head")
	assert.Equal(t, 1, snapshot.Queued[0].PriorityBypassCount)
	assert.Equal(t, domain.ValidationOrderingBoundedFairness, snapshot.Queued[0].OrderingReason)
	require.NoError(t, first.Close())

	restarted := NewAtPath(path, nil)
	t.Cleanup(func() { _ = restarted.Close() })
	_, err = restarted.FinishValidation(ctx, "first-p0", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(5*time.Second), ttl)
	require.NoError(t, err)
	snapshot, err = restarted.ValidationSnapshot(ctx, "project", now.Add(5*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{"older-p4"}, validationRequestIDs(snapshot.Active))
	assert.Equal(t, []string{"second-p0"}, validationRequestIDs(snapshot.Queued))
}

func TestPublicationValidationStartsImmediatelyAndRetiresLegacyDevelopmentAdmission(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	ttl := time.Minute
	capacity := func(id string) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dnb", Class: domain.ValidationClassShared, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeCapacity, IsolationMode: "worktree", EnvironmentFingerprint: "toolchain", Override: domain.ValidationOverrideNone, Profile: id, Command: "go test", SourceRevision: "abc123", TTL: ttl}
	}

	activeCapacity, err := store.AcquireValidation(ctx, capacity("capacity"), now)
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestActive, activeCapacity.State)
	legacyDevelopment, err := store.AcquireValidation(ctx, capacity("legacy-development"), now)
	require.NoError(t, err)
	require.Equal(t, domain.ValidationRequestActive, legacyDevelopment.State)
	db, err := store.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE daemon_validation_requests SET purpose='development' WHERE request_id='legacy-development'`)
	require.NoError(t, err)

	publication, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "push", LeaseToken: testValidationToken, ProjectID: "project", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate, IsolationMode: "repository-family", EnvironmentFingerprint: "toolchain", Override: domain.ValidationOverrideNone, Profile: "merge-gate", Command: "just merge-gate", SourceRevision: "abc123", TTL: ttl}, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestActive, publication.State, "publication must not queue behind capacity validation")
	duplicatePublication := domain.ValidationAcquire{RequestID: "push-duplicate", LeaseToken: testValidationToken, ProjectID: "project", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate, IsolationMode: "repository-family", EnvironmentFingerprint: "toolchain", Override: domain.ValidationOverrideNone, Profile: "merge-gate", Command: "just merge-gate", SourceRevision: "abc123", TTL: ttl}
	duplicate, err := store.AcquireValidation(ctx, duplicatePublication, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestActive, duplicate.State, "publication must not join and wait behind another publication run")
	assert.Equal(t, domain.ValidationExecutionExecuted, duplicate.Execution)

	snapshot, err := store.ValidationSnapshot(ctx, "project", now.Add(2*time.Second), ttl)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"capacity", "push", "push-duplicate"}, validationRequestIDs(snapshot.Active))
	var retired *domain.ValidationRequest
	for i := range snapshot.Recent {
		if snapshot.Recent[i].RequestID == "legacy-development" {
			retired = &snapshot.Recent[i]
			break
		}
	}
	require.NotNil(t, retired)
	assert.Equal(t, domain.ValidationRequestCancelled, retired.State)
	assert.Contains(t, retired.Outcome, "no longer uses daemon admission")
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

func TestValidationLeaseConcurrentStoresPreservePriorityFIFOAndBypassBound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	stores := []*SQLiteStore{NewAtPath(path, nil), NewAtPath(path, nil), NewAtPath(path, nil)}
	t.Cleanup(func() {
		for _, store := range stores {
			_ = store.Close()
		}
	})
	now := time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)
	ttl := time.Minute
	request := func(id string, priority domain.Priority) domain.ValidationAcquire {
		return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: id, Class: domain.ValidationClassAggregate, IssuePriority: priority, IssuePriorityResolved: true, Profile: id, Command: "go test", SourceRevision: "abc123", TTL: ttl}
	}
	_, err := stores[0].AcquireValidation(ctx, request("owner", domain.P2), now)
	require.NoError(t, err)
	_, err = stores[0].AcquireValidation(ctx, request("older-p4", domain.P4), now.Add(time.Second))
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan domain.ValidationRequest, 2)
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			ready.Done()
			<-start
			result, acquireErr := stores[i+1].AcquireValidation(ctx, request(fmt.Sprintf("p0-%d", i), domain.P0), now.Add(2*time.Second))
			results <- result
			errs <- acquireErr
		}(i)
	}
	ready.Wait()
	close(start)
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs)
		require.Equal(t, domain.ValidationRequestQueued, (<-results).State)
	}

	before, err := stores[0].ValidationSnapshot(ctx, "project", now.Add(2*time.Second), ttl)
	require.NoError(t, err)
	require.Len(t, before.Queued, 3)
	firstP0 := before.Queued[0]
	require.Equal(t, domain.P0, firstP0.IssuePriority)
	require.Less(t, firstP0.Sequence, before.Queued[1].Sequence, "equal-priority concurrent requests must follow durable insertion sequence")
	_, err = stores[0].FinishValidation(ctx, "owner", testValidationToken, domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{}, now.Add(3*time.Second), ttl)
	require.NoError(t, err)

	after, err := stores[0].ValidationSnapshot(ctx, "project", now.Add(3*time.Second), ttl)
	require.NoError(t, err)
	assert.Equal(t, []string{firstP0.RequestID}, validationRequestIDs(after.Active))
	require.Equal(t, "older-p4", after.Queued[0].RequestID)
	assert.Equal(t, domain.ValidationOrderingBoundedFairness, after.Queued[0].OrderingReason)
}

func TestValidationSnapshotRemainsReadableDuringValidationWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	store := NewAtPath(path, nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "lease", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "consumer-ticket",
		Class: domain.ValidationClassAggregate, Profile: "consumer-gate", Command: "make verify", SourceRevision: "abc123", TTL: time.Minute,
	}, now)
	require.NoError(t, err)

	writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(100)&_txlock=immediate")
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, `UPDATE daemon_validation_state SET revision=revision WHERE project_id='project'`)
	require.NoError(t, err)

	snapshot, err := store.ValidationSnapshot(ctx, "project", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []string{"lease"}, validationRequestIDs(snapshot.Active))
	require.Equal(t, domain.ValidationSnapshotFresh, snapshot.Freshness)
	require.Empty(t, snapshot.DegradedReason)
}

func TestValidationSnapshotMarksExpiredCapacityStaleWhenWriterPreventsReconciliation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	store := NewAtPath(path, nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "expired", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "consumer-ticket",
		Class: domain.ValidationClassAggregate, Profile: "consumer-gate", Command: "make verify", SourceRevision: "abc123", TTL: time.Minute,
	}, now)
	require.NoError(t, err)

	writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(100)&_txlock=immediate")
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, `UPDATE daemon_validation_state SET revision=revision WHERE project_id='project'`)
	require.NoError(t, err)

	snapshot, err := store.ValidationSnapshot(ctx, "project", now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []string{"expired"}, validationRequestIDs(snapshot.Active))
	require.Equal(t, domain.ValidationSnapshotStale, snapshot.Freshness)
	require.Contains(t, snapshot.DegradedReason, "expired leases pending reconciliation")
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
	_, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "aggregate", LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence, Profile: "cold", Command: "just test", SourceRevision: "abc123", ReviewerID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator, ReviewEpochEventID: 1, PublicationOperationID: "publication", AcceptedReviewEventID: 2, AcceptedPublicationOperationID: "publication", TTL: time.Minute}, now)
	require.NoError(t, err)
	want := domain.ValidationEvidence{Held: true, RequestID: "aggregate", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence, Execution: domain.ValidationExecutionExecuted, AuthoritativeRequestID: "aggregate", Profile: "cold", SourceRevision: "abc123", Present: true, ReportPath: ".tmp/report.json", FailureSummary: "FAIL make-verify::consumer-check\nexact consumer failure", OverlapDetected: true, ExternalGoProcesses: 3}
	_, err = store.FinishValidation(ctx, "aggregate", testValidationToken, domain.ValidationRequestCompleted, "passed", want, now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	got, err := store.LatestAggregateValidation(ctx, "project", "dkg", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want, got.Evidence)
}

func TestLatestReviewValidationSkipsLegacyAuthorityRows(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	review := reusableValidationAcquire("review-complete")
	review.Purpose = domain.ValidationPurposeReviewEvidence
	review.ReviewerID = "reviewer"
	review.ReviewerKind = domain.ReviewerOwnerKindOrchestrator
	review.ReviewEpochEventID = 42
	review.PublicationOperationID = "publication"
	review.AcceptedReviewEventID = 43
	review.AcceptedPublicationOperationID = "publication"
	complete, err := store.AcquireValidation(ctx, review, now)
	require.NoError(t, err)

	db, err := store.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE daemon_validation_requests SET reviewer_kind='', publication_operation_id='', accepted_review_event_id=0, accepted_publication_operation_id='' WHERE request_id=?`, complete.RequestID)
	require.NoError(t, err)

	latest, err := store.LatestReviewValidation(ctx, "project", review.IssueID, now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	assert.Nil(t, latest, "legacy review evidence must not authorize readiness")
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

func TestValidationLeaseReusesCompatibleCompletedEvidence(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	request := reusableValidationAcquire("source")
	source, err := store.AcquireValidation(ctx, request, now)
	require.NoError(t, err)
	require.Equal(t, domain.ValidationRequestActive, source.State)
	evidence := validationEvidenceFor(source)
	_, err = store.FinishValidation(ctx, source.RequestID, request.LeaseToken, domain.ValidationRequestCompleted, "exit 0", evidence, now.Add(time.Second), time.Minute)
	require.NoError(t, err)

	reusedRequest := reusableValidationAcquire("follower")
	reused, err := store.AcquireValidation(ctx, reusedRequest, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionReused, reused.Execution)
	assert.Equal(t, domain.ValidationRequestCompleted, reused.State)
	assert.Equal(t, source.RequestID, reused.AuthoritativeRequestID)
	assert.Equal(t, reused.RequestID, reused.Evidence.RequestID)
	assert.Equal(t, domain.ValidationExecutionReused, reused.Evidence.Execution)
	assert.Equal(t, source.RequestID, reused.Evidence.AuthoritativeRequestID)
	assert.Equal(t, evidence.SourceRevision, reused.Evidence.SourceRevision)
}

func TestValidationLeaseCoalescesConcurrentCompatibleRequests(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	sourceAcquire := reusableValidationAcquire("source")
	source, err := store.AcquireValidation(ctx, sourceAcquire, now)
	require.NoError(t, err)
	followerAcquire := reusableValidationAcquire("follower")
	follower, err := store.AcquireValidation(ctx, followerAcquire, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionJoined, follower.Execution)
	assert.Equal(t, domain.ValidationRequestQueued, follower.State)
	assert.Equal(t, source.RequestID, follower.AuthoritativeRequestID)

	evidence := validationEvidenceFor(source)
	_, err = store.FinishValidation(ctx, source.RequestID, sourceAcquire.LeaseToken, domain.ValidationRequestCompleted, "exit 0", evidence, now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	follower, err = store.AcquireValidation(ctx, followerAcquire, now.Add(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestCompleted, follower.State)
	assert.Equal(t, follower.RequestID, follower.Evidence.RequestID)
	assert.Equal(t, domain.ValidationExecutionJoined, follower.Evidence.Execution)
	assert.Equal(t, source.RequestID, follower.Evidence.AuthoritativeRequestID)
	assert.Equal(t, evidence.SourceRevision, follower.Evidence.SourceRevision)
}

func TestValidationSnapshotExcludesJoinedFollowersFromRunnablePositions(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	owner := reusableValidationAcquire("owner")
	owner.Command = "just test-owner"
	_, err := store.AcquireValidation(ctx, owner, now)
	require.NoError(t, err)

	sourceAcquire := reusableValidationAcquire("source")
	source, err := store.AcquireValidation(ctx, sourceAcquire, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, domain.ValidationRequestQueued, source.State)
	follower, err := store.AcquireValidation(ctx, reusableValidationAcquire("follower"), now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, domain.ValidationExecutionJoined, follower.Execution)
	runnableAcquire := reusableValidationAcquire("runnable")
	runnableAcquire.Command = "just test-runnable"
	runnable, err := store.AcquireValidation(ctx, runnableAcquire, now.Add(3*time.Second))
	require.NoError(t, err)
	require.Equal(t, domain.ValidationRequestQueued, runnable.State)

	snapshot, err := store.ValidationSnapshot(ctx, "project", now.Add(3*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []string{"source", "runnable", "follower"}, validationRequestIDs(snapshot.Queued))
	assert.Equal(t, 1, snapshot.Queued[0].QueuePosition)
	assert.Equal(t, 2, snapshot.Queued[1].QueuePosition)
	assert.Zero(t, snapshot.Queued[2].QueuePosition)
	assert.Equal(t, domain.ValidationOrderingJoinedSource, snapshot.Queued[2].OrderingReason)
	assert.Equal(t, source.RequestID, snapshot.Queued[2].AuthoritativeRequestID)
}

func TestValidationLeaseOverridesCannotManufactureEvidence(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	sourceAcquire := reusableValidationAcquire("source")
	source, err := store.AcquireValidation(ctx, sourceAcquire, now)
	require.NoError(t, err)
	_, err = store.FinishValidation(ctx, source.RequestID, sourceAcquire.LeaseToken, domain.ValidationRequestCompleted, "exit 0", validationEvidenceFor(source), now.Add(time.Second), time.Minute)
	require.NoError(t, err)

	noReuse := reusableValidationAcquire("no-reuse")
	noReuse.Override = domain.ValidationOverrideNoReuse
	got, err := store.AcquireValidation(ctx, noReuse, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionExecuted, got.Execution)
	assert.Equal(t, domain.ValidationRequestActive, got.State)

	force := reusableValidationAcquire("force")
	force.Override = domain.ValidationOverrideForceRerun
	got, err = store.AcquireValidation(ctx, force, now.Add(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionExecuted, got.Execution)
	assert.Equal(t, domain.ValidationRequestQueued, got.State)

	skip := reusableValidationAcquire("skip")
	skip.Override = domain.ValidationOverrideEmergency
	skip.OverrideActor = "operator"
	skip.OverrideReason = "restore production push path"
	got, err = store.AcquireValidation(ctx, skip, now.Add(4*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionSkipped, got.Execution)
	assert.Equal(t, domain.ValidationRequestCancelled, got.State)
	assert.False(t, got.Evidence.Present)
	assert.Contains(t, got.Outcome, skip.OverrideReason)
}

func TestValidationLeaseCompatibilityPolicyAllowsReviewEvidenceToSatisfyRepositoryPushOnly(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	review := reusableValidationAcquire("review")
	review.Purpose = domain.ValidationPurposeReviewEvidence
	review.ReviewerID = "reviewer"
	review.ReviewerKind = domain.ReviewerOwnerKindOrchestrator
	review.ReviewEpochEventID = 42
	review.PublicationOperationID = "publication"
	review.AcceptedReviewEventID = 43
	review.AcceptedPublicationOperationID = "publication"
	source, err := store.AcquireValidation(ctx, review, now)
	require.NoError(t, err)
	reviewEvidence := validationEvidenceFor(source)
	reviewEvidence.OverlapDetected = true
	reviewEvidence.ExternalGoProcesses = 2
	_, err = store.FinishValidation(ctx, source.RequestID, review.LeaseToken, domain.ValidationRequestCompleted, "exit 0", reviewEvidence, now.Add(time.Second), time.Minute)
	require.NoError(t, err)

	push := reusableValidationAcquire("push")
	push.IssueID = ""
	push.Scope = domain.ValidationScopeRepository
	push.Purpose = domain.ValidationPurposePushGate
	got, err := store.AcquireValidation(ctx, push, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionReused, got.Execution)
	assert.Equal(t, source.RequestID, got.AuthoritativeRequestID)

	incompatible := reusableValidationAcquire("different-toolchain")
	incompatible.EnvironmentFingerprint = "go1.26-darwin-arm64"
	got, err = store.AcquireValidation(ctx, incompatible, now.Add(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionExecuted, got.Execution)

	reverseStore := NewAtPath(filepath.Join(t.TempDir(), "reverse.db"), nil)
	t.Cleanup(func() { _ = reverseStore.Close() })
	pushSource := push
	pushSource.RequestID = "push-source"
	pushSource.Override = domain.ValidationOverrideNone
	pushRow, err := reverseStore.AcquireValidation(ctx, pushSource, now)
	require.NoError(t, err)
	_, err = reverseStore.FinishValidation(ctx, pushRow.RequestID, pushSource.LeaseToken, domain.ValidationRequestCompleted, "exit 0", validationEvidenceFor(pushRow), now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	reviewTarget := review
	reviewTarget.RequestID = "review-target"
	got, err = reverseStore.AcquireValidation(ctx, reviewTarget, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionExecuted, got.Execution, "repository push evidence must never authorize review")
}

func TestValidationLeaseCoalescingSurvivesStoreRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "project.db")
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	first := NewAtPath(dbPath, nil)
	sourceAcquire := reusableValidationAcquire("source-restart")
	source, err := first.AcquireValidation(ctx, sourceAcquire, now)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	restarted := NewAtPath(dbPath, nil)
	followerAcquire := reusableValidationAcquire("follower-restart")
	follower, err := restarted.AcquireValidation(ctx, followerAcquire, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationExecutionJoined, follower.Execution)
	_, err = restarted.FinishValidation(ctx, source.RequestID, sourceAcquire.LeaseToken, domain.ValidationRequestCompleted, "exit 0", validationEvidenceFor(source), now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	require.NoError(t, restarted.Close())

	reopened := NewAtPath(dbPath, nil)
	defer reopened.Close()
	follower, err = reopened.AcquireValidation(ctx, followerAcquire, now.Add(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, domain.ValidationRequestCompleted, follower.State)
	assert.Equal(t, source.RequestID, follower.AuthoritativeRequestID)
}

func reusableValidationAcquire(id string) domain.ValidationAcquire {
	return domain.ValidationAcquire{RequestID: id, LeaseToken: testValidationToken, ProjectID: "project", IssueID: "dmm", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeCapacity, IsolationMode: "worktree", EnvironmentFingerprint: "go1.25-darwin-arm64", Override: domain.ValidationOverrideNone, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: time.Minute}
}

func validationEvidenceFor(request domain.ValidationRequest) domain.ValidationEvidence {
	return domain.ValidationEvidence{Held: true, RequestID: request.RequestID, Class: request.Class, Scope: request.Scope, Purpose: request.Purpose, Execution: domain.ValidationExecutionExecuted, AuthoritativeRequestID: request.RequestID, Profile: request.Profile, SourceRevision: request.SourceRevision, Present: true}
}

func validationRequestIDs(requests []domain.ValidationRequest) []string {
	ids := make([]string, 0, len(requests))
	for _, request := range requests {
		ids = append(ids, request.RequestID)
	}
	return ids
}
