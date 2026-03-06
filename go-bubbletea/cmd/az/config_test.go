package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestRunCLIConfigShowJSONSuccessIncludesCommandOKAndKnownDefault(t *testing.T) {
	stubConfigLoaderForTest(t, func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"config", "show", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Command string          `json:"command"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "config.show" {
		t.Fatalf("expected command=config.show, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if !jsonContainsStringField(envelope.Result, "cliTool", "claude") {
		t.Fatalf(
			"expected result to contain effective default config key/value cliTool=claude.\nresult:\n%s",
			string(envelope.Result),
		)
	}
}

func TestRunCLIConfigValidateJSONSuccessMarksValidDeterministically(t *testing.T) {
	stubConfigLoaderForTest(t, func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	})

	exitCode1, stdout1, stderr1 := runCLIForTest([]string{"config", "validate", "--json"})
	if exitCode1 != 0 {
		t.Fatalf("expected first exit code 0, got %d (stderr: %q)", exitCode1, stderr1)
	}
	if stderr1 != "" {
		t.Fatalf("expected first stderr to be empty, got %q", stderr1)
	}

	exitCode2, stdout2, stderr2 := runCLIForTest([]string{"config", "validate", "--json"})
	if exitCode2 != 0 {
		t.Fatalf("expected second exit code 0, got %d (stderr: %q)", exitCode2, stderr2)
	}
	if stderr2 != "" {
		t.Fatalf("expected second stderr to be empty, got %q", stderr2)
	}

	var envelope1 struct {
		Command string          `json:"command"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout1), &envelope1); err != nil {
		t.Fatalf("expected first output to be valid JSON, got parse error: %v\noutput:\n%s", err, stdout1)
	}

	var envelope2 struct {
		Command string          `json:"command"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout2), &envelope2); err != nil {
		t.Fatalf("expected second output to be valid JSON, got parse error: %v\noutput:\n%s", err, stdout2)
	}

	if envelope1.Command != "config.validate" {
		t.Fatalf("expected first command=config.validate, got %q", envelope1.Command)
	}
	if envelope2.Command != "config.validate" {
		t.Fatalf("expected second command=config.validate, got %q", envelope2.Command)
	}
	if !envelope1.OK || !envelope2.OK {
		t.Fatalf("expected ok=true on repeated validate success calls")
	}

	valid1, found1 := jsonFindBoolField(envelope1.Result, "valid")
	if !found1 {
		t.Fatalf("expected first result to contain valid boolean field.\nresult:\n%s", string(envelope1.Result))
	}
	valid2, found2 := jsonFindBoolField(envelope2.Result, "valid")
	if !found2 {
		t.Fatalf("expected second result to contain valid boolean field.\nresult:\n%s", string(envelope2.Result))
	}
	if !valid1 || !valid2 {
		t.Fatalf("expected validation success to mark valid=true (got first=%t, second=%t)", valid1, valid2)
	}
}

func TestRunCLIConfigValidateJSONInvalidConfigReturnsDeterministicFailure(t *testing.T) {
	stubConfigLoaderForTest(t, func() (*config.Config, error) {
		return nil, fmt.Errorf("invalid config: session.timeoutMs must be >= 0")
	})

	exitCode1, stdout1, stderr1 := runCLIForTest([]string{"config", "validate", "--json"})
	if exitCode1 == 0 {
		t.Fatalf("expected non-zero first exit code for invalid config")
	}
	if stderr1 != "" {
		t.Fatalf("expected first stderr to be empty in JSON mode, got %q", stderr1)
	}

	exitCode2, stdout2, stderr2 := runCLIForTest([]string{"config", "validate", "--json"})
	if exitCode2 == 0 {
		t.Fatalf("expected non-zero second exit code for invalid config")
	}
	if stderr2 != "" {
		t.Fatalf("expected second stderr to be empty in JSON mode, got %q", stderr2)
	}

	if exitCode1 != exitCode2 {
		t.Fatalf("expected deterministic non-zero exit code for invalid config, got %d then %d", exitCode1, exitCode2)
	}

	var envelope1 struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout1), &envelope1); err != nil {
		t.Fatalf("expected first output to be valid JSON, got parse error: %v\noutput:\n%s", err, stdout1)
	}

	var envelope2 struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout2), &envelope2); err != nil {
		t.Fatalf("expected second output to be valid JSON, got parse error: %v\noutput:\n%s", err, stdout2)
	}

	if envelope1.Command != "config.validate" {
		t.Fatalf("expected first command=config.validate, got %q", envelope1.Command)
	}
	if envelope2.Command != "config.validate" {
		t.Fatalf("expected second command=config.validate, got %q", envelope2.Command)
	}
	if envelope1.OK || envelope2.OK {
		t.Fatalf("expected ok=false for invalid config validation path")
	}
	if envelope1.Error.Code != "invalid_config" {
		allowedFallbackCode := "config_validation_failed"
		if envelope1.Error.Code != allowedFallbackCode {
			t.Fatalf(
				"expected deterministic invalid-config code (preferred invalid_config, allowed %q), got %q",
				allowedFallbackCode,
				envelope1.Error.Code,
			)
		}
	}
	if envelope2.Error.Code != envelope1.Error.Code {
		t.Fatalf(
			"expected deterministic error code for invalid config, got %q then %q",
			envelope1.Error.Code,
			envelope2.Error.Code,
		)
	}
}

func TestRunCLIConfigInvalidUsagePaths(t *testing.T) {
	stubConfigLoaderForTest(t, func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	})

	t.Run("missing subcommand in human mode prints usage and exits non-zero", func(t *testing.T) {
		exitCode, stdout, stderr := runCLIForTest([]string{"config"})
		if exitCode == 0 {
			t.Fatalf("expected non-zero exit code for missing config subcommand")
		}
		if stdout != "" {
			t.Fatalf("expected empty stdout for missing subcommand in human mode, got %q", stdout)
		}
		if !strings.Contains(stderr, "Usage: az config") {
			t.Fatalf("expected usage text in stderr for missing subcommand, got %q", stderr)
		}
	})

	t.Run("unknown subcommand in JSON mode returns invalid_argument envelope", func(t *testing.T) {
		exitCode, stdout, stderr := runCLIForTest([]string{"config", "unexpected", "--json"})
		if exitCode == 0 {
			t.Fatalf("expected non-zero exit code for unknown config subcommand in JSON mode")
		}
		if stderr != "" {
			t.Fatalf("expected empty stderr in JSON mode, got %q", stderr)
		}

		var envelope struct {
			OK    bool `json:"ok"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
		}

		if envelope.OK {
			t.Fatalf("expected ok=false for unknown config subcommand in JSON mode")
		}
		if envelope.Error.Code != "invalid_argument" {
			t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
		}
	})
}

func stubConfigLoaderForTest(t *testing.T, loader func() (*config.Config, error)) {
	t.Helper()

	originalLoadConfig := loadConfig
	loadConfig = loader

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
	})
}

func jsonContainsStringField(raw json.RawMessage, key, expected string) bool {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false
	}
	return nestedStringFieldEquals(parsed, key, expected)
}

func nestedStringFieldEquals(value any, key, expected string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if rawField, ok := typed[key]; ok {
			if actual, ok := rawField.(string); ok && actual == expected {
				return true
			}
		}
		for _, nested := range typed {
			if nestedStringFieldEquals(nested, key, expected) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if nestedStringFieldEquals(nested, key, expected) {
				return true
			}
		}
	}

	return false
}

func jsonFindBoolField(raw json.RawMessage, key string) (bool, bool) {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false, false
	}
	return nestedBoolField(parsed, key)
}

func nestedBoolField(value any, key string) (bool, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if rawField, ok := typed[key]; ok {
			if actual, ok := rawField.(bool); ok {
				return actual, true
			}
		}
		for _, nested := range typed {
			if actual, found := nestedBoolField(nested, key); found {
				return actual, true
			}
		}
	case []any:
		for _, nested := range typed {
			if actual, found := nestedBoolField(nested, key); found {
				return actual, true
			}
		}
	}

	return false, false
}
