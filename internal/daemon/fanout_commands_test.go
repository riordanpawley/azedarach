package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

func TestHandleIssueFanoutDriftUsesProjectionChangedFiles(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj-fanout"
	issueID := "az-123"
	worktree := filepath.Join(repoDir, "wt-az-123")

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), nil)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rawStatus, err := json.Marshal(git.GitStatus{
		Modified:   []string{"internal/a.go"},
		Added:      []string{"cmd/new.go"},
		Untracked:  []string{"notes/todo.md"},
		HasChanges: true,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    "riordan/az-123/task",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree state: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Now().UTC()); err != nil {
		t.Fatalf("seed worktree git status: %v", err)
	}

	if err := saveFanoutRegistry(repoDir, map[string]fanoutRegistryEntry{
		issueID: {
			IssueID:      issueID,
			ParentIssue:  "az-parent",
			Key:          "n1",
			Kind:         "work",
			FileBudget:   []string{"internal/**"},
			CreatedAtUTC: time.Now().UTC().Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("save fanout registry: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	body, err := json.Marshal(protocol.FanoutDriftCommandBody{
		RepoDir: repoDir,
		IssueID: issueID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	resp, err := d.handleIssueFanoutDrift(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-fanout-drift",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandIssueFanoutDrift,
		Meta:            protocol.Metadata{ProjectID: projectID},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleIssueFanoutDrift error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("fanout drift response not OK: %+v", resp.Error)
	}

	var out protocol.FanoutDriftResult
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.IssueID != issueID {
		t.Fatalf("issue id = %q, want %q", out.IssueID, issueID)
	}
	if len(out.ChangedFiles) != 3 {
		t.Fatalf("changed files = %+v, want 3 projected files", out.ChangedFiles)
	}
	if len(out.OutOfBudget) != 2 {
		t.Fatalf("out of budget = %+v, want 2", out.OutOfBudget)
	}
}
