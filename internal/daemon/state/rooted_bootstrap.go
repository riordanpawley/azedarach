package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// RootedBootstrapAcknowledgement is the durable accepted prompt-delivery
// projection for one rooted orchestration scope and live tmux marker.
type RootedBootstrapAcknowledgement struct {
	Identity       domain.OrchestratorIdentity
	SessionID      string
	PromptHash     string
	RuntimeNonce   string
	AcknowledgedAt time.Time
	UpdatedAt      time.Time
}

type RootedBootstrapAcknowledgementStore interface {
	ListRootedBootstrapAcknowledgements(context.Context, string) ([]RootedBootstrapAcknowledgement, error)
	UpsertRootedBootstrapAcknowledgement(context.Context, RootedBootstrapAcknowledgement) error
	DeleteRootedBootstrapAcknowledgement(context.Context, domain.OrchestratorIdentity, string) error
}

// RootedBootstrapAcknowledgementAuthority enforces refresh-then-cache reads.
// Callers combine its durable projection with live tmux only through the
// rooted-bootstrap hybrid invariant.
type RootedBootstrapAcknowledgementAuthority struct {
	store RootedBootstrapAcknowledgementStore
	mu    sync.RWMutex
	cache map[string]map[string]RootedBootstrapAcknowledgement
}

func NewRootedBootstrapAcknowledgementAuthority(store RootedBootstrapAcknowledgementStore) *RootedBootstrapAcknowledgementAuthority {
	return &RootedBootstrapAcknowledgementAuthority{store: store, cache: make(map[string]map[string]RootedBootstrapAcknowledgement)}
}

func (a *RootedBootstrapAcknowledgementAuthority) Refresh(ctx context.Context, projectID string) error {
	projectID = normalizedProjectID(projectID)
	rows, err := a.store.ListRootedBootstrapAcknowledgements(ctx, projectID)
	if err != nil {
		return err
	}
	refreshed := make(map[string]RootedBootstrapAcknowledgement, len(rows))
	for _, row := range rows {
		refreshed[row.Identity.Scope.RootIssueID.String()] = row
	}
	a.mu.Lock()
	a.cache[projectID] = refreshed
	a.mu.Unlock()
	return nil
}

func (a *RootedBootstrapAcknowledgementAuthority) Get(ctx context.Context, identity domain.OrchestratorIdentity) (RootedBootstrapAcknowledgement, bool, error) {
	identity, err := normalizeRootedBootstrapIdentity(identity)
	if err != nil {
		return RootedBootstrapAcknowledgement{}, false, err
	}
	if err := a.Refresh(ctx, identity.ProjectID); err != nil {
		return RootedBootstrapAcknowledgement{}, false, err
	}
	a.mu.RLock()
	row, found := a.cache[identity.ProjectID][identity.Scope.RootIssueID.String()]
	a.mu.RUnlock()
	return row, found, nil
}

func (a *RootedBootstrapAcknowledgementAuthority) FindBySession(ctx context.Context, projectID, sessionID string) (RootedBootstrapAcknowledgement, bool, error) {
	projectID, sessionID = normalizedProjectID(projectID), strings.TrimSpace(sessionID)
	if err := a.Refresh(ctx, projectID); err != nil {
		return RootedBootstrapAcknowledgement{}, false, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, row := range a.cache[projectID] {
		if row.SessionID == sessionID {
			return row, true, nil
		}
	}
	return RootedBootstrapAcknowledgement{}, false, nil
}

func (a *RootedBootstrapAcknowledgementAuthority) Acknowledge(ctx context.Context, acknowledgement RootedBootstrapAcknowledgement) error {
	if err := a.store.UpsertRootedBootstrapAcknowledgement(ctx, acknowledgement); err != nil {
		return err
	}
	return a.Refresh(ctx, acknowledgement.Identity.ProjectID)
}

func (a *RootedBootstrapAcknowledgementAuthority) Invalidate(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string) error {
	identity, err := normalizeRootedBootstrapIdentity(identity)
	if err != nil {
		return err
	}
	if err := a.store.DeleteRootedBootstrapAcknowledgement(ctx, identity, sessionID); err != nil {
		return err
	}
	return a.Refresh(ctx, identity.ProjectID)
}

var _ RootedBootstrapAcknowledgementStore = (*RuntimeStateStore)(nil)

func (s *RuntimeStateStore) ListRootedBootstrapAcknowledgements(ctx context.Context, projectID string) ([]RootedBootstrapAcknowledgement, error) {
	projectID = normalizedProjectID(projectID)
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT root_issue_id,session_id,prompt_hash,runtime_nonce,acknowledged_at,updated_at FROM `+rootedBootstrapAckTable+` WHERE project_id=? ORDER BY root_issue_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list rooted bootstrap acknowledgements: %w", err)
	}
	defer rows.Close()
	var out []RootedBootstrapAcknowledgement
	for rows.Next() {
		var rootID, sessionID, promptHash, runtimeNonce, acknowledgedAt, updatedAt string
		if err := rows.Scan(&rootID, &sessionID, &promptHash, &runtimeNonce, &acknowledgedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan rooted bootstrap acknowledgement: %w", err)
		}
		identity, err := domain.NewOrchestratorIdentity(projectID, domain.OrchestrationScope{Kind: domain.OrchestrationScopeRooted, RootIssueID: naming.IssueID(rootID)})
		if err != nil {
			return nil, fmt.Errorf("decode rooted bootstrap identity: %w", err)
		}
		ackTime, err := time.Parse(time.RFC3339Nano, acknowledgedAt)
		if err != nil {
			return nil, fmt.Errorf("parse rooted bootstrap acknowledged_at: %w", err)
		}
		updateTime, err := time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse rooted bootstrap updated_at: %w", err)
		}
		out = append(out, RootedBootstrapAcknowledgement{Identity: identity, SessionID: sessionID, PromptHash: promptHash, RuntimeNonce: runtimeNonce, AcknowledgedAt: ackTime, UpdatedAt: updateTime})
	}
	return out, rows.Err()
}

func (s *RuntimeStateStore) UpsertRootedBootstrapAcknowledgement(ctx context.Context, acknowledgement RootedBootstrapAcknowledgement) error {
	acknowledgement, err := normalizeRootedBootstrapAcknowledgement(acknowledgement)
	if err != nil {
		return err
	}
	return s.withRetryingWriteLock(ctx, "upsert_rooted_bootstrap_acknowledgement", func(writeCtx context.Context) error {
		db, err := s.dbHandle()
		if err != nil {
			return err
		}
		_, err = db.ExecContext(writeCtx, `INSERT INTO `+rootedBootstrapAckTable+` (project_id,root_issue_id,session_id,prompt_hash,runtime_nonce,acknowledged_at,updated_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(project_id,root_issue_id) DO UPDATE SET session_id=excluded.session_id,prompt_hash=excluded.prompt_hash,runtime_nonce=excluded.runtime_nonce,acknowledged_at=excluded.acknowledged_at,updated_at=excluded.updated_at`, acknowledgement.Identity.ProjectID, acknowledgement.Identity.Scope.RootIssueID, acknowledgement.SessionID, acknowledgement.PromptHash, acknowledgement.RuntimeNonce, acknowledgement.AcknowledgedAt.Format(time.RFC3339Nano), acknowledgement.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("upsert rooted bootstrap acknowledgement: %w", err)
		}
		return nil
	})
}

func (s *RuntimeStateStore) DeleteRootedBootstrapAcknowledgement(ctx context.Context, identity domain.OrchestratorIdentity, sessionID string) error {
	identity, err := normalizeRootedBootstrapIdentity(identity)
	if err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	return s.withRetryingWriteLock(ctx, "delete_rooted_bootstrap_acknowledgement", func(writeCtx context.Context) error {
		db, err := s.dbHandle()
		if err != nil {
			return err
		}
		result, err := db.ExecContext(writeCtx, `DELETE FROM `+rootedBootstrapAckTable+` WHERE project_id=? AND root_issue_id=? AND session_id=?`, identity.ProjectID, identity.Scope.RootIssueID, sessionID)
		if err != nil {
			return fmt.Errorf("delete rooted bootstrap acknowledgement: %w", err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return err
		} else if affected == 0 {
			return nil
		}
		return nil
	})
}

func normalizeRootedBootstrapAcknowledgement(row RootedBootstrapAcknowledgement) (RootedBootstrapAcknowledgement, error) {
	identity, err := normalizeRootedBootstrapIdentity(row.Identity)
	if err != nil {
		return row, err
	}
	row.Identity = identity
	row.SessionID, row.PromptHash, row.RuntimeNonce = strings.TrimSpace(row.SessionID), strings.TrimSpace(row.PromptHash), strings.TrimSpace(row.RuntimeNonce)
	if row.SessionID == "" || row.PromptHash == "" || row.RuntimeNonce == "" {
		return row, errors.New("rooted bootstrap acknowledgement requires session, prompt hash, and runtime nonce")
	}
	if row.AcknowledgedAt.IsZero() {
		return row, errors.New("rooted bootstrap acknowledgement requires acknowledged time")
	}
	row.AcknowledgedAt = row.AcknowledgedAt.UTC()
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = row.AcknowledgedAt
	}
	row.UpdatedAt = row.UpdatedAt.UTC()
	return row, nil
}

func normalizeRootedBootstrapIdentity(identity domain.OrchestratorIdentity) (domain.OrchestratorIdentity, error) {
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return identity, err
	}
	if identity.Scope.Kind != domain.OrchestrationScopeRooted {
		return identity, errors.New("rooted bootstrap acknowledgement requires rooted orchestration scope")
	}
	return identity, nil
}
