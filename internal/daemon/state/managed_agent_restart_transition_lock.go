package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

const managedAgentRestartTransitionLockPollInterval = 10 * time.Millisecond

// WithManagedAgentRestartTransition serializes replacement of one exact
// managed-agent incarnation across daemon processes. The lock is deliberately
// scoped to the old incarnation: after a successful replacement, a waiter must
// refresh durable and live identity and refuse to act on its stale candidate.
func (s *RuntimeStateStore) WithManagedAgentRestartTransition(ctx context.Context, projectID, sessionID, logicalPaneID, incarnation string, fn func(context.Context) error) error {
	if s == nil {
		return errors.New("managed-agent restart transition store is unavailable")
	}
	if fn == nil {
		return errors.New("managed-agent restart transition callback is required")
	}
	projectID = strings.TrimSpace(projectID)
	sessionID = strings.TrimSpace(sessionID)
	logicalPaneID = strings.TrimSpace(logicalPaneID)
	incarnation = strings.TrimSpace(incarnation)
	if projectID == "" || sessionID == "" || logicalPaneID == "" || incarnation == "" {
		return errors.New("managed-agent restart transition identity is incomplete")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := projectID + "\x00" + sessionID + "\x00" + logicalPaneID + "\x00" + incarnation
	digest := sha256.Sum256([]byte(key))
	lockPath := fmt.Sprintf("%s.managed-agent-restart-%x.lock", sqliteutil.CanonicalPath(s.dbPath), digest[:12])
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open managed-agent restart transition lock: %w", err)
	}
	defer file.Close()

	ticker := time.NewTicker(managedAgentRestartTransitionLockPollInterval)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("acquire managed-agent restart transition lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck
	return fn(ctx)
}
