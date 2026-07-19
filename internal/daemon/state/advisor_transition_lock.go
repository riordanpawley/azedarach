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

const advisorTransitionLockPollInterval = 10 * time.Millisecond

// WithAdvisorSessionTransition serializes one interaction's durable advisor
// reservation and tmux launch without holding the SQLite write lock needed by
// the agent's hook acknowledgement.
func (s *RuntimeStateStore) WithAdvisorSessionTransition(ctx context.Context, projectID, requestID string, fn func(context.Context) error) error {
	if s == nil || fn == nil {
		return errors.New("advisor session transition store and callback are required")
	}
	projectID, requestID = strings.TrimSpace(projectID), strings.TrimSpace(requestID)
	if projectID == "" || requestID == "" {
		return errors.New("advisor session transition identity is incomplete")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	digest := sha256.Sum256([]byte(projectID + "\x00" + requestID))
	lockPath := fmt.Sprintf("%s.advisor-transition-%x.lock", sqliteutil.CanonicalPath(s.dbPath), digest[:12])
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open advisor transition lock: %w", err)
	}
	defer file.Close()
	ticker := time.NewTicker(advisorTransitionLockPollInterval)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("acquire advisor transition lock: %w", err)
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
