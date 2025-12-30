package config

import (
	"encoding/json"
	"testing"
)

func TestParseVersionedConfig_LegacyConfig(t *testing.T) {
	// Legacy config without version field
	legacyJSON := `{
		"cliTool": "opencode",
		"git": {
			"baseBranch": "develop"
		}
	}`

	cfg, err := ParseVersionedConfig([]byte(legacyJSON))
	if err != nil {
		t.Fatalf("Failed to parse legacy config: %v", err)
	}

	if cfg.CLITool != "opencode" {
		t.Errorf("Expected CLITool 'opencode', got '%s'", cfg.CLITool)
	}

	if cfg.Git.BaseBranch != "develop" {
		t.Errorf("Expected BaseBranch 'develop', got '%s'", cfg.Git.BaseBranch)
	}
}

func TestParseVersionedConfig_Version1(t *testing.T) {
	// Config with version 1
	v1JSON := `{
		"version": 1,
		"cliTool": "claude",
		"git": {
			"baseBranch": "main",
			"workflowMode": "origin"
		}
	}`

	cfg, err := ParseVersionedConfig([]byte(v1JSON))
	if err != nil {
		t.Fatalf("Failed to parse v1 config: %v", err)
	}

	if cfg.CLITool != "claude" {
		t.Errorf("Expected CLITool 'claude', got '%s'", cfg.CLITool)
	}

	if cfg.Git.WorkflowMode != "origin" {
		t.Errorf("Expected WorkflowMode 'origin', got '%s'", cfg.Git.WorkflowMode)
	}
}

func TestParseVersionedConfig_FutureVersion(t *testing.T) {
	// Config with future version should fail
	futureJSON := `{
		"version": 999,
		"cliTool": "future-tool"
	}`

	_, err := ParseVersionedConfig([]byte(futureJSON))
	if err == nil {
		t.Error("Expected error for future version, got nil")
	}
}

func TestApplyMigrations_V0ToV1(t *testing.T) {
	// Legacy config (version 0)
	data := map[string]interface{}{
		"cliTool": "claude",
		"git": map[string]interface{}{
			"baseBranch": "main",
		},
	}

	migrated, err := ApplyMigrations(data, 0)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	version, ok := migrated["version"].(int)
	if !ok || version != 1 {
		t.Errorf("Expected version 1, got %v", migrated["version"])
	}

	// Verify data is preserved
	if cliTool, ok := migrated["cliTool"].(string); !ok || cliTool != "claude" {
		t.Errorf("Expected cliTool 'claude', got %v", migrated["cliTool"])
	}
}

func TestMarshalVersionedConfig(t *testing.T) {
	cfg := &Config{
		CLITool: "claude",
		Git: GitConfig{
			BaseBranch:   "main",
			WorkflowMode: "worktree",
		},
	}

	data, err := MarshalVersionedConfig(cfg)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Parse to verify structure
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to parse output: %v", err)
	}

	// Check version is present
	if version, ok := result["version"].(float64); !ok || int(version) != CurrentVersion {
		t.Errorf("Expected version %d, got %v", CurrentVersion, result["version"])
	}

	// Check config fields are preserved
	if cliTool, ok := result["cliTool"].(string); !ok || cliTool != "claude" {
		t.Errorf("Expected cliTool 'claude', got %v", result["cliTool"])
	}
}

func TestRoundTrip(t *testing.T) {
	// Create a config
	original := &Config{
		CLITool: "claude",
		Git: GitConfig{
			BaseBranch:           "main",
			WorkflowMode:         "origin",
			ShowLineChanges:      true,
			DefaultMergeStrategy: "squash",
		},
		Session: SessionConfig{
			Shell:     "bash",
			TimeoutMs: 60000,
		},
		PR: PRConfig{
			DraftByDefault: false,
			AutoLink:       true,
		},
	}

	// Marshal to JSON
	data, err := MarshalVersionedConfig(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Parse back
	parsed, err := ParseVersionedConfig(data)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	// Verify fields match
	if parsed.CLITool != original.CLITool {
		t.Errorf("CLITool mismatch: %s != %s", parsed.CLITool, original.CLITool)
	}

	if parsed.Git.BaseBranch != original.Git.BaseBranch {
		t.Errorf("BaseBranch mismatch: %s != %s", parsed.Git.BaseBranch, original.Git.BaseBranch)
	}

	if parsed.Git.WorkflowMode != original.Git.WorkflowMode {
		t.Errorf("WorkflowMode mismatch: %s != %s", parsed.Git.WorkflowMode, original.Git.WorkflowMode)
	}

	if parsed.Git.DefaultMergeStrategy != original.Git.DefaultMergeStrategy {
		t.Errorf("DefaultMergeStrategy mismatch: %s != %s", parsed.Git.DefaultMergeStrategy, original.Git.DefaultMergeStrategy)
	}

	if parsed.Session.Shell != original.Session.Shell {
		t.Errorf("Shell mismatch: %s != %s", parsed.Session.Shell, original.Session.Shell)
	}

	if parsed.Session.TimeoutMs != original.Session.TimeoutMs {
		t.Errorf("TimeoutMs mismatch: %d != %d", parsed.Session.TimeoutMs, original.Session.TimeoutMs)
	}

	if parsed.PR.DraftByDefault != original.PR.DraftByDefault {
		t.Errorf("DraftByDefault mismatch: %v != %v", parsed.PR.DraftByDefault, original.PR.DraftByDefault)
	}
}

func TestCurrentVersion(t *testing.T) {
	// Ensure CurrentVersion is at least 1
	if CurrentVersion < 1 {
		t.Errorf("CurrentVersion should be at least 1, got %d", CurrentVersion)
	}
}
