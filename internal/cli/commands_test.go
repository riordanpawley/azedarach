package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
)

type fakeDaemonTransport struct {
	handshakeFn func(context.Context, protocol.Hello) (protocol.HelloAck, error)
	commandFn   func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func ptrToString(v string) *string {
	return &v
}

func marshalTaskListBody(tasks []domain.Task) ([]byte, error) {
	return json.Marshal(protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 0,
		ProjectID:        naming.ProjectID(protocol.DefaultProjectID),
		LastCheckedAt:    time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            tasks,
	})
}

func TestNewDependenciesAtNormalizesWorktreeToBaseRepoRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.RepoDir != repo {
		t.Fatalf("RepoDir = %q, want %q", deps.RepoDir, repo)
	}
	if deps.RuntimeRepoDir != repo {
		t.Fatalf("RuntimeRepoDir = %q, want %q", deps.RuntimeRepoDir, repo)
	}
	wantProjectID, err := config.ProjectIDForRoot(repo)
	if err != nil {
		t.Fatalf("ProjectIDForRoot() error = %v", err)
	}
	if deps.ProjectID != wantProjectID {
		t.Fatalf("ProjectID = %q, want %q", deps.ProjectID, wantProjectID)
	}
	if deps.DaemonSocket != config.GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.GlobalDaemonSocketPath())
	}
}

func TestNewDependenciesAtUsesScopedSocketWhenEnabled(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.DaemonSocket != config.ScopedDaemonSocketPath(start) {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.ScopedDaemonSocketPath(start))
	}
	if deps.RuntimeRepoDir != worktree {
		t.Fatalf("RuntimeRepoDir = %q, want %q", deps.RuntimeRepoDir, worktree)
	}
}

func TestNewDependenciesAtUsesDistinctProjectIDsForDistinctRoots(t *testing.T) {
	base := t.TempDir()
	startA := filepath.Join(base, "a", "repo")
	startB := filepath.Join(base, "b", "repo")

	if err := os.MkdirAll(filepath.Join(startA, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(startA .git): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(startB, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(startB .git): %v", err)
	}

	t.Setenv("PATH", "")

	depsA, err := NewDependenciesAt(config.DefaultConfig(), startA)
	if err != nil {
		t.Fatalf("NewDependenciesAt(startA) error = %v", err)
	}
	depsB, err := NewDependenciesAt(config.DefaultConfig(), startB)
	if err != nil {
		t.Fatalf("NewDependenciesAt(startB) error = %v", err)
	}

	if depsA.ProjectID == depsB.ProjectID {
		t.Fatalf("ProjectID collision: %q", depsA.ProjectID)
	}
}

func TestNewDependenciesAtIgnoresAmbientGitDirRoutingVars(t *testing.T) {
	base := t.TempDir()
	repoA := filepath.Join(base, "repo-a")
	repoB := filepath.Join(base, "repo-b")

	if err := os.MkdirAll(filepath.Join(repoA, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repoA .git): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoB, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repoB .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("GIT_DIR", filepath.Join(repoA, ".git"))
	t.Setenv("GIT_WORK_TREE", repoA)

	deps, err := NewDependenciesAt(config.DefaultConfig(), repoB)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.RepoDir != repoB {
		t.Fatalf("RepoDir = %q, want %q", deps.RepoDir, repoB)
	}
}

func (f *fakeDaemonTransport) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	if f.handshakeFn != nil {
		return f.handshakeFn(ctx, hello)
	}
	return protocol.HelloAck{Accepted: true}, nil
}

func (f *fakeDaemonTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if f.commandFn != nil {
		return f.commandFn(ctx, req)
	}
	return protocol.ResponseEnvelope{}, nil
}

func (f *fakeDaemonTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func TestStartCommandUsesDaemonEnvelope(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case commandSessionStart:
					gotReq = req
					return responseWithOutput(req, "Starting session for: issue-1 - Example\nCreating worktree from branch: main\nWorktree created: /tmp/repo-issue-1\nCreating tmux session: issue-1\n\n✓ Session started successfully\n  To attach: az attach issue-1\n  Or run:    tmux attach-session -t issue-1\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	if gotReq.Command != commandSessionStart {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandSessionStart)
	}
	if len(commands) != 2 || commands[0] != daemonclient.CommandTaskList || commands[1] != commandSessionStart {
		t.Fatalf("commands = %v", commands)
	}
	var body sessionRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.SessionID != "issue-1" || body.BaseBranch != "main" {
		t.Fatalf("body = %+v", body)
	}
	if output != "Starting session for: issue-1 - Example\nCreating worktree from branch: main\nWorktree created: /tmp/repo-issue-1\nCreating tmux session: issue-1\n\n✓ Session started successfully\n  To attach: az attach issue-1\n  Or run:    tmux attach-session -t issue-1\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestAttachKillAndStatusCommandsUseDaemonEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		command     func(*Dependencies, string) error
		sessionID   string
		wantCommand string
		response    string
	}{
		{
			name:        "attach",
			command:     AttachCommand,
			sessionID:   "issue-2",
			wantCommand: commandSessionAttach,
			response:    "Attaching to session: issue-2\n(Press Ctrl+B then D to detach)\n",
		},
		{
			name:        "kill",
			command:     KillCommand,
			sessionID:   "issue-3",
			wantCommand: commandSessionStop,
			response:    "Killing session: issue-3\n✓ Session killed: issue-3\n  Note: Worktree is preserved. Use 'git worktree remove' to clean up.\n",
		},
		{
			name:        "status",
			command:     StatusCommand,
			sessionID:   "",
			wantCommand: commandSessionStatus,
			response:    "Active Sessions (1):\n\nISSUE ID  STATUS   TITLE\n-------  ------   -----\nbead-4   active   Example task\n\nUse 'az attach <issue-id>' to attach to a session\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			commands := []string{}
			taskListBody, err := marshalTaskListBody([]domain.Task{
				{ID: naming.IssueID(tt.sessionID), Title: "Example task", Status: domain.StatusOpen},
			})
			if err != nil {
				t.Fatalf("marshal task list: %v", err)
			}
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						commands = append(commands, req.Command)
						if tt.wantCommand != commandSessionStatus && req.Command == daemonclient.CommandTaskList {
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								CompletedAt:     req.SentAt,
								OK:              true,
								Body:            taskListBody,
							}, nil
						}
						if req.Command != tt.wantCommand {
							t.Fatalf("unexpected command: %s", req.Command)
						}
						gotReq = req
						return responseWithOutput(req, tt.response), nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			output := captureStdout(t, func() error {
				return tt.command(deps, tt.sessionID)
			})

			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			switch tt.wantCommand {
			case commandSessionAttach, commandSessionStop:
				if len(commands) != 2 || commands[0] != daemonclient.CommandTaskList || commands[1] != tt.wantCommand {
					t.Fatalf("commands = %v", commands)
				}
			case commandSessionStatus:
				if len(commands) != 1 || commands[0] != commandSessionStatus {
					t.Fatalf("commands = %v", commands)
				}
			}
			if gotReq.Meta.ProjectID != "proj" {
				t.Fatalf("meta project_id = %q, want proj", gotReq.Meta.ProjectID)
			}
			if output != tt.response {
				t.Fatalf("output = %q, want %q", output, tt.response)
			}
		})
	}
}

func TestSessionCommandsRejectInvalidOrUnknownIssueIDs(t *testing.T) {
	tests := []struct {
		name       string
		command    func(*Dependencies, string) error
		issueID    string
		taskIDs    []string
		wantSubstr string
	}{
		{
			name:       "start invalid id format",
			command:    StartCommand,
			issueID:    "bad id",
			wantSubstr: "invalid issue id",
		},
		{
			name:       "start unknown id",
			command:    StartCommand,
			issueID:    "az-missing",
			taskIDs:    []string{"az-1"},
			wantSubstr: "issue not found: az-missing",
		},
		{
			name:       "attach unknown id",
			command:    AttachCommand,
			issueID:    "az-missing",
			taskIDs:    []string{"az-1"},
			wantSubstr: "issue not found: az-missing",
		},
		{
			name:       "kill unknown id",
			command:    KillCommand,
			issueID:    "az-missing",
			taskIDs:    []string{"az-1"},
			wantSubstr: "issue not found: az-missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands := []string{}
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						commands = append(commands, req.Command)
						if req.Command != daemonclient.CommandTaskList {
							t.Fatalf("unexpected command: %s", req.Command)
						}
						tasks := make([]domain.Task, 0, len(tt.taskIDs))
						for _, id := range tt.taskIDs {
							tasks = append(tasks, domain.Task{ID: naming.IssueID(id), Title: id, Status: domain.StatusOpen})
						}
						body, err := marshalTaskListBody(tasks)
						if err != nil {
							t.Fatalf("marshal task list: %v", err)
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							Meta:            req.Meta,
							CompletedAt:     req.SentAt,
							OK:              true,
							Body:            body,
						}, nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			err := tt.command(deps, tt.issueID)
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("err = %v, want substring %q", err, tt.wantSubstr)
			}

			if strings.Contains(tt.wantSubstr, "invalid issue id") {
				if len(commands) != 0 {
					t.Fatalf("commands for invalid ID = %v, want none", commands)
				}
				return
			}
			if len(commands) != 1 || commands[0] != daemonclient.CommandTaskList {
				t.Fatalf("commands for unknown ID = %v, want [%s]", commands, daemonclient.CommandTaskList)
			}
		})
	}
}

func TestStartCommandPrintsNestedOperationOutput(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				}
				nested, err := json.Marshal(commandOutputBody{Output: "wrapped output\n"})
				if err != nil {
					t.Fatalf("marshal nested response: %v", err)
				}
				body, err := json.Marshal(map[string]any{
					"operation_id": "op-start",
					"state":        "done",
					"result":       json.RawMessage(nested),
				})
				if err != nil {
					t.Fatalf("marshal wrapped response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	if output != "wrapped output\n" {
		t.Fatalf("output = %q, want wrapped output", output)
	}
}

func TestBranchMergeToMainCommandUsesDaemonGitFlow(t *testing.T) {
	commands := make([]string, 0, 8)
	mainWorktree := t.TempDir()
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-123",
								"branch":   "riordan/az-123/some-change",
								"issue_id": "az-123",
							},
						},
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree != "/tmp/azedarach-az-123" && body.Worktree != mainWorktree {
						t.Fatalf("git status worktree = %q", body.Worktree)
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: mainWorktree,
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: mainWorktree,
						Branch:   "main",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: mainWorktree,
						Branch:   "riordan/az-123/some-change",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   mainWorktree,
	}

	output := captureStdout(t, func() error {
		return BranchMergeToMainCommand(deps, "az-123")
	})
	if !strings.Contains(output, "merge complete") {
		t.Fatalf("output = %q, want merge output", output)
	}
	if !strings.Contains(output, "Merged riordan/az-123/some-change into main (az-123)") {
		t.Fatalf("output = %q, want final summary", output)
	}

	want := []string{
		daemonclient.CommandWorktreeList,
		daemonclient.CommandGitStatus,
		daemonclient.CommandGitStatus,
		daemonclient.CommandGitFetch,
		daemonclient.CommandGitCheckout,
		daemonclient.CommandGitMerge,
	}
	filtered := make([]string, 0, len(commands))
	for _, cmd := range commands {
		if cmd == protocol.CommandOperationGet {
			continue
		}
		filtered = append(filtered, cmd)
	}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("commands = %#v, want %#v", filtered, want)
	}
}

func TestBranchMergeToMainCommandFailsOnDirtyPreflight(t *testing.T) {
	commands := make([]string, 0, 8)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-123",
								"branch":   "riordan/az-123/some-change",
								"issue_id": "az-123",
							},
						},
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree == "/tmp/azedarach-az-123" {
						return responseWithJSON(req, map[string]any{
							"status": gitservice.GitStatus{
								HasChanges: true,
								Modified:   []string{"foo.go"},
							},
						}), nil
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	err := BranchMergeToMainCommand(deps, "az-123")
	if err == nil || !strings.Contains(err.Error(), "merge preflight failed") {
		t.Fatalf("err = %v, want preflight failure", err)
	}
	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitFetch || cmd == daemonclient.CommandGitCheckout || cmd == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected post-preflight command: %s", cmd)
		}
	}
}

func TestBranchMergeToMainCommandUsesEnvIssueIDWhenArgumentMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-999")

	commands := make([]string, 0, 8)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-999",
								"branch":   "riordan/az-999/some-change",
								"issue_id": "az-999",
							},
						},
					}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: ".", Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: ".", Branch: "main"}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: ".",
						Branch:   "riordan/az-999/some-change",
						Result: gitservice.MergeResult{
							Success: true,
						},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	if err := BranchMergeToMainCommand(deps, ""); err != nil {
		t.Fatalf("BranchMergeToMainCommand error = %v", err)
	}

	foundMerge := false
	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitMerge {
			foundMerge = true
			break
		}
	}
	if !foundMerge {
		t.Fatalf("expected git merge command in flow, commands=%v", commands)
	}
}

func TestBranchMergeToMainCommandTreatsAzedarachRuntimeConfigAsDirtyInPreflight(t *testing.T) {
	commands := make([]string, 0, 8)
	mainWorktree := t.TempDir()
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-bhv",
								"branch":   "riordan/bhv/fix-mtm-timeout",
								"issue_id": "bhv",
							},
						},
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree == mainWorktree {
						return responseWithJSON(req, map[string]any{
							"status": gitservice.GitStatus{
								HasChanges: true,
								Modified:   []string{".azedarach/config.json"},
							},
						}), nil
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: mainWorktree, Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: mainWorktree, Branch: "main"}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: mainWorktree,
						Branch:   "riordan/bhv/fix-mtm-timeout",
						Result: gitservice.MergeResult{
							Success: true,
						},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   mainWorktree,
	}

	err := BranchMergeToMainCommand(deps, "bhv")
	if err == nil || !strings.Contains(err.Error(), "merge preflight failed") {
		t.Fatalf("err = %v, want preflight failure", err)
	}

	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected post-preflight command: %s", cmd)
		}
	}
}

func TestStartCommandPrintsPendingOperationState(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				}
				body, err := json.Marshal(map[string]any{
					"operation_id": "op-start",
					"state":        string(protocol.OperationStateQueued),
				})
				if err != nil {
					t.Fatalf("marshal wrapped response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	if output != "Operation queued: op-start\n" {
		t.Fatalf("output = %q, want queued operation line", output)
	}
}

func TestStartCommandWithWaitPrintsTerminalOperationOutput(t *testing.T) {
	var calls int
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				calls++
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case commandSessionStart:
					body, err := json.Marshal(map[string]any{
						"operation_id": "op-start",
						"state":        string(protocol.OperationStateQueued),
					})
					if err != nil {
						t.Fatalf("marshal pending response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            body,
					}, nil
				case protocol.CommandOperationGet:
					body, err := json.Marshal(protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-start",
							ProjectID:   "proj",
							Kind:        commandSessionStart,
							State:       protocol.OperationStateDone,
							Result:      mustJSON(t, commandOutputBody{Output: "started\n"}),
						},
					})
					if err != nil {
						t.Fatalf("marshal get response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            body,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "issue-1", SessionCommandOptions{
			Wait:         true,
			PollInterval: time.Millisecond,
		})
	})

	if output != "started\n" {
		t.Fatalf("output = %q, want terminal operation output", output)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestOperationCommandsParseAndRender(t *testing.T) {
	getOpts, err := ParseOperationGetArgs([]string{"--wait", "--poll-interval", "500ms", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationGetArgs error: %v", err)
	}
	if getOpts.OperationID != "op-1" || !getOpts.Wait || getOpts.PollInterval != 500*time.Millisecond {
		t.Fatalf("get opts = %+v", getOpts)
	}

	logsOpts, err := ParseOperationLogsArgs([]string{"--json", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationLogsArgs error: %v", err)
	}
	if logsOpts.OperationID != "op-1" || !logsOpts.JSON {
		t.Fatalf("logs opts = %+v", logsOpts)
	}

	listOpts, err := ParseOperationListArgs([]string{"--issue", "az-1", "--kind", "session.start", "--state", "queued", "--states", "running", "--limit", "3"})
	if err != nil {
		t.Fatalf("ParseOperationListArgs error: %v", err)
	}
	if listOpts.IssueID != "az-1" || listOpts.Kind != "session.start" || listOpts.Limit != 3 || len(listOpts.States) != 2 {
		t.Fatalf("list opts = %+v", listOpts)
	}

	cancelOpts, err := ParseOperationCancelArgs([]string{"--reason", "user request", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationCancelArgs error: %v", err)
	}
	if cancelOpts.OperationID != "op-1" || cancelOpts.Reason != "user request" {
		t.Fatalf("cancel opts = %+v", cancelOpts)
	}
}

func TestParseLogArgs(t *testing.T) {
	opts, err := ParseLogArgs([]string{"--source", "daemon,tui", "--lines", "50", "--no-follow", "cli"})
	if err != nil {
		t.Fatalf("ParseLogArgs() error = %v", err)
	}
	if opts.Lines != 50 || opts.Follow {
		t.Fatalf("ParseLogArgs() lines/follow = %+v", opts)
	}
	if got, want := opts.Sources, []string{"daemon", "tui", "cli"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLogArgs() sources = %v, want %v", got, want)
	}

	defaultOpts, err := ParseLogArgs(nil)
	if err != nil {
		t.Fatalf("ParseLogArgs(nil) error = %v", err)
	}
	if got, want := defaultOpts.Sources, []string{"daemon", "tui", "cli"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLogArgs(nil) sources = %v, want %v", got, want)
	}

	_, err = ParseLogArgs([]string{"daemon", "tui", "--no-follow", "--lines", "100"})
	if err == nil || !strings.Contains(err.Error(), "flags must come before positional sources") {
		t.Fatalf("ParseLogArgs(interspersed) error = %v, want ordering guidance", err)
	}

	if _, err := ParseLogArgs([]string{"worker"}); err == nil {
		t.Fatal("expected unknown source error")
	}
	if _, err := ParseLogArgs([]string{"--lines", "0"}); err == nil {
		t.Fatal("expected lines validation error")
	}
}

func TestOperationCommandsUseDaemonClient(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   "proj",
							Kind:        "session.start",
							State:       protocol.OperationStateFailed,
							Payload:     mustJSON(t, map[string]string{"project_id": "proj", "session_id": "az-1"}),
							Result:      mustJSON(t, map[string]string{"output": "tmux attach failed"}),
							Error: &protocol.OperationError{
								Code:    protocol.ErrorCodeInternal,
								Message: "tmux attach failed: exited 1",
							},
						},
					}), nil
				case protocol.CommandOperationList:
					return responseWithJSON(req, protocol.OperationListResponseBody{
						ProjectID: "proj",
						Operations: []protocol.OperationRecord{
							{
								OperationID: "op-1",
								ProjectID:   "proj",
								Kind:        "session.start",
								State:       protocol.OperationStateQueued,
							},
						},
					}), nil
				case protocol.CommandOperationCancel:
					return responseWithJSON(req, protocol.OperationCancelResponseBody{
						Cancelled: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   "proj",
							Kind:        "session.start",
							State:       protocol.OperationStateCancelled,
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		if err := OperationGetCommand(deps, OperationGetOptions{OperationID: "op-1"}); err != nil {
			return err
		}
		if err := OperationLogsCommand(deps, OperationLogsOptions{OperationID: "op-1"}); err != nil {
			return err
		}
		if err := OperationListCommand(deps, OperationListOptions{IssueID: "az-1", Limit: 5}); err != nil {
			return err
		}
		return OperationCancelCommand(deps, OperationCancelOptions{OperationID: "op-1"})
	})

	for _, needle := range []string{"ID", "STATE", "KIND", "op-1", "failed", "queued", "cancelled", "Payload:", "Result (raw JSON):", "tmux attach failed: exited 1"} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output = %q, want %q", output, needle)
		}
	}
}

func TestLogCommandPrintsSourcePrefixedPrettyLines(t *testing.T) {
	repoDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	deps := &Dependencies{
		Config:  cfg,
		RepoDir: repoDir,
	}
	daemonLogPath := filepath.Join(repoDir, ".azedarach", "daemon.log")
	tuiLogPath := filepath.Join(cfg.Session.LogDir, "az.log")
	if err := os.MkdirAll(filepath.Dir(daemonLogPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(daemon log dir): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tuiLogPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(tui log dir): %v", err)
	}
	if err := os.WriteFile(daemonLogPath, []byte("2026/04/01 16:50:04 INFO daemon started\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(daemon log): %v", err)
	}
	if err := os.WriteFile(tuiLogPath, []byte("time=2026-04-01T16:50:15.468+11:00 level=INFO msg=\"hello from tui\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tui log): %v", err)
	}

	output := captureStdout(t, func() error {
		return LogCommand(deps, LogOptions{
			Sources: []string{"daemon", "tui", "cli"},
			Lines:   25,
			Follow:  false,
		})
	})
	if !strings.Contains(output, "[daemon] 2026-04-01 16:50:04") {
		t.Fatalf("output = %q, want daemon timestamp in normalized format", output)
	}
	if !strings.Contains(output, "INFO daemon started") {
		t.Fatalf("output = %q, want daemon message", output)
	}
	if !strings.Contains(output, "[tui]") {
		t.Fatalf("output = %q, want tui source prefix", output)
	}
	if strings.Contains(output, "time=2026-04-01T16:50:15.468+11:00") {
		t.Fatalf("output = %q, want tui time field removed from message body", output)
	}
	if !strings.Contains(output, "level=INFO msg=\"hello from tui\"") {
		t.Fatalf("output = %q, want tui message payload", output)
	}
}

func TestResolveSessionLogDirFor_UsesScopedWorktreeDirInJustRunMode(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	t.Setenv("PATH", "")
	t.Chdir(nested)

	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	got := resolveSessionLogDirFor(cfg, nested)
	want := filepath.Join(worktree, ".azedarach")
	if got != want {
		t.Fatalf("resolveSessionLogDirFor() = %q, want %q", got, want)
	}
}

func TestLogCommandReadsScopedWorktreeDaemonAndTUILogs(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	t.Setenv("PATH", "")
	t.Chdir(nested)

	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	deps := &Dependencies{
		Config:  cfg,
		RepoDir: repo,
	}

	daemonLogPath := filepath.Join(worktree, ".azedarach", "daemon.log")
	tuiLogPath := filepath.Join(worktree, ".azedarach", "az.log")
	if err := os.MkdirAll(filepath.Dir(daemonLogPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(log dir): %v", err)
	}
	if err := os.WriteFile(daemonLogPath, []byte("2026/04/01 16:50:04 INFO daemon started scoped\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(daemon log): %v", err)
	}
	if err := os.WriteFile(tuiLogPath, []byte("time=2026-04-01T16:50:15.468+11:00 level=INFO msg=\"hello from scoped tui\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tui log): %v", err)
	}

	output := captureStdout(t, func() error {
		return LogCommand(deps, LogOptions{
			Sources: []string{"daemon", "tui"},
			Lines:   25,
			Follow:  false,
		})
	})
	if !strings.Contains(output, "daemon started scoped") {
		t.Fatalf("output = %q, want scoped daemon log line", output)
	}
	if !strings.Contains(output, "hello from scoped tui") {
		t.Fatalf("output = %q, want scoped tui log line", output)
	}
}

func TestLogCommandErrorsWhenAllSelectedLogsMissing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	deps := &Dependencies{
		Config:  cfg,
		RepoDir: t.TempDir(),
	}

	err := LogCommand(deps, LogOptions{
		Sources: []string{"daemon", "tui", "cli"},
		Lines:   10,
		Follow:  false,
	})
	if err == nil || !strings.Contains(err.Error(), "none of the selected log files exist yet") {
		t.Fatalf("LogCommand() error = %v, want missing files error", err)
	}
}

func TestCommandErrorUsesTransportMessage(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: protocol.CurrentVersion,
					RequestID:       "req",
					Kind:            protocol.EnvelopeKindResponse,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:      protocol.ErrorCodeConflict,
						Message:   "session already exists: issue-1 (use 'az attach issue-1' to connect)",
						Retryable: false,
					},
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err = StartCommand(deps, "issue-1")
	if err == nil || err.Error() != "session already exists: issue-1 (use 'az attach issue-1' to connect)" {
		t.Fatalf("error = %v", err)
	}
}

func TestResponseExitCode(t *testing.T) {
	tests := []struct {
		name string
		resp protocol.ResponseEnvelope
		want int
	}{
		{
			name: "success response",
			resp: protocol.ResponseEnvelope{OK: true},
			want: 0,
		},
		{
			name: "dry-run preview response",
			resp: protocol.ResponseEnvelope{
				OK: true,
				Body: mustApplyDryRunPreviewBody(t, applyDryRunPreviewBody{
					SchemaVersion:    protocol.ApplySchemaVersion,
					SnapshotRevision: 7,
					DryRun:           true,
					Operations: []applyDryRunPreviewOperationBody{
						{
							Index:   0,
							Command: "task.create",
							Body:    json.RawMessage(`{"title":"First task","description":"Draft","type":"task","priority":"high"}`),
						},
					},
				}),
			},
			want: 0,
		},
		{
			name: "partial failure response",
			resp: protocol.ResponseEnvelope{
				OK: true,
				Body: mustApplyResultBody(t, applyExecutionSummaryBody{
					Total:     3,
					Succeeded: 2,
					Failed:    1,
				}),
			},
			want: 2,
		},
		{
			name: "contract failure",
			resp: protocol.ResponseEnvelope{
				OK:   true,
				Body: []byte(`{"summary":`),
			},
			want: 1,
		},
		{
			name: "transport failure",
			resp: protocol.ResponseEnvelope{OK: false},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyResponseExitCode(tt.resp); got != tt.want {
				t.Fatalf("applyResponseExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseExportArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ExportOptions
		errContains string
	}{
		{
			name: "defaults",
			want: ExportOptions{
				Format: "json",
				Out:    "",
			},
		},
		{
			name: "explicit out path",
			args: []string{"--format", "json", "--out", "snapshot.json"},
			want: ExportOptions{
				Format: "json",
				Out:    "snapshot.json",
			},
		},
		{
			name:        "rejects unsupported format",
			args:        []string{"--format", "yaml"},
			errContains: "unsupported export format: yaml",
		},
		{
			name:        "rejects extra arguments",
			args:        []string{"unexpected"},
			errContains: "unexpected argument: unexpected",
		},
		{
			name:        "rejects unknown flag",
			args:        []string{"--bogus"},
			errContains: "flag provided but not defined: -bogus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExportArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseExportArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseExportArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseConfigSetArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ConfigSetOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"spec.enabled", "false"},
			want: ConfigSetOptions{Key: "spec.enabled", Value: "false", ProjectDir: ""},
		},
		{
			name: "project dir option",
			args: []string{"--project-dir", "workspace", "spec.enabled", "yes"},
			want: ConfigSetOptions{Key: "spec.enabled", Value: "yes", ProjectDir: "workspace"},
		},
		{
			name:        "rejects missing args",
			args:        []string{"spec.enabled"},
			errContains: "usage: az config set spec.enabled <true|false> [--project-dir <dir>]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConfigSetArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseConfigSetArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseConfigSetArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSyncArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        SyncOptions
		errContains string
	}{
		{
			name: "defaults",
			want: SyncOptions{},
		},
		{
			name: "all flag",
			args: []string{"--all"},
			want: SyncOptions{All: true},
		},
		{
			name: "positional project dir",
			args: []string{"workspace"},
			want: SyncOptions{ProjectDir: "workspace"},
		},
		{
			name: "project dir option",
			args: []string{"--project-dir", "workspace"},
			want: SyncOptions{ProjectDir: "workspace"},
		},
		{
			name:        "rejects conflicting project dir inputs",
			args:        []string{"--project-dir", "workspace", "other"},
			errContains: "usage: az sync [--all] [<directory>] [--project-dir <dir>]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSyncArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSyncArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseSyncArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseImplDeleteArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ImplDeleteOptions
		errContains string
	}{
		{
			name: "valid",
			args: []string{"--confirm", "go-bubbletea"},
			want: ImplDeleteOptions{Implementation: "go-bubbletea", Confirm: true},
		},
		{
			name:        "missing confirm",
			args:        []string{"go-bubbletea"},
			errContains: "missing required flag: --confirm",
		},
		{
			name:        "missing implementation",
			args:        []string{"--confirm"},
			errContains: "usage: az impl delete --confirm <implementation>",
		},
		{
			name:        "extra args",
			args:        []string{"go-bubbletea", "extra", "--confirm"},
			errContains: "usage: az impl delete --confirm <implementation>",
		},
		{
			name:        "reject positional before flag",
			args:        []string{"go-bubbletea", "--confirm"},
			errContains: "usage: az impl delete --confirm <implementation>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseImplDeleteArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImplDeleteArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseImplDeleteArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseImplListArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		errContains string
	}{
		{name: "valid"},
		{
			name:        "rejects extra args",
			args:        []string{"extra"},
			errContains: "usage: az impl list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseImplListArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImplListArgs() error = %v", err)
			}
		})
	}
}

func TestParseImplMigrateArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ImplMigrateOptions
		errContains string
	}{
		{
			name: "valid",
			args: []string{"ts-opentui", "default"},
			want: ImplMigrateOptions{
				FromImplementation: "ts-opentui",
				ToImplementation:   "default",
			},
		},
		{
			name:        "missing destination",
			args:        []string{"ts-opentui"},
			errContains: "usage: az impl migrate <from-implementation> <to-implementation>",
		},
		{
			name:        "same source and destination",
			args:        []string{"default", "default"},
			errContains: "source and destination implementations must differ",
		},
		{
			name:        "extra args",
			args:        []string{"ts-opentui", "default", "extra"},
			errContains: "usage: az impl migrate <from-implementation> <to-implementation>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseImplMigrateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImplMigrateArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseImplMigrateArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestExportCommandWritesStdoutByDefault(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 7,
	})

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return ExportCommand(deps, ExportOptions{Format: "json"})
	})

	if gotReq.Command != commandTaskSnapshotExport {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandTaskSnapshotExport)
	}
	if gotReq.Meta.ProjectID != "proj" {
		t.Fatalf("meta project_id = %q, want proj", gotReq.Meta.ProjectID)
	}
	if output != string(payload) {
		t.Fatalf("stdout = %q, want %q", output, string(payload))
	}
}

func TestExportCommandWritesFileWhenOutIsSet(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 11,
	})
	outPath := filepath.Join(t.TempDir(), "snapshot.json")

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	if err := ExportCommand(deps, ExportOptions{Format: "json", Out: outPath}); err != nil {
		t.Fatalf("ExportCommand() error = %v", err)
	}
	if gotReq.Command != commandTaskSnapshotExport {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandTaskSnapshotExport)
	}

	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(written) != string(payload) {
		t.Fatalf("file = %q, want %q", string(written), string(payload))
	}
}

func TestExportCommandSurfacesFileWriteErrors(t *testing.T) {
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 23,
	})
	outPath := filepath.Join(t.TempDir(), "missing", "snapshot.json")

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	err := ExportCommand(deps, ExportOptions{Format: "json", Out: outPath})
	if err == nil || !strings.Contains(err.Error(), "write export output to") {
		t.Fatalf("error = %v, want write failure", err)
	}
}

func TestConfigSetCommandWritesSpecEnabledConfig(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	output := captureStdout(t, func() error {
		return ConfigSetCommand(deps, ConfigSetOptions{Key: "spec.enabled", Value: "off"})
	})

	if !strings.Contains(output, "Updated ") || !strings.Contains(output, "spec.enabled=false") {
		t.Fatalf("config output missing update line: %q", output)
	}
	if !strings.Contains(output, "Spec workflows are disabled.") {
		t.Fatalf("config output missing spec-disabled note: %q", output)
	}

	cfg, err := config.LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Spec.Enabled {
		t.Fatalf("Spec.Enabled = true, want false")
	}
}

func TestConfigSetCommandRejectsInvalidBoolean(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "spec.enabled", Value: "maybe"})
	if err == nil || !strings.Contains(err.Error(), "Invalid boolean value 'maybe' for spec.enabled") {
		t.Fatalf("error = %v, want invalid boolean failure", err)
	}
}

func TestConfigSetCommandRejectsUnsupportedKey(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "git.baseBranch", Value: "main"})
	if err == nil || !strings.Contains(err.Error(), "Unsupported config key 'git.baseBranch'") {
		t.Fatalf("error = %v, want unsupported key failure", err)
	}
}

func TestSyncCommandAllUsesDaemonWorktreeTargetsAndDaemonSnapshot(t *testing.T) {
	var gotWorktreeReq protocol.RequestEnvelope
	var gotSnapshotReq protocol.RequestEnvelope
	tasks := []domain.Task{
		{ID: "az-1", Title: "Sync task one", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Sync task two", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeTask},
	}
	payload, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					gotWorktreeReq = req
					body, err := json.Marshal(map[string]any{
						"project_id": "proj",
						"worktrees": []map[string]any{
							{
								"path":     filepath.Join("worktree-a"),
								"branch":   "az/worktree-a",
								"issue_id": "az-1",
							},
							{
								"path":     filepath.Join("worktree-b"),
								"branch":   "az/worktree-b",
								"issue_id": "az-2",
							},
						},
					})
					if err != nil {
						t.Fatalf("marshal worktrees: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        41,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskList:
					gotSnapshotReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        42,
						Body:            payload,
					}, nil
				default:
					t.Fatalf("unexpected command = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return SyncCommand(deps, SyncOptions{All: true})
	})

	if gotWorktreeReq.Command != daemonclient.CommandWorktreeList {
		t.Fatalf("worktree command = %q, want %q", gotWorktreeReq.Command, daemonclient.CommandWorktreeList)
	}
	if gotWorktreeReq.Meta.ProjectID != "proj" {
		t.Fatalf("worktree project_id = %q, want proj", gotWorktreeReq.Meta.ProjectID)
	}
	var worktreeBody struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(gotWorktreeReq.Body, &worktreeBody); err != nil {
		t.Fatalf("unmarshal worktree request: %v", err)
	}
	if worktreeBody.ProjectID != "proj" {
		t.Fatalf("worktree request project_id = %q, want proj", worktreeBody.ProjectID)
	}
	if gotSnapshotReq.Command != daemonclient.CommandTaskList {
		t.Fatalf("snapshot command = %q, want %q", gotSnapshotReq.Command, daemonclient.CommandTaskList)
	}
	if !strings.Contains(output, "Syncing issue tracker state...") {
		t.Fatalf("sync output missing heading: %q", output)
	}
	if !strings.Contains(output, "Targets: 2 worktree(s)") {
		t.Fatalf("sync output missing target count: %q", output)
	}
	if !strings.Contains(output, "worktree-a") || !strings.Contains(output, "worktree-b") {
		t.Fatalf("sync output missing worktree paths: %q", output)
	}
	if !strings.Contains(output, "Snapshot: tasks=2 revision=42") {
		t.Fatalf("sync output missing snapshot summary: %q", output)
	}
}

func TestImplDeleteCommandRemovesAssignmentsAcrossIssues(t *testing.T) {
	tasks := []domain.Task{
		{ID: "az-1", Title: "One", Description: "desc", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, Implementations: []string{"go-bubbletea", "ts-opentui"}},
		{ID: "az-2", Title: "Two", Description: "desc", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug, Implementations: []string{"ts-opentui"}},
		{ID: "az-3", Title: "Three", Description: "desc", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeFeature, Implementations: []string{"go-bubbletea"}},
	}
	payload, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}

	type updateReq struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	updates := make([]updateReq, 0, 2)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            payload,
					}, nil
				case daemonclient.CommandTaskUpdate:
					var body updateReq
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal update request: %v", err)
					}
					updates = append(updates, body)
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return ImplDeleteCommand(deps, ImplDeleteOptions{Implementation: "ts-opentui", Confirm: true})
	})
	if !strings.Contains(output, "Deleted implementation assignment: ts-opentui") {
		t.Fatalf("output missing delete summary: %q", output)
	}
	if !strings.Contains(output, "Updated issues: 2") {
		t.Fatalf("output missing update count: %q", output)
	}
	if len(updates) != 2 {
		t.Fatalf("update call count = %d, want 2", len(updates))
	}

	got := map[string][]string{}
	for _, update := range updates {
		got[update.TaskID] = update.Implementations
	}
	if !reflect.DeepEqual(got["az-1"], []string{"go-bubbletea"}) {
		t.Fatalf("az-1 implementations = %+v, want [go-bubbletea]", got["az-1"])
	}
	if len(got["az-2"]) != 0 {
		t.Fatalf("az-2 implementations = %+v, want empty", got["az-2"])
	}
	if _, ok := got["az-3"]; ok {
		t.Fatalf("did not expect az-3 update, got map=%+v", got)
	}
}

func TestImplListCommandPrintsSortedUniqueImplementations(t *testing.T) {
	tasks := []domain.Task{
		{ID: "az-1", Implementations: []string{"go-bubbletea", "ts-opentui"}},
		{ID: "az-2", Implementations: []string{"default", "go-bubbletea"}},
		{ID: "az-3", Implementations: []string{" ", ""}},
	}
	payload, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return ImplListCommand(deps, ImplListOptions{})
	})
	if !strings.Contains(output, "Implementations: 3") {
		t.Fatalf("output missing implementation count: %q", output)
	}
	if !strings.Contains(output, "default\ngo-bubbletea\nts-opentui\n") {
		t.Fatalf("output missing sorted implementation rows: %q", output)
	}
}

func TestImplMigrateCommandMigratesAssignmentsAcrossIssues(t *testing.T) {
	tasks := []domain.Task{
		{ID: "az-1", Title: "One", Description: "desc", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, Implementations: []string{"default", "ts-opentui"}},
		{ID: "az-2", Title: "Two", Description: "desc", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug, Implementations: []string{"ts-opentui", "go-bubbletea", "default"}},
		{ID: "az-3", Title: "Three", Description: "desc", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeFeature, Implementations: []string{"go-bubbletea"}},
	}
	payload, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}

	type updateReq struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	updates := make([]updateReq, 0, 2)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            payload,
					}, nil
				case daemonclient.CommandTaskUpdate:
					var body updateReq
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal update request: %v", err)
					}
					updates = append(updates, body)
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return ImplMigrateCommand(deps, ImplMigrateOptions{FromImplementation: "ts-opentui", ToImplementation: "default"})
	})
	if !strings.Contains(output, "Migrated implementation assignment: ts-opentui -> default") {
		t.Fatalf("output missing migrate summary: %q", output)
	}
	if !strings.Contains(output, "Updated issues: 2") {
		t.Fatalf("output missing update count: %q", output)
	}
	if len(updates) != 2 {
		t.Fatalf("update call count = %d, want 2", len(updates))
	}

	got := map[string][]string{}
	for _, update := range updates {
		got[update.TaskID] = update.Implementations
	}
	if !reflect.DeepEqual(got["az-1"], []string{"default"}) {
		t.Fatalf("az-1 implementations = %+v, want [default]", got["az-1"])
	}
	if !reflect.DeepEqual(got["az-2"], []string{"default", "go-bubbletea"}) {
		t.Fatalf("az-2 implementations = %+v, want [default go-bubbletea]", got["az-2"])
	}
	if _, ok := got["az-3"]; ok {
		t.Fatalf("did not expect az-3 update, got map=%+v", got)
	}
}

func TestParseIssueListArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueListOptions
		errContains string
	}{
		{
			name: "defaults",
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit},
		},
		{
			name: "json output",
			args: []string{"--json"},
			want: IssueListOptions{JSON: true, Deps: false, Limit: defaultIssueListLimit},
		},
		{
			name: "deps projection",
			args: []string{"--deps"},
			want: IssueListOptions{JSON: false, Deps: true, Limit: defaultIssueListLimit},
		},
		{
			name: "limit override",
			args: []string{"--limit", "25"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: 25},
		},
		{
			name: "status filters",
			args: []string{"--status", "open", "--status", "blocked", "--statuses", "in_progress,open"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, States: []domain.Status{domain.StatusOpen, domain.StatusBlocked, domain.StatusInProgress}},
		},
		{
			name: "state aliases",
			args: []string{"--state", "open", "--states", "blocked"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, States: []domain.Status{domain.StatusOpen, domain.StatusBlocked}},
		},
		{
			name: "id filters",
			args: []string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, IDs: []string{"az-1", "az-2", "az-3", "az-4"}},
		},
		{
			name:        "invalid limit",
			args:        []string{"--limit", "0"},
			errContains: "limit must be >= 1",
		},
		{
			name:        "rejects extra args",
			args:        []string{"extra"},
			errContains: "unexpected argument: extra",
		},
		{
			name:        "invalid state",
			args:        []string{"--status", "queued"},
			errContains: "invalid status: queued",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueListArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueListArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseIssueListArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueGetArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueGetOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"az-1"},
			want: IssueGetOptions{IssueID: "az-1", JSON: false},
		},
		{
			name: "json output",
			args: []string{"--json", "az-2"},
			want: IssueGetOptions{IssueID: "az-2", JSON: true},
		},
		{
			name:        "missing issue id",
			args:        []string{},
			errContains: "usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		},
		{
			name:        "too many args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		},
		{
			name:        "deps flag rejected",
			args:        []string{"--deps", "az-3"},
			errContains: "flag provided but not defined: -deps",
		},
		{
			name: "named id",
			args: []string{"--id", "az-4"},
			want: IssueGetOptions{IssueID: "az-4", JSON: false},
		},
		{
			name:        "single-dash long flag rejected",
			args:        []string{"-id", "az-4"},
			errContains: "invalid flag \"-id\": use --id",
		},
		{
			name: "interspersed json flag after positional id",
			args: []string{"az-4", "--json"},
			want: IssueGetOptions{IssueID: "az-4", JSON: true},
		},
		{
			name:        "single-dash long interspersed flag rejected",
			args:        []string{"az-4", "-json"},
			errContains: "invalid flag \"-json\": use --json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueGetArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueGetArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueGetArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueCheckAndDoctorArgs(t *testing.T) {
	check, err := ParseIssueCheckArgs([]string{"az-1"})
	if err != nil {
		t.Fatalf("ParseIssueCheckArgs() error = %v", err)
	}
	if check.IssueID != "az-1" || check.JSON {
		t.Fatalf("ParseIssueCheckArgs() = %+v", check)
	}
	_, err = ParseIssueCheckArgs([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage: az issue check [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]") {
		t.Fatalf("expected check usage error, got %v", err)
	}

	doctor, err := ParseIssueDoctorArgs([]string{"az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() error = %v", err)
	}
	if doctor.IssueID != "az-2" {
		t.Fatalf("ParseIssueDoctorArgs() = %+v", doctor)
	}
	doctor, err = ParseIssueDoctorArgs([]string{"--id", "az-3"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() named id error = %v", err)
	}
	if doctor.IssueID != "az-3" {
		t.Fatalf("ParseIssueDoctorArgs() named id = %+v", doctor)
	}
	check, err = ParseIssueCheckArgs([]string{"az-1", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueCheckArgs() interspersed json error = %v", err)
	}
	if check.IssueID != "az-1" || !check.JSON {
		t.Fatalf("ParseIssueCheckArgs() interspersed json = %+v", check)
	}
	doctor, err = ParseIssueDoctorArgs([]string{"az-2", "--id", "az-3"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() interspersed named id error = %v", err)
	}
	if doctor.IssueID != "az-3" {
		t.Fatalf("ParseIssueDoctorArgs() interspersed named id = %+v", doctor)
	}
	_, err = ParseIssueDoctorArgs([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage: az issue doctor [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]") {
		t.Fatalf("expected doctor usage error, got %v", err)
	}
}

func TestParseIssueGetManyArgs(t *testing.T) {
	got, err := ParseIssueGetManyArgs([]string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueGetManyArgs() error = %v", err)
	}
	if !got.JSON {
		t.Fatalf("expected json output flag to be set")
	}
	if !reflect.DeepEqual(got.IssueIDs, []string{"az-1", "az-2", "az-3", "az-4"}) {
		t.Fatalf("ParseIssueGetManyArgs() ids = %+v", got.IssueIDs)
	}

	_, err = ParseIssueGetManyArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "unexpected argument: az-1") {
		t.Fatalf("expected positional arg rejection, got %v", err)
	}

	_, err = ParseIssueGetManyArgs([]string{"--json"})
	if err == nil || !strings.Contains(err.Error(), "usage: az issue get-many [--project <project-id>] --id <issue-id>") {
		t.Fatalf("expected usage error for missing ids, got %v", err)
	}
}

func TestParseIssueCreateArgs(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-parent")
	tests := []struct {
		name        string
		args        []string
		want        IssueCreateOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"Title"},
			want: IssueCreateOptions{
				Title:                 "Title",
				Type:                  domain.TypeTask,
				Priority:              domain.P2,
				AutoParentFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "explicit options",
			args: []string{"--impl", "go-bubbletea", "--type", "bug", "--priority", "P0", "--description", "details", "Title"},
			want: IssueCreateOptions{
				Title:                 "Title",
				Description:           "details",
				Type:                  domain.TypeBug,
				Priority:              domain.P0,
				PriorityExplicit:      true,
				Implementations:       []string{"go-bubbletea"},
				AutoParentFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "interspersed flags after title",
			args: []string{"Title", "--impl", "go-bubbletea", "--priority", "P1"},
			want: IssueCreateOptions{
				Title:                 "Title",
				Type:                  domain.TypeTask,
				Priority:              domain.P1,
				PriorityExplicit:      true,
				Implementations:       []string{"go-bubbletea"},
				AutoParentFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "deferred defaults priority",
			args: []string{"--deferred", "Title"},
			want: IssueCreateOptions{
				Title:    "Title",
				Type:     domain.TypeTask,
				Priority: domain.P4,
				Deferred: true,
			},
		},
		{
			name:        "invalid priority",
			args:        []string{"--priority", "high", "Title"},
			errContains: "invalid priority: high",
		},
		{
			name:        "missing title",
			args:        []string{},
			errContains: "usage: az issue create [--project <project-id>] [--impl <implementation> ...] [--deferred]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueCreateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueCreateArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseIssueCreateArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueCloseArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueCloseOptions
		errContains string
	}{
		{
			name: "valid",
			args: []string{"az-1"},
			want: IssueCloseOptions{IssueID: "az-1"},
		},
		{
			name:        "forbid impl",
			args:        []string{"--impl", "go-bubbletea", "az-1"},
			errContains: "--impl is not supported for issue close",
		},
		{
			name:        "missing id",
			args:        []string{},
			errContains: "usage: az issue close [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		},
		{
			name:        "extra args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue close [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
		},
		{
			name: "interspersed named id overrides positional",
			args: []string{"az-1", "--id", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueCloseArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueCloseArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueCloseArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueDeleteArgs(t *testing.T) {
	got, err := ParseIssueDeleteArgs([]string{"--confirm", "az-1"})
	if err != nil {
		t.Fatalf("ParseIssueDeleteArgs() error = %v", err)
	}
	if got.IssueID != "az-1" || !got.Confirm {
		t.Fatalf("ParseIssueDeleteArgs() = %+v", got)
	}
	_, err = ParseIssueDeleteArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --confirm") {
		t.Fatalf("expected missing confirm error, got %v", err)
	}
	_, err = ParseIssueDeleteArgs([]string{"--impl", "go-bubbletea", "--confirm", "az-1"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue delete") {
		t.Fatalf("expected impl forbidden error, got %v", err)
	}
	got, err = ParseIssueDeleteArgs([]string{"az-1", "--confirm"})
	if err != nil {
		t.Fatalf("ParseIssueDeleteArgs() interspersed confirm error = %v", err)
	}
	if got.IssueID != "az-1" || !got.Confirm {
		t.Fatalf("ParseIssueDeleteArgs() interspersed confirm = %+v", got)
	}
}

func TestParseIssueUpdateArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueUpdateOptions
		errContains string
	}{
		{
			name: "update title",
			args: []string{"--title", "Renamed", "az-1"},
			want: IssueUpdateOptions{
				IssueID: "az-1",
				Title:   "Renamed",
			},
		},
		{
			name: "update type and priority",
			args: []string{"--type", "epic", "--priority", "P0", "az-1"},
			want: func() IssueUpdateOptions {
				tt := domain.TypeEpic
				p := domain.P0
				return IssueUpdateOptions{
					IssueID:  "az-1",
					Type:     &tt,
					Priority: &p,
				}
			}(),
		},
		{
			name: "append notes",
			args: []string{"--append-notes", "Follow-up", "az-1"},
			want: IssueUpdateOptions{
				IssueID:     "az-1",
				AppendNotes: "Follow-up",
			},
		},
		{
			name:        "forbid impl",
			args:        []string{"--impl", "go-bubbletea", "--title", "Renamed", "az-1"},
			errContains: "--impl is not supported for issue update",
		},
		{
			name:        "no update fields",
			args:        []string{"az-1"},
			errContains: "no update fields provided",
		},
		{
			name:        "invalid status arg count",
			args:        []string{},
			errContains: "usage: az issue update [--project <project-id>] [--id <issue-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-9", "--title", "Renamed"},
			want: IssueUpdateOptions{
				IssueID: "az-9",
				Title:   "Renamed",
			},
		},
		{
			name: "interspersed positional then status flag",
			args: []string{"az-1", "--status", "blocked"},
			want: func() IssueUpdateOptions {
				status := domain.StatusBlocked
				return IssueUpdateOptions{
					IssueID: "az-1",
					Status:  &status,
				}
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueUpdateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueUpdateArgs() error = %v", err)
			}
			if got.IssueID != tt.want.IssueID || got.Title != tt.want.Title || got.Description != tt.want.Description || got.AppendNotes != tt.want.AppendNotes {
				t.Fatalf("ParseIssueUpdateArgs() = %+v, want %+v", got, tt.want)
			}
			if (got.Type == nil) != (tt.want.Type == nil) {
				t.Fatalf("type presence mismatch: got=%v want=%v", got.Type, tt.want.Type)
			}
			if got.Type != nil && *got.Type != *tt.want.Type {
				t.Fatalf("type mismatch: got=%v want=%v", *got.Type, *tt.want.Type)
			}
			if (got.Priority == nil) != (tt.want.Priority == nil) {
				t.Fatalf("priority presence mismatch: got=%v want=%v", got.Priority, tt.want.Priority)
			}
			if got.Priority != nil && *got.Priority != *tt.want.Priority {
				t.Fatalf("priority mismatch: got=%v want=%v", *got.Priority, *tt.want.Priority)
			}
		})
	}
}

func TestParseIssueDependencyArgs(t *testing.T) {
	add, err := ParseIssueDependencyAddArgs([]string{"--type", "related", "az-1", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" || add.Type != "related" {
		t.Fatalf("ParseIssueDependencyAddArgs() = %+v", add)
	}
	_, err = ParseIssueDependencyAddArgs([]string{"--impl", "go-bubbletea", "az-1", "az-2"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep add") {
		t.Fatalf("expected impl forbidden error for add, got %v", err)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"--issue-id", "az-1", "--depends-on-id", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() named flags error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" {
		t.Fatalf("ParseIssueDependencyAddArgs() named flags = %+v", add)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"az-1", "--depends-on-id", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() interspersed id+flag error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" {
		t.Fatalf("ParseIssueDependencyAddArgs() interspersed id+flag = %+v", add)
	}

	remove, err := ParseIssueDependencyRemoveArgs([]string{"--type", "blocks", "--confirm", "az-3", "az-4"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" || remove.Type != "blocks" || !remove.Confirm {
		t.Fatalf("ParseIssueDependencyRemoveArgs() = %+v", remove)
	}
	_, err = ParseIssueDependencyRemoveArgs([]string{"--impl", "go-bubbletea", "az-3"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep remove") {
		t.Fatalf("expected impl forbidden error for remove, got %v", err)
	}
	remove, err = ParseIssueDependencyRemoveArgs([]string{"--issue-id", "az-3", "--depends-on-id", "az-4"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() named flags error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" {
		t.Fatalf("ParseIssueDependencyRemoveArgs() named flags = %+v", remove)
	}
	remove, err = ParseIssueDependencyRemoveArgs([]string{"az-3", "--depends-on-id", "az-4", "--confirm"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() interspersed id+flags error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" || !remove.Confirm {
		t.Fatalf("ParseIssueDependencyRemoveArgs() interspersed id+flags = %+v", remove)
	}
}

func TestParseIssueBulkArgs(t *testing.T) {
	create, err := ParseIssueBulkCreateArgs([]string{"--impl", "go-bubbletea", "--input", "bulk-create.json", "--dry-run"})
	if err != nil {
		t.Fatalf("ParseIssueBulkCreateArgs() error = %v", err)
	}
	if create.Implementation != "go-bubbletea" || create.InputPath != "bulk-create.json" || !create.DryRun {
		t.Fatalf("ParseIssueBulkCreateArgs() = %+v", create)
	}
	createNoImpl, err := ParseIssueBulkCreateArgs([]string{"--input", "bulk-create.json"})
	if err != nil {
		t.Fatalf("ParseIssueBulkCreateArgs() missing impl should parse, got %v", err)
	}
	if createNoImpl.Implementation != "" || createNoImpl.InputPath != "bulk-create.json" || createNoImpl.DryRun {
		t.Fatalf("ParseIssueBulkCreateArgs() missing impl = %+v", createNoImpl)
	}

	update, err := ParseIssueBulkUpdateArgs([]string{"--impl", "go-bubbletea", "--input", "bulk-update.json"})
	if err != nil {
		t.Fatalf("ParseIssueBulkUpdateArgs() error = %v", err)
	}
	if update.Implementation != "go-bubbletea" || update.InputPath != "bulk-update.json" || update.DryRun {
		t.Fatalf("ParseIssueBulkUpdateArgs() = %+v", update)
	}
	updateNoImpl, err := ParseIssueBulkUpdateArgs([]string{"--input", "bulk-update.json"})
	if err != nil {
		t.Fatalf("ParseIssueBulkUpdateArgs() missing impl should parse, got %v", err)
	}
	if updateNoImpl.Implementation != "" || updateNoImpl.InputPath != "bulk-update.json" || updateNoImpl.DryRun {
		t.Fatalf("ParseIssueBulkUpdateArgs() missing impl = %+v", updateNoImpl)
	}
	_, err = ParseIssueBulkUpdateArgs([]string{"--impl", "go-bubbletea"})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --input") {
		t.Fatalf("expected missing input error for bulk-update, got %v", err)
	}

	depBulk, err := ParseIssueDependencyBulkApplyArgs([]string{"--input", "dep-bulk.json", "--dry-run", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyBulkApplyArgs() error = %v", err)
	}
	if depBulk.InputPath != "dep-bulk.json" || !depBulk.DryRun || !depBulk.JSON {
		t.Fatalf("ParseIssueDependencyBulkApplyArgs() = %+v", depBulk)
	}
	_, err = ParseIssueDependencyBulkApplyArgs([]string{"--impl", "go-bubbletea", "--input", "dep-bulk.json"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep bulk apply") {
		t.Fatalf("expected impl forbidden error for dep bulk apply, got %v", err)
	}
}

func TestResolveIssueWriteImplementation(t *testing.T) {
	t.Run("explicit implementation wins", func(t *testing.T) {
		impl, err := resolveIssueWriteImplementation(context.Background(), nil, "go-bubbletea")
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementation() error = %v", err)
		}
		if impl != "go-bubbletea" {
			t.Fatalf("resolveIssueWriteImplementation() = %q, want go-bubbletea", impl)
		}
	})

	t.Run("defaults to configured single implementation", func(t *testing.T) {
		deps := &Dependencies{
			Config: config.DefaultConfig(),
			DaemonClient: daemonclient.New(&fakeDaemonTransport{
				commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					if req.Command != daemonclient.CommandTaskList {
						t.Fatalf("unexpected command %q", req.Command)
					}
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"go-bubbletea"}},
					})
					if err != nil {
						t.Fatalf("marshal tasks: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				},
			}),
		}
		impl, err := resolveIssueWriteImplementation(context.Background(), deps, "")
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementation() error = %v", err)
		}
		if impl != "go-bubbletea" {
			t.Fatalf("resolveIssueWriteImplementation() = %q, want go-bubbletea", impl)
		}
	})

	t.Run("falls back to default when no implementations are configured", func(t *testing.T) {
		deps := &Dependencies{
			Config: config.DefaultConfig(),
			DaemonClient: daemonclient.New(&fakeDaemonTransport{
				commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					if req.Command != daemonclient.CommandTaskList {
						t.Fatalf("unexpected command %q", req.Command)
					}
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1"},
					})
					if err != nil {
						t.Fatalf("marshal tasks: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				},
			}),
		}
		impl, err := resolveIssueWriteImplementation(context.Background(), deps, "")
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementation() error = %v", err)
		}
		if impl != "default" {
			t.Fatalf("resolveIssueWriteImplementation() = %q, want default", impl)
		}
	})

	t.Run("requires explicit selection when multiple implementations are configured", func(t *testing.T) {
		deps := &Dependencies{
			Config: config.DefaultConfig(),
			DaemonClient: daemonclient.New(&fakeDaemonTransport{
				commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					if req.Command != daemonclient.CommandTaskList {
						t.Fatalf("unexpected command %q", req.Command)
					}
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"go-bubbletea"}},
						{ID: "az-2", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal tasks: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				},
			}),
		}
		_, err := resolveIssueWriteImplementation(context.Background(), deps, "")
		if err == nil || !strings.Contains(err.Error(), "missing required flag: --impl (multiple implementations configured: default, go-bubbletea)") {
			t.Fatalf("expected multi-implementation error, got %v", err)
		}
	})
}

func TestIssueListCommandUsesDaemonTaskList(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-2",
			Title:     "Older issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "az-1",
			Title:     "Newest issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeFeature,
			CreatedAt: now,
			UpdatedAt: now.Add(1 * time.Hour),
		},
	}

	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{})
	})

	if gotReq.Command != daemonclient.CommandTaskList {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandTaskList)
	}
	if !strings.Contains(output, "ID") || !strings.Contains(output, "STATUS") || !strings.Contains(output, "PRIORITY") || !strings.Contains(output, "TITLE") {
		t.Fatalf("output missing header: %q", output)
	}
	if first, second := strings.Index(output, "az-1"), strings.Index(output, "az-2"); !(first >= 0 && second > first) {
		t.Fatalf("expected newest issue first in output: %q", output)
	}
}

func TestIssueListCommandDepsProjection(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:       "az-1",
			Title:    "Dependent issue",
			Status:   domain.StatusInProgress,
			Priority: domain.P1,
			Type:     domain.TypeFeature,
			Dependencies: []domain.Dependency{
				{ID: "az-2", Type: domain.DependencyBlocks},
				{ID: "az-3", Type: domain.DependencyBlockedBy},
				{ID: "az-4", Type: domain.DependencyBlockedBy},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Deps: true})
	})
	if !strings.Contains(output, "DEPS") {
		t.Fatalf("deps output missing DEPS column: %q", output)
	}
	if !strings.Contains(output, "blocks:1,blocked-by:2") {
		t.Fatalf("deps output missing summary: %q", output)
	}
}

func TestIssueListCommand_IncludesListWindowMetadata(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-1",
			Title:     "Newest issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeFeature,
			CreatedAt: now,
			UpdatedAt: now.Add(1 * time.Hour),
		},
		{
			ID:        "az-2",
			Title:     "Older issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Limit: 1})
	})
	if !strings.Contains(output, "List window: listed=1 limit=1 total=2 truncated=yes") {
		t.Fatalf("list metadata missing expected window summary: %q", output)
	}
	if !strings.Contains(output, "Window note: additional matching issues may exist beyond current limit.") {
		t.Fatalf("list metadata missing truncated window note: %q", output)
	}
}

func TestIssueListCommand_IDSetFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
		{ID: "az-2", Title: "Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
		{ID: "az-3", Title: "Three", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{IDs: []string{"az-3", "az-1"}})
	})
	if strings.Contains(output, "az-2") {
		t.Fatalf("id-set filter should exclude az-2: %q", output)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "az-3") {
		t.Fatalf("id-set filter should include requested issues: %q", output)
	}
}

func TestIssueListCommand_StatusFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "Open", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
		{ID: "az-2", Title: "Blocked", Status: domain.StatusBlocked, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
		{ID: "az-3", Title: "Closed", Status: domain.StatusDone, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{States: []domain.Status{domain.StatusOpen, domain.StatusBlocked}})
	})
	if strings.Contains(output, "az-3") {
		t.Fatalf("status filter should exclude az-3: %q", output)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "az-2") {
		t.Fatalf("status filter should include matching issues: %q", output)
	}
}

func TestIssueListCommandDepsProjection_IncludesTopLevelGraphContext(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 10, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")
	tasks := []domain.Task{
		{
			ID:        parentID,
			Title:     "Parent issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeEpic,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        naming.IssueID("az-child"),
			Title:     "Child issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &parentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:       naming.IssueID("az-dependent"),
			Title:    "Depends on parent via blocks",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: parentID, Type: domain.DependencyBlocks},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Deps: true, Limit: 10})
	})
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Top-level issues:") {
		t.Fatalf("deps output should start with top-level context, got: %q", output)
	}
	for _, want := range []string{
		"Top-level issues:",
		"Dependency links (listed issues):",
		"- az-child -> az-parent (parent-child)",
		"- az-dependent -> az-parent (blocks)",
		"List window: listed=3 limit=10 total=3 truncated=no",
		"Window note: all matching issues are shown.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps graph context missing %q: %q", want, output)
		}
	}
}

func TestIssueGetCommandJSON(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Status:      domain.StatusBlocked,
			Priority:    domain.P0,
			Type:        domain.TypeBug,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5", JSON: true})
	})
	if !strings.Contains(output, "\"id\": \"az-5\"") || !strings.Contains(output, "\"title\": \"Lookup issue\"") {
		t.Fatalf("output missing issue json fields: %q", output)
	}
}

func TestIssueGetCommandTextIncludesNotes(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Notes:       "First note line\nSecond note line",
			Status:      domain.StatusBlocked,
			Priority:    domain.P0,
			Type:        domain.TypeBug,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5"})
	})
	if !strings.Contains(output, "Notes:\nFirst note line\nSecond note line\n") {
		t.Fatalf("output missing notes section: %q", output)
	}
}

func TestIssueGetCommandNotFound(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody([]domain.Task{})
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        1,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueGetCommand(deps, IssueGetOptions{IssueID: "az-missing"})
	if err == nil || !strings.Contains(err.Error(), "issue not found: az-missing") {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestIssueGetCommandDepsProjection(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-8",
			Title:       "Dependency detail",
			Description: "Detail context",
			Status:      domain.StatusOpen,
			Priority:    domain.P2,
			Type:        domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: "az-2", Type: domain.DependencyBlocks},
				{ID: "az-5", Type: domain.DependencyRelatedTo},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-8"})
	})
	if !strings.Contains(output, "Dependency edges:") {
		t.Fatalf("deps output missing dependency section: %q", output)
	}
	if !strings.Contains(output, "- az-2 (blocks, status=unknown)") || !strings.Contains(output, "- az-5 (related, status=unknown)") {
		t.Fatalf("deps output missing dependency rows: %q", output)
	}
}

func TestIssueGetCommandDepsProjectionCanonicalTypes(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-9",
			Title:       "Dependency matrix",
			Description: "Ensure deps output labels are canonical",
			Status:      domain.StatusOpen,
			Priority:    domain.P2,
			Type:        domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: "az-a", Type: domain.DependencyBlocks},
				{ID: "az-b", Type: domain.DependencyParentChild},
				{ID: "az-c", Type: domain.DependencyRelatedTo},
				{ID: "az-d", Type: domain.DependencyDiscovered},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        5,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-9"})
	})
	if !strings.Contains(output, "Dependency edges:") {
		t.Fatalf("deps output missing dependency section: %q", output)
	}
	for _, want := range []string{
		"- az-a (blocks, status=unknown)",
		"- az-b (parent-child, status=unknown)",
		"- az-c (related, status=unknown)",
		"- az-d (discovered-from, status=unknown)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps output missing %q: %q", want, output)
		}
	}
}

func TestIssueGetCommandDepsProjectionIncludesDependentsAndParentEdge(t *testing.T) {
	now := time.Date(2026, 3, 26, 1, 15, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")
	targetID := naming.IssueID("az-target")
	childParentID := targetID
	tasks := []domain.Task{
		{
			ID:        parentID,
			Title:     "Parent issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeEpic,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        targetID,
			Title:     "Target issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &parentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        naming.IssueID("az-child"),
			Title:     "Child issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &childParentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        6,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: targetID.String()})
	})
	for _, want := range []string{
		"Dependency edges:",
		"- az-parent (parent-child, status=open)",
		"Dependents:",
		"- az-child (parent-child, status=open)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps projection missing %q: %q", want, output)
		}
	}
}

func TestIssueGetManyCommand_JSONStableOrderWithPartialMisses(t *testing.T) {
	now := time.Date(2026, 3, 26, 3, 15, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "First", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "az-2", Title: "Second", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeFeature, CreatedAt: now, UpdatedAt: now},
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetManyCommand(deps, IssueGetManyOptions{
			IssueIDs: []string{"az-2", "az-missing", "az-1"},
			JSON:     true,
		})
	})
	var got issueGetManyResult
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal get-many output: %v", err)
	}
	if got.Requested != 3 || got.Found != 2 || got.Missing != 1 {
		t.Fatalf("unexpected summary: %+v", got)
	}
	if len(got.Results) != 3 {
		t.Fatalf("results length = %d, want 3", len(got.Results))
	}
	if got.Results[0].ID != "az-2" || got.Results[0].Status != "found" {
		t.Fatalf("result[0] = %+v", got.Results[0])
	}
	if got.Results[1].ID != "az-missing" || got.Results[1].Status != "not_found" {
		t.Fatalf("result[1] = %+v", got.Results[1])
	}
	if got.Results[2].ID != "az-1" || got.Results[2].Status != "found" {
		t.Fatalf("result[2] = %+v", got.Results[2])
	}
}

func TestIssueDependencyBulkApplyCommand_DryRunOutcomes(t *testing.T) {
	now := time.Date(2026, 3, 26, 4, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-a",
			Title:     "A",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []domain.Dependency{
				{ID: "az-b", Type: domain.DependencyBlocks},
			},
		},
		{ID: "az-b", Title: "B", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "az-c", Title: "C", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "dep-bulk.json")
	payload := `{
  "mutations": [
    {"action":"add","issue_id":"az-a","depends_on_id":"az-b","type":"blocks"},
    {"action":"add","issue_id":"az-a","depends_on_id":"az-c","type":"blocks"},
    {"action":"remove","issue_id":"az-a","depends_on_id":"az-z","type":"blocks"},
    {"action":"retarget","issue_id":"az-a","from_id":"az-b","to_id":"az-c","type":"blocks"},
    {"action":"add","issue_id":"az-missing","depends_on_id":"az-b","type":"blocks"}
  ]
}`
	if err := os.WriteFile(inputPath, []byte(payload), 0644); err != nil {
		t.Fatalf("write dep bulk file: %v", err)
	}

	output := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			DryRun:    true,
			JSON:      true,
		})
	})
	var result dependencyBulkResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("unmarshal dry-run output: %v", err)
	}
	if result.Summary.Requested != 5 || result.Summary.Planned != 2 || result.Summary.NoOp != 2 || result.Summary.Invalid != 1 {
		t.Fatalf("unexpected dry-run summary: %+v", result.Summary)
	}
	statuses := make([]string, 0, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		statuses = append(statuses, outcome.Status)
	}
	if !reflect.DeepEqual(statuses, []string{"no-op", "planned", "no-op", "planned", "invalid"}) {
		t.Fatalf("unexpected dry-run statuses: %+v", statuses)
	}
}

func TestIssueDependencyBulkApplyCommand_IdempotentReplayNoOp(t *testing.T) {
	now := time.Date(2026, 3, 26, 4, 45, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-a",
			Title:     "A",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{ID: "az-b", Title: "B", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	applyCalls := 0
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal tasks: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        5,
						Body:            body,
					}, nil
				case protocol.CommandTaskBulkApply:
					applyCalls++
					tasks[0].Dependencies = append(tasks[0].Dependencies, domain.Dependency{
						ID:   "az-b",
						Type: domain.DependencyBlocks,
					})
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        6,
						Body:            []byte(`{}`),
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "dep-bulk-apply.json")
	payload := `{"mutations":[{"action":"add","issue_id":"az-a","depends_on_id":"az-b","type":"blocks"}]}`
	if err := os.WriteFile(inputPath, []byte(payload), 0644); err != nil {
		t.Fatalf("write dep bulk file: %v", err)
	}

	first := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			JSON:      true,
		})
	})
	var firstResult dependencyBulkResult
	firstJSON := extractTrailingJSONResult(first)
	if err := json.Unmarshal([]byte(firstJSON), &firstResult); err != nil {
		t.Fatalf("unmarshal first result: %v", err)
	}
	if firstResult.Summary.Planned != 1 || firstResult.Summary.Applied != 1 || applyCalls != 1 {
		t.Fatalf("unexpected first apply result=%+v calls=%d", firstResult.Summary, applyCalls)
	}

	second := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			JSON:      true,
		})
	})
	var secondResult dependencyBulkResult
	secondJSON := extractTrailingJSONResult(second)
	if err := json.Unmarshal([]byte(secondJSON), &secondResult); err != nil {
		t.Fatalf("unmarshal second result: %v", err)
	}
	if secondResult.Summary.NoOp != 1 || secondResult.Summary.Applied != 0 || applyCalls != 1 {
		t.Fatalf("unexpected second apply result=%+v calls=%d", secondResult.Summary, applyCalls)
	}
}

func extractTrailingJSONResult(output string) string {
	needle := "{\n  \"dry_run\""
	idx := strings.LastIndex(output, needle)
	if idx < 0 {
		return output
	}
	return output[idx:]
}

func TestIssueCreateAndCloseCommandsUseDaemonTaskCommands(t *testing.T) {
	tests := []struct {
		name        string
		run         func(*Dependencies) error
		wantCommand string
		wantText    string
	}{
		{
			name: "create",
			run: func(deps *Dependencies) error {
				return IssueCreateCommand(deps, IssueCreateOptions{
					Implementations: []string{"go-bubbletea"},
					Title:           "New issue",
					Description:     "Context",
					Type:            domain.TypeFeature,
					Priority:        domain.P1,
				})
			},
			wantCommand: daemonclient.CommandTaskCreate,
			wantText:    "Created issue: az-42",
		},
		{
			name: "close",
			run: func(deps *Dependencies) error {
				return IssueCloseCommand(deps, IssueCloseOptions{
					IssueID: "az-9",
				})
			},
			wantCommand: daemonclient.CommandTaskUpdateStatus,
			wantText:    "Closed issue: az-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						gotReq = req
						body := []byte{}
						if req.Command == daemonclient.CommandTaskCreate {
							payload, err := json.Marshal(map[string]string{"task_id": "az-42"})
							if err != nil {
								t.Fatalf("marshal task create response: %v", err)
							}
							body = payload
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							Meta:            req.Meta,
							OK:              true,
							CompletedAt:     req.SentAt,
							Body:            body,
						}, nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			output := captureStdout(t, func() error {
				return tt.run(deps)
			})
			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			switch tt.name {
			case "create":
				var createReq daemonclient.TaskCreateParams
				if err := json.Unmarshal(gotReq.Body, &createReq); err != nil {
					t.Fatalf("unmarshal create body: %v", err)
				}
				if createReq.Title != "New issue" || createReq.Description != "Context" || createReq.Type != domain.TypeFeature || createReq.Priority != domain.P1 {
					t.Fatalf("create body = %+v", createReq)
				}
				if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
					t.Fatalf("create implementations = %+v, want [go-bubbletea]", createReq.Implementations)
				}
			case "close":
				var statusReq daemonclient.TaskStatusRequest
				if err := json.Unmarshal(gotReq.Body, &statusReq); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if statusReq.TaskID != "az-9" || statusReq.Status != domain.StatusDone {
					t.Fatalf("close body = %+v, want task_id=az-9 status=closed", statusReq)
				}
			}
			if !strings.Contains(output, tt.wantText) {
				t.Fatalf("output missing %q: %q", tt.wantText, output)
			}
		})
	}
}

func TestIssueCreateCommandAutoParentsAndInheritsImplsFromActiveIssue(t *testing.T) {
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				body := []byte{}
				switch req.Command {
				case daemonclient.CommandTaskList:
					tasks, err := marshalTaskListBody([]domain.Task{
						{
							ID:              "az-parent",
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"go-bubbletea"},
						},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					body = tasks
				case daemonclient.CommandTaskCreate:
					payload, err := json.Marshal(map[string]string{"task_id": "az-child"})
					if err != nil {
						t.Fatalf("marshal task create response: %v", err)
					}
					body = payload
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		parentID := "az-parent"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:                 "Child issue",
			Type:                  domain.TypeTask,
			Priority:              domain.P2,
			AutoParentFromIssueID: &parentID,
		})
	})

	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskList {
		t.Fatalf("requests[0].Command = %q, want %q", requests[0].Command, daemonclient.CommandTaskList)
	}
	if requests[1].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("requests[1].Command = %q, want %q", requests[1].Command, daemonclient.CommandTaskCreate)
	}

	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[1].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID == nil || *createReq.ParentID != "az-parent" {
		t.Fatalf("create parent = %+v, want az-parent", createReq.ParentID)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("create implementations = %+v, want [go-bubbletea]", createReq.Implementations)
	}
	if createReq.Title != "Child issue" || createReq.Type != domain.TypeTask {
		t.Fatalf("create body = %+v", createReq)
	}
	if createReq.Priority != domain.P2 {
		t.Fatalf("create priority = %s, want P2", createReq.Priority.String())
	}
	if !strings.Contains(output, "Created issue: az-child (parent: az-parent, auto-parent from AZEDARACH_ISSUE_ID)") {
		t.Fatalf("output missing auto-parent message: %q", output)
	}
}

func TestIssueCreateCommandDeferredIgnoresAutoParentFromIssueID(t *testing.T) {
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				if req.Command == daemonclient.CommandTaskList {
					t.Fatalf("unexpected %s request for deferred issue", daemonclient.CommandTaskList)
				}
				payload, err := json.Marshal(map[string]string{"task_id": "az-child"})
				if err != nil {
					t.Fatalf("marshal task create response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		parentID := "az-parent"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:                 "Child issue",
			Type:                  domain.TypeTask,
			Priority:              domain.P4,
			Deferred:              true,
			AutoParentFromIssueID: &parentID,
			Implementations:       []string{"default"},
		})
	})

	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("requests[0].Command = %q, want %q", requests[0].Command, daemonclient.CommandTaskCreate)
	}

	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[0].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID != nil {
		t.Fatalf("create parent = %+v, want nil", createReq.ParentID)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"default"}) {
		t.Fatalf("create implementations = %+v, want [default]", createReq.Implementations)
	}
	if !strings.Contains(output, "Created issue: az-child [deferred]") {
		t.Fatalf("output missing deferred message: %q", output)
	}
}

func TestIssueCreateCommandAutoDefaultsImplWhenSingleConfigured(t *testing.T) {
	var createReq daemonclient.TaskCreateParams
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"go-bubbletea"}},
					})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createReq); err != nil {
						t.Fatalf("unmarshal create request: %v", err)
					}
					body, err := json.Marshal(map[string]string{"task_id": "az-55"})
					if err != nil {
						t.Fatalf("marshal create response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:       "Auto impl",
		Description: "details",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	if err != nil {
		t.Fatalf("IssueCreateCommand() error = %v", err)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("create implementations = %+v, want [go-bubbletea]", createReq.Implementations)
	}
}

func TestIssueCreateCommandRequiresImplWhenMultipleConfigured(t *testing.T) {
	createCalled := false
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"go-bubbletea"}},
						{ID: "az-2", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskCreate:
					createCalled = true
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:       "Needs impl",
		Description: "details",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --impl (multiple implementations configured: default, go-bubbletea)") {
		t.Fatalf("expected multi-implementation error, got %v", err)
	}
	if createCalled {
		t.Fatalf("task.create should not be called when implementation selection is ambiguous")
	}
}

func TestIssueCheckDoctorAndDeleteCommandsUseDaemonTaskCommands(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotDeleteReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Check target",
							Description: "Desc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskDelete:
					gotDeleteReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	checkOut := captureStdout(t, func() error {
		return IssueCheckCommand(deps, IssueCheckOptions{IssueID: "az-1"})
	})
	if !strings.Contains(checkOut, "ID: az-1") {
		t.Fatalf("check output = %q", checkOut)
	}

	doctorOut := captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(doctorOut, "Doctor: OK az-1") {
		t.Fatalf("doctor output = %q", doctorOut)
	}

	deleteOut := captureStdout(t, func() error {
		return IssueDeleteCommand(deps, IssueDeleteOptions{
			IssueID: "az-1",
			Confirm: true,
		})
	})
	if gotDeleteReq.Command != daemonclient.CommandTaskDelete {
		t.Fatalf("delete command = %q, want %q", gotDeleteReq.Command, daemonclient.CommandTaskDelete)
	}
	if !strings.Contains(deleteOut, "Deleted issue: az-1") {
		t.Fatalf("delete output = %q", deleteOut)
	}
}

func TestIssueDeleteCommandBlocksWhenRuntimeAttachmentsPresent(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	deleteCalled := false
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:             "az-1",
							Title:          "Delete target",
							Type:           domain.TypeTask,
							Priority:       domain.P2,
							Status:         domain.StatusOpen,
							HasTmuxSession: true,
							HasWorktree:    true,
							CreatedAt:      now,
							UpdatedAt:      now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskDelete:
					deleteCalled = true
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueDeleteCommand(deps, IssueDeleteOptions{
		IssueID: "az-1",
		Confirm: true,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot delete issue az-1: active runtime attachments detected (session, worktree)") {
		t.Fatalf("IssueDeleteCommand() error = %v", err)
	}
	if deleteCalled {
		t.Fatal("IssueDeleteCommand() called task.delete despite runtime attachments")
	}
}

func TestIssueUpdateCommandUsesDaemonTaskUpdateCommand(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdate:
					gotUpdateReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID: "az-1",
			Title:   "New",
		})
	})
	if gotUpdateReq.Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update command = %q, want %q", gotUpdateReq.Command, daemonclient.CommandTaskUpdate)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueUpdateCommandAppendsNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotAppendReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskAppendNotes:
					gotAppendReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID:     "az-1",
			AppendNotes: "Follow-up detail",
		})
	})
	if gotAppendReq.Command != daemonclient.CommandTaskAppendNotes {
		t.Fatalf("append command = %q, want %q", gotAppendReq.Command, daemonclient.CommandTaskAppendNotes)
	}
	var appendBody daemonclient.TaskAppendNotesRequest
	if err := json.Unmarshal(gotAppendReq.Body, &appendBody); err != nil {
		t.Fatalf("unmarshal append body: %v", err)
	}
	if appendBody.TaskID != "az-1" || appendBody.Line != "Follow-up detail" {
		t.Fatalf("append body = %+v", appendBody)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueDependencyCommandsUseDaemonTaskCommands(t *testing.T) {
	var gotAddReq protocol.RequestEnvelope
	var gotRemoveReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskDependencyAdd:
					gotAddReq = req
				case daemonclient.CommandTaskDependencyRemove:
					gotRemoveReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	addOut := captureStdout(t, func() error {
		return IssueDependencyAddCommand(deps, IssueDependencyAddOptions{
			IssueID:     "az-5",
			DependsOnID: "az-2",
			Type:        "blocks",
		})
	})
	if gotAddReq.Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("add command = %q, want %q", gotAddReq.Command, daemonclient.CommandTaskDependencyAdd)
	}
	var addBody daemonclient.TaskDependencyParams
	if err := json.Unmarshal(gotAddReq.Body, &addBody); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if addBody.TaskID != "az-5" || addBody.DependsOnID != "az-2" || addBody.Type != "blocks" {
		t.Fatalf("add body = %+v", addBody)
	}
	if !strings.Contains(addOut, "Added dependency: az-5 --(blocks)--> az-2") {
		t.Fatalf("add output = %q", addOut)
	}

	removeOut := captureStdout(t, func() error {
		return IssueDependencyRemoveCommand(deps, IssueDependencyRemoveOptions{
			IssueID:     "az-5",
			DependsOnID: "az-2",
			Type:        "blocks",
			Confirm:     true,
		})
	})
	if gotRemoveReq.Command != daemonclient.CommandTaskDependencyRemove {
		t.Fatalf("remove command = %q, want %q", gotRemoveReq.Command, daemonclient.CommandTaskDependencyRemove)
	}
	var removeBody daemonclient.TaskDependencyRemoveParams
	if err := json.Unmarshal(gotRemoveReq.Body, &removeBody); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if removeBody.TaskID != "az-5" || removeBody.DependsOnID != "az-2" || removeBody.Type != "blocks" || !removeBody.Confirm {
		t.Fatalf("remove body = %+v", removeBody)
	}
	if !strings.Contains(removeOut, "Removed dependency: az-5 --(blocks)--> az-2") {
		t.Fatalf("remove output = %q", removeOut)
	}
}

func TestIssueBulkCommandsUseApplyCommand(t *testing.T) {
	tempDir := t.TempDir()
	bulkCreatePath := filepath.Join(tempDir, "bulk-create.json")
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update.json")
	if err := os.WriteFile(bulkCreatePath, []byte(`[{"title":"Bulk one","description":"Desc","type":"task","priority":"P2"}]`), 0o644); err != nil {
		t.Fatalf("write bulk-create file: %v", err)
	}
	if err := os.WriteFile(bulkUpdatePath, []byte(`[{"task_id":"az-1","title":"Renamed"}]`), 0o644); err != nil {
		t.Fatalf("write bulk-update file: %v", err)
	}

	var commands []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req)
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:          "az-1",
						Title:       "Old",
						Description: "Desc",
						Type:        domain.TypeTask,
						Priority:    domain.P2,
						Status:      domain.StatusOpen,
					}})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case protocol.CommandTaskBulkApply:
					body, err := json.Marshal(applyExecutionResultBody{
						Summary: applyExecutionSummaryBody{Total: 1, Succeeded: 1, Failed: 0},
					})
					if err != nil {
						t.Fatalf("marshal apply response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	_ = captureStdout(t, func() error {
		return IssueBulkCreateCommand(deps, IssueBulkCreateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkCreatePath,
			DryRun:         false,
		})
	})
	_ = captureStdout(t, func() error {
		return IssueBulkUpdateCommand(deps, IssueBulkUpdateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkUpdatePath,
			DryRun:         true,
		})
	})

	applyReqs := make([]protocol.RequestEnvelope, 0, 2)
	for _, req := range commands {
		if req.Command == protocol.CommandTaskBulkApply {
			applyReqs = append(applyReqs, req)
		}
	}
	if len(applyReqs) != 2 {
		t.Fatalf("bulk apply command count = %d, want 2", len(applyReqs))
	}
	var createBody protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReqs[0].Body, &createBody); err != nil {
		t.Fatalf("unmarshal create apply body: %v", err)
	}
	if createBody.DryRun {
		t.Fatalf("create body dry_run = true, want false")
	}
	if len(createBody.Operations) != 1 || createBody.Operations[0].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("create operations = %+v", createBody.Operations)
	}
	var updateBody protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReqs[1].Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update apply body: %v", err)
	}
	if !updateBody.DryRun {
		t.Fatalf("update body dry_run = false, want true")
	}
	if len(updateBody.Operations) != 1 || updateBody.Operations[0].Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update operations = %+v", updateBody.Operations)
	}
}

func TestIssueBulkUpdateCommand_DependencyRetargetBuildsApplyOps(t *testing.T) {
	tempDir := t.TempDir()
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update-retarget.json")
	if err := os.WriteFile(
		bulkUpdatePath,
		[]byte(`[{"task_id":"az-1","dependency_retargets":[{"from_id":"az-old","to_id":"az-new","type":"blocks"}]}]`),
		0o644,
	); err != nil {
		t.Fatalf("write bulk-update file: %v", err)
	}

	var commands []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req)
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:          "az-1",
						Title:       "Existing",
						Description: "Desc",
						Type:        domain.TypeTask,
						Priority:    domain.P2,
						Status:      domain.StatusOpen,
					}})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case protocol.CommandTaskBulkApply:
					body, err := json.Marshal(applyExecutionResultBody{
						Summary: applyExecutionSummaryBody{Total: 2, Succeeded: 2, Failed: 0},
					})
					if err != nil {
						t.Fatalf("marshal apply response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	_ = captureStdout(t, func() error {
		return IssueBulkUpdateCommand(deps, IssueBulkUpdateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkUpdatePath,
			DryRun:         true,
		})
	})

	var applyReq *protocol.RequestEnvelope
	for i := range commands {
		if commands[i].Command == protocol.CommandTaskBulkApply {
			applyReq = &commands[i]
			break
		}
	}
	if applyReq == nil {
		t.Fatalf("expected bulk apply command")
	}
	var body protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReq.Body, &body); err != nil {
		t.Fatalf("unmarshal apply body: %v", err)
	}
	if !body.DryRun {
		t.Fatalf("dry_run = false, want true")
	}
	if len(body.Operations) != 2 {
		t.Fatalf("operation count = %d, want 2", len(body.Operations))
	}
	if body.Operations[0].Command != daemonclient.CommandTaskDependencyRemove {
		t.Fatalf("operation[0] command = %q, want %q", body.Operations[0].Command, daemonclient.CommandTaskDependencyRemove)
	}
	if body.Operations[1].Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("operation[1] command = %q, want %q", body.Operations[1].Command, daemonclient.CommandTaskDependencyAdd)
	}

	var removeBody daemonclient.TaskDependencyRemoveParams
	if err := json.Unmarshal(body.Operations[0].Body, &removeBody); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if removeBody.TaskID != "az-1" || removeBody.DependsOnID != "az-old" || removeBody.Type != "blocks" || !removeBody.Confirm {
		t.Fatalf("remove body = %+v", removeBody)
	}

	var addBody daemonclient.TaskDependencyParams
	if err := json.Unmarshal(body.Operations[1].Body, &addBody); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if addBody.TaskID != "az-1" || addBody.DependsOnID != "az-new" || addBody.Type != "blocks" {
		t.Fatalf("add body = %+v", addBody)
	}
}

func TestPrintUsageIncludesExport(t *testing.T) {
	output := captureStdout(t, func() error {
		PrintUsage()
		return nil
	})

	if !strings.Contains(output, "export") {
		t.Fatalf("usage missing export command: %q", output)
	}
	if !strings.Contains(output, "log [sources]") {
		t.Fatalf("usage missing log command: %q", output)
	}
	if !strings.Contains(output, "az export --format json --out snapshot.json") {
		t.Fatalf("usage missing export example: %q", output)
	}
	if !strings.Contains(output, "az log --no-follow --lines 100 daemon tui") {
		t.Fatalf("usage missing log example: %q", output)
	}
	if !strings.Contains(output, "issue list [--project <project-id>] [--json] [--deps] [--status <status> ...]") {
		t.Fatalf("usage missing issue list command: %q", output)
	}
	if !strings.Contains(output, "issue get [--project <project-id>] [--id <id>] [--json] [<id>]") {
		t.Fatalf("usage missing issue get command: %q", output)
	}
	if !strings.Contains(output, "issue get-many [--project <project-id>] --id <id>") {
		t.Fatalf("usage missing issue get-many command: %q", output)
	}
	if !strings.Contains(output, "issue check [--project <project-id>] [--id <id>] [--json] [<id>]") {
		t.Fatalf("usage missing issue check command: %q", output)
	}
	if !strings.Contains(output, "issue doctor [--project <project-id>] [--id <id>] [--json] [<id>]") {
		t.Fatalf("usage missing issue doctor command: %q", output)
	}
	if !strings.Contains(output, "issue create [--project <project-id>] [--impl <implementation> ...] [--deferred]") {
		t.Fatalf("usage missing issue create command: %q", output)
	}
	if strings.Contains(output, "issue child ") {
		t.Fatalf("usage should not include issue child command: %q", output)
	}
	if !strings.Contains(output, "issue update [--project <project-id>] [--id <id>] [--json] [<id>]") {
		t.Fatalf("usage missing issue update command: %q", output)
	}
	if strings.Contains(output, "issue status --impl <implementation>") {
		t.Fatalf("usage should not include issue status command: %q", output)
	}
	if !strings.Contains(output, "issue close [--project <project-id>] [--id <id>] [--json] [<id>]") {
		t.Fatalf("usage missing issue close command: %q", output)
	}
	if !strings.Contains(output, "issue delete [--project <project-id>] [--id <id>] [--json] [<id>] --confirm") {
		t.Fatalf("usage missing issue delete command: %q", output)
	}
	if !strings.Contains(output, "issue dep add [--project <project-id>] --issue-id <issue-id> --depends-on-id <depends-on-id> [--type ...] [--json]") {
		t.Fatalf("usage missing issue dep add command: %q", output)
	}
	if !strings.Contains(output, "issue dep remove [--project <project-id>] --issue-id <issue-id> --depends-on-id <depends-on-id> [--type ...] [--confirm] [--json]") {
		t.Fatalf("usage missing issue dep remove command: %q", output)
	}
	if !strings.Contains(output, "issue dep bulk apply [--project <project-id>] --input <path>") {
		t.Fatalf("usage missing issue dep bulk apply command: %q", output)
	}
	if !strings.Contains(output, "issue bulk-create [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]") {
		t.Fatalf("usage missing issue bulk-create command: %q", output)
	}
	if !strings.Contains(output, "issue bulk-update [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]") {
		t.Fatalf("usage missing issue bulk-update command: %q", output)
	}
	if !strings.Contains(output, "config set spec.enabled <true|false> [--project-dir <dir>]") {
		t.Fatalf("usage missing config command: %q", output)
	}
	if !strings.Contains(output, "az config set spec.enabled false") {
		t.Fatalf("usage missing config example: %q", output)
	}
	if !strings.Contains(output, "sync [--all] [<directory>] [--project-dir <dir>]") {
		t.Fatalf("usage missing sync command: %q", output)
	}
	if !strings.Contains(output, "impl delete --confirm <implementation>") {
		t.Fatalf("usage missing impl delete command: %q", output)
	}
	if !strings.Contains(output, "impl list") {
		t.Fatalf("usage missing impl list command: %q", output)
	}
	if !strings.Contains(output, "impl migrate <from> <to>") {
		t.Fatalf("usage missing impl migrate command: %q", output)
	}
	if !strings.Contains(output, "az sync --all") {
		t.Fatalf("usage missing sync example: %q", output)
	}
	if !strings.Contains(output, "az impl list") {
		t.Fatalf("usage missing impl list example: %q", output)
	}
	if !strings.Contains(output, "az impl delete --confirm ts-opentui") {
		t.Fatalf("usage missing impl delete example: %q", output)
	}
	if !strings.Contains(output, "az impl migrate ts-opentui default") {
		t.Fatalf("usage missing impl migrate example: %q", output)
	}
	if !strings.Contains(output, "operation <subcommand>") {
		t.Fatalf("usage missing operation command family: %q", output)
	}
	if !strings.Contains(output, "az operation list --limit 20") {
		t.Fatalf("usage missing operation list example: %q", output)
	}
	if !strings.Contains(output, "az operation get --id op-123 --wait") {
		t.Fatalf("usage missing operation get example: %q", output)
	}
	if !strings.Contains(output, "az operation cancel --id op-123") {
		t.Fatalf("usage missing operation cancel example: %q", output)
	}
	if !strings.Contains(output, "az operation logs --id op-123") {
		t.Fatalf("usage missing operation logs example: %q", output)
	}
	if !strings.Contains(output, "prime") {
		t.Fatalf("usage missing prime command: %q", output)
	}
	if strings.Contains(output, "issue close --impl") || strings.Contains(output, "issue delete --impl") || strings.Contains(output, "issue dep add --impl") {
		t.Fatalf("usage should not include --impl for existing-issue commands: %q", output)
	}
	if !strings.Contains(output, "Argument ordering: place flags/options before positional arguments for deterministic parsing.") {
		t.Fatalf("usage missing canonical argument ordering hint: %q", output)
	}
}

func TestPrimeCommandWithoutIssueContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{})
	})

	if !strings.Contains(output, "Azedarach Session Primer") {
		t.Fatalf("prime output missing header: %q", output)
	}
	if !strings.Contains(output, "AZEDARACH_PRIMER_KEY:azedarach-prime-v1") {
		t.Fatalf("prime output missing evidence key: %q", output)
	}
	if !strings.Contains(output, "No active issue is preselected") {
		t.Fatalf("prime output missing no-issue guardrail: %q", output)
	}
	if !strings.Contains(output, "single-window fanout") {
		t.Fatalf("prime output missing orchestration shorthand: %q", output)
	}
	if !strings.Contains(output, "split work until each child issue is independently actionable and fits within a single subagent context window") {
		t.Fatalf("prime output missing subagent sizing guardrail: %q", output)
	}
	if !strings.Contains(output, "How to use `az` command map:") {
		t.Fatalf("prime output missing az command map section: %q", output)
	}
	if !strings.Contains(output, "`az issue fanout --input ./fanout.json`") {
		t.Fatalf("prime output missing fanout plan command example: %q", output)
	}
	if !strings.Contains(output, "`az issue fanout drift --issue <issue-id> --worktree <path> --fail-on-out`") {
		t.Fatalf("prime output missing fanout drift command example: %q", output)
	}
	if !strings.Contains(output, "`az mail send --parent <parent-issue> --type dependency-ready --body \"...\"`") {
		t.Fatalf("prime output missing mail send command example: %q", output)
	}
	if !strings.Contains(output, "`az session start <issue-id>`, `az session status [issue-id]`, `az daemon start|stop|restart`, `az export --format json [--out <path>]`") {
		t.Fatalf("prime output missing session/runtime command examples: %q", output)
	}
	if !strings.Contains(output, "Optional (only when splitting work): `az issue create \"Child task\"`") {
		t.Fatalf("prime output missing optional child-task split guidance: %q", output)
	}
	if strings.Contains(output, "`az issue create \"Title\"` (auto-parents under `AZEDARACH_ISSUE_ID`; use `--deferred` for non-immediate follow-ups)") {
		t.Fatalf("prime output should not require unconditional child issue creation: %q", output)
	}
}

func TestPrimeCommandWithActiveIssueContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := marshalTaskListBody([]domain.Task{{
					ID:              "az-1",
					Title:           "Prime issue",
					Status:          domain.StatusOpen,
					Priority:        domain.P2,
					Type:            domain.TypeTask,
					Implementations: []string{"go-bubbletea"},
					CreatedAt:       now,
					UpdatedAt:       now,
					Dependencies: []domain.Dependency{
						{ID: "az-2", Type: domain.DependencyBlocks},
					},
				}})
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Active issue context (AZEDARACH_ISSUE_ID=az-1)") {
		t.Fatalf("prime output missing active issue section: %q", output)
	}
	if !strings.Contains(output, "az-1: Prime issue [status=open priority=P2 type=task impl=go-bubbletea]") {
		t.Fatalf("prime output missing issue summary: %q", output)
	}
	if !strings.Contains(output, "Dependencies:\n- blocks: az-2") {
		t.Fatalf("prime output missing dependency summary: %q", output)
	}
}

func TestPrimeCommandWarnsWhenActiveIssueClosed(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-closed")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := marshalTaskListBody([]domain.Task{{
					ID:              "az-closed",
					Title:           "Closed issue",
					Status:          domain.StatusDone,
					Priority:        domain.P2,
					Type:            domain.TypeTask,
					Implementations: []string{"go-bubbletea"},
					CreatedAt:       now,
					UpdatedAt:       now,
				}})
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Active issue `az-closed` is currently `closed`") {
		t.Fatalf("prime output missing closed-issue warning: %q", output)
	}
	if !strings.Contains(output, "`az issue create \"Next task\" --deferred`") {
		t.Fatalf("prime output missing closed-issue next-step guidance: %q", output)
	}
}

func TestPrimeCommandQuestionFirstAndSpecBlock(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	t.Setenv("AZEDARACH_PRIME_MODE", "question-first")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{Config: &config.Config{Spec: config.SpecConfig{Enabled: true}}})
	})

	if !strings.Contains(output, "Question-first execution rules") {
		t.Fatalf("prime output missing question-first block: %q", output)
	}
	if !strings.Contains(output, "- Spec workflow:") {
		t.Fatalf("prime output missing spec workflow block: %q", output)
	}
	if !strings.Contains(output, "ALWAYS check `az spec` requirements/links before starting behavior work.") {
		t.Fatalf("prime output missing mandatory pre-work spec check guardrail: %q", output)
	}
	if !strings.Contains(output, "If implementation is not aligned with spec, update spec first, then implement.") {
		t.Fatalf("prime output missing spec-first update guardrail: %q", output)
	}
	if !strings.Contains(output, "Ensure implementation issue(s) are linked to relevant spec requirement(s) before execution.") {
		t.Fatalf("prime output missing issue/spec linking guardrail: %q", output)
	}
}

func TestPrimeCommandSpecBlockDisabled(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{Config: &config.Config{Spec: config.SpecConfig{Enabled: false}}})
	})

	if strings.Contains(output, "- Spec workflow:") {
		t.Fatalf("prime output should not include spec workflow block when disabled: %q", output)
	}
}

type fakeLauncher struct {
	startErr      error
	stopErr       error
	replaceErr    error
	startCalled   bool
	stopCalled    bool
	replaceCalled bool
}

func (f *fakeLauncher) Start(context.Context) error {
	f.startCalled = true
	return f.startErr
}

func (f *fakeLauncher) Stop(context.Context) error {
	f.stopCalled = true
	return f.stopErr
}

func (f *fakeLauncher) Replace(context.Context) error {
	f.replaceCalled = true
	return f.replaceErr
}

type timeoutBudgetLauncher struct {
	minBudget time.Duration
	started   *bool
}

func (l *timeoutBudgetLauncher) Start(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fmt.Errorf("wait for daemon socket readiness: %w", context.DeadlineExceeded)
	}
	if time.Until(deadline) < l.minBudget {
		return fmt.Errorf("wait for daemon socket readiness: %w", context.DeadlineExceeded)
	}
	if l.started != nil {
		*l.started = true
	}
	return nil
}

func (l *timeoutBudgetLauncher) Replace(ctx context.Context) error {
	return l.Start(ctx)
}

func (l *timeoutBudgetLauncher) Stop(context.Context) error { return nil }

func TestIssueCreateCommandUsesExtendedDaemonAttachTimeout(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	started := false
	newLauncher = func(_, _ string) daemonStarter {
		return &timeoutBudgetLauncher{
			minBudget: 8 * time.Second,
			started:   &started,
		}
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if !started {
					return protocol.ResponseEnvelope{}, errors.New("daemon socket unavailable")
				}
				switch req.Command {
				case daemonclient.CommandTaskCreate:
					body, err := json.Marshal(map[string]string{"task_id": "az-timeout"})
					if err != nil {
						t.Fatalf("marshal create response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:           "timeout budget",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Implementations: []string{"default"},
	})
	if err != nil {
		t.Fatalf("IssueCreateCommand() error = %v", err)
	}
}

func TestRestartDaemonCommand(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
		}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	output := captureStdout(t, func() error {
		return RestartDaemonCommand(deps)
	})

	if !fake.replaceCalled {
		t.Fatalf("expected replace to be called")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if !strings.Contains(output, "Daemon restarted successfully.") {
		t.Fatalf("output missing restart success: %q", output)
	}
}

func TestStartDaemonCommand(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
		}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	output := captureStdout(t, func() error {
		return StartDaemonCommand(deps)
	})

	if !fake.startCalled {
		t.Fatalf("expected start to be called")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if !strings.Contains(output, "Daemon started successfully.") {
		t.Fatalf("output missing start success: %q", output)
	}
}

func TestStartDaemonCommandStartFailure(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{startErr: errors.New("boom")}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      t.TempDir(),
	}

	err := StartDaemonCommand(deps)
	if err == nil || !strings.Contains(err.Error(), "start daemon: boom") {
		t.Fatalf("error = %v, want start daemon boom", err)
	}
}

func TestRestartDaemonCommandReplaceFailure(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{replaceErr: errors.New("boom")}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      t.TempDir(),
	}

	err := RestartDaemonCommand(deps)
	if err == nil || !strings.Contains(err.Error(), "restart daemon: boom") {
		t.Fatalf("error = %v, want restart daemon boom", err)
	}
}

func TestStopDaemonCommand(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	deps := &Dependencies{
		Config:         config.DefaultConfig(),
		DaemonClient:   daemonclient.New(&fakeDaemonTransport{}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	output := captureStdout(t, func() error {
		return StopDaemonCommand(deps)
	})

	if !fake.stopCalled {
		t.Fatalf("expected stop to be called")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if !strings.Contains(output, "Daemon stopped successfully.") {
		t.Fatalf("output missing stop success: %q", output)
	}
}

func TestStopDaemonCommandFailure(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{stopErr: errors.New("boom")}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      t.TempDir(),
	}

	err := StopDaemonCommand(deps)
	if err == nil || !strings.Contains(err.Error(), "stop daemon: boom") {
		t.Fatalf("error = %v, want stop daemon boom", err)
	}
}

func TestEnsureDaemonDoesNotReplaceOnAcceptedHandshake(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	handshakes := 0
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				handshakes++
				return protocol.HelloAck{Accepted: true}, nil
			},
		}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	if err := ensureDaemon(context.Background(), deps, "cli"); err != nil {
		t.Fatalf("ensureDaemon() error = %v", err)
	}
	if fake.replaceCalled {
		t.Fatalf("expected replace to remain false for accepted handshake")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if handshakes != 1 {
		t.Fatalf("handshakes = %d, want 1", handshakes)
	}
}

func responseWithOutput(req protocol.RequestEnvelope, output string) protocol.ResponseEnvelope {
	payload, err := json.Marshal(commandOutputBody{Output: output})
	if err != nil {
		panic(err)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     req.SentAt,
		OK:              true,
		Body:            payload,
	}
}

func responseWithJSON(req protocol.RequestEnvelope, body any) protocol.ResponseEnvelope {
	payload, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     req.SentAt,
		OK:              true,
		Body:            payload,
	}
}

func mustJSON(t *testing.T, body any) json.RawMessage {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal json body: %v", err)
	}
	return payload
}

func mustSnapshotPayloadJSON(t *testing.T, payload protocol.SnapshotPayload) []byte {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}
	return data
}

func mustApplyResultBody(t *testing.T, summary applyExecutionSummaryBody) []byte {
	t.Helper()

	data, err := json.Marshal(applyExecutionResultBody{Summary: summary})
	if err != nil {
		t.Fatalf("marshal apply result body: %v", err)
	}
	return data
}

type applyDryRunPreviewBody struct {
	SchemaVersion    uint16                            `json:"schema_version"`
	SnapshotRevision uint64                            `json:"snapshot_revision"`
	DryRun           bool                              `json:"dry_run"`
	Operations       []applyDryRunPreviewOperationBody `json:"operations"`
}

type applyDryRunPreviewOperationBody struct {
	Index   int             `json:"index"`
	Command string          `json:"command"`
	Body    json.RawMessage `json:"body,omitempty"`
}

func mustApplyDryRunPreviewBody(t *testing.T, preview applyDryRunPreviewBody) []byte {
	t.Helper()

	data, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("marshal dry-run preview body: %v", err)
	}
	return data
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- fn()
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)

	os.Stdout = oldStdout
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stdout: %v", copyErr)
	}
	if runErr != nil {
		t.Fatalf("command error: %v", runErr)
	}

	return buf.String()
}
