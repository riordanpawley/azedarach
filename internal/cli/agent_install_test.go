package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAIInstallArgsDefaultsToAutoAndAutoGenerate(t *testing.T) {
	opts, err := ParseAIInstallArgs([]string{})
	if err != nil {
		t.Fatalf("ParseAIInstallArgs: %v", err)
	}
	if len(opts.Targets) != 1 || opts.Targets[0] != AgentInstallTargetAuto {
		t.Fatalf("targets = %v, want [auto]", opts.Targets)
	}
	if opts.Generate != "auto" {
		t.Fatalf("generate = %q, want auto", opts.Generate)
	}
}

func TestParseAIInstallArgsAcceptsCSVTargets(t *testing.T) {
	opts, err := ParseAIInstallArgs([]string{"--target=claude,codex,rulesync"})
	if err != nil {
		t.Fatalf("ParseAIInstallArgs: %v", err)
	}
	got := make([]string, len(opts.Targets))
	for i, t := range opts.Targets {
		got[i] = string(t)
	}
	wantSet := map[string]bool{"claude": true, "codex": true, "rulesync": true}
	if len(got) != 3 {
		t.Fatalf("targets = %v, want 3 entries", got)
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Fatalf("unexpected target %q in %v", name, got)
		}
	}
}

func TestParseAIInstallArgsRejectsUnknownTarget(t *testing.T) {
	_, err := ParseAIInstallArgs([]string{"--target=banana"})
	if err == nil || !strings.Contains(err.Error(), "unsupported install target") {
		t.Fatalf("unsupported target err = %v", err)
	}
}

func TestParseAIInstallArgsRejectsUnknownGenerate(t *testing.T) {
	_, err := ParseAIInstallArgs([]string{"--generate=sometimes"})
	if err == nil || !strings.Contains(err.Error(), "unsupported --generate") {
		t.Fatalf("unsupported generate err = %v", err)
	}
}

func TestParseAIStatusArgsDefaultsToAuto(t *testing.T) {
	opts, err := ParseAIStatusArgs([]string{})
	if err != nil {
		t.Fatalf("ParseAIStatusArgs: %v", err)
	}
	if len(opts.Targets) != 1 || opts.Targets[0] != AgentInstallTargetAuto {
		t.Fatalf("targets = %v, want [auto]", opts.Targets)
	}
}

func TestParseAIStatusArgsAcceptsCSVTargetsAndJSON(t *testing.T) {
	opts, err := ParseAIStatusArgs([]string{"--target=codex,claude", "--json"})
	if err != nil {
		t.Fatalf("ParseAIStatusArgs: %v", err)
	}
	if !opts.JSON {
		t.Fatalf("JSON = false, want true")
	}
	if len(opts.Targets) != 2 || !containsTarget(opts.Targets, AgentInstallTargetCodex) || !containsTarget(opts.Targets, AgentInstallTargetClaude) {
		t.Fatalf("targets = %v, want codex+claude", opts.Targets)
	}
}

func TestAIStatusReportsInstalledAndMissingHooks(t *testing.T) {
	dir := t.TempDir()
	codexHooksPath := filepath.Join(dir, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(codexHooksPath), 0o755); err != nil {
		t.Fatalf("mkdir codex hooks dir: %v", err)
	}
	if err := os.WriteFile(codexHooksPath, []byte(`{"hooks":{"Stop":[{"command":"az ai hook run --agent=codex --json stop"}]}}`), 0o644); err != nil {
		t.Fatalf("write codex hooks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir claude marker: %v", err)
	}

	result, err := AIStatus(&Dependencies{RepoDir: dir}, AIStatusOptions{
		Targets:    []AgentInstallTarget{AgentInstallTargetCodex, AgentInstallTargetClaude},
		ProjectDir: dir,
	})
	if err != nil {
		t.Fatalf("AIStatus: %v", err)
	}
	byTarget := make(map[AgentInstallTarget]AIStatusTargetResult)
	for _, target := range result.Targets {
		byTarget[target.Target] = target
	}
	if !byTarget[AgentInstallTargetCodex].Detected || !byTarget[AgentInstallTargetCodex].Installed {
		t.Fatalf("codex status = %+v, want detected installed", byTarget[AgentInstallTargetCodex])
	}
	if !byTarget[AgentInstallTargetClaude].Detected || byTarget[AgentInstallTargetClaude].Installed {
		t.Fatalf("claude status = %+v, want detected missing", byTarget[AgentInstallTargetClaude])
	}
	if !strings.Contains(byTarget[AgentInstallTargetClaude].Reason, "managed Azedarach hook command not found") {
		t.Fatalf("claude reason = %q", byTarget[AgentInstallTargetClaude].Reason)
	}
}

func TestAIStatusCommandPrintsJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir codex marker: %v", err)
	}
	output := captureStdout(t, func() error {
		return AIStatusCommand(&Dependencies{RepoDir: dir}, AIStatusOptions{
			Targets:    []AgentInstallTarget{AgentInstallTargetCodex},
			ProjectDir: dir,
			JSON:       true,
		})
	})
	var result AIStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode status JSON: %v\n%s", err, output)
	}
	if len(result.Targets) != 1 || result.Targets[0].Target != AgentInstallTargetCodex {
		t.Fatalf("targets = %+v, want codex", result.Targets)
	}
	if result.Targets[0].Installed {
		t.Fatalf("codex status = %+v, want missing hook", result.Targets[0])
	}
}

func TestResolveInstallTargetsAutoDetectsRulesync(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".rulesync"), 0o755); err != nil {
		t.Fatalf("mkdir .rulesync: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	resolved, _ := resolveInstallTargets(dir, []AgentInstallTarget{AgentInstallTargetAuto})
	if len(resolved) != 1 || resolved[0] != AgentInstallTargetRulesync {
		t.Fatalf("rulesync presence should win auto detection, got %v", resolved)
	}
}

func TestResolveInstallTargetsAutoFallsBackToDetectedAgents(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	resolved, _ := resolveInstallTargets(dir, []AgentInstallTarget{AgentInstallTargetAuto})
	if containsTarget(resolved, AgentInstallTargetRulesync) {
		t.Fatalf("auto without .rulesync/ should not pick rulesync, got %v", resolved)
	}
	if !containsTarget(resolved, AgentInstallTargetClaude) || !containsTarget(resolved, AgentInstallTargetCodex) {
		t.Fatalf("expected claude+codex detected, got %v", resolved)
	}
}

func TestResolveInstallTargetsExplicitPasses(t *testing.T) {
	resolved, _ := resolveInstallTargets(t.TempDir(), []AgentInstallTarget{AgentInstallTargetClaude})
	if len(resolved) != 1 || resolved[0] != AgentInstallTargetClaude {
		t.Fatalf("explicit target should pass through, got %v", resolved)
	}
}

func TestRulesyncInstallerWritesCanonicalConfig(t *testing.T) {
	dir := t.TempDir()
	deps := &Dependencies{RepoDir: dir}

	if err := (rulesyncInstaller{}).Install(context.Background(), deps, AIInstallOptions{
		ProjectDir: dir,
		IssueID:    "az-1",
		Verbose:    false,
	}); err != nil {
		t.Fatalf("rulesync install: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".rulesync", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	if v, _ := parsed["version"].(float64); v != 1 {
		t.Fatalf("version = %v, want 1", parsed["version"])
	}

	claude, _ := parsed["claudecode"].(map[string]any)
	if claude == nil {
		t.Fatalf("missing claudecode section: %s", data)
	}
	claudeHooks, _ := claude["hooks"].(map[string]any)
	for _, key := range []string{"sessionStart", "preToolUse", "postToolUse", "stop", "notification", "permissionRequest"} {
		if _, ok := claudeHooks[key]; !ok {
			t.Fatalf("claudecode.hooks.%s missing: %s", key, data)
		}
	}

	codex, _ := parsed["codexcli"].(map[string]any)
	if codex == nil {
		t.Fatalf("missing codexcli section: %s", data)
	}
	codexHooks, _ := codex["hooks"].(map[string]any)
	for _, key := range []string{"sessionStart", "preToolUse", "postToolUse", "permissionRequest", "stop"} {
		if _, ok := codexHooks[key]; !ok {
			t.Fatalf("codexcli.hooks.%s missing: %s", key, data)
		}
	}

	// Issue id should be baked into the claude commands (per-issue scope).
	if !strings.Contains(string(data), "--issue=az-1") {
		t.Fatalf("claude commands should include --issue=az-1: %s", data)
	}
}

func TestRulesyncInstallerMergesNonDestructivelyAndDeDupes(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".rulesync", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initial := `{
  "version": 1,
  "hooks": {
    "sessionStart": [{"type": "command", "command": "echo user-managed"}]
  },
  "claudecode": {
    "hooks": {
      "sessionStart": [
        {"type": "command", "command": "echo user-claude-pre"},
        {"type": "command", "command": "az ai hook run --agent=claude --json session_start --issue=az-old"}
      ]
    }
  }
}`
	if err := os.WriteFile(hooksPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	deps := &Dependencies{RepoDir: dir}
	if err := (rulesyncInstaller{}).Install(context.Background(), deps, AIInstallOptions{
		ProjectDir: dir,
		IssueID:    "az-new",
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)

	// User-managed top-level hook preserved.
	if !strings.Contains(content, "echo user-managed") {
		t.Fatalf("user-managed shared hook removed: %s", content)
	}
	// User claudecode hook preserved.
	if !strings.Contains(content, "echo user-claude-pre") {
		t.Fatalf("user-managed claudecode hook removed: %s", content)
	}
	// Stale az-managed command (old issue) replaced.
	if strings.Contains(content, "--issue=az-old") {
		t.Fatalf("stale az-managed command should be pruned: %s", content)
	}
	// New az-managed command present exactly once.
	wantCmd := "az ai hook run --agent=claude --json session_start --issue=az-new"
	if strings.Count(content, wantCmd) != 1 {
		t.Fatalf("expected exactly one occurrence of new managed command, got %d: %s", strings.Count(content, wantCmd), content)
	}
}

func TestResolveRulesyncRunnerPicksPnpmWhenLockfilePresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: 9\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	runner, ok := resolveRulesyncRunner(dir)
	if !ok {
		t.Skip("pnpm not on PATH in this environment")
	}
	if runner.Argv[0] != "pnpm" {
		t.Fatalf("expected pnpm runner, got %v", runner.Argv)
	}
}

func TestResolveRulesyncRunnerFallsBackToPATHBinaryWhenNoLockfile(t *testing.T) {
	dir := t.TempDir()
	runner, ok := resolveRulesyncRunner(dir)
	if !ok {
		// rulesync isn't on PATH and there's no lockfile — expected absence.
		return
	}
	if runner.Argv[0] != "rulesync" {
		t.Fatalf("expected rulesync binary fallback, got %v", runner.Argv)
	}
}

func TestAIInstallCommandRoutesExplicitTargetRulesync(t *testing.T) {
	dir := t.TempDir()
	deps := &Dependencies{RepoDir: dir}
	opts, err := ParseAIInstallArgs([]string{"--target=rulesync", "--project-dir", dir, "--generate=never", "--issue", "az-explicit"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := AIInstallCommand(deps, opts); err != nil {
		t.Fatalf("AIInstallCommand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".rulesync", "hooks.json")); err != nil {
		t.Fatalf("expected .rulesync/hooks.json: %v", err)
	}
	// Should NOT have written claude/codex configs when target is explicit rulesync.
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.local.json")); err == nil {
		t.Fatalf("unexpected .claude config written for rulesync-only target")
	}
}

func TestAIInstallCommandAutoFallsBackToDetectedAgentsWhenNoRulesync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	deps := &Dependencies{RepoDir: dir}
	opts, err := ParseAIInstallArgs([]string{"--project-dir", dir, "--generate=never"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := AIInstallCommand(deps, opts); err != nil {
		t.Fatalf("AIInstallCommand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codex", "hooks.json")); err != nil {
		t.Fatalf("expected .codex/hooks.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".rulesync", "hooks.json")); err == nil {
		t.Fatalf("did not expect .rulesync/hooks.json without .rulesync/ marker")
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks: %v", err)
	}
	content := string(raw)
	for _, want := range []string{"SubagentStart", "subagent-start", "SubagentStop", "subagent-stop"} {
		if !strings.Contains(content, want) {
			t.Fatalf("codex hooks missing %q: %s", want, content)
		}
	}
}

func TestClaudeInstallerRequiresIssueID(t *testing.T) {
	dir := t.TempDir()
	deps := &Dependencies{RepoDir: dir}
	err := (claudeInstaller{}).Install(context.Background(), deps, AIInstallOptions{ProjectDir: dir})
	if err == nil || !strings.Contains(err.Error(), "requires --issue") {
		t.Fatalf("expected missing-issue error, got %v", err)
	}
}

func TestCodexCommandHookTrustHashMatchesCodexCurrentHash(t *testing.T) {
	command := buildCodexHookJSONCommand("permission-request")
	got := codexCommandHookTrustHash("permission_request", "", command)
	want := "sha256:eca2ae32b5c7d4e1310b8a0793063b88729f66cf8fdaea203ea9c35db9b81256"
	if got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}

	command = buildCodexHookJSONCommand("session-start")
	got = codexCommandHookTrustHash("session_start", "startup|resume", command)
	want = "sha256:47109ae6e8cf62ecfdbde08f0a668e147a2421176ac6cd4b647b70b155115193"
	if got != want {
		t.Fatalf("session_start hash = %s, want %s", got, want)
	}
}

func TestUpsertCodexHookTrustEntriesPreservesAndUpdates(t *testing.T) {
	input := `model = "gpt-5"

[hooks.state."/repo/.codex/hooks.json:pre_tool_use:0:0"]
enabled = false
trusted_hash = "sha256:old"

[hooks.state."/other/.codex/hooks.json:stop:0:0"]
trusted_hash = "sha256:keep"
`
	got := upsertCodexHookTrustEntries(input, map[string]string{
		"/repo/.codex/hooks.json:pre_tool_use:0:0": "sha256:new",
		"/repo/.codex/hooks.json:stop:0:0":         "sha256:added",
	})
	for _, want := range []string{
		`enabled = false`,
		`trusted_hash = "sha256:new"`,
		`[hooks.state."/repo/.codex/hooks.json:stop:0:0"]`,
		`trusted_hash = "sha256:added"`,
		`trusted_hash = "sha256:keep"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sha256:old") {
		t.Fatalf("old hash should be replaced:\n%s", got)
	}
}
