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
	CLITool          string                 `json:"cliTool"`
	IssueTracker     IssueTrackerConfig     `json:"issueTracker"`
	Git              GitConfig              `json:"git"`
	GitHooks         GitHooksConfig         `json:"githooks"`
	Keyboard         KeyboardConfig         `json:"keyboard"`
	Session          SessionConfig          `json:"session"`
	PR               PRConfig               `json:"pr"`
	Merge            MergeConfig            `json:"merge"`
	Notifications    NotifyConfig           `json:"notifications"`
	Issues           IssuesConfig           `json:"issues"`
	Network          NetworkConfig          `json:"network"`
	DevServer        DevServerConfig        `json:"devServer"`
	Worktree         WorktreeConfig         `json:"worktree"`
	IssueResources   IssueResourcesConfig   `json:"issueResources"`
	ScheduledScripts ScheduledScriptsConfig `json:"scheduledScripts"`
	Spec             SpecConfig             `json:"spec"`
	Orchestration    OrchestrationConfig    `json:"orchestration"`
	Diagnostics      DiagnosticsConfig      `json:"diagnostics"`
}

type IssueTrackerConfig struct {
	Backend string              `json:"backend"`
	Sync    IssueSyncConfig     `json:"sync"`
	Linear  LinearTrackerConfig `json:"linear"`
}

type IssueSyncConfig struct {
	Enabled bool `json:"enabled"`
}

type LinearTrackerConfig struct {
	Command        string               `json:"command"`
	Team           string               `json:"team"`
	Project        string               `json:"project"`
	ConflictPolicy string               `json:"conflictPolicy"`
	Filter         *LinearFilterConfig  `json:"filter,omitempty"`
	Webhooks       LinearWebhooksConfig `json:"webhooks"`
}

type LinearFilterConfig struct {
	Assignee string `json:"assignee"`
}

type LinearWebhooksConfig struct {
	Enabled   bool     `json:"enabled"`
	Transport string   `json:"transport"`
	URL       string   `json:"url"`
	Port      int      `json:"port"`
	Events    []string `json:"events"`
	Secret    string   `json:"secret"`
}

// GitConfig contains Git-related settings
type GitConfig struct {
	BaseBranch           string `json:"baseBranch"`
	WorkflowMode         string `json:"workflowMode"`
	ShowLineChanges      bool   `json:"showLineChanges"`
	DefaultMergeStrategy string `json:"defaultMergeStrategy"`
	PushEnabled          bool   `json:"pushEnabled"`
	FetchEnabled         bool   `json:"fetchEnabled"`
}

// GitHooksConfig controls az-managed git hook behavior.
type GitHooksConfig struct {
	Commands   map[string][]string   `json:"commands"`
	BestEffort bool                  `json:"bestEffort"`
	Restage    GitHooksRestageConfig `json:"restage"`
	// Deprecated: use Commands["pre-commit"] and Restage instead.
	SpecSync GitHookSpecSyncConfig `json:"specSync"`
	// Deprecated: use Commands["pre-commit"] instead.
	BoundaryCheck GitHookTaskConfig `json:"boundaryCheck"`
}

type GitHooksRestageConfig struct {
	Enabled bool     `json:"enabled"`
	Paths   []string `json:"paths"`
}

type GitHookSpecSyncConfig struct {
	Enabled       bool   `json:"enabled"`
	Command       string `json:"command"`
	AutoStageDocs bool   `json:"autoStageDocs"`
}

type GitHookTaskConfig struct {
	Enabled bool   `json:"enabled"`
	Command string `json:"command"`
}

// KeyboardConfig contains keyboard-related settings
type KeyboardConfig struct {
	JumpLabelChars string `json:"jumpLabelChars"`
}

// SessionConfig contains session management settings
type SessionConfig struct {
	DangerouslySkipPermissions bool   `json:"dangerouslySkipPermissions"`
	CodexAppServer             bool   `json:"codexAppServer"`
	Shell                      string `json:"shell"`
	TimeoutMs                  int    `json:"timeoutMs"`
	LogDir                     string `json:"logDir"`
	// Deprecated: use SyncInitCommands instead.
	InitCommands []string `json:"initCommands"`
	// Deprecated: use AsyncInitCommands instead.
	SideEffectCommands []string `json:"sideEffectCommands"`
	SyncInitCommands   []string `json:"syncInitCommands"`
	AsyncInitCommands  []string `json:"asyncInitCommands"`
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
	Path         string                 `json:"path"`
	SyncInterval int                    `json:"syncInterval"`
	AutoArchive  IssueAutoArchiveConfig `json:"autoArchive"`
}

type IssueAutoArchiveConfig struct {
	Enabled         bool   `json:"enabled"`
	ClosedAfterDays int    `json:"closedAfterDays"`
	Interval        string `json:"interval"`
}

// NetworkConfig contains network-related settings
type NetworkConfig struct {
	AutoDetect     bool `json:"autoDetect"`
	CheckInterval  int  `json:"checkInterval"`
	OfflineTimeout int  `json:"offlineTimeout"`
	RetryAttempts  int  `json:"retryAttempts"`
}

// DevServerConfig contains development server settings
type DevServerConfig struct {
	BasePort     int               `json:"basePort"`
	MaxPort      int               `json:"maxPort"`
	Environments map[string]string `json:"environments"`
}

// WorktreeConfig contains git worktree settings
type WorktreeConfig struct {
	BasePath          string   `json:"basePath"`
	NameFormat        string   `json:"nameFormat"`
	AutoCleanup       bool     `json:"autoCleanup"`
	KeepDays          int      `json:"keepDays"`
	InitCommands      []string `json:"initCommands"`
	SyncInitCommands  []string `json:"syncInitCommands"`
	AsyncInitCommands []string `json:"asyncInitCommands"`
}

// IssueResourcesConfig contains opt-in issue-scoped lifecycle hooks.
type IssueResourcesConfig struct {
	Env                        map[string]string `json:"env"`
	PrepareCommands            []string          `json:"prepareCommands"`
	FailedStartCleanupCommands []string          `json:"failedStartCleanupCommands"`
	CleanupCommands            []string          `json:"cleanupCommands"`
	ReconcileCommand           string            `json:"reconcileCommand"`
}

// ScheduledScriptsConfig contains daemon-owned project maintenance scripts.
type ScheduledScriptsConfig struct {
	Env     map[string]string       `json:"env"`
	Scripts []ScheduledScriptConfig `json:"scripts"`
}

type ScheduledScriptConfig struct {
	Name         string            `json:"name"`
	Command      string            `json:"command"`
	Enabled      bool              `json:"enabled"`
	CWD          string            `json:"cwd"`
	Interval     string            `json:"interval"`
	Schedule     string            `json:"schedule"`
	TimeoutMs    int               `json:"timeoutMs"`
	AllowOverlap bool              `json:"allowOverlap"`
	Env          map[string]string `json:"env"`
}

type SpecConfig struct {
	Enabled bool `json:"enabled"`
}

type OrchestrationConfig struct {
	Via           string `json:"via"`
	CompleteGrace string `json:"completeGrace"`
	WakeDebounce  string `json:"wakeDebounce"`
}

type DiagnosticsConfig struct {
	LatencyTrace bool `json:"latencyTrace"`
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()

	return &Config{
		CLITool: "claude",
		IssueTracker: IssueTrackerConfig{
			Backend: "local",
			Sync: IssueSyncConfig{
				Enabled: false,
			},
			Linear: LinearTrackerConfig{
				Command:        "linear-cli",
				ConflictPolicy: "last_write_wins",
				Webhooks: LinearWebhooksConfig{
					Transport: "sdk",
					Events:    []string{},
				},
			},
		},
		Git: GitConfig{
			BaseBranch:           "main",
			WorkflowMode:         "worktree",
			ShowLineChanges:      true,
			DefaultMergeStrategy: "merge",
			PushEnabled:          true,
			FetchEnabled:         true,
		},
		GitHooks: GitHooksConfig{
			Commands:   map[string][]string{},
			BestEffort: true,
			Restage: GitHooksRestageConfig{
				Enabled: false,
				Paths:   []string{},
			},
			SpecSync: GitHookSpecSyncConfig{
				Enabled:       false,
				Command:       "",
				AutoStageDocs: true,
			},
			BoundaryCheck: GitHookTaskConfig{
				Enabled: false,
				Command: "",
			},
		},
		Keyboard: KeyboardConfig{
			JumpLabelChars: "abcdefghijklmnopqrstuvwxyz",
		},
		Session: SessionConfig{
			Shell:              DefaultSessionShell(),
			TimeoutMs:          30000,
			LogDir:             filepath.Join(homeDir, ".azedarach", "logs"),
			InitCommands:       []string{},
			SideEffectCommands: []string{},
			SyncInitCommands:   []string{},
			AsyncInitCommands:  []string{},
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
			AutoArchive: IssueAutoArchiveConfig{
				Enabled:         false,
				ClosedAfterDays: 30,
				Interval:        "24h",
			},
		},
		Network: NetworkConfig{
			AutoDetect:     true,
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
			BasePath:          "../",
			NameFormat:        "{project}-{issueID}",
			AutoCleanup:       true,
			KeepDays:          7,
			InitCommands:      []string{},
			SyncInitCommands:  []string{},
			AsyncInitCommands: []string{},
		},
		IssueResources: IssueResourcesConfig{
			Env:                        map[string]string{},
			PrepareCommands:            []string{},
			FailedStartCleanupCommands: []string{},
			CleanupCommands:            []string{},
			ReconcileCommand:           "",
		},
		ScheduledScripts: ScheduledScriptsConfig{
			Env:     map[string]string{},
			Scripts: []ScheduledScriptConfig{},
		},
		Spec: SpecConfig{
			Enabled: true,
		},
		Orchestration: OrchestrationConfig{
			Via:           "az",
			CompleteGrace: "5m",
			WakeDebounce:  "2s",
		},
		Diagnostics: DiagnosticsConfig{
			LatencyTrace: false,
		},
	}
}

// SessionLogDirFor returns the user-facing log directory for CLI/TUI/daemon
// process logs. Explicit worktree-scoped daemon development keeps logs local to
// that worktree so test daemons do not mingle with the production log stream.
func SessionLogDirFor(cfg *Config, startPath string) string {
	if UseScopedDaemonRuntimeFor(startPath) {
		if worktreeRoot, err := ResolveWorktreeRoot(startPath); err == nil && strings.TrimSpace(worktreeRoot) != "" {
			return filepath.Join(worktreeRoot, ".azedarach")
		}
	}

	if cfg == nil {
		cfg = DefaultConfig()
	}
	logDir := strings.TrimSpace(cfg.Session.LogDir)
	if logDir != "" {
		return expandHomeDir(logDir)
	}
	if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
		return filepath.Join(homeDir, ".azedarach", "logs")
	}
	return filepath.Join(".", ".azedarach", "logs")
}

func expandHomeDir(path string) string {
	if path == "~" {
		if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
			return homeDir
		}
		return path
	}
	for _, prefix := range []string{"~/", `~\`} {
		if strings.HasPrefix(path, prefix) {
			if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
				return filepath.Join(homeDir, filepath.FromSlash(strings.TrimPrefix(path, prefix)))
			}
			return path
		}
	}
	return path
}

func DefaultSessionShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	return "zsh"
}

const (
	ConfigDirName        = ".azedarach"
	ConfigFileName       = "config.json"
	LocalConfigFileName  = "config.local.json"
	ConfigSchemaFileName = "config.schema.json"
	ConfigSchemaURL      = "https://raw.githubusercontent.com/riordanpawley/azedarach/main/docs/config.schema.json"
	CurrentConfigVersion = 11
)

type configFileMetadata struct {
	Schema  string `json:"$schema,omitempty"`
	Version int    `json:"$version,omitempty"`
}

// LoadConfig loads configuration from the project and worktree config roots.
// Config files are layered as defaults < config.json < config.local.json, with
// linked worktree config loaded after base-repository config. If no config file
// exists, defaults are returned.
func LoadConfig(projectPath string) (*Config, error) {
	baseDirs, err := ResolveConfigLayerBases(projectPath)
	if err != nil {
		return nil, err
	}

	cfg := DefaultConfig()
	for _, baseDir := range baseDirs {
		for _, name := range []string{ConfigFileName, LocalConfigFileName} {
			configPath := filepath.Join(baseDir, ConfigDirName, name)
			if err := loadConfigLayer(cfg, configPath); err != nil {
				return nil, err
			}
		}
	}

	return MergeWithDefaults(cfg), nil
}

func loadConfigLayer(cfg *Config, configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	var meta configFileMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("failed to parse %s metadata: %w", configPath, err)
	}
	if meta.Version > CurrentConfigVersion {
		return fmt.Errorf("unsupported config version %d in %s (max supported %d)", meta.Version, configPath, CurrentConfigVersion)
	}

	data, err = NormalizeConfigFileJSON(data)
	if err != nil {
		return fmt.Errorf("failed to migrate %s: %w", configPath, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("failed to parse %s: %w", configPath, err)
	}
	return nil
}

// SaveConfig saves configuration to the specified path with version information
func SaveConfig(cfg *Config, path string) error {
	cfgCopy := *cfg
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
	NormalizeConfigFileRaw(raw)
	return json.MarshalIndent(raw, "", "  ")
}

// NormalizeConfigFileJSON applies forward-compatible config file migrations to
// a raw JSON object. It is intentionally tolerant of removed fields so old
// project configs continue to load after schema cleanup.
func NormalizeConfigFileJSON(data []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	NormalizeConfigFileRaw(raw)
	return json.Marshal(raw)
}

// NormalizeConfigFileRaw mutates a raw config JSON object to the current
// schema version while removing retired keys that should not be preserved by
// raw JSON writers.
func NormalizeConfigFileRaw(raw map[string]any) {
	if raw == nil {
		return
	}
	if issues, ok := raw["issues"].(map[string]any); ok {
		delete(issues, "autoFinalizeOnClose")
	}
	if session, ok := raw["session"].(map[string]any); ok {
		migrateSessionInitCommands(session)
	}
	if worktree, ok := raw["worktree"].(map[string]any); ok {
		migrateWorktreeInitCommands(worktree)
	}
	raw["$schema"] = ConfigSchemaURL
	raw["$version"] = CurrentConfigVersion
}

func migrateSessionInitCommands(session map[string]any) {
	migrateCommandArray(session, "initCommands", "syncInitCommands")
	migrateCommandArray(session, "sideEffectCommands", "asyncInitCommands")
}

func migrateWorktreeInitCommands(worktree map[string]any) {
	migrateCommandArray(worktree, "initCommands", "syncInitCommands")
}

func migrateCommandArray(raw map[string]any, legacyKey, currentKey string) {
	initCommands, ok := configRawArray(raw[legacyKey])
	if !ok {
		delete(raw, legacyKey)
		return
	}
	if len(initCommands) > 0 {
		currentCommands, _ := configRawArray(raw[currentKey])
		migrated := make([]any, 0, len(initCommands)+len(currentCommands))
		migrated = append(migrated, initCommands...)
		migrated = append(migrated, currentCommands...)
		raw[currentKey] = migrated
	}
	delete(raw, legacyKey)
}

func configRawArray(value any) ([]any, bool) {
	switch values := value.(type) {
	case []any:
		return values, true
	case []string:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out, true
	default:
		return nil, false
	}
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
		for _, name := range []string{ConfigFileName, LocalConfigFileName} {
			configPath := filepath.Join(dir, ConfigDirName, name)
			if _, err := os.Stat(configPath); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		dir = parent
	}
}

func ResolveConfigLayerBases(startPath string) ([]string, error) {
	if strings.TrimSpace(startPath) == "" {
		var err error
		startPath, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve cwd: %w", err)
		}
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config base from %q: %w", startPath, err)
	}
	if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}

	if baseRoot, err := ResolveBaseGitRoot(abs); err == nil {
		bases := []string{baseRoot}
		if worktreeRoot, wtErr := ResolveWorktreeRoot(abs); wtErr == nil && !samePath(worktreeRoot, baseRoot) {
			bases = append(bases, worktreeRoot)
		}
		return bases, nil
	}

	base, err := ResolveConfigBase(abs)
	if err != nil {
		return nil, err
	}
	return []string{base}, nil
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	cleanA, errA := filepath.Abs(a)
	cleanB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		a = cleanA
		b = cleanB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// MergeWithDefaults fills in missing values with defaults
func MergeWithDefaults(cfg *Config) *Config {
	defaults := DefaultConfig()

	// Merge CLITool
	if cfg.CLITool == "" {
		cfg.CLITool = defaults.CLITool
	}
	if cfg.IssueTracker.Backend == "" {
		cfg.IssueTracker.Backend = defaults.IssueTracker.Backend
	}
	if cfg.IssueTracker.Linear.Command == "" {
		cfg.IssueTracker.Linear.Command = defaults.IssueTracker.Linear.Command
	}
	if cfg.IssueTracker.Linear.Webhooks.Transport == "" {
		cfg.IssueTracker.Linear.Webhooks.Transport = defaults.IssueTracker.Linear.Webhooks.Transport
	}
	if cfg.IssueTracker.Linear.Webhooks.Events == nil {
		cfg.IssueTracker.Linear.Webhooks.Events = defaults.IssueTracker.Linear.Webhooks.Events
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
	if cfg.Session.SideEffectCommands == nil {
		cfg.Session.SideEffectCommands = defaults.Session.SideEffectCommands
	}
	if cfg.Session.SyncInitCommands == nil {
		cfg.Session.SyncInitCommands = defaults.Session.SyncInitCommands
	}
	if cfg.Session.AsyncInitCommands == nil {
		cfg.Session.AsyncInitCommands = defaults.Session.AsyncInitCommands
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
	if cfg.Issues.AutoArchive.ClosedAfterDays == 0 {
		cfg.Issues.AutoArchive.ClosedAfterDays = defaults.Issues.AutoArchive.ClosedAfterDays
	}
	if strings.TrimSpace(cfg.Issues.AutoArchive.Interval) == "" {
		cfg.Issues.AutoArchive.Interval = defaults.Issues.AutoArchive.Interval
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
	if cfg.Worktree.SyncInitCommands == nil {
		cfg.Worktree.SyncInitCommands = defaults.Worktree.SyncInitCommands
	}
	if cfg.Worktree.AsyncInitCommands == nil {
		cfg.Worktree.AsyncInitCommands = defaults.Worktree.AsyncInitCommands
	}
	if cfg.IssueResources.Env == nil {
		cfg.IssueResources.Env = defaults.IssueResources.Env
	}
	if cfg.IssueResources.PrepareCommands == nil {
		cfg.IssueResources.PrepareCommands = defaults.IssueResources.PrepareCommands
	}
	if cfg.IssueResources.FailedStartCleanupCommands == nil {
		cfg.IssueResources.FailedStartCleanupCommands = defaults.IssueResources.FailedStartCleanupCommands
	}
	if cfg.IssueResources.CleanupCommands == nil {
		cfg.IssueResources.CleanupCommands = defaults.IssueResources.CleanupCommands
	}
	if cfg.ScheduledScripts.Env == nil {
		cfg.ScheduledScripts.Env = defaults.ScheduledScripts.Env
	}
	if cfg.ScheduledScripts.Scripts == nil {
		cfg.ScheduledScripts.Scripts = defaults.ScheduledScripts.Scripts
	}
	for i := range cfg.ScheduledScripts.Scripts {
		if cfg.ScheduledScripts.Scripts[i].Env == nil {
			cfg.ScheduledScripts.Scripts[i].Env = map[string]string{}
		}
	}
	if strings.TrimSpace(cfg.Orchestration.Via) == "" {
		cfg.Orchestration.Via = defaults.Orchestration.Via
	}
	if strings.TrimSpace(cfg.Orchestration.CompleteGrace) == "" {
		cfg.Orchestration.CompleteGrace = defaults.Orchestration.CompleteGrace
	}
	if strings.TrimSpace(cfg.Orchestration.WakeDebounce) == "" {
		cfg.Orchestration.WakeDebounce = defaults.Orchestration.WakeDebounce
	}

	// Merge Notifications config
	if cfg.Notifications.ErrorThreshold == 0 {
		cfg.Notifications.ErrorThreshold = defaults.Notifications.ErrorThreshold
	}
	if cfg.GitHooks.Commands == nil {
		cfg.GitHooks.Commands = defaults.GitHooks.Commands
	}
	if cfg.GitHooks.Restage.Paths == nil {
		cfg.GitHooks.Restage.Paths = defaults.GitHooks.Restage.Paths
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
