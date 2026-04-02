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
		"githooks <install|update|run|notify|hook>",
		"gate <issue-id>",
		"dev gate <issue-id>",
		"opencode <init|plugin>",
		"codex <install|guard|hook>",
		"spec <subcommand>",
		"az notify idle_prompt az-123",
		"az hooks install az-123",
		"az githooks install",
		"az githooks update",
		"az githooks run",
		"az githooks notify",
		"az githooks hook --hook pre-commit",
		"az gate az-123",
		"az dev gate az-123",
		"az opencode init",
		"az opencode plugin install",
		"az codex install",
		"az codex hook run --json pre-tool-use",
		"az spec req list --json",
		"az spec req create --id bfs-req-1 --title \"Restore az spec grammar\" --issue bgh",
		"az spec link add --issue bgh --req bfs-req-1 --role implements",
		"az spec sync --target md --check",
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
	cmd := exec.Command("git", "-C", projectDir, "init")
	cmd.Env = gitExecEnvWithoutRoutingVars()
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	opts, err := ParseGitHooksInstallArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseGitHooksInstallArgs error: %v", err)
	}

	output := captureStdout(t, func() error {
		return GitHooksInstallCommand(&Dependencies{RepoDir: projectDir}, opts)
	})
	if !strings.Contains(output, "Installed/updated git hooks in") {
		t.Fatalf("install output = %q", output)
	}

	preCommitPath := filepath.Join(projectDir, ".githooks", "pre-commit")
	data, err := os.ReadFile(preCommitPath)
	if err != nil {
		t.Fatalf("read pre-commit: %v", err)
	}
	if !strings.Contains(string(data), "az githooks hook --hook pre-commit") {
		t.Fatalf("pre-commit content = %q", string(data))
	}
	for _, hookName := range []string{"post-commit", "post-merge", "post-checkout", "post-rewrite"} {
		hookPath := filepath.Join(projectDir, ".githooks", hookName)
		hookData, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read %s: %v", hookName, err)
		}
		if !strings.Contains(string(hookData), "az githooks hook --hook "+hookName) {
			t.Fatalf("%s content = %q", hookName, string(hookData))
		}
	}
}

func TestGitHooksInstallCommandPreservesCustomHookContent(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".githooks"), 0o755); err != nil {
		t.Fatalf("mkdir .githooks: %v", err)
	}
	cmd := exec.Command("git", "-C", projectDir, "init")
	cmd.Env = gitExecEnvWithoutRoutingVars()
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	preCommitPath := filepath.Join(projectDir, ".githooks", "pre-commit")
	custom := "#!/usr/bin/env sh\nset -eu\necho custom-pre-commit\n"
	if err := os.WriteFile(preCommitPath, []byte(custom), 0o755); err != nil {
		t.Fatalf("seed pre-commit: %v", err)
	}

	opts, err := ParseGitHooksInstallArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseGitHooksInstallArgs error: %v", err)
	}
	if err := GitHooksInstallCommand(&Dependencies{RepoDir: projectDir}, opts); err != nil {
		t.Fatalf("GitHooksInstallCommand error: %v", err)
	}
	if err := GitHooksInstallCommand(&Dependencies{RepoDir: projectDir}, opts); err != nil {
		t.Fatalf("GitHooksInstallCommand second run error: %v", err)
	}

	data, err := os.ReadFile(preCommitPath)
	if err != nil {
		t.Fatalf("read pre-commit: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "echo custom-pre-commit") {
		t.Fatalf("pre-commit lost custom content: %q", content)
	}
	if got := strings.Count(content, "az githooks hook --hook pre-commit \"$@\""); got != 1 {
		t.Fatalf("managed command count = %d, want 1; content = %q", got, content)
	}
	if got := strings.Count(content, gitHookManagedBlockStart); got != 1 {
		t.Fatalf("managed block start count = %d, want 1; content = %q", got, content)
	}
}

func TestGitHooksInstallCommandInjectsManagedBlockBeforeEarlyReturn(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".githooks"), 0o755); err != nil {
		t.Fatalf("mkdir .githooks: %v", err)
	}
	cmd := exec.Command("git", "-C", projectDir, "init")
	cmd.Env = gitExecEnvWithoutRoutingVars()
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	preCommitPath := filepath.Join(projectDir, ".githooks", "pre-commit")
	custom := "#!/usr/bin/env sh\nset -eu\nexit 0\necho unreachable\n"
	if err := os.WriteFile(preCommitPath, []byte(custom), 0o755); err != nil {
		t.Fatalf("seed pre-commit: %v", err)
	}

	opts, err := ParseGitHooksInstallArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseGitHooksInstallArgs error: %v", err)
	}
	if err := GitHooksInstallCommand(&Dependencies{RepoDir: projectDir}, opts); err != nil {
		t.Fatalf("GitHooksInstallCommand error: %v", err)
	}

	data, err := os.ReadFile(preCommitPath)
	if err != nil {
		t.Fatalf("read pre-commit: %v", err)
	}
	content := string(data)
	managedIndex := strings.Index(content, "az githooks hook --hook pre-commit \"$@\"")
	exitIndex := strings.Index(content, "exit 0")
	if managedIndex < 0 || exitIndex < 0 {
		t.Fatalf("expected managed command and exit line in content: %q", content)
	}
	if managedIndex > exitIndex {
		t.Fatalf("managed command appears after early return: %q", content)
	}
}

func TestGitHooksNotifyCommandRefreshesDaemonGitStatus(t *testing.T) {
	projectDir := t.TempDir()
	initCmd := exec.Command("git", "-C", projectDir, "init")
	initCmd.Env = gitExecEnvWithoutRoutingVars()
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	opts, err := ParseGitHooksNotifyArgs([]string{"--project-dir", projectDir, "--hook", "post-commit"})
	if err != nil {
		t.Fatalf("ParseGitHooksNotifyArgs error: %v", err)
	}

	var gotReq protocol.RequestEnvelope
	transport := &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			gotReq = req
			return responseWithJSON(req, map[string]any{"status": map[string]any{}}), nil
		},
	}
	deps := &Dependencies{
		DaemonClient: daemonclient.New(transport).WithProjectID("proj-1"),
		ProjectID:    "proj-1",
		RepoDir:      projectDir,
	}

	if err := GitHooksNotifyCommand(deps, opts); err != nil {
		t.Fatalf("GitHooksNotifyCommand error: %v", err)
	}
	if gotReq.Command != daemonclient.CommandGitStatus {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandGitStatus)
	}
	var body daemonclient.GitCommandRequest
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	gotWorktree, err := filepath.EvalSymlinks(body.Worktree)
	if err != nil {
		gotWorktree = body.Worktree
	}
	wantWorktree, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		wantWorktree = projectDir
	}
	if gotWorktree != wantWorktree {
		t.Fatalf("worktree = %q (canon %q), want %q (canon %q)", body.Worktree, gotWorktree, projectDir, wantWorktree)
	}
}

func TestGitHooksRunCommandExecutesConfiguredPreCommitCommands(t *testing.T) {
	projectDir := t.TempDir()
	cmd := exec.Command("git", "-C", projectDir, "init")
	cmd.Env = gitExecEnvWithoutRoutingVars()
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.GitHooks.Commands["pre-commit"] = []string{"mkdir -p docs/spec && printf 'ok\\n' > docs/spec/.spec-sync-ran"}

	opts, err := ParseGitHooksRunArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseGitHooksRunArgs error: %v", err)
	}

	if err := GitHooksRunCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, opts); err != nil {
		t.Fatalf("GitHooksRunCommand error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "docs", "spec", ".spec-sync-ran")); err != nil {
		t.Fatalf("expected pre-commit command marker: %v", err)
	}
}

func TestGitHooksRunCommandBestEffortContinuesOnFailures(t *testing.T) {
	projectDir := t.TempDir()
	initCmd := exec.Command("git", "-C", projectDir, "init")
	initCmd.Env = gitExecEnvWithoutRoutingVars()
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.GitHooks.Commands["pre-commit"] = []string{"exit 7", "mkdir -p docs/spec && printf 'ok\\n' > docs/spec/.best-effort-ran"}

	opts, err := ParseGitHooksRunArgs([]string{"--project-dir", projectDir})
	if err != nil {
		t.Fatalf("ParseGitHooksRunArgs error: %v", err)
	}

	if err := GitHooksRunCommand(&Dependencies{RepoDir: projectDir, Config: cfg}, opts); err != nil {
		t.Fatalf("expected best-effort success, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "docs", "spec", ".best-effort-ran")); err != nil {
		t.Fatalf("expected post-failure command marker: %v", err)
	}
}

func runGitCommandIsolated(repoDir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") ||
			strings.HasPrefix(entry, "GIT_WORK_TREE=") ||
			strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	cmd.Env = filtered
	return cmd.Run()
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

func TestNotifyCommandUpdatesDaemonSessionStateForIdlePrompt(t *testing.T) {
	opts, err := ParseNotifyArgs([]string{"--json", "idle_prompt", "az-123"})
	if err != nil {
		t.Fatalf("ParseNotifyArgs error: %v", err)
	}

	var gotReq protocol.RequestEnvelope
	transport := &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			gotReq = req
			return responseWithOutput(req, "ok"), nil
		},
	}

	deps := &Dependencies{
		DaemonClient: daemonclient.New(transport).WithProjectID("proj-1"),
		ProjectID:    "proj-1",
	}
	output := captureStdout(t, func() error {
		return NotifyCommand(deps, opts)
	})

	if gotReq.Command != daemonclient.CommandSessionPause {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandSessionPause)
	}
	if strings.TrimSpace(output) != "{}" {
		t.Fatalf("notify json output = %q, want {}", output)
	}
}

func TestNotifyCommandResolvesIssueIDFromEnvForSessionStart(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-from-env")
	opts, err := ParseNotifyArgs([]string{"--json", "session_start"})
	if err != nil {
		t.Fatalf("ParseNotifyArgs error: %v", err)
	}

	var gotReq protocol.RequestEnvelope
	transport := &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			gotReq = req
			return responseWithOutput(req, "ok"), nil
		},
	}

	deps := &Dependencies{
		DaemonClient: daemonclient.New(transport).WithProjectID("proj-1"),
		ProjectID:    "proj-1",
	}
	if err := NotifyCommand(deps, opts); err != nil {
		t.Fatalf("NotifyCommand error: %v", err)
	}

	if gotReq.Command != daemonclient.CommandSessionResume {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandSessionResume)
	}

	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body.SessionID != "az-from-env" {
		t.Fatalf("session_id = %q, want az-from-env", body.SessionID)
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
		"az codex hook run --json session-start",
		"az codex hook run --json stop",
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
	if strings.Contains(content, "az notify --json post_tool_use") {
		t.Fatalf("legacy notify hook should be removed: %s", content)
	}
	if strings.Contains(content, "az codex guard --json post-tool-use") {
		t.Fatalf("legacy guard hook should be removed: %s", content)
	}
	if strings.Contains(content, "az codex hook run --json post-tool-use") {
		t.Fatalf("post-tool-use hook should not be installed: %s", content)
	}
	if strings.Contains(content, "az codex hook run --json user-prompt-submit") {
		t.Fatalf("user-prompt-submit hook should not be installed: %s", content)
	}
	if strings.Contains(content, "az codex hook run --json pre-tool-use") {
		t.Fatalf("pre-tool-use hook should not be installed: %s", content)
	}
}

func TestCodexGuardSessionStartBlocksWhenPromptMissingPrime(t *testing.T) {
	projectDir := t.TempDir()
	deps := &Dependencies{RepoDir: projectDir}

	original := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(`{"thread_id":"t-1"}`); err != nil {
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
	if !strings.Contains(output, `"decision":"block"`) {
		t.Fatalf("session-start output = %q, want block decision", output)
	}
	if !strings.Contains(output, "Run `az prime` now before any other shell commands.") {
		t.Fatalf("session-start output = %q, want az-prime reason", output)
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

func TestCodexHookRunCommandJSONMatchesGuardContract(t *testing.T) {
	projectDir := t.TempDir()
	deps := &Dependencies{RepoDir: projectDir}

	original := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(`{"thread_id":"t-3","tool_input":{"command":"pwd"}}`); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	defer func() {
		os.Stdin = original
		_ = r.Close()
	}()

	output := captureStdout(t, func() error {
		return CodexHookRunCommand(deps, CodexHookRunOptions{Event: "session-start", JSON: true})
	})
	if !strings.Contains(output, `"decision":"block"`) {
		t.Fatalf("hook run output = %q, want block decision", output)
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
