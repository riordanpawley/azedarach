// Package testisolation builds a fail-closed process environment for tests
// that may exercise configured Azedarach database paths.
package testisolation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/dbpathguard"
)

const RootEnv = dbpathguard.TestIsolationRootEnv

type Environment struct {
	Root         string
	OriginalDBs  []string
	overrides    map[string]string
	removeOnDone bool
}

// NewTemporary snapshots configured database paths before creating isolated
// HOME/config/database roots. cwd identifies the project whose default DB must
// also be protected.
func NewTemporary(cwd string) (*Environment, error) {
	root, err := os.MkdirTemp("", "azedarach-test-isolation-*")
	if err != nil {
		return nil, fmt.Errorf("create test isolation root: %w", err)
	}
	environment, err := New(root, cwd)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	environment.removeOnDone = true
	return environment, nil
}

// New snapshots configured paths and prepares an isolation environment rooted
// at root. It never loads the registry through mutation-capable helpers.
func New(root, cwd string) (*Environment, error) {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("canonicalize test isolation root: %w", err)
	}
	paths, err := configuredDatabasePaths(cwd)
	if err != nil {
		return nil, err
	}
	encoded, err := dbpathguard.Encode(paths)
	if err != nil {
		return nil, fmt.Errorf("encode original database paths: %w", err)
	}
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg-config")
	projectDB := filepath.Join(root, "project", ".azedarach", "azedarach.db")
	userDB := filepath.Join(root, "user", "azedarach.db")
	projectRoot, err := config.ResolveProjectRoot(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve current project root: %w", err)
	}
	if strings.TrimSpace(projectRoot) == "" {
		return nil, fmt.Errorf("resolve current project root: empty path")
	}
	currentProjectDB, err := dbpathguard.Canonical(filepath.Join(projectRoot, ".azedarach", "azedarach.db"))
	if err != nil {
		return nil, fmt.Errorf("canonicalize current project database: %w", err)
	}
	for _, dir := range []string{home, xdg, filepath.Dir(projectDB), filepath.Dir(userDB)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create isolated test directory %s: %w", dir, err)
		}
	}
	return &Environment{
		Root:        root,
		OriginalDBs: paths,
		overrides: map[string]string{
			"HOME":                               home,
			"XDG_CONFIG_HOME":                    xdg,
			"AZEDARACH_USER_DB_PATH":             userDB,
			"AZEDARACH_DB_PATH":                  projectDB,
			dbpathguard.RefusePathsEnv:           encoded,
			dbpathguard.LegacyRefusePathEnv:      "",
			RootEnv:                              root,
			dbpathguard.TestCurrentProjectDBEnv:  currentProjectDB,
			dbpathguard.TestIsolatedProjectDBEnv: projectDB,
		},
	}, nil
}

// Environ overlays isolation values onto base for a child process.
func (e *Environment) Environ(base []string) []string {
	values := make(map[string]string, len(base)+len(e.overrides))
	order := make([]string, 0, len(base)+len(e.overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = entry
	}
	for key, value := range e.overrides {
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = key + "=" + value
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, values[key])
	}
	return result
}

// Apply installs isolation in the current process and returns a restoration
// function. TestMain callers normally restore only to make cleanup explicit.
func (e *Environment) Apply() (func(), error) {
	type previous struct {
		value string
		set   bool
	}
	before := make(map[string]previous, len(e.overrides))
	applied := make([]string, 0, len(e.overrides))
	restoreKeys := func(keys []string) {
		for _, key := range keys {
			old := before[key]
			if old.set {
				_ = os.Setenv(key, old.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
	keys := make([]string, 0, len(e.overrides))
	for key := range e.overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := e.overrides[key]
		old, set := os.LookupEnv(key)
		before[key] = previous{value: old, set: set}
		if err := os.Setenv(key, value); err != nil {
			restoreKeys(applied)
			return nil, fmt.Errorf("set %s: %w", key, err)
		}
		applied = append(applied, key)
	}
	return func() {
		restoreKeys(applied)
	}, nil
}

func (e *Environment) Close() error {
	if e == nil || !e.removeOnDone {
		return nil
	}
	return os.RemoveAll(e.Root)
}

// CheckDatabaseClone rejects a proposed migration clone path when it matches
// the configured root-user, current-project, registered-project, or inherited
// refused database set. It is safe to call from direct focused tests that were
// not launched through the canonical runner.
func CheckDatabaseClone(path, cwd string) error {
	candidate, err := dbpathguard.Canonical(path)
	if err != nil {
		return fmt.Errorf("canonicalize database clone: %w", err)
	}
	configured, err := configuredDatabasePaths(cwd)
	if err != nil {
		return err
	}
	for _, original := range configured {
		if candidate == original {
			return fmt.Errorf("refusing configured original database clone: %s", candidate)
		}
	}
	return nil
}

func configuredDatabasePaths(cwd string) ([]string, error) {
	paths, err := dbpathguard.RefusedPaths()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve configured user home: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return nil, fmt.Errorf("resolve configured user home: empty path")
	}
	paths = append(paths, filepath.Join(home, ".azedarach", "azedarach.db"))
	registryPath := filepath.Join(home, ".config", "azedarach", "projects.json")
	data, readErr := os.ReadFile(registryPath)
	if readErr == nil {
		var registry config.ProjectsRegistry
		if err := json.Unmarshal(data, &registry); err != nil {
			return nil, fmt.Errorf("decode configured project registry %s: %w", registryPath, err)
		}
		for _, project := range registry.Projects {
			if strings.TrimSpace(project.Path) != "" {
				projectRoot, rootErr := config.ResolveProjectRoot(project.Path)
				if rootErr != nil {
					return nil, fmt.Errorf("resolve registered project root %s: %w", project.Path, rootErr)
				}
				if strings.TrimSpace(projectRoot) == "" {
					return nil, fmt.Errorf("resolve registered project root %s: empty path", project.Path)
				}
				paths = append(paths, filepath.Join(projectRoot, ".azedarach", "azedarach.db"))
			}
		}
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read configured project registry %s: %w", registryPath, readErr)
	}
	userDB, err := config.UserDBPath()
	if err != nil {
		return nil, fmt.Errorf("resolve configured user database: %w", err)
	}
	paths = append(paths, userDB)
	if override := strings.TrimSpace(os.Getenv("AZEDARACH_DB_PATH")); override != "" {
		paths = append(paths, override)
	}
	projectRoot, err := config.ResolveProjectRoot(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve configured project root: %w", err)
	}
	if strings.TrimSpace(projectRoot) == "" {
		return nil, fmt.Errorf("resolve configured project root: empty path")
	}
	paths = append(paths, filepath.Join(projectRoot, ".azedarach", "azedarach.db"))
	encoded, err := dbpathguard.Encode(paths)
	if err != nil {
		return nil, err
	}
	var canonical []string
	if err := json.Unmarshal([]byte(encoded), &canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}
