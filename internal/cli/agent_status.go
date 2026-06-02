package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AIStatusOptions configures `az ai status`.
type AIStatusOptions struct {
	Targets    []AgentInstallTarget
	ProjectDir string
	JSON       bool
}

type AIStatusResult struct {
	ProjectDir string                 `json:"project_dir"`
	Targets    []AIStatusTargetResult `json:"targets"`
}

type AIStatusTargetResult struct {
	Target    AgentInstallTarget `json:"target"`
	Detected  bool               `json:"detected"`
	Installed bool               `json:"installed"`
	Path      string             `json:"path,omitempty"`
	Reason    string             `json:"reason,omitempty"`
	Install   string             `json:"install"`
}

// ParseAIStatusArgs parses `az ai status` arguments.
func ParseAIStatusArgs(args []string) (AIStatusOptions, error) {
	opts := AIStatusOptions{}
	fs := flag.NewFlagSet("ai status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targetCSV := "auto"
	fs.StringVar(&targetCSV, "target", targetCSV, "comma-separated targets to inspect")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return AIStatusOptions{}, err
	}
	if fs.NArg() != 0 {
		return AIStatusOptions{}, fmt.Errorf("usage: az ai status [--target=auto|rulesync|claude|codex|opencode,...] [--project-dir <dir>] [--json]")
	}
	targets, err := parseAgentTargets(targetCSV)
	if err != nil {
		return AIStatusOptions{}, err
	}
	opts.Targets = targets
	return opts, nil
}

func PrintAIStatusUsage() {
	fmt.Println("Usage: az ai status [--target=auto|rulesync|claude|codex|opencode,...] [--project-dir <dir>] [--json]")
	fmt.Println("Check whether Azedarach-managed AI hook commands are installed for the current project.")
}

func AIStatusCommand(deps *Dependencies, opts AIStatusOptions) error {
	result, err := AIStatus(deps, opts)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("AI hook status for %s\n", result.ProjectDir)
	for _, target := range result.Targets {
		state := "missing"
		if target.Installed {
			state = "installed"
		}
		detected := "not detected"
		if target.Detected {
			detected = "detected"
		}
		fmt.Printf("- %s: %s (%s)\n", target.Target, state, detected)
		if target.Path != "" {
			fmt.Printf("  path: %s\n", target.Path)
		}
		if target.Reason != "" {
			fmt.Printf("  reason: %s\n", target.Reason)
		}
		fmt.Printf("  install/update: %s\n", target.Install)
	}
	return nil
}

func AIStatus(deps *Dependencies, opts AIStatusOptions) (AIStatusResult, error) {
	projectDir, err := resolveProjectDir(opts.ProjectDir, deps)
	if err != nil {
		return AIStatusResult{}, err
	}
	targets := opts.Targets
	if len(targets) == 0 {
		targets = []AgentInstallTarget{AgentInstallTargetAuto}
	}
	resolved, _ := resolveInstallTargets(projectDir, targets)
	if containsTarget(targets, AgentInstallTargetAuto) && len(resolved) == 0 {
		resolved = []AgentInstallTarget{
			AgentInstallTargetRulesync,
			AgentInstallTargetClaude,
			AgentInstallTargetCodex,
			AgentInstallTargetOpencode,
		}
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i] < resolved[j] })

	result := AIStatusResult{
		ProjectDir: projectDir,
		Targets:    make([]AIStatusTargetResult, 0, len(resolved)),
	}
	for _, target := range resolved {
		if target == AgentInstallTargetAuto {
			continue
		}
		result.Targets = append(result.Targets, aiStatusForTarget(projectDir, target))
	}
	return result, nil
}

func aiStatusForTarget(projectDir string, target AgentInstallTarget) AIStatusTargetResult {
	result := AIStatusTargetResult{
		Target:  target,
		Install: fmt.Sprintf("az ai install --target=%s", target),
	}
	switch target {
	case AgentInstallTargetRulesync:
		result.Detected = rulesyncProjectMarker(projectDir)
		result.Path = filepath.Join(projectDir, ".rulesync", "hooks.json")
		result.Installed = fileContainsAll(result.Path, "az ai hook run --agent=claude", "az ai hook run --agent=codex")
	case AgentInstallTargetClaude:
		result.Detected = fileExists(filepath.Join(projectDir, ".claude"))
		result.Path = filepath.Join(projectDir, ".claude", "settings.local.json")
		result.Installed = fileContainsAll(result.Path, claudeAIHookCommandPrefix)
	case AgentInstallTargetCodex:
		result.Detected = fileExists(filepath.Join(projectDir, ".codex"))
		result.Path = filepath.Join(projectDir, ".codex", "hooks.json")
		result.Installed = fileContainsAll(result.Path, "az ai hook run --agent=codex")
	case AgentInstallTargetOpencode:
		result.Detected = fileExists(filepath.Join(projectDir, ".opencode")) || fileExists(filepath.Join(projectDir, "opencode.json"))
		result.Path = filepath.Join(projectDir, ".opencode", "plugins", openCodePluginFilename)
		result.Installed = fileExists(result.Path) && opencodeProjectConfigIncludesTracker(projectDir)
	}
	if !result.Detected {
		result.Reason = "target marker not detected; pass --target to install explicitly"
	} else if !result.Installed {
		result.Reason = "managed Azedarach hook command not found"
	}
	return result
}

func fileContainsAll(path string, needles ...string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(raw)
	for _, needle := range needles {
		if !strings.Contains(content, needle) {
			return false
		}
	}
	return true
}

func opencodeProjectConfigIncludesTracker(projectDir string) bool {
	config, err := readJSONObject(filepath.Join(projectDir, "opencode.json"))
	if err != nil {
		return false
	}
	for _, plugin := range normalizeStrings(config["plugins"]) {
		if plugin == "opencode-tracker" {
			return true
		}
	}
	return false
}

func parseAgentTargets(targetCSV string) ([]AgentInstallTarget, error) {
	targets := make([]AgentInstallTarget, 0)
	seen := make(map[AgentInstallTarget]struct{})
	for _, raw := range strings.Split(targetCSV, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		target := AgentInstallTarget(name)
		if !target.IsKnown() {
			return nil, fmt.Errorf("unsupported target: %q (want auto, rulesync, claude, codex, opencode)", name)
		}
		if _, dup := seen[target]; dup {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		targets = append(targets, AgentInstallTargetAuto)
	}
	return targets, nil
}
