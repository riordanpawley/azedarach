package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const userDBPathEnv = "AZEDARACH_USER_DB_PATH"

// UserDBPath returns the single user-level database owned by the global daemon.
// It is deliberately independent of repository-root discovery.
func UserDBPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(userDBPathEnv)); override != "" {
		return filepath.Clean(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve user home: empty path")
	}
	return filepath.Join(home, ".azedarach", "azedarach.db"), nil
}
