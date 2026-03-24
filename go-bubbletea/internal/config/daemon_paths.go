package config

import (
	"os"
	"path/filepath"
)

const (
	globalDaemonRuntimeDirName = "azedarach"
	globalDaemonSocketFileName = "daemon.sock"
	globalDaemonLockFileName   = "daemon.lock"
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
