package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AIUninstallOptions configures `az ai uninstall`.
type AIUninstallOptions struct {
	Targets    []AgentInstallTarget
	ProjectDir string
	Verbose    bool
}

// ParseAIUninstallArgs parses `az ai uninstall` arguments.
//
// Usage: az ai uninstall [--target=auto|rulesync|claude|codex|opencode,...] [--project-dir <dir>] [--verbose]
func ParseAIUninstallArgs(args []string) (AIUninstallOptions, error) {
	opts := AIUninstallOptions{}
	fs := flag.NewFlagSet("ai uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targetCSV := "auto"
	fs.StringVar(&targetCSV, "target", targetCSV, "comma-separated uninstall targets")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")
	if err := fs.Parse(args); err != nil {
		return AIUninstallOptions{}, err
	}
	if fs.NArg() != 0 {
		return AIUninstallOptions{}, fmt.Errorf("usage: az ai uninstall [--target=...] [--project-dir <dir>] [--verbose]")
	}

	targets := make([]AgentInstallTarget, 0)
	seen := make(map[AgentInstallTarget]struct{})
	for _, raw := range strings.Split(targetCSV, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		target := AgentInstallTarget(name)
		if !target.IsKnown() {
			return AIUninstallOptions{}, fmt.Errorf("unsupported uninstall target: %q (want auto, rulesync, claude, codex, opencode)", name)
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
	opts.Targets = targets
	return opts, nil
}

// PrintAIUninstallUsage prints usage for `az ai uninstall`.
func PrintAIUninstallUsage() {
	fmt.Println("Usage: az ai uninstall [--target=auto|rulesync|claude|codex|opencode,...] [--project-dir <dir>] [--verbose]")
	fmt.Println("Remove az-managed hook entries from one or more AI agent harnesses. Auto-detects targets from existing config files when --target=auto (default).")
	fmt.Println("User-authored hook entries are preserved; only entries managed by `az ai install` are pruned.")
}

// agentUninstaller is the port each uninstall adapter implements.
type agentUninstaller interface {
	Name() AgentInstallTarget
	Uninstall(ctx context.Context, deps *Dependencies, opts AIUninstallOptions) (changed bool, err error)
}

// AIUninstallCommand orchestrates per-target uninstalls.
func AIUninstallCommand(deps *Dependencies, opts AIUninstallOptions) error {
	projectDir, err := resolveProjectDir(opts.ProjectDir, deps)
	if err != nil {
		return err
	}
	opts.ProjectDir = projectDir

	resolved, autoNote := resolveUninstallTargets(projectDir, opts.Targets)
	if len(resolved) == 0 {
		fmt.Printf("az ai uninstall: no managed hook configs detected under %s — nothing to do\n", projectDir)
		fmt.Println("  Hint: pass --target=claude|codex|opencode|rulesync to force a specific uninstall.")
		return nil
	}
	if autoNote != "" && opts.Verbose {
		fmt.Println(autoNote)
	}

	ctx := context.Background()
	anyChanged := false
	for _, target := range resolved {
		uninstaller := uninstallerForTarget(target)
		if uninstaller == nil {
			return fmt.Errorf("no uninstaller registered for target: %s", target)
		}
		changed, err := uninstaller.Uninstall(ctx, deps, opts)
		if err != nil {
			return fmt.Errorf("uninstall target %s: %w", target, err)
		}
		anyChanged = anyChanged || changed
	}
	if !anyChanged {
		fmt.Println("az ai uninstall: no az-managed hook entries found across resolved targets.")
	}
	return nil
}

// resolveUninstallTargets expands an auto target by looking for the config
// files we know how to prune. This is intentionally different from install's
// detection (which uses marker dirs) — a user may have removed `.claude/` but
// kept `settings.local.json` somewhere up the tree, and we should still clean
// stale managed entries.
func resolveUninstallTargets(projectDir string, requested []AgentInstallTarget) ([]AgentInstallTarget, string) {
	if !containsTarget(requested, AgentInstallTargetAuto) {
		return requested, ""
	}
	detected := make([]AgentInstallTarget, 0, 4)
	if fileExists(filepath.Join(projectDir, ".claude", "settings.local.json")) {
		detected = append(detected, AgentInstallTargetClaude)
	}
	if fileExists(filepath.Join(projectDir, ".codex", "hooks.json")) {
		detected = append(detected, AgentInstallTargetCodex)
	}
	if fileExists(filepath.Join(projectDir, "opencode.json")) || fileExists(filepath.Join(projectDir, ".opencode")) {
		detected = append(detected, AgentInstallTargetOpencode)
	}
	if fileExists(filepath.Join(projectDir, ".rulesync", "hooks.json")) {
		detected = append(detected, AgentInstallTargetRulesync)
	}
	if len(detected) == 0 {
		return nil, "az ai uninstall: no managed hook configs detected."
	}
	return detected, fmt.Sprintf("az ai uninstall: auto-detected targets: %s", joinTargets(detected))
}

func uninstallerForTarget(target AgentInstallTarget) agentUninstaller {
	switch target {
	case AgentInstallTargetRulesync:
		return rulesyncUninstaller{}
	case AgentInstallTargetClaude:
		return claudeUninstaller{}
	case AgentInstallTargetCodex:
		return codexUninstaller{}
	case AgentInstallTargetOpencode:
		return opencodeUninstaller{}
	}
	return nil
}

// managedAICommandPrefixes are the command-prefixes that identify entries
// emitted by `az ai install` across all agent harnesses. Listed once so all
// uninstallers strip the same things, plus any legacy prefixes for tidiness.
var managedAICommandPrefixes = append([]string{
	claudeAIHookCommandPrefix,                       // `az ai hook run --agent=claude`
	"az ai hook run --agent=codex",                  // bare codex form (rulesync emits this)
	`/bin/sh -c 'out="$(az ai hook run --agent=codex`, // wrapped codex form (claude/codex installers emit this)
}, legacyAzCommandPrefixes...)

// ----- claude adapter --------------------------------------------------------

type claudeUninstaller struct{}

func (claudeUninstaller) Name() AgentInstallTarget { return AgentInstallTargetClaude }

func (claudeUninstaller) Uninstall(_ context.Context, _ *Dependencies, opts AIUninstallOptions) (bool, error) {
	settingsPath := filepath.Join(opts.ProjectDir, ".claude", "settings.local.json")
	if !fileExists(settingsPath) {
		if opts.Verbose {
			fmt.Printf("claude uninstall: no settings file at %s\n", settingsPath)
		}
		return false, nil
	}
	settings, err := readJSONObject(settingsPath)
	if err != nil {
		return false, fmt.Errorf("read claude settings: %w", err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	pruner := newLegacyPrefixPruner(managedAICommandPrefixes)
	removed := 0
	for key := range hooks {
		before := countCommandsRecursive(hooks[key])
		hooks[key] = pruner.prune(hooks[key])
		after := countCommandsRecursive(hooks[key])
		removed += before - after
		if len(normalizeAnySlice(hooks[key])) == 0 {
			delete(hooks, key)
		}
	}
	if removed == 0 {
		if opts.Verbose {
			fmt.Printf("claude uninstall: no az-managed entries in %s\n", settingsPath)
		}
		return false, nil
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}
	if err := writeJSONObject(settingsPath, settings); err != nil {
		return false, fmt.Errorf("write claude settings: %w", err)
	}
	fmt.Printf("Uninstalled %d claude hook entr%s from %s\n", removed, pluralY(removed), settingsPath)
	return true, nil
}

// ----- codex adapter ---------------------------------------------------------

type codexUninstaller struct{}

func (codexUninstaller) Name() AgentInstallTarget { return AgentInstallTargetCodex }

func (codexUninstaller) Uninstall(_ context.Context, _ *Dependencies, opts AIUninstallOptions) (bool, error) {
	hooksPath := filepath.Join(opts.ProjectDir, ".codex", "hooks.json")
	if !fileExists(hooksPath) {
		if opts.Verbose {
			fmt.Printf("codex uninstall: no hooks file at %s\n", hooksPath)
		}
		return false, nil
	}
	config, err := readJSONObject(hooksPath)
	if err != nil {
		return false, fmt.Errorf("read codex hooks config: %w", err)
	}
	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	pruner := newLegacyPrefixPruner(managedAICommandPrefixes)
	removed := 0
	for key := range hooks {
		before := countCommandsRecursive(hooks[key])
		hooks[key] = pruner.prune(hooks[key])
		after := countCommandsRecursive(hooks[key])
		removed += before - after
		if len(normalizeAnySlice(hooks[key])) == 0 {
			delete(hooks, key)
		}
	}
	if removed == 0 {
		if opts.Verbose {
			fmt.Printf("codex uninstall: no az-managed entries in %s\n", hooksPath)
		}
		return false, nil
	}
	if len(hooks) == 0 {
		delete(config, "hooks")
	} else {
		config["hooks"] = hooks
	}
	if err := writeJSONObject(hooksPath, config); err != nil {
		return false, fmt.Errorf("write codex hooks config: %w", err)
	}
	fmt.Printf("Uninstalled %d codex hook entr%s from %s\n", removed, pluralY(removed), hooksPath)
	return true, nil
}

// ----- opencode adapter ------------------------------------------------------

type opencodeUninstaller struct{}

func (opencodeUninstaller) Name() AgentInstallTarget { return AgentInstallTargetOpencode }

func (opencodeUninstaller) Uninstall(_ context.Context, deps *Dependencies, opts AIUninstallOptions) (bool, error) {
	anyChanged := false

	configPath := filepath.Join(opts.ProjectDir, "opencode.json")
	if fileExists(configPath) {
		config, err := readJSONObject(configPath)
		if err != nil {
			return false, fmt.Errorf("read opencode config: %w", err)
		}
		plugins := normalizeStrings(config["plugins"])
		filtered := make([]string, 0, len(plugins))
		dropped := 0
		for _, p := range plugins {
			if strings.TrimSpace(p) == "opencode-tracker" {
				dropped++
				continue
			}
			filtered = append(filtered, p)
		}
		if dropped > 0 {
			if len(filtered) == 0 {
				delete(config, "plugins")
			} else {
				config["plugins"] = filtered
			}
			if err := writeJSONObject(configPath, config); err != nil {
				return false, fmt.Errorf("write opencode config: %w", err)
			}
			fmt.Printf("Removed opencode-tracker plugin reference from %s\n", configPath)
			anyChanged = true
		} else if opts.Verbose {
			fmt.Printf("opencode uninstall: opencode-tracker not in %s plugins\n", configPath)
		}
	}

	// Remove both the global plugin file and the per-project copy. install
	// writes a placeholder source unconditionally, so the file is fully
	// managed by us and safe to delete.
	pluginPaths := []string{}
	if configDir, err := os.UserConfigDir(); err == nil {
		pluginPaths = append(pluginPaths, filepath.Join(configDir, "opencode", "plugins", openCodePluginFilename))
	} else if deps != nil && strings.TrimSpace(deps.RepoDir) != "" {
		pluginPaths = append(pluginPaths, filepath.Join(deps.RepoDir, ".config", "opencode", "plugins", openCodePluginFilename))
	}
	pluginPaths = append(pluginPaths, filepath.Join(opts.ProjectDir, ".opencode", "plugins", openCodePluginFilename))
	for _, path := range pluginPaths {
		if !fileExists(path) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return anyChanged, fmt.Errorf("remove opencode plugin %s: %w", path, err)
		}
		fmt.Printf("Removed opencode plugin file: %s\n", path)
		anyChanged = true
	}

	if !anyChanged && opts.Verbose {
		fmt.Println("opencode uninstall: nothing to remove")
	}
	return anyChanged, nil
}

// ----- rulesync adapter ------------------------------------------------------

type rulesyncUninstaller struct{}

func (rulesyncUninstaller) Name() AgentInstallTarget { return AgentInstallTargetRulesync }

func (rulesyncUninstaller) Uninstall(_ context.Context, _ *Dependencies, opts AIUninstallOptions) (bool, error) {
	hooksPath := filepath.Join(opts.ProjectDir, ".rulesync", "hooks.json")
	if !fileExists(hooksPath) {
		if opts.Verbose {
			fmt.Printf("rulesync uninstall: no hooks file at %s\n", hooksPath)
		}
		return false, nil
	}
	config, err := readJSONObject(hooksPath)
	if err != nil {
		return false, fmt.Errorf("read rulesync hooks config: %w", err)
	}
	removed := 0
	for _, sectionKey := range []string{"claudecode", "codexcli"} {
		section, _ := config[sectionKey].(map[string]any)
		if section == nil {
			continue
		}
		hooks, _ := section["hooks"].(map[string]any)
		if hooks == nil {
			continue
		}
		for event := range hooks {
			before := len(normalizeAnySlice(hooks[event]))
			pruned := pruneRulesyncManagedEntries(normalizeAnySlice(hooks[event]), managedAICommandPrefixes...)
			after := len(pruned)
			removed += before - after
			if after == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = pruned
			}
		}
		if len(hooks) == 0 {
			delete(section, "hooks")
		} else {
			section["hooks"] = hooks
		}
		// If the section is now empty (no hooks, no other keys), drop it so
		// the file shrinks back toward its un-installed shape.
		if len(section) == 0 {
			delete(config, sectionKey)
		} else {
			config[sectionKey] = section
		}
	}
	if removed == 0 {
		if opts.Verbose {
			fmt.Printf("rulesync uninstall: no az-managed entries in %s\n", hooksPath)
		}
		return false, nil
	}
	if err := writeJSONObject(hooksPath, config); err != nil {
		return false, fmt.Errorf("write rulesync hooks config: %w", err)
	}
	fmt.Printf("Uninstalled %d rulesync hook entr%s from %s\n", removed, pluralY(removed), hooksPath)
	return true, nil
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
