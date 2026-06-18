package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
)

const commandTaskClosePreflight = "task.close_preflight"

func TestParseOrchestrateStatusArgs(t *testing.T) {
	opts, err := ParseOrchestrateStatusArgs([]string{"--root", "az-123", "--since", "10", "--limit", "25", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateStatusArgs error = %v", err)
	}
	if opts.RootIssueID != "az-123" {
		t.Fatalf("RootIssueID = %q, want az-123", opts.RootIssueID)
	}
	if opts.SinceSeq != 10 {
		t.Fatalf("SinceSeq = %d, want 10", opts.SinceSeq)
	}
	if opts.Limit != 25 {
		t.Fatalf("Limit = %d, want 25", opts.Limit)
	}
	if !opts.JSON {
		t.Fatal("JSON = false, want true")
	}
}

func TestParseOrchestrateStatusArgs_RequiresRoot(t *testing.T) {
	if _, err := ParseOrchestrateStatusArgs([]string{"--since", "10"}); err == nil {
		t.Fatal("expected error for missing --root")
	}
}

func TestParseOrchestrateStartArgs_DefaultLimitAndIssues(t *testing.T) {
	opts, err := ParseOrchestrateStartArgs([]string{"--root", "az-123", "--issue", "az-3", "--issue", "az-2", "--issue", "az-3"})
	if err != nil {
		t.Fatalf("ParseOrchestrateStartArgs error = %v", err)
	}
	if opts.Limit != 4 {
		t.Fatalf("Limit = %d, want 4", opts.Limit)
	}
	if len(opts.IssueIDs) != 2 || opts.IssueIDs[0] != "az-2" || opts.IssueIDs[1] != "az-3" {
		t.Fatalf("IssueIDs = %+v, want [az-2 az-3]", opts.IssueIDs)
	}
}

func TestSessionStartWarningFromOperationResult(t *testing.T) {
	raw, err := json.Marshal(map[string]string{
		"output": "Starting session\nWorktree setup warning: git hook failed; recovered existing worktree\nSession started",
	})
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	warning := sessionStartWarningFromOperationResult(raw)
	if warning != "Worktree setup warning: git hook failed; recovered existing worktree" {
		t.Fatalf("warning = %q", warning)
	}
}

func TestParseOrchestrateWatchArgs(t *testing.T) {
	opts, err := ParseOrchestrateWatchArgs([]string{"--root", "az-1", "--since", "12", "--once"})
	if err != nil {
		t.Fatalf("ParseOrchestrateWatchArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.SinceSeq != 12 || !opts.Once {
		t.Fatalf("opts = %+v", opts)
	}
	if !opts.JSONL {
		t.Fatalf("JSONL = false, want true")
	}
}

func TestParseOrchestrateMessageArgs(t *testing.T) {
	opts, err := ParseOrchestrateMessageArgs([]string{"--root", "az-1", "--issue", "az-2", "--body", "Proceed now", "--force-self-delivery", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateMessageArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.IssueID != "az-2" || opts.Body != "Proceed now" || opts.Type != "orchestrator-message" || !opts.ForceSelfDelivery || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestWatchDaemonCommandRestartsAfterTransientSocketLoss(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	launcher := &fakeLauncher{}
	newLauncher = func(_, _ string) daemonStarter {
		return launcher
	}

	calls := 0
	got, err := watchDaemonCommand(&Dependencies{}, func(context.Context) (string, error) {
		calls++
		if !launcher.startCalled {
			return "", errors.New("dial unix /tmp/azedarach.sock: connect: connection refused")
		}
		return "reattached", nil
	})
	if err != nil {
		t.Fatalf("watchDaemonCommand error = %v", err)
	}
	if got != "reattached" {
		t.Fatalf("result = %q, want reattached", got)
	}
	if !launcher.startCalled {
		t.Fatal("launcher.Start was not called after transient transport loss")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want initial call, retry probe, and post-start call", calls)
	}
}

func TestShouldContinueOrchestrateWatchAfterTimeout(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "short frame socket timeout",
			err:  errors.New("daemon command transport: short_frame: read unix ->/tmp/daemon.sock: i/o timeout"),
			want: true,
		},
		{
			name: "context deadline",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "ordinary failure",
			err:  errors.New("list tasks: invalid response"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldContinueOrchestrateWatchAfterError(tt.err); got != tt.want {
				t.Fatalf("shouldContinueOrchestrateWatchAfterError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOrchestrateStatusCommandIncludesActiveSessionActivity(t *testing.T) {
	root := naming.IssueID("az-1")
	busy := naming.IssueID("az-2")
	unknown := naming.IssueID("az-3")
	noAgent := naming.IssueID("az-4")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						Active:      []string{busy.String(), unknown.String(), noAgent.String()},
						ActiveSessions: []daemonclient.TaskActiveSession{
							{IssueID: busy.String(), Activity: "busy", ActivitySource: "hooks", State: string(domain.SessionBusy), TmuxAttachedCount: 1},
							{IssueID: unknown.String(), Activity: "unknown", ActivitySource: "none", State: string(domain.SessionBusy), TmuxAttachedCount: 1, Advice: "activity unknown: inspect hooks with az ai status --target=auto; run az ai install --target=auto only if hooks are missing, outdated, or not installed; use sparse pane capture only if status/watch looks stale, failed, or contradictory for az-3"},
							{IssueID: noAgent.String(), Activity: "no-agent", ActivitySource: "session", State: string(domain.SessionBusy), TmuxAttachedCount: 1},
						},
					}), nil
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.ActiveSessions) != 3 {
		t.Fatalf("active_sessions = %+v, want three entries", result.ActiveSessions)
	}
	byID := map[string]orchestrateActiveSession{}
	for _, active := range result.ActiveSessions {
		byID[active.IssueID] = active
	}
	if byID[busy.String()].Activity != "busy" || byID[busy.String()].ActivitySource != "hooks" || byID[busy.String()].Advice != "" {
		t.Fatalf("busy active session = %+v", byID[busy.String()])
	}
	if byID[unknown.String()].Activity != "unknown" || !strings.Contains(byID[unknown.String()].Advice, "az ai status --target=auto") || !strings.Contains(byID[unknown.String()].Advice, "az ai install --target=auto") {
		t.Fatalf("unknown active session = %+v", byID[unknown.String()])
	}
	if byID[noAgent.String()].Activity != "no-agent" || byID[noAgent.String()].ActivitySource != "session" || byID[noAgent.String()].Advice != "" {
		t.Fatalf("no-agent active session = %+v", byID[noAgent.String()])
	}
}

func TestOrchestrateStatusCommandIncludesSessionStartProgress(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{},
						Blocked:     map[string]string{},
						SessionStartProgress: []daemonclient.TaskSessionStartProgress{
							{
								IssueID:        child.String(),
								OperationID:    "op-session-start",
								OperationState: "running",
								Phase:          "init_commands",
								Message:        "configured init commands likely running before agent hooks",
								Percent:        90,
								ElapsedMS:      12345,
							},
						},
					}), nil
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.SessionStartProgress) != 1 {
		t.Fatalf("session_start_progress = %+v, want one entry", result.SessionStartProgress)
	}
	progress := result.SessionStartProgress[0]
	if progress.IssueID != child.String() || progress.OperationID != "op-session-start" || progress.Phase != "init_commands" || progress.ElapsedMS != 12345 {
		t.Fatalf("session start progress = %+v", progress)
	}
}

func TestOrchestrateStatusCommandWarnsWhenRootEpicLacksWorktree(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	body, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		RepoDir:        "/repo",
		RuntimeRepoDir: "/repo-cif",
		ProjectID:      protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Warnings) != 1 ||
		!strings.Contains(result.Warnings[0], "root epic az-1 has child orchestration but no dedicated worktree") ||
		!strings.Contains(result.Warnings[0], "current worktree/path is /repo-cif") ||
		!strings.Contains(result.Warnings[0], "az worktree create az-1") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestBuildOrchestrateWatchFrameIncludesActiveSessionActivity(t *testing.T) {
	root := naming.IssueID("az-1")
	idle := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						Active:      []string{idle.String()},
						ActiveSessions: []daemonclient.TaskActiveSession{
							{IssueID: idle.String(), Activity: "idle", ActivitySource: "hooks", State: string(domain.SessionPaused), TmuxAttachedCount: 1},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{{Seq: 7, ParentIssue: root.String(), IssueID: idle, Type: "worker-progress"}}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	frame, err := buildOrchestrateWatchFrame(deps, root.String(), 3)
	if err != nil {
		t.Fatalf("buildOrchestrateWatchFrame error = %v", err)
	}
	if len(frame.ActiveSessions) != 1 {
		t.Fatalf("active_sessions = %+v, want one entry", frame.ActiveSessions)
	}
	active := frame.ActiveSessions[0]
	if active.IssueID != idle.String() || active.Activity != "idle" || active.ActivitySource != "hooks" || active.Advice != "" {
		t.Fatalf("active session = %+v", active)
	}
}

func TestBuildOrchestrateWatchFrameIncludesSessionStartProgress(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						SessionStartProgress: []daemonclient.TaskSessionStartProgress{
							{IssueID: child.String(), OperationID: "op-session-start", OperationState: "queued", Phase: "queued", Message: "queued session.start"},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	frame, err := buildOrchestrateWatchFrame(deps, root.String(), 0)
	if err != nil {
		t.Fatalf("buildOrchestrateWatchFrame error = %v", err)
	}
	if len(frame.SessionStartProgress) != 1 {
		t.Fatalf("session_start_progress = %+v, want one entry", frame.SessionStartProgress)
	}
	progress := frame.SessionStartProgress[0]
	if progress.IssueID != child.String() || progress.OperationID != "op-session-start" || progress.Phase != "queued" {
		t.Fatalf("session start progress = %+v", progress)
	}
}

func TestEmitOrchestrateWatchFramePrintsSessionStartProgress(t *testing.T) {
	frame := orchestrateWatchFrame{
		RootIssueID: "az-1",
		Active:      []string{"az-2"},
		ActiveSessions: []orchestrateActiveSession{
			{
				IssueID:        "az-2",
				Activity:       "busy",
				ActivitySource: "session",
				StartProgress: &orchestrateSessionStartProgress{
					IssueID:        "az-2",
					OperationState: "running",
					Phase:          "init_commands",
					Message:        "configured init commands likely running before agent hooks",
					Percent:        90,
				},
			},
		},
		SessionStartProgress: []orchestrateSessionStartProgress{
			{
				IssueID:        "az-3",
				OperationID:    "op-session-start",
				OperationState: "queued",
				Phase:          "queued",
				Message:        "waiting for session.start resources",
			},
		},
		Blocked: map[string]string{},
	}

	output := captureStdout(t, func() error {
		return emitOrchestrateWatchFrame(frame, false)
	})
	for _, want := range []string{
		"start: state=running phase=init_commands progress=90% configured init commands likely running before agent hooks",
		"session start progress:",
		"az-3: state=queued phase=queued operation=op-session-start waiting for session.start resources",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("watch output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateWatchSnapshotKeyIgnoresElapsedProgressTime(t *testing.T) {
	frame := orchestrateWatchFrame{
		Runnable: []string{"az-3"},
		Active:   []string{"az-2"},
		ActiveSessions: []orchestrateActiveSession{
			{
				IssueID:        "az-2",
				Activity:       "busy",
				ActivitySource: "session",
				StartProgress: &orchestrateSessionStartProgress{
					IssueID:        "az-2",
					OperationState: "running",
					Phase:          "init_commands",
					ElapsedMS:      1000,
				},
			},
		},
		SessionStartProgress: []orchestrateSessionStartProgress{
			{IssueID: "az-4", OperationID: "op-4", OperationState: "running", Phase: "worktree_preflight", ElapsedMS: 1000},
		},
		Blocked: map[string]string{},
	}
	first := orchestrateWatchFrameSnapshotKey(frame)
	frame.ActiveSessions[0].StartProgress.ElapsedMS = 5000
	frame.SessionStartProgress[0].ElapsedMS = 5000
	if second := orchestrateWatchFrameSnapshotKey(frame); second != first {
		t.Fatalf("snapshot key changed after elapsed-only update:\nfirst=%s\nsecond=%s", first, second)
	}
	frame.SessionStartProgress[0].Phase = "issue_resources"
	if second := orchestrateWatchFrameSnapshotKey(frame); second == first {
		t.Fatalf("snapshot key did not change after phase update")
	}
}

func TestParseOrchestrateCompleteCheckArgs(t *testing.T) {
	opts, err := ParseOrchestrateCompleteCheckArgs([]string{"--root", "az-1", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateCompleteCheckArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseOrchestratePromptArgs(t *testing.T) {
	opts, err := ParseOrchestratePromptArgs([]string{"--root", "az-1", "--issue", "az-2", "--coordination", "mailbox", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestratePromptArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.IssueID != "az-2" || opts.Coordination != "mailbox" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
	defaults, err := ParseOrchestratePromptArgs([]string{"--issue", "az-2"})
	if err != nil {
		t.Fatalf("ParseOrchestratePromptArgs default error = %v", err)
	}
	if defaults.Coordination != "native" {
		t.Fatalf("default Coordination = %q, want native", defaults.Coordination)
	}
	if _, err := ParseOrchestratePromptArgs([]string{"--root", "az-1"}); err == nil {
		t.Fatal("expected prompt error for missing --issue")
	}
	if _, err := ParseOrchestratePromptArgs([]string{"--issue", "az-2", "--coordination", "banana"}); err == nil {
		t.Fatal("expected prompt error for invalid coordination")
	}
}

func TestParseOrchestrateIntegrateAndCloseSessionArgs(t *testing.T) {
	integrate, err := ParseOrchestrateIntegrateArgs([]string{"--issue", "az-2", "--apply", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateIntegrateArgs error = %v", err)
	}
	if integrate.IssueID != "az-2" || !integrate.Apply || !integrate.JSON {
		t.Fatalf("integrate opts = %+v", integrate)
	}

	closeSession, err := ParseOrchestrateCloseSessionArgs([]string{"--issue", "az-2", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateCloseSessionArgs error = %v", err)
	}
	if closeSession.IssueID != "az-2" || !closeSession.JSON {
		t.Fatalf("close-session opts = %+v", closeSession)
	}

	if _, err := ParseOrchestrateIntegrateArgs([]string{"--json"}); err == nil {
		t.Fatal("expected integrate error for missing --issue")
	}
	if _, err := ParseOrchestrateCloseSessionArgs([]string{"--json"}); err == nil {
		t.Fatal("expected close-session error for missing --issue")
	}
}

func TestOrchestrateStartSubmitsOperationAndWarnsOnDirtyParent(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	}
	taskListBody, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	commands := []string{}
	var submitted protocol.OperationSubmitRequestBody
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: child.String(), TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{Modified: []string{"parent.go"}}}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": "az-2", "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case protocol.CommandOperationSubmit:
					if err := json.Unmarshal(req.Body, &submitted); err != nil {
						t.Fatalf("decode submit body: %v", err)
					}
					return responseWithJSON(req, protocol.OperationSubmitResponseBody{
						Created: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateQueued,
						},
					}), nil
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateDone,
						},
					}), nil
				case protocol.CommandMailSend:
					return responseWithJSON(req, protocol.MailEvent{Seq: 1, ParentIssue: "az-1", IssueID: child, Type: "session-started"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStartCommand(deps, OrchestrateStartOptions{RootIssueID: "az-1", IssueIDs: []string{"az-2"}, Limit: 4, JSON: true})
	})

	var result orchestrateStartResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Launched) != 1 || result.Launched[0].OperationID != "op-1" || result.Launched[0].WorktreePath != "/repo-az-2" {
		t.Fatalf("launched = %+v", result.Launched)
	}
	if !strings.Contains(result.Advice.WatchInstruction, "leave it running") || strings.Contains(result.Advice.WatchCommand, "--once") {
		t.Fatalf("watch advice = %+v, want continuous watch instruction without --once", result.Advice)
	}
	if len(result.Warnings) != 2 || !strings.Contains(strings.Join(result.Warnings, "\n"), "az worktree create az-1") || !strings.Contains(strings.Join(result.Warnings, "\n"), "uncommitted tracked changes") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	if submitted.Kind != commandSessionStart || submitted.IssueID != child || len(submitted.Payload) == 0 {
		t.Fatalf("submitted = %+v", submitted)
	}
	if strings.Join(commands, ",") == "" || !containsString(commands, protocol.CommandOperationSubmit) || !containsString(commands, protocol.CommandOperationGet) {
		t.Fatalf("commands = %+v, want operation submit and wait", commands)
	}
}

func TestOrchestrateStartWaitsForAllSubmittedOperationsBeforeMail(t *testing.T) {
	root := naming.IssueID("az-1")
	childA := naming.IssueID("az-2")
	childB := naming.IssueID("az-3")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: childA, Title: "Worker A", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
		{ID: childB, Title: "Worker B", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	}
	taskListBody, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	submitted := map[string]bool{}
	waited := map[string]bool{}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{childA.String(), childB.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					var body struct {
						TaskID string `json:"task_id"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode merge base target body: %v", err)
					}
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: body.TaskID, TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case protocol.CommandOperationSubmit:
					var body protocol.OperationSubmitRequestBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode submit body: %v", err)
					}
					submitted[body.IssueID.String()] = true
					return responseWithJSON(req, protocol.OperationSubmitResponseBody{
						Created: true,
						Operation: protocol.OperationRecord{
							OperationID: naming.OperationID("op-" + body.IssueID.String()),
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     body.IssueID,
							State:       protocol.OperationStateQueued,
						},
					}), nil
				case protocol.CommandOperationGet:
					var body protocol.OperationGetRequestBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode operation get body: %v", err)
					}
					if !submitted[childA.String()] || !submitted[childB.String()] {
						t.Fatalf("waited for %s before all requested starts were submitted: submitted=%+v", body.OperationID, submitted)
					}
					issueID := strings.TrimPrefix(body.OperationID.String(), "op-")
					waited[issueID] = true
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: body.OperationID,
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     naming.IssueID(issueID),
							State:       protocol.OperationStateDone,
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"project_id": protocol.DefaultProjectID, "worktrees": []map[string]string{}}), nil
				case protocol.CommandMailSend:
					var body protocol.MailSendCommandBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode mail body: %v", err)
					}
					if !waited[body.IssueID.String()] {
						t.Fatalf("sent session-started mail for %s before operation wait completed", body.IssueID)
					}
					return responseWithJSON(req, protocol.MailEvent{Seq: 1, ParentIssue: "az-1", IssueID: body.IssueID, Type: "session-started"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	result, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: "az-1", IssueIDs: []string{"az-2", "az-3"}, Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart error = %v", err)
	}
	if len(result.Started) != 2 || len(result.Failed) != 0 {
		t.Fatalf("result started=%+v failed=%+v, want both started with no failures", result.Started, result.Failed)
	}
}

func TestOrchestrateStartWarnsWhenRootWorktreeMissingWithoutRunnableLaunches(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Blocked worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		RepoDir:        "/repo",
		RuntimeRepoDir: "/repo-cif",
		ProjectID:      protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{},
						Blocked:     map[string]string{child.String(): "blocked by az-3"},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"project_id": protocol.DefaultProjectID, "worktrees": []map[string]string{}}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	result, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: root.String(), Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart error = %v", err)
	}
	if len(result.Requested) != 0 || len(result.Started) != 0 {
		t.Fatalf("requested=%+v started=%+v, want no launches", result.Requested, result.Started)
	}
	if len(result.Warnings) != 1 ||
		!strings.Contains(result.Warnings[0], "root epic az-1 has child orchestration but no dedicated worktree") ||
		!strings.Contains(result.Warnings[0], "current worktree/path is /repo-cif") ||
		!strings.Contains(result.Warnings[0], "az worktree create az-1") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestOrchestratePromptCommandPrintsNativeWorkerHandoff(t *testing.T) {
	deps, root, child := orchestratePromptTestDeps(t)

	output := captureStdout(t, func() error {
		return OrchestratePromptCommand(deps, OrchestratePromptOptions{RootIssueID: root.String(), IssueID: child.String(), Coordination: "native"})
	})
	for _, want := range []string{
		"Work on issue az-2: Worker task",
		"Coordination mode: native",
		"az spec read --issue az-2",
		"Current notes: present but omitted from worker prompt. Run `az issue get az-2 --with-notes` only if full note history is necessary.",
		"Status semantics: use `in_progress` while actively working, `in_review` when your implementation is complete and ready for orchestrator review/integration",
		"Blocked work is represented by dependency edges or `worker-blocked` mailbox events, not by setting issue status to `in_review`.",
		"Do not append raw logs, exploratory transcripts, routine progress narration, duplicate prompt context, or speculative scratch work to notes.",
		"Return progress, blockers, and final results through the native subagent result channel.",
		"Do not use `az mail` unless the orchestrator explicitly asks for mailbox coordination.",
		"Do not close root issue `az-1`",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for _, unexpected := range []string{"Coordination mailbox parent:", "az mail send", "worker-complete"} {
		if strings.Contains(output, unexpected) {
			t.Fatalf("output unexpectedly contains %q:\n%s", unexpected, output)
		}
	}
	if strings.Contains(output, "Spec impact TBD") {
		t.Fatalf("output should not include full historical notes by default:\n%s", output)
	}
}

func TestOrchestratePromptCommandMailboxCoordinationOptIn(t *testing.T) {
	deps, root, child := orchestratePromptTestDeps(t)

	output := captureStdout(t, func() error {
		return OrchestratePromptCommand(deps, OrchestratePromptOptions{RootIssueID: root.String(), IssueID: child.String(), Coordination: "mailbox"})
	})
	for _, want := range []string{
		"Coordination mode: mailbox",
		"Coordination mailbox parent: az-1",
		"Use mailbox events for hybrid coordination",
		"Check inbound orchestrator messages with `az mail list --parent az-1 --since 0 --json` before declaring yourself blocked or idle",
		"Report to parent `az-1` with `az mail send --parent az-1 --issue az-2 --type <worker-progress|worker-blocked|worker-integration-ready> --body \"<evidence>\"`; do not use `az orchestrate message` for your own status",
		"az mail list --parent az-1 --since 0 --json",
		"`worker-ready` and `worker-complete` are accepted only as legacy aliases for `worker-integration-ready`",
		"az mail send --parent az-1 --issue az-2 --type worker-integration-ready",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateMessageCommandRecordsMailboxThenDeliversToSession(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-9")
	commands := []string{}
	var mailBody protocol.MailSendCommandBody
	var sessionBody struct {
		SessionID naming.SessionID `json:"session_id"`
		Message   string           `json:"message"`
	}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case protocol.CommandMailSend:
					if err := json.Unmarshal(req.Body, &mailBody); err != nil {
						t.Fatalf("decode mail body: %v", err)
					}
					return responseWithJSON(req, protocol.MailEvent{
						Seq:         7,
						ParentIssue: "az-1",
						IssueID:     naming.IssueID("az-2"),
						Type:        "orchestrator-message",
						From:        "orchestrator",
						To:          "az-2",
						Body:        "Proceed now",
					}), nil
				case daemonclient.CommandSessionMessage:
					if err := json.Unmarshal(req.Body, &sessionBody); err != nil {
						t.Fatalf("decode session message body: %v", err)
					}
					return responseWithJSON(req, map[string]string{"output": "Sent message to session: az-2\n"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateMessageCommand(deps, OrchestrateMessageOptions{
			RootIssueID: "az-1",
			IssueID:     "az-2",
			Type:        "orchestrator-message",
			Body:        "Proceed now",
			JSON:        true,
		})
	})

	if len(commands) != 2 || commands[0] != protocol.CommandMailSend || commands[1] != daemonclient.CommandSessionMessage {
		t.Fatalf("commands = %+v, want mail send then session message", commands)
	}
	if mailBody.ParentIssue != "az-1" || mailBody.IssueID != "az-2" || mailBody.Type != "orchestrator-message" || mailBody.From != "orchestrator" || mailBody.To != "az-2" || mailBody.Body != "Proceed now" {
		t.Fatalf("mail body = %+v", mailBody)
	}
	if sessionBody.SessionID != "az-2" || !strings.Contains(sessionBody.Message, "Orchestrator message for issue az-2 under root az-1") || !strings.Contains(sessionBody.Message, "Proceed now") {
		t.Fatalf("session body = %+v", sessionBody)
	}
	var result orchestrateMessageResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, output)
	}
	if !result.Delivered || result.Mailbox.Seq != 7 {
		t.Fatalf("result = %+v", result)
	}
}

func TestOrchestrateMessageCommandRejectsActiveWorkerSelfDelivery(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-2")
	commands := []string{}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				t.Fatalf("unexpected command after self-delivery guard: %s", req.Command)
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	err := OrchestrateMessageCommand(deps, OrchestrateMessageOptions{
		RootIssueID: "az-1",
		IssueID:     "az-2",
		Type:        "worker-integration-ready",
		Body:        "ready",
	})
	if err == nil {
		t.Fatal("expected self-delivery guard error")
	}
	if !strings.Contains(err.Error(), "refusing to deliver orchestrate message to the active issue az-2") ||
		!strings.Contains(err.Error(), "az mail send --parent az-1 --issue az-2 --type worker-integration-ready") {
		t.Fatalf("error = %v, want self-delivery guidance", err)
	}
	if len(commands) != 0 {
		t.Fatalf("commands = %+v, want no mailbox or session commands", commands)
	}
}

func TestOrchestrateMessageCommandForceSelfDelivery(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-2")
	commands := []string{}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case protocol.CommandMailSend:
					return responseWithJSON(req, protocol.MailEvent{Seq: 8, ParentIssue: "az-1", IssueID: naming.IssueID("az-2"), Type: "orchestrator-message"}), nil
				case daemonclient.CommandSessionMessage:
					return responseWithJSON(req, map[string]string{"output": "Sent message to session: az-2\n"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateMessageCommand(deps, OrchestrateMessageOptions{
			RootIssueID:       "az-1",
			IssueID:           "az-2",
			Type:              "orchestrator-message",
			Body:              "intentional self message",
			ForceSelfDelivery: true,
			JSON:              true,
		})
	})

	if len(commands) != 2 || commands[0] != protocol.CommandMailSend || commands[1] != daemonclient.CommandSessionMessage {
		t.Fatalf("commands = %+v, want mail send then session message", commands)
	}
	var result orchestrateMessageResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, output)
	}
	if !result.Delivered || result.Mailbox.Seq != 8 {
		t.Fatalf("result = %+v", result)
	}
}

func orchestratePromptTestDeps(t *testing.T) (*Dependencies, naming.IssueID, naming.IssueID) {
	t.Helper()
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{
			ID:              child,
			Title:           "Worker task",
			Description:     "Do a focused slice",
			Design:          "Keep it small",
			Acceptance:      "Tests pass",
			Notes:           "Spec impact TBD",
			Status:          domain.StatusOpen,
			Priority:        domain.P2,
			Type:            domain.TypeTask,
			ParentID:        &root,
			Implementations: []string{"go-bubbletea"},
		},
	}
	body, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list response: %v", err)
	}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
			},
		}),
	}
	return deps, root, child
}

func TestOrchestrateIntegrateCommandPrintsGuidance(t *testing.T) {
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
						IssueID:       child.String(),
						ParentIssueID: parent.String(),
						Ready:         true,
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}
	output := captureStdout(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2"})
	})
	for _, want := range []string{"Worktree: /repo-az-2", "az branch merge az-2", "az issue close --id az-2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateIntegrateCommandBlocksMergeWithoutCompletionEvidence(t *testing.T) {
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
						IssueID:       child.String(),
						ParentIssueID: parent.String(),
						Ready:         false,
						Reasons: []string{
							"issue az-2 is not closed",
							"no worker-integration-ready mailbox event found under parent az-1 for az-2",
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}
	output := captureStdout(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: child.String()})
	})
	if !strings.Contains(output, "Merge guidance: BLOCKED") {
		t.Fatalf("output missing blocked merge guidance:\n%s", output)
	}
	if strings.Contains(output, "az branch merge az-2") {
		t.Fatalf("output unexpectedly suggests branch merge:\n%s", output)
	}
	if strings.Contains(output, "az issue close --id az-2") || strings.Contains(output, "az orchestrate close-session --issue az-2") {
		t.Fatalf("output unexpectedly suggests close/cleanup while blocked:\n%s", output)
	}
}

func TestOrchestrateIntegrateApplySuccess(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "")
	output := captureStdout(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true})
	})
	for _, want := range []string{"Merged user/az-2/worker into user/az-1/parent", "integrate_and_close: success", "append_evidence: success"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for _, want := range []string{daemonclient.CommandTaskClose, daemonclient.CommandTaskAppendNotes} {
		if !containsString(*commands, want) {
			t.Fatalf("commands = %+v, want %s", *commands, want)
		}
	}
	for _, unexpected := range []string{daemonclient.CommandGitMerge, commandTaskClosePreflight, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(*commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect client-side integration/cleanup command %s", *commands, unexpected)
		}
	}
}

func TestOrchestrateIntegrateApplyRequiresCompletionEvidence(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "missing_evidence")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true, JSON: true})
	})
	if err == nil {
		t.Fatal("expected missing completion evidence error")
	}
	if !strings.Contains(output, `"apply": true`) || !strings.Contains(output, `"applied": false`) || !strings.Contains(output, `"name": "completion_evidence"`) || !strings.Contains(output, `"recovery"`) {
		t.Fatalf("output missing structured failure:\n%s", output)
	}
	for _, unexpected := range []string{daemonclient.CommandGitMerge, daemonclient.CommandTaskClose, commandTaskClosePreflight, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(*commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect %s", *commands, unexpected)
		}
	}
}

func TestOrchestrateIntegrateApplyDaemonIntegrationFailureStopsBeforeAppend(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "merge")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true})
	})
	if err == nil {
		t.Fatal("expected daemon integration failure")
	}
	if !strings.Contains(output, "integrate_and_close: failed") || !strings.Contains(output, "az branch merge az-2") {
		t.Fatalf("output missing daemon integration failure recovery:\n%s", output)
	}
	if containsString(*commands, daemonclient.CommandTaskAppendNotes) || containsString(*commands, daemonclient.CommandGitMerge) || containsString(*commands, commandTaskClosePreflight) || containsString(*commands, commandSessionStop) || containsString(*commands, daemonclient.CommandWorktreeRemove) || containsString(*commands, daemonclient.CommandTaskUpdateStatus) {
		t.Fatalf("commands = %+v, append/client integration cleanup should not run after daemon integration failure", *commands)
	}
}

func TestOrchestrateIntegrateApplySurfacesDaemonIntegrationConflict(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "merge_conflict")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true, JSON: true})
	})
	if err == nil {
		t.Fatal("expected daemon integration conflict")
	}
	if !strings.Contains(output, `"applied": false`) || !strings.Contains(output, "README.md") || !strings.Contains(output, `"name": "integrate_and_close"`) {
		t.Fatalf("output missing daemon integration conflict:\n%s", output)
	}
	if containsString(*commands, daemonclient.CommandTaskAppendNotes) || containsString(*commands, daemonclient.CommandGitMerge) || containsString(*commands, commandTaskClosePreflight) || containsString(*commands, commandSessionStop) || containsString(*commands, daemonclient.CommandWorktreeRemove) || containsString(*commands, daemonclient.CommandTaskUpdateStatus) {
		t.Fatalf("commands = %+v, append/client integration cleanup should not run after daemon integration conflict", *commands)
	}
}

func TestOrchestrateIntegrateApplyReportsDaemonCloseFailure(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "close_issue")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true})
	})
	if err == nil {
		t.Fatal("expected daemon close failure")
	}
	if !strings.Contains(output, "integrate_and_close: failed") || !strings.Contains(output, "Recovery:") {
		t.Fatalf("output missing daemon close failure:\n%s", output)
	}
	if !containsString(*commands, daemonclient.CommandTaskClose) || containsString(*commands, daemonclient.CommandTaskAppendNotes) {
		t.Fatalf("commands = %+v, want daemon close without append after close failure", *commands)
	}
	for _, unexpected := range []string{daemonclient.CommandGitMerge, commandTaskClosePreflight, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(*commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect client-side integration/cleanup command %s", *commands, unexpected)
		}
	}
}

func orchestrateIntegrateApplyDeps(t *testing.T, failStep string) (*Dependencies, *[]string) {
	t.Helper()
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	commands := make([]string, 0, 16)
	deps := &Dependencies{
		RepoDir:   "/repo-parent",
		ProjectID: protocol.DefaultProjectID,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": parent.String(), "path": "/repo-parent", "branch": "user/az-1/parent"},
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					if failStep == "missing_evidence" {
						return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
							IssueID:       child.String(),
							ParentIssueID: parent.String(),
							Ready:         false,
							Reasons: []string{
								"issue az-2 is not closed",
								"no worker-integration-ready mailbox event found under parent az-1 for az-2",
							},
						}), nil
					}
					return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
						IssueID:       child.String(),
						ParentIssueID: parent.String(),
						Ready:         true,
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        child.String(),
						TargetID:       parent.String(),
						Branch:         "user/az-1/parent",
						WorktreePath:   "/repo-parent",
						BranchAttached: true,
						AncestorChain:  []string{parent.String()},
					}), nil
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: parent, Status: domain.StatusInProgress, Type: domain.TypeTask},
						{ID: child, Status: domain.StatusDone, Type: domain.TypeTask, ParentID: &parent, HasTmuxSession: true, HasWorktree: true},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{HasChanges: false}}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: "/repo-parent", Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: "/repo-parent", Branch: "user/az-1/parent"}), nil
				case daemonclient.CommandGitMerge:
					if failStep == "merge" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("merge failed")
					}
					if failStep == "merge_conflict" {
						return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
							Worktree: "/repo-parent",
							Branch:   "user/az-2/worker",
							Result: gitservice.MergeResult{
								Success:       false,
								HasConflicts:  true,
								ConflictFiles: []string{"README.md"},
								Message:       "CONFLICT (content): Merge conflict in README.md",
							},
						}), nil
					}
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: "/repo-parent",
						Branch:   "user/az-2/worker",
						Result:   gitservice.MergeResult{Success: true},
					}), nil
				case daemonclient.CommandTaskClose:
					var body struct {
						IntegrateBeforeClose bool `json:"integrate_before_close"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode task close body: %v", err)
					}
					if !body.IntegrateBeforeClose {
						t.Fatalf("task.close integrate_before_close = false, want true")
					}
					if failStep == "merge" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("integrate before closing az-2: merge failed")
					}
					if failStep == "merge_conflict" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("integrate before closing az-2: merge user/az-2/worker into user/az-1/parent failed: CONFLICT (content): Merge conflict in README.md")
					}
					if failStep == "close_issue" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("close failed")
					}
					return responseWithJSON(req, daemonclient.TaskCloseResult{
						TaskID:                 child.String(),
						Status:                 string(domain.StatusDone),
						IntegrationRequested:   true,
						Integrated:             true,
						IntegratedSourceBranch: "user/az-2/worker",
						IntegratedTargetBranch: "user/az-1/parent",
						SessionStopped:         true,
						WorktreeRemoved:        true,
						Revision:               3,
					}), nil
				case daemonclient.CommandTaskAppendNotes:
					return responseWithJSON(req, map[string]any{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}
	return deps, &commands
}

func TestOrchestrateCloseSessionCommandStopsSession(t *testing.T) {
	var gotCommand string
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotCommand = req.Command
				return responseWithOutput(req, "stopped\n"), nil
			},
		}),
	}
	output := captureStdout(t, func() error {
		return OrchestrateCloseSessionCommand(deps, OrchestrateCloseSessionOptions{IssueID: "az-2"})
	})
	if gotCommand != commandSessionStop {
		t.Fatalf("command = %q, want %q", gotCommand, commandSessionStop)
	}
	if !strings.Contains(output, "az issue close --id az-2") || strings.Contains(output, "--cleanup") {
		t.Fatalf("output = %q, want cleanup-close guidance", output)
	}
}

func TestNextMailboxSeq(t *testing.T) {
	events := []protocol.MailEvent{
		{Seq: 11},
		{Seq: 18},
		{Seq: 14},
	}
	if got := nextMailboxSeq(events, 10); got != 18 {
		t.Fatalf("nextMailboxSeq = %d, want 18", got)
	}
	if got := nextMailboxSeq(nil, 10); got != 10 {
		t.Fatalf("nextMailboxSeq(nil) = %d, want 10", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func captureStdoutAllowError(t *testing.T, fn func() error) (string, error) {
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

	var buf strings.Builder
	_, copyErr := io.Copy(&buf, r)
	os.Stdout = oldStdout
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stdout: %v", copyErr)
	}
	return buf.String(), runErr
}
