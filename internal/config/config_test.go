package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfigFile(t *testing.T, baseDir string, body string) string {
	t.Helper()
	cfgDir := filepath.Join(baseDir, ConfigDirName)
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	cfgPath := filepath.Join(cfgDir, ConfigFileName)
	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o644))
	return cfgPath
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "claude", cfg.CLITool)
	assert.Equal(t, "local", cfg.IssueTracker.Backend)
	assert.False(t, cfg.IssueTracker.Sync.Enabled)
	assert.Equal(t, "linear-cli", cfg.IssueTracker.Linear.Command)
	assert.Equal(t, "sdk", cfg.IssueTracker.Linear.Webhooks.Transport)
	assert.NotNil(t, cfg.IssueTracker.Linear.Webhooks.Events)
	assert.Equal(t, "main", cfg.Git.BaseBranch)
	assert.Equal(t, "worktree", cfg.Git.WorkflowMode)
	assert.True(t, cfg.Git.ShowLineChanges)
	assert.Equal(t, "merge", cfg.Git.DefaultMergeStrategy)
	assert.True(t, cfg.Git.PushEnabled)
	assert.True(t, cfg.Git.FetchEnabled)

	assert.Equal(t, DefaultSessionShell(), cfg.Session.Shell)
	assert.False(t, cfg.Session.DangerouslySkipPermissions)
	assert.Equal(t, 30000, cfg.Session.TimeoutMs)
	assert.NotEmpty(t, cfg.Session.LogDir)
	assert.NotNil(t, cfg.Session.InitCommands)

	assert.Equal(t, 3000, cfg.DevServer.BasePort)
	assert.Equal(t, 3100, cfg.DevServer.MaxPort)
	assert.NotNil(t, cfg.DevServer.Environments)

	assert.True(t, cfg.Network.AutoDetect)
	assert.Equal(t, 60, cfg.Network.CheckInterval)
	assert.Equal(t, 300, cfg.Network.OfflineTimeout)
	assert.Equal(t, 3, cfg.Network.RetryAttempts)

	assert.True(t, cfg.PR.DraftByDefault)
	assert.True(t, cfg.PR.AutoLink)
	assert.True(t, cfg.PR.NotifyAfterCreate)
	assert.False(t, cfg.PR.CreateWithoutMerge)

	assert.Equal(t, "../", cfg.Worktree.BasePath)
	assert.Equal(t, "{project}-{issueID}", cfg.Worktree.NameFormat)
	assert.True(t, cfg.Worktree.AutoCleanup)
	assert.Equal(t, 7, cfg.Worktree.KeepDays)
	assert.NotNil(t, cfg.Worktree.InitCommands)

	assert.True(t, cfg.Spec.Enabled)
	assert.False(t, cfg.GitHooks.SpecSync.Enabled)
	assert.Empty(t, cfg.GitHooks.SpecSync.Command)
	assert.False(t, cfg.GitHooks.BoundaryCheck.Enabled)
	assert.Empty(t, cfg.GitHooks.BoundaryCheck.Command)
	assert.True(t, cfg.GitHooks.BestEffort)
	assert.NotNil(t, cfg.GitHooks.Commands)
	assert.False(t, cfg.GitHooks.Restage.Enabled)
	assert.NotNil(t, cfg.GitHooks.Restage.Paths)
}

func TestResolveConfigBaseFindsNearestAncestorConfig(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$schema":"./config.schema.json","$version":7}`)

	nested := filepath.Join(root, "go-bubbletea", "internal", "cli")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	base, err := ResolveConfigBase(nested)
	require.NoError(t, err)
	assert.Equal(t, root, base)
}

func TestLoadConfigFromDotAzedarachConfigJSON(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "cliTool": "opencode",
  "git": {
    "baseBranch": "develop",
    "workflowMode": "branch",
    "pushEnabled": false,
    "fetchEnabled": false
  },
  "pr": {
    "draftByDefault": false,
    "autoLink": false,
    "notifyAfterCreate": false,
    "createWithoutMerge": true
  },
  "network": {
    "autoDetect": false,
    "checkInterval": 120
  },
  "session": {
    "dangerouslySkipPermissions": true,
    "shell": "bash",
    "timeoutMs": 60000
  },
  "worktree": {
    "initCommands": ["direnv allow", "bun install"]
  },
  "spec": {
    "enabled": false
  },
  "devServer": {
    "basePort": 4000,
    "maxPort": 4100,
    "environments": {
      "NODE_ENV": "development"
    }
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)

	assert.Equal(t, "opencode", cfg.CLITool)
	assert.True(t, cfg.Session.DangerouslySkipPermissions)
	assert.Equal(t, "develop", cfg.Git.BaseBranch)
	assert.Equal(t, "branch", cfg.Git.WorkflowMode)
	assert.False(t, cfg.Git.PushEnabled)
	assert.False(t, cfg.Git.FetchEnabled)
	assert.Equal(t, "bash", cfg.Session.Shell)
	assert.Equal(t, 60000, cfg.Session.TimeoutMs)
	assert.Empty(t, cfg.Session.InitCommands)
	assert.Equal(t, []string{"direnv allow", "bun install"}, cfg.Worktree.InitCommands)
	assert.False(t, cfg.Spec.Enabled)
	assert.False(t, cfg.PR.DraftByDefault)
	assert.False(t, cfg.PR.AutoLink)
	assert.False(t, cfg.PR.NotifyAfterCreate)
	assert.True(t, cfg.PR.CreateWithoutMerge)
	assert.False(t, cfg.Network.AutoDetect)
	assert.Equal(t, 120, cfg.Network.CheckInterval)
	assert.Equal(t, 4000, cfg.DevServer.BasePort)
	assert.Equal(t, 4100, cfg.DevServer.MaxPort)
	assert.Equal(t, "development", cfg.DevServer.Environments["NODE_ENV"])

	// Defaults still merged for unspecified fields.
	assert.Equal(t, ".azedarach", cfg.Issues.Path)
	assert.Equal(t, 300, cfg.Issues.SyncInterval)
}

func TestLoadConfigKeepsSessionAndWorktreeInitCommandsDistinct(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "session": {
    "initCommands": ["session one", "session two"]
  },
  "worktree": {
    "initCommands": ["worktree one"]
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"session one", "session two"}, cfg.Session.InitCommands)
	assert.Equal(t, []string{"worktree one"}, cfg.Worktree.InitCommands)
}

func TestLoadAndSaveConfigUsesSingleIssueTrackerBackendConfig(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "issueTracker": {
    "backend": "linear",
    "sync": {
      "enabled": true
    },
    "linear": {
      "team": "CHE",
      "project": "Chefy",
      "webhooks": {
        "enabled": true,
        "transport": "cli",
        "events": ["Issue", "Comment"]
      }
    }
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, "linear", cfg.IssueTracker.Backend)
	assert.True(t, cfg.IssueTracker.Sync.Enabled)
	assert.Equal(t, "linear-cli", cfg.IssueTracker.Linear.Command)
	assert.Equal(t, "CHE", cfg.IssueTracker.Linear.Team)
	assert.Equal(t, "Chefy", cfg.IssueTracker.Linear.Project)
	assert.True(t, cfg.IssueTracker.Linear.Webhooks.Enabled)
	assert.Equal(t, "cli", cfg.IssueTracker.Linear.Webhooks.Transport)
	assert.Equal(t, []string{"Issue", "Comment"}, cfg.IssueTracker.Linear.Webhooks.Events)

	require.NoError(t, SaveConfig(cfg, cfgPath))
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Contains(t, raw, "issueTracker")
	require.NotContains(t, raw, "linear")
	require.JSONEq(t, `{
	  "backend": "linear",
	  "sync": {"enabled": true},
	  "linear": {
	    "command": "linear-cli",
	    "team": "CHE",
	    "project": "Chefy",
	    "webhooks": {
	      "enabled": true,
	      "transport": "cli",
	      "url": "",
	      "port": 0,
	      "events": ["Issue", "Comment"],
	      "secret": ""
	    }
	  }
	}`, mustMarshalJSON(t, raw["issueTracker"]))
}

func TestLoadConfigFromNestedPathUsesWorktreeBaseConfig(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$schema":"./config.schema.json","$version":7,"git":{"baseBranch":"release"}}`)
	nested := filepath.Join(root, "go-bubbletea")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	cfg, err := LoadConfig(nested)
	require.NoError(t, err)
	assert.Equal(t, "release", cfg.Git.BaseBranch)
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}

func TestLoadConfigNoConfigReturnsDefaults(t *testing.T) {
	tmp := t.TempDir()
	cfg, err := LoadConfig(tmp)
	require.NoError(t, err)

	defaults := DefaultConfig()
	assert.Equal(t, defaults.CLITool, cfg.CLITool)
	assert.Equal(t, defaults.Git.BaseBranch, cfg.Git.BaseBranch)
	assert.Equal(t, defaults.Spec.Enabled, cfg.Spec.Enabled)
}

func TestLoadConfigInvalidJSONReturnsError(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$schema":"./config.schema.json",`)

	_, err := LoadConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".azedarach/config.json")
}

func TestLoadConfigRejectsFutureVersion(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$schema":"./config.schema.json","$version":999}`)

	_, err := LoadConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported config version")
}

func TestSaveConfigWritesSchemaAndVersion(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ConfigDirName, ConfigFileName)

	cfg := DefaultConfig()
	cfg.CLITool = "codex"
	cfg.Session.DangerouslySkipPermissions = true
	cfg.Git.PushEnabled = false
	cfg.Git.FetchEnabled = false
	cfg.PR.DraftByDefault = false
	cfg.PR.AutoLink = false
	cfg.PR.NotifyAfterCreate = false
	cfg.PR.CreateWithoutMerge = true
	cfg.Network.AutoDetect = false
	cfg.Spec.Enabled = false
	cfg.Session.InitCommands = []string{"direnv allow", "bun install"}

	err := SaveConfig(cfg, path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, ConfigSchemaURL, raw["$schema"])
	assert.Equal(t, float64(CurrentConfigVersion), raw["$version"])

	sessionRaw, ok := raw["session"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, sessionRaw["dangerouslySkipPermissions"])

	gitRaw, ok := raw["git"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, gitRaw["pushEnabled"])
	assert.Equal(t, false, gitRaw["fetchEnabled"])

	prRaw, ok := raw["pr"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, prRaw["draftByDefault"])
	assert.Equal(t, false, prRaw["autoLink"])
	assert.Equal(t, false, prRaw["notifyAfterCreate"])
	assert.Equal(t, true, prRaw["createWithoutMerge"])

	networkRaw, ok := raw["network"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, networkRaw["autoDetect"])

	worktreeRaw, ok := raw["worktree"].(map[string]any)
	require.True(t, ok)
	initRaw, ok := worktreeRaw["initCommands"].([]any)
	require.True(t, ok)
	require.Len(t, initRaw, 0)
}

func TestMergeWithDefaultsPreservesExplicitValues(t *testing.T) {
	partial := &Config{
		CLITool: "opencode",
		Git: GitConfig{
			BaseBranch:   "develop",
			PushEnabled:  true,
			FetchEnabled: true,
		},
		Session:   SessionConfig{Shell: "bash"},
		DevServer: DevServerConfig{BasePort: 4000},
		Network:   NetworkConfig{AutoDetect: true},
		PR: PRConfig{
			DraftByDefault:     true,
			AutoLink:           true,
			NotifyAfterCreate:  true,
			CreateWithoutMerge: true,
		},
		Spec: SpecConfig{Enabled: false},
	}

	merged := MergeWithDefaults(partial)

	assert.Equal(t, "opencode", merged.CLITool)
	assert.Equal(t, "develop", merged.Git.BaseBranch)
	assert.True(t, merged.Git.PushEnabled)
	assert.True(t, merged.Git.FetchEnabled)
	assert.Equal(t, "bash", merged.Session.Shell)
	assert.Equal(t, 4000, merged.DevServer.BasePort)
	assert.True(t, merged.Network.AutoDetect)
	assert.True(t, merged.PR.DraftByDefault)
	assert.True(t, merged.PR.AutoLink)
	assert.True(t, merged.PR.NotifyAfterCreate)
	assert.True(t, merged.PR.CreateWithoutMerge)
	assert.False(t, merged.Spec.Enabled)
	assert.Equal(t, "worktree", merged.Git.WorkflowMode)
	assert.Equal(t, 30000, merged.Session.TimeoutMs)
	assert.Equal(t, 3100, merged.DevServer.MaxPort)
}
