package daemon

import (
	"strings"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
)

type daemonProjectRuntimeConfig struct {
	BaseBranch           string
	CLITool              string
	IssueTracker         appconfig.IssueTrackerConfig
	SessionShell         string
	SessionInitCommands  []string
	WorktreeInitCommands []string
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
		BaseBranch:           strings.TrimSpace(d.cfg.BaseBranch),
		CLITool:              strings.TrimSpace(d.cfg.CLITool),
		IssueTracker:         appconfig.DefaultConfig().IssueTracker,
		SessionShell:         strings.TrimSpace(d.cfg.SessionShell),
		SessionInitCommands:  append([]string(nil), d.cfg.SessionInitCommands...),
		WorktreeInitCommands: append([]string(nil), d.cfg.WorktreeInitCommands...),
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
	if d.worktreeInitCommandsByProject == nil {
		d.worktreeInitCommandsByProject = map[string][]string{}
	}
	if d.worktreeInitCommandsByRoot == nil {
		d.worktreeInitCommandsByRoot = map[string][]string{}
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
		if cmds, ok := d.worktreeInitCommandsByProject[projectID]; ok {
			cfg.WorktreeInitCommands = append([]string(nil), cmds...)
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
			if cmds, ok := d.worktreeInitCommandsByRoot[repoDir]; ok {
				cfg.WorktreeInitCommands = append([]string(nil), cmds...)
			}
			d.baseBranchByProject[projectID] = cfg.BaseBranch
			d.cliToolByProject[projectID] = cfg.CLITool
			d.sessionShellByProject[projectID] = cfg.SessionShell
			d.sessionInitCommandsByProject[projectID] = append([]string(nil), cfg.SessionInitCommands...)
			d.worktreeInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeInitCommands...)
			d.projectConfigMu.Unlock()
			return cfg
		}
		d.projectConfigMu.Unlock()

		if loaded, err := appconfig.LoadConfig(repoDir); err == nil && loaded != nil {
			if candidate := strings.TrimSpace(loaded.Git.BaseBranch); candidate != "" {
				cfg.BaseBranch = candidate
			}
			if tool := strings.TrimSpace(loaded.CLITool); tool != "" {
				cfg.CLITool = tool
			}
			cfg.IssueTracker = loaded.IssueTracker
			if shell := strings.TrimSpace(loaded.Session.Shell); shell != "" {
				cfg.SessionShell = shell
			}
			cfg.SessionInitCommands = append([]string(nil), loaded.Session.InitCommands...)
			cfg.WorktreeInitCommands = append([]string(nil), loaded.Worktree.InitCommands...)
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
	d.worktreeInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeInitCommands...)
	if repoDir != "" {
		if d.baseBranchByRoot == nil {
			d.baseBranchByRoot = map[string]string{}
		}
		d.baseBranchByRoot[repoDir] = cfg.BaseBranch
		d.cliToolByRoot[repoDir] = cfg.CLITool
		d.sessionShellByRoot[repoDir] = cfg.SessionShell
		d.sessionInitCommandsByRoot[repoDir] = append([]string(nil), cfg.SessionInitCommands...)
		d.worktreeInitCommandsByRoot[repoDir] = append([]string(nil), cfg.WorktreeInitCommands...)
	}
	d.projectConfigMu.Unlock()

	return cfg
}
