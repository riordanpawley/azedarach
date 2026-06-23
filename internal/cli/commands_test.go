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
	"github.com/riordanpawley/azedarach/internal/logging"
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

func marshalTaskListBodyForProject(projectID string, tasks []domain.Task) ([]byte, error) {
	return json.Marshal(protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 0,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            tasks,
	})
}

func assertMetadataOnlyTaskGetManyRequest(t *testing.T, req protocol.RequestEnvelope, issueID string) {
	t.Helper()
	if req.Command != daemonclient.CommandTaskGetMany {
		t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskGetMany)
	}
	var body daemonclient.TaskIDsRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal task.get_many request body: %v", err)
	}
	if len(body.TaskIDs) != 1 || body.TaskIDs[0].String() != issueID {
		t.Fatalf("task_ids = %+v, want [%s]", body.TaskIDs, issueID)
	}
	if !body.IncludeAncestors || !body.ExcludeDependents || !body.MetadataOnly {
		t.Fatalf("request flags ancestors=%v exclude_dependents=%v metadata_only=%v, want all true", body.IncludeAncestors, body.ExcludeDependents, body.MetadataOnly)
	}
}

func TestNewDependenciesAtUsesBaseProjectAndGlobalRuntimeForLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
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

func TestNewDependenciesAtUsesGlobalRuntimeForLinkedWorktreeWithoutEnv(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.DaemonSocket != config.GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.GlobalDaemonSocketPath())
	}
	if deps.RuntimeRepoDir != repo {
		t.Fatalf("RuntimeRepoDir = %q, want %q", deps.RuntimeRepoDir, repo)
	}
}

func TestNewDependenciesAtRejectsSharedDaemonFromLinkedAzedarachWorktreeBinary(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")
	executable := filepath.Join(worktree, "bin", "az")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatalf("MkdirAll(bin): %v", err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(executable): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	_, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err == nil {
		t.Fatal("NewDependenciesAt() error = nil, want shared daemon fence error")
	}
	if !strings.Contains(err.Error(), "refusing to use the shared production daemon") {
		t.Fatalf("NewDependenciesAt() error = %q, want shared daemon fence error", err)
	}
}

func TestNewDependenciesAtAllowsSharedDaemonFromCanonicalBinaryOutsideLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")
	executable := filepath.Join(repo, "bin", "az")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatalf("MkdirAll(bin): %v", err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(executable): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.DaemonSocket != config.GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.GlobalDaemonSocketPath())
	}
}

func TestNewDependenciesAtUsesScopedSocketForLinkedWorktreeWhenExplicit(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
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

func TestMergePreflightDirtyFilesIgnoreUntrackedOnlyStatus(t *testing.T) {
	status := daemonclient.GitStatus{
		HasChanges: true,
		Untracked:  []string{".azedarach/images/", "docs/"},
	}

	if got := dirtyFilesFromGitStatus(status); len(got) != 0 {
		t.Fatalf("dirty files = %v, want none for untracked-only status", got)
	}
	if got := summarizeGitStatusCounts(status); got != "clean" {
		t.Fatalf("summary = %q, want clean", got)
	}
}

func TestMergePreflightDirtyFilesKeepTrackedChanges(t *testing.T) {
	status := daemonclient.GitStatus{
		HasChanges: true,
		Modified:   []string{"b.go"},
		Staged:     []string{"a.go"},
		Untracked:  []string{"scratch/"},
	}

	got := dirtyFilesFromGitStatus(status)
	want := []string{"a.go", "b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dirty files = %v, want %v", got, want)
	}
	if got := summarizeGitStatusCounts(status); got != "1 staged, 1 modified" {
		t.Fatalf("summary = %q, want tracked-only summary", got)
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
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
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
	if len(commands) != 3 || commands[0] != daemonclient.CommandTaskGetMany || commands[1] != daemonclient.CommandTaskMergeBaseTarget || commands[2] != commandSessionStart {
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

func TestStartCommandValidatesIssueFromMetadataOnlyLocalSnapshot(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Local issue", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					<-ctx.Done()
					return protocol.ResponseEnvelope{}, ctx.Err()
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case commandSessionStart:
					gotReq = req
					return responseWithOutput(req, "started\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj").WithReadWaitPolicy(daemonclient.ReadWaitPolicy{
			Default:  time.Nanosecond,
			Explicit: 2 * time.Nanosecond,
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	if err := StartCommand(deps, "issue-1"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}
	if gotReq.Command != commandSessionStart {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandSessionStart)
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandTaskGetMany, daemonclient.CommandTaskMergeBaseTarget, commandSessionStart}) {
		t.Fatalf("commands = %v", commands)
	}
}

func TestParseWorktreeCreateArgs(t *testing.T) {
	opts, err := ParseWorktreeCreateArgs([]string{"--project", "proj-a", "--base", "parent-branch", "--json", "az-1"})
	if err != nil {
		t.Fatalf("ParseWorktreeCreateArgs error: %v", err)
	}
	if opts.Project != "proj-a" || opts.BaseBranch != "parent-branch" || opts.IssueID != "az-1" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestWorktreeCreateCommandCreatesWorktreeWithoutStartingSession(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "az-1", Title: "Parent", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var gotCreateReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case daemonclient.CommandWorktreeCreate:
					gotCreateReq = req
					return responseWithJSON(req, map[string]any{
						"project_id": "proj",
						"worktree": map[string]any{
							"path":     "/tmp/az-1",
							"branch":   "az/az-1",
							"issue_id": "az-1",
						},
					}), nil
				case daemonclient.CommandSessionStart:
					t.Fatalf("unexpected session start command")
					return protocol.ResponseEnvelope{}, nil
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
		return WorktreeCreateCommand(deps, WorktreeCreateOptions{IssueID: "az-1"})
	})

	if gotCreateReq.Command != daemonclient.CommandWorktreeCreate {
		t.Fatalf("command = %q, want %q", gotCreateReq.Command, daemonclient.CommandWorktreeCreate)
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandTaskGetMany, daemonclient.CommandTaskMergeBaseTarget, daemonclient.CommandWorktreeCreate}) {
		t.Fatalf("commands = %v", commands)
	}
	var body map[string]any
	if err := json.Unmarshal(gotCreateReq.Body, &body); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if body["project_id"] != "proj" || body["issue_id"] != "az-1" || body["base_branch"] != "main" {
		t.Fatalf("create body = %+v", body)
	}
	if output != "Worktree ready: /tmp/az-1\nBranch: az/az-1\nBase: main\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestStartCommandUsesExtendedTimeout(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var startDeadline time.Time
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case commandSessionStart:
					var ok bool
					startDeadline, ok = ctx.Deadline()
					if !ok {
						t.Fatal("session.start context missing deadline")
					}
					return responseWithOutput(req, "started\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	if err := StartCommand(deps, "issue-1"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}

	remaining := time.Until(startDeadline)
	if remaining < 4*time.Minute {
		t.Fatalf("session.start deadline too short: remaining=%s", remaining)
	}
	if remaining > sessionStartCommandTimeout+2*time.Second {
		t.Fatalf("session.start deadline too long: remaining=%s", remaining)
	}
}

func TestStartCommandPrintsProgressStatus(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case commandSessionStart:
					return responseWithOutput(req, "started\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	stderr := captureStderr(t, func() error {
		return StartCommand(deps, "issue-1")
	})
	if !strings.Contains(stderr, "Starting session for issue-1...") {
		t.Fatalf("stderr = %q, want start progress message", stderr)
	}
}

func TestStartCommandUsesParentWorktreeBranchForChildIssue(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	parentID := naming.IssueID("az-parent")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusInProgress},
		{ID: "az-child", Title: "Child", Status: domain.StatusOpen, ParentID: &parentID},
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
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-child")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-parent",
						Branch:         "az/az-parent",
						WorktreePath:   "/tmp/parent",
						BranchAttached: true,
						Reason:         "selected closest ancestor worktree branch",
						AncestorChain:  []string{"az-parent"},
					}), nil
				case commandSessionStart:
					gotReq = req
					return responseWithOutput(req, "ok\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	if err := StartCommand(deps, "az-child"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}
	if len(commands) != 3 || commands[0] != daemonclient.CommandTaskGetMany || commands[1] != daemonclient.CommandTaskMergeBaseTarget || commands[2] != commandSessionStart {
		t.Fatalf("commands = %v", commands)
	}
	var body sessionRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.BaseBranch != "az/az-parent" {
		t.Fatalf("base_branch = %q, want %q", body.BaseBranch, "az/az-parent")
	}
}

func TestStartCommandUsesNearestAncestorWorktreeBranchForNestedChildIssue(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	rootID := naming.IssueID("az-root")
	planningID := naming.IssueID("az-plan")
	childID := naming.IssueID("az-child")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: rootID, Title: "Root", Status: domain.StatusInProgress},
		{ID: planningID, Title: "Planning", Status: domain.StatusOpen, ParentID: &rootID},
		{ID: childID, Title: "Child", Status: domain.StatusOpen, ParentID: &planningID},
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
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-child")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-root",
						Branch:         "user/az-root/root-branch",
						WorktreePath:   "/tmp/root",
						BranchAttached: true,
						Reason:         "selected closest ancestor worktree branch",
						AncestorChain:  []string{"az-plan", "az-root"},
					}), nil
				case commandSessionStart:
					gotReq = req
					return responseWithOutput(req, "ok\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	if err := StartCommand(deps, "az-child"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}
	if len(commands) != 3 ||
		commands[0] != daemonclient.CommandTaskGetMany ||
		commands[1] != daemonclient.CommandTaskMergeBaseTarget ||
		commands[2] != commandSessionStart {
		t.Fatalf("commands = %v", commands)
	}
	var body sessionRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.BaseBranch != "user/az-root/root-branch" {
		t.Fatalf("base_branch = %q, want %q", body.BaseBranch, "user/az-root/root-branch")
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
						if tt.wantCommand != commandSessionStatus && req.Command == daemonclient.CommandTaskGetMany {
							assertMetadataOnlyTaskGetManyRequest(t, req, tt.sessionID)
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
				if len(commands) != 2 || commands[0] != daemonclient.CommandTaskGetMany || commands[1] != tt.wantCommand {
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

func TestSessionRestartAllCommandUsesDaemonEnvelope(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandSessionRestartAll {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				gotReq = req
				body, err := json.Marshal(protocol.SessionRestartAllResponseBody{
					ProjectID: naming.ProjectID("proj"),
					ForceBusy: false,
					Restarted: 1,
					Skipped:   1,
					Sessions: []protocol.SessionRestartAllItem{
						{IssueID: naming.IssueID("az-1"), SessionID: naming.SessionID("proj-az-1"), Activity: "idle", Restarted: true},
						{IssueID: naming.IssueID("az-2"), SessionID: naming.SessionID("proj-az-2"), Activity: "busy", Skipped: true, Reason: "busy"},
					},
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
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
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return SessionRestartAllCommand(deps, SessionRestartAllOptions{Yolo: true})
	})

	if gotReq.Command != daemonclient.CommandSessionRestartAll {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandSessionRestartAll)
	}
	var body protocol.SessionRestartAllRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.ForceBusy || !body.Yolo {
		t.Fatalf("request body = %+v, want project proj force false", body)
	}
	if !strings.Contains(output, "Restarted 1 session(s) (1 skipped, 0 failed)") ||
		!strings.Contains(output, "az-2") ||
		!strings.Contains(output, "skipped: busy") {
		t.Fatalf("output = %q, want restart summary and skipped session", output)
	}
}

func TestSessionRestartAllCommandPrintsFailuresBeforeReturningError(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := json.Marshal(protocol.SessionRestartAllResponseBody{
					ProjectID: naming.ProjectID("proj"),
					Failed:    1,
					Sessions: []protocol.SessionRestartAllItem{{
						IssueID:   naming.IssueID("az-1"),
						SessionID: naming.SessionID("proj-az-1"),
						Activity:  "idle",
						Error:     "send-keys failed",
					}},
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
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
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	var commandErr error
	output := captureStdout(t, func() error {
		commandErr = SessionRestartAllCommand(deps, SessionRestartAllOptions{})
		return nil
	})

	if commandErr == nil || !strings.Contains(commandErr.Error(), "failed to restart 1 session") {
		t.Fatalf("error = %v, want failed restart error", commandErr)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "failed: send-keys failed") {
		t.Fatalf("output = %q, want failed session detail", output)
	}
}

func TestStatusCommandSkipsTaskValidationReadWait(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var gotReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					<-ctx.Done()
					return protocol.ResponseEnvelope{}, ctx.Err()
				case commandSessionStatus:
					gotReq = req
					return responseWithOutput(req, "ok\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj").WithReadWaitPolicy(daemonclient.ReadWaitPolicy{
			Default:  time.Nanosecond,
			Explicit: 2 * time.Nanosecond,
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StatusCommand(deps, "eqa")
	})

	if output != "ok\n" {
		t.Fatalf("output = %q, want ok", output)
	}
	if gotReq.Command != commandSessionStatus {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandSessionStatus)
	}
	if commands[0] == daemonclient.CommandTaskList {
		t.Fatalf("commands = %v, want status to avoid task validation read wait", commands)
	}
	var body sessionRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.SessionID != "eqa" {
		t.Fatalf("session request body = %+v, want project proj session eqa", body)
	}
}

func TestSessionCommandsRejectInvalidOrUnknownIssueIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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
						if req.Command != daemonclient.CommandTaskGetMany {
							t.Fatalf("unexpected command: %s", req.Command)
						}
						assertMetadataOnlyTaskGetManyRequest(t, req, tt.issueID)
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
			if len(commands) != 1 || commands[0] != daemonclient.CommandTaskGetMany {
				t.Fatalf("commands for unknown ID = %v, want [%s]", commands, daemonclient.CommandTaskGetMany)
			}
		})
	}
}

func TestSessionCommandsResolveProjectPrefixedIssueIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "project-a")
	repoB := filepath.Join(home, "project-b")
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "project-a", Path: repoA},
			{Name: "project-b", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("project A id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("project B id: %v", err)
	}

	tests := []struct {
		name        string
		command     func(*Dependencies, string) error
		arg         string
		wantCommand string
	}{
		{
			name:        "stop explicit project issue",
			command:     KillCommand,
			arg:         "project-b:bxc",
			wantCommand: commandSessionStop,
		},
		{
			name:        "status explicit project issue",
			command:     StatusCommand,
			arg:         "project-b:bxc",
			wantCommand: commandSessionStatus,
		},
		{
			name:        "stop default tmux session form",
			command:     KillCommand,
			arg:         naming.CanonicalSessionID(repoB, "bxc"),
			wantCommand: commandSessionStop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			resolvedIssueID := "bxc"
			commands := []string{}
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						commands = append(commands, req.Command+":"+req.Meta.ProjectID.String())
						switch req.Command {
						case daemonclient.CommandTaskGetMany:
							var requestBody daemonclient.TaskIDsRequest
							if err := json.Unmarshal(req.Body, &requestBody); err != nil {
								t.Fatalf("unmarshal task.get_many request body: %v", err)
							}
							if len(requestBody.TaskIDs) != 1 {
								t.Fatalf("task_ids = %+v, want one", requestBody.TaskIDs)
							}
							if !requestBody.IncludeAncestors || !requestBody.ExcludeDependents || !requestBody.MetadataOnly {
								t.Fatalf("request flags ancestors=%v exclude_dependents=%v metadata_only=%v, want all true", requestBody.IncludeAncestors, requestBody.ExcludeDependents, requestBody.MetadataOnly)
							}
							resolvedIssueID = requestBody.TaskIDs[0].String()
							projectID := req.Meta.ProjectID.String()
							tasks := []domain.Task{{ID: "local-only", Title: "Local", Status: domain.StatusOpen}}
							if projectID == projectB {
								tasks = []domain.Task{{ID: naming.IssueID(resolvedIssueID), Title: "Remote", Status: domain.StatusOpen}}
							}
							body, err := marshalTaskListBodyForProject(projectID, tasks)
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
						case tt.wantCommand:
							gotReq = req
							return responseWithOutput(req, "ok\n"), nil
						default:
							t.Fatalf("unexpected command: %s", req.Command)
							return protocol.ResponseEnvelope{}, nil
						}
					},
				}).WithProjectID(projectA),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: projectA,
				RepoDir:   repoA,
			}

			output := captureStdout(t, func() error {
				return tt.command(deps, tt.arg)
			})

			if output != "ok\n" {
				t.Fatalf("output = %q, want ok", output)
			}
			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			if gotReq.Meta.ProjectID.String() != projectB {
				t.Fatalf("meta project_id = %q, want %q", gotReq.Meta.ProjectID, projectB)
			}
			var body sessionRequestBody
			if err := json.Unmarshal(gotReq.Body, &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body.ProjectID != projectB || body.SessionID != resolvedIssueID {
				t.Fatalf("session request body = %+v, want project %s issue %s", body, projectB, resolvedIssueID)
			}
			if len(commands) == 0 || !strings.Contains(commands[len(commands)-1], tt.wantCommand+":"+projectB) {
				t.Fatalf("commands = %v, want final %s:%s", commands, tt.wantCommand, projectB)
			}
		})
	}
}

func TestParseSessionStartArgsSupportsProjectFlag(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantIssueID string
		wantProject string
		wantWait    bool
	}{
		{
			name:        "project before issue",
			args:        []string{"--project", "azedarach", "cif"},
			wantIssueID: "cif",
			wantProject: "azedarach",
		},
		{
			name:        "project after issue with wait",
			args:        []string{"cif", "--project", "azedarach", "--wait"},
			wantIssueID: "cif",
			wantProject: "azedarach",
			wantWait:    true,
		},
		{
			name:        "project equals form",
			args:        []string{"--project=azedarach", "cif"},
			wantIssueID: "cif",
			wantProject: "azedarach",
		},
		{
			name:        "wait before issue",
			args:        []string{"--wait", "cif"},
			wantIssueID: "cif",
			wantWait:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIssueID, gotOpts, err := ParseSessionStartArgs(tt.args, true, "usage")
			if err != nil {
				t.Fatalf("ParseSessionStartArgs error = %v", err)
			}
			if gotIssueID != tt.wantIssueID || gotOpts.Project != tt.wantProject || gotOpts.Wait != tt.wantWait {
				t.Fatalf("issueID=%q opts=%+v, want issueID=%q project=%q wait=%v", gotIssueID, gotOpts, tt.wantIssueID, tt.wantProject, tt.wantWait)
			}
		})
	}
}

func TestParseSessionStartArgsRejectsProjectFlagOnAlias(t *testing.T) {
	_, _, err := ParseSessionStartArgs([]string{"--project", "azedarach", "cif"}, false, "usage: az start <issue-id> [--wait]")
	if err == nil || !strings.Contains(err.Error(), "usage: az start <issue-id> [--wait]") {
		t.Fatalf("err = %v, want alias usage", err)
	}
}

func TestStartCommandWithProjectOptionTargetsRegisteredProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "project-a")
	repoB := filepath.Join(home, "project-b")
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "azedarach", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("project A id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("project B id: %v", err)
	}

	var gotReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command+":"+req.Meta.ProjectID.String())
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "cif")
					projectID := req.Meta.ProjectID.String()
					tasks := []domain.Task{{ID: "local-only", Title: "Local", Status: domain.StatusOpen}}
					if projectID == projectB {
						tasks = []domain.Task{{ID: "cif", Title: "Remote", Status: domain.StatusOpen}}
					}
					body, err := marshalTaskListBodyForProject(projectID, tasks)
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
				case commandSessionStart:
					gotReq = req
					return responseWithOutput(req, "started\n"), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "cif",
						TargetID: "base",
						Branch:   "main",
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID(projectA),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: projectA,
		RepoDir:   repoA,
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "cif", SessionCommandOptions{Project: "azedarach"})
	})

	if output != "started\n" {
		t.Fatalf("output = %q, want started", output)
	}
	if gotReq.Command != commandSessionStart {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandSessionStart)
	}
	if gotReq.Meta.ProjectID.String() != projectB {
		t.Fatalf("meta project_id = %q, want %q", gotReq.Meta.ProjectID, projectB)
	}
	var body sessionRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != projectB || body.SessionID != "cif" || body.BaseBranch != "main" {
		t.Fatalf("session request body = %+v, want project %s issue cif base main", body, projectB)
	}
	if !reflect.DeepEqual(commands, []string{
		daemonclient.CommandTaskGetMany + ":" + projectB,
		daemonclient.CommandTaskMergeBaseTarget + ":" + projectB,
		commandSessionStart + ":" + projectB,
	}) {
		t.Fatalf("commands = %v, want task list and start scoped to %s", commands, projectB)
	}
}

func TestSessionCommandsKeepBareIssueIDsCurrentProjectScoped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "project-a")
	repoB := filepath.Join(home, "project-b")
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "project-b", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("project A id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("project B id: %v", err)
	}

	seenProjects := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskGetMany {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				assertMetadataOnlyTaskGetManyRequest(t, req, "bxc")
				projectID := req.Meta.ProjectID.String()
				seenProjects = append(seenProjects, projectID)
				tasks := []domain.Task{}
				if projectID == projectB {
					tasks = []domain.Task{{ID: "bxc", Title: "Remote", Status: domain.StatusOpen}}
				}
				body, err := marshalTaskListBodyForProject(projectID, tasks)
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
		}).WithProjectID(projectA),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: projectA,
		RepoDir:   repoA,
	}

	err = KillCommand(deps, "bxc")
	if err == nil || !strings.Contains(err.Error(), "issue not found: bxc") {
		t.Fatalf("err = %v, want current-project issue not found", err)
	}
	if len(seenProjects) != 1 || seenProjects[0] != projectA {
		t.Fatalf("task get projects = %v, want only current project %s", seenProjects, projectA)
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
				if req.Command == daemonclient.CommandTaskGetMany {
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
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

func TestBranchMergeToBaseCommandUsesDaemonGitFlow(t *testing.T) {
	commands := make([]string, 0, 8)
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
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
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "trunk",
						Reason:   "no ancestor chain; selected default base target",
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree != "/tmp/azedarach-az-123" && body.Worktree != baseWorktree {
						t.Fatalf("git status worktree = %q", body.Worktree)
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "trunk",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Branch:   "trunk",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
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
		RepoDir:   baseWorktree,
	}

	output := captureStdout(t, func() error {
		return BranchMergeToBaseCommand(deps, "az-123")
	})
	if !strings.Contains(output, "merge complete") {
		t.Fatalf("output = %q, want merge output", output)
	}
	if !strings.Contains(output, "Merged riordan/az-123/some-change into trunk (az-123)") {
		t.Fatalf("output = %q, want final summary", output)
	}
	if !strings.Contains(output, "- Phase timings:") || !strings.Contains(output, "merge:") {
		t.Fatalf("output = %q, want phase timings", output)
	}

	want := []string{
		daemonclient.CommandWorktreeList,
		daemonclient.CommandTaskMergeBaseTarget,
		daemonclient.CommandGitWorktreeForBranch,
		daemonclient.CommandGitStatus,
		daemonclient.CommandGitStatus,
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

func TestBranchMergeToBaseCommandCreatesFixerIssueForHookFailure(t *testing.T) {
	commands := make([]string, 0, 12)
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	var createBody daemonclient.TaskCreateParams
	var depBody daemonclient.TaskDependencyParams
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-123", Status: domain.StatusInReview}},
					}), nil
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
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "trunk",
						Reason:   "no ancestor chain; selected default base target",
					}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "trunk",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Branch:   "trunk",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
						Branch:   "riordan/az-123/some-change",
						Result: gitservice.MergeResult{
							Success: false,
							Message: "commit-msg hook failed\nmissing trailer",
						},
					}), nil
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createBody); err != nil {
						t.Fatalf("unmarshal task create body: %v", err)
					}
					return responseWithJSON(req, daemonclient.TaskIDResponse{TaskID: "az-fix"}), nil
				case daemonclient.CommandTaskDependencyAdd:
					if err := json.Unmarshal(req.Body, &depBody); err != nil {
						t.Fatalf("unmarshal dependency body: %v", err)
					}
					return responseWithJSON(req, map[string]any{}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-123")
	if err == nil {
		t.Fatal("BranchMergeToBaseCommand error = nil, want hook failure")
	}
	errText := err.Error()
	for _, want := range []string{"commit-msg hook failed", "created fixer issue az-fix"} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error = %q, want %q", errText, want)
		}
	}
	if createBody.Title != "Fix merge hook/check failure for az-123" {
		t.Fatalf("fixer title = %q", createBody.Title)
	}
	for _, want := range []string{
		"Source issue: az-123",
		"Source branch: riordan/az-123/some-change",
		"Target branch: trunk",
		"Target worktree: " + baseWorktree,
		"Retry: az issue close --id az-123",
		"missing trailer",
	} {
		if !strings.Contains(createBody.Notes, want) {
			t.Fatalf("fixer notes missing %q:\n%s", want, createBody.Notes)
		}
	}
	if depBody.TaskID.String() != "az-123" || depBody.DependsOnID.String() != "az-fix" || depBody.Type != string(domain.DependencyBlocks) {
		t.Fatalf("dependency body = %+v, want source blocked by fixer", depBody)
	}
	if !containsString(commands, daemonclient.CommandTaskCreate) || !containsString(commands, daemonclient.CommandTaskDependencyAdd) {
		t.Fatalf("commands = %v, want fixer create and dependency add", commands)
	}
}

func TestBranchMergeToBaseCommandUsesAttachedTargetBranchWorktree(t *testing.T) {
	commands := make([]string, 0, 8)
	baseWorktree := t.TempDir()
	targetWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
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
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "trunk",
						Reason:   "no ancestor chain; selected default base target",
					}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git worktree branch body: %v", err)
					}
					if body.Branch != "trunk" {
						t.Fatalf("branch lookup = %q, want trunk", body.Branch)
					}
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch:   "trunk",
						Worktree: targetWorktree,
						Found:    true,
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree != "/tmp/azedarach-az-123" && body.Worktree != targetWorktree {
						t.Fatalf("git status worktree = %q", body.Worktree)
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git fetch body: %v", err)
					}
					if body.Worktree != targetWorktree {
						t.Fatalf("fetch worktree = %q, want %q", body.Worktree, targetWorktree)
					}
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: targetWorktree,
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitMerge:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git merge body: %v", err)
					}
					if body.Worktree != targetWorktree || body.Branch != "riordan/az-123/some-change" {
						t.Fatalf("merge body = %+v", body)
					}
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: targetWorktree,
						Branch:   "riordan/az-123/some-change",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				case daemonclient.CommandGitCheckout:
					t.Fatalf("checkout should not run when target branch is already attached to %s", targetWorktree)
					return protocol.ResponseEnvelope{}, nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	output := captureStdout(t, func() error {
		return BranchMergeToBaseCommand(deps, "az-123")
	})
	if !strings.Contains(output, "Merged riordan/az-123/some-change into trunk (az-123)") {
		t.Fatalf("output = %q, want final summary", output)
	}
	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitCheckout {
			t.Fatalf("checkout command should not be issued, commands=%v", commands)
		}
	}
}

func TestBranchMergeToBaseCommandFailsOnDirtyPreflight(t *testing.T) {
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
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "main",
						Reason:   "no ancestor chain; selected default base target",
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

	err := BranchMergeToBaseCommand(deps, "az-123")
	if err == nil || !strings.Contains(err.Error(), "merge preflight failed") {
		t.Fatalf("err = %v, want preflight failure", err)
	}
	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitFetch || cmd == daemonclient.CommandGitCheckout || cmd == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected post-preflight command: %s", cmd)
		}
	}
}

func TestBranchMergeToBaseCommandUsesEnvIssueIDWhenArgumentMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-999")

	commands := make([]string, 0, 8)
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-999", Status: domain.StatusOpen}},
					}), nil
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
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-999", TargetID: "base", Branch: "trunk"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: ".", Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: ".", Branch: "trunk"}), nil
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

	if err := BranchMergeToBaseCommand(deps, ""); err != nil {
		t.Fatalf("BranchMergeToBaseCommand error = %v", err)
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

func TestBranchMergeToBaseCommandTreatsAzedarachRuntimeConfigAsDirtyInPreflight(t *testing.T) {
	commands := make([]string, 0, 8)
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "bhv", Status: domain.StatusOpen}},
					}), nil
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
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "bhv", TargetID: "base", Branch: "trunk"}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree == baseWorktree {
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
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Branch: "trunk"}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
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
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "bhv")
	if err == nil || !strings.Contains(err.Error(), "merge preflight failed") {
		t.Fatalf("err = %v, want preflight failure", err)
	}

	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected post-preflight command: %s", cmd)
		}
	}
}

func TestBranchMergeToBaseCommandUsesNearestNonClosedAncestorBranch(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	var mergedIn string
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusInProgress},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
							{"path": "/tmp/az-parent", "branch": "riordan/az-parent/work", "issue_id": "az-parent"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-parent",
						Branch:         "riordan/az-parent/work",
						WorktreePath:   "/tmp/az-parent",
						BranchAttached: true,
						AncestorChain:  []string{"az-parent"},
					}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git fetch body: %v", err)
					}
					if body.Worktree != "/tmp/az-parent" {
						t.Fatalf("fetch worktree = %q, want /tmp/az-parent", body.Worktree)
					}
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: "/tmp/az-parent", Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					t.Fatalf("checkout should not run when parent branch is already attached")
					return protocol.ResponseEnvelope{}, nil
				case daemonclient.CommandGitMerge:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git merge body: %v", err)
					}
					mergedIn = body.Worktree
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: body.Worktree,
						Branch:   "riordan/az-child/work",
						Result:   gitservice.MergeResult{Success: true},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	if err := BranchMergeToBaseCommand(deps, "az-child"); err != nil {
		t.Fatalf("BranchMergeToBaseCommand error = %v", err)
	}
	if mergedIn != "/tmp/az-parent" {
		t.Fatalf("merge worktree = %q, want /tmp/az-parent", mergedIn)
	}
}

func TestBranchMergeToBaseCommandBlocksChildWithoutAncestorWorktreeUnlessOverride(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusInProgress},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("refusing to merge child issue az-child directly into base: no active ancestor worktree branch was found; run `az worktree create az-parent`, then close the child into that target")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "no active ancestor worktree branch was found") || strings.Contains(err.Error(), "--allow-base-for-child") {
		t.Fatalf("err = %v, want child base merge refusal without override suggestion", err)
	}
}

func TestBranchMergeToBaseCommandFailsWhenIssueMissingFromTaskSnapshot(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-other", Status: domain.StatusOpen}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("cannot resolve merge target for az-child: issue not found in task projection; refusing fallback to base")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "issue not found in task projection") {
		t.Fatalf("err = %v, want missing issue projection error", err)
	}
}

func TestBranchMergeToBaseCommandFailsWhenParentMissingFromTaskSnapshot(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("cannot resolve merge target for az-child: parent issue az-parent missing from task projection; refusing fallback to base")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "parent issue az-parent missing from task projection") {
		t.Fatalf("err = %v, want missing parent projection error", err)
	}
}

func TestBranchMergeToBaseCommandBlocksChildBaseMergeWithoutOverride(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusDone},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("refusing to merge child issue az-child directly into base: no active ancestor worktree branch was found; run `az worktree create az-parent`, then close the child into that target")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "az worktree create az-parent") || strings.Contains(err.Error(), "--allow-base-for-child") {
		t.Fatalf("err = %v, want child base merge refusal without override suggestion", err)
	}
}

func TestBranchMergeToBaseCommandAllowsChildBaseMergeWithOverride(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusDone},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:       "az-child",
						TargetID:      "base",
						Branch:        "trunk",
						AncestorChain: []string{"az-parent"},
					}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Branch: "trunk"}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
						Branch:   "riordan/az-child/work",
						Result:   gitservice.MergeResult{Success: true},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommandWithOptions(deps, BranchMergeToBaseOptions{
		IssueID:           "az-child",
		AllowBaseForChild: true,
	})
	if err != nil {
		t.Fatalf("BranchMergeToBaseCommandWithOptions error = %v", err)
	}
}

func TestBranchAgentMergeCommandLaunchesAgentWhenPreflightConflicts(t *testing.T) {
	commands := make([]string, 0, 4)
	var resolveBody protocol.SessionResolveConflictRequestBody
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
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
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-123", TargetID: "base", Branch: "trunk"}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "trunk",
						Found:  false,
					}), nil
				case daemonclient.CommandGitMergePreflight:
					var body daemonclient.GitMergePreflightRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal preflight body: %v", err)
					}
					if body.SourceID != "az-123" || body.TargetID != "base" || body.TargetRef != "trunk" || body.SourceBranch != "riordan/az-123/some-change" {
						t.Fatalf("preflight body = %+v", body)
					}
					return responseWithJSON(req, daemonclient.GitMergePreflightResponse{
						SourceID:       "az-123",
						SourceWorktree: "/tmp/azedarach-az-123",
						TargetID:       "base",
						TargetWorktree: baseWorktree,
						Clean:          false,
						Reasons:        []string{"Merge would conflict in 1 files: README.md"},
						ConflictFiles:  []string{"README.md"},
					}), nil
				case daemonclient.CommandSessionResolveConflict:
					if err := json.Unmarshal(req.Body, &resolveBody); err != nil {
						t.Fatalf("unmarshal resolve body: %v", err)
					}
					return responseWithJSON(req, protocol.SessionResolveConflictResponseBody{
						ProjectID:  naming.ProjectID("proj"),
						IssueID:    naming.IssueID("az-123"),
						SessionID:  naming.SessionID("az-123"),
						Worktree:   "/tmp/azedarach-az-123",
						WindowName: "resolve-conflict",
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	output := captureStdout(t, func() error {
		return BranchAgentMergeCommand(deps, BranchAgentMergeOptions{IssueID: "az-123", Target: "base"})
	})
	if !strings.Contains(output, "Agent merge launched for az-123 -> base") {
		t.Fatalf("output = %q, want launched summary", output)
	}
	if resolveBody.IssueID != "az-123" || resolveBody.Worktree != "/tmp/azedarach-az-123" {
		t.Fatalf("resolve body = %+v", resolveBody)
	}
	if !reflect.DeepEqual(resolveBody.ConflictFiles, []string{"README.md"}) {
		t.Fatalf("resolve conflict files = %+v", resolveBody.ConflictFiles)
	}
	if !strings.Contains(resolveBody.Prompt, "merge trunk into riordan/az-123/some-change") {
		t.Fatalf("prompt = %q, want base merge instruction", resolveBody.Prompt)
	}
	want := []string{daemonclient.CommandWorktreeList, daemonclient.CommandTaskMergeBaseTarget, daemonclient.CommandGitWorktreeForBranch, daemonclient.CommandGitMergePreflight, daemonclient.CommandSessionResolveConflict}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestBranchAgentMergeCommandCleanPreflightDoesNotLaunchAgent(t *testing.T) {
	commands := make([]string, 0, 4)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/azedarach-az-123", "branch": "az/az-123", "issue_id": "az-123"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-123", TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitMergePreflight:
					return responseWithJSON(req, daemonclient.GitMergePreflightResponse{Clean: true}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return BranchAgentMergeCommand(deps, BranchAgentMergeOptions{IssueID: "az-123"})
	})
	if !strings.Contains(output, "Merge preflight clean for az-123 -> base; no agent needed.") {
		t.Fatalf("output = %q, want clean preflight message", output)
	}
	for _, command := range commands {
		if command == daemonclient.CommandSessionResolveConflict {
			t.Fatalf("unexpected resolve conflict command: %v", commands)
		}
	}
}

func TestBranchAgentMergeCommandBaseTargetUsesNearestNonClosedAncestorBranch(t *testing.T) {
	var preflightBody daemonclient.GitMergePreflightRequest
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
							{"path": "/tmp/az-parent", "branch": "riordan/az-parent/work", "issue_id": "az-parent"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-parent",
						Branch:         "riordan/az-parent/work",
						WorktreePath:   "/tmp/az-parent",
						BranchAttached: true,
						AncestorChain:  []string{"az-parent"},
					}), nil
				case daemonclient.CommandGitMergePreflight:
					if err := json.Unmarshal(req.Body, &preflightBody); err != nil {
						t.Fatalf("unmarshal preflight body: %v", err)
					}
					return responseWithJSON(req, daemonclient.GitMergePreflightResponse{Clean: true}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return BranchAgentMergeCommand(deps, BranchAgentMergeOptions{IssueID: "az-child", Target: "base"})
	})
	if !strings.Contains(output, "Merge preflight clean for az-child -> base; no agent needed.") {
		t.Fatalf("output = %q, want clean preflight summary", output)
	}
	if preflightBody.TargetID != "az-parent" || preflightBody.TargetRef != "riordan/az-parent/work" {
		t.Fatalf("preflight body = %+v", preflightBody)
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
				if req.Command == daemonclient.CommandTaskGetMany {
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				}
				if req.Command == daemonclient.CommandTaskMergeBaseTarget {
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
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
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
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
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
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
	daemonLogPath := filepath.Join(repoDir, ".azedarach", logging.DaemonLogFileName)
	tuiLogPath := filepath.Join(cfg.Session.LogDir, logging.TUILogFileName)
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
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
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
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
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
		RepoDir: nested,
	}

	daemonLogPath := filepath.Join(worktree, ".azedarach", logging.DaemonLogFileName)
	tuiLogPath := filepath.Join(worktree, ".azedarach", logging.TUILogFileName)
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
				if req.Command == daemonclient.CommandTaskGetMany {
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				}
				if req.Command == daemonclient.CommandTaskMergeBaseTarget {
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
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

func TestSessionResolveConflictCommandUsesDaemonClient(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				if req.Command != daemonclient.CommandSessionResolveConflict {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
				}
				return responseWithJSON(req, protocol.SessionResolveConflictResponseBody{
					ProjectID:  naming.ProjectID("proj"),
					IssueID:    naming.IssueID("bxc"),
					SessionID:  naming.SessionID("bxc"),
					Worktree:   "/tmp/bxc",
					WindowName: "resolve-conflict",
				}), nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return SessionResolveConflictCommand(deps, SessionResolveConflictOptions{
			IssueID:       " bxc ",
			Worktree:      "/tmp/bxc",
			ConflictFiles: []string{"README.md", "cmd/az/main.go"},
			Prompt:        "Resolve the conflict and keep tests green.",
		})
	})

	if gotReq.Command != daemonclient.CommandSessionResolveConflict {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandSessionResolveConflict)
	}
	var body protocol.SessionResolveConflictRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.IssueID != "bxc" || body.Worktree != "/tmp/bxc" {
		t.Fatalf("body route fields = %+v", body)
	}
	if !reflect.DeepEqual(body.ConflictFiles, []string{"README.md", "cmd/az/main.go"}) {
		t.Fatalf("conflict files = %+v", body.ConflictFiles)
	}
	if body.Prompt != "Resolve the conflict and keep tests green." {
		t.Fatalf("prompt = %q", body.Prompt)
	}
	wantOutput := "Conflict resolution agent launched for bxc\nWorktree: /tmp/bxc\nWindow: resolve-conflict\n"
	if output != wantOutput {
		t.Fatalf("output = %q, want %q", output, wantOutput)
	}
}

func TestSessionResolveConflictCommandReturnsDaemonError(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandSessionResolveConflict {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					CompletedAt:     req.SentAt,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:    protocol.ErrorCodeConflict,
						Message: "session is not attached",
					},
				}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := SessionResolveConflictCommand(deps, SessionResolveConflictOptions{IssueID: "bxc"})
	if err == nil || err.Error() != "failed to resolve conflicts for bxc: conflict: session is not attached" {
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
			errContains: "usage: az config set <key> <value> [--project-dir <dir>]",
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
			errContains: "usage: az sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]",
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

func TestConfigSetCommandWritesLatencyTraceConfig(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	output := captureStdout(t, func() error {
		return ConfigSetCommand(deps, ConfigSetOptions{Key: "diagnostics.latencyTrace", Value: "on"})
	})

	if !strings.Contains(output, "diagnostics.latencyTrace=true") {
		t.Fatalf("config output missing diagnostics update: %q", output)
	}
	cfg, err := config.LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.Diagnostics.LatencyTrace {
		t.Fatalf("Diagnostics.LatencyTrace = false, want true")
	}
}

func TestConfigSetCommandRejectsRemovedCloseCleanupConfig(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "issues.autoFinalizeOnClose", Value: "yes"})
	if err == nil || !strings.Contains(err.Error(), "Unsupported config key") || strings.Contains(err.Error(), "Supported keys: spec.enabled, diagnostics.latencyTrace, issues.autoFinalizeOnClose") {
		t.Fatalf("ConfigSetCommand error = %v, want removed setting rejected", err)
	}
}

func TestConfigSetCommandRejectsInvalidLatencyTraceBoolean(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "diagnostics.latencyTrace", Value: "maybe"})
	if err == nil || !strings.Contains(err.Error(), "Invalid boolean value 'maybe' for diagnostics.latencyTrace") {
		t.Fatalf("error = %v, want invalid latency trace boolean failure", err)
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

func TestSyncCommandAllUsesDaemonWorktreeTargetsAndDaemonSyncRun(t *testing.T) {
	var gotWorktreeReq protocol.RequestEnvelope
	var gotSyncReq protocol.RequestEnvelope
	payload, err := json.Marshal(daemonclient.IssueSyncSummary{
		Provider:     "linear",
		Enabled:      true,
		RemoteIssues: 2,
		LocalIssues:  2,
		Imported:     1,
		UpdatedLocal: 1,
		PushedRemote: 1,
	})
	if err != nil {
		t.Fatalf("marshal sync summary: %v", err)
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
				case daemonclient.CommandSyncRun:
					gotSyncReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
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
	if gotSyncReq.Command != daemonclient.CommandSyncRun {
		t.Fatalf("sync command = %q, want %q", gotSyncReq.Command, daemonclient.CommandSyncRun)
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
	if !strings.Contains(output, "Linear: remote=2 local=2 imported=1 updated_local=1 pushed_remote=1 conflicts=0") {
		t.Fatalf("sync output missing sync summary: %q", output)
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
				case daemonclient.CommandTaskGet:
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
	for _, update := range updates {
		if update.Description != "desc" {
			t.Fatalf("update %s description = %q, want preserved desc", update.TaskID, update.Description)
		}
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
				case daemonclient.CommandTaskGet:
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
		if update.Description != "desc" {
			t.Fatalf("update %s description = %q, want preserved desc", update.TaskID, update.Description)
		}
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
			args: []string{"--status", "open", "--status", "in_review", "--statuses", "in_progress,open"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, States: []domain.Status{domain.StatusOpen, domain.StatusInReview, domain.StatusInProgress}},
		},
		{
			name: "state aliases",
			args: []string{"--state", "open", "--states", "in_review"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, States: []domain.Status{domain.StatusOpen, domain.StatusInReview}},
		},
		{
			name: "id filters",
			args: []string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, IDs: []string{"az-1", "az-2", "az-3", "az-4"}},
		},
		{
			name: "parent and dependency filters",
			args: []string{"--parent", "az-parent-1", "--parents", "az-parent-2", "--depends-on", "az-upstream-1", "--depends-on-ids", "az-upstream-2"},
			want: IssueListOptions{
				JSON:         false,
				Deps:         false,
				Limit:        defaultIssueListLimit,
				ParentIDs:    []string{"az-parent-1", "az-parent-2"},
				DependsOnIDs: []string{"az-upstream-1", "az-upstream-2"},
			},
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
			name: "include notes",
			args: []string{"--with-notes", "az-2"},
			want: IssueGetOptions{IssueID: "az-2", IncludeNotes: true},
		},
		{
			name:        "missing issue id",
			args:        []string{},
			errContains: "usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]",
		},
		{
			name:        "too many args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]",
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
	got, err := ParseIssueGetManyArgs([]string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4", "--json", "--with-notes"})
	if err != nil {
		t.Fatalf("ParseIssueGetManyArgs() error = %v", err)
	}
	if !got.JSON {
		t.Fatalf("expected json output flag to be set")
	}
	if !got.IncludeNotes {
		t.Fatalf("expected with-notes flag to be set")
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
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P2,
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "explicit options",
			args: []string{"--impl", "go-bubbletea", "--type", "bug", "--priority", "P0", "--description", "details", "Title"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Description:            "details",
				Type:                   domain.TypeBug,
				Priority:               domain.P0,
				PriorityExplicit:       true,
				Implementations:        []string{"go-bubbletea"},
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "title flag",
			args: []string{"--title", "Title", "--impl", "go-bubbletea"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P2,
				Implementations:        []string{"go-bubbletea"},
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "interspersed flags after title",
			args: []string{"Title", "--impl", "go-bubbletea", "--priority", "P1"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P1,
				PriorityExplicit:       true,
				Implementations:        []string{"go-bubbletea"},
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "deferred defaults priority",
			args: []string{"--deferred", "Title"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P4,
				Deferred:               true,
				AutoCreatedFromIssueID: ptrToString("az-parent"),
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
		{
			name:        "title flag and positional are ambiguous",
			args:        []string{"--title", "Flag title", "Positional title"},
			errContains: "provide title either as --title or as a positional argument, not both",
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

func TestParseIssueSplitArgsDefaultsParentFromEnv(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-parent")
	opts, err := ParseIssueSplitArgs([]string{"--description", "do this elsewhere", "--priority", "P1", "Child work"})
	if err != nil {
		t.Fatalf("ParseIssueSplitArgs error = %v", err)
	}
	if opts.ParentIssueID != "az-parent" || opts.Title != "Child work" || opts.Description != "do this elsewhere" {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.Priority != domain.P1 || !opts.PriorityExplicit {
		t.Fatalf("priority = %s explicit=%v, want P1 explicit", opts.Priority, opts.PriorityExplicit)
	}
}

func TestParseIssueSplitArgsRequiresParent(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	_, err := ParseIssueSplitArgs([]string{"Child work"})
	if err == nil || !strings.Contains(err.Error(), "missing parent issue") {
		t.Fatalf("error = %v, want missing parent issue", err)
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
			errContains: "usage: az issue close [--project <project-id>] [--id <issue-id>|-i <issue-id>] [--json] [--force-worktree] [<issue-id>]",
		},
		{
			name:        "extra args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue close [--project <project-id>] [--id <issue-id>|-i <issue-id>] [--json] [--force-worktree] [<issue-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
		},
		{
			name: "short id",
			args: []string{"-i", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
		},
		{
			name:        "cleanup flag removed",
			args:        []string{"--id", "az-2", "--cleanup"},
			errContains: "flag provided but not defined: -cleanup",
		},
		{
			name: "force worktree",
			args: []string{"--id", "az-2", "--force-worktree"},
			want: IssueCloseOptions{IssueID: "az-2", ForceWorktree: true},
		},
		{
			name:        "allow base for child unsupported on close",
			args:        []string{"--id", "az-2", "--allow-base-for-child"},
			errContains: "flag provided but not defined: -allow-base-for-child",
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
	got, err = ParseIssueDeleteArgs([]string{"az-1", "--confirm", "--cleanup", "--force-worktree"})
	if err != nil {
		t.Fatalf("ParseIssueDeleteArgs() cleanup error = %v", err)
	}
	if got.IssueID != "az-1" || !got.StopSession || !got.RemoveWorktree || !got.ForceWorktree {
		t.Fatalf("ParseIssueDeleteArgs() cleanup = %+v", got)
	}
	_, err = ParseIssueDeleteArgs([]string{"az-1", "--confirm", "--force-worktree"})
	if err == nil || !strings.Contains(err.Error(), "--force-worktree requires --remove-worktree or --cleanup") {
		t.Fatalf("expected force-worktree dependency error, got %v", err)
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
			name: "update description",
			args: []string{"--description", "New details", "az-1"},
			want: IssueUpdateOptions{
				IssueID:        "az-1",
				Description:    "New details",
				DescriptionSet: true,
			},
		},
		{
			name: "clear description",
			args: []string{"--description", "", "az-1"},
			want: IssueUpdateOptions{
				IssueID:        "az-1",
				DescriptionSet: true,
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
			name: "replace notes",
			args: []string{"--notes", "Replacement", "az-1"},
			want: func() IssueUpdateOptions {
				notes := "Replacement"
				return IssueUpdateOptions{
					IssueID: "az-1",
					Notes:   &notes,
				}
			}(),
		},
		{
			name: "clear notes",
			args: []string{"--notes", "", "az-1"},
			want: func() IssueUpdateOptions {
				notes := ""
				return IssueUpdateOptions{
					IssueID: "az-1",
					Notes:   &notes,
				}
			}(),
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
			args: []string{"az-1", "--status", "in_review"},
			want: func() IssueUpdateOptions {
				status := domain.StatusInReview
				return IssueUpdateOptions{
					IssueID: "az-1",
					Status:  &status,
				}
			}(),
		},
		{
			name: "force worktree on closed status",
			args: []string{"az-1", "--status", "closed", "--force-worktree"},
			want: func() IssueUpdateOptions {
				status := domain.StatusDone
				return IssueUpdateOptions{
					IssueID:       "az-1",
					Status:        &status,
					ForceWorktree: true,
				}
			}(),
		},
		{
			name:        "cleanup flag removed",
			args:        []string{"az-1", "--status", "closed", "--cleanup"},
			errContains: "flag provided but not defined: -cleanup",
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
			if got.IssueID != tt.want.IssueID || got.Title != tt.want.Title || got.Description != tt.want.Description || got.DescriptionSet != tt.want.DescriptionSet || got.AppendNotes != tt.want.AppendNotes {
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
	add, err = ParseIssueDependencyAddArgs([]string{"--type", "created-in", "az-1", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs(created-in) error = %v", err)
	}
	if add.Type != "created-in" {
		t.Fatalf("created-in dependency type = %q", add.Type)
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
	add, err = ParseIssueDependencyAddArgs([]string{"az-1", "az-2", "--type", "parent-child", "--force-parent-change"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() force parent change error = %v", err)
	}
	if !add.ForceParentChange {
		t.Fatalf("ParseIssueDependencyAddArgs() force parent change not set: %+v", add)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"chefy:az-1", "chefy:az-2", "--type", "blocks"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() project-qualified refs error = %v", err)
	}
	if add.Project != "chefy" || add.IssueID != "az-1" || add.DependsOnID != "az-2" || add.IssueProjectID != "chefy" || add.DependsOnProjectID != "chefy" {
		t.Fatalf("ParseIssueDependencyAddArgs() project-qualified refs = %+v", add)
	}
	_, err = ParseIssueDependencyAddArgs([]string{"chefy:az-1", "azedarach:az-2"})
	if err == nil || !strings.Contains(err.Error(), "dependency endpoints must be in the same project") {
		t.Fatalf("expected cross-project dependency rejection, got %v", err)
	}
	_, err = ParseIssueDependencyAddArgs([]string{"--project", "azedarach", "chefy:az-1", "chefy:az-2"})
	if err == nil || !strings.Contains(err.Error(), "does not match --project") {
		t.Fatalf("expected endpoint/project mismatch rejection, got %v", err)
	}

	remove, err := ParseIssueDependencyRemoveArgs([]string{"--type", "blocks", "--confirm", "--confirm-parent-orphan", "az-3", "az-4"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" || remove.Type != "blocks" || !remove.Confirm || !remove.ConfirmParentOrphan {
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

func TestParseIssueImageArgs(t *testing.T) {
	add, err := ParseIssueImageAddArgs([]string{"--issue-id", "az-1", "--path", "image.png"})
	if err != nil {
		t.Fatalf("ParseIssueImageAddArgs() error = %v", err)
	}
	if add.IssueID != "az-1" || add.SourcePath != "image.png" {
		t.Fatalf("ParseIssueImageAddArgs() = %+v", add)
	}
	add, err = ParseIssueImageAddArgs([]string{"az-2", "--path", "snap.png"})
	if err != nil {
		t.Fatalf("ParseIssueImageAddArgs() interspersed args error = %v", err)
	}
	if add.IssueID != "az-2" || add.SourcePath != "snap.png" {
		t.Fatalf("ParseIssueImageAddArgs() interspersed args = %+v", add)
	}
	_, err = ParseIssueImageAddArgs([]string{"--impl", "go-bubbletea", "az-1", "image.png"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue image add") {
		t.Fatalf("expected impl forbidden error for image add, got %v", err)
	}

	remove, err := ParseIssueImageRemoveArgs([]string{"--issue-id", "az-1", "--attachment-id", "abc123"})
	if err != nil {
		t.Fatalf("ParseIssueImageRemoveArgs() error = %v", err)
	}
	if remove.IssueID != "az-1" || remove.AttachmentID != "abc123" {
		t.Fatalf("ParseIssueImageRemoveArgs() = %+v", remove)
	}
	remove, err = ParseIssueImageRemoveArgs([]string{"az-2", "--attachment-id", "def456"})
	if err != nil {
		t.Fatalf("ParseIssueImageRemoveArgs() interspersed args error = %v", err)
	}
	if remove.IssueID != "az-2" || remove.AttachmentID != "def456" {
		t.Fatalf("ParseIssueImageRemoveArgs() interspersed args = %+v", remove)
	}
	_, err = ParseIssueImageRemoveArgs([]string{"--impl", "go-bubbletea", "az-1", "abc123"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue image remove") {
		t.Fatalf("expected impl forbidden error for image remove, got %v", err)
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
	estimateThree := 3
	estimateEight := 8
	tasks := []domain.Task{
		{
			ID:       "az-2",
			Title:    "Older issue",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Assignee: "alex",
			Estimate: &estimateThree,
			Implementations: []string{
				"default",
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:       "az-1",
			Title:    "Newest issue",
			Status:   domain.StatusInProgress,
			Priority: domain.P1,
			Type:     domain.TypeFeature,
			Assignee: "sam",
			Estimate: &estimateEight,
			Implementations: []string{
				"go-bubbletea",
			},
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
	if !strings.Contains(output, "ID") || !strings.Contains(output, "STATUS") || !strings.Contains(output, "PRIORITY") || !strings.Contains(output, "ASSIGNEE") || !strings.Contains(output, "EST") || !strings.Contains(output, "IMPL") || !strings.Contains(output, "TITLE") {
		t.Fatalf("output missing header: %q", output)
	}
	for _, want := range []string{"go-bubbletea", "default", "sam", "alex", "8", "3"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
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
		{ID: "az-2", Title: "Blocked", Status: domain.StatusInReview, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
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
		return IssueListCommand(deps, IssueListOptions{States: []domain.Status{domain.StatusOpen, domain.StatusInReview}})
	})
	if strings.Contains(output, "az-3") {
		t.Fatalf("status filter should exclude az-3: %q", output)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "az-2") {
		t.Fatalf("status filter should include matching issues: %q", output)
	}
}

func TestIssueListCommand_ParentFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")
	otherParentID := naming.IssueID("az-other-parent")
	tasks := []domain.Task{
		{ID: "az-1", ParentID: &parentID, Title: "Child One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
		{ID: "az-2", ParentID: &otherParentID, Title: "Child Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
		{ID: "az-3", Title: "Top Level", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
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
		return IssueListCommand(deps, IssueListOptions{ParentIDs: []string{"az-parent"}})
	})
	if strings.Contains(output, "az-2") || strings.Contains(output, "az-3") {
		t.Fatalf("parent filter should exclude non-matching tasks: %q", output)
	}
	if !strings.Contains(output, "az-1") {
		t.Fatalf("parent filter should include matching task: %q", output)
	}
}

func TestIssueListCommand_DependencyTargetFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID: "az-1", Title: "Depends on az-100", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour),
			Dependencies: []domain.Dependency{{ID: "az-100", Type: domain.DependencyBlocks}},
		},
		{
			ID: "az-2", Title: "Depends on az-200", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour),
			Dependencies: []domain.Dependency{{ID: "az-200", Type: domain.DependencyRelatedTo}},
		},
		{
			ID: "az-3", Title: "No dependencies", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour),
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
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{DependsOnIDs: []string{"az-100"}})
	})
	if strings.Contains(output, "az-2") || strings.Contains(output, "az-3") {
		t.Fatalf("dependency filter should exclude non-matching tasks: %q", output)
	}
	if !strings.Contains(output, "az-1") {
		t.Fatalf("dependency filter should include matching task: %q", output)
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
			Status:      domain.StatusInReview,
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

func TestIssueGetCommandUsesSingleIssueDaemonRead(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	var taskGetCalled bool
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					taskGetCalled = true
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task get body: %v", err)
					}
					if body.TaskID != "az-5" {
						t.Fatalf("task_id = %q, want az-5", body.TaskID)
					}
					bodyBytes, err := marshalTaskListBody([]domain.Task{{
						ID:        "az-5",
						Title:     "Lookup issue",
						Status:    domain.StatusOpen,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						CreatedAt: now,
						UpdatedAt: now,
					}})
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
						Body:            bodyBytes,
					}, nil
				case daemonclient.CommandDecisionLinkList:
					body, _ := json.Marshal(daemonclient.DecisionLinkListResult{})
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
					t.Fatalf("unexpected daemon command: %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	_ = captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5"})
	})
	if !taskGetCalled {
		t.Fatalf("expected task.get to be invoked")
	}
}

func TestIssueGetCommandTextHidesNotesByDefault(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Notes:       "First note line\nSecond note line",
			Status:      domain.StatusInReview,
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
	if !strings.Contains(output, "Notes: present (hidden in text output; use `az issue get az-5 --with-notes`") {
		t.Fatalf("output missing hidden notes sentinel: %q", output)
	}
	if strings.Contains(output, "First note line") || strings.Contains(output, "Second note line") {
		t.Fatalf("output should not include full notes by default: %q", output)
	}
}

func TestIssueGetCommandTextIncludesNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Notes:       "First note line\nSecond note line",
			Status:      domain.StatusInReview,
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
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5", IncludeNotes: true})
	})
	if !strings.Contains(output, "Notes:\nFirst note line\nSecond note line\n") {
		t.Fatalf("output missing notes section: %q", output)
	}
}

// issueGetDecisionMock answers task.get with the given task list and
// decision.link.list with the given enriched result. Other commands fail
// the test.
func issueGetDecisionMock(t *testing.T, tasks []domain.Task, decisionResult daemonclient.DecisionLinkListResult) *fakeDaemonTransport {
	t.Helper()
	return &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandTaskGet, daemonclient.CommandTaskList:
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
			case daemonclient.CommandDecisionLinkList:
				body, err := json.Marshal(decisionResult)
				if err != nil {
					t.Fatalf("marshal decision result: %v", err)
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
				t.Fatalf("unexpected command: %s", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		},
	}
}

func TestIssueGetCommandTextRendersDecisions(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-5",
			Title:     "Lookup issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	decisions := daemonclient.DecisionLinkListResult{
		Links: []daemonclient.DecisionLink{
			{DecisionID: "dec-1", TargetKind: daemonclient.DecisionTargetIssue, TargetID: "az-5", Relation: "applies-to"},
			{DecisionID: "dec-2", TargetKind: daemonclient.DecisionTargetIssue, TargetID: "az-5", Relation: "informs", Note: "discussed at sync"},
		},
		Decisions: []daemonclient.Decision{
			{ID: "dec-1", Title: "Use SQLite for decision store"},
			{ID: "dec-2", Title: "Polymorphic decision_links table"},
		},
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(issueGetDecisionMock(t, tasks, decisions)),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5"})
	})

	for _, want := range []string{
		"Decisions: 2",
		"applies-to",
		"dec-1",
		"Use SQLite for decision store",
		"informs",
		"dec-2",
		"Polymorphic decision_links table",
		"discussed at sync",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, output)
		}
	}
}

func TestIssueGetCommandJSONIncludesDecisions(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-5",
			Title:     "Lookup issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	decisions := daemonclient.DecisionLinkListResult{
		Links: []daemonclient.DecisionLink{
			{DecisionID: "dec-1", TargetKind: daemonclient.DecisionTargetIssue, TargetID: "az-5", Relation: "applies-to"},
		},
		Decisions: []daemonclient.Decision{
			{ID: "dec-1", Title: "Use SQLite for decision store"},
		},
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(issueGetDecisionMock(t, tasks, decisions)),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5", JSON: true})
	})

	if !strings.Contains(output, "\"id\": \"az-5\"") {
		t.Fatalf("output missing task id: %q", output)
	}
	if !strings.Contains(output, "\"decisions\"") {
		t.Fatalf("output missing decisions key: %q", output)
	}
	for _, want := range []string{
		"\"id\": \"dec-1\"",
		"\"title\": \"Use SQLite for decision store\"",
		"\"relation\": \"applies-to\"",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("decisions json missing %q\nfull output:\n%s", want, output)
		}
	}

	// And the JSON must still parse with the task fields at the top level so
	// existing consumers that unmarshal into a Task-like shape keep working.
	var parsed struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Decisions []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("decode envelope: %v\nraw=%s", err, output)
	}
	if parsed.ID != "az-5" || parsed.Title != "Lookup issue" {
		t.Fatalf("envelope task fields = %+v", parsed)
	}
	if len(parsed.Decisions) != 1 || parsed.Decisions[0].ID != "dec-1" {
		t.Fatalf("envelope decisions = %+v", parsed.Decisions)
	}
}

func TestIssueGetCommandTextIncludesRuntimeGitAndImplementations(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	started := now.Add(-15 * time.Minute)
	estimate := 13
	tasks := []domain.Task{
		{
			ID:                    "az-5",
			Title:                 "Lookup issue",
			Design:                "Design notes",
			Acceptance:            "- acceptance one",
			Assignee:              "sam",
			Labels:                []string{"cli", "notes"},
			Estimate:              &estimate,
			Status:                domain.StatusInReview,
			Priority:              domain.P0,
			Type:                  domain.TypeBug,
			Implementations:       []string{"default", "go-bubbletea"},
			Session:               &domain.Session{IssueID: "az-5", State: domain.SessionBusy, TmuxAttached: true, TmuxAttachedCount: 1, StartedAt: &started},
			HasTmuxSession:        true,
			HasWorktree:           true,
			HasUncommittedChanges: true,
			GitAdditions:          12,
			GitDeletions:          3,
			GitAheadCount:         2,
			GitBehindCount:        1,
			CreatedAt:             now,
			UpdatedAt:             now,
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
	for _, want := range []string{
		"Implementations: default, go-bubbletea",
		"Assignee: sam",
		"Labels: cli, notes",
		"Estimate: 13",
		"Runtime: session=busy tmux_attached=yes since 2026-03-25T10:45:00Z, worktree=yes",
		"Git: dirty, +12/-3, ahead=2 behind=1",
		"Design:\nDesign notes\n",
		"Acceptance:\n- acceptance one\n",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
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
		{ID: "az-1", Title: "First", Notes: "first notes", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "az-2", Title: "Second", Notes: "second notes", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeFeature, Dependencies: []domain.Dependency{{ID: "az-1", Type: domain.DependencyBlocks}}, CreatedAt: now, UpdatedAt: now},
	}
	var commands []string
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				if req.Command != daemonclient.CommandTaskGetMany {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskGetMany)
				}
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
	if len(commands) != 1 {
		t.Fatalf("commands = %v, want one batch read", commands)
	}
	if len(got.Results) != 3 {
		t.Fatalf("results length = %d, want 3", len(got.Results))
	}
	if got.Results[0].ID != "az-2" || got.Results[0].Status != "found" {
		t.Fatalf("result[0] = %+v", got.Results[0])
	}
	if got.Results[0].Issue == nil || got.Results[0].Issue.Notes != "" {
		t.Fatalf("result[0] notes should be omitted by default: %+v", got.Results[0].Issue)
	}
	if len(got.Results[0].Dependencies) != 1 || got.Results[0].Dependencies[0].ID != "az-1" {
		t.Fatalf("result[0] dependencies = %+v", got.Results[0].Dependencies)
	}
	if got.Results[1].ID != "az-missing" || got.Results[1].Status != "not_found" {
		t.Fatalf("result[1] = %+v", got.Results[1])
	}
	if got.Results[2].ID != "az-1" || got.Results[2].Status != "found" {
		t.Fatalf("result[2] = %+v", got.Results[2])
	}
	if len(got.Results[2].Dependents) != 1 || got.Results[2].Dependents[0].ID != "az-2" {
		t.Fatalf("result[2] dependents = %+v", got.Results[2].Dependents)
	}
}

func TestIssueGetManyCommand_JSONIncludesNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 26, 3, 15, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "First", Notes: "first notes", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
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
			IssueIDs:     []string{"az-1"},
			JSON:         true,
			IncludeNotes: true,
		})
	})
	var got issueGetManyResult
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal get-many output: %v", err)
	}
	if got.Results[0].Issue == nil || got.Results[0].Issue.Notes != "first notes" {
		t.Fatalf("result notes = %+v, want included notes", got.Results[0].Issue)
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
			wantCommand: daemonclient.CommandTaskClose,
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
						} else if req.Command == daemonclient.CommandTaskList {
							payload, err := marshalTaskListBody([]domain.Task{
								{ID: "az-9", Title: "Close me", Status: domain.StatusInReview},
							})
							if err != nil {
								t.Fatalf("marshal task list response: %v", err)
							}
							body = payload
						} else if req.Command == daemonclient.CommandTaskClose {
							payload, err := json.Marshal(daemonclient.TaskCloseResult{
								TaskID: "az-9",
								Status: string(domain.StatusDone),
							})
							if err != nil {
								t.Fatalf("marshal task close response: %v", err)
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
				var closeReq struct {
					TaskID naming.IssueID `json:"task_id"`
				}
				if err := json.Unmarshal(gotReq.Body, &closeReq); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if closeReq.TaskID != "az-9" {
					t.Fatalf("close body = %+v, want task_id=az-9", closeReq)
				}
			}
			if !strings.Contains(output, tt.wantText) {
				t.Fatalf("output missing %q: %q", tt.wantText, output)
			}
		})
	}
}

func TestIssueCloseCommandConfirmedCleanupStopsClosesAndRemovesWorktree(t *testing.T) {
	var commands []string
	var closeForce bool
	worktreeListBody, err := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		Worktrees []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		} `json:"worktrees"`
	}{
		ProjectID: "proj",
		Worktrees: []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		}{
			{Path: "/tmp/az-9", Branch: "riordan/az-9/finish-flow", IssueID: "az-9"},
		},
	})
	if err != nil {
		t.Fatalf("marshal worktree list: %v", err)
	}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{
			ID:             "az-9",
			Title:          "Finish flow",
			Status:         domain.StatusInProgress,
			HasTmuxSession: true,
			HasWorktree:    true,
			Session:        &domain.Session{IssueID: naming.IssueID("az-9"), Worktree: "/tmp/az-9"},
		},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            worktreeListBody,
					}, nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-9", TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{HasChanges: false}}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "main",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Branch:   "main",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: ".",
						Branch:   "riordan/az-9/finish-flow",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				case daemonclient.CommandTaskClose:
					deadline, ok := ctx.Deadline()
					if !ok {
						t.Fatal("task close context has no deadline")
					}
					if remaining := time.Until(deadline); remaining < issueCloseCleanupTimeout-10*time.Second {
						t.Fatalf("task close timeout budget = %s, want near %s", remaining, issueCloseCleanupTimeout)
					}
					var body struct {
						ForceWorktree        bool `json:"force_worktree"`
						IntegrateBeforeClose bool `json:"integrate_before_close"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task close body: %v", err)
					}
					closeForce = body.ForceWorktree
					if !body.IntegrateBeforeClose {
						t.Fatalf("task close integrate_before_close = false, want true")
					}
					return responseWithJSON(req, daemonclient.TaskCloseResult{
						TaskID:                 "az-9",
						Status:                 string(domain.StatusDone),
						IntegrationRequested:   true,
						Integrated:             true,
						IntegratedSourceBranch: "riordan/az-9/finish-flow",
						IntegratedTargetBranch: "main",
						SessionStopped:         true,
						WorktreeRemoved:        true,
						WorktreeForced:         body.ForceWorktree,
						Phases: []daemonclient.TaskClosePhaseTiming{
							{Name: "integrate_before_close", ElapsedMS: 123},
							{Name: "session_cleanup", ElapsedMS: 0, Skipped: true},
						},
					}), nil
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
		return IssueCloseCommand(deps, IssueCloseOptions{
			IssueID:       "az-9",
			ForceWorktree: true,
		})
	})

	wantCommands := []string{
		daemonclient.CommandTaskClose,
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if !closeForce {
		t.Fatal("task close force_worktree = false, want true")
	}
	if !strings.Contains(output, "Closed issue: az-9") || !strings.Contains(output, "- Integration requested") || !strings.Contains(output, "- Cleanup performed") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "- Phase timings:") || !strings.Contains(output, "integrate_before_close: 123ms") || !strings.Contains(output, "session_cleanup: 0s (skipped)") {
		t.Fatalf("output = %q", output)
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
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-parent")
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
				case daemonclient.CommandTaskDependencyAdd:
					body = []byte(`{}`)
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
			Title:                  "Child issue",
			Type:                   domain.TypeTask,
			Priority:               domain.P2,
			AutoParentFromIssueID:  &parentID,
			AutoCreatedFromIssueID: &parentID,
		})
	})

	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskGetMany {
		t.Fatalf("requests[0].Command = %q, want %q", requests[0].Command, daemonclient.CommandTaskGetMany)
	}
	if requests[1].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("requests[1].Command = %q, want %q", requests[1].Command, daemonclient.CommandTaskCreate)
	}
	if requests[2].Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("requests[2].Command = %q, want %q", requests[2].Command, daemonclient.CommandTaskDependencyAdd)
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
	var depReq daemonclient.TaskDependencyParams
	if err := json.Unmarshal(requests[2].Body, &depReq); err != nil {
		t.Fatalf("unmarshal dependency body: %v", err)
	}
	if depReq.TaskID != "az-child" || depReq.DependsOnID != "az-parent" || depReq.Type != string(domain.DependencyCreatedIn) {
		t.Fatalf("dependency body = %+v, want az-child created-in az-parent", depReq)
	}
	if !strings.Contains(output, "Created issue: az-child (parent: az-parent, auto-parent from AZEDARACH_ISSUE_ID) [created-from: az-parent]") {
		t.Fatalf("output missing auto-parent/provenance message: %q", output)
	}
}

func TestIssueCreateCommandAutoParentsFromTmuxSessionWhenEnvMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	previousTmuxPaneSessionName := tmuxPaneSessionName
	tmuxPaneSessionName = func(context.Context) (string, error) {
		return "pr-az-parent", nil
	}
	t.Cleanup(func() {
		tmuxPaneSessionName = previousTmuxPaneSessionName
	})

	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				body := []byte{}
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-parent")
					tasks, err := marshalTaskListBody([]domain.Task{{
						ID:              "az-parent",
						Title:           "Parent",
						Status:          domain.StatusInProgress,
						Priority:        domain.P1,
						Type:            domain.TypeTask,
						Implementations: []string{"go-bubbletea"},
					}})
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
				case daemonclient.CommandTaskDependencyAdd:
					body = []byte(`{}`)
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
		RepoDir:   "/tmp/proj",
	}

	output := captureStdout(t, func() error {
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:    "Child issue",
			Type:     domain.TypeTask,
			Priority: domain.P2,
		})
	})

	if len(requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskGetMany || requests[1].Command != daemonclient.CommandTaskGetMany {
		t.Fatalf("first requests = %s, %s; want targeted task metadata confirmation then parent resolution", requests[0].Command, requests[1].Command)
	}
	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[2].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID == nil || *createReq.ParentID != "az-parent" {
		t.Fatalf("create parent = %+v, want az-parent", createReq.ParentID)
	}
	if !strings.Contains(output, "Created issue: az-child (parent: az-parent, auto-parent from AZEDARACH_ISSUE_ID) [created-from: az-parent]") {
		t.Fatalf("output missing tmux auto-parent/provenance message: %q", output)
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
				if req.Command == daemonclient.CommandTaskDependencyAdd {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            []byte(`{}`),
					}, nil
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
			Title:                  "Child issue",
			Type:                   domain.TypeTask,
			Priority:               domain.P4,
			Deferred:               true,
			AutoParentFromIssueID:  &parentID,
			AutoCreatedFromIssueID: &parentID,
			Implementations:        []string{"default"},
		})
	})

	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("requests[0].Command = %q, want %q", requests[0].Command, daemonclient.CommandTaskCreate)
	}
	if requests[1].Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("requests[1].Command = %q, want %q", requests[1].Command, daemonclient.CommandTaskDependencyAdd)
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
	var depReq daemonclient.TaskDependencyParams
	if err := json.Unmarshal(requests[1].Body, &depReq); err != nil {
		t.Fatalf("unmarshal dependency body: %v", err)
	}
	if depReq.TaskID != "az-child" || depReq.DependsOnID != "az-parent" || depReq.Type != string(domain.DependencyCreatedIn) {
		t.Fatalf("dependency body = %+v, want az-child created-in az-parent", depReq)
	}
	if !strings.Contains(output, "Created issue: az-child [created-from: az-parent] [deferred: standalone later work, not auto-parented]") {
		t.Fatalf("output missing deferred/provenance message: %q", output)
	}
}

func TestIssueCreateCommandCrossProjectSkipsImplicitAutoParentAndCreatedFrom(t *testing.T) {
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				if req.Command == daemonclient.CommandTaskList {
					t.Fatalf("unexpected %s request for cross-project create", daemonclient.CommandTaskList)
				}
				if req.Command == daemonclient.CommandTaskDependencyAdd {
					t.Fatalf("unexpected %s request for cross-project create", daemonclient.CommandTaskDependencyAdd)
				}
				payload, err := json.Marshal(map[string]string{"task_id": "cnd"})
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
		}).WithProjectID("chefy"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "chefy",
	}

	output := captureStdout(t, func() error {
		activeID := "eik"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Project:                "azedarach",
			Title:                  "Prevent worker self-delivery",
			Type:                   domain.TypeBug,
			Priority:               domain.P2,
			AutoParentFromIssueID:  &activeID,
			AutoCreatedFromIssueID: &activeID,
			Implementations:        []string{"default"},
		})
	})

	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("request command = %q, want %q", requests[0].Command, daemonclient.CommandTaskCreate)
	}
	if requests[0].Meta.ProjectID.String() != "azedarach" {
		t.Fatalf("request project = %q, want azedarach", requests[0].Meta.ProjectID)
	}
	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[0].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID != nil {
		t.Fatalf("create parent = %+v, want nil", createReq.ParentID)
	}
	if !strings.Contains(output, "Created issue: azedarach:cnd") {
		t.Fatalf("output missing project-qualified created issue: %q", output)
	}
	if strings.Contains(output, "created-from") || strings.Contains(output, "parent:") {
		t.Fatalf("output should not mention implicit parent/provenance for cross-project create: %q", output)
	}
}

func TestIssueCreateCommandReportsPartialSuccessWhenCreatedFromEdgeFails(t *testing.T) {
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				body := []byte{}
				switch req.Command {
				case daemonclient.CommandTaskCreate:
					payload, err := json.Marshal(map[string]string{"task_id": "az-child"})
					if err != nil {
						t.Fatalf("marshal task create response: %v", err)
					}
					body = payload
				case daemonclient.CommandTaskDependencyAdd:
					return protocol.ResponseEnvelope{}, fmt.Errorf("not found")
				default:
					t.Fatalf("unexpected command %s", req.Command)
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
		}).WithProjectID("azedarach"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "azedarach",
	}

	output, err := captureStdoutAllowError(t, func() error {
		activeID := "az-parent"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:                  "Child issue",
			Type:                   domain.TypeTask,
			Priority:               domain.P2,
			Deferred:               true,
			AutoCreatedFromIssueID: &activeID,
			Implementations:        []string{"default"},
		})
	})

	if output != "" {
		t.Fatalf("stdout = %q, want empty on text partial error", output)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	var partial issueCreatePartialError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want issueCreatePartialError", err, err)
	}
	if partial.Result.IssueID != "az-child" || partial.Result.ProjectID != "azedarach" || partial.Result.CreatedFromID != "az-parent" {
		t.Fatalf("partial result = %+v", partial.Result)
	}
	if !strings.Contains(err.Error(), "issue creation partially succeeded: created azedarach:az-child") {
		t.Fatalf("error missing created issue: %v", err)
	}
	if !strings.Contains(err.Error(), "azedarach:az-child -> azedarach:az-parent") {
		t.Fatalf("error missing qualified edge ids: %v", err)
	}
}

func TestIssueSplitCommandCreatesChildAndStartsOrchestratedSession(t *testing.T) {
	root := naming.IssueID("az-parent")
	child := naming.IssueID("az-child")
	var requests []protocol.RequestEnvelope
	var createReq daemonclient.TaskCreateParams
	var submitted protocol.OperationSubmitRequestBody
	taskListCalls := 0
	deps := &Dependencies{
		Config:    config.DefaultConfig(),
		RepoDir:   "/repo",
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					taskListCalls++
					tasks := []domain.Task{{
						ID:              root,
						Title:           "Parent",
						Status:          domain.StatusInProgress,
						Priority:        domain.P1,
						Type:            domain.TypeTask,
						Implementations: []string{"go-bubbletea"},
					}}
					if taskListCalls > 1 {
						tasks = append(tasks, domain.Task{
							ID:       child,
							Title:    "Child work",
							Status:   domain.StatusOpen,
							Priority: domain.P2,
							Type:     domain.TypeTask,
							ParentID: &root,
						})
					}
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandTaskGetMany:
					var getManyReq daemonclient.TaskIDsRequest
					if err := json.Unmarshal(req.Body, &getManyReq); err != nil {
						t.Fatalf("decode task.get_many request: %v", err)
					}
					if len(getManyReq.TaskIDs) != 1 || !getManyReq.IncludeAncestors || !getManyReq.ExcludeDependents || !getManyReq.MetadataOnly {
						t.Fatalf("task.get_many request = %+v, want one metadata-only issue with ancestors/no dependents", getManyReq)
					}
					var tasks []domain.Task
					switch getManyReq.TaskIDs[0] {
					case root:
						tasks = []domain.Task{{
							ID:              root,
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"go-bubbletea"},
						}}
					case child:
						tasks = []domain.Task{{
							ID:       child,
							Title:    "Child work",
							Status:   domain.StatusOpen,
							Priority: domain.P2,
							Type:     domain.TypeTask,
							ParentID: &root,
						}, {
							ID:              root,
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"go-bubbletea"},
						}}
					default:
						t.Fatalf("unexpected task.get_many id: %+v", getManyReq.TaskIDs)
					}
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal task get-many response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createReq); err != nil {
						t.Fatalf("decode create request: %v", err)
					}
					return responseWithJSON(req, map[string]string{"task_id": child.String()}), nil
				case daemonclient.CommandTaskDependencyAdd:
					var depReq daemonclient.TaskDependencyParams
					if err := json.Unmarshal(req.Body, &depReq); err != nil {
						t.Fatalf("decode dependency request: %v", err)
					}
					if depReq.TaskID != child || depReq.DependsOnID != root || depReq.Type != string(domain.DependencyCreatedIn) {
						t.Fatalf("dependency request = %+v, want child created-in root", depReq)
					}
					return responseWithJSON(req, map[string]any{}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": root.String(), "path": "/repo-az-parent", "branch": "user/az-parent/parent-work"},
							{"issue_id": child.String(), "path": "/repo-az-child", "branch": "user/az-child/child-work"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        child.String(),
						TargetID:       root.String(),
						Branch:         "user/az-parent/parent-work",
						WorktreePath:   "/repo-az-parent",
						BranchAttached: true,
						AncestorChain:  []string{root.String()},
					}), nil
				case protocol.CommandOperationSubmit:
					if err := json.Unmarshal(req.Body, &submitted); err != nil {
						t.Fatalf("decode operation submit: %v", err)
					}
					return responseWithJSON(req, protocol.OperationSubmitResponseBody{
						Created: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-split",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateQueued,
						},
					}), nil
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-split",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateDone,
						},
					}), nil
				case protocol.CommandMailSend:
					return responseWithJSON(req, protocol.MailEvent{Seq: 1, ParentIssue: root.String(), IssueID: child, Type: "session-started"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return IssueSplitCommand(deps, IssueSplitOptions{
			ParentIssueID: root.String(),
			Title:         "Child work",
			Type:          domain.TypeTask,
			Priority:      domain.P2,
			JSON:          true,
		})
	})

	var result issueSplitResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result.ChildIssueID != child.String() || len(result.Start.Started) != 1 || result.Start.Started[0] != child.String() {
		t.Fatalf("result = %+v", result)
	}
	if createReq.ParentID == nil || *createReq.ParentID != root {
		t.Fatalf("create parent = %+v, want %s", createReq.ParentID, root)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("create implementations = %+v, want inherited parent impl", createReq.Implementations)
	}
	if submitted.Kind != commandSessionStart || submitted.IssueID != child {
		t.Fatalf("submitted = %+v", submitted)
	}
	var sessionReq sessionRequestBody
	if err := json.Unmarshal(submitted.Payload, &sessionReq); err != nil {
		t.Fatalf("decode submitted session payload: %v", err)
	}
	if sessionReq.BaseBranch != "user/az-parent/parent-work" {
		t.Fatalf("submitted base_branch = %q, want parent worktree branch", sessionReq.BaseBranch)
	}
	if !strings.Contains(result.Advice.IntegrateCommand, child.String()) {
		t.Fatalf("advice = %+v, want child integration command", result.Advice)
	}
	if !strings.Contains(result.Advice.MergeCommand, child.String()) || !strings.Contains(result.Advice.Summary, "not merged at creation") {
		t.Fatalf("advice = %+v, want explicit review/close guidance", result.Advice)
	}
	commands := commandNames(requests)
	if !containsString(commands, protocol.CommandOperationSubmit) || !containsString(commands, protocol.CommandMailSend) {
		t.Fatalf("commands = %+v, want operation submit and mail send", commands)
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

func TestIssueCreateCommandSuggestsImplWhenInferenceUnavailable(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{}, errors.New("transport unavailable")
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

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:    "Needs impl",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --impl (unable to infer implementation automatically:") || !strings.Contains(err.Error(), "Specify --impl <implementation>") {
		t.Fatalf("expected actionable impl inference failure, got %v", err)
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
				case daemonclient.CommandTaskGet:
					var getBody daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &getBody); err != nil {
						t.Fatalf("unmarshal task get body: %v", err)
					}
					if getBody.TaskID != "az-1" {
						t.Fatalf("task_id = %q, want az-1", getBody.TaskID)
					}
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
						t.Fatalf("marshal task get: %v", err)
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
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Check target",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusOpen,
							CreatedAt: now,
							UpdatedAt: now,
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
					return responseWithJSON(req, daemonclient.TaskDeleteResult{
						TaskID:   "az-1",
						Deleted:  true,
						Revision: 3,
					}), nil
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
	cfg := config.DefaultConfig()
	cfg.IssueResources.PrepareCommands = []string{"just prepare"}
	cfg.IssueResources.ReconcileCommand = "just reconcile"
	deps.Config = cfg
	doctorOut = captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(doctorOut, "Doctor: WARN az-1") ||
		!strings.Contains(doctorOut, "issueResources config mixes reconcileCommand") {
		t.Fatalf("doctor mixed lifecycle output = %q", doctorOut)
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
	deleteCalled := false
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskDelete:
					deleteCalled = true
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              false,
						CompletedAt:     req.SentAt,
						Error: &protocol.ErrorEnvelope{
							Code:    protocol.ErrorCodeInternal,
							Message: "cannot delete issue az-1: runtime metadata fields still present (session, worktree); repair with az issue delete az-1 --confirm --cleanup --remove-worktree --force-worktree, or rerun with stop_session remove_worktree",
						},
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
	if err == nil || !strings.Contains(err.Error(), "cannot delete issue az-1: runtime metadata fields still present (session, worktree)") ||
		!strings.Contains(err.Error(), "az issue delete az-1 --confirm --cleanup --remove-worktree --force-worktree") {
		t.Fatalf("IssueDeleteCommand() error = %v", err)
	}
	if !deleteCalled {
		t.Fatal("IssueDeleteCommand() did not call daemon task.delete")
	}
}

func TestIssueDeleteCommandCleansRuntimeAttachmentsBeforeDelete(t *testing.T) {
	commands := []string{}
	var deleteCleanup bool
	var deleteStopSession bool
	var deleteRemoveWorktree bool
	var deleteForceWorktree bool
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskDelete:
					var body struct {
						Cleanup        bool `json:"cleanup"`
						StopSession    bool `json:"stop_session"`
						RemoveWorktree bool `json:"remove_worktree"`
						ForceWorktree  bool `json:"force_worktree"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task delete body: %v", err)
					}
					deleteCleanup = body.Cleanup
					deleteStopSession = body.StopSession
					deleteRemoveWorktree = body.RemoveWorktree
					deleteForceWorktree = body.ForceWorktree
					respBody, err := json.Marshal(daemonclient.TaskDeleteResult{
						TaskID:          "az-1",
						Deleted:         true,
						SessionStopped:  true,
						WorktreeRemoved: true,
						WorktreeForced:  true,
						Revision:        3,
					})
					if err != nil {
						t.Fatalf("marshal task delete response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            respBody,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueDeleteCommand(deps, IssueDeleteOptions{
			IssueID:        "az-1",
			Confirm:        true,
			StopSession:    true,
			RemoveWorktree: true,
			ForceWorktree:  true,
		})
	})

	wantCommands := []string{
		daemonclient.CommandTaskDelete,
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if deleteCleanup || !deleteStopSession || !deleteRemoveWorktree || !deleteForceWorktree {
		t.Fatalf("task delete cleanup flags cleanup=%v stop=%v remove=%v force=%v, want stop/remove/force only", deleteCleanup, deleteStopSession, deleteRemoveWorktree, deleteForceWorktree)
	}
	for _, want := range []string{"Deleted issue: az-1", "- Session stopped", "- Worktree removed", "- Worktree removal forced"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want substring %q", output, want)
		}
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
				case daemonclient.CommandTaskGet:
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
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	if err := json.Unmarshal(gotUpdateReq.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	if updateBody.Title != "New" || updateBody.Description != "OldDesc" {
		t.Fatalf("update body = %+v, want new title with preserved description", updateBody)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueUpdateCommandCanClearDescription(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
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
			IssueID:        "az-1",
			DescriptionSet: true,
		})
	})
	if gotUpdateReq.Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update command = %q, want %q", gotUpdateReq.Command, daemonclient.CommandTaskUpdate)
	}
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	if err := json.Unmarshal(gotUpdateReq.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	if updateBody.TaskID != "az-1" || updateBody.Title != "Old" || updateBody.Description != "" {
		t.Fatalf("update body = %+v, want preserved title and cleared description", updateBody)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueUpdateCommandConfirmedClosedCleansBeforeStatus(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	status := domain.StatusDone
	commands := make([]string, 0, 5)
	var closeForce bool
	worktreeListBody, err := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		Worktrees []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		} `json:"worktrees"`
	}{
		ProjectID: "proj",
		Worktrees: []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		}{
			{Path: "/tmp/az-1", Branch: "riordan/az-1/ready", IssueID: "az-1"},
		},
	})
	if err != nil {
		t.Fatalf("marshal worktree list: %v", err)
	}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{
			ID:             "az-1",
			Title:          "Ready",
			Type:           domain.TypeTask,
			Priority:       domain.P2,
			Status:         domain.StatusInReview,
			CreatedAt:      now,
			UpdatedAt:      now,
			HasTmuxSession: true,
			HasWorktree:    true,
			Session:        &domain.Session{IssueID: naming.IssueID("az-1"), Worktree: "/tmp/az-1"},
		},
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
				case daemonclient.CommandTaskGet:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskUpdate:
					return responseWithJSON(req, map[string]any{}), nil
				case daemonclient.CommandWorktreeList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            worktreeListBody,
					}, nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-1", TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{HasChanges: false}}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "main",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Branch:   "main",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: ".",
						Branch:   "riordan/az-1/ready",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				case daemonclient.CommandTaskClose:
					var body struct {
						ForceWorktree        bool `json:"force_worktree"`
						IntegrateBeforeClose bool `json:"integrate_before_close"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task close body: %v", err)
					}
					closeForce = body.ForceWorktree
					if !body.IntegrateBeforeClose {
						t.Fatalf("task close integrate_before_close = false, want true")
					}
					return responseWithJSON(req, daemonclient.TaskCloseResult{
						TaskID:                 "az-1",
						Status:                 string(domain.StatusDone),
						IntegrationRequested:   true,
						Integrated:             true,
						IntegratedSourceBranch: "riordan/az-1/ready",
						IntegratedTargetBranch: "main",
						SessionStopped:         true,
						WorktreeRemoved:        true,
						WorktreeForced:         body.ForceWorktree,
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID:       "az-1",
			Status:        &status,
			ForceWorktree: true,
		})
	})

	wantCommands := []string{
		daemonclient.CommandTaskGet,
		daemonclient.CommandTaskUpdate,
		daemonclient.CommandTaskClose,
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if !closeForce {
		t.Fatal("task close force_worktree = false, want true")
	}
	if !strings.Contains(output, "Updated issue: az-1") {
		t.Fatalf("output = %q", output)
	}
}

func TestIssueUpdateCommandReplacesNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Notes:       "Existing notes",
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

	notes := "Replacement notes"
	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID: "az-1",
			Notes:   &notes,
		})
	})
	if gotUpdateReq.Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update command = %q, want %q", gotUpdateReq.Command, daemonclient.CommandTaskUpdate)
	}
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	if err := json.Unmarshal(gotUpdateReq.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	if updateBody.TaskID != "az-1" || updateBody.Notes == nil || *updateBody.Notes != "Replacement notes" {
		t.Fatalf("update body = %+v", updateBody)
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
				case daemonclient.CommandTaskGet:
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
			IssueID:           "az-5",
			DependsOnID:       "az-2",
			Type:              "parent-child",
			ForceParentChange: true,
		})
	})
	if gotAddReq.Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("add command = %q, want %q", gotAddReq.Command, daemonclient.CommandTaskDependencyAdd)
	}
	var addBody daemonclient.TaskDependencyParams
	if err := json.Unmarshal(gotAddReq.Body, &addBody); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if addBody.TaskID != "az-5" || addBody.DependsOnID != "az-2" || addBody.Type != "parent-child" || !addBody.ForceParentChange {
		t.Fatalf("add body = %+v", addBody)
	}
	if !strings.Contains(addOut, "Added dependency: az-5 --(parent-child)--> az-2") {
		t.Fatalf("add output = %q", addOut)
	}
	if !strings.Contains(addOut, "This makes az-5 a child of az-2.") {
		t.Fatalf("add output = %q", addOut)
	}

	removeOut := captureStdout(t, func() error {
		return IssueDependencyRemoveCommand(deps, IssueDependencyRemoveOptions{
			IssueID:             "az-5",
			DependsOnID:         "az-2",
			Type:                "parent-child",
			Confirm:             true,
			ConfirmParentOrphan: true,
		})
	})
	if gotRemoveReq.Command != daemonclient.CommandTaskDependencyRemove {
		t.Fatalf("remove command = %q, want %q", gotRemoveReq.Command, daemonclient.CommandTaskDependencyRemove)
	}
	var removeBody daemonclient.TaskDependencyRemoveParams
	if err := json.Unmarshal(gotRemoveReq.Body, &removeBody); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if removeBody.TaskID != "az-5" || removeBody.DependsOnID != "az-2" || removeBody.Type != "parent-child" || !removeBody.Confirm || !removeBody.ConfirmParentOrphan {
		t.Fatalf("remove body = %+v", removeBody)
	}
	if !strings.Contains(removeOut, "Removed dependency: az-5 --(parent-child)--> az-2") {
		t.Fatalf("remove output = %q", removeOut)
	}
}

func TestIssueDependencyAddCommandCarriesProjectQualifiedEndpointMetadata(t *testing.T) {
	var gotReq protocol.RequestEnvelope
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
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	opts, err := ParseIssueDependencyAddArgs([]string{"chefy:az-5", "chefy:az-2", "--type", "blocks"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() error = %v", err)
	}
	_ = captureStdout(t, func() error {
		return IssueDependencyAddCommand(deps, opts)
	})

	if gotReq.Meta.ProjectID.String() != "chefy" {
		t.Fatalf("request project = %q, want chefy", gotReq.Meta.ProjectID)
	}
	var body daemonclient.TaskDependencyParams
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if body.TaskID != "az-5" || body.DependsOnID != "az-2" || body.IssueProjectID != "chefy" || body.DependsOnProjectID != "chefy" {
		t.Fatalf("dependency body = %+v", body)
	}
}

func TestIssueDependencyAddParentChildErrorIncludesDirectionGuidance(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:    protocol.ErrorCodeInternal,
						Message: "refusing to change parent for az-5: current parent az-1, requested parent az-2",
					},
					CompletedAt: req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueDependencyAddCommand(deps, IssueDependencyAddOptions{
		IssueID:     "az-5",
		DependsOnID: "az-2",
		Type:        "parent-child",
	})
	if err == nil {
		t.Fatal("IssueDependencyAddCommand() error = nil, want parent-change guidance")
	}
	msg := err.Error()
	for _, want := range []string{
		"This would make az-5 a child of az-2",
		"az issue dep add az-2 az-5 --type parent-child",
		"--force-parent-change",
		"current parent az-1, requested parent az-2",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestIssueImageCommands(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	tempRepo := t.TempDir()
	sourceImage := filepath.Join(t.TempDir(), "screenshot.png")
	if err := os.WriteFile(sourceImage, []byte("fake-png"), 0o644); err != nil {
		t.Fatalf("write source image: %v", err)
	}

	var appendNotesReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Task",
							Status:    domain.StatusOpen,
							CreatedAt: now,
							UpdatedAt: now,
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
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskAppendNotes:
					appendNotesReq = req
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
		RepoDir:   tempRepo,
	}

	addOut := captureStdout(t, func() error {
		return IssueImageAddCommand(deps, IssueImageAddOptions{
			IssueID:    "az-1",
			SourcePath: sourceImage,
		})
	})
	if !strings.Contains(addOut, "Attached image to issue az-1:") {
		t.Fatalf("add output = %q", addOut)
	}
	if appendNotesReq.Command != daemonclient.CommandTaskAppendNotes {
		t.Fatalf("append notes command = %q, want %q", appendNotesReq.Command, daemonclient.CommandTaskAppendNotes)
	}
	var appendBody daemonclient.TaskAppendNotesRequest
	if err := json.Unmarshal(appendNotesReq.Body, &appendBody); err != nil {
		t.Fatalf("unmarshal append notes body: %v", err)
	}
	if appendBody.TaskID != "az-1" || !strings.Contains(appendBody.Line, ".azedarach/images/az-1/") {
		t.Fatalf("append body = %+v", appendBody)
	}

	files, err := filepath.Glob(filepath.Join(tempRepo, ".azedarach", "images", "az-1", "*-screenshot.png"))
	if err != nil {
		t.Fatalf("glob attachments: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("attachment files = %v, want 1 file", files)
	}
	filename := filepath.Base(files[0])
	parts := strings.SplitN(filename, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("attachment filename = %q, want <id>-<name> format", filename)
	}

	removeOut := captureStdout(t, func() error {
		return IssueImageRemoveCommand(deps, IssueImageRemoveOptions{
			IssueID:      "az-1",
			AttachmentID: parts[0],
		})
	})
	if !strings.Contains(removeOut, "Removed image attachment "+parts[0]+" from issue az-1") {
		t.Fatalf("remove output = %q", removeOut)
	}
	if _, statErr := os.Stat(files[0]); !os.IsNotExist(statErr) {
		t.Fatalf("attachment file still exists after remove: statErr=%v", statErr)
	}
}

func TestIssueBulkCommandsUseApplyCommand(t *testing.T) {
	tempDir := t.TempDir()
	bulkCreatePath := filepath.Join(tempDir, "bulk-create.json")
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update.json")
	if err := os.WriteFile(bulkCreatePath, []byte(`[{"title":"Bulk epic","description":"Parent","type":"epic","priority":"P2","children":[{"title":"Bulk child","description":"Child","type":"task","priority":"P3"}]}]`), 0o644); err != nil {
		t.Fatalf("write bulk-create file: %v", err)
	}
	if err := os.WriteFile(bulkUpdatePath, []byte(`[{"task_id":"az-1","title":"Renamed","priority":"P1"}]`), 0o644); err != nil {
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
						ID:       "az-1",
						Title:    "Old",
						Type:     domain.TypeTask,
						Priority: domain.P2,
						Status:   domain.StatusOpen,
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
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:          "az-1",
						Title:       "Old",
						Description: "Desc",
						Type:        domain.TypeTask,
						Priority:    domain.P2,
						Status:      domain.StatusOpen,
					}})
					if err != nil {
						t.Fatalf("marshal get response: %v", err)
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
	if len(createBody.Operations) != 2 || createBody.Operations[0].Command != daemonclient.CommandTaskCreate || createBody.Operations[1].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("create operations = %+v", createBody.Operations)
	}
	var parentCreate struct {
		Title           string   `json:"title"`
		Description     string   `json:"description"`
		Type            string   `json:"type"`
		Priority        string   `json:"priority"`
		Implementations []string `json:"implementations,omitempty"`
		Ref             string   `json:"ref,omitempty"`
	}
	if err := json.Unmarshal(createBody.Operations[0].Body, &parentCreate); err != nil {
		t.Fatalf("unmarshal parent create: %v", err)
	}
	if parentCreate.Type != string(domain.TypeEpic) || parentCreate.Priority != "P2" || parentCreate.Ref == "" || !equalStrings(parentCreate.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("parent create = %+v, want epic P2 with generated ref and implementation", parentCreate)
	}
	var childCreate struct {
		Title     string `json:"title"`
		Priority  string `json:"priority"`
		ParentRef string `json:"parent_ref,omitempty"`
	}
	if err := json.Unmarshal(createBody.Operations[1].Body, &childCreate); err != nil {
		t.Fatalf("unmarshal child create: %v", err)
	}
	if childCreate.Title != "Bulk child" || childCreate.ParentRef != parentCreate.Ref || childCreate.Priority != "P3" {
		t.Fatalf("child create = %+v, want parent_ref %q and P3", childCreate, parentCreate.Ref)
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
	var updateParams struct {
		TaskID      string `json:"task_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
	}
	if err := json.Unmarshal(updateBody.Operations[0].Body, &updateParams); err != nil {
		t.Fatalf("unmarshal update operation body: %v", err)
	}
	if updateParams.Title != "Renamed" || updateParams.Description != "Desc" || updateParams.Priority != "P1" {
		t.Fatalf("update params = %+v, want renamed title with preserved description and P1", updateParams)
	}
}

func TestCompileBulkCreateItemRejectsDuplicateRefs(t *testing.T) {
	input := issueBulkCreateInputItem{
		Title:       "Epic",
		Description: "Parent",
		Type:        "epic",
		Priority:    "P2",
		Ref:         "same",
		Children: []issueBulkCreateInputItem{{
			Title:       "Child",
			Description: "Nested",
			Type:        "task",
			Priority:    "P2",
			Ref:         "same",
		}},
	}
	_, err := compileBulkCreateItem(input, "bulk-create item 0", "go-bubbletea", "", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `duplicate ref "same"`) {
		t.Fatalf("compileBulkCreateItem() error = %v, want duplicate ref", err)
	}
}

func TestCompileBulkCreateItemRejectsInvalidParentID(t *testing.T) {
	parentID := "not/an/issue"
	input := issueBulkCreateInputItem{
		Title:       "Child",
		Description: "Bad parent",
		Type:        "task",
		Priority:    "P2",
		ParentID:    &parentID,
	}
	_, err := compileBulkCreateItem(input, "bulk-create item 0", "go-bubbletea", "", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `bulk-create item 0: invalid parent_id "not/an/issue"`) {
		t.Fatalf("compileBulkCreateItem() error = %v, want invalid parent_id", err)
	}
}

func TestCompileBulkCreateItemRejectsEmptyParentID(t *testing.T) {
	parentID := "  "
	input := issueBulkCreateInputItem{
		Title:       "Child",
		Description: "Blank parent",
		Type:        "task",
		Priority:    "P2",
		ParentID:    &parentID,
	}
	_, err := compileBulkCreateItem(input, "bulk-create item 0.children[0]", "go-bubbletea", "parent-ref", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `bulk-create item 0.children[0]: parent_id cannot be empty`) {
		t.Fatalf("compileBulkCreateItem() error = %v, want empty parent_id", err)
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
	if !strings.Contains(output, "issue get [--project <project-id>] [--id <id>] [--json] [--with-notes] [<id>]") {
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
	if !strings.Contains(output, "issue split [--project <project-id>] [--parent <id>]") {
		t.Fatalf("usage missing issue split command: %q", output)
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
	if !strings.Contains(output, "issue close [--project <project-id>] [--id <id>|-i <id>] [--json] [--force-worktree] [<id>]") {
		t.Fatalf("usage missing issue close command: %q", output)
	}
	if strings.Contains(output, "finalize") {
		t.Fatalf("usage should not include removed close alias: %q", output)
	}
	if !strings.Contains(output, "issue delete [--project <project-id>] [--id <id>] [--json] [<id>] --confirm [--cleanup|--stop-session] [--remove-worktree] [--force-worktree]") {
		t.Fatalf("usage missing issue delete command: %q", output)
	}
	if !strings.Contains(output, "issue image add [--project <project-id>] [--issue-id <issue-id>] [--path <file>] [<issue-id> <file>] [--json]") {
		t.Fatalf("usage missing issue image add command: %q", output)
	}
	if !strings.Contains(output, "issue image remove [--project <project-id>] [--issue-id <issue-id>] [--attachment-id <attachment-id>] [<issue-id> <attachment-id>] [--json]") {
		t.Fatalf("usage missing issue image remove command: %q", output)
	}
	if !strings.Contains(output, "issue dep add [--project <project-id>] --issue-id <issue-id> --depends-on-id <depends-on-id> [--type ...] [--force-parent-change] [--json]") {
		t.Fatalf("usage missing issue dep add command: %q", output)
	}
	if !strings.Contains(output, "issue dep remove [--project <project-id>] --issue-id <issue-id> --depends-on-id <depends-on-id> [--type ...] [--confirm] [--confirm-parent-orphan] [--json]") {
		t.Fatalf("usage missing issue dep remove command: %q", output)
	}
	if !strings.Contains(output, "issue dep bulk apply [--project <project-id>] --input <path>") {
		t.Fatalf("usage missing issue dep bulk apply command: %q", output)
	}
	if !strings.Contains(output, "issue bulk-create [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]  Create issues, epics, or nested children trees from JSON") {
		t.Fatalf("usage missing issue bulk-create command: %q", output)
	}
	if !strings.Contains(output, "issue bulk-update [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]") {
		t.Fatalf("usage missing issue bulk-update command: %q", output)
	}
	if !strings.Contains(output, "config set <key> <value> [--project-dir <dir>]") {
		t.Fatalf("usage missing config command: %q", output)
	}
	if !strings.Contains(output, "az config set spec.enabled false") {
		t.Fatalf("usage missing config example: %q", output)
	}
	if !strings.Contains(output, "sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]") {
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
	if !strings.Contains(output, "az issue create \"New task\"") {
		t.Fatalf("usage missing plain issue create example: %q", output)
	}
	if !strings.Contains(output, "assigns implementation metadata, not graph parentage") {
		t.Fatalf("usage missing impl-not-graph example clarification: %q", output)
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
	setPrimeTmuxAvailable(t, true)

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{})
	})

	if !strings.Contains(output, "Azedarach Session Primer") {
		t.Fatalf("prime output missing header: %q", output)
	}
	if !strings.Contains(output, "AZEDARACH_PRIMER_KEY:azedarach-prime-v1") {
		t.Fatalf("prime output missing evidence key: %q", output)
	}
	if strings.Contains(output, "Active issue ID:") {
		t.Fatalf("prime output should not print active issue id when unset: %q", output)
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
	if !strings.Contains(output, "create many issues, epics, or nested `children` trees from JSON when shaping a graph up front") {
		t.Fatalf("prime output missing bulk-create nested tree guidance: %q", output)
	}
	if !strings.Contains(output, "Child issue creation without worker launch:") {
		t.Fatalf("prime output missing child-creation/no-worker section: %q", output)
	}
	if !strings.Contains(output, "Batch planning/catalog fanout: `az issue bulk-create --input issues.json --json` with `parent_id` or nested `children` creates parented child issues without starting sessions or orchestration.") {
		t.Fatalf("prime output missing non-orchestrating batch child creation guidance: %q", output)
	}
	if !strings.Contains(output, "Single tracking-only child: `az issue create \"Child task\" [--json]` auto-parents under `AZEDARACH_ISSUE_ID`; for another parent, create the issue and attach it with `az issue dep add <child-id> <parent-id> --type parent-child`.") {
		t.Fatalf("prime output missing non-orchestrating single child creation guidance: %q", output)
	}
	if !strings.Contains(output, "Worker fanout/session launch: `az issue split --parent <issue-id> \"Child task\"` can create the child and start orchestration/session work; use `az issue split` only when you intentionally want isolated worker fanout.") {
		t.Fatalf("prime output missing split/session launch warning: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate status --root <issue-id> [--since <seq>] [--limit <n>] [--json]`") {
		t.Fatalf("prime output missing orchestrate status command example: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate watch --root <issue-id> --since <seq> [--jsonl]` (start in another pane/session and leave running while workers are active; reserve `--once` for diagnostic single polls only)") {
		t.Fatalf("prime output missing continuous watch command guidance: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate start --root <issue-id> [--limit <n>] [--issue <issue-id> ...] [--json]`") {
		t.Fatalf("prime output missing orchestrate start command example: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate message --root <issue-id> --issue <worker-issue> --body \"...\" [--json]`") {
		t.Fatalf("prime output missing orchestrate message command example: %q", output)
	}
	if !strings.Contains(output, "rejects accidental self-delivery when the target matches `AZEDARACH_ISSUE_ID`") {
		t.Fatalf("prime output missing orchestrate message self-delivery guard: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate integrate --issue <issue-id> [--json]`") {
		t.Fatalf("prime output missing orchestrate integrate command example: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate close-session --issue <issue-id> [--json]`") {
		t.Fatalf("prime output missing orchestrate close-session command example: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate complete-check --root <issue-id> [--json]`") {
		t.Fatalf("prime output missing orchestrate complete-check command example: %q", output)
	}
	if !strings.Contains(output, "`orchestration.via` is `az`: use `az orchestrate` as the worker launch and coordination authority") {
		t.Fatalf("prime output missing az orchestration authority guidance: %q", output)
	}
	if !strings.Contains(output, "Do not use harness-native subagent/delegation tooling for this graph unless the user explicitly overrides this or changes `orchestration.via` to `native`.") {
		t.Fatalf("prime output missing native tooling prohibition in az mode: %q", output)
	}
	if !strings.Contains(output, "Use `az orchestrate` for durable tracked task graphs that need issue dependencies, mailbox events, recoverable sessions, isolated worktrees, integration guidance, and `complete-check`.") {
		t.Fatalf("prime output missing az/native tradeoff guidance: %q", output)
	}
	if !strings.Contains(output, "After every `az orchestrate start`, immediately start `az orchestrate watch --root <issue-id> --since <seq> --jsonl` in another pane/session and keep it running") {
		t.Fatalf("prime output missing post-start continuous watch guidance: %q", output)
	}
	if !strings.Contains(output, "Trust hook-backed `activity=busy|idle` for idleness checks") {
		t.Fatalf("prime output missing bounded tmux observation guidance: %q", output)
	}
	if !strings.Contains(output, "treat `activity=no-agent` as an intentional session-only shell") {
		t.Fatalf("prime output missing no-agent guidance: %q", output)
	}
	if !strings.Contains(output, "when activity is `unknown`, inspect hooks with `az ai status --target=auto` and run `az ai install --target=auto` only when hooks are missing, outdated, or not installed") {
		t.Fatalf("prime output missing hook status/install fallback guidance: %q", output)
	}
	if !strings.Contains(output, "Do not poll tmux panes on a fixed interval") {
		t.Fatalf("prime output missing tmux polling guardrail: %q", output)
	}
	if !strings.Contains(output, "Worker intervention threshold: use status/watch/mailbox output for routine observation, and do not preemptively message, interrupt, or redirect child workers unless there is concrete, high-confidence evidence that the worker is about to violate scope, use the wrong parent/root, take a destructive action, or make a material mistake.") {
		t.Fatalf("prime output missing worker intervention threshold guidance: %q", output)
	}
	if !strings.Contains(output, "`orchestration.via=az` means do not spawn harness-native subagents yourself.") {
		t.Fatalf("prime output missing follow-up native subagent prohibition: %q", output)
	}
	if !strings.Contains(output, "Then run the az orchestration loop: `status` to identify runnable leaves and active worker activity") {
		t.Fatalf("prime output missing az orchestration workflow guidance: %q", output)
	}
	if !strings.Contains(output, "use `az orchestrate message --root <parent> --issue <worker> --body \"...\"` only for evidence-backed worker nudges when the intervention threshold is met") {
		t.Fatalf("prime output missing active nudge loop guidance: %q", output)
	}
	if !strings.Contains(output, "Az orchestration loop:") {
		t.Fatalf("prime output missing az orchestration operating loop: %q", output)
	}
	if !strings.Contains(output, "use `az orchestrate message` only for evidence-backed worker nudges when the intervention threshold is met") {
		t.Fatalf("prime output missing active nudge step guidance: %q", output)
	}
	if !strings.Contains(output, "Small graph: 1-3 leaves") {
		t.Fatalf("prime output missing small graph orchestration guidance: %q", output)
	}
	if !strings.Contains(output, "Medium graph: 4-10 leaves") {
		t.Fatalf("prime output missing medium graph orchestration guidance: %q", output)
	}
	if !strings.Contains(output, "Large graph: 10+ leaves or multiple dependency phases") {
		t.Fatalf("prime output missing large graph orchestration guidance: %q", output)
	}
	if !strings.Contains(output, "Repeat status -> start -> watch until `az orchestrate complete-check --root <issue-id>` passes") {
		t.Fatalf("prime output missing completion loop guidance: %q", output)
	}
	if !strings.Contains(output, "close accepted child issues with `az issue close --id <issue-id>`; that command owns merge, session stop, worktree cleanup, and issue closure") {
		t.Fatalf("prime output missing worker integration guidance: %q", output)
	}
	if !strings.Contains(output, "Status semantics: use `open` for not-yet-active work, `in_progress` while actively working, `in_review` when implementation is complete") {
		t.Fatalf("prime output missing status semantics guidance: %q", output)
	}
	if !strings.Contains(output, "Blocked is not an issue status. Represent blocked work with dependency edges") {
		t.Fatalf("prime output missing blocked-as-graph guidance: %q", output)
	}
	if !strings.Contains(output, "Parent-child edges are hierarchy/board-nesting edges, not readiness controls.") {
		t.Fatalf("prime output missing parent-child hierarchy guidance: %q", output)
	}
	if !strings.Contains(output, "Issue graph/root membership comes only from auto-parenting with `AZEDARACH_ISSUE_ID` or explicit `parent-child` dependency edges.") {
		t.Fatalf("prime output missing graph membership source guidance: %q", output)
	}
	if !strings.Contains(output, "`--impl` is implementation metadata only; it never attaches an issue to a graph/root.") {
		t.Fatalf("prime output missing impl-not-graph guidance: %q", output)
	}
	if !strings.Contains(output, "If a child issue was requested and `az issue create --json` returns `parent_id: \"\"`, stop before launching work") {
		t.Fatalf("prime output missing missing-parent stop guidance: %q", output)
	}
	if !strings.Contains(output, "To pause or supersede child work inside an orchestration graph, keep the parent-child edge") {
		t.Fatalf("prime output missing safe pause/supersede guidance: %q", output)
	}
	if !strings.Contains(output, "When the user names a graph/root, verify it before starting workers: run `az issue get <intended-root>` and `az orchestrate status --root <intended-root> --json`") {
		t.Fatalf("prime output missing pre-start root verification guidance: %q", output)
	}
	if !strings.Contains(output, "If work was launched under the wrong parent, do not treat it as a simple move") {
		t.Fatalf("prime output missing wrong-parent correction guidance: %q", output)
	}
	if !strings.Contains(output, "Worker completion flow: workers should leave their issue `in_review`") {
		t.Fatalf("prime output missing in-review worker completion guidance: %q", output)
	}
	if !strings.Contains(output, "`az mail send --parent <parent-issue> --type dependency-ready --body \"...\"`") {
		t.Fatalf("prime output missing mail send command example: %q", output)
	}
	if !strings.Contains(output, "Use `az orchestrate message --root <parent-issue> --issue <worker-issue> --body \"...\"` only for evidence-backed orchestrator-to-running-worker nudges when the intervention threshold is met") {
		t.Fatalf("prime output missing active worker nudge guidance: %q", output)
	}
	if !strings.Contains(output, "workers reporting their own progress/status must use `az mail send --parent <parent-issue> --issue <worker-issue> --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`") {
		t.Fatalf("prime output missing worker safe reporting guidance: %q", output)
	}
	if !strings.Contains(output, "bare `az mail send` is durable mailbox-only and may not be seen until the worker next checks mail") {
		t.Fatalf("prime output missing passive mailbox warning: %q", output)
	}
	if !strings.Contains(output, "Decision records:") {
		t.Fatalf("prime output missing decision command map section: %q", output)
	}
	if !strings.Contains(output, "Use `az decision` to capture durable architecture/product choices and link them to issues, requirements, or prior decisions.") {
		t.Fatalf("prime output missing decision use guidance: %q", output)
	}
	if !strings.Contains(output, "`az decision list [--json] [--issue <id>] [--req <id>] [--id <id> ...] [--query <text>]`") {
		t.Fatalf("prime output missing decision list options: %q", output)
	}
	if !strings.Contains(output, "`az decision record --title <text> --rationale <text> [--context <text>] [--consequences <text>] [--issue <id> ...] [--req <id> ...] [--json]`") {
		t.Fatalf("prime output missing decision record options: %q", output)
	}
	if !strings.Contains(output, "`az decision link add --id <decision-id> (--issue <id> | --req <id> | --decision <id>) [--relation <applies-to|revises|informs>] [--note <text>] [--json]`") {
		t.Fatalf("prime output missing decision link options: %q", output)
	}
	if !strings.Contains(output, "`az decision sync [--check] [--json]` writes `docs/decisions/*.md` from the store; `az decision import [--check] [--force] [--json]` reads markdown back into the store.") {
		t.Fatalf("prime output missing decision sync/import guidance: %q", output)
	}
	if !strings.Contains(output, "`az session status [issue-id]`, `az worktree create <issue-id>`") {
		t.Fatalf("prime output missing session/runtime command examples: %q", output)
	}
	if !strings.Contains(output, "`az session start <issue-id>` is for explicit/manual orchestration; agents should not run it unless the user asks.") {
		t.Fatalf("prime output missing session start guardrail: %q", output)
	}
	if !strings.Contains(output, "Optional child-work setup: `az issue split \"Child task\"` launches isolated worker fanout; use `az issue create \"Child task\"` or `az issue bulk-create --input issues.json` for tracking-only child issues") {
		t.Fatalf("prime output missing optional child-task split guidance: %q", output)
	}
	if strings.Contains(output, "`az spec` (inspect linked requirements before behavior changes)") {
		t.Fatalf("prime output should not instruct agents to run bare az spec: %q", output)
	}
	if strings.Contains(output, "`az issue create \"Title\"` (auto-parents under `AZEDARACH_ISSUE_ID`; use `--deferred` for non-immediate follow-ups)") {
		t.Fatalf("prime output should not require unconditional child issue creation: %q", output)
	}
	if !strings.Contains(output, "immediate child work; auto-parents under `AZEDARACH_ISSUE_ID`") {
		t.Fatalf("prime output missing auto-parent semantics: %q", output)
	}
	if !strings.Contains(output, "standalone backlog only; skips auto-parenting and is not a worktree/session control") {
		t.Fatalf("prime output missing deferred standalone semantics: %q", output)
	}
	if !strings.Contains(output, "Do not use `--deferred` for child tasks, blockers, or work required before closing the active issue") {
		t.Fatalf("prime output missing deferred blocker guardrail: %q", output)
	}
	if !strings.Contains(output, "Parent vs child issue scope:") {
		t.Fatalf("prime output missing parent-vs-child scope section: %q", output)
	}
	if !strings.Contains(output, "Parent/epic issues describe the overarching goal, scope, and success criteria; keep their description high-level and stable.") {
		t.Fatalf("prime output missing parent overarching-goal guidance: %q", output)
	}
	if !strings.Contains(output, "Track subtask goals, implementation steps, and nitty-gritty decisions in child issues created with `az issue create \"Child task\"` for tracking-only child work or `az issue split \"Child task\"` only when the child should launch immediately") {
		t.Fatalf("prime output missing subtask-detail-into-child-issue guidance: %q", output)
	}
	if !strings.Contains(output, "When a new subtask surfaces mid-work, create a child issue immediately rather than expanding the parent's description") {
		t.Fatalf("prime output missing mid-work child-issue creation guidance: %q", output)
	}
	if !strings.Contains(output, "capture subtask-level detail on the relevant child issue instead of bloating a parent/epic description") {
		t.Fatalf("prime output missing keep-current parent/child notes guidance: %q", output)
	}
	if !strings.Contains(output, "Keep notes terse and evidence-oriented: final commands run, key outputs/assertions, files changed, AC pass/fail, blockers, and remaining scope only.") {
		t.Fatalf("prime output missing terse notes guidance: %q", output)
	}
	if !strings.Contains(output, "Do not append raw logs, exploratory transcripts, routine progress narration, duplicate primer context, or speculative scratch work to notes.") {
		t.Fatalf("prime output missing notes anti-bloat guidance: %q", output)
	}
	if !strings.Contains(output, "`az issue get` hides historical notes in text output by default; use `az issue get <issue-id> --with-notes` only when full note history is needed.") {
		t.Fatalf("prime output missing tiered notes retrieval guidance: %q", output)
	}
	if !strings.Contains(output, "Atomic-merge test before using `--deferred`") {
		t.Fatalf("prime output missing atomic-merge test heading in parent/child scope section: %q", output)
	}
	if !strings.Contains(output, "would the parent be correct and complete if it lands in the base branch without this new piece?") {
		t.Fatalf("prime output missing atomic-merge test framing: %q", output)
	}
	if !strings.Contains(output, "child issues are part of the parent's merge unit and land in the same base-branch commit") {
		t.Fatalf("prime output missing child-merge-unit clarification: %q", output)
	}
	if !strings.Contains(output, "Atomic-merge test before reaching for `--deferred`") {
		t.Fatalf("prime output missing pre-deferred atomic-merge guardrail in follow-up rules: %q", output)
	}
	if !strings.Contains(output, "child issues land in the same base-branch commit as the parent") {
		t.Fatalf("prime output missing child-lands-with-parent clarification in follow-up rules: %q", output)
	}
	if !strings.Contains(output, "Reserve `--deferred` for work that can land separately later.") {
		t.Fatalf("prime output missing deferred-purpose clarification: %q", output)
	}
	if !strings.Contains(output, "`--deferred` only changes issue bookkeeping: it skips active-issue auto-parenting and defaults priority lower; do not use it to avoid, force, or reason about worktree/session creation.") {
		t.Fatalf("prime output missing deferred worktree/session clarification: %q", output)
	}
	if !strings.Contains(output, "It is not a child-task shortcut and not a way to control worktree/session creation.") {
		t.Fatalf("prime output missing follow-up deferred worktree/session clarification: %q", output)
	}
	if !strings.Contains(output, "Close the issue only when the issue scope and acceptance criteria are fully complete") {
		t.Fatalf("prime output missing close-only-when-fully-complete guardrail: %q", output)
	}
	if !strings.Contains(output, "If work is partial, keep status `in_progress`, append notes with remaining scope, and create child issues for unfinished required work.") {
		t.Fatalf("prime output missing partial-work guardrail: %q", output)
	}
	if !strings.Contains(output, "Child work should target the closest ancestor with an active worktree branch.") {
		t.Fatalf("prime output missing child ancestor target guidance: %q", output)
	}
	if !strings.Contains(output, "run `az worktree create <ancestor-issue>` to materialize the parent/ancestor integration target before closing the child") {
		t.Fatalf("prime output missing parent/ancestor restore guidance: %q", output)
	}
	if !strings.Contains(output, "az worktree create <issue-id>") {
		t.Fatalf("prime output missing worktree create recovery command: %q", output)
	}
	if strings.Contains(output, "--allow-base-for-child") || strings.Contains(output, "child-to-base override") {
		t.Fatalf("prime output should not mention child-to-base override guidance: %q", output)
	}
	if strings.Contains(output, "base-target merge is allowed") {
		t.Fatalf("prime output should not suggest base-target child merge completion: %q", output)
	}
	if strings.Contains(output, "same PR") || strings.Contains(output, "one PR") {
		t.Fatalf("prime output should frame the heuristic around base-branch atomic merges, not PRs: %q", output)
	}
}

func TestPrimeCommandWithActiveIssueContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")

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
					ParentID:        &parentID,
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
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Active issue ID: `az-1`") {
		t.Fatalf("prime output missing explicit active issue id: %q", output)
	}
	if !strings.Contains(output, "`az spec read --issue az-1` (inspect linked requirements before behavior changes)") {
		t.Fatalf("prime output missing active-issue spec read command: %q", output)
	}
	if !strings.Contains(output, "Active issue context (AZEDARACH_ISSUE_ID=az-1)") {
		t.Fatalf("prime output missing active issue section: %q", output)
	}
	if !strings.Contains(output, "az-1: Prime issue [status=open priority=P2 type=task impl=go-bubbletea]") {
		t.Fatalf("prime output missing issue summary: %q", output)
	}
	if !strings.Contains(output, "Dependencies:\n- blocks: az-2") {
		t.Fatalf("prime output missing dependency summary: %q", output)
	}
	if !strings.Contains(output, "Parent: az-parent") {
		t.Fatalf("prime output missing active issue parent: %q", output)
	}
	if !strings.Contains(output, "Worker mailbox: receive orchestrator messages with `az mail list --parent az-parent --since 0 --json`") {
		t.Fatalf("prime output missing worker mailbox receive guidance: %q", output)
	}
	if !strings.Contains(output, "`az mail watch --parent az-parent --since <seq> --jsonl` only when explicitly asked") {
		t.Fatalf("prime output missing bounded mailbox watch guidance: %q", output)
	}
	if strings.Contains(output, "Parent-context recommendation:") {
		t.Fatalf("prime output should not show parent-context recommendation for task without children: %q", output)
	}
}

func TestPrimeCommandRecommendsChildIssuesForEpicContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	setPrimeTmuxAvailable(t, true)
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
					ID:        "az-1",
					Title:     "Parent epic",
					Status:    domain.StatusInProgress,
					Priority:  domain.P2,
					Type:      domain.TypeEpic,
					CreatedAt: now,
					UpdatedAt: now,
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
		Config: &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Parent-context recommendation: `az-1` is an epic or already has child issues") {
		t.Fatalf("prime output missing epic child-work recommendation: %q", output)
	}
	if !strings.Contains(output, "keep implementation/subtask work in child issues with `az issue create \"Child task\"` for tracking-only child work; use `az issue split \"Child task\"` only when that child should launch immediately in its own session") {
		t.Fatalf("prime output missing child issue commands for epic context: %q", output)
	}
	if !strings.Contains(output, "Do the child implementation from the child issue execution context: preferably a child session (`az session start <child-issue>` or `az issue split \"Child task\"`) and at minimum the child worktree (`az worktree create <child-issue>`).") {
		t.Fatalf("prime output missing child execution context guidance for epic context: %q", output)
	}
}

func TestPrimeCommandRecommendsChildIssuesWhenActiveIssueHasChildren(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	setPrimeTmuxAvailable(t, false)
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	parentID := naming.IssueID("az-1")

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
				body, err := marshalTaskListBody([]domain.Task{
					{
						ID:        "az-1",
						Title:     "Parent task",
						Status:    domain.StatusInProgress,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						CreatedAt: now,
						UpdatedAt: now,
					},
					{
						ID:        "az-2",
						Title:     "Child task",
						Status:    domain.StatusOpen,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						ParentID:  &parentID,
						CreatedAt: now,
						UpdatedAt: now,
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
					Body:            body,
				}, nil
			},
		}),
		Config: &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Parent-context recommendation: `az-1` is an epic or already has child issues") {
		t.Fatalf("prime output missing child-work recommendation for parent task: %q", output)
	}
	if !strings.Contains(output, "with `az issue create \"Child task\"` for tracking-only child work instead of accumulating detailed work on the parent") {
		t.Fatalf("prime output missing non-tmux child issue command: %q", output)
	}
	if !strings.Contains(output, "Do the child implementation from the child issue execution context: preferably a child session (`az session start <child-issue>`) and at minimum the child worktree (`az worktree create <child-issue>`).") {
		t.Fatalf("prime output missing child execution context guidance for parent task: %q", output)
	}
	if strings.Contains(output, "Parent-context recommendation: `az-1` is an epic or already has child issues; keep implementation/subtask work in child issues with `az issue create \"Child task\"` or `az issue split \"Child task\"`") {
		t.Fatalf("prime output should not mention split command when tmux is unavailable: %q", output)
	}
}

func TestPrimeCommandUsesTmuxSessionContextWhenEnvMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	previousTmuxPaneSessionName := tmuxPaneSessionName
	tmuxPaneSessionName = func(context.Context) (string, error) {
		return "pr-az-1", nil
	}
	t.Cleanup(func() {
		tmuxPaneSessionName = previousTmuxPaneSessionName
	})
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
					Implementations: []string{"default"},
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
		RepoDir:   "/tmp/proj",
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Active issue ID: `az-1`") {
		t.Fatalf("prime output missing tmux-derived active issue id: %q", output)
	}
	if !strings.Contains(output, "`AZEDARACH_ISSUE_ID` is absent, but the current tmux session resolves to issue `az-1`") {
		t.Fatalf("prime output missing missing-env tmux warning: %q", output)
	}
	if !strings.Contains(output, "Active issue context (AZEDARACH_ISSUE_ID=az-1)") {
		t.Fatalf("prime output missing tmux-derived active issue section: %q", output)
	}
}

func TestPrimeCommandShowsImplementationOptionsWhenMultipleConfigured(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
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
				body, err := marshalTaskListBody([]domain.Task{
					{
						ID:              "az-1",
						Title:           "Default work",
						Status:          domain.StatusOpen,
						Priority:        domain.P2,
						Type:            domain.TypeTask,
						Implementations: []string{"default"},
						CreatedAt:       now,
						UpdatedAt:       now,
					},
					{
						ID:              "az-2",
						Title:           "Marketing work",
						Status:          domain.StatusOpen,
						Priority:        domain.P2,
						Type:            domain.TypeTask,
						Implementations: []string{"marketing"},
						CreatedAt:       now,
						UpdatedAt:       now,
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
					Body:            body,
				}, nil
			},
		}),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Implementation selection (multi-implementation project):") {
		t.Fatalf("prime output missing implementation selection block: %q", output)
	}
	if !strings.Contains(output, "Available implementations: `default`, `marketing`") {
		t.Fatalf("prime output missing available implementation options: %q", output)
	}
	if !strings.Contains(output, "Use `az impl list` to refresh the available options.") {
		t.Fatalf("prime output missing impl list guidance: %q", output)
	}
	if !strings.Contains(output, "`--impl` selects implementation/spec variant assignment only; it does not attach an issue to a parent/root graph.") {
		t.Fatalf("prime output missing multi-impl graph distinction: %q", output)
	}
	if !strings.Contains(output, "`az issue create --impl default \"Implementation-specific task\"`") {
		t.Fatalf("prime output missing create-with-impl example: %q", output)
	}
	if !strings.Contains(output, "Existing issue updates do not use `--impl`; use `--update-impl` only when changing assignments.") {
		t.Fatalf("prime output missing update impl distinction: %q", output)
	}
}

func TestPrimeCommandTruncatesLargeIssueDescription(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	longDescription := strings.Repeat("line content for noisy transcript output\n", 40)

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
					Description:     longDescription,
					Status:          domain.StatusOpen,
					Priority:        domain.P2,
					Type:            domain.TypeTask,
					Implementations: []string{"default"},
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

	if !strings.Contains(output, "… (truncated; run `az issue get az-1` for full context)") {
		t.Fatalf("prime output should include truncated description sentinel: %q", output)
	}
	if strings.Count(output, "line content for noisy transcript output") >= 12 {
		t.Fatalf("prime output should not include full long description: %q", output)
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
	if !strings.Contains(output, "`az issue list --limit 20` or `az issue create \"Next task\"") {
		t.Fatalf("prime output missing closed-issue next-step guidance: %q", output)
	}
	if !strings.Contains(output, "Use `--deferred` only for standalone backlog work.") {
		t.Fatalf("prime output missing closed-issue deferred caveat: %q", output)
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
	if !strings.Contains(output, "`az spec read --issue <issue-id>` (inspect linked requirements before behavior changes)") {
		t.Fatalf("prime output missing concrete spec read command: %q", output)
	}
	if !strings.Contains(output, "ALWAYS run `az spec read --issue <issue-id>` before starting behavior work; use `az spec link list --issue <issue-id>` when you need link-only detail.") {
		t.Fatalf("prime output missing mandatory pre-work spec check guardrail: %q", output)
	}
	if !strings.Contains(output, "To choose spec traceability, first inspect linked requirements, then use `az spec req list` and `az spec read --req <req-id>` to find nearby requirements by feature area or acceptance intent.") {
		t.Fatalf("prime output missing spec discovery guardrail: %q", output)
	}
	if !strings.Contains(output, "Link an existing requirement when it already defines the intended behavior; create or update a requirement before implementation when work adds behavior, changes user-visible behavior, changes a CLI/API/TUI contract, alters persistence/daemon semantics, or reveals an underspecified contract.") {
		t.Fatalf("prime output missing spec link/create decision guardrail: %q", output)
	}
	if !strings.Contains(output, "Contract-preserving work usually does not need a new requirement: refactors, tests, formatting, tooling, observability, dependency/internal cleanup, docs/process-only edits, or fixes that restore already-specified behavior.") {
		t.Fatalf("prime output missing contract-preserving spec exception guardrail: %q", output)
	}
	if !strings.Contains(output, "For contract-preserving work, record explicit issue-note evidence such as `Spec impact: none (contract-preserving refactor)`, `Spec impact: none (tests/tooling only)`, or `Spec impact: none (fix restores existing behavior)`.") {
		t.Fatalf("prime output missing spec-impact note examples: %q", output)
	}
	if !strings.Contains(output, "If behavior work has no linked requirements after that check, do not treat missing links as permission to skip spec alignment.") {
		t.Fatalf("prime output missing no-linked-requirements remediation guardrail: %q", output)
	}
	if !strings.Contains(output, "If implementation is not aligned with spec, update spec first, then implement.") {
		t.Fatalf("prime output missing spec-first update guardrail: %q", output)
	}
	if !strings.Contains(output, "Ensure implementation issue(s) are linked to relevant spec requirement(s) before execution.") {
		t.Fatalf("prime output missing issue/spec linking guardrail: %q", output)
	}
	if strings.Contains(output, "ALWAYS check `az spec` requirements/links") {
		t.Fatalf("prime output should not include bare az spec guardrail: %q", output)
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

func TestPrimeCommandNativeFanoutGuidance(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	setPrimeTmuxAvailable(t, true)

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{
			Config: &config.Config{
				Spec:          config.SpecConfig{Enabled: true},
				Orchestration: config.OrchestrationConfig{Via: "native"},
			},
		})
	})

	if !strings.Contains(output, "`orchestration.via` is `native`: use your harness-native subagent delegation commands/keywords") {
		t.Fatalf("prime output missing native fanout guidance: %q", output)
	}
	if !strings.Contains(output, "Shorthand: `single-window fanout (native)`") {
		t.Fatalf("prime output missing native single-window shorthand: %q", output)
	}
	if strings.Contains(output, "`az orchestrate status --root <issue-id> [--since <seq>] [--limit <n>] [--json]`") {
		t.Fatalf("prime output should avoid az orchestrate command map in native mode: %q", output)
	}
	if strings.Contains(output, "Shorthand: `single-window fanout` means split until each child is ready for one subagent, then fan out one subagent per child.") {
		t.Fatalf("prime output should not include az fanout shorthand in native mode: %q", output)
	}
	if !strings.Contains(output, "Use native delegation for short-lived ad hoc exploration or review where the result can be summarized back into the current session.") {
		t.Fatalf("prime output missing native/az tradeoff guidance: %q", output)
	}
}

func TestPrimeCommandAzOrchestrationGuidanceRequiresAzVia(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	setPrimeTmuxAvailable(t, true)

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{
			Config: &config.Config{
				Spec:          config.SpecConfig{Enabled: true},
				Orchestration: config.OrchestrationConfig{Via: "az"},
			},
		})
	})

	if !strings.Contains(output, "`orchestration.via` is `az`: use `az orchestrate` as the worker launch and coordination authority") {
		t.Fatalf("prime output missing explicit az orchestration guidance: %q", output)
	}
	if strings.Contains(output, "Unsupported `orchestration.via` value") {
		t.Fatalf("prime output should not report unsupported orchestration mode for az: %q", output)
	}
}

func TestPrimeCommandAzOrchestrationGuidanceRequiresTmux(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	setPrimeTmuxAvailable(t, false)

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{
			Config: &config.Config{
				Spec:          config.SpecConfig{Enabled: true},
				Orchestration: config.OrchestrationConfig{Via: "az"},
			},
		})
	})

	if !strings.Contains(output, "`orchestration.via` is `az`, but CLI-managed worker fanout requires `tmux` on `PATH`; `tmux` is not available.") {
		t.Fatalf("prime output missing tmux unavailable guidance: %q", output)
	}
	if strings.Contains(output, "`az orchestrate ") {
		t.Fatalf("prime output should not print az orchestrate commands when tmux is unavailable: %q", output)
	}
	if strings.Contains(output, "az orchestration loop") {
		t.Fatalf("prime output should not print az orchestration loop when tmux is unavailable: %q", output)
	}
}

func TestPrimeCommandNativeGuidanceAvoidsAzOrchestrateWhenTmuxMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	setPrimeTmuxAvailable(t, false)

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{
			Config: &config.Config{
				Spec:          config.SpecConfig{Enabled: true},
				Orchestration: config.OrchestrationConfig{Via: "native"},
			},
		})
	})

	if !strings.Contains(output, "CLI-managed worker fanout requires `tmux` on `PATH`; `tmux` is not available") {
		t.Fatalf("prime output missing native-mode tmux unavailable guidance: %q", output)
	}
	if !strings.Contains(output, "`az orchestrate prompt --issue <issue-id> [--root <issue-id>]`") {
		t.Fatalf("prime output missing native prompt guidance when tmux is unavailable: %q", output)
	}
	for _, disallowed := range []string{"`az orchestrate start ", "`az orchestrate status ", "`az orchestrate watch ", "az orchestration loop"} {
		if strings.Contains(output, disallowed) {
			t.Fatalf("prime output should not print CLI-managed orchestrate flow %q in native mode when tmux is unavailable: %q", disallowed, output)
		}
	}
}

func TestPrimeCommandUnsupportedOrchestrationViaDoesNotPrintAzWorkflow(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{
			Config: &config.Config{
				Spec:          config.SpecConfig{Enabled: true},
				Orchestration: config.OrchestrationConfig{Via: "banana"},
			},
		})
	})

	if !strings.Contains(output, "Unsupported `orchestration.via` value: `banana`") {
		t.Fatalf("prime output missing unsupported orchestration warning: %q", output)
	}
	if strings.Contains(output, "`orchestration.via` is `az`: use `az orchestrate` as the worker launch and coordination authority") {
		t.Fatalf("prime output should not print az guidance for unsupported orchestration mode: %q", output)
	}
	if strings.Contains(output, "`orchestration.via` is `native`: use your harness-native subagent") {
		t.Fatalf("prime output should not print native guidance for unsupported orchestration mode: %q", output)
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
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				if !started {
					return protocol.HelloAck{}, errors.New("daemon socket unavailable")
				}
				return protocol.HelloAck{Accepted: true}, nil
			},
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

func commandNames(requests []protocol.RequestEnvelope) []string {
	out := make([]string, 0, len(requests))
	for _, req := range requests {
		out = append(out, req.Command)
	}
	return out
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

func setPrimeTmuxAvailable(t *testing.T, available bool) {
	t.Helper()
	previous := primeLookPath
	primeLookPath = func(file string) (string, error) {
		if file != "tmux" {
			return "", errors.New("unexpected lookup: " + file)
		}
		if available {
			return "/usr/bin/tmux", nil
		}
		return "", errors.New("tmux not found")
	}
	t.Cleanup(func() {
		primeLookPath = previous
	})
}

func captureStderr(t *testing.T, fn func() error) string {
	t.Helper()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- fn()
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)

	os.Stderr = oldStderr
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stderr: %v", copyErr)
	}
	if runErr != nil {
		t.Fatalf("command error: %v", runErr)
	}

	return buf.String()
}
