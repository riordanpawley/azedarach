package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type mockWorktreeService struct {
	listFn            func(context.Context, string) ([]git.Worktree, error)
	createFn          func(context.Context, string, string, string) (*git.Worktree, error)
	deleteFn          func(context.Context, string, string, bool) error
	cleanupOrphanedFn func(context.Context, string) (*CleanupOrphanedResult, error)
}

type recordingWorktreeLongRunningExecutor struct {
	commands []string
}

func (r *recordingWorktreeLongRunningExecutor) Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) protocol.ResponseEnvelope) protocol.ResponseEnvelope {
	r.commands = append(r.commands, command)
	return exec(ctx)
}

func (m mockWorktreeService) List(ctx context.Context, projectID string) ([]git.Worktree, error) {
	return m.listFn(ctx, projectID)
}

func (m mockWorktreeService) Create(ctx context.Context, projectID, issueID, baseBranch string) (*git.Worktree, error) {
	return m.createFn(ctx, projectID, issueID, baseBranch)
}

func (m mockWorktreeService) Delete(ctx context.Context, projectID, issueID string, force bool) error {
	return m.deleteFn(ctx, projectID, issueID, force)
}

func (m mockWorktreeService) CleanupOrphaned(ctx context.Context, projectID string) (*CleanupOrphanedResult, error) {
	return m.cleanupOrphanedFn(ctx, projectID)
}

func TestWorktreeHandlerHappyPath(t *testing.T) {
	h := NewWorktreeHandler(mockWorktreeService{
		listFn: func(context.Context, string) ([]git.Worktree, error) {
			return []git.Worktree{
				{Path: "/tmp/repo-a", Branch: "az/issue-a", IssueID: "issue-a"},
				{Path: "/tmp/repo-b", Branch: "az/issue-b", IssueID: "issue-b"},
			}, nil
		},
		createFn: func(context.Context, string, string, string) (*git.Worktree, error) {
			return &git.Worktree{Path: "/tmp/repo-c", Branch: "az/issue-c", IssueID: "issue-c"}, nil
		},
		deleteFn: func(context.Context, string, string, bool) error {
			return nil
		},
		cleanupOrphanedFn: func(context.Context, string) (*CleanupOrphanedResult, error) {
			return &CleanupOrphanedResult{
				ProjectID: "proj",
				Removed: []git.Worktree{
					{Path: "/tmp/repo-c", Branch: "az/issue-c", IssueID: "issue-c"},
					{Path: "/tmp/repo-a", Branch: "az/issue-a", IssueID: "issue-a"},
				},
				Skipped: []git.Worktree{
					{Path: "/tmp/repo-d", Branch: "az/issue-d", IssueID: "issue-d"},
					{Path: "/tmp/repo-b", Branch: "az/issue-b", IssueID: "issue-b"},
				},
			}, nil
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
				if body.Worktrees[0].IssueID != "issue-a" || body.Worktrees[1].IssueID != "issue-b" {
					t.Fatalf("unexpected worktrees: %+v", body.Worktrees)
				}
			},
		},
		{
			name:    "create",
			command: CommandWorktreeCreate,
			body: map[string]string{
				"project_id":  "proj",
				"issue_id":    "issue-c",
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
				if body.Worktree.IssueID != "issue-c" || body.Worktree.Path != "/tmp/repo-c" {
					t.Fatalf("unexpected worktree body: %+v", body.Worktree)
				}
			},
		},
		{
			name:    "remove",
			command: CommandWorktreeRemove,
			body: map[string]string{
				"project_id": "proj",
				"issue_id":   "issue-c",
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
				if body.ProjectID != "proj" || body.IssueID != "issue-c" {
					t.Fatalf("unexpected remove body: %+v", body)
				}
			},
		},
		{
			name:    "cleanup orphaned",
			command: CommandWorktreeCleanupOrphaned,
			body: map[string]string{
				"project_id": "proj",
			},
			check: func(t *testing.T, resp protocol.ResponseEnvelope) {
				t.Helper()
				if !resp.OK {
					t.Fatalf("response = %+v", resp)
				}
				var body protocol.CleanupOrphanedResponseBody
				if err := json.Unmarshal(resp.Body, &body); err != nil {
					t.Fatalf("unmarshal cleanup body: %v", err)
				}
				if body.ProjectID != "proj" {
					t.Fatalf("project_id = %q, want proj", body.ProjectID)
				}
				if body.WorktreesRemoved != 2 {
					t.Fatalf("worktrees_removed = %d, want 2", body.WorktreesRemoved)
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
				"issue_id":    "issue-c",
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
				"issue_id":   "issue-c",
			},
			err:  context.DeadlineExceeded,
			code: protocol.ErrorCodeTimeout,
		},
		{
			name:    "cleanup invalid request",
			command: CommandWorktreeCleanupOrphaned,
			body:    map[string]string{"project_id": "proj"},
			err:     ErrCleanupOrphanedInvalidRequest,
			code:    protocol.ErrorCodeInvalidRequest,
		},
		{
			name:    "cleanup conflict",
			command: CommandWorktreeCleanupOrphaned,
			body:    map[string]string{"project_id": "proj"},
			err:     ErrCleanupOrphanedConflict,
			code:    protocol.ErrorCodeConflict,
		},
		{
			name:    "cleanup timeout",
			command: CommandWorktreeCleanupOrphaned,
			body:    map[string]string{"project_id": "proj"},
			err:     context.DeadlineExceeded,
			code:    protocol.ErrorCodeTimeout,
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
				deleteFn: func(context.Context, string, string, bool) error {
					return tc.err
				},
				cleanupOrphanedFn: func(context.Context, string) (*CleanupOrphanedResult, error) {
					return nil, tc.err
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

func TestWorktreeHandlerRemovePassesForceFlag(t *testing.T) {
	var gotForce bool
	h := NewWorktreeHandler(mockWorktreeService{
		listFn: func(context.Context, string) ([]git.Worktree, error) { return nil, nil },
		createFn: func(context.Context, string, string, string) (*git.Worktree, error) {
			return nil, nil
		},
		deleteFn: func(context.Context, string, string, bool) error {
			gotForce = true
			return nil
		},
		cleanupOrphanedFn: func(context.Context, string) (*CleanupOrphanedResult, error) {
			return &CleanupOrphanedResult{}, nil
		},
	})

	body, err := json.Marshal(map[string]any{
		"project_id": "proj",
		"issue_id":   "issue-c",
		"force":      true,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-force",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandWorktreeRemove,
		Body:            body,
	})
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}
	if !gotForce {
		t.Fatal("expected force flag passed to worktree service")
	}
}

func TestWorktreeHandlerUnsupportedCommand(t *testing.T) {
	h := NewWorktreeHandler(mockWorktreeService{
		listFn:   func(context.Context, string) ([]git.Worktree, error) { return nil, nil },
		createFn: func(context.Context, string, string, string) (*git.Worktree, error) { return nil, nil },
		deleteFn: func(context.Context, string, string, bool) error { return nil },
		cleanupOrphanedFn: func(context.Context, string) (*CleanupOrphanedResult, error) {
			return nil, nil
		},
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

func TestNormalizeCleanupOrphanedResult(t *testing.T) {
	result := &CleanupOrphanedResult{
		Removed: []git.Worktree{
			{Path: "/tmp/repo-c", Branch: "az/issue-c", IssueID: "issue-c"},
			{Path: "/tmp/repo-a", Branch: "az/issue-a", IssueID: "issue-a"},
			{Path: "/tmp/repo-b", Branch: "az/issue-b", IssueID: "issue-a"},
		},
		Skipped: []git.Worktree{
			{Path: "/tmp/repo-d", Branch: "az/issue-d", IssueID: "issue-d"},
			{Path: "/tmp/repo-b", Branch: "az/issue-b", IssueID: "issue-b"},
		},
	}

	normalizeCleanupOrphanedResult(result)

	if got, want := result.Removed, []git.Worktree{
		{Path: "/tmp/repo-a", Branch: "az/issue-a", IssueID: "issue-a"},
		{Path: "/tmp/repo-b", Branch: "az/issue-b", IssueID: "issue-a"},
		{Path: "/tmp/repo-c", Branch: "az/issue-c", IssueID: "issue-c"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Removed = %+v, want %+v", got, want)
	}

	if got, want := result.Skipped, []git.Worktree{
		{Path: "/tmp/repo-b", Branch: "az/issue-b", IssueID: "issue-b"},
		{Path: "/tmp/repo-d", Branch: "az/issue-d", IssueID: "issue-d"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Skipped = %+v, want %+v", got, want)
	}
}

func TestWorktreeHandlerUsesLongRunningExecutorForMutations(t *testing.T) {
	executor := &recordingWorktreeLongRunningExecutor{}
	h := NewWorktreeHandler(mockWorktreeService{
		listFn: func(context.Context, string) ([]git.Worktree, error) { return nil, nil },
		createFn: func(context.Context, string, string, string) (*git.Worktree, error) {
			return &git.Worktree{Path: "/tmp/repo-c", Branch: "az/issue-c", IssueID: "issue-c"}, nil
		},
		deleteFn: func(context.Context, string, string, bool) error { return nil },
		cleanupOrphanedFn: func(context.Context, string) (*CleanupOrphanedResult, error) {
			return &CleanupOrphanedResult{ProjectID: "proj"}, nil
		},
	}, WithWorktreeLongRunningExecutor(executor))

	body, err := json.Marshal(map[string]string{
		"project_id":  "proj",
		"issue_id":    "issue-c",
		"base_branch": "main",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-create",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandWorktreeCreate,
		Body:            body,
	})
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}
	if len(executor.commands) != 1 || executor.commands[0] != CommandWorktreeCreate {
		t.Fatalf("commands = %v, want [%s]", executor.commands, CommandWorktreeCreate)
	}
}

func TestWorktreeHandlerProjectIDFallbacks(t *testing.T) {
	t.Run("uses metadata project id when body project id is empty", func(t *testing.T) {
		var gotProjectID string
		h := NewWorktreeHandler(mockWorktreeService{
			listFn: func(_ context.Context, projectID string) ([]git.Worktree, error) {
				gotProjectID = projectID
				return []git.Worktree{}, nil
			},
			createFn: func(context.Context, string, string, string) (*git.Worktree, error) {
				return nil, nil
			},
			deleteFn: func(context.Context, string, string, bool) error { return nil },
			cleanupOrphanedFn: func(context.Context, string) (*CleanupOrphanedResult, error) {
				return &CleanupOrphanedResult{}, nil
			},
		})

		body, _ := json.Marshal(map[string]string{})
		resp := h.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-meta-project",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandWorktreeList,
			Meta:            protocol.Metadata{ProjectID: "proj-meta"},
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("response = %+v", resp.Error)
		}
		if gotProjectID != "proj-meta" {
			t.Fatalf("project id = %q, want proj-meta", gotProjectID)
		}
	})

	t.Run("rejects when project id is missing from body and metadata", func(t *testing.T) {
		var gotProjectID string
		h := NewWorktreeHandler(mockWorktreeService{
			listFn: func(_ context.Context, projectID string) ([]git.Worktree, error) {
				gotProjectID = projectID
				return []git.Worktree{}, nil
			},
			createFn: func(context.Context, string, string, string) (*git.Worktree, error) {
				return nil, nil
			},
			deleteFn: func(context.Context, string, string, bool) error { return nil },
			cleanupOrphanedFn: func(context.Context, string) (*CleanupOrphanedResult, error) {
				return &CleanupOrphanedResult{}, nil
			},
		})

		body, _ := json.Marshal(map[string]string{})
		resp := h.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-default-project",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandWorktreeList,
			Body:            body,
		})
		if resp.OK {
			t.Fatalf("expected invalid request for missing project route")
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
			t.Fatalf("error = %+v, want invalid_request", resp.Error)
		}
		if gotProjectID != "" {
			t.Fatalf("project id = %q, want service not called", gotProjectID)
		}
	})
}
