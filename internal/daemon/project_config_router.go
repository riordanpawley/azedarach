package daemon

import (
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
)

type daemonProjectRuntimeConfig struct {
	BaseBranch                 string
	CLITool                    string
	IssueTracker               appconfig.IssueTrackerConfig
	DangerouslySkipPermissions bool
	SessionShell               string
	SessionInitCommands        []string
	SessionSideEffectCommands  []string
	WorktreeInitCommands       []string
	IssueResources             appconfig.IssueResourcesConfig
}

func (d *Daemon) baseBranchForProject(projectID string) string {
	return d.runtimeConfigForProject(projectID).BaseBranch
}

func (d *Daemon) runtimeConfigForProject(projectID string) daemonProjectRuntimeConfig {
	if d == nil {
		return daemonProjectRuntimeConfig{
			BaseBranch:   "main",
			CLITool:      "claude",
			SessionShell: appconfig.DefaultSessionShell(),
		}
	}
	projectID = d.canonicalProjectID(projectID)

	defaultConfig := daemonProjectRuntimeConfig{
		BaseBranch:                 strings.TrimSpace(d.cfg.BaseBranch),
		CLITool:                    strings.TrimSpace(d.cfg.CLITool),
		IssueTracker:               appconfig.DefaultConfig().IssueTracker,
		DangerouslySkipPermissions: d.cfg.DangerouslySkipPermissions,
		SessionShell:               strings.TrimSpace(d.cfg.SessionShell),
		SessionInitCommands:        append([]string(nil), d.cfg.SessionInitCommands...),
		SessionSideEffectCommands:  append([]string(nil), d.cfg.SessionSideEffectCommands...),
		WorktreeInitCommands:       append([]string(nil), d.cfg.WorktreeInitCommands...),
		IssueResources:             cloneIssueResourcesConfig(d.cfg.IssueResources),
	}
	if defaultConfig.BaseBranch == "" {
		defaultConfig.BaseBranch = "main"
	}
	if defaultConfig.CLITool == "" {
		defaultConfig.CLITool = "claude"
	}
	if defaultConfig.SessionShell == "" {
		defaultConfig.SessionShell = appconfig.DefaultSessionShell()
	}

	d.projectConfigMu.Lock()
	if d.baseBranchByProject == nil {
		d.baseBranchByProject = map[string]string{}
	}
	if d.baseBranchByRoot == nil {
		d.baseBranchByRoot = map[string]string{}
	}
	if d.cliToolByProject == nil {
		d.cliToolByProject = map[string]string{}
	}
	if d.cliToolByRoot == nil {
		d.cliToolByRoot = map[string]string{}
	}
	if d.sessionShellByProject == nil {
		d.sessionShellByProject = map[string]string{}
	}
	if d.sessionShellByRoot == nil {
		d.sessionShellByRoot = map[string]string{}
	}
	if d.sessionInitCommandsByProject == nil {
		d.sessionInitCommandsByProject = map[string][]string{}
	}
	if d.sessionInitCommandsByRoot == nil {
		d.sessionInitCommandsByRoot = map[string][]string{}
	}
	if d.sessionSideEffectCommandsByProject == nil {
		d.sessionSideEffectCommandsByProject = map[string][]string{}
	}
	if d.sessionSideEffectCommandsByRoot == nil {
		d.sessionSideEffectCommandsByRoot = map[string][]string{}
	}
	if d.worktreeInitCommandsByProject == nil {
		d.worktreeInitCommandsByProject = map[string][]string{}
	}
	if d.worktreeInitCommandsByRoot == nil {
		d.worktreeInitCommandsByRoot = map[string][]string{}
	}
	if d.issueResourcesByProject == nil {
		d.issueResourcesByProject = map[string]appconfig.IssueResourcesConfig{}
	}
	if d.issueResourcesByRoot == nil {
		d.issueResourcesByRoot = map[string]appconfig.IssueResourcesConfig{}
	}
	if cached, ok := d.baseBranchByProject[projectID]; ok && strings.TrimSpace(cached) != "" {
		cfg := defaultConfig
		cfg.BaseBranch = cached
		if tool, ok := d.cliToolByProject[projectID]; ok && strings.TrimSpace(tool) != "" {
			cfg.CLITool = tool
		}
		if shell, ok := d.sessionShellByProject[projectID]; ok && strings.TrimSpace(shell) != "" {
			cfg.SessionShell = shell
		}
		if cmds, ok := d.sessionInitCommandsByProject[projectID]; ok {
			cfg.SessionInitCommands = append([]string(nil), cmds...)
		}
		if cmds, ok := d.sessionSideEffectCommandsByProject[projectID]; ok {
			cfg.SessionSideEffectCommands = append([]string(nil), cmds...)
		}
		if cmds, ok := d.worktreeInitCommandsByProject[projectID]; ok {
			cfg.WorktreeInitCommands = append([]string(nil), cmds...)
		}
		if resources, ok := d.issueResourcesByProject[projectID]; ok {
			cfg.IssueResources = cloneIssueResourcesConfig(resources)
		}
		d.projectConfigMu.Unlock()
		return cfg
	}
	d.projectConfigMu.Unlock()

	repoDir := d.resolveRepoDirForProject(projectID)
	cfg := defaultConfig

	if repoDir != "" {
		d.projectConfigMu.Lock()
		if cached, ok := d.baseBranchByRoot[repoDir]; ok && strings.TrimSpace(cached) != "" {
			cfg.BaseBranch = cached
			if tool, ok := d.cliToolByRoot[repoDir]; ok && strings.TrimSpace(tool) != "" {
				cfg.CLITool = tool
			}
			if shell, ok := d.sessionShellByRoot[repoDir]; ok && strings.TrimSpace(shell) != "" {
				cfg.SessionShell = shell
			}
			if cmds, ok := d.sessionInitCommandsByRoot[repoDir]; ok {
				cfg.SessionInitCommands = append([]string(nil), cmds...)
			}
			if cmds, ok := d.sessionSideEffectCommandsByRoot[repoDir]; ok {
				cfg.SessionSideEffectCommands = append([]string(nil), cmds...)
			}
			if cmds, ok := d.worktreeInitCommandsByRoot[repoDir]; ok {
				cfg.WorktreeInitCommands = append([]string(nil), cmds...)
			}
			if resources, ok := d.issueResourcesByRoot[repoDir]; ok {
				cfg.IssueResources = cloneIssueResourcesConfig(resources)
			}
			d.baseBranchByProject[projectID] = cfg.BaseBranch
			d.cliToolByProject[projectID] = cfg.CLITool
			d.sessionShellByProject[projectID] = cfg.SessionShell
			d.sessionInitCommandsByProject[projectID] = append([]string(nil), cfg.SessionInitCommands...)
			d.sessionSideEffectCommandsByProject[projectID] = append([]string(nil), cfg.SessionSideEffectCommands...)
			d.worktreeInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeInitCommands...)
			d.issueResourcesByProject[projectID] = cloneIssueResourcesConfig(cfg.IssueResources)
			d.projectConfigMu.Unlock()
			return cfg
		}
		d.projectConfigMu.Unlock()

		if hasConfigLayers(repoDir) {
			if loaded, err := appconfig.LoadConfig(repoDir); err == nil && loaded != nil {
				if candidate := strings.TrimSpace(loaded.Git.BaseBranch); candidate != "" {
					cfg.BaseBranch = candidate
				}
				if tool := strings.TrimSpace(loaded.CLITool); tool != "" {
					cfg.CLITool = tool
				}
				cfg.IssueTracker = loaded.IssueTracker
				cfg.DangerouslySkipPermissions = loaded.Session.DangerouslySkipPermissions
				if shell := strings.TrimSpace(loaded.Session.Shell); shell != "" {
					cfg.SessionShell = shell
				}
				cfg.SessionInitCommands = append([]string(nil), loaded.Session.InitCommands...)
				cfg.SessionSideEffectCommands = append([]string(nil), loaded.Session.SideEffectCommands...)
				cfg.WorktreeInitCommands = append([]string(nil), loaded.Worktree.InitCommands...)
				cfg.IssueResources = cloneIssueResourcesConfig(loaded.IssueResources)
			}
		}
	}

	d.projectConfigMu.Lock()
	if d.baseBranchByProject == nil {
		d.baseBranchByProject = map[string]string{}
	}
	d.baseBranchByProject[projectID] = cfg.BaseBranch
	d.cliToolByProject[projectID] = cfg.CLITool
	d.sessionShellByProject[projectID] = cfg.SessionShell
	d.sessionInitCommandsByProject[projectID] = append([]string(nil), cfg.SessionInitCommands...)
	d.sessionSideEffectCommandsByProject[projectID] = append([]string(nil), cfg.SessionSideEffectCommands...)
	d.worktreeInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeInitCommands...)
	d.issueResourcesByProject[projectID] = cloneIssueResourcesConfig(cfg.IssueResources)
	if repoDir != "" {
		if d.baseBranchByRoot == nil {
			d.baseBranchByRoot = map[string]string{}
		}
		d.baseBranchByRoot[repoDir] = cfg.BaseBranch
		d.cliToolByRoot[repoDir] = cfg.CLITool
		d.sessionShellByRoot[repoDir] = cfg.SessionShell
		d.sessionInitCommandsByRoot[repoDir] = append([]string(nil), cfg.SessionInitCommands...)
		d.sessionSideEffectCommandsByRoot[repoDir] = append([]string(nil), cfg.SessionSideEffectCommands...)
		d.worktreeInitCommandsByRoot[repoDir] = append([]string(nil), cfg.WorktreeInitCommands...)
		d.issueResourcesByRoot[repoDir] = cloneIssueResourcesConfig(cfg.IssueResources)
	}
	d.projectConfigMu.Unlock()

	return cfg
}

func cloneIssueResourcesConfig(cfg appconfig.IssueResourcesConfig) appconfig.IssueResourcesConfig {
	clone := appconfig.IssueResourcesConfig{
		Env:                        map[string]string{},
		PrepareCommands:            append([]string(nil), cfg.PrepareCommands...),
		FailedStartCleanupCommands: append([]string(nil), cfg.FailedStartCleanupCommands...),
		CleanupCommands:            append([]string(nil), cfg.CleanupCommands...),
	}
	for key, value := range cfg.Env {
		clone.Env[key] = value
	}
	return clone
}

func hasConfigLayers(projectPath string) bool {
	baseDirs, err := appconfig.ResolveConfigLayerBases(projectPath)
	if err != nil {
		return false
	}
	for _, baseDir := range baseDirs {
		for _, name := range []string{appconfig.ConfigFileName, appconfig.LocalConfigFileName} {
			configPath := filepath.Join(baseDir, appconfig.ConfigDirName, name)
			if _, err := os.Stat(configPath); err == nil {
				return true
			}
		}
	}
	return false
}
