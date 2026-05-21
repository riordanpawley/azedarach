package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAIUninstallArgsDefaultsToAuto(t *testing.T) {
	opts, err := ParseAIUninstallArgs([]string{})
	if err != nil {
		t.Fatalf("ParseAIUninstallArgs: %v", err)
	}
	if len(opts.Targets) != 1 || opts.Targets[0] != AgentInstallTargetAuto {
		t.Fatalf("targets = %v, want [auto]", opts.Targets)
	}
}

func TestParseAIUninstallArgsAcceptsCSVTargets(t *testing.T) {
	opts, err := ParseAIUninstallArgs([]string{"--target=claude,opencode"})
	if err != nil {
		t.Fatalf("ParseAIUninstallArgs: %v", err)
	}
	if len(opts.Targets) != 2 {
		t.Fatalf("targets = %v, want 2 entries", opts.Targets)
	}
}

func TestParseAIUninstallArgsRejectsUnknownTarget(t *testing.T) {
	_, err := ParseAIUninstallArgs([]string{"--target=banana"})
	if err == nil || !strings.Contains(err.Error(), "unsupported uninstall target") {
		t.Fatalf("unsupported target err = %v", err)
	}
}

func TestResolveUninstallTargetsAutoDetectsExistingConfigs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".rulesync"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".rulesync", "hooks.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, _ := resolveUninstallTargets(dir, []AgentInstallTarget{AgentInstallTargetAuto})
	if !containsTarget(resolved, AgentInstallTargetClaude) || !containsTarget(resolved, AgentInstallTargetRulesync) {
		t.Fatalf("expected claude+rulesync detected from existing configs, got %v", resolved)
	}
	if containsTarget(resolved, AgentInstallTargetCodex) {
		t.Fatalf("did not expect codex without .codex/hooks.json, got %v", resolved)
	}
}

func TestResolveUninstallTargetsAutoEmptyWhenNoConfigs(t *testing.T) {
	dir := t.TempDir()
	resolved, _ := resolveUninstallTargets(dir, []AgentInstallTarget{AgentInstallTargetAuto})
	if len(resolved) != 0 {
		t.Fatalf("expected no targets in empty dir, got %v", resolved)
	}
}

func TestClaudeUninstallerStripsManagedEntriesPreservesUserEntries(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{
  "hooks": {
    "Notification": [
      {"matcher": "idle_prompt", "hooks": [
        {"type": "command", "command": "az ai hook run --agent=claude --json idle_prompt --issue=az-1"},
        {"type": "command", "command": "echo user-hook"}
      ]}
    ],
    "Stop": [
      {"hooks": [
        {"type": "command", "command": "az ai hook run --agent=claude --json stop --issue=az-1"}
      ]}
    ]
  },
  "permissions": {"allow": []}
}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := (claudeUninstaller{}).Uninstall(context.Background(), nil, AIUninstallOptions{ProjectDir: dir})
	if err != nil {
		t.Fatalf("claude uninstall: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if strings.Contains(content, "az ai hook run --agent=claude") {
		t.Fatalf("managed claude entries should be removed: %s", content)
	}
	if !strings.Contains(content, "echo user-hook") {
		t.Fatalf("user-managed hook removed: %s", content)
	}
	// Non-hooks keys like permissions should remain.
	if !strings.Contains(content, "permissions") {
		t.Fatalf("permissions key removed: %s", content)
	}

	// Re-running on a clean file should be a no-op (changed=false, no error).
	changed2, err := (claudeUninstaller{}).Uninstall(context.Background(), nil, AIUninstallOptions{ProjectDir: dir})
	if err != nil {
		t.Fatalf("re-run claude uninstall: %v", err)
	}
	if changed2 {
		t.Fatalf("second uninstall should be no-op, changed=true")
	}
}

func TestCodexUninstallerStripsWrappedShellEntries(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	wrappedCmd := buildCodexHookJSONCommand("session-start")
	initial := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "startup|resume",
					"hooks": []any{
						map[string]any{"type": "command", "command": wrappedCmd},
						map[string]any{"type": "command", "command": "echo user-codex-hook"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(hooksPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := (codexUninstaller{}).Uninstall(context.Background(), nil, AIUninstallOptions{ProjectDir: dir})
	if err != nil {
		t.Fatalf("codex uninstall: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	raw, _ := os.ReadFile(hooksPath)
	content := string(raw)
	if strings.Contains(content, "az ai hook run --agent=codex") {
		t.Fatalf("managed codex entries should be removed: %s", content)
	}
	if !strings.Contains(content, "echo user-codex-hook") {
		t.Fatalf("user-managed hook should be preserved: %s", content)
	}
}

func TestRulesyncUninstallerStripsManagedEntriesPreservesUserEntries(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".rulesync", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{
  "version": 1,
  "claudecode": {
    "hooks": {
      "sessionStart": [
        {"type": "command", "command": "echo user-pre"},
        {"type": "command", "command": "az ai hook run --agent=claude --json session_start --issue=az-1"}
      ]
    }
  },
  "codexcli": {
    "hooks": {
      "preToolUse": [
        {"type": "command", "command": "az ai hook run --agent=codex --json pre_tool_use"}
      ]
    }
  }
}`
	if err := os.WriteFile(hooksPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := (rulesyncUninstaller{}).Uninstall(context.Background(), nil, AIUninstallOptions{ProjectDir: dir})
	if err != nil {
		t.Fatalf("rulesync uninstall: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	raw, _ := os.ReadFile(hooksPath)
	content := string(raw)
	if strings.Contains(content, "az ai hook run") {
		t.Fatalf("managed rulesync entries should be removed: %s", content)
	}
	if !strings.Contains(content, "echo user-pre") {
		t.Fatalf("user-managed claudecode hook removed: %s", content)
	}
	// codexcli section had only managed entries — it should now be either
	// missing or have no hooks, but never reference the managed command.
	parsed := map[string]any{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if section, ok := parsed["codexcli"].(map[string]any); ok {
		if hooks, ok := section["hooks"].(map[string]any); ok && len(hooks) > 0 {
			t.Fatalf("codexcli.hooks should be empty/removed, got %v", hooks)
		}
	}
}

func TestOpencodeUninstallerRemovesPluginReferenceAndProjectFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")
	initial := `{
  "$schema": "https://opencode.ai/config.json",
  "plugins": ["opencode-tracker", "other-plugin"],
  "instructions": ["CLAUDE.md"]
}`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	projectPlugin := filepath.Join(dir, ".opencode", "plugins", openCodePluginFilename)
	if err := os.MkdirAll(filepath.Dir(projectPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPlugin, []byte("// managed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := (opencodeUninstaller{}).Uninstall(context.Background(), nil, AIUninstallOptions{ProjectDir: dir})
	if err != nil {
		t.Fatalf("opencode uninstall: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	raw, _ := os.ReadFile(configPath)
	content := string(raw)
	if strings.Contains(content, "opencode-tracker") {
		t.Fatalf("opencode-tracker should be removed from plugins: %s", content)
	}
	if !strings.Contains(content, "other-plugin") {
		t.Fatalf("other-plugin should be preserved: %s", content)
	}
	if _, err := os.Stat(projectPlugin); err == nil {
		t.Fatalf("project plugin file should be removed: %s", projectPlugin)
	}
}

func TestAIUninstallCommandAutoNoOpWhenNothingInstalled(t *testing.T) {
	dir := t.TempDir()
	deps := &Dependencies{RepoDir: dir}
	opts, err := ParseAIUninstallArgs([]string{"--project-dir", dir})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := AIUninstallCommand(deps, opts); err != nil {
		t.Fatalf("AIUninstallCommand on empty dir: %v", err)
	}
}

func TestAIInstallThenUninstallRoundTripLeavesNoManagedEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{RepoDir: dir}

	installOpts, err := ParseAIInstallArgs([]string{"--target=claude,codex", "--project-dir", dir, "--issue", "az-roundtrip", "--generate=never"})
	if err != nil {
		t.Fatalf("parse install: %v", err)
	}
	if err := AIInstallCommand(deps, installOpts); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Sanity: managed entries should now be present.
	claudeBefore, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if !strings.Contains(string(claudeBefore), "az ai hook run --agent=claude") {
		t.Fatalf("expected claude managed entries after install: %s", claudeBefore)
	}
	codexBefore, _ := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	if !strings.Contains(string(codexBefore), "az ai hook run --agent=codex") {
		t.Fatalf("expected codex managed entries after install: %s", codexBefore)
	}

	uninstallOpts, err := ParseAIUninstallArgs([]string{"--target=claude,codex", "--project-dir", dir})
	if err != nil {
		t.Fatalf("parse uninstall: %v", err)
	}
	if err := AIUninstallCommand(deps, uninstallOpts); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	claudeAfter, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if strings.Contains(string(claudeAfter), "az ai hook run --agent=claude") {
		t.Fatalf("claude managed entries should be gone after uninstall: %s", claudeAfter)
	}
	codexAfter, _ := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	if strings.Contains(string(codexAfter), "az ai hook run --agent=codex") {
		t.Fatalf("codex managed entries should be gone after uninstall: %s", codexAfter)
	}
}
