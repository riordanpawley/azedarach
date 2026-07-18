package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

var (
	ErrOrchestratorLeaseNotFound = errors.New("orchestrator scope lease not found")
	ErrOrchestratorLeaseConflict = errors.New("orchestrator scope lease conflict")
)

// OrchestratorLeaseConflictError returns the existing durable identity to a
// caller that cannot attach to it with the requested session ID.
type OrchestratorLeaseConflictError struct {
	Lease OrchestratorScopeLease
}

func (e *OrchestratorLeaseConflictError) Error() string {
	return fmt.Sprintf("%s: scope already owned by session %s", ErrOrchestratorLeaseConflict, e.Lease.SessionID)
}

func (e *OrchestratorLeaseConflictError) Unwrap() error { return ErrOrchestratorLeaseConflict }

// OrchestratorScopeLease is the durable identity and lifecycle projection for
// one exact orchestration scope. Paused leases remain durable so wake and
// daemon recovery do not need startup environment variables.
type OrchestratorScopeLease struct {
	Identity       domain.OrchestratorIdentity
	SessionID      string
	Lifecycle      domain.OrchestratorLifecycle
	AcquiredAt     time.Time
	UpdatedAt      time.Time
	CompleteSince  *time.Time
	LastWakeAt     *time.Time
	LastWakeReason domain.OrchestratorWakeReason
	Cursor         int64
}

type OrchestratorLeaseAcquireDisposition string

const (
	OrchestratorLeaseAcquired       OrchestratorLeaseAcquireDisposition = "acquired"
	OrchestratorLeaseAttached       OrchestratorLeaseAcquireDisposition = "attached"
	OrchestratorLeaseRecoveredStale OrchestratorLeaseAcquireDisposition = "recovered-stale"
)

type OrchestratorLeaseAcquireResult struct {
	Lease       OrchestratorScopeLease
	Disposition OrchestratorLeaseAcquireDisposition
}

// SessionRuntimeProbe checks live tmux runtime presence for a session ID.
type SessionRuntimeProbe func(context.Context, string) (bool, error)

type OrchestratorScopeLeaseStore interface {
	WithOrchestratorScopeTransition(context.Context, domain.OrchestratorIdentity, func(context.Context) error) error
	GetOrchestratorScopeLease(context.Context, domain.OrchestratorIdentity) (OrchestratorScopeLease, bool, error)
	ListOrchestratorScopeLeases(context.Context, string) ([]OrchestratorScopeLease, error)
	AcquireOrchestratorScopeLease(context.Context, domain.OrchestratorIdentity, string, SessionRuntimeProbe) (OrchestratorLeaseAcquireResult, error)
	AcquireRootedOrchestratorScopeLease(context.Context, domain.OrchestratorIdentity, Session, SessionRuntimeProbe) (OrchestratorLeaseAcquireResult, error)
	SetOrchestratorScopeLeaseLifecycle(context.Context, domain.OrchestratorIdentity, string, domain.OrchestratorLifecycle) (OrchestratorScopeLease, error)
	EvaluateOrchestratorScopeLease(context.Context, domain.OrchestratorIdentity, string, time.Time, domain.OrchestratorLifecycleFacts, domain.OrchestratorLifecyclePolicy) (OrchestratorScopeLease, error)
	WakeOrchestratorScopeLease(context.Context, domain.OrchestratorIdentity, time.Time, domain.OrchestratorWakeReason, domain.OrchestratorLifecyclePolicy) (OrchestratorScopeLease, bool, error)
	AdvanceOrchestratorScopeCursor(context.Context, domain.OrchestratorIdentity, int64) (OrchestratorScopeLease, error)
	ReleaseOrchestratorScopeLease(context.Context, domain.OrchestratorIdentity, string) error
}

type rootedOrchestratorTransitionHookKey struct{}

// WithRootedOrchestratorTransitionHookForTest injects a deterministic failure
// after the lease mutation and before the desired-role replacement. The hook
// runs inside the shared SQLite transaction and exists only for crash-boundary
// regression tests.
func WithRootedOrchestratorTransitionHookForTest(ctx context.Context, hook func(string) error) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, rootedOrchestratorTransitionHookKey{}, hook)
}

// OrchestratorLeaseAuthority is the refresh-then-cache authority used by
// projection and hybrid invariant consumers. The durable store remains the
// serialization point; this cache is never evaluated without first refreshing
// the relevant project from SQLite.
type OrchestratorLeaseAuthority struct {
	store OrchestratorScopeLeaseStore
	mu    sync.RWMutex
	cache map[string]map[string]OrchestratorScopeLease
}

func NewOrchestratorLeaseAuthority(store OrchestratorScopeLeaseStore) *OrchestratorLeaseAuthority {
	return &OrchestratorLeaseAuthority{store: store, cache: make(map[string]map[string]OrchestratorScopeLease)}
}

func (a *OrchestratorLeaseAuthority) Refresh(ctx context.Context, projectID string) error {
	identity, err := domain.NewOrchestratorIdentity(projectID, domain.ProjectOrchestrationScope())
	if err != nil {
		return err
	}
	leases, err := a.store.ListOrchestratorScopeLeases(ctx, identity.ProjectID)
	if err != nil {
		return err
	}
	refreshed := make(map[string]OrchestratorScopeLease, len(leases))
	for _, lease := range leases {
		refreshed[orchestratorScopeCacheKey(lease.Identity)] = lease
	}
	a.mu.Lock()
	a.cache[identity.ProjectID] = refreshed
	a.mu.Unlock()
	return nil
}

func (a *OrchestratorLeaseAuthority) Get(ctx context.Context, identity domain.OrchestratorIdentity) (OrchestratorScopeLease, bool, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorScopeLease{}, false, err
	}
	if err := a.Refresh(ctx, identity.ProjectID); err != nil {
		return OrchestratorScopeLease{}, false, err
	}
	a.mu.RLock()
	lease, found := a.cache[identity.ProjectID][orchestratorScopeCacheKey(identity)]
	a.mu.RUnlock()
	return lease, found, nil
}

func (a *OrchestratorLeaseAuthority) FindBySession(ctx context.Context, projectID, sessionID string) (OrchestratorScopeLease, bool, error) {
	projectID, sessionID = normalizedProjectID(projectID), strings.TrimSpace(sessionID)
	if err := a.Refresh(ctx, projectID); err != nil {
		return OrchestratorScopeLease{}, false, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, lease := range a.cache[projectID] {
		if lease.SessionID == sessionID {
			return lease, true, nil
		}
	}
	return OrchestratorScopeLease{}, false, nil
}

func (a *OrchestratorLeaseAuthority) Acquire(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string, probe SessionRuntimeProbe) (OrchestratorLeaseAcquireResult, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorLeaseAcquireResult{}, err
	}
	var result OrchestratorLeaseAcquireResult
	err = a.store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil {
			return refreshErr
		}
		var acquireErr error
		result, acquireErr = a.store.AcquireOrchestratorScopeLease(lockCtx, identity, sessionID, probe)
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil && acquireErr == nil {
			return refreshErr
		}
		return acquireErr
	})
	return result, err
}

// AcquireRooted atomically acquires the exact rooted scope and replaces the
// matching physical session's legacy worker intent with rooted-orchestrator
// intent. Neither durable half is externally visible without the other.
func (a *OrchestratorLeaseAuthority) AcquireRooted(ctx context.Context, identity domain.OrchestratorIdentity, session Session, probe SessionRuntimeProbe) (OrchestratorLeaseAcquireResult, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorLeaseAcquireResult{}, err
	}
	var result OrchestratorLeaseAcquireResult
	err = a.store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil {
			return refreshErr
		}
		var acquireErr error
		result, acquireErr = a.store.AcquireRootedOrchestratorScopeLease(lockCtx, identity, session, probe)
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil && acquireErr == nil {
			return refreshErr
		}
		return acquireErr
	})
	return result, err
}

func (a *OrchestratorLeaseAuthority) SetLifecycle(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string, lifecycle domain.OrchestratorLifecycle) (OrchestratorScopeLease, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorScopeLease{}, err
	}
	var lease OrchestratorScopeLease
	err = a.store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
		var setErr error
		lease, setErr = a.store.SetOrchestratorScopeLeaseLifecycle(lockCtx, identity, sessionID, lifecycle)
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil && setErr == nil {
			return refreshErr
		}
		return setErr
	})
	return lease, err
}

func (a *OrchestratorLeaseAuthority) Evaluate(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string, now time.Time, facts domain.OrchestratorLifecycleFacts, policy domain.OrchestratorLifecyclePolicy) (OrchestratorScopeLease, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorScopeLease{}, err
	}
	var lease OrchestratorScopeLease
	err = a.store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil {
			return refreshErr
		}
		var evaluateErr error
		lease, evaluateErr = a.store.EvaluateOrchestratorScopeLease(lockCtx, identity, sessionID, now, facts, policy)
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil && evaluateErr == nil {
			return refreshErr
		}
		return evaluateErr
	})
	return lease, err
}

func (a *OrchestratorLeaseAuthority) Wake(ctx context.Context, identity domain.OrchestratorIdentity, now time.Time, reason domain.OrchestratorWakeReason, policy domain.OrchestratorLifecyclePolicy) (OrchestratorScopeLease, bool, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorScopeLease{}, false, err
	}
	var lease OrchestratorScopeLease
	var changed bool
	err = a.store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil {
			return refreshErr
		}
		var wakeErr error
		lease, changed, wakeErr = a.store.WakeOrchestratorScopeLease(lockCtx, identity, now, reason, policy)
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil && wakeErr == nil {
			return refreshErr
		}
		return wakeErr
	})
	return lease, changed, err
}

func (a *OrchestratorLeaseAuthority) AdvanceCursor(ctx context.Context, identity domain.OrchestratorIdentity, cursor int64) (OrchestratorScopeLease, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorScopeLease{}, err
	}
	var lease OrchestratorScopeLease
	err = a.store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil {
			return refreshErr
		}
		var advanceErr error
		lease, advanceErr = a.store.AdvanceOrchestratorScopeCursor(lockCtx, identity, cursor)
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil && advanceErr == nil {
			return refreshErr
		}
		return advanceErr
	})
	return lease, err
}

func (a *OrchestratorLeaseAuthority) Release(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string) error {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return err
	}
	return a.store.WithOrchestratorScopeTransition(ctx, identity, func(lockCtx context.Context) error {
		releaseErr := a.store.ReleaseOrchestratorScopeLease(lockCtx, identity, sessionID)
		if refreshErr := a.Refresh(lockCtx, identity.ProjectID); refreshErr != nil && releaseErr == nil {
			return refreshErr
		}
		return releaseErr
	})
}

func orchestratorScopeCacheKey(identity domain.OrchestratorIdentity) string {
	return string(identity.Scope.Kind) + "\x00" + string(identity.Scope.RootIssueID)
}

var _ OrchestratorScopeLeaseStore = (*RuntimeStateStore)(nil)

func (s *RuntimeStateStore) GetOrchestratorScopeLease(ctx context.Context, identity domain.OrchestratorIdentity) (OrchestratorScopeLease, bool, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return OrchestratorScopeLease{}, false, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return OrchestratorScopeLease{}, false, err
	}
	return scanOrchestratorLease(db.QueryRowContext(ctx, `SELECT session_id, lifecycle, acquired_at, updated_at, complete_since, last_wake_at, last_wake_reason, cursor FROM `+orchestratorLeaseTable+` WHERE project_id = ? AND scope_kind = ? AND root_issue_id = ?`, identity.ProjectID, identity.Scope.Kind, identity.Scope.RootIssueID), identity)
}

func (s *RuntimeStateStore) ListOrchestratorScopeLeases(ctx context.Context, projectID string) ([]OrchestratorScopeLease, error) {
	projectID = normalizedProjectID(projectID)
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT scope_kind, root_issue_id, session_id, lifecycle, acquired_at, updated_at, complete_since, last_wake_at, last_wake_reason, cursor FROM `+orchestratorLeaseTable+` WHERE project_id = ? ORDER BY scope_kind, root_issue_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list orchestrator scope leases: %w", err)
	}
	defer rows.Close()
	var leases []OrchestratorScopeLease
	for rows.Next() {
		var kind domain.OrchestrationScopeKind
		var root, sessionID, lifecycle, acquired, updated, wakeReason string
		var completeSince, lastWake sql.NullString
		var cursor int64
		if err := rows.Scan(&kind, &root, &sessionID, &lifecycle, &acquired, &updated, &completeSince, &lastWake, &wakeReason, &cursor); err != nil {
			return nil, fmt.Errorf("scan orchestrator scope lease: %w", err)
		}
		identity, err := domain.NewOrchestratorIdentity(projectID, domain.OrchestrationScope{Kind: kind, RootIssueID: naming.IssueID(root)})
		if err != nil {
			return nil, fmt.Errorf("decode orchestrator scope lease: %w", err)
		}
		lease, err := decodeOrchestratorLease(identity, sessionID, lifecycle, acquired, updated, completeSince, lastWake, wakeReason, cursor)
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

func (s *RuntimeStateStore) AcquireOrchestratorScopeLease(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string, probe SessionRuntimeProbe) (result OrchestratorLeaseAcquireResult, err error) {
	identity, err = normalizeOrchestratorIdentity(identity)
	if err != nil {
		return result, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return result, fmt.Errorf("acquire orchestrator scope lease: session id is required")
	}
	if probe == nil {
		return result, fmt.Errorf("acquire orchestrator scope lease: runtime probe is required")
	}
	err = s.withRetryingWriteLock(ctx, "acquire_orchestrator_scope_lease", func(writeCtx context.Context) error {
		result = OrchestratorLeaseAcquireResult{}
		db, dbErr := s.dbHandle()
		if dbErr != nil {
			return dbErr
		}
		result, dbErr = acquireOrchestratorScopeLeaseWith(writeCtx, db, identity, sessionID, probe)
		return dbErr
	})
	return result, err
}

func (s *RuntimeStateStore) AcquireRootedOrchestratorScopeLease(ctx context.Context, identity domain.OrchestratorIdentity, session Session, probe SessionRuntimeProbe) (result OrchestratorLeaseAcquireResult, err error) {
	identity, err = normalizeOrchestratorIdentity(identity)
	if err != nil {
		return result, err
	}
	if identity.Scope.Kind != domain.OrchestrationScopeRooted {
		return result, fmt.Errorf("acquire rooted orchestrator scope lease: rooted scope is required")
	}
	session.ID = strings.TrimSpace(session.ID)
	session.IssueID = identity.Scope.RootIssueID.String()
	session.Role = SessionRoleOrchestrator
	session.ScopeKind = SessionScopeOrchestration
	session.ScopeID = identity.Scope.RootIssueID.String()
	session.State = NormalizeSessionState(session.State)
	session.ObservedState = NormalizeSessionState(session.ObservedState)
	session.Activity = strings.ToLower(strings.TrimSpace(session.Activity))
	session.ActivitySource = strings.ToLower(strings.TrimSpace(session.ActivitySource))
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = time.Now().UTC()
	}
	if session.StartedAt == nil && session.State != SessionStateStopped {
		started := session.UpdatedAt.UTC()
		session.StartedAt = &started
	}
	normalizeSessionProjectionActivity(&session)
	if err := ValidateSessionProduct(session); err != nil {
		return result, fmt.Errorf("acquire rooted orchestrator scope lease: %w", err)
	}
	if probe == nil {
		return result, fmt.Errorf("acquire rooted orchestrator scope lease: runtime probe is required")
	}
	err = s.withRetryingWriteLock(ctx, "acquire_rooted_orchestrator_scope_lease", func(writeCtx context.Context) error {
		db, dbErr := s.dbHandle()
		if dbErr != nil {
			return dbErr
		}
		tx, txErr := db.BeginTx(writeCtx, nil)
		if txErr != nil {
			return fmt.Errorf("begin rooted orchestrator scope transition: %w", txErr)
		}
		defer func() { _ = tx.Rollback() }()
		result, txErr = acquireOrchestratorScopeLeaseWith(writeCtx, tx, identity, session.ID, probe)
		if txErr != nil {
			return txErr
		}
		if hook, _ := writeCtx.Value(rootedOrchestratorTransitionHookKey{}).(func(string) error); hook != nil {
			if hookErr := hook("after_lease_acquire"); hookErr != nil {
				return hookErr
			}
		}
		if txErr = s.upsertPreparedSessionStateTx(writeCtx, tx, identity.ProjectID, session, sessionStateTable); txErr != nil {
			return txErr
		}
		if txErr = tx.Commit(); txErr != nil {
			return fmt.Errorf("commit rooted orchestrator scope transition: %w", txErr)
		}
		return nil
	})
	return result, err
}

type orchestratorLeaseQueryExecer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func acquireOrchestratorScopeLeaseWith(ctx context.Context, db orchestratorLeaseQueryExecer, identity domain.OrchestratorIdentity, sessionID string, probe SessionRuntimeProbe) (OrchestratorLeaseAcquireResult, error) {
	existing, found, err := scanOrchestratorLease(db.QueryRowContext(ctx, `SELECT session_id, lifecycle, acquired_at, updated_at, complete_since, last_wake_at, last_wake_reason, cursor FROM `+orchestratorLeaseTable+` WHERE project_id = ? AND scope_kind = ? AND root_issue_id = ?`, identity.ProjectID, identity.Scope.Kind, identity.Scope.RootIssueID), identity)
	if err != nil {
		return OrchestratorLeaseAcquireResult{}, err
	}
	disposition := OrchestratorLeaseAcquired
	acquiredAt := time.Now().UTC()
	if found {
		acquiredAt = existing.AcquiredAt
		if existing.SessionID == sessionID {
			return OrchestratorLeaseAcquireResult{Lease: existing, Disposition: OrchestratorLeaseAttached}, nil
		}
		if existing.Lifecycle == domain.OrchestratorPaused {
			return OrchestratorLeaseAcquireResult{}, &OrchestratorLeaseConflictError{Lease: existing}
		}
		live, probeErr := probe(ctx, existing.SessionID)
		if probeErr != nil {
			return OrchestratorLeaseAcquireResult{}, fmt.Errorf("probe orchestrator session %s: %w", existing.SessionID, probeErr)
		}
		if live {
			return OrchestratorLeaseAcquireResult{}, &OrchestratorLeaseConflictError{Lease: existing}
		}
		disposition = OrchestratorLeaseRecoveredStale
		acquiredAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	lease := OrchestratorScopeLease{Identity: identity, SessionID: sessionID, Lifecycle: domain.OrchestratorWorking, AcquiredAt: acquiredAt, UpdatedAt: now}
	if found {
		lease.Cursor, lease.LastWakeAt, lease.LastWakeReason = existing.Cursor, existing.LastWakeAt, existing.LastWakeReason
	}
	if err := upsertOrchestratorLeaseWith(ctx, db, lease); err != nil {
		return OrchestratorLeaseAcquireResult{}, err
	}
	return OrchestratorLeaseAcquireResult{Lease: lease, Disposition: disposition}, nil
}

func (s *RuntimeStateStore) SetOrchestratorScopeLeaseLifecycle(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string, lifecycle domain.OrchestratorLifecycle) (lease OrchestratorScopeLease, err error) {
	if !validOrchestratorLifecycle(lifecycle) {
		return lease, fmt.Errorf("invalid orchestrator lifecycle %q", lifecycle)
	}
	identity, err = normalizeOrchestratorIdentity(identity)
	if err != nil {
		return lease, err
	}
	err = s.withRetryingWriteLock(ctx, "set_orchestrator_scope_lease_lifecycle", func(writeCtx context.Context) error {
		lease = OrchestratorScopeLease{}
		current, found, loadErr := s.GetOrchestratorScopeLease(writeCtx, identity)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return ErrOrchestratorLeaseNotFound
		}
		if current.SessionID != strings.TrimSpace(sessionID) {
			return fmt.Errorf("%w: scope owned by session %s", ErrOrchestratorLeaseConflict, current.SessionID)
		}
		current.Lifecycle, current.UpdatedAt = lifecycle, time.Now().UTC()
		if writeErr := s.upsertOrchestratorLease(writeCtx, current); writeErr != nil {
			return writeErr
		}
		lease = current
		return nil
	})
	return lease, err
}

func (s *RuntimeStateStore) EvaluateOrchestratorScopeLease(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string, now time.Time, facts domain.OrchestratorLifecycleFacts, policy domain.OrchestratorLifecyclePolicy) (lease OrchestratorScopeLease, err error) {
	identity, err = normalizeOrchestratorIdentity(identity)
	if err != nil {
		return lease, err
	}
	now = now.UTC()
	err = s.withRetryingWriteLock(ctx, "evaluate_orchestrator_scope_lease", func(writeCtx context.Context) error {
		lease = OrchestratorScopeLease{}
		current, found, loadErr := s.GetOrchestratorScopeLease(writeCtx, identity)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return ErrOrchestratorLeaseNotFound
		}
		if current.SessionID != strings.TrimSpace(sessionID) {
			return fmt.Errorf("%w: scope owned by session %s", ErrOrchestratorLeaseConflict, current.SessionID)
		}
		if facts.Complete() {
			if current.CompleteSince == nil {
				started := now
				current.CompleteSince = &started
			}
		} else {
			current.CompleteSince = nil
		}
		facts.CompleteSince = current.CompleteSince
		current.Lifecycle = domain.EvaluateOrchestratorLifecycle(now, facts, policy)
		current.UpdatedAt = now
		if writeErr := s.upsertOrchestratorLease(writeCtx, current); writeErr != nil {
			return writeErr
		}
		lease = current
		return nil
	})
	return lease, err
}

func (s *RuntimeStateStore) WakeOrchestratorScopeLease(ctx context.Context, identity domain.OrchestratorIdentity, now time.Time, reason domain.OrchestratorWakeReason, policy domain.OrchestratorLifecyclePolicy) (lease OrchestratorScopeLease, changed bool, err error) {
	if !reason.Valid() {
		return lease, false, fmt.Errorf("invalid orchestrator wake reason %q", reason)
	}
	identity, err = normalizeOrchestratorIdentity(identity)
	if err != nil {
		return lease, false, err
	}
	now = now.UTC()
	err = s.withRetryingWriteLock(ctx, "wake_orchestrator_scope_lease", func(writeCtx context.Context) error {
		lease, changed = OrchestratorScopeLease{}, false
		current, found, loadErr := s.GetOrchestratorScopeLease(writeCtx, identity)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return ErrOrchestratorLeaseNotFound
		}
		if current.LastWakeAt != nil && !policy.WakeAllowed(*current.LastWakeAt, now) {
			lease = current
			return nil
		}
		current.Lifecycle, current.CompleteSince, current.LastWakeReason = domain.OrchestratorWorking, nil, reason
		current.LastWakeAt, current.UpdatedAt = &now, now
		if writeErr := s.upsertOrchestratorLease(writeCtx, current); writeErr != nil {
			return writeErr
		}
		lease, changed = current, true
		return nil
	})
	return lease, changed, err
}

func (s *RuntimeStateStore) AdvanceOrchestratorScopeCursor(ctx context.Context, identity domain.OrchestratorIdentity, cursor int64) (lease OrchestratorScopeLease, err error) {
	identity, err = normalizeOrchestratorIdentity(identity)
	if err != nil {
		return lease, err
	}
	if cursor < 0 {
		return lease, fmt.Errorf("orchestrator cursor cannot be negative")
	}
	err = s.withRetryingWriteLock(ctx, "advance_orchestrator_scope_cursor", func(writeCtx context.Context) error {
		lease = OrchestratorScopeLease{}
		current, found, loadErr := s.GetOrchestratorScopeLease(writeCtx, identity)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return ErrOrchestratorLeaseNotFound
		}
		if cursor > current.Cursor {
			current.Cursor = cursor
			current.UpdatedAt = time.Now().UTC()
			if writeErr := s.upsertOrchestratorLease(writeCtx, current); writeErr != nil {
				return writeErr
			}
		}
		lease = current
		return nil
	})
	return lease, err
}

func (s *RuntimeStateStore) ReleaseOrchestratorScopeLease(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string) error {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return err
	}
	return s.withRetryingWriteLock(ctx, "release_orchestrator_scope_lease", func(writeCtx context.Context) error {
		current, found, loadErr := s.GetOrchestratorScopeLease(writeCtx, identity)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return nil
		}
		if current.SessionID != strings.TrimSpace(sessionID) {
			return fmt.Errorf("%w: scope owned by session %s", ErrOrchestratorLeaseConflict, current.SessionID)
		}
		db, dbErr := s.dbHandle()
		if dbErr != nil {
			return dbErr
		}
		_, dbErr = db.ExecContext(writeCtx, `DELETE FROM `+orchestratorLeaseTable+` WHERE project_id = ? AND scope_kind = ? AND root_issue_id = ?`, identity.ProjectID, identity.Scope.Kind, identity.Scope.RootIssueID)
		return dbErr
	})
}

func (s *RuntimeStateStore) upsertOrchestratorLease(ctx context.Context, lease OrchestratorScopeLease) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	return upsertOrchestratorLeaseWith(ctx, db, lease)
}

func upsertOrchestratorLeaseWith(ctx context.Context, db orchestratorLeaseQueryExecer, lease OrchestratorScopeLease) error {
	_, err := db.ExecContext(ctx, `INSERT INTO `+orchestratorLeaseTable+` (project_id, scope_kind, root_issue_id, session_id, lifecycle, acquired_at, updated_at, complete_since, last_wake_at, last_wake_reason, cursor) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(project_id, scope_kind, root_issue_id) DO UPDATE SET session_id=excluded.session_id, lifecycle=excluded.lifecycle, acquired_at=excluded.acquired_at, updated_at=excluded.updated_at, complete_since=excluded.complete_since, last_wake_at=excluded.last_wake_at, last_wake_reason=excluded.last_wake_reason, cursor=excluded.cursor`, lease.Identity.ProjectID, lease.Identity.Scope.Kind, lease.Identity.Scope.RootIssueID, lease.SessionID, lease.Lifecycle, lease.AcquiredAt.Format(time.RFC3339Nano), lease.UpdatedAt.Format(time.RFC3339Nano), nullableLeaseTime(lease.CompleteSince), nullableLeaseTime(lease.LastWakeAt), lease.LastWakeReason, lease.Cursor)
	if err != nil {
		return fmt.Errorf("upsert orchestrator scope lease: %w", err)
	}
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanOrchestratorLease(row rowScanner, identity domain.OrchestratorIdentity) (OrchestratorScopeLease, bool, error) {
	var sessionID, lifecycle, acquired, updated, wakeReason string
	var completeSince, lastWake sql.NullString
	var cursor int64
	if err := row.Scan(&sessionID, &lifecycle, &acquired, &updated, &completeSince, &lastWake, &wakeReason, &cursor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OrchestratorScopeLease{}, false, nil
		}
		return OrchestratorScopeLease{}, false, fmt.Errorf("get orchestrator scope lease: %w", err)
	}
	lease, err := decodeOrchestratorLease(identity, sessionID, lifecycle, acquired, updated, completeSince, lastWake, wakeReason, cursor)
	return lease, err == nil, err
}

func decodeOrchestratorLease(identity domain.OrchestratorIdentity, sessionID, lifecycle, acquired, updated string, completeSince, lastWake sql.NullString, wakeReason string, cursor int64) (OrchestratorScopeLease, error) {
	acquiredAt, err := time.Parse(time.RFC3339Nano, acquired)
	if err != nil {
		return OrchestratorScopeLease{}, fmt.Errorf("parse orchestrator lease acquired_at: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return OrchestratorScopeLease{}, fmt.Errorf("parse orchestrator lease updated_at: %w", err)
	}
	state := domain.OrchestratorLifecycle(lifecycle)
	if !validOrchestratorLifecycle(state) {
		return OrchestratorScopeLease{}, fmt.Errorf("invalid persisted orchestrator lifecycle %q", lifecycle)
	}
	completeAt, err := parseNullableLeaseTime("complete_since", completeSince)
	if err != nil {
		return OrchestratorScopeLease{}, err
	}
	wakeAt, err := parseNullableLeaseTime("last_wake_at", lastWake)
	if err != nil {
		return OrchestratorScopeLease{}, err
	}
	return OrchestratorScopeLease{Identity: identity, SessionID: sessionID, Lifecycle: state, AcquiredAt: acquiredAt, UpdatedAt: updatedAt, CompleteSince: completeAt, LastWakeAt: wakeAt, LastWakeReason: domain.OrchestratorWakeReason(wakeReason), Cursor: cursor}, nil
}

func nullableLeaseTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func parseNullableLeaseTime(name string, value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, fmt.Errorf("parse orchestrator lease %s: %w", name, err)
	}
	return &parsed, nil
}

func normalizeOrchestratorIdentity(identity domain.OrchestratorIdentity) (domain.OrchestratorIdentity, error) {
	return domain.NewOrchestratorIdentity(identity.ProjectID, identity.Scope)
}

func validOrchestratorLifecycle(lifecycle domain.OrchestratorLifecycle) bool {
	switch lifecycle {
	case domain.OrchestratorWorking, domain.OrchestratorQuiescent, domain.OrchestratorCompleteGrace, domain.OrchestratorPaused:
		return true
	default:
		return false
	}
}
