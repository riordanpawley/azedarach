package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
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
	startedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	if err := d.sessionRuntimeStateStore(projectID).UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:             "pr-az-42",
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		StartedAt:      &startedAt,
		UpdatedAt:      startedAt,
	}); err != nil {
		t.Fatalf("seed canonical session projection: %v", err)
	}

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
	if !runtimeSignalStageOK(body.Stages, "agent_activity") ||
		!runtimeSignalStageOK(body.Stages, "agent_activity_evidence") ||
		!runtimeSignalStageOK(body.Stages, "agent_activity_canonical") ||
		!runtimeSignalStageOK(body.Stages, "hook_log") {
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
		session.Activity != "waiting" ||
		session.ActivitySource != "hooks" {
		t.Fatalf("session projection = %+v", session)
	}
	canonical, found, err := d.sessionRuntimeStateStore(projectID).GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.IssueID != issueID ||
		canonical.State != daemonstate.SessionStateRunning ||
		canonical.Activity != "waiting" ||
		canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v", canonical)
	}
	evidence, found, err := d.sessionRuntimeStateStore(projectID).GetSessionActivityEvidence(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionActivityEvidence: %v", err)
	}
	if !found {
		t.Fatal("expected session activity evidence")
	}
	if evidence.Activity != "waiting" ||
		evidence.ActivitySource != "hooks" ||
		evidence.SourceSessionID != sessionID ||
		evidence.Agent != "codex" ||
		evidence.Hook != "permission_request" {
		t.Fatalf("session activity evidence = %+v", evidence)
	}

	events := d.listHookLogEvents(projectID, 10)
	if len(events) != 1 || events[0].Source != "codex.hook" || events[0].IssueID.String() != issueID {
		t.Fatalf("hook log events = %+v", events)
	}
}

func TestRuntimeSignalAgentLifecycleClassifiesProcessExit(t *testing.T) {
	zero := 0
	nonzero := 137
	tests := []struct {
		name       string
		exitStatus *int
		want       string
	}{
		{name: "clean exit", exitStatus: &zero, want: "idle"},
		{name: "process died", exitStatus: &nonzero, want: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, ok := runtimeSignalAgentLifecycle(protocol.RuntimeSignalIngestCommandBody{
				Event:      "session_end",
				ExitStatus: tt.exitStatus,
			})
			if !ok || got != tt.want {
				t.Fatalf("runtimeSignalAgentLifecycle() = (%q, %v), want (%q, true)", got, ok, tt.want)
			}
		})
	}
}

func TestRuntimeSignalIngestKeepsCanonicalBusyWhenAnotherPaneWaits(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"
	parentSessionID := "pr-az-42"
	startedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	if err := d.sessionRuntimeStateStore(projectID).UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:             parentSessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		StartedAt:      &startedAt,
		UpdatedAt:      startedAt,
	}); err != nil {
		t.Fatalf("seed canonical session projection: %v", err)
	}

	for _, signal := range []protocol.RuntimeSignalIngestCommandBody{
		{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: parentSessionID + ".pane-34",
			Worktree:  repoDir,
			Agent:     "codex",
			Hook:      "pre_tool_use",
			Event:     "pre_tool_use",
		},
		{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: parentSessionID + ".pane-12",
			Worktree:  repoDir,
			Agent:     "codex",
			Hook:      "permission_request",
			Event:     "permission_request",
		},
	} {
		resp, err := d.command(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID("runtime-signal-agent-" + signal.Hook),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         protocol.CommandRuntimeSignalIngest,
			Body:            mustMarshal(t, signal),
		})
		if err != nil {
			t.Fatalf("runtime.signal.ingest command error: %v", err)
		}
		if !resp.OK {
			t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
		}
	}

	canonical, found, err := d.sessionRuntimeStateStore(projectID).GetSessionState(context.Background(), projectID, parentSessionID)
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.Activity != "busy" || canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v, want busy/hooks", canonical)
	}
}

func TestRuntimeSignalIngestCanonicalStopClearsStalePaneBusyForDisplay(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"
	parentSessionID := "pr-az-42"

	signals := []protocol.RuntimeSignalIngestCommandBody{
		{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: parentSessionID + ".pane-500",
			Worktree:  repoDir,
			Agent:     "codex",
			Hook:      "pre_tool_use",
			Event:     "pre_tool_use",
		},
		{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: parentSessionID,
			Worktree:  repoDir,
			Agent:     "codex",
			Hook:      "stop",
			Event:     "stop",
		},
	}
	for i, signal := range signals {
		if i > 0 {
			time.Sleep(time.Millisecond)
		}
		resp, err := d.command(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID("runtime-signal-agent-" + signal.Hook),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         protocol.CommandRuntimeSignalIngest,
			Body:            mustMarshal(t, signal),
		})
		if err != nil {
			t.Fatalf("runtime.signal.ingest command error: %v", err)
		}
		if !resp.OK {
			t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
		}
	}

	tasks := d.enrichTasksWithSessionState(context.Background(), projectID, []domain.Task{{
		ID:    naming.IssueID(issueID),
		Title: "Codex worker",
		Type:  domain.TypeTask,
	}})
	if len(tasks) != 1 || tasks[0].Session == nil {
		t.Fatalf("missing session projection: %+v", tasks)
	}
	if tasks[0].Session.Activity != "idle" || tasks[0].Session.ActivitySource != "hooks" {
		t.Fatalf("display activity = %s/%s, want idle/hooks", tasks[0].Session.Activity, tasks[0].Session.ActivitySource)
	}
}

func TestRuntimeSignalIngestAgentHookCreatesRunningCanonicalProjection(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "runtime-signal-agent-new-canonical",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandRuntimeSignalIngest,
		Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: "pr-az-42.pane-12",
			Worktree:  repoDir,
			Agent:     "codex",
			Hook:      "permission_request",
			Event:     "permission_request",
		}),
	})
	if err != nil {
		t.Fatalf("runtime.signal.ingest command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
	}

	canonical, found, err := d.sessionRuntimeStateStore(projectID).GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.IssueID != issueID ||
		canonical.State != daemonstate.SessionStateRunning ||
		canonical.ObservedState != daemonstate.SessionStateRunning ||
		canonical.Activity != "waiting" ||
		canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v", canonical)
	}
}

func TestRuntimeSignalIngestAgentIdlePromptPersistsWaitingActivity(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "runtime-signal-agent-idle-prompt",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandRuntimeSignalIngest,
		Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: "pr-az-42.pane-12",
			Worktree:  repoDir,
			Agent:     "codex",
			Hook:      "idle_prompt",
			Event:     "idle_prompt",
			Log:       true,
		}),
	})
	if err != nil {
		t.Fatalf("runtime.signal.ingest command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
	}

	canonical, found, err := d.sessionRuntimeStateStore(projectID).GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.IssueID != issueID ||
		canonical.State != daemonstate.SessionStateRunning ||
		canonical.Activity != "waiting" ||
		canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v", canonical)
	}
}

func TestRuntimeSignalMaterializesStoredActivityEvidenceToCanonical(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"
	startedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	store := d.sessionRuntimeStateStore(projectID)
	if err := store.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:             "pr-az-42",
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		StartedAt:      &startedAt,
		UpdatedAt:      startedAt,
	}); err != nil {
		t.Fatalf("seed canonical session projection: %v", err)
	}
	if err := store.UpsertSessionActivityEvidence(context.Background(), daemonstate.SessionActivityEvidence{
		ProjectID:       projectID,
		SessionID:       "pr-az-42",
		IssueID:         issueID,
		Activity:        "idle",
		ActivitySource:  "hooks",
		SourceSessionID: "pr-az-42.pane-12",
		ObservedAt:      startedAt.Add(time.Second),
		UpdatedAt:       startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed activity evidence: %v", err)
	}

	if err := d.materializeSessionActivityEvidence(context.Background(), protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, []string{issueID}); err != nil {
		t.Fatalf("materializeSessionActivityEvidence: %v", err)
	}

	canonical, found, err := store.GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.IssueID != issueID ||
		canonical.State != daemonstate.SessionStateRunning ||
		canonical.Activity != "idle" ||
		canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v", canonical)
	}
}

func TestRuntimeSignalMaterializesBusyWhenAnyPaneEvidenceBusy(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"
	startedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	store := d.sessionRuntimeStateStore(projectID)
	if err := store.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:             "pr-az-42",
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "idle",
		ActivitySource: "hooks",
		StartedAt:      &startedAt,
		UpdatedAt:      startedAt,
	}); err != nil {
		t.Fatalf("seed canonical session projection: %v", err)
	}
	if err := store.UpsertSessionActivityEvidence(context.Background(), daemonstate.SessionActivityEvidence{
		ProjectID:       projectID,
		SessionID:       "pr-az-42",
		IssueID:         issueID,
		Activity:        "idle",
		ActivitySource:  "hooks",
		SourceSessionID: "pr-az-42.pane-12",
		ObservedAt:      startedAt.Add(2 * time.Second),
		UpdatedAt:       startedAt.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("seed idle activity evidence: %v", err)
	}
	if err := store.UpsertSessionActivityEvidence(context.Background(), daemonstate.SessionActivityEvidence{
		ProjectID:       projectID,
		SessionID:       "pr-az-42",
		IssueID:         issueID,
		Activity:        "busy",
		ActivitySource:  "hooks",
		SourceSessionID: "pr-az-42.pane-34",
		ObservedAt:      startedAt.Add(time.Second),
		UpdatedAt:       startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed busy activity evidence: %v", err)
	}

	if err := d.materializeSessionActivityEvidence(context.Background(), protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, []string{issueID}); err != nil {
		t.Fatalf("materializeSessionActivityEvidence: %v", err)
	}

	canonical, found, err := store.GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.Activity != "busy" || canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v, want busy/hooks", canonical)
	}
}

func TestRuntimeSignalActivityEvidenceDoesNotReviveStoppedCanonicalSession(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "az-42"
	startedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	store := d.sessionRuntimeStateStore(projectID)
	if err := store.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:             "pr-az-42",
		IssueID:        issueID,
		State:          daemonstate.SessionStateStopped,
		ObservedState:  daemonstate.SessionStateStopped,
		Activity:       "busy",
		ActivitySource: "session",
		StartedAt:      &startedAt,
		UpdatedAt:      startedAt,
	}); err != nil {
		t.Fatalf("seed stopped canonical session projection: %v", err)
	}
	if err := store.UpsertSessionActivityEvidence(context.Background(), daemonstate.SessionActivityEvidence{
		ProjectID:       projectID,
		SessionID:       "pr-az-42",
		IssueID:         issueID,
		Activity:        "idle",
		ActivitySource:  "hooks",
		SourceSessionID: "pr-az-42.pane-12",
		ObservedAt:      startedAt.Add(time.Second),
		UpdatedAt:       startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed activity evidence: %v", err)
	}

	if err := d.materializeSessionActivityEvidence(context.Background(), protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, []string{issueID}); err != nil {
		t.Fatalf("materializeSessionActivityEvidence: %v", err)
	}

	canonical, found, err := store.GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.State != daemonstate.SessionStateStopped ||
		canonical.ObservedState != daemonstate.SessionStateStopped ||
		canonical.Activity != "" ||
		canonical.ActivitySource != "" {
		t.Fatalf("canonical session projection = %+v, want stopped without activity", canonical)
	}
}

func TestRuntimeSignalMaterializesActivityEvidenceForRequestedIssuesOnly(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	startedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	store := d.sessionRuntimeStateStore(projectID)
	for _, issueID := range []string{"az-42", "az-43"} {
		sessionID := "pr-" + issueID
		if err := store.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
			ID:             sessionID,
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "session",
			StartedAt:      &startedAt,
			UpdatedAt:      startedAt,
		}); err != nil {
			t.Fatalf("seed canonical session projection %s: %v", issueID, err)
		}
		if err := store.UpsertSessionActivityEvidence(context.Background(), daemonstate.SessionActivityEvidence{
			ProjectID:       projectID,
			SessionID:       sessionID,
			IssueID:         issueID,
			Activity:        "idle",
			ActivitySource:  "hooks",
			SourceSessionID: sessionID + ".pane-12",
			ObservedAt:      startedAt.Add(time.Second),
			UpdatedAt:       startedAt.Add(time.Second),
		}); err != nil {
			t.Fatalf("seed activity evidence %s: %v", issueID, err)
		}
	}

	if err := d.materializeSessionActivityEvidence(context.Background(), protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, []string{"az-42"}); err != nil {
		t.Fatalf("materializeSessionActivityEvidence: %v", err)
	}

	materialized, found, err := store.GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState az-42: %v", err)
	}
	if !found || materialized.Activity != "idle" || materialized.ActivitySource != "hooks" {
		t.Fatalf("az-42 projection = %+v, found=%v, want materialized hook activity", materialized, found)
	}
	untouched, found, err := store.GetSessionState(context.Background(), projectID, "pr-az-43")
	if err != nil {
		t.Fatalf("GetSessionState az-43: %v", err)
	}
	if !found || untouched.Activity != "busy" || untouched.ActivitySource != "session" {
		t.Fatalf("az-43 projection = %+v, found=%v, want untouched session activity", untouched, found)
	}
}

func TestRuntimeReconcileMaterializesStoredActivityEvidenceToCanonical(t *testing.T) {
	projectID := "proj-signals"
	issueID := "az-42"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	tmuxRunner := &runtimeSignalNoRealTmuxRunner{liveSessions: []string{"pr-az-42"}}
	// This test exercises projection/evidence materialization through runtime.reconcile.
	// Keep it on a fake tmux client so fixture IDs such as pr-az-42 can never touch
	// the user's real tmux server.
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}
	startedAt := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	store := d.sessionRuntimeStateStore(projectID)
	if err := store.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:             "pr-az-42",
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		StartedAt:      &startedAt,
		UpdatedAt:      startedAt,
	}); err != nil {
		t.Fatalf("seed canonical session projection: %v", err)
	}
	if err := store.UpsertSessionActivityEvidence(context.Background(), daemonstate.SessionActivityEvidence{
		ProjectID:       projectID,
		SessionID:       "pr-az-42",
		IssueID:         issueID,
		Activity:        "idle",
		ActivitySource:  "hooks",
		SourceSessionID: "pr-az-42.pane-12",
		ObservedAt:      startedAt.Add(time.Second),
		UpdatedAt:       startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed activity evidence: %v", err)
	}

	if _, err := d.ensureRuntimeReconciler().Reconcile(context.Background(), projectID); err != nil {
		t.Fatalf("runtime reconcile: %v", err)
	}

	canonical, found, err := store.GetSessionState(context.Background(), projectID, "pr-az-42")
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found {
		t.Fatal("expected canonical session projection")
	}
	if canonical.IssueID != issueID ||
		canonical.State != daemonstate.SessionStateRunning ||
		canonical.Activity != "idle" ||
		canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v", canonical)
	}
	if got := tmuxRunner.mutatingCalls(); len(got) != 0 {
		t.Fatalf("runtime reconcile attempted mutating tmux commands: %+v", got)
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

type runtimeSignalNoRealTmuxRunner struct {
	calls        [][]string
	liveSessions []string
}

func (r *runtimeSignalNoRealTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "list-sessions":
		if len(r.liveSessions) == 0 {
			return "", nil
		}
		return strings.Join(r.liveSessions, "\n"), nil
	case "list-panes":
		return "", nil
	default:
		return "", nil
	}
}

func (r *runtimeSignalNoRealTmuxRunner) mutatingCalls() [][]string {
	mutating := make([][]string, 0)
	for _, call := range r.calls {
		if len(call) == 0 {
			continue
		}
		switch call[0] {
		case "list-sessions", "list-panes":
			continue
		default:
			mutating = append(mutating, append([]string(nil), call...))
		}
	}
	return mutating
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
