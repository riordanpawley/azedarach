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
	opts, err := ParseOrchestrateMessageArgs([]string{"--root", "az-1", "--issue", "az-2", "--body", "Proceed now", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateMessageArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.IssueID != "az-2" || opts.Body != "Proceed now" || opts.Type != "orchestrator-message" || !opts.JSON {
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

func TestOrchestrateStatusCommandIncludesActiveSessionActivity(t *testing.T) {
	root := naming.IssueID("az-1")
	busy := naming.IssueID("az-2")
	unknown := naming.IssueID("az-3")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{
			ID:             busy,
			Title:          "Busy worker",
			Status:         domain.StatusInProgress,
			Type:           domain.TypeTask,
			ParentID:       &root,
			HasTmuxSession: true,
			Session:        &domain.Session{IssueID: busy, State: domain.SessionBusy, Activity: "busy", ActivitySource: "hooks", TmuxAttachedCount: 1},
		},
		{
			ID:             unknown,
			Title:          "Unknown worker",
			Status:         domain.StatusInProgress,
			Type:           domain.TypeTask,
			ParentID:       &root,
			HasTmuxSession: true,
			Session:        &domain.Session{IssueID: unknown, State: domain.SessionBusy, Activity: "unknown", ActivitySource: "none", TmuxAttachedCount: 1},
		},
	}
	body, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list response: %v", err)
	}
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
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
	if len(result.ActiveSessions) != 2 {
		t.Fatalf("active_sessions = %+v, want two entries", result.ActiveSessions)
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
}

func TestBuildOrchestrateWatchFrameIncludesActiveSessionActivity(t *testing.T) {
	root := naming.IssueID("az-1")
	idle := naming.IssueID("az-2")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{
			ID:             idle,
			Title:          "Idle worker",
			Status:         domain.StatusInProgress,
			Type:           domain.TypeTask,
			ParentID:       &root,
			HasTmuxSession: true,
			Session:        &domain.Session{IssueID: idle, State: domain.SessionPaused, Activity: "idle", ActivitySource: "hooks", TmuxAttachedCount: 1},
		},
	}
	body, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list response: %v", err)
	}
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
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

func TestEvaluateOrchestrateCompleteCheck_Pass(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	tasks := []domain.Task{
		{ID: root, Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, ParentID: &root, Status: domain.StatusDone, Type: domain.TypeTask},
	}
	result, err := evaluateOrchestrateCompleteCheck("az-1", tasks)
	if err != nil {
		t.Fatalf("evaluateOrchestrateCompleteCheck error = %v", err)
	}
	if !result.Pass {
		t.Fatalf("Pass = false, reasons = %+v", result.Reasons)
	}
}

func TestEvaluateOrchestrateCompleteCheck_Failures(t *testing.T) {
	root := naming.IssueID("az-1")
	leaf1 := naming.IssueID("az-2")
	leaf2 := naming.IssueID("az-3")
	tasks := []domain.Task{
		{ID: root, Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: leaf1, ParentID: &root, Status: domain.StatusOpen, Type: domain.TypeTask},
		{ID: leaf2, ParentID: &root, Status: domain.StatusDone, Type: domain.TypeTask, HasTmuxSession: true},
	}
	result, err := evaluateOrchestrateCompleteCheck("az-1", tasks)
	if err != nil {
		t.Fatalf("evaluateOrchestrateCompleteCheck error = %v", err)
	}
	if result.Pass {
		t.Fatalf("Pass = true, want false")
	}
	if len(result.Reasons) < 2 {
		t.Fatalf("reasons = %+v, want multiple blockers", result.Reasons)
	}
	if len(result.Advice) == 0 {
		t.Fatalf("advice = empty, want remediation commands")
	}
	if joined := strings.Join(result.Advice, "\n"); !strings.Contains(joined, "az orchestrate close-session --issue az-3") || !strings.Contains(joined, "az issue close --id az-2") || strings.Contains(joined, "--cleanup") {
		t.Fatalf("advice = %+v, want close-session and cleanup-close guidance", result.Advice)
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
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
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
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "uncommitted tracked changes") {
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
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
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
				case daemonclient.CommandTaskList:
					tasks := []domain.Task{
						{ID: parent, Status: domain.StatusInProgress, Type: domain.TypeTask},
						{ID: child, Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &parent},
					}
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{
						{Seq: 3, ParentIssue: parent.String(), IssueID: child, Type: "worker-integration-ready"},
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

func TestIsWorkerIntegrationReadyMailTypeAcceptsLegacyAliases(t *testing.T) {
	tests := map[string]bool{
		"worker-integration-ready":       true,
		" worker-integration-ready ":     true,
		"WORKER-INTEGRATION-READY":       true,
		"worker-ready":                   true,
		" worker-ready ":                 true,
		"worker-complete":                true,
		" worker-complete ":              true,
		"worker-progress":                false,
		"worker-blocked":                 false,
		"dependency-ready":               false,
		"worker-integration-ready-later": false,
		"worker-ready-later":             false,
		"worker-completed":               false,
	}
	for eventType, want := range tests {
		t.Run(eventType, func(t *testing.T) {
			if got := isWorkerIntegrationReadyMailType(eventType); got != want {
				t.Fatalf("isWorkerIntegrationReadyMailType(%q) = %v, want %v", eventType, got, want)
			}
		})
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
				case daemonclient.CommandTaskList:
					tasks := []domain.Task{
						{ID: parent, Status: domain.StatusInProgress, Type: domain.TypeTask},
						{ID: child, Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &parent},
					}
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{{Seq: 2, ParentIssue: parent.String(), IssueID: child, Type: "worker-progress"}}), nil
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
	for _, want := range []string{"Merged user/az-2/worker into user/az-1/parent (az-2)", "merge: success", "close_preflight: success", "stop_session: success", "remove_worktree: success", "close_issue: success", "append_evidence: success"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for _, want := range []string{daemonclient.CommandGitMerge, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus, daemonclient.CommandTaskAppendNotes} {
		if !containsString(*commands, want) {
			t.Fatalf("commands = %+v, want %s", *commands, want)
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
	for _, unexpected := range []string{daemonclient.CommandGitMerge, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(*commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect %s", *commands, unexpected)
		}
	}
}

func TestOrchestrateIntegrateApplyMergeFailureStopsBeforeCleanup(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "merge")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true})
	})
	if err == nil {
		t.Fatal("expected merge failure")
	}
	if !strings.Contains(output, "merge: failed") || !strings.Contains(output, "az branch merge az-2") {
		t.Fatalf("output missing merge failure recovery:\n%s", output)
	}
	if containsString(*commands, commandSessionStop) || containsString(*commands, daemonclient.CommandWorktreeRemove) || containsString(*commands, daemonclient.CommandTaskUpdateStatus) {
		t.Fatalf("commands = %+v, cleanup should not run after merge failure", *commands)
	}
}

func TestOrchestrateIntegrateApplyTreatsNonSuccessfulMergeResultAsFailure(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "merge_conflict")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true, JSON: true})
	})
	if err == nil {
		t.Fatal("expected merge result failure")
	}
	if !strings.Contains(output, `"applied": false`) || !strings.Contains(output, "README.md") || !strings.Contains(output, `"name": "merge"`) {
		t.Fatalf("output missing merge result failure:\n%s", output)
	}
	if containsString(*commands, commandSessionStop) || containsString(*commands, daemonclient.CommandWorktreeRemove) || containsString(*commands, daemonclient.CommandTaskUpdateStatus) {
		t.Fatalf("commands = %+v, cleanup should not run after non-successful merge result", *commands)
	}
}

func TestOrchestrateIntegrateApplyReportsPartialCloseFailures(t *testing.T) {
	deps, commands := orchestrateIntegrateApplyDeps(t, "close_issue")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true})
	})
	if err == nil {
		t.Fatal("expected partial cleanup failure")
	}
	if !strings.Contains(output, "close_issue: failed") || !strings.Contains(output, "Recovery:") {
		t.Fatalf("output missing partial close failure:\n%s", output)
	}
	if !containsString(*commands, commandSessionStop) || !containsString(*commands, daemonclient.CommandWorktreeRemove) || !containsString(*commands, daemonclient.CommandTaskAppendNotes) {
		t.Fatalf("commands = %+v, want cleanup continuation after close failure", *commands)
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
				case daemonclient.CommandTaskList:
					childStatus := domain.StatusDone
					if failStep == "missing_evidence" {
						childStatus = domain.StatusInProgress
					}
					body, err := marshalTaskListBody([]domain.Task{
						{ID: parent, Status: domain.StatusInProgress, Type: domain.TypeTask},
						{ID: child, Status: childStatus, Type: domain.TypeTask, ParentID: &parent, HasTmuxSession: true, HasWorktree: true},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{{Seq: 1, ParentIssue: parent.String(), IssueID: child, Type: "worker-progress"}}), nil
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
				case commandSessionStop:
					return responseWithOutput(req, "stopped\n"), nil
				case daemonclient.CommandWorktreeRemove:
					if failStep == "remove_worktree" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("remove worktree failed")
					}
					return responseWithJSON(req, map[string]any{}), nil
				case daemonclient.CommandTaskUpdateStatus:
					if failStep == "close_issue" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("close failed")
					}
					return responseWithJSON(req, map[string]any{}), nil
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
