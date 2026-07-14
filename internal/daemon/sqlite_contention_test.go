package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestBoardFetchRemainsAvailableDuringIssueAndRuntimeProjectionWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("project id: %v", err)
	}
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	if err := client.EnsureBoardViews(ctx, projectID); err != nil {
		t.Fatalf("initialize board views: %v", err)
	}
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "contention target", Type: domain.TypeBug, Priority: domain.P1})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: slog.Default()},
		issues:                 client,
		issueClientsByRoot:     map[string]*issues.Client{daemonStoreRootKey(repoDir): client},
		issueClientsByProject:  map[string]*issues.Client{projectID: client},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{daemonStoreRootKey(repoDir): runtimeStore},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		uiState:                map[string]string{},
		revision:               map[string]uint64{},
	}

	errCh := make(chan error, 64)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		errCh <- client.WithMutationLock(ctx, func(lockCtx context.Context) error {
			if err := client.Update(lockCtx, issueID, domain.StatusInProgress); err != nil {
				return fmt.Errorf("status active: %w", err)
			}
			if err := runtimeStore.UpsertSessionState(lockCtx, projectID, daemonstate.Session{ID: naming.CanonicalSessionID(projectID, issueID), IssueID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC()}); err != nil {
				return fmt.Errorf("runtime projection: %w", err)
			}
			return client.Update(lockCtx, issueID, domain.StatusInReview)
		})
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: issueID, Path: repoDir, Branch: "contention", UpdatedAt: time.Now().UTC()})
			if err != nil {
				errCh <- fmt.Errorf("runtime write %d: %w", i, err)
				return
			}
		}
		errCh <- nil
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			resp, err := d.handleBoardViewList(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}})
			if err != nil {
				errCh <- fmt.Errorf("board fetch %d: %w", i, err)
				return
			}
			if !resp.OK {
				errCh <- fmt.Errorf("board fetch %d failed: %+v", i, resp.Error)
				return
			}
		}
		errCh <- nil
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("contention workload exceeded bounded latency: %v", ctx.Err())
	}
}
