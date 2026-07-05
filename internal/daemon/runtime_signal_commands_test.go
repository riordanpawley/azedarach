package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

func TestRuntimeSignalIngestGitHookPersistsFastProjectionAndQueuesEnrichment(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "runtime-signal-git",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandRuntimeSignalIngest,
		Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
			Source:   protocol.RuntimeSignalSourceGitHook,
			Kind:     protocol.RuntimeSignalKindGitWorktreeChanged,
			Worktree: repoDir,
			Hook:     "post-commit",
			Event:    "post-commit",
			Log:      true,
		}),
	})
	if err != nil {
		t.Fatalf("runtime.signal.ingest command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
	}
	var body protocol.RuntimeSignalIngestResponseBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal runtime signal response: %v", err)
	}
	if !body.Accepted || !body.EnrichmentQueued || len(body.ProjectionRevisions) == 0 {
		t.Fatalf("runtime signal response = %+v", body)
	}
	if !runtimeSignalStageOK(body.Stages, "git_status_fast") {
		t.Fatalf("runtime signal stages = %+v, want git_status_fast ok", body.Stages)
	}

	projection, found, err := d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByPath(context.Background(), projectID, repoDir)
	if err != nil {
		t.Fatalf("GetWorktreeStateByPath: %v", err)
	}
	if !found {
		t.Fatalf("expected worktree projection for %s", repoDir)
	}
	var status git.GitStatus
	if err := json.Unmarshal(projection.GitStatusRaw, &status); err != nil {
		t.Fatalf("unmarshal projected git status: %v", err)
	}
	if !status.HasChanges {
		t.Fatalf("projected git status = %+v, want dirty status", status)
	}

	events := d.listHookLogEvents(projectID, 10)
	if len(events) != 1 || events[0].Source != "githooks.hook" || events[0].Hook != "post-commit" {
		t.Fatalf("hook log events = %+v", events)
	}
}

func TestRuntimeSignalIngestAgentHookPersistsActivityAndHookLog(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"
	sessionID := "pr-az-42.pane-12"

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "runtime-signal-agent",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandRuntimeSignalIngest,
		Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: sessionID,
			Worktree:  repoDir,
			Agent:     "codex",
			Hook:      "permission_request",
			Event:     "permission_request",
			Log:       true,
		}),
	})
	if err != nil {
		t.Fatalf("runtime.signal.ingest command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
	}
	var body protocol.RuntimeSignalIngestResponseBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal runtime signal response: %v", err)
	}
	if !runtimeSignalStageOK(body.Stages, "agent_activity") || !runtimeSignalStageOK(body.Stages, "hook_log") {
		t.Fatalf("runtime signal stages = %+v", body.Stages)
	}

	session, found, err := d.sessionRuntimeStateStore(projectID).GetSessionState(context.Background(), projectID, sessionID)
	if err != nil {
		t.Fatalf("GetSessionState: %v", err)
	}
	if !found {
		t.Fatalf("expected session projection for %s", sessionID)
	}
	if session.IssueID != issueID ||
		session.State != daemonstate.SessionStatePaused ||
		session.Activity != "idle" ||
		session.ActivitySource != "hooks" {
		t.Fatalf("session projection = %+v", session)
	}

	events := d.listHookLogEvents(projectID, 10)
	if len(events) != 1 || events[0].Source != "codex.hook" || events[0].IssueID.String() != issueID {
		t.Fatalf("hook log events = %+v", events)
	}
}

func TestRuntimeSignalIngestRejectsUnsupportedKindWithoutHookLogSideEffect(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "runtime-signal-invalid",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandRuntimeSignalIngest,
		Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
			Source:   protocol.RuntimeSignalSourceGitHook,
			Kind:     "made_up",
			Worktree: repoDir,
			Hook:     "post-commit",
			Log:      true,
		}),
	})
	if err != nil {
		t.Fatalf("runtime.signal.ingest command error: %v", err)
	}
	if resp.OK {
		t.Fatalf("runtime.signal.ingest response OK for unsupported kind")
	}
	if events := d.listHookLogEvents(projectID, 10); len(events) != 0 {
		t.Fatalf("hook log events = %+v, want none for invalid signal", events)
	}
}

func runtimeSignalStageOK(stages []protocol.RuntimeSignalStageOutcome, name string) bool {
	for _, stage := range stages {
		if stage.Name == name {
			return stage.OK
		}
	}
	return false
}

func initRuntimeSignalGitRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	runRuntimeSignalGitCommand(t, repoDir, "init")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runRuntimeSignalGitCommand(t, repoDir, "add", "README.md")
	runRuntimeSignalGitCommand(t, repoDir, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "seed")
	runRuntimeSignalGitCommand(t, repoDir, "checkout", "-b", "riordan/az-42/runtime-signal")
	return repoDir
}

func runRuntimeSignalGitCommand(t *testing.T, repoDir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}
