package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type fakeGitService struct {
	fetchFn             func(context.Context, string, string, string) error
	pullBaseFn          func(context.Context, string, string, string, string) error
	mergeFn             func(context.Context, string, string, string) (*git.MergeResult, error)
	checkoutFn          func(context.Context, string, string, string) error
	abortMergeFn        func(context.Context, string, string) error
	diffStatFn          func(context.Context, string, string, string) (string, error)
	statusFn            func(context.Context, string, string) (*git.GitStatus, error)
	refreshStatusFn     func(context.Context, string, string) (*git.GitStatus, error)
	hookRefreshStatusFn func(context.Context, string, string) (*git.GitStatus, error)
	runtimeSignalsFn    func(context.Context, string, []GitRuntimeSignalsTarget, string, bool, string, bool) ([]GitRuntimeSignalsResult, int, error)
	worktreeForBranchFn func(context.Context, string, string) (string, bool, error)
	preflightFn         func(context.Context, string, GitMergePreflightRequest) (*GitMergePreflightResult, error)
	discardFn           func(context.Context, string, string) (*GitDiscardChangesResult, error)
	checkpointFn        func(context.Context, string, GitCheckpointRequest) (*GitCheckpointResult, error)
}

type recordingGitLongRunningExecutor struct {
	commands []string
}

func (r *recordingGitLongRunningExecutor) Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) protocol.ResponseEnvelope) protocol.ResponseEnvelope {
	r.commands = append(r.commands, command)
	return exec(ctx)
}

func (f *fakeGitService) Fetch(ctx context.Context, projectID, worktree, remote string) error {
	if f.fetchFn != nil {
		return f.fetchFn(ctx, projectID, worktree, remote)
	}
	return nil
}

func (f *fakeGitService) PullBase(ctx context.Context, projectID, worktree, remote, baseBranch string) error {
	if f.pullBaseFn != nil {
		return f.pullBaseFn(ctx, projectID, worktree, remote, baseBranch)
	}
	return nil
}

func (f *fakeGitService) Merge(ctx context.Context, projectID, worktree, branch string) (*git.MergeResult, error) {
	if f.mergeFn != nil {
		return f.mergeFn(ctx, projectID, worktree, branch)
	}
	return &git.MergeResult{Success: true}, nil
}

func (f *fakeGitService) Checkout(ctx context.Context, projectID, worktree, branch string) error {
	if f.checkoutFn != nil {
		return f.checkoutFn(ctx, projectID, worktree, branch)
	}
	return nil
}

func (f *fakeGitService) AbortMerge(ctx context.Context, projectID, worktree string) error {
	if f.abortMergeFn != nil {
		return f.abortMergeFn(ctx, projectID, worktree)
	}
	return nil
}

func (f *fakeGitService) DiffStat(ctx context.Context, projectID, worktree, baseBranch string) (string, error) {
	if f.diffStatFn != nil {
		return f.diffStatFn(ctx, projectID, worktree, baseBranch)
	}
	return "", nil
}

func (f *fakeGitService) Status(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	if f.statusFn != nil {
		return f.statusFn(ctx, projectID, worktree)
	}
	return &git.GitStatus{}, nil
}

func (f *fakeGitService) RefreshStatus(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	if f.refreshStatusFn != nil {
		return f.refreshStatusFn(ctx, projectID, worktree)
	}
	return &git.GitStatus{}, nil
}

func (f *fakeGitService) RefreshStatusForHook(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	if f.hookRefreshStatusFn != nil {
		return f.hookRefreshStatusFn(ctx, projectID, worktree)
	}
	return &git.GitStatus{}, nil
}

func (f *fakeGitService) RuntimeSignals(ctx context.Context, projectID string, targets []GitRuntimeSignalsTarget, baseBranch string, compareRemote bool, remote string, refresh bool) ([]GitRuntimeSignalsResult, int, error) {
	if f.runtimeSignalsFn != nil {
		return f.runtimeSignalsFn(ctx, projectID, targets, baseBranch, compareRemote, remote, refresh)
	}
	return nil, 0, nil
}

func (f *fakeGitService) WorktreePathForBranch(ctx context.Context, projectID, branch string) (string, bool, error) {
	if f.worktreeForBranchFn != nil {
		return f.worktreeForBranchFn(ctx, projectID, branch)
	}
	return "", false, nil
}

func (f *fakeGitService) MergePreflight(ctx context.Context, projectID string, req GitMergePreflightRequest) (*GitMergePreflightResult, error) {
	if f.preflightFn != nil {
		return f.preflightFn(ctx, projectID, req)
	}
	return &GitMergePreflightResult{
		SourceWorktree: req.SourceWorktree,
		TargetWorktree: req.TargetWorktree,
		Clean:          true,
	}, nil
}

func (f *fakeGitService) DiscardChanges(ctx context.Context, projectID, worktree string) (*GitDiscardChangesResult, error) {
	if f.discardFn != nil {
		return f.discardFn(ctx, projectID, worktree)
	}
	return &GitDiscardChangesResult{Worktree: worktree}, nil
}

func (f *fakeGitService) Checkpoint(ctx context.Context, projectID string, req GitCheckpointRequest) (*GitCheckpointResult, error) {
	if f.checkpointFn != nil {
		return f.checkpointFn(ctx, projectID, req)
	}
	return &GitCheckpointResult{Worktree: req.Worktree, Message: req.Message}, nil
}

func TestGitHandlerPullBaseDefaultsRemoteAndUsesLongRunningExecutor(t *testing.T) {
	var gotProject, gotWorktree, gotRemote, gotBase string
	executor := &recordingGitLongRunningExecutor{}
	handler := NewGitHandler(&fakeGitService{
		pullBaseFn: func(_ context.Context, projectID, worktree, remote, baseBranch string) error {
			gotProject = projectID
			gotWorktree = worktree
			gotRemote = remote
			gotBase = baseBranch
			return nil
		},
	}, WithGitLongRunningExecutor(executor))

	req := gitRequest(t, CommandGitPullBase, map[string]string{
		"project_id":  "proj-1",
		"worktree":    "/repo/root",
		"base_branch": "main",
	})
	resp := handler.Handle(context.Background(), req)
	if !resp.OK || resp.Error != nil {
		t.Fatalf("pull base response OK = %v error = %+v", resp.OK, resp.Error)
	}
	if gotProject != "proj-1" || gotWorktree != "/repo/root" || gotRemote != "origin" || gotBase != "main" {
		t.Fatalf("pull base args = project:%q worktree:%q remote:%q base:%q", gotProject, gotWorktree, gotRemote, gotBase)
	}
	if len(executor.commands) != 1 || executor.commands[0] != CommandGitPullBase {
		t.Fatalf("long-running commands = %v, want %q", executor.commands, CommandGitPullBase)
	}

	var body gitActionResultBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Worktree != "/repo/root" || body.Remote != "origin" || body.Branch != "main" {
		t.Fatalf("response body = %+v", body)
	}
}

func gitRequest(t *testing.T, command string, body any) protocol.RequestEnvelope {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-" + command),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         command,
		Body:            payload,
	}
}

func TestGitHandlerRoutesCommands(t *testing.T) {
	handler := NewGitHandler(&fakeGitService{
		fetchFn: func(_ context.Context, _ string, worktree, remote string) error {
			if worktree != "/tmp/az-1" || remote != "origin" {
				t.Fatalf("fetch args = %q %q", worktree, remote)
			}
			return nil
		},
		mergeFn: func(_ context.Context, _ string, worktree, branch string) (*git.MergeResult, error) {
			if worktree != "/tmp/az-1" || branch != "main" {
				t.Fatalf("merge args = %q %q", worktree, branch)
			}
			return &git.MergeResult{
				Success:       true,
				HasConflicts:  true,
				ConflictFiles: []string{"README.md"},
				Message:       "merge conflicted",
			}, nil
		},
		checkoutFn: func(_ context.Context, _ string, worktree, branch string) error {
			if worktree != "/tmp/az-1" || branch != "feature/one" {
				t.Fatalf("checkout args = %q %q", worktree, branch)
			}
			return nil
		},
		abortMergeFn: func(_ context.Context, _ string, worktree string) error {
			if worktree != "/tmp/az-1" {
				t.Fatalf("abort merge args = %q", worktree)
			}
			return nil
		},
		diffStatFn: func(_ context.Context, _ string, worktree, baseBranch string) (string, error) {
			if worktree != "/tmp/az-1" {
				t.Fatalf("diff stat args = %q", worktree)
			}
			if baseBranch != "" {
				t.Fatalf("diff stat base branch = %q, want empty", baseBranch)
			}
			return " README.md | 2 ++\n 1 file changed, 2 insertions(+)", nil
		},
		statusFn: func(_ context.Context, _ string, worktree string) (*git.GitStatus, error) {
			if worktree != "/tmp/az-1" {
				t.Fatalf("status args = %q", worktree)
			}
			return &git.GitStatus{HasChanges: true, Modified: []string{"README.md"}}, nil
		},
		worktreeForBranchFn: func(_ context.Context, _ string, branch string) (string, bool, error) {
			if branch != "main" {
				t.Fatalf("worktree for branch args = %q", branch)
			}
			return "/tmp/main", true, nil
		},
		preflightFn: func(_ context.Context, _ string, req GitMergePreflightRequest) (*GitMergePreflightResult, error) {
			if req.SourceWorktree != "/tmp/az-1" || req.TargetWorktree != "/tmp/main" {
				t.Fatalf("preflight worktrees = %+v", req)
			}
			if req.TargetRef != "main" || req.SourceBranch != "az/az-1" {
				t.Fatalf("preflight refs = %+v", req)
			}
			return &GitMergePreflightResult{
				SourceID:       req.SourceID,
				SourceWorktree: req.SourceWorktree,
				TargetID:       req.TargetID,
				TargetWorktree: req.TargetWorktree,
				Clean:          true,
			}, nil
		},
		discardFn: func(_ context.Context, _ string, worktree string) (*GitDiscardChangesResult, error) {
			if worktree != "/tmp/az-1" {
				t.Fatalf("discard args = %q", worktree)
			}
			return &GitDiscardChangesResult{Worktree: worktree}, nil
		},
		checkpointFn: func(_ context.Context, _ string, req GitCheckpointRequest) (*GitCheckpointResult, error) {
			if req.Worktree != "/tmp/az-1" || req.Message != "chore: pre-merge checkpoint" {
				t.Fatalf("checkpoint args = %+v", req)
			}
			return &GitCheckpointResult{Worktree: req.Worktree, Message: req.Message}, nil
		},
	})

	for _, tc := range []struct {
		name    string
		req     protocol.RequestEnvelope
		wantCmd string
	}{
		{
			name:    "fetch",
			req:     gitRequest(t, CommandGitFetch, gitCommandBody{Worktree: "/tmp/az-1", Remote: "origin"}),
			wantCmd: CommandGitFetch,
		},
		{
			name:    "merge",
			req:     gitRequest(t, CommandGitMerge, gitCommandBody{Worktree: "/tmp/az-1", Branch: "main"}),
			wantCmd: CommandGitMerge,
		},
		{
			name:    "checkout",
			req:     gitRequest(t, CommandGitCheckout, gitCommandBody{Worktree: "/tmp/az-1", Branch: "feature/one"}),
			wantCmd: CommandGitCheckout,
		},
		{
			name:    "abort merge",
			req:     gitRequest(t, CommandGitAbortMerge, gitCommandBody{Worktree: "/tmp/az-1"}),
			wantCmd: CommandGitAbortMerge,
		},
		{
			name:    "diff stat",
			req:     gitRequest(t, CommandGitDiffStat, gitCommandBody{Worktree: "/tmp/az-1"}),
			wantCmd: CommandGitDiffStat,
		},
		{
			name:    "status",
			req:     gitRequest(t, CommandGitStatus, gitCommandBody{Worktree: "/tmp/az-1"}),
			wantCmd: CommandGitStatus,
		},
		{
			name: "merge preflight",
			req: gitRequest(t, CommandGitMergePreflight, GitMergePreflightRequest{
				SourceID:       "az-1",
				SourceWorktree: "/tmp/az-1",
				TargetID:       "main",
				TargetWorktree: "/tmp/main",
				TargetRef:      "main",
				SourceBranch:   "az/az-1",
			}),
			wantCmd: CommandGitMergePreflight,
		},
		{
			name:    "worktree for branch",
			req:     gitRequest(t, CommandGitWorktreeForBranch, gitCommandBody{Branch: "main"}),
			wantCmd: CommandGitWorktreeForBranch,
		},
		{
			name:    "discard changes",
			req:     gitRequest(t, CommandGitDiscardChanges, GitDiscardChangesRequest{Worktree: "/tmp/az-1"}),
			wantCmd: CommandGitDiscardChanges,
		},
		{
			name:    "checkpoint",
			req:     gitRequest(t, CommandGitCheckpoint, GitCheckpointRequest{Worktree: "/tmp/az-1", Message: "chore: pre-merge checkpoint"}),
			wantCmd: CommandGitCheckpoint,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := handler.Handle(context.Background(), tc.req)
			if !resp.OK {
				t.Fatalf("expected OK response, got %+v", resp.Error)
			}
			switch tc.wantCmd {
			case CommandGitFetch:
				var body gitActionResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Remote != "origin" {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitMerge:
				var body gitMergeResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Branch != "main" {
					t.Fatalf("response body = %+v", body)
				}
				if !body.Result.HasConflicts || len(body.Result.ConflictFiles) != 1 {
					t.Fatalf("merge result = %+v", body.Result)
				}
			case CommandGitCheckout:
				var body gitActionResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Branch != "feature/one" {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitAbortMerge:
				var body gitActionResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitDiffStat:
				var body gitOutputResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Output == "" {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitStatus:
				var body gitStatusResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || !body.Status.HasChanges {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitMergePreflight:
				var body GitMergePreflightResult
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.SourceWorktree != "/tmp/az-1" || body.TargetWorktree != "/tmp/main" || !body.Clean {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitWorktreeForBranch:
				var body gitWorktreeForBranchResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Branch != "main" || body.Worktree != "/tmp/main" || !body.Found {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitDiscardChanges:
				var body GitDiscardChangesResult
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" {
					t.Fatalf("response body = %+v", body)
				}
			case CommandGitCheckpoint:
				var body GitCheckpointResult
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Message != "chore: pre-merge checkpoint" {
					t.Fatalf("response body = %+v", body)
				}
			}
		})
	}
}

func TestGitHandlerRuntimeSignalsPassesRefreshFlag(t *testing.T) {
	handler := NewGitHandler(&fakeGitService{
		runtimeSignalsFn: func(_ context.Context, projectID string, targets []GitRuntimeSignalsTarget, baseBranch string, compareRemote bool, remote string, refresh bool) ([]GitRuntimeSignalsResult, int, error) {
			if projectID == "" {
				t.Fatal("projectID is empty")
			}
			if len(targets) != 1 || targets[0].IssueID != "az-1" || targets[0].Worktree != "/tmp/az-1" {
				t.Fatalf("targets = %+v", targets)
			}
			if baseBranch != "preview" || !compareRemote || remote != "origin" || !refresh {
				t.Fatalf("runtime signal args base=%q compare=%v remote=%q refresh=%v", baseBranch, compareRemote, remote, refresh)
			}
			return []GitRuntimeSignalsResult{{
				IssueID:      "az-1",
				Worktree:     "/tmp/az-1",
				GitAdditions: 4,
				GitDeletions: 2,
			}}, 0, nil
		},
	})

	resp := handler.Handle(context.Background(), gitRequest(t, CommandGitRuntimeSignals, gitCommandBody{
		BaseBranch:    "preview",
		CompareRemote: true,
		Remote:        "origin",
		Refresh:       true,
		Targets:       []GitRuntimeSignalsTarget{{IssueID: "az-1", Worktree: "/tmp/az-1"}},
	}))
	if !resp.OK {
		t.Fatalf("expected OK response, got %+v", resp.Error)
	}
	var body gitRuntimeSignalsBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Signals) != 1 || body.Signals[0].GitAdditions != 4 || body.Signals[0].GitDeletions != 2 {
		t.Fatalf("signals = %+v, want refreshed metrics", body.Signals)
	}
}

func TestGitHandlerStatusRefreshUsesRefreshService(t *testing.T) {
	var statusCalls int
	var refreshCalls int
	handler := NewGitHandler(&fakeGitService{
		statusFn: func(context.Context, string, string) (*git.GitStatus, error) {
			statusCalls++
			return &git.GitStatus{}, nil
		},
		refreshStatusFn: func(_ context.Context, _ string, worktree string) (*git.GitStatus, error) {
			refreshCalls++
			if worktree != "/tmp/az-1" {
				t.Fatalf("refresh status worktree = %q, want /tmp/az-1", worktree)
			}
			return &git.GitStatus{HasChanges: true, Modified: []string{"README.md"}}, nil
		},
	})

	resp := handler.Handle(context.Background(), gitRequest(t, CommandGitStatus, gitCommandBody{
		Worktree: "/tmp/az-1",
		Refresh:  true,
	}))
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}
	if statusCalls != 0 {
		t.Fatalf("status calls = %d, want 0 for refresh path", statusCalls)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	var body gitStatusResultBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if !body.Status.HasChanges {
		t.Fatalf("status = %+v, want refreshed dirty status", body.Status)
	}
}

func TestGitHandlerHookStatusRefreshUsesHookRefreshService(t *testing.T) {
	var manualRefreshCalls int
	var hookRefreshCalls int
	handler := NewGitHandler(&fakeGitService{
		refreshStatusFn: func(context.Context, string, string) (*git.GitStatus, error) {
			manualRefreshCalls++
			return &git.GitStatus{}, nil
		},
		hookRefreshStatusFn: func(_ context.Context, _ string, worktree string) (*git.GitStatus, error) {
			hookRefreshCalls++
			if worktree != "/tmp/az-1" {
				t.Fatalf("hook refresh worktree = %q, want /tmp/az-1", worktree)
			}
			return &git.GitStatus{HasChanges: true, Modified: []string{"README.md"}}, nil
		},
	})

	resp := handler.Handle(context.Background(), gitRequest(t, CommandGitStatus, gitCommandBody{
		Worktree:      "/tmp/az-1",
		Refresh:       true,
		HookTriggered: true,
	}))
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}
	if manualRefreshCalls != 0 {
		t.Fatalf("manual refresh calls = %d, want 0 for hook path", manualRefreshCalls)
	}
	if hookRefreshCalls != 1 {
		t.Fatalf("hook refresh calls = %d, want 1", hookRefreshCalls)
	}
}

func TestGitHandlerValidationAndErrorMapping(t *testing.T) {
	handler := NewGitHandler(&fakeGitService{
		fetchFn: func(context.Context, string, string, string) error {
			return context.DeadlineExceeded
		},
		mergeFn: func(context.Context, string, string, string) (*git.MergeResult, error) {
			return nil, errors.New("merge failed")
		},
		checkoutFn: func(context.Context, string, string, string) error {
			return nil
		},
		abortMergeFn: func(context.Context, string, string) error {
			return context.DeadlineExceeded
		},
		diffStatFn: func(context.Context, string, string, string) (string, error) {
			return "", context.DeadlineExceeded
		},
		statusFn: func(context.Context, string, string) (*git.GitStatus, error) {
			return nil, context.DeadlineExceeded
		},
		preflightFn: func(context.Context, string, GitMergePreflightRequest) (*GitMergePreflightResult, error) {
			return nil, errors.New("preflight failed")
		},
		discardFn: func(context.Context, string, string) (*GitDiscardChangesResult, error) {
			return nil, context.DeadlineExceeded
		},
		checkpointFn: func(context.Context, string, GitCheckpointRequest) (*GitCheckpointResult, error) {
			return nil, errors.New("checkpoint failed")
		},
	})

	resp := handler.Handle(context.Background(), gitRequest(t, CommandGitFetch, gitCommandBody{Worktree: "/tmp/az-1"}))
	if resp.OK {
		t.Fatal("expected fetch timeout failure with default remote")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeTimeout || !resp.Error.Retryable {
		t.Fatalf("unexpected fetch mapping with default remote: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitFetch, gitCommandBody{}))
	if resp.OK {
		t.Fatal("expected fetch validation failure when worktree is missing")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unexpected fetch validation error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitFetch, gitCommandBody{Worktree: "/tmp/az-1", Remote: "origin"}))
	if resp.OK {
		t.Fatal("expected fetch timeout failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeTimeout || !resp.Error.Retryable {
		t.Fatalf("unexpected timeout mapping: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitMerge, gitCommandBody{Worktree: "/tmp/az-1", Branch: "main"}))
	if resp.OK {
		t.Fatal("expected merge failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInternal {
		t.Fatalf("unexpected merge mapping: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitAbortMerge, gitCommandBody{Worktree: "/tmp/az-1"}))
	if resp.OK {
		t.Fatal("expected abort merge failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeTimeout || !resp.Error.Retryable {
		t.Fatalf("unexpected abort merge timeout mapping: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitDiffStat, gitCommandBody{Worktree: "/tmp/az-1"}))
	if resp.OK {
		t.Fatal("expected diff stat failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeTimeout || !resp.Error.Retryable {
		t.Fatalf("unexpected diff stat timeout mapping: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitDiffStat, gitCommandBody{}))
	if resp.OK {
		t.Fatal("expected diff stat validation failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unexpected diff stat validation error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitStatus, gitCommandBody{Worktree: "/tmp/az-1"}))
	if resp.OK {
		t.Fatal("expected status failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeTimeout || !resp.Error.Retryable {
		t.Fatalf("unexpected status timeout mapping: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitStatus, gitCommandBody{}))
	if resp.OK {
		t.Fatal("expected status validation failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unexpected status validation error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitMergePreflight, GitMergePreflightRequest{}))
	if resp.OK {
		t.Fatal("expected merge preflight validation failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unexpected merge preflight validation error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitMergePreflight, GitMergePreflightRequest{
		SourceWorktree: "/tmp/az-1",
		TargetWorktree: "/tmp/main",
	}))
	if resp.OK {
		t.Fatal("expected merge preflight failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInternal {
		t.Fatalf("unexpected merge preflight mapping: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitDiscardChanges, GitDiscardChangesRequest{}))
	if resp.OK {
		t.Fatal("expected discard validation failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unexpected discard validation error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitDiscardChanges, GitDiscardChangesRequest{Worktree: "/tmp/az-1"}))
	if resp.OK {
		t.Fatal("expected discard timeout failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeTimeout || !resp.Error.Retryable {
		t.Fatalf("unexpected discard mapping: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitCheckpoint, GitCheckpointRequest{}))
	if resp.OK {
		t.Fatal("expected checkpoint validation failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unexpected checkpoint validation error: %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), gitRequest(t, CommandGitCheckpoint, GitCheckpointRequest{Worktree: "/tmp/az-1"}))
	if resp.OK {
		t.Fatal("expected checkpoint failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInternal {
		t.Fatalf("unexpected checkpoint mapping: %+v", resp.Error)
	}
}

func TestGitHandlerUsesLongRunningExecutorForMutatingCommands(t *testing.T) {
	executor := &recordingGitLongRunningExecutor{}
	handler := NewGitHandler(&fakeGitService{}, WithGitLongRunningExecutor(executor))

	resp := handler.Handle(context.Background(), gitRequest(t, CommandGitMerge, gitCommandBody{Worktree: "/tmp/az-1", Branch: "main"}))
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}
	if len(executor.commands) != 1 || executor.commands[0] != CommandGitMerge {
		t.Fatalf("commands = %v, want [%s]", executor.commands, CommandGitMerge)
	}

	_ = handler.Handle(context.Background(), gitRequest(t, CommandGitStatus, gitCommandBody{Worktree: "/tmp/az-1"}))
	if len(executor.commands) != 1 {
		t.Fatalf("status should not use long-running executor, commands = %v", executor.commands)
	}

	_ = handler.Handle(context.Background(), gitRequest(t, CommandGitDiscardChanges, GitDiscardChangesRequest{Worktree: "/tmp/az-1"}))
	_ = handler.Handle(context.Background(), gitRequest(t, CommandGitCheckpoint, GitCheckpointRequest{Worktree: "/tmp/az-1", Message: "checkpoint"}))
	if got, want := len(executor.commands), 3; got != want {
		t.Fatalf("commands length = %d, want %d (%v)", got, want, executor.commands)
	}
	if executor.commands[1] != CommandGitDiscardChanges || executor.commands[2] != CommandGitCheckpoint {
		t.Fatalf("commands = %v", executor.commands)
	}
}
