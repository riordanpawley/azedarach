package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the full Azedarach configuration
type Config struct {
	CLITool       string          `json:"cliTool"`
	Git           GitConfig       `json:"git"`
	Keyboard      KeyboardConfig  `json:"keyboard"`
	Session       SessionConfig   `json:"session"`
	PR            PRConfig        `json:"pr"`
	Merge         MergeConfig     `json:"merge"`
	Notifications NotifyConfig    `json:"notifications"`
	Issues        IssuesConfig    `json:"issues"`
	Network       NetworkConfig   `json:"network"`
	DevServer     DevServerConfig `json:"devServer"`
	Worktree      WorktreeConfig  `json:"worktree"`
	Spec          SpecConfig      `json:"spec"`
}

// GitConfig contains Git-related settings
type GitConfig struct {
	BaseBranch           string `json:"baseBranch"`
	WorkflowMode         string `json:"workflowMode"`
	ShowLineChanges      bool   `json:"showLineChanges"`
	DefaultMergeStrategy string `json:"defaultMergeStrategy"`
}

// KeyboardConfig contains keyboard-related settings
type KeyboardConfig struct {
	JumpLabelChars string `json:"jumpLabelChars"`
}

// SessionConfig contains session management settings
type SessionConfig struct {
	DangerouslySkipPermissions bool     `json:"dangerouslySkipPermissions"`
	Shell                      string   `json:"shell"`
	TimeoutMs                  int      `json:"timeoutMs"`
	LogDir                     string   `json:"logDir"`
	InitCommands               []string `json:"initCommands"`
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

// IssuesConfig contains issue store settings (legacy key name retained for compatibility)
type IssuesConfig struct {
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
	BasePath     string   `json:"basePath"`
	NameFormat   string   `json:"nameFormat"`
	AutoCleanup  bool     `json:"autoCleanup"`
	KeepDays     int      `json:"keepDays"`
	InitCommands []string `json:"initCommands"`
}

type SpecConfig struct {
	Enabled bool `json:"enabled"`
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
		Keyboard: KeyboardConfig{
			JumpLabelChars: "abcdefghijklmnopqrstuvwxyz",
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
		Issues: IssuesConfig{
			Path:         ".azedarach",
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
			BasePath:     "../",
			NameFormat:   "{project}-{issueID}",
			AutoCleanup:  true,
			KeepDays:     7,
			InitCommands: []string{},
		},
		Spec: SpecConfig{
			Enabled: true,
		},
	}
}

const (
	ConfigDirName        = ".azedarach"
	ConfigFileName       = "config.json"
	ConfigSchemaFileName = "config.schema.json"
	CurrentConfigVersion = 7
)

type configFileMetadata struct {
	Schema  string `json:"$schema,omitempty"`
	Version int    `json:"$version,omitempty"`
}

// LoadConfig loads configuration from the nearest project/worktree base containing
// .azedarach/config.json. If no config file exists, defaults are returned.
func LoadConfig(projectPath string) (*Config, error) {
	baseDir, err := ResolveConfigBase(projectPath)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(baseDir, ConfigDirName, ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	var meta configFileMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse %s metadata: %w", configPath, err)
	}
	if meta.Version > CurrentConfigVersion {
		return nil, fmt.Errorf("unsupported config version %d in %s (max supported %d)", meta.Version, configPath, CurrentConfigVersion)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", configPath, err)
	}

	// ts-opentui stores init commands under worktree.initCommands; map for Go runtime.
	if len(cfg.Worktree.InitCommands) > 0 {
		cfg.Session.InitCommands = append([]string(nil), cfg.Worktree.InitCommands...)
	}

	return MergeWithDefaults(cfg), nil
}

// SaveConfig saves configuration to the specified path with version information
func SaveConfig(cfg *Config, path string) error {
	cfgCopy := *cfg
	if len(cfgCopy.Session.InitCommands) > 0 {
		cfgCopy.Worktree.InitCommands = append([]string(nil), cfgCopy.Session.InitCommands...)
	}
	data, err := marshalConfigFile(&cfgCopy)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

func marshalConfigFile(cfg *Config) ([]byte, error) {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(cfgJSON, &raw); err != nil {
		return nil, err
	}
	raw["$schema"] = "./" + ConfigSchemaFileName
	raw["$version"] = CurrentConfigVersion
	return json.MarshalIndent(raw, "", "  ")
}

func ResolveConfigBase(startPath string) (string, error) {
	if strings.TrimSpace(startPath) == "" {
		var err error
		startPath, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to resolve cwd: %w", err)
		}
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve config base from %q: %w", startPath, err)
	}
	info, err := os.Stat(abs)
	if err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}

	if baseRoot, err := ResolveBaseGitRoot(abs); err == nil {
		return baseRoot, nil
	}

	// Fallback for non-git test/directories.
	dir := abs
	for {
		configPath := filepath.Join(dir, ConfigDirName, ConfigFileName)
		if _, err := os.Stat(configPath); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		dir = parent
	}
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
	if cfg.Keyboard.JumpLabelChars == "" {
		cfg.Keyboard.JumpLabelChars = defaults.Keyboard.JumpLabelChars
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

	// Merge issue store config (legacy field name)
	if cfg.Issues.Path == "" {
		cfg.Issues.Path = defaults.Issues.Path
	}
	if cfg.Issues.SyncInterval == 0 {
		cfg.Issues.SyncInterval = defaults.Issues.SyncInterval
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
	if cfg.Worktree.InitCommands == nil {
		cfg.Worktree.InitCommands = defaults.Worktree.InitCommands
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
