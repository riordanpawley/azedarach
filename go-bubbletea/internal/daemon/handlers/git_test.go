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
	fetchFn    func(context.Context, string, string) error
	mergeFn    func(context.Context, string, string) (*git.MergeResult, error)
	checkoutFn func(context.Context, string, string) error
}

func (f *fakeGitService) Fetch(ctx context.Context, worktree, remote string) error {
	if f.fetchFn != nil {
		return f.fetchFn(ctx, worktree, remote)
	}
	return nil
}

func (f *fakeGitService) Merge(ctx context.Context, worktree, branch string) (*git.MergeResult, error) {
	if f.mergeFn != nil {
		return f.mergeFn(ctx, worktree, branch)
	}
	return &git.MergeResult{Success: true}, nil
}

func (f *fakeGitService) Checkout(ctx context.Context, worktree, branch string) error {
	if f.checkoutFn != nil {
		return f.checkoutFn(ctx, worktree, branch)
	}
	return nil
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
		fetchFn: func(_ context.Context, worktree, remote string) error {
			if worktree != "/tmp/az-1" || remote != "origin" {
				t.Fatalf("fetch args = %q %q", worktree, remote)
			}
			return nil
		},
		mergeFn: func(_ context.Context, worktree, branch string) (*git.MergeResult, error) {
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
		checkoutFn: func(_ context.Context, worktree, branch string) error {
			if worktree != "/tmp/az-1" || branch != "feature/one" {
				t.Fatalf("checkout args = %q %q", worktree, branch)
			}
			return nil
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
			}
		})
	}
}

func TestGitHandlerValidationAndErrorMapping(t *testing.T) {
	handler := NewGitHandler(&fakeGitService{
		fetchFn: func(context.Context, string, string) error {
			return context.DeadlineExceeded
		},
		mergeFn: func(context.Context, string, string) (*git.MergeResult, error) {
			return nil, errors.New("merge failed")
		},
		checkoutFn: func(context.Context, string, string) error {
			return nil
		},
	})

	resp := handler.Handle(context.Background(), gitRequest(t, CommandGitFetch, gitCommandBody{Worktree: "/tmp/az-1"}))
	if resp.OK {
		t.Fatal("expected fetch validation failure")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("unexpected validation error: %+v", resp.Error)
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
}
