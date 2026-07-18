package daemon

import (
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
)

type daemonProjectRuntimeConfig struct {
	BaseBranch                 string
	WorkflowMode               string
	CLITool                    string
	IssueTracker               appconfig.IssueTrackerConfig
	DangerouslySkipPermissions bool
	CodexAppServer             bool
	SessionShell               string
	SessionSyncInitCommands    []string
	SessionAsyncInitCommands   []string
	WorktreeInitCommands       []string
	WorktreeAsyncInitCommands  []string
	GateFailureArtifactPaths   []string
	IssueResources             appconfig.IssueResourcesConfig
	IssueAutoArchive           appconfig.IssueAutoArchiveConfig
	ScheduledScripts           appconfig.ScheduledScriptsConfig
	Orchestration              appconfig.OrchestrationConfig
}

func (d *Daemon) baseBranchForProject(projectID string) string {
	return d.runtimeConfigForProject(projectID).BaseBranch
}

func (d *Daemon) workflowModeForProject(projectID string) string {
	return d.runtimeConfigForProject(projectID).WorkflowMode
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
		WorkflowMode:               strings.TrimSpace(d.cfg.GitWorkflowMode),
		CLITool:                    strings.TrimSpace(d.cfg.CLITool),
		IssueTracker:               appconfig.DefaultConfig().IssueTracker,
		DangerouslySkipPermissions: d.cfg.DangerouslySkipPermissions,
		CodexAppServer:             d.cfg.CodexAppServer,
		SessionShell:               strings.TrimSpace(d.cfg.SessionShell),
		SessionSyncInitCommands:    append([]string(nil), d.cfg.SessionSyncInitCommands...),
		SessionAsyncInitCommands:   append([]string(nil), d.cfg.SessionAsyncInitCommands...),
		WorktreeInitCommands:       append([]string(nil), d.cfg.WorktreeInitCommands...),
		WorktreeAsyncInitCommands:  append([]string(nil), d.cfg.WorktreeAsyncInitCommands...),
		GateFailureArtifactPaths:   append([]string(nil), d.cfg.GateFailureArtifactPaths...),
		IssueResources:             cloneIssueResourcesConfig(d.cfg.IssueResources),
		IssueAutoArchive:           cloneIssueAutoArchiveConfig(d.cfg.IssueAutoArchive),
		ScheduledScripts:           cloneScheduledScriptsConfig(d.cfg.ScheduledScripts),
		Orchestration:              d.cfg.Orchestration,
	}
	if defaultConfig.BaseBranch == "" {
		defaultConfig.BaseBranch = "main"
	}
	if defaultConfig.WorkflowMode == "" {
		defaultConfig.WorkflowMode = "worktree"
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
	if d.workflowModeByProject == nil {
		d.workflowModeByProject = map[string]string{}
	}
	if d.workflowModeByRoot == nil {
		d.workflowModeByRoot = map[string]string{}
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
	if d.codexAppServerByProject == nil {
		d.codexAppServerByProject = map[string]bool{}
	}
	if d.codexAppServerByRoot == nil {
		d.codexAppServerByRoot = map[string]bool{}
	}
	if d.sessionSyncInitCommandsByProject == nil {
		d.sessionSyncInitCommandsByProject = map[string][]string{}
	}
	if d.sessionSyncInitCommandsByRoot == nil {
		d.sessionSyncInitCommandsByRoot = map[string][]string{}
	}
	if d.sessionAsyncInitCommandsByProject == nil {
		d.sessionAsyncInitCommandsByProject = map[string][]string{}
	}
	if d.sessionAsyncInitCommandsByRoot == nil {
		d.sessionAsyncInitCommandsByRoot = map[string][]string{}
	}
	if d.worktreeInitCommandsByProject == nil {
		d.worktreeInitCommandsByProject = map[string][]string{}
	}
	if d.worktreeInitCommandsByRoot == nil {
		d.worktreeInitCommandsByRoot = map[string][]string{}
	}
	if d.worktreeAsyncInitCommandsByProject == nil {
		d.worktreeAsyncInitCommandsByProject = map[string][]string{}
	}
	if d.worktreeAsyncInitCommandsByRoot == nil {
		d.worktreeAsyncInitCommandsByRoot = map[string][]string{}
	}
	if d.gateFailureArtifactPathsByProject == nil {
		d.gateFailureArtifactPathsByProject = map[string][]string{}
	}
	if d.gateFailureArtifactPathsByRoot == nil {
		d.gateFailureArtifactPathsByRoot = map[string][]string{}
	}
	if d.issueResourcesByProject == nil {
		d.issueResourcesByProject = map[string]appconfig.IssueResourcesConfig{}
	}
	if d.issueResourcesByRoot == nil {
		d.issueResourcesByRoot = map[string]appconfig.IssueResourcesConfig{}
	}
	if d.issueAutoArchiveByProject == nil {
		d.issueAutoArchiveByProject = map[string]appconfig.IssueAutoArchiveConfig{}
	}
	if d.issueAutoArchiveByRoot == nil {
		d.issueAutoArchiveByRoot = map[string]appconfig.IssueAutoArchiveConfig{}
	}
	if d.scheduledScriptsByProject == nil {
		d.scheduledScriptsByProject = map[string]appconfig.ScheduledScriptsConfig{}
	}
	if d.scheduledScriptsByRoot == nil {
		d.scheduledScriptsByRoot = map[string]appconfig.ScheduledScriptsConfig{}
	}
	if d.orchestrationByProject == nil {
		d.orchestrationByProject = map[string]appconfig.OrchestrationConfig{}
	}
	if d.orchestrationByRoot == nil {
		d.orchestrationByRoot = map[string]appconfig.OrchestrationConfig{}
	}
	if cached, ok := d.baseBranchByProject[projectID]; ok && strings.TrimSpace(cached) != "" {
		cfg := defaultConfig
		cfg.BaseBranch = cached
		if workflowMode, ok := d.workflowModeByProject[projectID]; ok && strings.TrimSpace(workflowMode) != "" {
			cfg.WorkflowMode = workflowMode
		}
		if tool, ok := d.cliToolByProject[projectID]; ok && strings.TrimSpace(tool) != "" {
			cfg.CLITool = tool
		}
		if shell, ok := d.sessionShellByProject[projectID]; ok && strings.TrimSpace(shell) != "" {
			cfg.SessionShell = shell
		}
		cfg.CodexAppServer = d.codexAppServerByProject[projectID]
		if cmds, ok := d.sessionSyncInitCommandsByProject[projectID]; ok {
			cfg.SessionSyncInitCommands = append([]string(nil), cmds...)
		}
		if cmds, ok := d.sessionAsyncInitCommandsByProject[projectID]; ok {
			cfg.SessionAsyncInitCommands = append([]string(nil), cmds...)
		}
		if cmds, ok := d.worktreeInitCommandsByProject[projectID]; ok {
			cfg.WorktreeInitCommands = append([]string(nil), cmds...)
		}
		if cmds, ok := d.worktreeAsyncInitCommandsByProject[projectID]; ok {
			cfg.WorktreeAsyncInitCommands = append([]string(nil), cmds...)
		}
		if paths, ok := d.gateFailureArtifactPathsByProject[projectID]; ok {
			cfg.GateFailureArtifactPaths = append([]string(nil), paths...)
		}
		if resources, ok := d.issueResourcesByProject[projectID]; ok {
			cfg.IssueResources = cloneIssueResourcesConfig(resources)
		}
		if autoArchive, ok := d.issueAutoArchiveByProject[projectID]; ok {
			cfg.IssueAutoArchive = cloneIssueAutoArchiveConfig(autoArchive)
		}
		if scripts, ok := d.scheduledScriptsByProject[projectID]; ok {
			cfg.ScheduledScripts = cloneScheduledScriptsConfig(scripts)
		}
		if orchestration, ok := d.orchestrationByProject[projectID]; ok {
			cfg.Orchestration = orchestration
		}
		d.projectConfigMu.Unlock()
		return cfg
	}
	d.projectConfigMu.Unlock()

	repoDir := d.resolveRepoDirForProject(projectID)
	cfg := defaultConfig
	if repoDir != "" && daemonStoreRootKey(repoDir) != daemonStoreRootKey(d.cfg.RepoDir) {
		// Capabilities are project-owned. A registered project with no explicit
		// artifact configuration must not inherit the daemon root consumer's paths.
		cfg.GateFailureArtifactPaths = []string{}
	}

	if repoDir != "" {
		d.projectConfigMu.Lock()
		if cached, ok := d.baseBranchByRoot[repoDir]; ok && strings.TrimSpace(cached) != "" {
			cfg.BaseBranch = cached
			if workflowMode, ok := d.workflowModeByRoot[repoDir]; ok && strings.TrimSpace(workflowMode) != "" {
				cfg.WorkflowMode = workflowMode
			}
			if tool, ok := d.cliToolByRoot[repoDir]; ok && strings.TrimSpace(tool) != "" {
				cfg.CLITool = tool
			}
			if shell, ok := d.sessionShellByRoot[repoDir]; ok && strings.TrimSpace(shell) != "" {
				cfg.SessionShell = shell
			}
			cfg.CodexAppServer = d.codexAppServerByRoot[repoDir]
			if cmds, ok := d.sessionSyncInitCommandsByRoot[repoDir]; ok {
				cfg.SessionSyncInitCommands = append([]string(nil), cmds...)
			}
			if cmds, ok := d.sessionAsyncInitCommandsByRoot[repoDir]; ok {
				cfg.SessionAsyncInitCommands = append([]string(nil), cmds...)
			}
			if cmds, ok := d.worktreeInitCommandsByRoot[repoDir]; ok {
				cfg.WorktreeInitCommands = append([]string(nil), cmds...)
			}
			if cmds, ok := d.worktreeAsyncInitCommandsByRoot[repoDir]; ok {
				cfg.WorktreeAsyncInitCommands = append([]string(nil), cmds...)
			}
			if paths, ok := d.gateFailureArtifactPathsByRoot[repoDir]; ok {
				cfg.GateFailureArtifactPaths = append([]string(nil), paths...)
			}
			if resources, ok := d.issueResourcesByRoot[repoDir]; ok {
				cfg.IssueResources = cloneIssueResourcesConfig(resources)
			}
			if autoArchive, ok := d.issueAutoArchiveByRoot[repoDir]; ok {
				cfg.IssueAutoArchive = cloneIssueAutoArchiveConfig(autoArchive)
			}
			if scripts, ok := d.scheduledScriptsByRoot[repoDir]; ok {
				cfg.ScheduledScripts = cloneScheduledScriptsConfig(scripts)
			}
			if orchestration, ok := d.orchestrationByRoot[repoDir]; ok {
				cfg.Orchestration = orchestration
			}
			d.baseBranchByProject[projectID] = cfg.BaseBranch
			d.workflowModeByProject[projectID] = cfg.WorkflowMode
			d.cliToolByProject[projectID] = cfg.CLITool
			d.sessionShellByProject[projectID] = cfg.SessionShell
			d.codexAppServerByProject[projectID] = cfg.CodexAppServer
			d.sessionSyncInitCommandsByProject[projectID] = append([]string(nil), cfg.SessionSyncInitCommands...)
			d.sessionAsyncInitCommandsByProject[projectID] = append([]string(nil), cfg.SessionAsyncInitCommands...)
			d.worktreeInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeInitCommands...)
			d.worktreeAsyncInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeAsyncInitCommands...)
			d.gateFailureArtifactPathsByProject[projectID] = append([]string(nil), cfg.GateFailureArtifactPaths...)
			d.issueResourcesByProject[projectID] = cloneIssueResourcesConfig(cfg.IssueResources)
			d.issueAutoArchiveByProject[projectID] = cloneIssueAutoArchiveConfig(cfg.IssueAutoArchive)
			d.scheduledScriptsByProject[projectID] = cloneScheduledScriptsConfig(cfg.ScheduledScripts)
			d.orchestrationByProject[projectID] = cfg.Orchestration
			d.projectConfigMu.Unlock()
			return cfg
		}
		d.projectConfigMu.Unlock()

		if hasConfigLayers(repoDir) {
			if loaded, err := appconfig.LoadConfig(repoDir); err == nil && loaded != nil {
				if candidate := strings.TrimSpace(loaded.Git.BaseBranch); candidate != "" {
					cfg.BaseBranch = candidate
				}
				if workflowMode := strings.TrimSpace(loaded.Git.WorkflowMode); workflowMode != "" {
					cfg.WorkflowMode = workflowMode
				}
				if tool := strings.TrimSpace(loaded.CLITool); tool != "" {
					cfg.CLITool = tool
				}
				cfg.IssueTracker = loaded.IssueTracker
				cfg.DangerouslySkipPermissions = loaded.Session.DangerouslySkipPermissions
				cfg.CodexAppServer = loaded.Session.CodexAppServer
				if shell := strings.TrimSpace(loaded.Session.Shell); shell != "" {
					cfg.SessionShell = shell
				}
				cfg.SessionSyncInitCommands = append([]string(nil), loaded.Session.SyncInitCommands...)
				cfg.SessionAsyncInitCommands = append([]string(nil), loaded.Session.AsyncInitCommands...)
				cfg.WorktreeInitCommands = append([]string(nil), loaded.Worktree.SyncInitCommands...)
				cfg.WorktreeAsyncInitCommands = append([]string(nil), loaded.Worktree.AsyncInitCommands...)
				cfg.GateFailureArtifactPaths = append([]string(nil), loaded.Gate.FailureArtifactPaths...)
				cfg.IssueResources = cloneIssueResourcesConfig(loaded.IssueResources)
				cfg.IssueAutoArchive = cloneIssueAutoArchiveConfig(loaded.Issues.AutoArchive)
				cfg.ScheduledScripts = cloneScheduledScriptsConfig(loaded.ScheduledScripts)
				cfg.Orchestration = loaded.Orchestration
			}
		}
	}

	d.projectConfigMu.Lock()
	if d.baseBranchByProject == nil {
		d.baseBranchByProject = map[string]string{}
	}
	d.baseBranchByProject[projectID] = cfg.BaseBranch
	d.workflowModeByProject[projectID] = cfg.WorkflowMode
	d.cliToolByProject[projectID] = cfg.CLITool
	d.sessionShellByProject[projectID] = cfg.SessionShell
	d.codexAppServerByProject[projectID] = cfg.CodexAppServer
	d.sessionSyncInitCommandsByProject[projectID] = append([]string(nil), cfg.SessionSyncInitCommands...)
	d.sessionAsyncInitCommandsByProject[projectID] = append([]string(nil), cfg.SessionAsyncInitCommands...)
	d.worktreeInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeInitCommands...)
	d.worktreeAsyncInitCommandsByProject[projectID] = append([]string(nil), cfg.WorktreeAsyncInitCommands...)
	d.gateFailureArtifactPathsByProject[projectID] = append([]string(nil), cfg.GateFailureArtifactPaths...)
	d.issueResourcesByProject[projectID] = cloneIssueResourcesConfig(cfg.IssueResources)
	d.issueAutoArchiveByProject[projectID] = cloneIssueAutoArchiveConfig(cfg.IssueAutoArchive)
	d.scheduledScriptsByProject[projectID] = cloneScheduledScriptsConfig(cfg.ScheduledScripts)
	d.orchestrationByProject[projectID] = cfg.Orchestration
	if repoDir != "" {
		if d.baseBranchByRoot == nil {
			d.baseBranchByRoot = map[string]string{}
		}
		d.baseBranchByRoot[repoDir] = cfg.BaseBranch
		d.workflowModeByRoot[repoDir] = cfg.WorkflowMode
		d.cliToolByRoot[repoDir] = cfg.CLITool
		d.sessionShellByRoot[repoDir] = cfg.SessionShell
		d.codexAppServerByRoot[repoDir] = cfg.CodexAppServer
		d.sessionSyncInitCommandsByRoot[repoDir] = append([]string(nil), cfg.SessionSyncInitCommands...)
		d.sessionAsyncInitCommandsByRoot[repoDir] = append([]string(nil), cfg.SessionAsyncInitCommands...)
		d.worktreeInitCommandsByRoot[repoDir] = append([]string(nil), cfg.WorktreeInitCommands...)
		d.worktreeAsyncInitCommandsByRoot[repoDir] = append([]string(nil), cfg.WorktreeAsyncInitCommands...)
		d.gateFailureArtifactPathsByRoot[repoDir] = append([]string(nil), cfg.GateFailureArtifactPaths...)
		d.issueResourcesByRoot[repoDir] = cloneIssueResourcesConfig(cfg.IssueResources)
		d.issueAutoArchiveByRoot[repoDir] = cloneIssueAutoArchiveConfig(cfg.IssueAutoArchive)
		d.scheduledScriptsByRoot[repoDir] = cloneScheduledScriptsConfig(cfg.ScheduledScripts)
		d.orchestrationByRoot[repoDir] = cfg.Orchestration
	}
	d.projectConfigMu.Unlock()

	return cfg
}

func cloneIssueAutoArchiveConfig(cfg appconfig.IssueAutoArchiveConfig) appconfig.IssueAutoArchiveConfig {
	return cfg
}

func cloneIssueResourcesConfig(cfg appconfig.IssueResourcesConfig) appconfig.IssueResourcesConfig {
	clone := appconfig.IssueResourcesConfig{
		Env:                        map[string]string{},
		PrepareCommands:            append([]string(nil), cfg.PrepareCommands...),
		FailedStartCleanupCommands: append([]string(nil), cfg.FailedStartCleanupCommands...),
		CleanupCommands:            append([]string(nil), cfg.CleanupCommands...),
		ReconcileCommand:           cfg.ReconcileCommand,
	}
	for key, value := range cfg.Env {
		clone.Env[key] = value
	}
	return clone
}

func cloneScheduledScriptsConfig(cfg appconfig.ScheduledScriptsConfig) appconfig.ScheduledScriptsConfig {
	clone := appconfig.ScheduledScriptsConfig{
		Env:     map[string]string{},
		Scripts: make([]appconfig.ScheduledScriptConfig, 0, len(cfg.Scripts)),
	}
	for key, value := range cfg.Env {
		clone.Env[key] = value
	}
	for _, script := range cfg.Scripts {
		next := script
		next.Env = map[string]string{}
		for key, value := range script.Env {
			next.Env[key] = value
		}
		clone.Scripts = append(clone.Scripts, next)
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
