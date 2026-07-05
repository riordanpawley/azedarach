package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
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

func TestIssueSpecServicePackBuildsStageAwareSourcePack(t *testing.T) {
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
		LocalID: "REQ-PACK",
		Title:   "pack requirement",
	})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	_, err = client.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-PACK",
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	service := newTestIssueSpecService(client, repoDir)
	out, err := service.Pack(ctx, protocol.SpecPackRequestBody{
		IssueID: naming.IssueID(issueID),
		Stage:   protocol.SpecPackStageRepair,
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if out.Stage != protocol.SpecPackStageRepair {
		t.Fatalf("stage = %q, want repair", out.Stage)
	}
	if len(out.Requirements) != 1 || out.Requirements[0].ID != "REQ-PACK" {
		t.Fatalf("requirements = %+v", out.Requirements)
	}
	if len(out.Links) != 1 {
		t.Fatalf("links = %+v", out.Links)
	}
	if len(out.Guidance) == 0 || !strings.Contains(out.Guidance[0], "Compare current source behavior") {
		t.Fatalf("guidance = %+v", out.Guidance)
	}
	if len(out.Gates) == 0 || out.Gates[0] != "az spec lint" {
		t.Fatalf("gates = %+v", out.Gates)
	}
	if len(out.Sharding.Missing) != 1 || out.Sharding.Missing[0] != "REQ-PACK" {
		t.Fatalf("sharding missing = %+v", out.Sharding.Missing)
	}
}

func TestIssueSpecServicePackLoadsShardingSidecar(t *testing.T) {
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
		LocalID: "REQ-SHARDED",
		Title:   "pack requirement",
	})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	_, err = client.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-SHARDED",
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	sidecarDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatalf("mkdir sidecar dir: %v", err)
	}
	content := `{"REQ-SHARDED":{"domain":"spec","slice":"slice-a","tier":"0","priority":"P1","depends_on":["slice-root"],"test_pack":"pack-core"}}`
	if err := os.WriteFile(filepath.Join(sidecarDir, "spec-shards.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	service := newTestIssueSpecService(client, repoDir)
	out, err := service.Pack(ctx, protocol.SpecPackRequestBody{
		IssueID: naming.IssueID(issueID),
		Stage:   protocol.SpecPackStageBrownfield,
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if out.Sharding.SourcePath != ".azedarach/spec-shards.json" {
		t.Fatalf("sharding source path = %q", out.Sharding.SourcePath)
	}
	entry, ok := out.Sharding.ByRequirement["REQ-SHARDED"]
	if !ok || entry.Slice != "slice-a" || entry.TestPack != "pack-core" {
		t.Fatalf("sharding entry = %+v", out.Sharding.ByRequirement)
	}
	if len(out.Sharding.Missing) != 0 {
		t.Fatalf("sharding missing = %+v, want none", out.Sharding.Missing)
	}
}

func TestIssueSpecServicePackLoadsShardingSidecarFromRequestProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	baseClient, baseRepo := newTestIssueClient(t)
	otherClient, otherRepo := newTestIssueClient(t)
	_ = baseClient

	issueID, err := otherClient.Create(ctx, issues.CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	_, err = otherClient.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID: "REQ-PROJECT",
		Title:   "project requirement",
	})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	_, err = otherClient.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-PROJECT",
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{{Name: "other", Path: otherRepo}},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}
	baseSidecar := filepath.Join(baseRepo, ".azedarach", "spec-shards.json")
	if err := os.WriteFile(baseSidecar, []byte(`{"REQ-PROJECT":{"slice":"bootstrap"}}`), 0o644); err != nil {
		t.Fatalf("write base sidecar: %v", err)
	}
	otherSidecar := filepath.Join(otherRepo, ".azedarach", "spec-shards.json")
	if err := os.WriteFile(otherSidecar, []byte(`{"REQ-PROJECT":{"slice":"request-project","test_pack":"pack-project"}}`), 0o644); err != nil {
		t.Fatalf("write other sidecar: %v", err)
	}

	otherProjectID, err := appconfig.ProjectIDForRoot(otherRepo)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(other): %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: baseRepo},
		issueClientsByProject: map[string]*issues.Client{
			protocol.NormalizeProjectID(otherProjectID): otherClient,
		},
		issueClientsByRoot: map[string]*issues.Client{
			daemonStoreRootKey(otherRepo): otherClient,
		},
	}
	service := issueSpecService{daemon: d}
	out, err := service.Pack(withDaemonProjectIDContext(ctx, "other"), protocol.SpecPackRequestBody{
		IssueID: naming.IssueID(issueID),
		Stage:   protocol.SpecPackStageBrownfield,
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	entry, ok := out.Sharding.ByRequirement["REQ-PROJECT"]
	if !ok {
		t.Fatalf("missing project sharding entry: %+v", out.Sharding)
	}
	if entry.Slice != "request-project" || entry.TestPack != "pack-project" {
		t.Fatalf("sharding entry = %+v, want request project sidecar", entry)
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
	branchErr error
}

func (r *staticWorktreeListRunner) Run(_ context.Context, args ...string) (string, error) {
	r.commands = append(r.commands, strings.Join(args, " "))
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		r.listCalls++
		return r.output, nil
	}
	if len(args) >= 2 && args[0] == "branch" && args[1] == "-D" && r.branchErr != nil {
		return "", r.branchErr
	}
	return "", nil
}

type sequenceWorktreeListRunner struct {
	outputs  []string
	commands []string
}

func (r *sequenceWorktreeListRunner) Run(_ context.Context, args ...string) (string, error) {
	r.commands = append(r.commands, strings.Join(args, " "))
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		if len(r.outputs) == 0 {
			return "", nil
		}
		output := r.outputs[0]
		r.outputs = r.outputs[1:]
		return output, nil
	}
	return "", nil
}

type recordingWorktreeCreateRunner struct {
	repoDir    string
	eventsFile string
	worktree   map[string]git.Worktree
	adds       []recordedWorktreeAdd
}

type recordedWorktreeAdd struct {
	IssueID string
	Branch  string
	Base    string
	Path    string
}

func (r *recordingWorktreeCreateRunner) Run(_ context.Context, args ...string) (string, error) {
	if r.worktree == nil {
		r.worktree = map[string]git.Worktree{}
	}
	if len(args) >= 2 && args[0] == "config" && args[1] == "user.name" {
		return "testuser\n", nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		var b strings.Builder
		b.WriteString("worktree " + r.repoDir + "\nHEAD abc123\nbranch refs/heads/main\n\n")
		ids := make([]string, 0, len(r.worktree))
		for id := range r.worktree {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			wt := r.worktree[id]
			b.WriteString("worktree " + wt.Path + "\nHEAD def456\nbranch refs/heads/" + wt.Branch + "\n\n")
		}
		return b.String(), nil
	}
	if len(args) >= 6 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
		branch := args[3]
		path := args[4]
		base := args[5]
		issueID := strings.TrimPrefix(filepath.Base(path), filepath.Base(r.repoDir)+"-")
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", err
		}
		if strings.TrimSpace(r.eventsFile) != "" {
			f, err := os.OpenFile(r.eventsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return "", err
			}
			if _, err := fmt.Fprintf(f, "add:%s:base=%s\n", issueID, base); err != nil {
				_ = f.Close()
				return "", err
			}
			if err := f.Close(); err != nil {
				return "", err
			}
		}
		r.worktree[issueID] = git.Worktree{IssueID: issueID, Path: path, Branch: branch}
		r.adds = append(r.adds, recordedWorktreeAdd{
			IssueID: issueID,
			Branch:  branch,
			Base:    base,
			Path:    path,
		})
		return "", nil
	}
	return "", nil
}

func TestWorktreeServiceAdapterCreateMaterializesMissingAncestorChain(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repoDir := filepath.Join(t.TempDir(), "repo")
	runner := &recordingWorktreeCreateRunner{repoDir: repoDir}
	manager := git.NewWorktreeManager(runner, repoDir, logger)

	rootID := "az-root"
	parentID := "az-parent"
	childID := "az-child"
	tasks := map[string]domain.Task{
		rootID: {
			ID:     naming.IssueID(rootID),
			Title:  "Root issue",
			Type:   domain.TypeTask,
			Status: domain.StatusOpen,
		},
		parentID: {
			ID:       naming.IssueID(parentID),
			Title:    "Parent issue",
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: ptrIssueID(rootID),
		},
		childID: {
			ID:       naming.IssueID(childID),
			Title:    "Child issue",
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: ptrIssueID(parentID),
		},
	}
	initEvents := []string{}
	adapter := &worktreeServiceAdapter{
		manager: manager,
		logger:  logger,
		runtimeIssueTasks: func(context.Context, string, []string) map[string]domain.Task {
			return tasks
		},
		runWorktreeSyncInit: func(_ context.Context, initCtx worktreeInitContext) error {
			initEvents = append(initEvents, fmt.Sprintf("sync:%s:adds=%d:parent=%s", initCtx.IssueID, len(runner.adds), initCtx.ParentIssueID))
			return nil
		},
		startWorktreeAsyncInit: func(initCtx worktreeInitContext) {
			initEvents = append(initEvents, fmt.Sprintf("async:%s:adds=%d", initCtx.IssueID, len(runner.adds)))
		},
	}

	worktree, effectiveBase, err := adapter.Create(ctx, "proj", childID, "main")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if worktree.IssueID != childID {
		t.Fatalf("worktree issue = %q, want %q", worktree.IssueID, childID)
	}
	if len(runner.adds) != 3 {
		t.Fatalf("adds = %+v, want root, parent, child", runner.adds)
	}
	if runner.adds[0].IssueID != rootID || runner.adds[0].Base != "main" {
		t.Fatalf("root add = %+v, want base main", runner.adds[0])
	}
	if runner.adds[1].IssueID != parentID || runner.adds[1].Base != runner.adds[0].Branch {
		t.Fatalf("parent add = %+v, want base %q", runner.adds[1], runner.adds[0].Branch)
	}
	if runner.adds[2].IssueID != childID || runner.adds[2].Base != runner.adds[1].Branch {
		t.Fatalf("child add = %+v, want base %q", runner.adds[2], runner.adds[1].Branch)
	}
	if effectiveBase != runner.adds[1].Branch {
		t.Fatalf("effective base = %q, want parent branch %q", effectiveBase, runner.adds[1].Branch)
	}
	wantInitEvents := []string{
		"sync:" + rootID + ":adds=1:parent=",
		"async:" + rootID + ":adds=1",
		"sync:" + parentID + ":adds=2:parent=" + rootID,
		"async:" + parentID + ":adds=2",
		"sync:" + childID + ":adds=3:parent=" + parentID,
		"async:" + childID + ":adds=3",
	}
	if strings.Join(initEvents, "\n") != strings.Join(wantInitEvents, "\n") {
		t.Fatalf("init events = %#v, want %#v", initEvents, wantInitEvents)
	}
}

func ptrIssueID(id string) *naming.IssueID {
	issueID := naming.IssueID(id)
	return &issueID
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
		"worktree remove " + worktreePath,
		"branch -D riordan/bvx/worktree-delete",
	}
	if strings.Join(runner.commands, "\n") != strings.Join(wantCommands, "\n") {
		t.Fatalf("commands = %v, want %v", runner.commands, wantCommands)
	}
}

func TestWorktreeServiceAdapterDeleteReturnsBranchCleanupError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repoDir := t.TempDir()
	worktreePath := filepath.Join(t.TempDir(), "repo-bvx")
	branchName := "riordan/bvx/worktree-delete"
	runner := &staticWorktreeListRunner{
		output: `worktree ` + repoDir + `
HEAD abc123
branch refs/heads/main

worktree ` + worktreePath + `
HEAD def456
branch refs/heads/` + branchName + `
`,
		branchErr: errors.New("cannot lock ref"),
	}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	adapter := &worktreeServiceAdapter{
		manager: manager,
		logger:  logger,
	}

	err := adapter.Delete(context.Background(), "proj", "bvx", false)

	if err == nil || !strings.Contains(err.Error(), "failed to delete branch "+branchName) {
		t.Fatalf("Delete error = %v, want branch cleanup failure", err)
	}
	wantCommands := []string{
		"worktree list --porcelain",
		"worktree remove " + worktreePath,
		"branch -D " + branchName,
	}
	if strings.Join(runner.commands, "\n") != strings.Join(wantCommands, "\n") {
		t.Fatalf("commands = %v, want %v", runner.commands, wantCommands)
	}
}

func TestWorktreeServiceAdapterDeleteCleansProjectedBranchWhenWorktreeAlreadyGone(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectID := "proj"
	issueID := "bvx"
	repoDir := t.TempDir()
	branchName := "riordan/bvx/worktree-delete"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      filepath.Join(t.TempDir(), "repo-bvx"),
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	runner := &staticWorktreeListRunner{
		output: `worktree ` + repoDir + `
HEAD abc123
branch refs/heads/main
`,
	}
	writer := &recordingRuntimeProjectionWriter{}
	adapter := &worktreeServiceAdapter{
		manager:                 git.NewWorktreeManager(runner, repoDir, logger),
		runtimeStateStore:       store,
		runtimeProjectionWriter: writer,
		logger:                  logger,
	}

	if err := adapter.Delete(ctx, projectID, issueID, false); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	wantCommands := []string{
		"worktree list --porcelain",
		"branch -D " + branchName,
	}
	if strings.Join(runner.commands, "\n") != strings.Join(wantCommands, "\n") {
		t.Fatalf("commands = %v, want %v", runner.commands, wantCommands)
	}
	if strings.Join(writer.snapshot(), "\n") != "worktree.delete+publish" {
		t.Fatalf("projection writer calls = %v, want worktree.delete+publish", writer.snapshot())
	}
}

func TestWorktreeServiceAdapterDeleteCleansProjectedBranchWhenWorktreeDisappearsDuringDelete(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectID := "proj"
	issueID := "bvx"
	repoDir := t.TempDir()
	worktreePath := filepath.Join(t.TempDir(), "repo-bvx")
	branchName := "riordan/bvx/worktree-delete"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	runner := &sequenceWorktreeListRunner{outputs: []string{
		`worktree ` + repoDir + `
HEAD abc123
branch refs/heads/main

worktree ` + worktreePath + `
HEAD def456
branch refs/heads/` + branchName + `
`,
		`worktree ` + repoDir + `
HEAD abc123
branch refs/heads/main
`,
	}}
	writer := &recordingRuntimeProjectionWriter{}
	adapter := &worktreeServiceAdapter{
		manager:                 git.NewWorktreeManager(runner, repoDir, logger),
		runtimeStateStore:       store,
		runtimeProjectionWriter: writer,
		logger:                  logger,
	}

	if err := adapter.Delete(ctx, projectID, issueID, false); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	wantCommands := []string{
		"worktree list --porcelain",
		"worktree remove " + worktreePath,
		"branch -D " + branchName,
	}
	if strings.Join(runner.commands, "\n") != strings.Join(wantCommands, "\n") {
		t.Fatalf("commands = %v, want %v", runner.commands, wantCommands)
	}
	if strings.Join(writer.snapshot(), "\n") != "worktree.delete+publish" {
		t.Fatalf("projection writer calls = %v, want worktree.delete+publish", writer.snapshot())
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

	wt, _, err := adapter.Create(context.Background(), "proj", "bvx", "main")
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

func TestWorktreeServiceAdapterCreateReusesExistingWorktree(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repoDir := t.TempDir()
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-bvx")
	runner := &staticWorktreeListRunner{
		output: `worktree ` + repoDir + `
HEAD abc123
branch refs/heads/main

worktree ` + worktreePath + `
HEAD def456
branch refs/heads/riordan/bvx/work
`,
	}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	adapter := &worktreeServiceAdapter{
		manager: manager,
		logger:  logger,
	}

	wt, _, err := adapter.Create(context.Background(), "proj", "bvx", "main")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if wt == nil || wt.Path != worktreePath || wt.Branch != "riordan/bvx/work" || wt.IssueID != "bvx" {
		t.Fatalf("worktree = %+v, want existing worktree", wt)
	}
	for _, command := range runner.commands {
		if strings.HasPrefix(command, "worktree add ") {
			t.Fatalf("existing worktree should be reused without add command, commands=%v", runner.commands)
		}
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

func TestWorktreeServiceAdapterSnapshotSkipsClosedIssueWorktrees(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir, err := os.MkdirTemp("", "azedarach-worktree-closed-*")
	if err != nil {
		t.Fatalf("create runtime state temp dir: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(dir, "projection.db"), logger)
	t.Cleanup(func() {
		_ = store.Close()
		_ = os.RemoveAll(dir)
	})

	projectID := "proj"
	closedID := naming.IssueID("az-closed")
	adapter := &worktreeServiceAdapter{
		runtimeStateStore: store,
		logger:            logger,
		runtimeIssueTasks: func(context.Context, string, []string) map[string]domain.Task {
			return map[string]domain.Task{
				closedID.String(): {
					ID:     closedID,
					Status: domain.StatusDone,
					Type:   domain.TypeTask,
				},
			}
		},
	}

	taskByIssue := adapter.runtimeIssueTaskSnapshot(context.Background(), projectID, []string{closedID.String()})
	adapter.writeWorktreeProjectionSnapshot(context.Background(), projectID, []git.Worktree{
		{IssueID: closedID.String(), Path: "/tmp/repo-az-closed", Branch: "az/az-closed"},
	}, taskByIssue)

	worktrees, err := store.ListWorktreeStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListWorktreeStates returned error: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("worktrees = %+v, want none for closed issue", worktrees)
	}
}
