package cli

import (
	"context"
	"testing"
	"time"
)

func TestSleepWatchPollReturnsWhenWatchContextCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepWatchPoll(ctx, time.Hour)
	if err == nil {
		t.Fatal("sleepWatchPoll error = nil, want context cancellation")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("sleepWatchPoll elapsed = %v, want prompt cancellation", elapsed)
	}
}
