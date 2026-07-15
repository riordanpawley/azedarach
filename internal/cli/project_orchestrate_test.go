package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRootlessOrchestrateParsersAcceptProjectScope(t *testing.T) {
	tests := []struct {
		name  string
		parse func([]string) error
	}{
		{"status", func(args []string) error { _, err := ParseOrchestrateStatusArgs(args); return err }},
		{"start", func(args []string) error { _, err := ParseOrchestrateStartArgs(args); return err }},
		{"watch", func(args []string) error { _, err := ParseOrchestrateWatchArgs(args); return err }},
		{"complete-check", func(args []string) error { _, err := ParseOrchestrateCompleteCheckArgs(args); return err }},
		{"review accept", func(args []string) error {
			_, err := ParseOrchestrateReviewArgs("accept", []string{"--issue", "az-review"})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.parse(nil); err != nil {
				t.Fatalf("rootless parse: %v", err)
			}
		})
	}
}

func TestParseOrchestrateReviewArgsEnforcesDecisionShape(t *testing.T) {
	accept, err := ParseOrchestrateReviewArgs("accept", []string{"--root", "az-root", "--issue", "az-2", "--issue", "az-1", "--issue", "az-2", "--intent-key", "accept-1", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if accept.Action != "accept" || accept.RootIssueID != "az-root" || accept.IntentKey != "accept-1" || !accept.JSON || len(accept.IssueIDs) != 2 || accept.IssueIDs[0] != "az-1" {
		t.Fatalf("accept options = %+v", accept)
	}
	returned, err := ParseOrchestrateReviewArgs("return", []string{"--issue", "az-2", "--finding", "fix race", "--finding", "add regression", "--severity", "medium", "--restart-worker"})
	if err != nil {
		t.Fatal(err)
	}
	if returned.Action != "return" || len(returned.Findings) != 2 || returned.Severity != "medium" || !returned.RestartWorker {
		t.Fatalf("return options = %+v", returned)
	}
	for _, tt := range []struct {
		action string
		args   []string
	}{
		{"accept", nil},
		{"accept", []string{"--issue", "az-1", "--finding", "not allowed"}},
		{"return", []string{"--issue", "az-1"}},
		{"return", []string{"--issue", "az-1", "--issue", "az-2", "--finding", "shared ambiguity"}},
		{"unknown", []string{"--issue", "az-1"}},
	} {
		if _, err := ParseOrchestrateReviewArgs(tt.action, tt.args); err == nil {
			t.Fatalf("ParseOrchestrateReviewArgs(%q, %v) succeeded", tt.action, tt.args)
		}
	}
}

func TestOrchestrateReviewAcceptUsesDaemonIntentAndReportsAuthoritativeClose(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	var got protocol.OrchestrationIntentRequest
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: "project",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{passOrchestrationIntent: true, commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandOrchestrationIntent {
				t.Fatalf("command = %q", req.Command)
			}
			if err := json.Unmarshal(req.Body, &got); err != nil {
				t.Fatal(err)
			}
			return responseWithJSON(req, protocol.OrchestrationIntentResult{Scope: got.Scope, Kind: got.Kind, IntentKey: got.IntentKey, Requested: got.IssueIDs, Closed: got.IssueIDs}), nil
		}}).WithProjectID("project"),
	}
	output := captureStdout(t, func() error {
		return OrchestrateReviewCommand(deps, OrchestrateReviewOptions{Action: "accept", IntentKey: "accept-batch-1", IssueIDs: []string{"az-1", "az-2"}})
	})
	if got.Kind != protocol.OrchestrationIntentReviewAccept || got.IntentKey != "accept-batch-1" || got.ActorID == "" || got.RepoDir != "/repo" || len(got.IssueIDs) != 2 {
		t.Fatalf("intent = %+v", got)
	}
	for _, want := range []string{"Review accept intent: accept-batch-1", "accepted and closed: az-1", "accepted and closed: az-2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}

func TestOrchestrateReviewFailurePreservesIntentKeyForRetry(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	deps := &Dependencies{
		RepoDir: "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{passOrchestrationIntent: true, commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			var got protocol.OrchestrationIntentRequest
			if err := json.Unmarshal(req.Body, &got); err != nil {
				t.Fatal(err)
			}
			return responseWithJSON(req, protocol.OrchestrationIntentResult{Scope: got.Scope, Kind: got.Kind, IntentKey: got.IntentKey, Requested: got.IssueIDs, Failed: map[string]string{"az-1": "authoritative close: conflict"}}), nil
		}}),
	}
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateReviewCommand(deps, OrchestrateReviewOptions{Action: "accept", IntentKey: "accept-retry-1", IssueIDs: []string{"az-1"}})
	})
	if err == nil || !strings.Contains(err.Error(), "retry the same decision with --intent-key accept-retry-1") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(output, "failed az-1: authoritative close: conflict") {
		t.Fatalf("output = %s", output)
	}
}

func TestProjectOrchestrationWatchPollIntervalIsBounded(t *testing.T) {
	if got := projectOrchestrationWatchPollInterval(250 * time.Millisecond); got != time.Second {
		t.Fatalf("default project watch interval = %s, want %s", got, time.Second)
	}
	if got := projectOrchestrationWatchPollInterval(3 * time.Second); got != 3*time.Second {
		t.Fatalf("explicit slow project watch interval = %s, want %s", got, 3*time.Second)
	}
}

func TestOrchestratorSessionAttachIsDeclarativeCLICommand(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	t.Setenv("HOME", t.TempDir())
	var gotCommand string
	deps := &Dependencies{ProjectID: "0123456789ab-azedarach", RepoDir: "/work/azedarach", DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		gotCommand = req.Command
		var body protocol.OrchestratorSessionRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Scope.Kind != domain.OrchestrationScopeProject {
			t.Fatalf("scope = %+v", body.Scope)
		}
		return responseWithJSON(req, protocol.OrchestratorSessionResult{Scope: body.Scope, SessionID: "az-orchestrator-project", Disposition: "attached", Lifecycle: domain.OrchestratorWorking, Live: true}), nil
	}})}
	output := captureStdout(t, func() error { return OrchestratorSessionCommand(deps, "attach", OrchestratorSessionOptions{}) })
	if gotCommand != protocol.CommandOrchestratorSessionAttach {
		t.Fatalf("command = %q", gotCommand)
	}
	for _, want := range []string{"Project: azedarach (0123456789ab-azedarach)", "Orchestrator session: az-orchestrator-project", "Disposition: attached", "Live: true"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}

func TestOrchestratorSessionStopUsesTypedDaemonCommand(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	var gotCommand string
	deps := &Dependencies{DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		gotCommand = req.Command
		return responseWithJSON(req, protocol.OrchestratorSessionResult{Scope: domain.ProjectOrchestrationScope(), SessionID: "az-orchestrator-project", Disposition: "stopped-forced", Lifecycle: domain.OrchestratorPaused, Forced: true}), nil
	}})}
	output := captureStdout(t, func() error { return OrchestratorSessionCommand(deps, "stop", OrchestratorSessionOptions{}) })
	if gotCommand != protocol.CommandOrchestratorSessionStop {
		t.Fatalf("command = %q", gotCommand)
	}
	for _, want := range []string{"Disposition: stopped-forced", "State: paused", "Live: false", "Forced cleanup: true"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}

func TestProjectOrchestrationWatchKeyIgnoresGenerationTime(t *testing.T) {
	first := protocol.OrchestrationSnapshot{Scope: domain.ProjectOrchestrationScope(), Revision: 7, GeneratedAt: time.Now()}
	second := first
	second.GeneratedAt = first.GeneratedAt.Add(time.Minute)
	firstKey, err := projectOrchestrationWatchKey(first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := projectOrchestrationWatchKey(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey != secondKey {
		t.Fatalf("generation time changed watch key")
	}
	second.Revision++
	secondKey, err = projectOrchestrationWatchKey(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey == secondKey {
		t.Fatalf("revision change did not change watch key")
	}
}

func TestResolveCLIOrchestrationScopePrecedence(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "env-root")
	scope, err := resolveCLIOrchestrationScope("explicit-root")
	if err != nil || scope.Kind != domain.OrchestrationScopeRooted || scope.RootIssueID != "explicit-root" {
		t.Fatalf("explicit scope = %#v, %v", scope, err)
	}
	scope, err = resolveCLIOrchestrationScope("")
	if err != nil || scope.RootIssueID != "env-root" {
		t.Fatalf("environment scope = %#v, %v", scope, err)
	}
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	scope, err = resolveCLIOrchestrationScope("")
	if err != nil || scope.Kind != domain.OrchestrationScopeProject {
		t.Fatalf("project scope = %#v, %v", scope, err)
	}
}

func TestParseOrchestratorSessionArgsUsesFlagsBeforePositionals(t *testing.T) {
	opts, err := ParseOrchestratorSessionArgs("start", []string{"--root", "az-root", "--project", "project", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.RootIssueID != "az-root" || opts.Project != "project" || !opts.JSON {
		t.Fatalf("options = %#v", opts)
	}
	if _, err := ParseOrchestratorSessionArgs("start", []string{"az-root"}); err == nil {
		t.Fatal("expected positional argument rejection")
	}
}

func TestRootlessAgentHookUsesExplicitOrchestratorSessionID(t *testing.T) {
	t.Setenv("AZEDARACH_SESSION_ID", "azedarach-orchestrator-project")
	t.Setenv("TMUX_PANE", "%7")
	if got := agentHookSessionID("project", ""); got != "azedarach-orchestrator-project" {
		t.Fatalf("session id = %q", got)
	}
}
