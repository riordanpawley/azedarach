package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// DaemonSocketPathFor returns either the global or worktree-scoped daemon socket
// path depending on runtime mode. Scoped daemons are opt-in for daemon/runtime
// development; normal CLI use goes through the user-global singleton daemon.
func DaemonSocketPathFor(startPath string) string {
	if UseScopedDaemonRuntimeFor(startPath) {
		return ScopedDaemonSocketPath(startPath)
	}
	return GlobalDaemonSocketPath()
}

// DaemonLockPathFor returns either the global or worktree-scoped daemon lock path
// depending on runtime mode.
func DaemonLockPathFor(startPath string) string {
	if UseScopedDaemonRuntimeFor(startPath) {
		return ScopedDaemonLockPath(startPath)
	}
	return GlobalDaemonLockPath()
}

// ValidateSharedDaemonExecutable rejects a development-worktree az binary before
// it can talk to the user-global production daemon.
func ValidateSharedDaemonExecutable(socketPath, executable string) error {
	if filepath.Clean(socketPath) != filepath.Clean(GlobalDaemonSocketPath()) {
		return nil
	}
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return nil
	}
	absExecutable, err := filepath.Abs(executable)
	if err != nil {
		return nil
	}
	resolvedExecutable := resolveSymlinkBestEffort(absExecutable)
	if !IsAzedarachDevelopmentWorktree(resolvedExecutable) {
		return nil
	}
	return fmt.Errorf("refusing to use the shared production daemon from Azedarach development worktree binary %s; run the canonical production az binary or set AZEDARACH_DAEMON_SCOPE=worktree when intentionally testing this worktree", resolvedExecutable)
}

// ManagedGenerationBinDir returns the immutable managed install generation
// containing executable. Both az and azd must be executable siblings so callers
// never seed a partial or incoherent generation into a long-lived process.
func ManagedGenerationBinDir(executable, expectedBinary string) (string, bool) {
	executable = strings.TrimSpace(executable)
	expectedBinary = strings.TrimSpace(expectedBinary)
	if executable == "" || expectedBinary == "" {
		return "", false
	}
	absExecutable, err := filepath.Abs(executable)
	if err != nil {
		return "", false
	}
	resolvedExecutable := resolveSymlinkBestEffort(absExecutable)
	if filepath.Base(resolvedExecutable) != expectedBinary {
		return "", false
	}
	generationDir := filepath.Dir(resolvedExecutable)
	generationName := filepath.Base(generationDir)
	if !strings.HasPrefix(generationName, "generation.") ||
		len(generationName) <= len("generation.") ||
		filepath.Base(filepath.Dir(generationDir)) != ".azedarach-generations" {
		return "", false
	}
	for _, binary := range []string{"az", "azd"} {
		if !executableRegularFile(filepath.Join(generationDir, binary)) {
			return "", false
		}
	}
	return generationDir, true
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

// UseScopedDaemonRuntimeFor reports whether daemon runtime assets should be
// scoped away from the user-global daemon while developing Azedarach itself.
// Scoped daemons are only for explicitly requested Azedarach linked worktree
// validation, where a worktree can run a changed azd without replacing or
// protocol-conflicting with production.
func UseScopedDaemonRuntimeFor(startPath string) bool {
	mode := strings.TrimSpace(strings.ToLower(os.Getenv("AZEDARACH_DAEMON_SCOPE")))
	switch mode {
	case "worktree", "scoped", "local":
		return IsAzedarachDevelopmentWorktree(startPath)
	case "global", "shared", "user", "off", "none":
		return false
	}
	return false
}

func IsAzedarachDevelopmentWorktree(startPath string) bool {
	if !IsLinkedGitWorktree(startPath) {
		return false
	}
	projectRoot, err := ResolveProjectRoot(startPath)
	if err != nil || strings.TrimSpace(projectRoot) == "" {
		return false
	}
	modulePath := filepath.Join(projectRoot, "go.mod")
	data, err := os.ReadFile(modulePath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1] == "github.com/riordanpawley/azedarach"
		}
	}
	return false
}

func IsLinkedGitWorktree(startPath string) bool {
	worktreeRoot, err := ResolveWorktreeRoot(startPath)
	if err != nil {
		return false
	}
	projectRoot, err := ResolveProjectRoot(startPath)
	if err != nil {
		return false
	}
	if strings.TrimSpace(worktreeRoot) == "" || strings.TrimSpace(projectRoot) == "" {
		return false
	}
	return filepath.Clean(worktreeRoot) != filepath.Clean(projectRoot)
}

func resolveSymlinkBestEffort(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || strings.TrimSpace(resolved) == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(resolved)
}

func executableRegularFile(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.Mode().IsRegular() && stat.Mode().Perm()&0o111 != 0
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
