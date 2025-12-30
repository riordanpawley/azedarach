package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Test basic defaults
	assert.Equal(t, "claude", cfg.CLITool)
	assert.Equal(t, "main", cfg.Git.BaseBranch)
	assert.Equal(t, "worktree", cfg.Git.WorkflowMode)
	assert.True(t, cfg.Git.ShowLineChanges)
	assert.Equal(t, "merge", cfg.Git.DefaultMergeStrategy)

	// Test session defaults
	assert.Equal(t, "zsh", cfg.Session.Shell)
	assert.Equal(t, 30000, cfg.Session.TimeoutMs)
	assert.NotEmpty(t, cfg.Session.LogDir)
	assert.NotNil(t, cfg.Session.InitCommands)

	// Test PR defaults
	assert.True(t, cfg.PR.DraftByDefault)
	assert.True(t, cfg.PR.AutoLink)
	assert.True(t, cfg.PR.NotifyAfterCreate)
	assert.False(t, cfg.PR.CreateWithoutMerge)

	// Test merge defaults
	assert.Equal(t, "merge", cfg.Merge.Strategy)
	assert.False(t, cfg.Merge.AutoMerge)
	assert.True(t, cfg.Merge.CompareWithOrigin)

	// Test notifications defaults
	assert.True(t, cfg.Notifications.CompletedTask)
	assert.True(t, cfg.Notifications.FailedTask)
	assert.Equal(t, 3, cfg.Notifications.ErrorThreshold)

	// Test beads defaults
	assert.Equal(t, ".beads", cfg.Beads.Path)
	assert.Equal(t, 300, cfg.Beads.SyncInterval)

	// Test network defaults
	assert.Equal(t, 60, cfg.Network.CheckInterval)
	assert.Equal(t, 300, cfg.Network.OfflineTimeout)
	assert.Equal(t, 3, cfg.Network.RetryAttempts)

	// Test dev server defaults
	assert.Equal(t, 3000, cfg.DevServer.BasePort)
	assert.Equal(t, 3100, cfg.DevServer.MaxPort)
	assert.NotNil(t, cfg.DevServer.Environments)

	// Test worktree defaults
	assert.Equal(t, "../", cfg.Worktree.BasePath)
	assert.Equal(t, "{project}-{beadID}", cfg.Worktree.NameFormat)
	assert.True(t, cfg.Worktree.AutoCleanup)
	assert.Equal(t, 7, cfg.Worktree.KeepDays)
}

func TestLoadConfigFromAzedarachJSON(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create .azedarach.json with partial config
	configContent := `{
  "cliTool": "opencode",
  "git": {
    "baseBranch": "develop"
  },
  "session": {
    "shell": "bash",
    "timeoutMs": 60000
  },
  "devServer": {
    "basePort": 4000,
    "environments": {
      "NODE_ENV": "development",
      "DEBUG": "true"
    }
  }
}`
	configPath := filepath.Join(tmpDir, ".azedarach.json")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0644))

	// Load config
	cfg, err := LoadConfig(tmpDir)
	require.NoError(t, err)

	// Check custom values
	assert.Equal(t, "opencode", cfg.CLITool)
	assert.Equal(t, "develop", cfg.Git.BaseBranch)
	assert.Equal(t, "bash", cfg.Session.Shell)
	assert.Equal(t, 60000, cfg.Session.TimeoutMs)
	assert.Equal(t, 4000, cfg.DevServer.BasePort)
	assert.Equal(t, "development", cfg.DevServer.Environments["NODE_ENV"])
	assert.Equal(t, "true", cfg.DevServer.Environments["DEBUG"])

	// Check defaults are filled in
	assert.Equal(t, "worktree", cfg.Git.WorkflowMode)
	assert.Equal(t, 3100, cfg.DevServer.MaxPort)
	assert.Equal(t, ".beads", cfg.Beads.Path)
}

func TestLoadConfigFromPackageJSON(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create package.json with azedarach config
	packageContent := `{
  "name": "my-project",
  "version": "1.0.0",
  "azedarach": {
    "cliTool": "opencode",
    "git": {
      "baseBranch": "master",
      "workflowMode": "branch"
    },
    "pr": {
      "draftByDefault": false
    }
  }
}`
	packagePath := filepath.Join(tmpDir, "package.json")
	require.NoError(t, os.WriteFile(packagePath, []byte(packageContent), 0644))

	// Load config
	cfg, err := LoadConfig(tmpDir)
	require.NoError(t, err)

	// Check custom values
	assert.Equal(t, "opencode", cfg.CLITool)
	assert.Equal(t, "master", cfg.Git.BaseBranch)
	assert.Equal(t, "branch", cfg.Git.WorkflowMode)
	assert.False(t, cfg.PR.DraftByDefault)

	// Check defaults are filled in
	assert.Equal(t, "zsh", cfg.Session.Shell)
	assert.Equal(t, 30000, cfg.Session.TimeoutMs)
}

func TestLoadConfigPriority(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create both .azedarach.json and package.json
	azedarachContent := `{
  "cliTool": "claude",
  "git": {
    "baseBranch": "main"
  }
}`
	packageContent := `{
  "name": "my-project",
  "azedarach": {
    "cliTool": "opencode",
    "git": {
      "baseBranch": "master"
    }
  }
}`

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".azedarach.json"), []byte(azedarachContent), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageContent), 0644))

	// Load config - should prefer .azedarach.json
	cfg, err := LoadConfig(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, "claude", cfg.CLITool)
	assert.Equal(t, "main", cfg.Git.BaseBranch)
}

func TestLoadConfigNoFiles(t *testing.T) {
	// Create empty temp directory
	tmpDir := t.TempDir()

	// Load config - should return defaults
	cfg, err := LoadConfig(tmpDir)
	require.NoError(t, err)

	// Should be same as defaults
	defaults := DefaultConfig()
	assert.Equal(t, defaults.CLITool, cfg.CLITool)
	assert.Equal(t, defaults.Git.BaseBranch, cfg.Git.BaseBranch)
	assert.Equal(t, defaults.Session.Shell, cfg.Session.Shell)
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create invalid .azedarach.json
	invalidContent := `{
  "cliTool": "claude",
  "git": {
    "baseBranch": "main"
  // missing closing brace
}`
	configPath := filepath.Join(tmpDir, ".azedarach.json")
	require.NoError(t, os.WriteFile(configPath, []byte(invalidContent), 0644))

	// Load config - should return error
	_, err := LoadConfig(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse .azedarach.json")
}

func TestSaveConfig(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.json")

	// Create custom config
	cfg := DefaultConfig()
	cfg.CLITool = "opencode"
	cfg.Git.BaseBranch = "develop"
	cfg.Session.Shell = "fish"
	cfg.DevServer.Environments = map[string]string{
		"NODE_ENV": "test",
		"PORT":     "5000",
	}

	// Save config
	err := SaveConfig(cfg, configPath)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(configPath)
	require.NoError(t, err)

	// Load it back and verify
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".azedarach.json"), data, 0644))

	reloaded, err := LoadConfig(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, "opencode", reloaded.CLITool)
	assert.Equal(t, "develop", reloaded.Git.BaseBranch)
	assert.Equal(t, "fish", reloaded.Session.Shell)
	assert.Equal(t, "test", reloaded.DevServer.Environments["NODE_ENV"])
	assert.Equal(t, "5000", reloaded.DevServer.Environments["PORT"])
}

func TestMergeWithDefaults(t *testing.T) {
	// Create partial config
	partial := &Config{
		CLITool: "opencode",
		Git: GitConfig{
			BaseBranch: "develop",
			// Other fields empty
		},
		Session: SessionConfig{
			Shell: "bash",
			// TimeoutMs not set (0)
		},
		DevServer: DevServerConfig{
			BasePort: 4000,
			// MaxPort not set (0)
		},
	}

	// Merge with defaults
	merged := MergeWithDefaults(partial)

	// Check custom values preserved
	assert.Equal(t, "opencode", merged.CLITool)
	assert.Equal(t, "develop", merged.Git.BaseBranch)
	assert.Equal(t, "bash", merged.Session.Shell)
	assert.Equal(t, 4000, merged.DevServer.BasePort)

	// Check defaults filled in
	assert.Equal(t, "worktree", merged.Git.WorkflowMode)
	assert.Equal(t, 30000, merged.Session.TimeoutMs)
	assert.Equal(t, 3100, merged.DevServer.MaxPort)
	assert.NotEmpty(t, merged.Session.LogDir)
	assert.NotNil(t, merged.Session.InitCommands)
	assert.Equal(t, ".beads", merged.Beads.Path)
}

func TestMergeWithDefaultsEmptyConfig(t *testing.T) {
	// Create completely empty config
	empty := &Config{}

	// Merge with defaults
	merged := MergeWithDefaults(empty)

	// Should be same as defaults
	defaults := DefaultConfig()
	assert.Equal(t, defaults.CLITool, merged.CLITool)
	assert.Equal(t, defaults.Git.BaseBranch, merged.Git.BaseBranch)
	assert.Equal(t, defaults.Git.WorkflowMode, merged.Git.WorkflowMode)
	assert.Equal(t, defaults.Session.Shell, merged.Session.Shell)
	assert.Equal(t, defaults.Session.TimeoutMs, merged.Session.TimeoutMs)
}

func TestMergeWithDefaultsNilSlices(t *testing.T) {
	// Create config with nil slices
	cfg := &Config{
		Session: SessionConfig{
			Shell:        "bash",
			InitCommands: nil, // nil slice
		},
		DevServer: DevServerConfig{
			Environments: nil, // nil map
		},
	}

	// Merge with defaults
	merged := MergeWithDefaults(cfg)

	// Check nil values replaced with defaults
	assert.NotNil(t, merged.Session.InitCommands)
	assert.NotNil(t, merged.DevServer.Environments)
}

func TestLoadConfigPackageJSONWithoutAzedarach(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create package.json without azedarach config
	packageContent := `{
  "name": "my-project",
  "version": "1.0.0",
  "dependencies": {}
}`
	packagePath := filepath.Join(tmpDir, "package.json")
	require.NoError(t, os.WriteFile(packagePath, []byte(packageContent), 0644))

	// Load config - should return defaults
	cfg, err := LoadConfig(tmpDir)
	require.NoError(t, err)

	// Should be same as defaults
	defaults := DefaultConfig()
	assert.Equal(t, defaults.CLITool, cfg.CLITool)
	assert.Equal(t, defaults.Git.BaseBranch, cfg.Git.BaseBranch)
}

func TestComplexConfig(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create complex config with all fields
	complexContent := `{
  "cliTool": "opencode",
  "git": {
    "baseBranch": "develop",
    "workflowMode": "branch",
    "showLineChanges": false,
    "defaultMergeStrategy": "rebase"
  },
  "session": {
    "shell": "fish",
    "timeoutMs": 60000,
    "logDir": "/custom/logs",
    "initCommands": ["source ~/.config/fish/config.fish", "cd ~/projects"]
  },
  "pr": {
    "draftByDefault": false,
    "autoLink": false,
    "notifyAfterCreate": false,
    "createWithoutMerge": true
  },
  "merge": {
    "strategy": "squash",
    "autoMerge": true,
    "compareWithOrigin": false
  },
  "notifications": {
    "completedTask": false,
    "failedTask": false,
    "errorThreshold": 5
  },
  "beads": {
    "path": ".custom-beads",
    "syncInterval": 600
  },
  "network": {
    "checkInterval": 120,
    "offlineTimeout": 600,
    "retryAttempts": 5
  },
  "devServer": {
    "basePort": 5000,
    "maxPort": 5100,
    "environments": {
      "NODE_ENV": "production",
      "API_URL": "https://api.example.com",
      "DEBUG": "false"
    }
  },
  "worktree": {
    "basePath": "/tmp/worktrees",
    "nameFormat": "{beadID}-{project}",
    "autoCleanup": false,
    "keepDays": 30
  }
}`
	configPath := filepath.Join(tmpDir, ".azedarach.json")
	require.NoError(t, os.WriteFile(configPath, []byte(complexContent), 0644))

	// Load config
	cfg, err := LoadConfig(tmpDir)
	require.NoError(t, err)

	// Verify all fields
	assert.Equal(t, "opencode", cfg.CLITool)

	assert.Equal(t, "develop", cfg.Git.BaseBranch)
	assert.Equal(t, "branch", cfg.Git.WorkflowMode)
	assert.False(t, cfg.Git.ShowLineChanges)
	assert.Equal(t, "rebase", cfg.Git.DefaultMergeStrategy)

	assert.Equal(t, "fish", cfg.Session.Shell)
	assert.Equal(t, 60000, cfg.Session.TimeoutMs)
	assert.Equal(t, "/custom/logs", cfg.Session.LogDir)
	assert.Len(t, cfg.Session.InitCommands, 2)
	assert.Equal(t, "source ~/.config/fish/config.fish", cfg.Session.InitCommands[0])

	assert.False(t, cfg.PR.DraftByDefault)
	assert.False(t, cfg.PR.AutoLink)
	assert.False(t, cfg.PR.NotifyAfterCreate)
	assert.True(t, cfg.PR.CreateWithoutMerge)

	assert.Equal(t, "squash", cfg.Merge.Strategy)
	assert.True(t, cfg.Merge.AutoMerge)
	assert.False(t, cfg.Merge.CompareWithOrigin)

	assert.False(t, cfg.Notifications.CompletedTask)
	assert.False(t, cfg.Notifications.FailedTask)
	assert.Equal(t, 5, cfg.Notifications.ErrorThreshold)

	assert.Equal(t, ".custom-beads", cfg.Beads.Path)
	assert.Equal(t, 600, cfg.Beads.SyncInterval)

	assert.Equal(t, 120, cfg.Network.CheckInterval)
	assert.Equal(t, 600, cfg.Network.OfflineTimeout)
	assert.Equal(t, 5, cfg.Network.RetryAttempts)

	assert.Equal(t, 5000, cfg.DevServer.BasePort)
	assert.Equal(t, 5100, cfg.DevServer.MaxPort)
	assert.Equal(t, "production", cfg.DevServer.Environments["NODE_ENV"])
	assert.Equal(t, "https://api.example.com", cfg.DevServer.Environments["API_URL"])
	assert.Equal(t, "false", cfg.DevServer.Environments["DEBUG"])

	assert.Equal(t, "/tmp/worktrees", cfg.Worktree.BasePath)
	assert.Equal(t, "{beadID}-{project}", cfg.Worktree.NameFormat)
	assert.False(t, cfg.Worktree.AutoCleanup)
	assert.Equal(t, 30, cfg.Worktree.KeepDays)
}
