package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

var (
	currentParentPID        = os.Getppid
	watchParentPollInterval = 250 * time.Millisecond
)

func newWatchCommandContext(commandName string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	initialPPID := currentParentPID()
	if initialPPID <= 1 {
		warnWatchParentDisappeared(commandName, initialPPID, initialPPID)
		cancel()
		return ctx, cancel
	}
	go func() {
		ticker := time.NewTicker(watchParentPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentPPID := currentParentPID()
				if currentPPID <= 1 {
					warnWatchParentDisappeared(commandName, initialPPID, currentPPID)
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func sleepWatchPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isWatchContextDone(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func warnWatchParentDisappeared(commandName string, initialPPID, currentPPID int) {
	fmt.Fprintf(os.Stderr, "warning: az %s exiting because its owning parent process disappeared (initial_ppid=%d current_ppid=%d)\n", commandName, initialPPID, currentPPID)
}
