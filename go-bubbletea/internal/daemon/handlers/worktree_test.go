package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type mockWorktreeService struct {
	listFn   func(context.Context, string) ([]git.Worktree, error)
	createFn func(context.Context, string, string, string) (*git.Worktree, error)
	deleteFn func(context.Context, string, string) error
}

func (m mockWorktreeService) List(ctx context.Context, projectID string) ([]git.Worktree, error) {
	return m.listFn(ctx, projectID)
}

func (m mockWorktreeService) Create(ctx context.Context, projectID, beadID, baseBranch string) (*git.Worktree, error) {
	return m.createFn(ctx, projectID, beadID, baseBranch)
}

func (m mockWorktreeService) Delete(ctx context.Context, projectID, beadID string) error {
	return m.deleteFn(ctx, projectID, beadID)
}

func TestWorktreeHandlerHappyPath(t *testing.T) {
	h := NewWorktreeHandler(mockWorktreeService{
		listFn: func(context.Context, string) ([]git.Worktree, error) {
			return []git.Worktree{
				{Path: "/tmp/repo-a", Branch: "az/bead-a", BeadID: "bead-a"},
				{Path: "/tmp/repo-b", Branch: "az/bead-b", BeadID: "bead-b"},
			}, nil
		},
		createFn: func(context.Context, string, string, string) (*git.Worktree, error) {
			return &git.Worktree{Path: "/tmp/repo-c", Branch: "az/bead-c", BeadID: "bead-c"}, nil
		},
		deleteFn: func(context.Context, string, string) error {
			return nil
		},
	})

	tests := []struct {
		name    string
		command string
		body    map[string]string
		check   func(*testing.T, protocol.ResponseEnvelope)
	}{
		{
			name:    "list",
			command: CommandWorktreeList,
			body:    map[string]string{"project_id": "proj"},
			check: func(t *testing.T, resp protocol.ResponseEnvelope) {
				t.Helper()
				if !resp.OK {
					t.Fatalf("response = %+v", resp)
				}
				var body worktreeListResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal list body: %v", err)
				}
				if body.ProjectID != "proj" {
					t.Fatalf("project_id = %q, want proj", body.ProjectID)
				}
				if len(body.Worktrees) != 2 {
					t.Fatalf("worktrees len = %d, want 2", len(body.Worktrees))
				}
				if body.Worktrees[0].BeadID != "bead-a" || body.Worktrees[1].BeadID != "bead-b" {
					t.Fatalf("unexpected worktrees: %+v", body.Worktrees)
				}
			},
		},
		{
			name:    "create",
			command: CommandWorktreeCreate,
			body: map[string]string{
				"project_id":  "proj",
				"bead_id":     "bead-c",
				"base_branch": "main",
			},
			check: func(t *testing.T, resp protocol.ResponseEnvelope) {
				t.Helper()
				if !resp.OK {
					t.Fatalf("response = %+v", resp)
				}
				var body worktreeResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal create body: %v", err)
				}
				if body.Worktree.BeadID != "bead-c" || body.Worktree.Path != "/tmp/repo-c" {
					t.Fatalf("unexpected worktree body: %+v", body.Worktree)
				}
			},
		},
		{
			name:    "remove",
			command: CommandWorktreeRemove,
			body: map[string]string{
				"project_id": "proj",
				"bead_id":    "bead-c",
			},
			check: func(t *testing.T, resp protocol.ResponseEnvelope) {
				t.Helper()
				if !resp.OK {
					t.Fatalf("response = %+v", resp)
				}
				var body worktreeRemoveResultBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal remove body: %v", err)
				}
				if body.ProjectID != "proj" || body.BeadID != "bead-c" {
					t.Fatalf("unexpected remove body: %+v", body)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}

			resp := h.Handle(context.Background(), protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-" + tc.name,
				Kind:            protocol.EnvelopeKindCommand,
				Command:         tc.command,
				Body:            body,
			})
			tc.check(t, resp)
		})
	}
}

func TestWorktreeHandlerErrorMapping(t *testing.T) {
	tests := []struct {
		name    string
		command string
		body    map[string]string
		err     error
		code    protocol.ErrorCode
	}{
		{
			name:    "list not found",
			command: CommandWorktreeList,
			body:    map[string]string{"project_id": "proj"},
			err:     ErrWorktreeNotFound,
			code:    protocol.ErrorCodeInvalidRequest,
		},
		{
			name:    "create already exists",
			command: CommandWorktreeCreate,
			body: map[string]string{
				"project_id":  "proj",
				"bead_id":     "bead-c",
				"base_branch": "main",
			},
			err:  ErrWorktreeAlreadyExists,
			code: protocol.ErrorCodeConflict,
		},
		{
			name:    "remove timeout",
			command: CommandWorktreeRemove,
			body: map[string]string{
				"project_id": "proj",
				"bead_id":    "bead-c",
			},
			err:  context.DeadlineExceeded,
			code: protocol.ErrorCodeTimeout,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := mockWorktreeService{
				listFn: func(context.Context, string) ([]git.Worktree, error) {
					return nil, tc.err
				},
				createFn: func(context.Context, string, string, string) (*git.Worktree, error) {
					return nil, tc.err
				},
				deleteFn: func(context.Context, string, string) error {
					return tc.err
				},
			}
			h := NewWorktreeHandler(service)

			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}

			resp := h.Handle(context.Background(), protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-" + tc.name,
				Kind:            protocol.EnvelopeKindCommand,
				Command:         tc.command,
				Body:            body,
			})

			if resp.OK {
				t.Fatalf("expected error response, got %+v", resp)
			}
			if resp.Error == nil {
				t.Fatal("expected error envelope")
			}
			if resp.Error.Code != tc.code {
				t.Fatalf("error code = %q, want %q", resp.Error.Code, tc.code)
			}
		})
	}
}

func TestWorktreeHandlerUnsupportedCommand(t *testing.T) {
	h := NewWorktreeHandler(mockWorktreeService{
		listFn:   func(context.Context, string) ([]git.Worktree, error) { return nil, nil },
		createFn: func(context.Context, string, string, string) (*git.Worktree, error) { return nil, nil },
		deleteFn: func(context.Context, string, string) error { return nil },
	})

	body, err := json.Marshal(map[string]string{"project_id": "proj"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-unsupported",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "worktree.rename",
		Body:            body,
	})

	if resp.OK {
		t.Fatalf("expected unsupported command response, got %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("error envelope = %+v", resp.Error)
	}
}

func TestMapWorktreeError(t *testing.T) {
	if got := mapWorktreeError(errors.New("boom")); got == nil || got.Code != protocol.ErrorCodeInternal {
		t.Fatalf("mapWorktreeError internal = %+v", got)
	}
}

func TestMapCleanupOrphanedError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  protocol.ErrorCode
		retryable bool
	}{
		{
			name:     "invalid request",
			err:      ErrCleanupOrphanedInvalidRequest,
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "not found",
			err:      ErrCleanupOrphanedNotFound,
			wantCode: protocol.ErrorCodeInvalidRequest,
		},
		{
			name:     "conflict",
			err:      ErrCleanupOrphanedConflict,
			wantCode: protocol.ErrorCodeConflict,
		},
		{
			name:      "timeout",
			err:       context.DeadlineExceeded,
			wantCode:  protocol.ErrorCodeTimeout,
			retryable: true,
		},
		{
			name:     "internal",
			err:      errors.New("boom"),
			wantCode: protocol.ErrorCodeInternal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapCleanupOrphanedError(tc.err)
			if got == nil {
				t.Fatal("expected error envelope")
			}
			if got.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", got.Code, tc.wantCode)
			}
			if got.Retryable != tc.retryable {
				t.Fatalf("Retryable = %v, want %v", got.Retryable, tc.retryable)
			}
		})
	}
}
