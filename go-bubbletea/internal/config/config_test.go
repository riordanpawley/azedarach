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
	assert.Equal(t, "main", cfg.Git.BaseBranch)
	assert.Equal(t, "worktree", cfg.Git.WorkflowMode)
	assert.True(t, cfg.Git.ShowLineChanges)
	assert.Equal(t, "merge", cfg.Git.DefaultMergeStrategy)

	assert.Equal(t, DefaultSessionShell(), cfg.Session.Shell)
	assert.Equal(t, 30000, cfg.Session.TimeoutMs)
	assert.NotEmpty(t, cfg.Session.LogDir)
	assert.NotNil(t, cfg.Session.InitCommands)

	assert.Equal(t, 3000, cfg.DevServer.BasePort)
	assert.Equal(t, 3100, cfg.DevServer.MaxPort)
	assert.NotNil(t, cfg.DevServer.Environments)

	assert.Equal(t, "../", cfg.Worktree.BasePath)
	assert.Equal(t, "{project}-{issueID}", cfg.Worktree.NameFormat)
	assert.True(t, cfg.Worktree.AutoCleanup)
	assert.Equal(t, 7, cfg.Worktree.KeepDays)
	assert.NotNil(t, cfg.Worktree.InitCommands)

	assert.True(t, cfg.Spec.Enabled)
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
    "workflowMode": "branch"
  },
  "session": {
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
	assert.Equal(t, "develop", cfg.Git.BaseBranch)
	assert.Equal(t, "branch", cfg.Git.WorkflowMode)
	assert.Equal(t, "bash", cfg.Session.Shell)
	assert.Equal(t, 60000, cfg.Session.TimeoutMs)
	assert.Equal(t, []string{"direnv allow", "bun install"}, cfg.Session.InitCommands)
	assert.False(t, cfg.Spec.Enabled)
	assert.Equal(t, 4000, cfg.DevServer.BasePort)
	assert.Equal(t, 4100, cfg.DevServer.MaxPort)
	assert.Equal(t, "development", cfg.DevServer.Environments["NODE_ENV"])

	// Defaults still merged for unspecified fields.
	assert.Equal(t, ".azedarach", cfg.Issues.Path)
	assert.Equal(t, 300, cfg.Issues.SyncInterval)
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
	path := filepath.Join(tmp, ConfigFileName)

	cfg := DefaultConfig()
	cfg.CLITool = "codex"
	cfg.Spec.Enabled = false
	cfg.Session.InitCommands = []string{"direnv allow", "bun install"}

	err := SaveConfig(cfg, path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "./config.schema.json", raw["$schema"])
	assert.Equal(t, float64(CurrentConfigVersion), raw["$version"])

	worktreeRaw, ok := raw["worktree"].(map[string]any)
	require.True(t, ok)
	initRaw, ok := worktreeRaw["initCommands"].([]any)
	require.True(t, ok)
	require.Len(t, initRaw, 2)
}

func TestMergeWithDefaultsPreservesExplicitValues(t *testing.T) {
	partial := &Config{
		CLITool: "opencode",
		Git: GitConfig{
			BaseBranch: "develop",
		},
		Session:   SessionConfig{Shell: "bash"},
		DevServer: DevServerConfig{BasePort: 4000},
		Spec:      SpecConfig{Enabled: false},
	}

	merged := MergeWithDefaults(partial)

	assert.Equal(t, "opencode", merged.CLITool)
	assert.Equal(t, "develop", merged.Git.BaseBranch)
	assert.Equal(t, "bash", merged.Session.Shell)
	assert.Equal(t, 4000, merged.DevServer.BasePort)
	assert.False(t, merged.Spec.Enabled)
	assert.Equal(t, "worktree", merged.Git.WorkflowMode)
	assert.Equal(t, 30000, merged.Session.TimeoutMs)
	assert.Equal(t, 3100, merged.DevServer.MaxPort)
}
