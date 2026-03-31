package git

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/network"
)

type syncRunner struct {
	mu                 sync.Mutex
	fetchCalls         int
	revListCalls       int
	activeFetches      int
	maxConcurrentFetch int
	firstFetchStarted  chan struct{}
	releaseFirstFetch  chan struct{}
}

func newSyncRunner() *syncRunner {
	return &syncRunner{
		firstFetchStarted: make(chan struct{}),
		releaseFirstFetch: make(chan struct{}),
	}
}

func (r *syncRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing git command")
	}

	switch args[0] {
	case "fetch":
		r.mu.Lock()
		r.fetchCalls++
		r.activeFetches++
		if r.activeFetches > r.maxConcurrentFetch {
			r.maxConcurrentFetch = r.activeFetches
		}
		callNumber := r.fetchCalls
		r.mu.Unlock()

		if callNumber == 1 {
			select {
			case <-r.firstFetchStarted:
			default:
				close(r.firstFetchStarted)
			}

			select {
			case <-r.releaseFirstFetch:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		r.mu.Lock()
		r.activeFetches--
		r.mu.Unlock()

		return "", nil

	case "rev-list":
		r.mu.Lock()
		r.revListCalls++
		r.mu.Unlock()
		return "3", nil

	default:
		return "", fmt.Errorf("unexpected git command: %v", args)
	}
}

func TestGitSyncServiceFetchAndCheck_CatchesUpWithoutOverlap(t *testing.T) {
	runner := newSyncRunner()
	client := NewClient(runner, slog.Default())
	checker := network.NewStatusChecker()
	cfg := config.DefaultConfig()
	cfg.Git.WorkflowMode = "origin"

	service := NewGitSyncService(client, checker, cfg, "/tmp/worktree", slog.Default())

	firstDone := make(chan struct{})
	secondDone := make(chan struct{})

	go func() {
		cmd := service.FetchAndCheck()
		_ = cmd()
		close(firstDone)
	}()

	select {
	case <-runner.firstFetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first fetch never started")
	}

	go func() {
		cmd := service.FetchAndCheck()
		_ = cmd()
		close(secondDone)
	}()

	time.Sleep(50 * time.Millisecond)
	close(runner.releaseFirstFetch)

	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first fetch did not finish")
	}

	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second fetch did not finish")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()

	if runner.fetchCalls != 2 {
		t.Fatalf("fetch calls = %d, want 2", runner.fetchCalls)
	}
	if runner.revListCalls != 2 {
		t.Fatalf("rev-list calls = %d, want 2", runner.revListCalls)
	}
	if runner.maxConcurrentFetch != 1 {
		t.Fatalf("max concurrent fetches = %d, want 1", runner.maxConcurrentFetch)
	}
}
