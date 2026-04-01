package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type fakeGitService struct {
	fetchFn      func(context.Context, string, string, string) error
	mergeFn      func(context.Context, string, string, string) (*git.MergeResult, error)
	checkoutFn   func(context.Context, string, string, string) error
	abortMergeFn func(context.Context, string, string) error
	diffStatFn   func(context.Context, string, string, string) (string, error)
	statusFn     func(context.Context, string, string) (*git.GitStatus, error)
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

func gitRequest(t *testing.T, command string, body any) protocol.RequestEnvelope {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-" + command,
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
			}
		})
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
}
