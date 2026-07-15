// Package gocache owns Azedarach's bounded, worktree-aware Go build-cache protocol.
package gocache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
	"golang.org/x/sys/unix"
)

const (
	DefaultSoftLimitBytes int64 = 10 << 30
	DefaultHardLimitBytes int64 = 28 << 30
	LayoutVersion               = "v1"
)

type Kind string

const (
	KindNormal   Kind = "normal"
	KindRace     Kind = "race"
	KindCoverage Kind = "coverage"
)

type Config struct {
	Root           string
	Owner          string
	Kind           Kind
	SoftLimitBytes int64
	HardLimitBytes int64
}

type Stats struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
}

type Telemetry struct {
	Namespace      string `json:"namespace"`
	Kind           Kind   `json:"kind"`
	Path           string `json:"path"`
	Policy         string `json:"policy"`
	Before         Stats  `json:"before"`
	After          Stats  `json:"after"`
	DeltaBytes     int64  `json:"delta_bytes"`
	DeltaFiles     int64  `json:"delta_files"`
	FamilyBytes    int64  `json:"family_bytes"`
	SoftLimitBytes int64  `json:"soft_limit_bytes"`
	HardLimitBytes int64  `json:"hard_limit_bytes"`
	Decision       string `json:"decision"`
}

func FromEnvironment(kind Kind) (Config, error) {
	return FromEnvironmentForRepository(context.Background(), kind, ".")
}

// FromEnvironmentForRepository resolves the only daemon-recoverable cache
// root for repoDir. Repository-family root redirection is rejected because the
// global daemon cannot authoritatively recover a shell-local custom location.
func FromEnvironmentForRepository(ctx context.Context, kind Kind, repoDir string) (Config, error) {
	root := RootForRepository(ctx, repoDir)
	if override := strings.TrimSpace(os.Getenv("AZEDARACH_GO_CACHE_ROOT")); override != "" {
		overrideAbs, err := filepath.Abs(filepath.Clean(override))
		if err != nil {
			return Config{}, fmt.Errorf("resolve AZEDARACH_GO_CACHE_ROOT: %w", err)
		}
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			return Config{}, fmt.Errorf("resolve repository Go cache root: %w", err)
		}
		if overrideAbs != rootAbs {
			return Config{}, fmt.Errorf("AZEDARACH_GO_CACHE_ROOT must equal daemon-authoritative project root %s (got %s)", rootAbs, overrideAbs)
		}
	}
	owner := sanitizeOwner(os.Getenv("AZEDARACH_GO_CACHE_OWNER"))
	if owner == "" {
		owner = ownerFromGit()
	}
	if kind == "" {
		kind = KindNormal
	}
	if !kind.Valid() {
		return Config{}, fmt.Errorf("unsupported Go cache kind %q", kind)
	}
	soft, err := envBytes("AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES", DefaultSoftLimitBytes)
	if err != nil {
		return Config{}, err
	}
	hard, err := envBytes("AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES", DefaultHardLimitBytes)
	if err != nil {
		return Config{}, err
	}
	if soft <= 0 || hard <= soft {
		return Config{}, fmt.Errorf("Go cache thresholds require 0 < soft (%d) < hard (%d)", soft, hard)
	}
	cfg := Config{Root: filepath.Clean(root), Owner: owner, Kind: kind, SoftLimitBytes: soft, HardLimitBytes: hard}
	if override := strings.TrimSpace(os.Getenv("AZEDARACH_GOCACHE")); override != "" && filepath.Clean(override) != cfg.CachePath() {
		return Config{}, fmt.Errorf("AZEDARACH_GOCACHE must equal managed namespace %s (got %s)", cfg.CachePath(), override)
	}
	return cfg, nil
}

func ownerFromGit() string {
	gitDirOutput, gitDirErr := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-dir").Output()
	commonOutput, commonErr := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if gitDirErr != nil || commonErr != nil || strings.TrimSpace(string(gitDirOutput)) == strings.TrimSpace(string(commonOutput)) {
		return "main"
	}
	branchOutput, _ := exec.Command("git", "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	parts := strings.Split(strings.TrimSpace(string(branchOutput)), "/")
	if len(parts) >= 3 {
		return sanitizeOwner("issue-" + parts[1])
	}
	return "main"
}

func (c Config) CachePath() string {
	return filepath.Join(c.Root, "caches", LayoutVersion, string(c.Kind), c.Owner)
}

func (c Config) Namespace() string  { return string(c.Kind) + "/" + c.Owner }
func (c Config) LayoutRoot() string { return filepath.Join(c.Root, "caches", LayoutVersion) }
func (c Config) LockPath() string   { return filepath.Join(c.Root, "caches", ".maintenance.lock") }

func (k Kind) Valid() bool { return k == KindNormal || k == KindRace || k == KindCoverage }

func KindForProfile(profile string) Kind {
	if profile == "race" {
		return KindRace
	}
	return KindNormal
}

// RootForRepository resolves the central repository-family cache root even
// when repoDir itself is a linked worktree.
func RootForRepository(ctx context.Context, repoDir string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if output, err := cmd.Output(); err == nil {
		commonDir := strings.TrimSpace(string(output))
		if filepath.Base(commonDir) == ".git" {
			return filepath.Join(filepath.Dir(commonDir), ".azedarach", "go")
		}
	}
	absolute, err := filepath.Abs(repoDir)
	if err != nil {
		absolute = filepath.Clean(repoDir)
	}
	return filepath.Join(absolute, ".azedarach", "go")
}

func StatsFor(path string) (Stats, error) {
	var out Stats
	err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out.Files++
		out.Bytes += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return Stats{}, nil
	}
	return out, err
}

// StatsManaged measures the full managed v1 family without following cache
// hierarchy symlinks.
func StatsManaged(cfg Config) (Stats, error) {
	return managedStats(cfg, false)
}

func ManagedLayoutExists(cfg Config) (bool, error) {
	dir, err := openManagedDir(cfg, []string{"caches", LayoutVersion}, false)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = dir.Close()
	return true, nil
}

// WithExclusiveLock serializes supported cache maintenance and validators that
// explicitly opt into auto-maintenance.
func WithExclusiveLock(ctx context.Context, cfg Config, fn func() error) error {
	return withCacheLock(ctx, cfg, unix.LOCK_EX, fn)
}

// WithSharedLock lets independent validators reuse their owned cache
// namespaces concurrently while remaining mutually exclusive with maintenance.
func WithSharedLock(ctx context.Context, cfg Config, fn func() error) error {
	return withCacheLock(ctx, cfg, unix.LOCK_SH, fn)
}

// WithValidationLock prepares a managed cache under the lock required by its
// policy. Ordinary validators share the lock; explicit auto-maintenance takes
// it exclusively so `go clean -cache` cannot race another validator.
func WithValidationLock(ctx context.Context, cfg Config, autoMaintain bool, fn func(Telemetry, error) error) error {
	mode := unix.LOCK_SH
	if autoMaintain {
		mode = unix.LOCK_EX
	}
	return withCacheLock(ctx, cfg, mode, func() error {
		telemetry, err := Prepare(ctx, cfg, autoMaintain)
		return fn(telemetry, err)
	})
}

func withCacheLock(ctx context.Context, cfg Config, mode int, fn func() error) error {
	lockDir, err := openManagedDir(cfg, []string{"caches"}, true)
	if err != nil {
		return fmt.Errorf("open Go cache lock directory: %w", err)
	}
	defer lockDir.Close()
	var fd int
	for attempt := 0; ; attempt++ {
		fd, err = unix.Openat(int(lockDir.Fd()), ".maintenance.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == nil || !errors.Is(err, unix.ENOENT) || attempt == 4 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err != nil {
		return fmt.Errorf("open Go cache maintenance lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), cfg.LockPath())
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("wrap Go cache maintenance lock descriptor")
	}
	defer file.Close()
	for {
		if err := unix.Flock(int(file.Fd()), mode|unix.LOCK_NB); err == nil {
			break
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock Go cache maintenance: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for Go cache maintenance lock: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck
	return fn()
}

// Prepare enforces the family hard limit before a managed validation starts.
// If auto-maintenance is enabled, it cleans only the selected namespace through
// `go clean -cache`; otherwise the command is refused with an explicit remedy.
func Prepare(ctx context.Context, cfg Config, autoMaintain bool) (Telemetry, error) {
	telemetry := Telemetry{Namespace: cfg.Namespace(), Kind: cfg.Kind, Path: cfg.CachePath(), Policy: "retained-build-cache", SoftLimitBytes: cfg.SoftLimitBytes, HardLimitBytes: cfg.HardLimitBytes, Decision: "within-limits"}
	before, err := managedStats(cfg, true)
	if err != nil {
		return telemetry, fmt.Errorf("measure selected Go cache: %w", err)
	}
	family, err := managedStats(cfg, false)
	if err != nil {
		return telemetry, fmt.Errorf("measure Go cache family: %w", err)
	}
	telemetry.Before, telemetry.FamilyBytes = before, family.Bytes
	if family.Bytes > cfg.HardLimitBytes {
		if !autoMaintain {
			telemetry.Decision = "refused-hard-limit"
			return telemetry, fmt.Errorf("Go build-cache family uses %d bytes, above hard limit %d; run `just go-cache-maintain` or set AZEDARACH_GO_CACHE_AUTO_MAINTAIN=1", family.Bytes, cfg.HardLimitBytes)
		}
		if err := CleanManaged(ctx, cfg); err != nil {
			telemetry.Decision = "maintenance-failed"
			return telemetry, err
		}
		maintainedFamily, measureErr := managedStats(cfg, false)
		if measureErr != nil {
			telemetry.Decision = "maintenance-measurement-failed"
			return telemetry, measureErr
		}
		telemetry.FamilyBytes = maintainedFamily.Bytes
		if maintainedFamily.Bytes > cfg.HardLimitBytes {
			telemetry.Decision = "maintenance-insufficient-hard-limit"
			return telemetry, fmt.Errorf("Go build-cache family remains above hard limit after selected-namespace maintenance (%d > %d); clean inactive owners explicitly", maintainedFamily.Bytes, cfg.HardLimitBytes)
		}
		telemetry.Decision = "cleaned-selected-namespace"
	} else if family.Bytes > cfg.SoftLimitBytes {
		telemetry.Decision = "warn-soft-limit"
	}
	return telemetry, nil
}

func Finish(cfg Config, telemetry Telemetry) (Telemetry, error) {
	after, err := managedStats(cfg, true)
	if err != nil {
		return telemetry, err
	}
	family, err := managedStats(cfg, false)
	if err != nil {
		return telemetry, err
	}
	telemetry.After = after
	telemetry.DeltaBytes = after.Bytes - telemetry.Before.Bytes
	telemetry.DeltaFiles = after.Files - telemetry.Before.Files
	telemetry.FamilyBytes = family.Bytes
	if family.Bytes > cfg.HardLimitBytes {
		telemetry.Decision = "exceeded-hard-limit-after-run"
	} else if family.Bytes > cfg.SoftLimitBytes && telemetry.Decision == "within-limits" {
		telemetry.Decision = "warn-soft-limit"
	}
	return telemetry, nil
}

func CleanPath(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("Go cache path is required")
	}
	dir, _, err := openCacheRoot(path, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	return cleanDir(ctx, dir, path)
}

// CleanManaged invokes Go's supported cleanup against a namespace opened
// component-by-component beneath the configured root without following links.
func CleanManaged(ctx context.Context, cfg Config) error {
	dir, err := openManagedDir(cfg, []string{"caches", LayoutVersion, string(cfg.Kind), cfg.Owner}, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	return cleanDir(ctx, dir, cfg.CachePath())
}

func cleanDir(ctx context.Context, dir *os.File, displayPath string) error {
	dupFD, err := unix.Openat(int(dir.Fd()), ".", secureOpenFlags, 0)
	if err != nil {
		return fmt.Errorf("duplicate Go cache namespace descriptor: %w", err)
	}
	childDir := os.NewFile(uintptr(dupFD), "go-cache")
	if childDir == nil {
		_ = unix.Close(dupFD)
		return errors.New("wrap Go cache namespace descriptor")
	}
	defer childDir.Close()
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
	cmd.ExtraFiles = []*os.File{childDir}
	cmd.Env = replaceEnv(os.Environ(), "GOCACHE", "/dev/fd/3")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("supported Go cache cleanup for %s: %w: %s", displayPath, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// CleanupOwner removes all instrument variants for an inactive issue namespace.
func CleanupOwner(ctx context.Context, root, repoDir, issueID string) error {
	owner := sanitizeOwner("issue-" + issueID)
	if owner == "" || owner == "issue-" {
		return errors.New("issue ID is required for Go cache cleanup")
	}
	exists, err := ownerCacheExists(root, owner)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	active, err := ownerActive(ctx, repoDir, issueID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("refusing Go cache cleanup for live worktree/session owner %s", owner)
	}
	return CleanupInactiveOwner(ctx, root, issueID)
}

// CleanupInactiveOwner is for the authoritative lifecycle path after it has
// successfully removed the worktree and stopped its managed session.
func CleanupInactiveOwner(ctx context.Context, root, issueID string) error {
	owner := sanitizeOwner("issue-" + issueID)
	if owner == "" || owner == "issue-" {
		return errors.New("issue ID is required for Go cache cleanup")
	}
	cfg := Config{Root: filepath.Clean(root), Owner: owner, Kind: KindNormal, SoftLimitBytes: DefaultSoftLimitBytes, HardLimitBytes: DefaultHardLimitBytes}
	anyExists, err := ownerCacheExists(root, owner)
	if err != nil {
		return err
	}
	if !anyExists {
		return nil
	}
	return WithExclusiveLock(ctx, cfg, func() error {
		for _, kind := range []Kind{KindNormal, KindRace, KindCoverage} {
			cfg.Kind = kind
			if err := cleanupManagedNamespace(ctx, cfg); err != nil {
				return err
			}
		}
		return nil
	})
}

func cleanupManagedNamespace(ctx context.Context, cfg Config) error {
	kindDir, err := openManagedDir(cfg, []string{"caches", LayoutVersion, string(cfg.Kind)}, false)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer kindDir.Close()
	ownerDir, err := openChildDir(kindDir, cfg.Owner, false)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer ownerDir.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(int(ownerDir.Fd()), &opened); err != nil {
		return fmt.Errorf("inspect opened Go cache namespace %s: %w", cfg.CachePath(), err)
	}
	if err := cleanDir(ctx, ownerDir, cfg.CachePath()); err != nil {
		return err
	}
	if cleanupOwnerBeforeRemoveHook != nil {
		cleanupOwnerBeforeRemoveHook(cfg.CachePath())
	}
	if err := verifyDirEntry(kindDir, cfg.Owner, &opened); err != nil {
		return err
	}
	if err := removeDirContents(ownerDir); err != nil {
		return fmt.Errorf("remove cleaned Go cache namespace contents %s: %w", cfg.CachePath(), err)
	}
	if err := verifyDirEntry(kindDir, cfg.Owner, &opened); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(kindDir.Fd()), cfg.Owner, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove cleaned Go cache namespace %s: %w", cfg.CachePath(), err)
	}
	return nil
}

func verifyDirEntry(parent *os.File, name string, opened *unix.Stat_t) error {
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("verify Go cache namespace %s during cleanup: %w", name, err)
	}
	if !sameFileIdentity(opened, &current) {
		return fmt.Errorf("Go cache namespace changed during cleanup at %s", name)
	}
	return nil
}

func ownerCacheExists(root, owner string) (bool, error) {
	for _, kind := range []Kind{KindNormal, KindRace, KindCoverage} {
		cfg := Config{Root: filepath.Clean(root), Owner: owner, Kind: kind}
		dir, err := openManagedDir(cfg, []string{"caches", LayoutVersion, string(kind), owner}, false)
		if err == nil {
			_ = dir.Close()
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOENT) {
			return false, fmt.Errorf("inspect Go cache namespace for owner %s: %w", owner, err)
		}
	}
	return false, nil
}

func ownerActive(ctx context.Context, repoDir, issueID string) (bool, error) {
	if strings.TrimSpace(repoDir) == "" {
		return false, errors.New("repository directory is required to verify Go cache ownership")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "worktree", "list", "--porcelain")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("list worktrees before Go cache cleanup: %w: %s", err, strings.TrimSpace(string(output)))
	}
	needle := strings.ToLower(strings.TrimSpace(issueID))
	for _, block := range strings.Split(strings.ToLower(string(output)), "\n\n") {
		if strings.Contains(block, "/"+needle+"/") || strings.Contains(block, "-"+needle+"\n") {
			return true, nil
		}
	}
	tmux := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", "#{session_name}")
	tmuxOutput, tmuxErr := tmux.CombinedOutput()
	if tmuxErr != nil {
		message := strings.ToLower(string(tmuxOutput))
		if errors.Is(tmuxErr, exec.ErrNotFound) || errors.Is(tmuxErr, os.ErrNotExist) || strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") {
			return false, nil
		}
		return false, fmt.Errorf("list tmux sessions before Go cache cleanup: %w: %s", tmuxErr, strings.TrimSpace(string(tmuxOutput)))
	}
	for _, sessionName := range strings.Fields(string(tmuxOutput)) {
		parsed, ok := naming.ParseIssueIDFromSessionName(sessionName, repoDir)
		if ok && naming.IssueIDsEqual(parsed, issueID) {
			return true, nil
		}
	}
	return false, nil
}

func LegacyPaths(root string) []string {
	repoRoot := repositoryRoot(root)
	return []string{filepath.Join(root, "build-cache"), filepath.Join(repoRoot, ".gocache"), filepath.Join(repoRoot, ".gopath")}
}

func repositoryRoot(root string) string {
	clean := filepath.Clean(root)
	if filepath.Base(clean) == "go" && filepath.Base(filepath.Dir(clean)) == ".azedarach" {
		return filepath.Dir(filepath.Dir(clean))
	}
	return filepath.Dir(clean)
}

func envBytes(key string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func sanitizeOwner(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}
