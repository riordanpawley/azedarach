package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const (
	globalDaemonRuntimeDirName = "azedarach"
	globalDaemonSocketFileName = "daemon.sock"
	globalDaemonLockFileName   = "daemon.lock"
	scopedDaemonRuntimeDirName = "scopes"
)

// GlobalDaemonRuntimeDir returns the user-global directory used for singleton daemon runtime assets.
func GlobalDaemonRuntimeDir() string {
	candidates := make([]string, 0, 3)

	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		candidates = append(candidates, filepath.Join(runtimeDir, globalDaemonRuntimeDirName))
	}

	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		candidates = append(candidates, filepath.Join(homeDir, ".azedarach", "run"))
	}

	candidates = append(candidates, filepath.Join(os.TempDir(), globalDaemonRuntimeDirName))

	for _, candidate := range candidates {
		if daemonRuntimeDirWritable(candidate) {
			return candidate
		}
	}

	return candidates[len(candidates)-1]
}

// GlobalDaemonSocketPath returns the user-global daemon socket path.
func GlobalDaemonSocketPath() string {
	return filepath.Join(GlobalDaemonRuntimeDir(), globalDaemonSocketFileName)
}

// GlobalDaemonLockPath returns the user-global singleton lock path.
func GlobalDaemonLockPath() string {
	return filepath.Join(GlobalDaemonRuntimeDir(), globalDaemonLockFileName)
}

// ScopedDaemonRuntimeDir returns a deterministic runtime directory for a worktree scope.
func ScopedDaemonRuntimeDir(startPath string) string {
	scopeRoot, err := ResolveWorktreeRoot(startPath)
	if err != nil || strings.TrimSpace(scopeRoot) == "" {
		scopeRoot = startPath
	}
	scopeID := daemonScopeID(scopeRoot)
	return filepath.Join(GlobalDaemonRuntimeDir(), scopedDaemonRuntimeDirName, scopeID)
}

// ScopedDaemonSocketPath returns the daemon socket path for a worktree scope.
func ScopedDaemonSocketPath(startPath string) string {
	return filepath.Join(ScopedDaemonRuntimeDir(startPath), globalDaemonSocketFileName)
}

// ScopedDaemonLockPath returns the daemon lock path for a worktree scope.
func ScopedDaemonLockPath(startPath string) string {
	return filepath.Join(ScopedDaemonRuntimeDir(startPath), globalDaemonLockFileName)
}

func daemonScopeID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

func daemonRuntimeDirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}

	f, err := os.CreateTemp(dir, ".azedarach-runtime-write-check-*")
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return true
}
