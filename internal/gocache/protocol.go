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
	root := strings.TrimSpace(os.Getenv("AZEDARACH_GO_CACHE_ROOT"))
	if root == "" {
		cache := strings.TrimSpace(os.Getenv("GOCACHE"))
		for _, directory := range []string{"caches", "build-cache"} {
			marker := string(filepath.Separator) + directory + string(filepath.Separator)
			if idx := strings.Index(cache, marker); idx >= 0 {
				root = cache[:idx]
				break
			}
		}
	}
	if root == "" {
		cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
		if output, err := cmd.Output(); err == nil {
			commonDir := strings.TrimSpace(string(output))
			if filepath.Base(commonDir) == ".git" {
				root = filepath.Join(filepath.Dir(commonDir), ".azedarach", "go")
			}
		}
	}
	if root == "" {
		return Config{}, errors.New("AZEDARACH_GO_CACHE_ROOT is required outside a Git repository")
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
	return Config{Root: filepath.Clean(root), Owner: owner, Kind: kind, SoftLimitBytes: soft, HardLimitBytes: hard}, nil
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

// WithExclusiveLock serializes managed validation and supported cache maintenance.
func WithExclusiveLock(ctx context.Context, cfg Config, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(cfg.LockPath()), 0o755); err != nil {
		return fmt.Errorf("create Go cache lock directory: %w", err)
	}
	file, err := os.OpenFile(cfg.LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open Go cache maintenance lock: %w", err)
	}
	defer file.Close()
	for {
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
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
	before, err := StatsFor(cfg.CachePath())
	if err != nil {
		return telemetry, fmt.Errorf("measure selected Go cache: %w", err)
	}
	family, err := StatsFor(cfg.LayoutRoot())
	if err != nil {
		return telemetry, fmt.Errorf("measure Go cache family: %w", err)
	}
	telemetry.Before, telemetry.FamilyBytes = before, family.Bytes
	if family.Bytes > cfg.HardLimitBytes {
		if !autoMaintain {
			telemetry.Decision = "refused-hard-limit"
			return telemetry, fmt.Errorf("Go build-cache family uses %d bytes, above hard limit %d; run `just go-cache-maintain` or set AZEDARACH_GO_CACHE_AUTO_MAINTAIN=1", family.Bytes, cfg.HardLimitBytes)
		}
		if err := CleanPath(ctx, cfg.CachePath()); err != nil {
			telemetry.Decision = "maintenance-failed"
			return telemetry, err
		}
		maintainedFamily, measureErr := StatsFor(cfg.LayoutRoot())
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
	after, err := StatsFor(cfg.CachePath())
	if err != nil {
		return telemetry, err
	}
	family, err := StatsFor(cfg.LayoutRoot())
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
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing supported Go cache cleanup through symlink namespace %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Go cache namespace %s: %w", path, err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create Go cache namespace: %w", err)
	}
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
	cmd.Env = replaceEnv(os.Environ(), "GOCACHE", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("supported Go cache cleanup for %s: %w: %s", path, err, strings.TrimSpace(string(output)))
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
			path := filepath.Join(cfg.LayoutRoot(), string(kind), owner)
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err := CleanPath(ctx, path); err != nil {
				return err
			}
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove cleaned Go cache namespace %s: %w", path, err)
			}
		}
		return nil
	})
}

func ownerCacheExists(root, owner string) (bool, error) {
	layoutRoot := filepath.Join(filepath.Clean(root), "caches", LayoutVersion)
	for _, kind := range []Kind{KindNormal, KindRace, KindCoverage} {
		if _, err := os.Stat(filepath.Join(layoutRoot, string(kind), owner)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
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
