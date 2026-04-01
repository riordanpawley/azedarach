package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

func TestPrintUsageIncludesNewCommandFamilies(t *testing.T) {
	output := captureStdout(t, func() error {
		PrintUsage()
		return nil
	})

	for _, want := range []string{
		"notify <event> <issue-id>",
		"hooks install <issue-id>",
		"githooks <install|run>",
		"gate <issue-id>",
		"dev gate <issue-id>",
		"opencode <init|plugin>",
		"codex <install|init|guard>",
		"az notify idle_prompt az-123",
		"az notify --json post_tool_use",
		"az hooks install az-123",
		"az githooks install",
		"az githooks run",
		"az gate az-123",
		"az dev gate az-123",
		"az opencode init",
		"az opencode plugin install",
		"az codex install",
		"az codex guard --json pre-tool-use",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage missing %q: %q", want, output)
		}
	}
}

func TestGitHooksInstallCommandWritesPreCommitHook(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".githooks"), 0o755); err != nil {
		t.Fatalf("mkdir .githooks: %v", err)
	}
	if err := exec.Command("git", "-C", projectDir, "init").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	opts, err := ParseGitHooksInstallArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseGitHooksInstallArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return GitHooksInstallCommand(&Dependencies{RepoDir: projectDir}, opts)
	})
	if !strings.Contains(output, "Installed git hooks in") {
		t.Fatalf("install output = %q", output)
	}

	preCommitPath := filepath.Join(projectDir, ".githooks", "pre-commit")
	data, err := os.ReadFile(preCommitPath)
	if err != nil {
		t.Fatalf("read pre-commit: %v", err)
	}
	if !strings.Contains(string(data), "az githooks run") {
		t.Fatalf("pre-commit content = %q", string(data))
	}
}

func TestGitHooksRunCommandExecutesConfiguredSpecSync(t *testing.T) {
	projectDir := t.TempDir()
	if err := exec.Command("git", "-C", projectDir, "init").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.GitHooks.SpecSync.Enabled = true
	cfg.GitHooks.SpecSync.Command = "mkdir -p docs/spec && printf 'ok\\n' > docs/spec/.spec-sync-ran"
	cfg.GitHooks.SpecSync.AutoStageDocs = false
	cfg.GitHooks.BoundaryCheck.Enabled = false

	opts, err := ParseGitHooksRunArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseGitHooksRunArgs error: %v", err)
	}

	if err := GitHooksRunCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, opts); err != nil {
		t.Fatalf("GitHooksRunCommand error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "docs", "spec", ".spec-sync-ran")); err != nil {
		t.Fatalf("expected spec sync marker: %v", err)
	}
}

func TestNotifyCommandParsesAndPrintsStatus(t *testing.T) {
	opts, err := ParseNotifyArgs([]string{"--verbose", "idle_prompt", "az-123"})
	if err != nil {
		t.Fatalf("ParseNotifyArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return NotifyCommand(&Dependencies{}, opts)
	})

	if !strings.Contains(output, "Hook notification: idle_prompt for az-123 -> waiting") {
		t.Fatalf("notify output = %q", output)
	}
}

func TestNotifyCommandJSONOutput(t *testing.T) {
	opts, err := ParseNotifyArgs([]string{"--json", "post_tool_use"})
	if err != nil {
		t.Fatalf("ParseNotifyArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return NotifyCommand(&Dependencies{}, opts)
	})

	if strings.TrimSpace(output) != "{}" {
		t.Fatalf("notify json output = %q, want {}", output)
	}
}

func TestParseNotifyArgsRejectsFlagsAfterPositionals(t *testing.T) {
	_, err := ParseNotifyArgs([]string{"post_tool_use", "--json"})
	if err == nil {
		t.Fatal("expected parse error for flags after positional arguments")
	}
	if !strings.Contains(err.Error(), "flags must come before positional arguments") {
		t.Fatalf("unexpected parse error: %v", err)
	}
}

func TestCodexInstallCommandWritesHooksConfig(t *testing.T) {
	projectDir := t.TempDir()
	opts, err := ParseCodexInstallArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseCodexInstallArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return CodexInstallCommand(&Dependencies{RepoDir: projectDir}, opts)
	})
	if !strings.Contains(output, "Installed Codex hooks in") {
		t.Fatalf("codex install output = %q", output)
	}

	hooksPath := filepath.Join(projectDir, ".codex", "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"az notify --json session_start",
		"az notify --json user_prompt_submit",
		"az notify --json pre_tool_use",
		"az notify --json post_tool_use",
		"az notify --json stop",
		"az codex guard --json session-start",
		"az codex guard --json user-prompt-submit",
		"az codex guard --json pre-tool-use",
		"az codex guard --json post-tool-use",
		"az codex guard --json stop",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("hooks config missing %q: %s", want, content)
		}
	}
}

func TestCodexInstallCommandMergesExistingHooks(t *testing.T) {
	projectDir := t.TempDir()
	hooksPath := filepath.Join(projectDir, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	initial := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"echo keep-me"}]}],"CustomEvent":[{"hooks":[{"type":"command","command":"echo custom"}]}]}}`
	if err := os.WriteFile(hooksPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("seed hooks config: %v", err)
	}

	opts, err := ParseCodexInstallArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseCodexInstallArgs error: %v", err)
	}
	if err := CodexInstallCommand(&Dependencies{RepoDir: projectDir}, opts); err != nil {
		t.Fatalf("CodexInstallCommand error: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "echo keep-me") {
		t.Fatalf("existing post-tool hook removed: %s", content)
	}
	if !strings.Contains(content, "echo custom") {
		t.Fatalf("existing custom event hook removed: %s", content)
	}
	if !strings.Contains(content, "az notify --json post_tool_use") {
		t.Fatalf("codex post-tool hook missing: %s", content)
	}
	if !strings.Contains(content, "az codex guard --json post-tool-use") {
		t.Fatalf("codex post-tool guard hook missing: %s", content)
	}
}

func TestCodexGuardRequiresPrimeBeforeOtherCommands(t *testing.T) {
	projectDir := t.TempDir()
	deps := &Dependencies{RepoDir: projectDir}

	writePayloadToStdin := func(t *testing.T, payload string) func() {
		t.Helper()
		original := os.Stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		if _, err := w.WriteString(payload); err != nil {
			t.Fatalf("write payload: %v", err)
		}
		_ = w.Close()
		os.Stdin = r
		return func() {
			os.Stdin = original
			_ = r.Close()
		}
	}

	restore := writePayloadToStdin(t, `{"thread_id":"t-1"}`)
	if err := CodexGuardCommand(deps, CodexGuardOptions{Event: "session-start", JSON: true}); err != nil {
		restore()
		t.Fatalf("session-start guard error: %v", err)
	}
	restore()

	restore = writePayloadToStdin(t, `{"thread_id":"t-1","tool_input":{"command":"pwd"}}`)
	output := captureStdout(t, func() error {
		return CodexGuardCommand(deps, CodexGuardOptions{Event: "pre-tool-use", JSON: true})
	})
	restore()
	if !strings.Contains(output, `"decision":"block"`) {
		t.Fatalf("pre-tool-use output = %q, want block decision", output)
	}

	restore = writePayloadToStdin(t, `{"thread_id":"t-1","tool_input":{"command":"az prime"}}`)
	if err := CodexGuardCommand(deps, CodexGuardOptions{Event: "post-tool-use", JSON: true}); err != nil {
		restore()
		t.Fatalf("post-tool-use guard error: %v", err)
	}
	restore()

	restore = writePayloadToStdin(t, `{"thread_id":"t-1","tool_input":{"command":"pwd"}}`)
	output = captureStdout(t, func() error {
		return CodexGuardCommand(deps, CodexGuardOptions{Event: "pre-tool-use", JSON: true})
	})
	restore()
	if strings.Contains(output, `"decision":"block"`) {
		t.Fatalf("pre-tool-use output = %q, want allow after az prime", output)
	}
}

func TestCodexGuardSessionStartSkipsReminderWhenPromptMentionsPrime(t *testing.T) {
	projectDir := t.TempDir()
	deps := &Dependencies{RepoDir: projectDir}

	original := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(`{"thread_id":"t-2","prompt":"run az prime first"}`); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	defer func() {
		os.Stdin = original
		_ = r.Close()
	}()

	output := captureStdout(t, func() error {
		return CodexGuardCommand(deps, CodexGuardOptions{Event: "session-start", JSON: true})
	})
	if strings.TrimSpace(output) != "{}" {
		t.Fatalf("session-start output = %q, want {}", output)
	}
}

func TestHooksInstallCommandMergesAndWritesSettings(t *testing.T) {
	projectDir := t.TempDir()
	settingsPath := filepath.Join(projectDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"permissions":{"shell":["git status"]},"hooks":{"Notification":[{"matcher":"idle_prompt","hooks":[{"type":"command","command":"az notify idle_prompt existing"}]}]}}`), 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	opts, err := ParseHooksInstallArgs([]string{"--project-dir", projectDir, "az-123"})
	if err != nil {
		t.Fatalf("ParseHooksInstallArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return HooksInstallCommand(&Dependencies{RepoDir: projectDir}, opts)
	})

	if !strings.Contains(output, "Installed hooks for issue az-123") {
		t.Fatalf("hooks output = %q", output)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks section missing: %#v", settings["hooks"])
	}

	notification := hooks["Notification"].([]any)
	if len(notification) != 2 {
		t.Fatalf("notification hook count = %d, want 2", len(notification))
	}
	lastNotification := notification[1].(map[string]any)
	lastHooks := lastNotification["hooks"].([]any)
	lastHook := lastHooks[0].(map[string]any)
	if got := lastHook["command"]; got != "az notify idle_prompt az-123" {
		t.Fatalf("notification hook command = %v, want az notify idle_prompt az-123", got)
	}
	if !strings.Contains(string(data), "az notify idle_prompt az-123") {
		t.Fatalf("settings missing idle_prompt hook command: %s", string(data))
	}
	if !strings.Contains(string(data), "az notify stop az-123") {
		t.Fatalf("settings missing stop hook command: %s", string(data))
	}
}

func TestGateCommandParsesAndPrintsStub(t *testing.T) {
	opts, err := ParseGateArgs([]string{"--fix", "--verbose", "--project-dir", "/tmp/project", "az-123"})
	if err != nil {
		t.Fatalf("ParseGateArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return GateCommand(&Dependencies{}, opts)
	})

	for _, want := range []string{
		"Running quality gates for: az-123",
		"Project dir: /tmp/project",
		"Fix mode requested",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("gate output missing %q: %q", want, output)
		}
	}
}

func TestPrintDevUsageIncludesNewCommandFamilies(t *testing.T) {
	output := captureStdout(t, func() error {
		PrintDevUsage()
		return nil
	})

	for _, want := range []string{
		"Usage: az dev <gate|start|stop|restart|status|list>",
		"az dev gate <issue-id>",
		"az dev start <issue-id>",
		"az dev stop <issue-id>",
		"az dev restart <issue-id>",
		"az dev status <issue-id>",
		"az dev list",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage missing %q: %q", want, output)
		}
	}
}

func TestParseDevIssueArg(t *testing.T) {
	got, err := ParseDevIssueArg("start", []string{"az-123"})
	if err != nil {
		t.Fatalf("ParseDevIssueArg error: %v", err)
	}
	if got != "az-123" {
		t.Fatalf("issue id = %q, want az-123", got)
	}

	for _, args := range [][]string{{}, {"--help"}, {"az-123", "extra"}} {
		if _, err := ParseDevIssueArg("start", args); err == nil || !strings.Contains(err.Error(), "usage: az dev start <issue-id>") {
			t.Fatalf("ParseDevIssueArg(%v) error = %v, want usage error", args, err)
		}
	}
}

func TestDevServerLifecycleCommandsUseDaemonClient(t *testing.T) {
	tests := []struct {
		name        string
		command     func(*Dependencies, string) error
		issueID     string
		wantCommand string
		server      devserver.Server
		wantOutput  []string
	}{
		{
			name:        "start",
			command:     DevServerStartCommand,
			issueID:     "az-123",
			wantCommand: daemonclient.CommandDevServerStart,
			server: devserver.Server{
				ID:      "az-123",
				Name:    "default",
				Port:    3010,
				Status:  "running",
				IssueID: "az-123",
				Uptime:  65 * 2,
			},
			wantOutput: []string{"Started dev server 'default' for az-123", "Port: 3010"},
		},
		{
			name:        "stop",
			command:     DevServerStopCommand,
			issueID:     "az-123",
			wantCommand: daemonclient.CommandDevServerStop,
			server: devserver.Server{
				ID:      "az-123",
				Name:    "default",
				Port:    0,
				Status:  "stopped",
				IssueID: "az-123",
			},
			wantOutput: []string{"Stopped dev server 'default' for az-123"},
		},
		{
			name:        "restart",
			command:     DevServerRestartCommand,
			issueID:     "az-123",
			wantCommand: "",
			server: devserver.Server{
				ID:      "az-123",
				Name:    "default",
				Port:    3020,
				Status:  "running",
				IssueID: "az-123",
			},
			wantOutput: []string{"Restarted dev server 'default' for az-123", "Port: 3020"},
		},
		{
			name:        "status",
			command:     DevServerStatusCommand,
			issueID:     "az-123",
			wantCommand: daemonclient.CommandDevServerStatus,
			server: devserver.Server{
				ID:      "az-123",
				Name:    "default",
				Port:    3010,
				Status:  "running",
				IssueID: "az-123",
				Uptime:  125 * time.Second,
			},
			wantOutput: []string{"Dev server 'default' for az-123", "Status: running", "Port: 3010", "Uptime: 2m 5s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &fakeDaemonTransport{}
			var gotReqs []protocol.RequestEnvelope
			transport.commandFn = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReqs = append(gotReqs, req)
				switch req.Command {
				case daemonclient.CommandDevServerStart, daemonclient.CommandDevServerStop, daemonclient.CommandDevServerStatus:
					return responseWithJSON(req, struct {
						IssueID string           `json:"issue_id"`
						Server  devserver.Server `json:"server"`
					}{
						IssueID: tt.issueID,
						Server:  tt.server,
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			}

			deps := &Dependencies{
				DaemonClient: daemonclient.New(transport).WithProjectID("proj-1"),
				ProjectID:    "proj-1",
			}

			output := captureStdout(t, func() error {
				return tt.command(deps, tt.issueID)
			})

			if tt.name == "restart" {
				if len(gotReqs) != 2 {
					t.Fatalf("restart command count = %d, want 2", len(gotReqs))
				}
				if gotReqs[0].Command != daemonclient.CommandDevServerStop {
					t.Fatalf("restart first command = %q, want %q", gotReqs[0].Command, daemonclient.CommandDevServerStop)
				}
				if gotReqs[1].Command != daemonclient.CommandDevServerStart {
					t.Fatalf("restart second command = %q, want %q", gotReqs[1].Command, daemonclient.CommandDevServerStart)
				}
			} else {
				if len(gotReqs) != 1 {
					t.Fatalf("command count = %d, want 1", len(gotReqs))
				}
				if tt.wantCommand != "" && gotReqs[0].Command != tt.wantCommand {
					t.Fatalf("command = %q, want %q", gotReqs[0].Command, tt.wantCommand)
				}
			}
			for _, want := range tt.wantOutput {
				if !strings.Contains(output, want) {
					t.Fatalf("output = %q, want substring %q", output, want)
				}
			}
		})
	}
}

func TestDevServerListCommandFiltersRunningServers(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	transport := &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			gotReq = req
			return responseWithJSON(req, devServerListResponseBody{
				ProjectID: "proj-1",
				Servers: []devServerView{
					{
						Name:    "default",
						Port:    3010,
						Status:  "running",
						IssueID: "az-1",
					},
					{
						Name:    "secondary",
						Port:    3011,
						Status:  "stopped",
						IssueID: "az-2",
					},
				},
			}), nil
		},
	}

	deps := &Dependencies{
		DaemonClient: daemonclient.New(transport).WithProjectID("proj-1"),
		ProjectID:    "proj-1",
	}

	output := captureStdout(t, func() error {
		return DevServerListCommand(deps)
	})

	if gotReq.Command != commandDevServerList {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandDevServerList)
	}
	if gotReq.Meta.ProjectID != "proj-1" {
		t.Fatalf("project_id = %q, want proj-1", gotReq.Meta.ProjectID)
	}
	if !strings.Contains(output, "Running dev servers:") {
		t.Fatalf("output = %q, want running servers header", output)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "default") {
		t.Fatalf("output = %q, want running server entry", output)
	}
	if strings.Contains(output, "az-2") {
		t.Fatalf("output = %q, want stopped server filtered out", output)
	}
}

func TestOpenCodeInitCommandCreatesConfig(t *testing.T) {
	projectDir := t.TempDir()
	opts, err := ParseOpenCodeInitArgs([]string{"--project-dir", projectDir, "--verbose"})
	if err != nil {
		t.Fatalf("ParseOpenCodeInitArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return OpenCodeInitCommand(&Dependencies{RepoDir: projectDir}, opts)
	})

	if !strings.Contains(output, "Initialized OpenCode support in") {
		t.Fatalf("init output = %q", output)
	}

	data, err := os.ReadFile(filepath.Join(projectDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	for _, want := range []string{
		`"$schema": "https://opencode.ai/config.json"`,
		`"opencode-tracker"`,
		`"theme": "tokyonight"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("opencode config missing %q: %s", want, string(data))
		}
	}
}

func TestOpenCodePluginInstallCommandCreatesPlaceholderFiles(t *testing.T) {
	projectDir := t.TempDir()
	globalDir := filepath.Join(t.TempDir(), "plugins")
	opts, err := ParseOpenCodePluginInstallArgs([]string{
		"--global-dir", globalDir,
		"--project-dir", projectDir,
		"--verbose",
	})
	if err != nil {
		t.Fatalf("ParseOpenCodePluginInstallArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return OpenCodePluginInstallCommand(&Dependencies{RepoDir: projectDir}, opts)
	})

	if !strings.Contains(output, "Installed global plugin:") {
		t.Fatalf("plugin install output = %q", output)
	}

	for _, path := range []string{
		filepath.Join(globalDir, openCodePluginFilename),
		filepath.Join(projectDir, ".opencode", "plugins", openCodePluginFilename),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected plugin file %s: %v", path, err)
		}
	}
}
