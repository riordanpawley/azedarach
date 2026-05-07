package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestIssueSpecServiceReadResolvesExternalCodeSelector(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)

	issueID, err := client.Create(ctx, issues.CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	ext := "EXT-123"
	_, err = client.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID:      "REQ-LOCAL",
		ExternalCode: &ext,
		Title:        "requirement",
	})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}

	_, err = client.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: ext,
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	service := newTestIssueSpecService(client, repoDir)
	out, err := service.Read(ctx, protocol.SpecReadRequestBody{
		IssueID: naming.IssueID(issueID),
		ReqID:   naming.RequirementID(ext),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out.Requirements) != 1 {
		t.Fatalf("requirements len = %d, want 1", len(out.Requirements))
	}
	if out.Requirements[0].ID != "REQ-LOCAL" {
		t.Fatalf("requirement id = %q, want REQ-LOCAL", out.Requirements[0].ID)
	}
	if len(out.Links) != 1 {
		t.Fatalf("links len = %d, want 1", len(out.Links))
	}
	if out.Links[0].ReqID != "REQ-LOCAL" {
		t.Fatalf("link req_id = %q, want REQ-LOCAL", out.Links[0].ReqID)
	}
}

func TestIssueSpecServiceReadIssueScopeIncludesLinkedRequirementsWithoutIssueID(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)

	issueID, err := client.Create(ctx, issues.CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	_, err = client.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID: "REQ-LINKED",
		Title:   "linked requirement without issue_id",
	})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	_, err = client.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-LINKED",
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	service := newTestIssueSpecService(client, repoDir)
	out, err := service.Read(ctx, protocol.SpecReadRequestBody{IssueID: naming.IssueID(issueID)})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out.Links) != 1 {
		t.Fatalf("links len = %d, want 1", len(out.Links))
	}
	if len(out.Requirements) != 1 {
		t.Fatalf("requirements len = %d, want 1", len(out.Requirements))
	}
	if out.Requirements[0].ID != "REQ-LINKED" {
		t.Fatalf("requirement id = %q, want REQ-LINKED", out.Requirements[0].ID)
	}
}

func TestIssueSpecServiceLintDoesNotFailOnOverlappingLocalAndExternalCodes(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)

	issueID, err := client.Create(ctx, issues.CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	_, err = client.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID: "REQ-A",
		Title:   "requirement A",
	})
	if err != nil {
		t.Fatalf("create requirement A: %v", err)
	}

	_, err = client.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-A",
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	ext := "REQ-A"
	_, err = client.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID:      "REQ-B",
		ExternalCode: &ext,
		Title:        "requirement B",
	})
	if err != nil {
		t.Fatalf("create requirement B: %v", err)
	}

	service := newTestIssueSpecService(client, repoDir)
	out, err := service.Lint(ctx, protocol.SpecLintRequestBody{})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if out.OK {
		t.Fatalf("lint OK = true, want false due to unlinked REQ-B")
	}
	if len(out.Diagnostics) == 0 {
		t.Fatalf("diagnostics empty, want unlinked requirement diagnostic")
	}
}

func newTestIssueSpecService(client *issues.Client, repoDir string) issueSpecService {
	d := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
		},
		issues: client,
		issueClientsByProject: map[string]*issues.Client{
			protocol.DefaultProjectID: client,
		},
		issueClientsByRoot: map[string]*issues.Client{
			repoDir: client,
		},
	}
	return issueSpecService{daemon: d}
}

func newTestIssueClient(t *testing.T) (*issues.Client, string) {
	t.Helper()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	client := issues.NewClient(repoDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = client.CloseDB() })
	return client, repoDir
}

type countingWorktreeListRunner struct {
	listCalls int
}

func (r *countingWorktreeListRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		r.listCalls++
		return "", errors.New("live worktree list should not be called for projection-only reads")
	}
	return "", nil
}

type staticWorktreeListRunner struct {
	listCalls int
	output    string
	commands  []string
}

func (r *staticWorktreeListRunner) Run(_ context.Context, args ...string) (string, error) {
	r.commands = append(r.commands, strings.Join(args, " "))
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		r.listCalls++
		return r.output, nil
	}
	return "", nil
}

func TestWorktreeServiceAdapterDeleteDoesNotRequireProjectWideRuntimeFreshness(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repoDir := t.TempDir()
	worktreePath := filepath.Join(t.TempDir(), "repo-bvx")
	runner := &staticWorktreeListRunner{
		output: `worktree ` + repoDir + `
HEAD abc123
branch refs/heads/main

worktree ` + worktreePath + `
HEAD def456
branch refs/heads/riordan/bvx/worktree-delete
`,
	}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	adapter := &worktreeServiceAdapter{
		manager: manager,
		logger:  logger,
		ensureRuntimeFreshForMutation: func(context.Context, string, string) error {
			return errors.New("project-wide runtime freshness should not block targeted worktree deletion")
		},
	}

	if err := adapter.Delete(context.Background(), "proj", "bvx", false); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	wantCommands := []string{
		"worktree list --porcelain",
		"worktree list --porcelain",
		"worktree remove " + worktreePath,
		"branch -D riordan/bvx/worktree-delete",
	}
	if strings.Join(runner.commands, "\n") != strings.Join(wantCommands, "\n") {
		t.Fatalf("commands = %v, want %v", runner.commands, wantCommands)
	}
}

func TestWorktreeServiceAdapterCreateUsesIssueScopedRuntimeFreshness(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repoDir := t.TempDir()
	runner := &staticWorktreeListRunner{
		output: `worktree ` + repoDir + `
HEAD abc123
branch refs/heads/main
`,
	}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	var issueFreshCalls []string
	adapter := &worktreeServiceAdapter{
		manager: manager,
		logger:  logger,
		ensureRuntimeFreshForMutation: func(context.Context, string, string) error {
			return errors.New("project-wide runtime freshness should not run for targeted worktree create")
		},
		ensureRuntimeFreshForIssueMutation: func(_ context.Context, projectID, issueID, reason string) error {
			issueFreshCalls = append(issueFreshCalls, projectID+":"+issueID+":"+reason)
			return nil
		},
	}

	wt, err := adapter.Create(context.Background(), "proj", "bvx", "main")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if wt == nil || wt.IssueID != "bvx" {
		t.Fatalf("worktree = %+v, want issue bvx", wt)
	}
	wantFreshCalls := []string{"proj:bvx:" + daemonhandlers.CommandWorktreeCreate}
	if strings.Join(issueFreshCalls, "\n") != strings.Join(wantFreshCalls, "\n") {
		t.Fatalf("issue-scoped freshness calls = %v, want %v", issueFreshCalls, wantFreshCalls)
	}
}

func TestWorktreeServiceAdapterListUsesProjectionOnlyWhenRuntimeStoreAvailable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir, err := os.MkdirTemp("", "azedarach-worktree-adapter-*")
	if err != nil {
		t.Fatalf("create runtime state temp dir: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(dir, "projection.db"), logger)
	t.Cleanup(func() {
		_ = store.Close()
		_ = os.RemoveAll(dir)
	})

	runner := &countingWorktreeListRunner{}
	manager := git.NewWorktreeManager(runner, t.TempDir(), logger)
	adapter := &worktreeServiceAdapter{
		manager:           manager,
		runtimeStateStore: store,
		logger:            logger,
	}

	worktrees, err := adapter.List(context.Background(), "proj")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("worktrees = %v, want empty projection-backed result", worktrees)
	}
	if runner.listCalls != 0 {
		t.Fatalf("live worktree list calls = %d, want 0", runner.listCalls)
	}
}

func TestWorktreeServiceAdapterPollerRefreshesProjection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir, err := os.MkdirTemp("", "azedarach-worktree-poller-*")
	if err != nil {
		t.Fatalf("create runtime state temp dir: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(dir, "projection.db"), logger)

	projectID := "proj"
	if err := store.UpsertWorktreeState(context.Background(), daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   "bux",
		Path:      "/tmp/repo-bux",
		Branch:    "riordan/bux/task",
		UpdatedAt: time.Now().UTC().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("seed stale projection: %v", err)
	}

	runner := &staticWorktreeListRunner{
		output: `worktree /tmp/repo
HEAD abc123
branch refs/heads/main
`,
	}
	manager := git.NewWorktreeManager(runner, "/tmp/repo", logger)
	adapter := &worktreeServiceAdapter{
		manager:           manager,
		runtimeStateStore: store,
		logger:            logger,
		pollInterval:      20 * time.Millisecond,
	}
	t.Cleanup(func() {
		adapter.mu.Lock()
		for _, cancel := range adapter.pollers {
			cancel()
		}
		adapter.mu.Unlock()
		_ = store.Close()
		_ = os.RemoveAll(dir)
	})

	adapter.ensureBackgroundPoller(projectID)

	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		worktrees, err := store.ListWorktreeStates(context.Background(), projectID)
		if err != nil {
			t.Fatalf("ListWorktreeStates returned error: %v", err)
		}
		if len(worktrees) == 0 {
			if runner.listCalls == 0 {
				t.Fatal("expected live worktree list calls while poller reconciles projection")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for poller to reconcile stale worktree projection")
}
