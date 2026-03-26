package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintUsageIncludesNewCommandFamilies(t *testing.T) {
	output := captureStdout(t, func() error {
		PrintUsage()
		return nil
	})

	for _, want := range []string{
		"notify <event> <issue-id>",
		"hooks install <issue-id>",
		"gate <issue-id>",
		"dev gate <issue-id>",
		"opencode <init|plugin>",
		"az notify idle_prompt az-123",
		"az hooks install az-123",
		"az gate az-123",
		"az dev gate az-123",
		"az opencode init",
		"az opencode plugin install",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage missing %q: %q", want, output)
		}
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
