package cli

import (
	"context"
	"encoding/json"
	"errors"
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

func TestParseOrchestrateCompleteCheckArgs(t *testing.T) {
	opts, err := ParseOrchestrateCompleteCheckArgs([]string{"--root", "az-1", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateCompleteCheckArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseOrchestrateIntegrateAndCloseSessionArgs(t *testing.T) {
	integrate, err := ParseOrchestrateIntegrateArgs([]string{"--issue", "az-2", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateIntegrateArgs error = %v", err)
	}
	if integrate.IssueID != "az-2" || !integrate.JSON {
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
	if joined := strings.Join(result.Advice, "\n"); !strings.Contains(joined, "az orchestrate close-session --issue az-3") || !strings.Contains(joined, "az issue close az-2") {
		t.Fatalf("advice = %+v, want close-session and issue-close guidance", result.Advice)
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
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "uncommitted tracked changes") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	if submitted.Kind != commandSessionStart || submitted.IssueID != child || len(submitted.Payload) == 0 {
		t.Fatalf("submitted = %+v", submitted)
	}
	if strings.Join(commands, ",") == "" || !containsString(commands, protocol.CommandOperationSubmit) {
		t.Fatalf("commands = %+v, want operation submit", commands)
	}
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
						{Seq: 3, ParentIssue: parent.String(), IssueID: child, Type: "worker-complete"},
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
	for _, want := range []string{"Worktree: /repo-az-2", "az branch merge az-2", "az orchestrate close-session --issue az-2"} {
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
	if !strings.Contains(output, "az orchestrate close-session --issue az-2") {
		t.Fatalf("output missing close-session command:\n%s", output)
	}
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
	if !strings.Contains(output, "az issue close az-2") {
		t.Fatalf("output = %q, want issue close guidance", output)
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
