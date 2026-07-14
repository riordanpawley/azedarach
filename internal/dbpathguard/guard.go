// Package dbpathguard prevents database constructors from opening paths that
// the caller has marked as authoritative and off-limits.
package dbpathguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// RefusePathsEnv contains a JSON array of database paths that must not be
	// opened. JSON avoids platform-specific path-list ambiguity.
	RefusePathsEnv = "AZEDARACH_REFUSE_DB_PATHS"
	// LegacyRefusePathEnv protects one path for backwards compatibility.
	LegacyRefusePathEnv = "AZEDARACH_REFUSE_DB_PATH"
	// TestIsolationRootEnv marks a process launched by the broad-suite guard.
	TestIsolationRootEnv = "AZEDARACH_TEST_ISOLATION_ROOT"
	// TestCurrentProjectDBEnv records the configured current-project DB captured
	// before isolation was applied.
	TestCurrentProjectDBEnv = "AZEDARACH_TEST_CURRENT_PROJECT_DB_PATH"
	// TestIsolatedProjectDBEnv records the standard isolated override, allowing
	// individual tests to replace AZEDARACH_DB_PATH deliberately.
	TestIsolatedProjectDBEnv = "AZEDARACH_TEST_PROJECT_DB_PATH"
)

// Check returns an error when path matches any configured refused path. Call
// it before directory creation or sql.Open so refusal has no filesystem effect.
func Check(path string) error {
	candidate, err := Canonical(path)
	if err != nil {
		return fmt.Errorf("canonicalize database path: %w", err)
	}
	refused, err := RefusedPaths()
	if err != nil {
		return err
	}
	for _, configured := range refused {
		blocked, canonicalErr := Canonical(configured)
		if canonicalErr != nil {
			return fmt.Errorf("canonicalize refused database path %q: %w", configured, canonicalErr)
		}
		if candidate == blocked {
			return fmt.Errorf("refusing configured database path: %s", candidate)
		}
	}
	return nil
}

// RefusedPaths decodes the configured refusal set.
func RefusedPaths() ([]string, error) {
	paths := make([]string, 0)
	if raw := strings.TrimSpace(os.Getenv(RefusePathsEnv)); raw != "" {
		if err := json.Unmarshal([]byte(raw), &paths); err != nil {
			return nil, fmt.Errorf("decode %s: %w", RefusePathsEnv, err)
		}
	}
	if legacy := strings.TrimSpace(os.Getenv(LegacyRefusePathEnv)); legacy != "" {
		paths = append(paths, legacy)
	}
	return paths, nil
}

// UseProjectOverride reports whether a default-path resolver should honor the
// process-wide AZEDARACH_DB_PATH override for candidate. Under broad-suite
// isolation, safe explicit fixture roots retain their own databases instead of
// collapsing into one shared parallel-test database. The captured current
// project still routes to the isolated override, and an individual test may
// deliberately replace the standard override.
func UseProjectOverride(candidate, override string) (bool, error) {
	if strings.TrimSpace(os.Getenv(TestIsolationRootEnv)) == "" {
		return true, nil
	}
	isolated := strings.TrimSpace(os.Getenv(TestIsolatedProjectDBEnv))
	if isolated == "" || strings.TrimSpace(override) != isolated {
		return true, nil
	}
	current := strings.TrimSpace(os.Getenv(TestCurrentProjectDBEnv))
	if current == "" {
		return false, fmt.Errorf("%s is required during test isolation", TestCurrentProjectDBEnv)
	}
	candidatePath, err := Canonical(candidate)
	if err != nil {
		return false, err
	}
	currentPath, err := Canonical(current)
	if err != nil {
		return false, err
	}
	return candidatePath == currentPath, nil
}

// Encode returns the stable environment representation for paths.
func Encode(paths []string) (string, error) {
	canonical := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		clean, err := Canonical(path)
		if err != nil {
			return "", err
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		canonical = append(canonical, clean)
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode refused database paths: %w", err)
	}
	return string(data), nil
}

// Canonical resolves symlinks in the deepest existing ancestor as well as an
// existing database path, so a missing alias below a symlink cannot evade a
// refusal.
func Canonical(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		return filepath.Clean(resolved), nil
	}

	ancestor := abs
	tail := make([]string, 0)
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		tail = append(tail, filepath.Base(ancestor))
		ancestor = parent
	}
	if resolved, resolveErr := filepath.EvalSymlinks(ancestor); resolveErr == nil {
		ancestor = resolved
	}
	for i := len(tail) - 1; i >= 0; i-- {
		ancestor = filepath.Join(ancestor, tail[i])
	}
	return filepath.Clean(ancestor), nil
}
