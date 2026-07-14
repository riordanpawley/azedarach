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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.parse(nil); err != nil {
				t.Fatalf("rootless parse: %v", err)
			}
		})
	}
}

func TestOrchestratorSessionAttachIsDeclarativeCLICommand(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	var gotCommand string
	deps := &Dependencies{DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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
	for _, want := range []string{"Orchestrator session: az-orchestrator-project", "Disposition: attached", "Live: true"} {
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
