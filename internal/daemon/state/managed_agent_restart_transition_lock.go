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

// WithManagedAgentRestartTransition serializes replacement of one managed
// logical pane across daemon processes. The stable session/pane lock is reused
// across incarnations; callers keep the old incarnation as their stale-identity
// compare-and-refuse input after acquiring it.
func (s *RuntimeStateStore) WithManagedAgentRestartTransition(ctx context.Context, projectID, sessionID, logicalPaneID string, fn func(context.Context) error) error {
	if s == nil {
		return errors.New("managed-agent restart transition store is unavailable")
	}
	if fn == nil {
		return errors.New("managed-agent restart transition callback is required")
	}
	projectID = strings.TrimSpace(projectID)
	sessionID = strings.TrimSpace(sessionID)
	logicalPaneID = strings.TrimSpace(logicalPaneID)
	if projectID == "" || sessionID == "" || logicalPaneID == "" {
		return errors.New("managed-agent restart transition identity is incomplete")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := projectID + "\x00" + sessionID + "\x00" + logicalPaneID
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
