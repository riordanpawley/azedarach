package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfigFile(t *testing.T, baseDir string, body string) string {
	t.Helper()
	return writeConfigFileNamed(t, baseDir, ConfigFileName, body)
}

func writeLocalConfigFile(t *testing.T, baseDir string, body string) string {
	t.Helper()
	return writeConfigFileNamed(t, baseDir, LocalConfigFileName, body)
}

func writeConfigFileNamed(t *testing.T, baseDir string, name string, body string) string {
	t.Helper()
	cfgDir := filepath.Join(baseDir, ConfigDirName)
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	cfgPath := filepath.Join(cfgDir, name)
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
	assert.Empty(t, cfg.Gate.Command)
	assert.Empty(t, cfg.Gate.FailureArtifactPaths)

	assert.Equal(t, DefaultSessionShell(), cfg.Session.Shell)
	assert.False(t, cfg.Session.DangerouslySkipPermissions)
	assert.Equal(t, 30000, cfg.Session.TimeoutMs)
	assert.NotEmpty(t, cfg.Session.LogDir)
	assert.NotNil(t, cfg.Session.SyncInitCommands)
	assert.NotNil(t, cfg.Session.AsyncInitCommands)

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
	assert.NotNil(t, cfg.IssueResources.Env)
	assert.NotNil(t, cfg.IssueResources.PrepareCommands)
	assert.NotNil(t, cfg.IssueResources.FailedStartCleanupCommands)
	assert.NotNil(t, cfg.IssueResources.CleanupCommands)
	assert.Empty(t, cfg.IssueResources.ReconcileCommand)

	assert.True(t, cfg.Spec.Enabled)
	assert.Equal(t, "az", cfg.Orchestration.Via)
	assert.False(t, cfg.Diagnostics.LatencyTrace)
	assert.False(t, cfg.GitHooks.SpecSync.Enabled)
	assert.Empty(t, cfg.GitHooks.SpecSync.Command)
	assert.False(t, cfg.GitHooks.BoundaryCheck.Enabled)
	assert.Empty(t, cfg.GitHooks.BoundaryCheck.Command)
	assert.True(t, cfg.GitHooks.BestEffort)
	assert.NotNil(t, cfg.GitHooks.Commands)
	assert.False(t, cfg.GitHooks.Restage.Enabled)
	assert.NotNil(t, cfg.GitHooks.Restage.Paths)
	assert.False(t, cfg.Session.CodexAppServer)
}

func TestLoadConfigReadsProjectGateCommand(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$version":11,"gate":{"command":"npm run review","environmentFingerprint":"node-22-lock-a1"}}`)
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, "npm run review", cfg.Gate.Command)
	assert.Equal(t, "node-22-lock-a1", cfg.Gate.EnvironmentFingerprint)
}

func TestLoadConfigReadsPortableGateFailureArtifactPaths(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$version":11,"gate":{"command":"npm test","failureArtifactPaths":["build/test-results","coverage/raw"]}}`)
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"build/test-results", "coverage/raw"}, cfg.Gate.FailureArtifactPaths)
}

func TestLoadConfigReadsConsumerNeutralGateStageDAG(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$version":11,"gate":{"stages":[{"id":"lint","command":"acme-check","resources":["workspace"]},{"id":"package","command":"acme-pack","dependsOn":["lint"],"artifactPaths":["out/report"]}]}}`)
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Gate.Stages, 2)
	assert.Equal(t, []string{"lint"}, cfg.Gate.Stages[1].DependsOn)
}

func TestLoadConfigRejectsAbsentGateStageCapability(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$version":11,"gate":{"stages":[{"id":"package","command":"acme-pack","dependsOn":["missing"]}]}}`)
	_, err := LoadConfig(root)
	require.ErrorContains(t, err, `depends on unknown stage "missing"`)
}

func TestLoadConfigRejectsGateStageCycles(t *testing.T) {
	for _, stages := range []string{
		`[{"id":"self","command":"acme-check","dependsOn":["self"]}]`,
		`[{"id":"independent","command":"acme-safe"},{"id":"one","command":"acme-one","dependsOn":["two"]},{"id":"two","command":"acme-two","dependsOn":["one"]}]`,
	} {
		root := t.TempDir()
		writeConfigFile(t, root, `{"$version":11,"gate":{"stages":`+stages+`}}`)
		_, err := LoadConfig(root)
		require.ErrorContains(t, err, "gate stage graph contains a cycle")
	}
}

func TestLoadConfigRejectsUnsafeGateFailureArtifactPaths(t *testing.T) {
	for _, path := range []string{"", ".", "../outside", "/absolute"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			body, err := json.Marshal(map[string]any{"$version": CurrentConfigVersion, "gate": map[string]any{"failureArtifactPaths": []string{path}}})
			require.NoError(t, err)
			writeConfigFile(t, root, string(body))
			_, err = LoadConfig(root)
			require.ErrorContains(t, err, "gate.failureArtifactPaths[0]")
		})
	}
}

func TestLoadConfigEnablesCodexAppServer(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{"$schema":"./config.schema.json","$version":11,"cliTool":"codex","session":{"codexAppServer":true}}`)
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.True(t, cfg.Session.CodexAppServer)
}

func TestConfigSchemaVersionMatchesCurrentConfigVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "config.schema.json"))
	require.NoError(t, err)

	var schema struct {
		Properties map[string]struct {
			Const int `json:"const"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(data, &schema))
	require.Contains(t, schema.Properties, "$version")
	assert.Equal(t, CurrentConfigVersion, schema.Properties["$version"].Const)
}

func TestConfigSchemaDoesNotDefaultToAzedarachDogfoodCommands(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "config.schema.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"default": "just`)
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
    "timeoutMs": 60000,
    "asyncInitCommands": ["pnpm type-check"]
  },
  "worktree": {
    "initCommands": ["direnv allow"],
    "syncInitCommands": ["cp .env.local.example .env.local"],
    "asyncInitCommands": ["bun install"]
  },
  "issueResources": {
    "env": {"DATABASE_URL": "postgres://localhost/az_$AZEDARACH_ISSUE_ID"},
    "prepareCommands": ["just db-prepare"],
    "failedStartCleanupCommands": ["just db-cleanup-failed"],
    "cleanupCommands": ["just db-cleanup"],
    "reconcileCommand": "just resource-reconcile"
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
	assert.Empty(t, cfg.Session.SyncInitCommands)
	assert.Equal(t, []string{"pnpm type-check"}, cfg.Session.AsyncInitCommands)
	assert.Empty(t, cfg.Worktree.InitCommands)
	assert.Equal(t, []string{"direnv allow", "cp .env.local.example .env.local"}, cfg.Worktree.SyncInitCommands)
	assert.Equal(t, []string{"bun install"}, cfg.Worktree.AsyncInitCommands)
	assert.Equal(t, "postgres://localhost/az_$AZEDARACH_ISSUE_ID", cfg.IssueResources.Env["DATABASE_URL"])
	assert.Equal(t, []string{"just db-prepare"}, cfg.IssueResources.PrepareCommands)
	assert.Equal(t, []string{"just db-cleanup-failed"}, cfg.IssueResources.FailedStartCleanupCommands)
	assert.Equal(t, []string{"just db-cleanup"}, cfg.IssueResources.CleanupCommands)
	assert.Equal(t, "just resource-reconcile", cfg.IssueResources.ReconcileCommand)
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
	    "syncInitCommands": ["session one", "session two"],
	    "asyncInitCommands": ["session async"]
	  },
  "worktree": {
    "initCommands": ["worktree one"],
    "syncInitCommands": ["worktree sync"],
    "asyncInitCommands": ["worktree async"]
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"session one", "session two"}, cfg.Session.SyncInitCommands)
	assert.Equal(t, []string{"session async"}, cfg.Session.AsyncInitCommands)
	assert.Empty(t, cfg.Worktree.InitCommands)
	assert.Equal(t, []string{"worktree one", "worktree sync"}, cfg.Worktree.SyncInitCommands)
	assert.Equal(t, []string{"worktree async"}, cfg.Worktree.AsyncInitCommands)
}

func TestNormalizeConfigMigratesSessionInitCommandsToSyncAndAsyncInitCommands(t *testing.T) {
	raw := map[string]any{
		"$version": float64(9),
		"session": map[string]any{
			"initCommands":       []any{"legacy sync one", "legacy sync two"},
			"syncInitCommands":   []any{"explicit sync"},
			"sideEffectCommands": []any{"legacy async"},
			"asyncInitCommands":  []any{"explicit async"},
		},
	}

	NormalizeConfigFileRaw(raw)

	sessionRaw, ok := raw["session"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, sessionRaw, "initCommands")
	assert.NotContains(t, sessionRaw, "sideEffectCommands")
	assert.Equal(t, []any{"legacy sync one", "legacy sync two", "explicit sync"}, sessionRaw["syncInitCommands"])
	assert.Equal(t, []any{"legacy async", "explicit async"}, sessionRaw["asyncInitCommands"])
	assert.Equal(t, CurrentConfigVersion, raw["$version"])
}

func TestNormalizeConfigMigratesWorktreeInitCommandsToSyncInitCommands(t *testing.T) {
	raw := map[string]any{
		"$version": float64(8),
		"worktree": map[string]any{
			"initCommands":      []any{"legacy one", "legacy two"},
			"syncInitCommands":  []any{"sync one"},
			"asyncInitCommands": []any{"async one"},
		},
	}

	NormalizeConfigFileRaw(raw)

	worktreeRaw, ok := raw["worktree"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, worktreeRaw, "initCommands")
	assert.Equal(t, []any{"legacy one", "legacy two", "sync one"}, worktreeRaw["syncInitCommands"])
	assert.Equal(t, CurrentConfigVersion, raw["$version"])
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
	assert.Equal(t, "last_write_wins", cfg.IssueTracker.Linear.ConflictPolicy)
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
	    "conflictPolicy": "last_write_wins",
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

func TestLoadConfigWorktreeLocalOverridesBaseRepositoryConfig(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "internal", "config")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))

	writeConfigFile(t, repo, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "cliTool": "claude",
  "git": {
    "baseBranch": "main"
  },
  "session": {
    "shell": "bash"
  }
}`)
	writeLocalConfigFile(t, worktree, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "cliTool": "codex",
  "git": {
    "baseBranch": "local"
  }
}`)

	t.Setenv("PATH", "")
	cfg, err := LoadConfig(nested)
	require.NoError(t, err)

	assert.Equal(t, "codex", cfg.CLITool)
	assert.Equal(t, "local", cfg.Git.BaseBranch)
	assert.Equal(t, "bash", cfg.Session.Shell)
}

func TestLoadConfigLocalOverridesWorkspaceConfig(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "cliTool": "claude",
  "git": {
    "baseBranch": "develop",
    "pushEnabled": true
  },
  "session": {
    "shell": "bash"
  }
}`)
	writeLocalConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "cliTool": "codex",
  "git": {
    "baseBranch": "local",
    "pushEnabled": false
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)

	assert.Equal(t, "codex", cfg.CLITool)
	assert.Equal(t, "local", cfg.Git.BaseBranch)
	assert.False(t, cfg.Git.PushEnabled)
	assert.Equal(t, "bash", cfg.Session.Shell)
}

func TestLoadConfigDeepMergesObjectsAcrossWorkspaceAndLocal(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "devServer": {
    "environments": {
      "NODE_ENV": "development",
      "SHARED": "workspace"
    }
  },
  "issueTracker": {
    "linear": {
      "team": "AZD",
      "webhooks": {
        "transport": "cli"
      }
    }
  }
}`)
	writeLocalConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "devServer": {
    "environments": {
      "LOCAL_ONLY": "true",
      "SHARED": "local"
    }
  },
  "issueTracker": {
    "linear": {
      "project": "Workbench",
      "webhooks": {
        "enabled": true
      }
    }
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)

	assert.Equal(t, "development", cfg.DevServer.Environments["NODE_ENV"])
	assert.Equal(t, "true", cfg.DevServer.Environments["LOCAL_ONLY"])
	assert.Equal(t, "local", cfg.DevServer.Environments["SHARED"])
	assert.Equal(t, "AZD", cfg.IssueTracker.Linear.Team)
	assert.Equal(t, "Workbench", cfg.IssueTracker.Linear.Project)
	assert.Equal(t, "cli", cfg.IssueTracker.Linear.Webhooks.Transport)
	assert.True(t, cfg.IssueTracker.Linear.Webhooks.Enabled)
}

func TestLoadConfigLocalArraysReplaceWorkspaceArrays(t *testing.T) {
	root := t.TempDir()
	// Keep these fixtures on an older config version to verify legacy session
	// init keys migrate before higher-priority local arrays replace them.
	writeConfigFile(t, root, `{
	  "$schema": "./config.schema.json",
	  "$version": 7,
	  "session": {
	    "initCommands": ["workspace one", "workspace two"],
	    "sideEffectCommands": ["workspace async one", "workspace async two"]
	  },
  "issueTracker": {
    "linear": {
      "webhooks": {
        "events": ["Issue", "Comment"]
      }
    }
  }
}`)
	writeLocalConfigFile(t, root, `{
	  "$schema": "./config.schema.json",
	  "$version": 7,
	  "session": {
	    "initCommands": ["local one"],
	    "sideEffectCommands": ["local async one"]
	  },
  "issueTracker": {
    "linear": {
      "webhooks": {
        "events": ["Issue"]
      }
    }
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"local one"}, cfg.Session.SyncInitCommands)
	assert.Equal(t, []string{"local async one"}, cfg.Session.AsyncInitCommands)
	assert.Equal(t, []string{"Issue"}, cfg.IssueTracker.Linear.Webhooks.Events)
}

func TestLoadConfigMissingWorkspaceConfigWithLocalConfig(t *testing.T) {
	root := t.TempDir()
	writeLocalConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "cliTool": "opencode",
  "git": {
    "baseBranch": "local-only"
  }
}`)
	nested := filepath.Join(root, "internal", "config")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	cfg, err := LoadConfig(nested)
	require.NoError(t, err)

	assert.Equal(t, "opencode", cfg.CLITool)
	assert.Equal(t, "local-only", cfg.Git.BaseBranch)
	assert.Equal(t, "worktree", cfg.Git.WorkflowMode)
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
	assert.Equal(t, defaults.Orchestration.Via, cfg.Orchestration.Via)
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

func TestLoadConfigAcceptsCurrentVersion(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, fmt.Sprintf(`{"$schema":"./config.schema.json","$version":%d}`, CurrentConfigVersion))

	_, err := LoadConfig(root)
	require.NoError(t, err)
}

func TestLoadConfigRejectsFutureVersionInLocalConfigWithPath(t *testing.T) {
	root := t.TempDir()
	writeLocalConfigFile(t, root, `{"$schema":"./config.schema.json","$version":999}`)

	_, err := LoadConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported config version")
	assert.Contains(t, err.Error(), ".azedarach/config.local.json")
}

func TestLoadConfigInvalidLocalJSONReturnsErrorWithPath(t *testing.T) {
	root := t.TempDir()
	writeLocalConfigFile(t, root, `{"$schema":"./config.schema.json",`)

	_, err := LoadConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".azedarach/config.local.json")
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
	cfg.Diagnostics.LatencyTrace = true
	cfg.Session.InitCommands = []string{"legacy session sync"}
	cfg.Session.SideEffectCommands = []string{"legacy session async"}
	cfg.Session.SyncInitCommands = []string{"explicit session sync"}
	cfg.Session.AsyncInitCommands = []string{"explicit session async"}
	cfg.Worktree.InitCommands = []string{"legacy worktree init"}
	cfg.Worktree.SyncInitCommands = []string{"explicit worktree sync"}

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
	assert.NotContains(t, sessionRaw, "initCommands")
	assert.NotContains(t, sessionRaw, "sideEffectCommands")
	sessionSyncInitRaw, ok := sessionRaw["syncInitCommands"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"legacy session sync", "explicit session sync"}, sessionSyncInitRaw)
	sessionAsyncInitRaw, ok := sessionRaw["asyncInitCommands"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"legacy session async", "explicit session async"}, sessionAsyncInitRaw)

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

	diagnosticsRaw, ok := raw["diagnostics"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, diagnosticsRaw["latencyTrace"])

	worktreeRaw, ok := raw["worktree"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, worktreeRaw, "initCommands")
	syncInitRaw, ok := worktreeRaw["syncInitCommands"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"legacy worktree init", "explicit worktree sync"}, syncInitRaw)
	asyncInitRaw, ok := worktreeRaw["asyncInitCommands"].([]any)
	require.True(t, ok)
	require.Len(t, asyncInitRaw, 0)
}

func TestSaveConfigMigratesRetiredIssueAutoFinalizeSetting(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeConfigFile(t, root, `{
  "$schema": "./config.schema.json",
  "$version": 7,
  "issues": {
    "path": ".azedarach",
    "syncInterval": 120,
    "autoFinalizeOnClose": true
  }
}`)

	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, ".azedarach", cfg.Issues.Path)
	assert.Equal(t, 120, cfg.Issues.SyncInterval)

	require.NoError(t, SaveConfig(cfg, cfgPath))

	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, float64(CurrentConfigVersion), raw["$version"])

	issuesRaw, ok := raw["issues"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, issuesRaw, "autoFinalizeOnClose")
	assert.Equal(t, ".azedarach", issuesRaw["path"])
	assert.Equal(t, float64(120), issuesRaw["syncInterval"])
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

func TestSessionLogDirForUsesConfiguredGlobalLogDir(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	cfg := DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")

	got := SessionLogDirFor(cfg, t.TempDir())

	assert.Equal(t, cfg.Session.LogDir, got)
}

func TestSessionLogDirForExpandsTildeConfiguredGlobalLogDir(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	worktree := filepath.Join(t.TempDir(), "repo-worktree")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	cfg := DefaultConfig()
	cfg.Session.LogDir = "~/.azedarach/logs"

	got := SessionLogDirFor(cfg, worktree)

	assert.Equal(t, filepath.Join(homeDir, ".azedarach", "logs"), got)
}

func TestSessionLogDirForUsesWorktreeLocalDirForScopedRuntime(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "nested")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644))
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	t.Setenv("PATH", "")
	cfg := DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")

	got := SessionLogDirFor(cfg, nested)

	assert.Equal(t, filepath.Join(worktree, ".azedarach"), got)
}
