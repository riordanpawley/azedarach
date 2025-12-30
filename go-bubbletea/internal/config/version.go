package config

import (
	"encoding/json"
	"fmt"
)

// CurrentVersion is the current config schema version
const CurrentVersion = 1

// VersionedConfig wraps a Config with a version field for migrations
type VersionedConfig struct {
	Version int     `json:"version"`
	Config  *Config `json:"config,omitempty"`

	// Inline config fields for backwards compatibility
	// These are used when loading legacy configs without version field
	CLITool       string          `json:"cliTool,omitempty"`
	Git           GitConfig       `json:"git,omitempty"`
	Session       SessionConfig   `json:"session,omitempty"`
	PR            PRConfig        `json:"pr,omitempty"`
	Merge         MergeConfig     `json:"merge,omitempty"`
	Notifications NotifyConfig    `json:"notifications,omitempty"`
	Beads         BeadsConfig     `json:"beads,omitempty"`
	Network       NetworkConfig   `json:"network,omitempty"`
	DevServer     DevServerConfig `json:"devServer,omitempty"`
	Worktree      WorktreeConfig  `json:"worktree,omitempty"`
}

// Migration represents a config migration function
type Migration struct {
	FromVersion int
	ToVersion   int
	Migrate     func(data map[string]interface{}) (map[string]interface{}, error)
}

// migrations is the list of migrations in order
var migrations = []Migration{
	// Migration 0 -> 1: Add version field, no structural changes
	{
		FromVersion: 0,
		ToVersion:   1,
		Migrate: func(data map[string]interface{}) (map[string]interface{}, error) {
			// No structural changes in v1, just add version
			data["version"] = 1
			return data, nil
		},
	},
	// Future migrations go here:
	// {
	// 	FromVersion: 1,
	// 	ToVersion:   2,
	// 	Migrate: func(data map[string]interface{}) (map[string]interface{}, error) {
	// 		// Example: rename a field
	// 		if old, ok := data["oldFieldName"]; ok {
	// 			data["newFieldName"] = old
	// 			delete(data, "oldFieldName")
	// 		}
	// 		data["version"] = 2
	// 		return data, nil
	// 	},
	// },
}

// ParseVersionedConfig parses config data with version migration support
func ParseVersionedConfig(data []byte) (*Config, error) {
	// First, parse as raw JSON to get version
	var rawConfig map[string]interface{}
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	// Detect version (0 if not present = legacy config)
	version := 0
	if v, ok := rawConfig["version"].(float64); ok {
		version = int(v)
	}

	// Apply migrations if needed
	if version < CurrentVersion {
		var err error
		rawConfig, err = ApplyMigrations(rawConfig, version)
		if err != nil {
			return nil, fmt.Errorf("failed to migrate config: %w", err)
		}
	}

	// Check for future version
	if version > CurrentVersion {
		return nil, fmt.Errorf("config version %d is newer than supported version %d", version, CurrentVersion)
	}

	// Now parse into proper struct
	// Re-marshal and unmarshal to get proper types
	migratedData, err := json.Marshal(rawConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal migrated config: %w", err)
	}

	// Try parsing as versioned config (with nested config field)
	var versioned VersionedConfig
	if err := json.Unmarshal(migratedData, &versioned); err != nil {
		return nil, fmt.Errorf("failed to parse versioned config: %w", err)
	}

	// If Config field is set, use it
	if versioned.Config != nil {
		return versioned.Config, nil
	}

	// Otherwise, parse as flat config (inline fields for backwards compat)
	var cfg Config
	if err := json.Unmarshal(migratedData, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse flat config: %w", err)
	}

	return &cfg, nil
}

// ApplyMigrations applies all migrations from the given version to CurrentVersion
func ApplyMigrations(data map[string]interface{}, fromVersion int) (map[string]interface{}, error) {
	for _, migration := range migrations {
		if migration.FromVersion == fromVersion {
			var err error
			data, err = migration.Migrate(data)
			if err != nil {
				return nil, fmt.Errorf("migration %d -> %d failed: %w",
					migration.FromVersion, migration.ToVersion, err)
			}
			fromVersion = migration.ToVersion
		}
	}

	if fromVersion < CurrentVersion {
		return nil, fmt.Errorf("no migration path from version %d to %d", fromVersion, CurrentVersion)
	}

	return data, nil
}

// MarshalVersionedConfig serializes a config with version information
func MarshalVersionedConfig(cfg *Config) ([]byte, error) {
	versioned := struct {
		Version int     `json:"version"`
		Config  *Config `json:",inline"`
	}{
		Version: CurrentVersion,
		Config:  cfg,
	}

	// For cleaner output, we'll create a flat structure with version
	// Marshal config first
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	// Parse back as map
	var cfgMap map[string]interface{}
	if err := json.Unmarshal(cfgData, &cfgMap); err != nil {
		return nil, err
	}

	// Add version at the top
	result := make(map[string]interface{})
	result["version"] = versioned.Version

	// Add all config fields
	for k, v := range cfgMap {
		result[k] = v
	}

	return json.MarshalIndent(result, "", "  ")
}
