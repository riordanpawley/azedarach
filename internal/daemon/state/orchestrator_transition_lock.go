package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

const orchestratorTransitionLockPollInterval = 10 * time.Millisecond

type orchestratorTransitionLockContextKey struct{}

// WithOrchestratorScopeTransition serializes one exact orchestrator scope
// across daemon processes while lease, desired intent, and tmux runtime state
// converge. The stable lock file is intentionally retained so concurrent
// processes never switch to different inodes while a transition is active.
func (s *RuntimeStateStore) WithOrchestratorScopeTransition(ctx context.Context, identity domain.OrchestratorIdentity, fn func(context.Context) error) error {
	if s == nil {
		return fmt.Errorf("orchestrator scope transition store is unavailable")
	}
	if fn == nil {
		return fmt.Errorf("orchestrator scope transition callback is required")
	}
	identity, err := normalizeOrchestratorIdentity(identity)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lockPath := orchestratorTransitionLockPath(s.dbPath, identity)
	if held, _ := ctx.Value(orchestratorTransitionLockContextKey{}).(map[string]struct{}); held != nil {
		if _, ok := held[lockPath]; ok {
			return fn(ctx)
		}
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open orchestrator scope transition lock: %w", err)
	}
	defer file.Close()

	ticker := time.NewTicker(orchestratorTransitionLockPollInterval)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("acquire orchestrator scope transition lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck
	held, _ := ctx.Value(orchestratorTransitionLockContextKey{}).(map[string]struct{})
	next := make(map[string]struct{}, len(held)+1)
	for heldPath := range held {
		next[heldPath] = struct{}{}
	}
	next[lockPath] = struct{}{}
	return fn(context.WithValue(ctx, orchestratorTransitionLockContextKey{}, next))
}

func orchestratorTransitionLockPath(dbPath string, identity domain.OrchestratorIdentity) string {
	key := identity.ProjectID + "\x00" + string(identity.Scope.Kind) + "\x00" + identity.Scope.RootIssueID.String()
	digest := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%s.orchestrator-%x.lock", sqliteutil.CanonicalPath(dbPath), digest[:12])
}
