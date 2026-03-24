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
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, globalDaemonRuntimeDirName)
	}

	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		return filepath.Join(homeDir, ".azedarach", "run")
	}

	return filepath.Join(os.TempDir(), globalDaemonRuntimeDirName)
}

// GlobalDaemonSocketPath returns the user-global daemon socket path.
func GlobalDaemonSocketPath() string {
	return filepath.Join(GlobalDaemonRuntimeDir(), globalDaemonSocketFileName)
}

// GlobalDaemonLockPath returns the user-global singleton lock path.
func GlobalDaemonLockPath() string {
	return filepath.Join(GlobalDaemonRuntimeDir(), globalDaemonLockFileName)
}
