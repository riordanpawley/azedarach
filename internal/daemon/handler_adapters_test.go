package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
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

func TestWorktreeServiceAdapterListUsesProjectionOnlyWhenRuntimeStoreAvailable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), logger)
	t.Cleanup(func() { _ = store.Close() })

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
