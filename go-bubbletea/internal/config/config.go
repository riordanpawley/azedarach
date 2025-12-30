package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config represents the full Azedarach configuration
type Config struct {
	CLITool       string          `json:"cliTool"`
	Git           GitConfig       `json:"git"`
	Session       SessionConfig   `json:"session"`
	PR            PRConfig        `json:"pr"`
	Merge         MergeConfig     `json:"merge"`
	Notifications NotifyConfig    `json:"notifications"`
	Beads         BeadsConfig     `json:"beads"`
	Network       NetworkConfig   `json:"network"`
	DevServer     DevServerConfig `json:"devServer"`
	Worktree      WorktreeConfig  `json:"worktree"`
}

// GitConfig contains Git-related settings
type GitConfig struct {
	BaseBranch           string `json:"baseBranch"`
	WorkflowMode         string `json:"workflowMode"`
	ShowLineChanges      bool   `json:"showLineChanges"`
	DefaultMergeStrategy string `json:"defaultMergeStrategy"`
}

// SessionConfig contains session management settings
type SessionConfig struct {
	Shell        string   `json:"shell"`
	TimeoutMs    int      `json:"timeoutMs"`
	LogDir       string   `json:"logDir"`
	InitCommands []string `json:"initCommands"`
}

// PRConfig contains pull request settings
type PRConfig struct {
	DraftByDefault     bool `json:"draftByDefault"`
	AutoLink           bool `json:"autoLink"`
	NotifyAfterCreate  bool `json:"notifyAfterCreate"`
	CreateWithoutMerge bool `json:"createWithoutMerge"`
}

// MergeConfig contains merge strategy settings
type MergeConfig struct {
	Strategy          string `json:"strategy"`
	AutoMerge         bool   `json:"autoMerge"`
	CompareWithOrigin bool   `json:"compareWithOrigin"`
}

// NotifyConfig contains notification settings
type NotifyConfig struct {
	CompletedTask  bool `json:"completedTask"`
	FailedTask     bool `json:"failedTask"`
	ErrorThreshold int  `json:"errorThreshold"`
}

// BeadsConfig contains beads task tracking settings
type BeadsConfig struct {
	Path         string `json:"path"`
	SyncInterval int    `json:"syncInterval"`
}

// NetworkConfig contains network-related settings
type NetworkConfig struct {
	CheckInterval  int `json:"checkInterval"`
	OfflineTimeout int `json:"offlineTimeout"`
	RetryAttempts  int `json:"retryAttempts"`
}

// DevServerConfig contains development server settings
type DevServerConfig struct {
	BasePort     int               `json:"basePort"`
	MaxPort      int               `json:"maxPort"`
	Environments map[string]string `json:"environments"`
}

// WorktreeConfig contains git worktree settings
type WorktreeConfig struct {
	BasePath    string `json:"basePath"`
	NameFormat  string `json:"nameFormat"`
	AutoCleanup bool   `json:"autoCleanup"`
	KeepDays    int    `json:"keepDays"`
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()

	return &Config{
		CLITool: "claude",
		Git: GitConfig{
			BaseBranch:           "main",
			WorkflowMode:         "worktree",
			ShowLineChanges:      true,
			DefaultMergeStrategy: "merge",
		},
		Session: SessionConfig{
			Shell:        "zsh",
			TimeoutMs:    30000,
			LogDir:       filepath.Join(homeDir, ".azedarach", "logs"),
			InitCommands: []string{},
		},
		PR: PRConfig{
			DraftByDefault:     true,
			AutoLink:           true,
			NotifyAfterCreate:  true,
			CreateWithoutMerge: false,
		},
		Merge: MergeConfig{
			Strategy:          "merge",
			AutoMerge:         false,
			CompareWithOrigin: true,
		},
		Notifications: NotifyConfig{
			CompletedTask:  true,
			FailedTask:     true,
			ErrorThreshold: 3,
		},
		Beads: BeadsConfig{
			Path:         ".beads",
			SyncInterval: 300, // 5 minutes
		},
		Network: NetworkConfig{
			CheckInterval:  60,  // 1 minute
			OfflineTimeout: 300, // 5 minutes
			RetryAttempts:  3,
		},
		DevServer: DevServerConfig{
			BasePort:     3000,
			MaxPort:      3100,
			Environments: make(map[string]string),
		},
		Worktree: WorktreeConfig{
			BasePath:    "../",
			NameFormat:  "{project}-{beadID}",
			AutoCleanup: true,
			KeepDays:    7,
		},
	}
}

// LoadConfig loads configuration from project path with priority:
// 1. CLI flags (not implemented yet)
// 2. .azedarach.json in project root (with version migration support)
// 3. package.json "azedarach" key
// 4. Defaults
func LoadConfig(projectPath string) (*Config, error) {
	// Start with defaults
	defaultCfg := DefaultConfig()

	// Try loading from .azedarach.json with version migration
	azedarachPath := filepath.Join(projectPath, ".azedarach.json")
	if data, err := os.ReadFile(azedarachPath); err == nil {
		cfg, err := ParseVersionedConfig(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse .azedarach.json: %w", err)
		}
		return MergeWithDefaults(cfg), nil
	}

	// Try loading from package.json
	packagePath := filepath.Join(projectPath, "package.json")
	if data, err := os.ReadFile(packagePath); err == nil {
		var packageJSON struct {
			Azedarach json.RawMessage `json:"azedarach"`
		}
		if err := json.Unmarshal(data, &packageJSON); err == nil && packageJSON.Azedarach != nil {
			// Parse with version migration support
			cfg, err := ParseVersionedConfig(packageJSON.Azedarach)
			if err != nil {
				// Fall back to direct parsing for backwards compat
				var cfgDirect Config
				if err := json.Unmarshal(packageJSON.Azedarach, &cfgDirect); err == nil {
					return MergeWithDefaults(&cfgDirect), nil
				}
				return nil, fmt.Errorf("failed to parse package.json azedarach config: %w", err)
			}
			return MergeWithDefaults(cfg), nil
		}
	}

	// Return defaults if no config files found
	return defaultCfg, nil
}

// SaveConfig saves configuration to the specified path with version information
func SaveConfig(cfg *Config, path string) error {
	data, err := MarshalVersionedConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// MergeWithDefaults fills in missing values with defaults
func MergeWithDefaults(cfg *Config) *Config {
	defaults := DefaultConfig()

	// Merge CLITool
	if cfg.CLITool == "" {
		cfg.CLITool = defaults.CLITool
	}

	// Merge Git config
	if cfg.Git.BaseBranch == "" {
		cfg.Git.BaseBranch = defaults.Git.BaseBranch
	}
	if cfg.Git.WorkflowMode == "" {
		cfg.Git.WorkflowMode = defaults.Git.WorkflowMode
	}
	if cfg.Git.DefaultMergeStrategy == "" {
		cfg.Git.DefaultMergeStrategy = defaults.Git.DefaultMergeStrategy
	}

	// Merge Session config
	if cfg.Session.Shell == "" {
		cfg.Session.Shell = defaults.Session.Shell
	}
	if cfg.Session.TimeoutMs == 0 {
		cfg.Session.TimeoutMs = defaults.Session.TimeoutMs
	}
	if cfg.Session.LogDir == "" {
		cfg.Session.LogDir = defaults.Session.LogDir
	}
	if cfg.Session.InitCommands == nil {
		cfg.Session.InitCommands = defaults.Session.InitCommands
	}

	// Merge Merge config
	if cfg.Merge.Strategy == "" {
		cfg.Merge.Strategy = defaults.Merge.Strategy
	}

	// Merge Beads config
	if cfg.Beads.Path == "" {
		cfg.Beads.Path = defaults.Beads.Path
	}
	if cfg.Beads.SyncInterval == 0 {
		cfg.Beads.SyncInterval = defaults.Beads.SyncInterval
	}

	// Merge Network config
	if cfg.Network.CheckInterval == 0 {
		cfg.Network.CheckInterval = defaults.Network.CheckInterval
	}
	if cfg.Network.OfflineTimeout == 0 {
		cfg.Network.OfflineTimeout = defaults.Network.OfflineTimeout
	}
	if cfg.Network.RetryAttempts == 0 {
		cfg.Network.RetryAttempts = defaults.Network.RetryAttempts
	}

	// Merge DevServer config
	if cfg.DevServer.BasePort == 0 {
		cfg.DevServer.BasePort = defaults.DevServer.BasePort
	}
	if cfg.DevServer.MaxPort == 0 {
		cfg.DevServer.MaxPort = defaults.DevServer.MaxPort
	}
	if cfg.DevServer.Environments == nil {
		cfg.DevServer.Environments = defaults.DevServer.Environments
	}

	// Merge Worktree config
	if cfg.Worktree.BasePath == "" {
		cfg.Worktree.BasePath = defaults.Worktree.BasePath
	}
	if cfg.Worktree.NameFormat == "" {
		cfg.Worktree.NameFormat = defaults.Worktree.NameFormat
	}
	if cfg.Worktree.KeepDays == 0 {
		cfg.Worktree.KeepDays = defaults.Worktree.KeepDays
	}

	// Merge Notifications config
	if cfg.Notifications.ErrorThreshold == 0 {
		cfg.Notifications.ErrorThreshold = defaults.Notifications.ErrorThreshold
	}

	return cfg
}

// Load is a convenience function that loads config from current directory
func Load() (*Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	return LoadConfig(cwd)
}
