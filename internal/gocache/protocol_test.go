package gocache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromEnvironmentSelectsDistinctInstrumentNamespacesAndThresholds(t *testing.T) {
	root := RootForRepository(context.Background(), ".")
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", root)
	t.Setenv("AZEDARACH_GO_CACHE_OWNER", "issue-DHC")
	t.Setenv("AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES", "100")
	t.Setenv("AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES", "200")

	normal, err := FromEnvironment(KindNormal)
	require.NoError(t, err)
	race, err := FromEnvironment(KindRace)
	require.NoError(t, err)
	coverage, err := FromEnvironment(KindCoverage)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "caches", "v1", "normal", "issue-dhc"), normal.CachePath())
	assert.NotEqual(t, normal.CachePath(), race.CachePath())
	assert.NotEqual(t, race.CachePath(), coverage.CachePath())
	assert.EqualValues(t, 100, normal.SoftLimitBytes)
	assert.EqualValues(t, 200, normal.HardLimitBytes)
	assert.Equal(t, KindRace, KindForProfile("race"))
	assert.Equal(t, KindNormal, KindForProfile("cold"))
}

func TestFromEnvironmentUsesManagedCapacityDefaultsForAnyRepository(t *testing.T) {
	repo := initTestRepo(t)
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", "")
	t.Setenv("AZEDARACH_GOCACHE", "")
	t.Setenv("AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES", "")
	t.Setenv("AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES", "")

	cfg, err := FromEnvironmentForRepository(context.Background(), KindNormal, repo)
	require.NoError(t, err)
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	assert.Equal(t, int64(50)<<30, DefaultSoftLimitBytes)
	assert.Equal(t, int64(70)<<30, DefaultHardLimitBytes)
	assert.Equal(t, DefaultSoftLimitBytes, cfg.SoftLimitBytes)
	assert.Equal(t, DefaultHardLimitBytes, cfg.HardLimitBytes)
	assert.Equal(t, filepath.Join(canonicalRepo, ".azedarach", "go"), cfg.Root)
}

func TestPrepareEnforcesManagedCapacityBoundaries(t *testing.T) {
	const oldHardLimitBytes int64 = 28 << 30
	tests := []struct {
		name     string
		bytes    int64
		decision string
		wantErr  bool
	}{
		{name: "just over old limit is allowed", bytes: oldHardLimitBytes + 1, decision: "within-limits"},
		{name: "just under new limit is allowed", bytes: DefaultHardLimitBytes - 1, decision: "warn-soft-limit"},
		{name: "above new limit is refused", bytes: DefaultHardLimitBytes + 1, decision: "refused-hard-limit", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Root: t.TempDir(), Owner: "non-azedarach-consumer", Kind: KindNormal, SoftLimitBytes: DefaultSoftLimitBytes, HardLimitBytes: DefaultHardLimitBytes}
			require.NoError(t, os.MkdirAll(cfg.CachePath(), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(cfg.CachePath(), "sparse"), nil, 0o644))
			require.NoError(t, os.Truncate(filepath.Join(cfg.CachePath(), "sparse"), tt.bytes))

			telemetry, err := Prepare(context.Background(), cfg, false)
			if tt.wantErr {
				require.ErrorContains(t, err, fmt.Sprintf("above hard limit %d", DefaultHardLimitBytes))
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.decision, telemetry.Decision)
			assert.Equal(t, tt.bytes, telemetry.FamilyBytes)
			assert.Equal(t, DefaultSoftLimitBytes, telemetry.SoftLimitBytes)
			assert.Equal(t, DefaultHardLimitBytes, telemetry.HardLimitBytes)
		})
	}
}

func TestPrepareAutoMaintenanceUsesNewHardLimit(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Owner: "issue-dqb", Kind: KindNormal, SoftLimitBytes: DefaultSoftLimitBytes, HardLimitBytes: DefaultHardLimitBytes}
	require.NoError(t, os.MkdirAll(cfg.CachePath(), 0o755))
	sparse := filepath.Join(cfg.CachePath(), "sparse")
	require.NoError(t, os.WriteFile(sparse, nil, 0o644))
	require.NoError(t, os.Truncate(sparse, DefaultHardLimitBytes+1))
	legacy := filepath.Join(LegacyPaths(cfg.Root)[0], "preserved")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	require.NoError(t, os.WriteFile(legacy, []byte("legacy data"), 0o644))

	telemetry, err := Prepare(context.Background(), cfg, true)
	require.NoError(t, err)
	assert.Equal(t, "cleaned-selected-namespace", telemetry.Decision)
	assert.LessOrEqual(t, telemetry.FamilyBytes, DefaultHardLimitBytes)
	data, err := os.ReadFile(legacy)
	require.NoError(t, err)
	assert.Equal(t, "legacy data", string(data))
}

func TestPrepareRefusesHardLimitAndFinishReportsDeltas(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Owner: "issue-dhc", Kind: KindNormal, SoftLimitBytes: 4, HardLimitBytes: 8}
	require.NoError(t, os.MkdirAll(cfg.CachePath(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.CachePath(), "large"), []byte("0123456789"), 0o644))

	telemetry, err := Prepare(context.Background(), cfg, false)
	require.ErrorContains(t, err, "above hard limit")
	require.ErrorContains(t, err, "project's cache-maintenance workflow")
	assert.NotContains(t, err.Error(), "just")
	assert.Equal(t, "refused-hard-limit", telemetry.Decision)
	assert.EqualValues(t, 10, telemetry.Before.Bytes)

	require.NoError(t, os.Remove(filepath.Join(cfg.CachePath(), "large")))
	telemetry, err = Prepare(context.Background(), cfg, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.CachePath(), "new"), []byte("abc"), 0o644))
	telemetry, err = Finish(cfg, telemetry)
	require.NoError(t, err)
	assert.EqualValues(t, 3, telemetry.DeltaBytes)
	assert.EqualValues(t, 1, telemetry.DeltaFiles)
}

func TestPrepareAutoMaintenanceStillRefusesWhenAnotherOwnerExceedsHardLimit(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Owner: "issue-dhc", Kind: KindNormal, SoftLimitBytes: 4, HardLimitBytes: 8}
	other := filepath.Join(cfg.LayoutRoot(), "normal", "issue-other")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "large"), []byte("0123456789"), 0o644))

	telemetry, err := Prepare(context.Background(), cfg, true)
	require.ErrorContains(t, err, "remains above hard limit")
	assert.Equal(t, "maintenance-insufficient-hard-limit", telemetry.Decision)
}

func TestPrepareAutoMaintenanceCleansSelectedNamespaceAndUnblocksHardLimit(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Owner: "issue-dhc", Kind: KindNormal, SoftLimitBytes: 1, HardLimitBytes: 1}
	module := initTestRepo(t)
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = module
	cmd.Env = append(filteredEnvironment("GOCACHE"), "GOCACHE="+cfg.CachePath())
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)

	before, err := StatsManaged(cfg)
	require.NoError(t, err)
	require.Greater(t, before.Bytes, int64(1))

	telemetry, err := Prepare(context.Background(), cfg, true)
	require.NoError(t, err)
	assert.Equal(t, "cleaned-selected-namespace", telemetry.Decision)
	assert.Less(t, telemetry.FamilyBytes, before.Bytes)
	assert.LessOrEqual(t, telemetry.FamilyBytes, cfg.HardLimitBytes)
}

func TestExclusiveLockSerializesManagedValidation(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Owner: "issue-dhc", Kind: KindNormal}
	entered := make(chan struct{})
	release := make(chan struct{})
	var orderMu sync.Mutex
	var order []string
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- WithExclusiveLock(context.Background(), cfg, func() error {
			orderMu.Lock()
			order = append(order, "first")
			orderMu.Unlock()
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	done := make(chan error, 1)
	go func() {
		done <- WithExclusiveLock(context.Background(), cfg, func() error {
			orderMu.Lock()
			order = append(order, "second")
			orderMu.Unlock()
			return nil
		})
	}()
	select {
	case <-done:
		t.Fatal("second validation entered while lock was held")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-done)
	assert.Equal(t, []string{"first", "second"}, order)
}

func TestSharedLockAllowsConcurrentValidators(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Owner: "issue-dhc", Kind: KindNormal}
	require.NoError(t, WithSharedLock(context.Background(), cfg, func() error { return nil }))
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	done := make(chan error, 2)
	for range 2 {
		go func() {
			done <- WithSharedLock(context.Background(), cfg, func() error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("shared validator did not enter concurrently")
		}
	}
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
}

func TestSharedLockConcurrentInitializesCacheRoot(t *testing.T) {
	cfg := Config{Root: filepath.Join(t.TempDir(), "new-root"), Owner: "issue-dhc", Kind: KindNormal}
	start := make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			done <- WithSharedLock(context.Background(), cfg, func() error { return nil })
		}()
	}
	close(start)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
}

func TestValidationLockUsesExclusiveModeForAutoMaintenance(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Owner: "issue-dhc", Kind: KindNormal, SoftLimitBytes: 100, HardLimitBytes: 200}
	sharedEntered := make(chan struct{})
	releaseShared := make(chan struct{})
	sharedDone := make(chan error, 1)
	go func() {
		sharedDone <- WithSharedLock(context.Background(), cfg, func() error {
			close(sharedEntered)
			<-releaseShared
			return nil
		})
	}()
	<-sharedEntered
	autoEntered := make(chan struct{})
	autoDone := make(chan error, 1)
	go func() {
		autoDone <- WithValidationLock(context.Background(), cfg, true, func(_ Telemetry, err error) error {
			close(autoEntered)
			return err
		})
	}()
	select {
	case <-autoEntered:
		t.Fatal("auto-maintaining validator entered while shared validator held cache lock")
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseShared)
	require.NoError(t, <-sharedDone)
	select {
	case <-autoEntered:
	case <-time.After(time.Second):
		t.Fatal("auto-maintaining validator did not enter after shared validator released")
	}
	require.NoError(t, <-autoDone)
}

func TestCleanupInactiveOwnerRemovesOnlySelectedVariants(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []Kind{KindNormal, KindRace, KindCoverage} {
		for _, owner := range []string{"issue-dhc", "issue-other"} {
			path := filepath.Join(root, "caches", "v1", string(kind), owner)
			require.NoError(t, os.MkdirAll(path, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(path, "entry"), []byte("cache"), 0o644))
		}
	}
	require.NoError(t, CleanupInactiveOwner(context.Background(), root, "dhc"))
	for _, kind := range []Kind{KindNormal, KindRace, KindCoverage} {
		_, err := os.Stat(filepath.Join(root, "caches", "v1", string(kind), "issue-dhc"))
		assert.ErrorIs(t, err, os.ErrNotExist)
		_, err = os.Stat(filepath.Join(root, "caches", "v1", string(kind), "issue-other", "entry"))
		assert.NoError(t, err)
	}
}

func TestCleanupInactiveOwnerRefusesSymlinkNamespace(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	entry := filepath.Join(target, "keep")
	require.NoError(t, os.WriteFile(entry, []byte("user-data"), 0o644))
	namespace := filepath.Join(root, "caches", "v1", "normal", "issue-dhc")
	require.NoError(t, os.MkdirAll(filepath.Dir(namespace), 0o755))
	require.NoError(t, os.Symlink(target, namespace))

	err := CleanupInactiveOwner(context.Background(), root, "dhc")
	require.ErrorContains(t, err, "symlink namespace")
	data, readErr := os.ReadFile(entry)
	require.NoError(t, readErr)
	assert.Equal(t, "user-data", string(data))
}

func TestCleanupInactiveOwnerRefusesAncestorSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	entry := filepath.Join(target, "v1", "normal", "issue-dhc", "keep")
	require.NoError(t, os.MkdirAll(filepath.Dir(entry), 0o755))
	require.NoError(t, os.WriteFile(entry, []byte("user-data"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(root, "caches")))

	err := CleanupInactiveOwner(context.Background(), root, "dhc")
	require.ErrorContains(t, err, "symlink namespace")
	data, readErr := os.ReadFile(entry)
	require.NoError(t, readErr)
	assert.Equal(t, "user-data", string(data))
}

func TestCleanupInactiveOwnerRejectsRootAncestorSwap(t *testing.T) {
	base := t.TempDir()
	ancestor := filepath.Join(base, "family")
	root := filepath.Join(ancestor, "go")
	namespace := filepath.Join(root, "caches", "v1", "normal", "issue-dhc")
	require.NoError(t, os.MkdirAll(namespace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(namespace, "cache"), []byte("owned"), 0o644))

	external := t.TempDir()
	externalNamespace := filepath.Join(external, "go", "caches", "v1", "normal", "issue-dhc")
	require.NoError(t, os.MkdirAll(externalNamespace, 0o755))
	keep := filepath.Join(externalNamespace, "keep")
	require.NoError(t, os.WriteFile(keep, []byte("outside"), 0o644))

	openCacheRootAfterCanonicalizeHook = func(string) {
		require.NoError(t, os.Rename(ancestor, ancestor+".moved"))
		require.NoError(t, os.Symlink(external, ancestor))
		openCacheRootAfterCanonicalizeHook = nil
	}
	t.Cleanup(func() { openCacheRootAfterCanonicalizeHook = nil })

	err := CleanupInactiveOwner(context.Background(), root, "dhc")
	require.ErrorContains(t, err, "symlink namespace")
	data, readErr := os.ReadFile(keep)
	require.NoError(t, readErr)
	assert.Equal(t, "outside", string(data))
}

func TestCleanupInactiveOwnerDetectsNamespaceSwap(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	keep := filepath.Join(target, "keep")
	require.NoError(t, os.WriteFile(keep, []byte("outside"), 0o644))
	namespace := filepath.Join(root, "caches", "v1", "normal", "issue-dhc")
	require.NoError(t, os.MkdirAll(namespace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(namespace, "entry"), []byte("cache"), 0o644))

	cleanupOwnerBeforeRemoveHook = func(path string) {
		require.Equal(t, namespace, path)
		require.NoError(t, os.Rename(namespace, namespace+".moved"))
		require.NoError(t, os.Symlink(target, namespace))
	}
	t.Cleanup(func() { cleanupOwnerBeforeRemoveHook = nil })

	err := CleanupInactiveOwner(context.Background(), root, "dhc")
	require.ErrorContains(t, err, "changed during cleanup")
	data, readErr := os.ReadFile(keep)
	require.NoError(t, readErr)
	assert.Equal(t, "outside", string(data))
}

func TestCleanupOwnerRefusesLiveWorktree(t *testing.T) {
	repo := initTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "repo-dhc")
	runGit(t, repo, "worktree", "add", "-b", "tester/dhc/live-owner", worktree, "HEAD")
	root := filepath.Join(repo, ".azedarach", "go")
	entry := filepath.Join(root, "caches", "v1", "normal", "issue-dhc", "entry")
	require.NoError(t, os.MkdirAll(filepath.Dir(entry), 0o755))
	require.NoError(t, os.WriteFile(entry, []byte("cache"), 0o644))

	err := CleanupOwner(context.Background(), root, repo, "dhc")
	require.ErrorContains(t, err, "live worktree/session owner")
	_, statErr := os.Stat(entry)
	assert.NoError(t, statErr)

	runGit(t, repo, "worktree", "remove", worktree)
	require.NoError(t, CleanupOwner(context.Background(), root, repo, "dhc"))
	_, statErr = os.Stat(filepath.Dir(entry))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestCleanupOwnerRefusesLiveTmuxSessionWithoutWorktree(t *testing.T) {
	repo := initTestRepo(t)
	root := filepath.Join(repo, ".azedarach", "go")
	entry := filepath.Join(root, "caches", "v1", "normal", "issue-dhc", "entry")
	require.NoError(t, os.MkdirAll(filepath.Dir(entry), 0o755))
	require.NoError(t, os.WriteFile(entry, []byte("cache"), 0o644))
	bin := t.TempDir()
	tmux := filepath.Join(bin, "tmux")
	sessionName := "" + filepath.Base(repo)[:2] + "-dhc"
	require.NoError(t, os.WriteFile(tmux, []byte("#!/bin/sh\nprintf '%s\\n' '"+sessionName+"'\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := CleanupOwner(context.Background(), root, repo, "dhc")
	require.ErrorContains(t, err, "live worktree/session owner")
	_, statErr := os.Stat(entry)
	assert.NoError(t, statErr)
}

func TestLegacyPathsTargetRepositoryRootWithoutOverlappingManagedLayout(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, ".azedarach", "go")
	paths := LegacyPaths(root)
	assert.Equal(t, []string{
		filepath.Join(root, "build-cache"),
		filepath.Join(repo, ".gocache"),
		filepath.Join(repo, ".gopath"),
	}, paths)
	assert.NotContains(t, paths, filepath.Join(root, "caches", "v1"))
}

func TestStatsManagedExcludesLegacyFootprint(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, ".azedarach", "go")
	cfg := Config{Root: root, Owner: "main", Kind: KindNormal}
	require.NoError(t, os.MkdirAll(cfg.CachePath(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.CachePath(), "managed"), []byte("managed"), 0o644))
	for _, path := range LegacyPaths(root) {
		require.NoError(t, os.MkdirAll(path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(path, "legacy"), []byte("a much larger legacy footprint"), 0o644))
	}

	stats, err := StatsManaged(cfg)
	require.NoError(t, err)
	assert.EqualValues(t, len("managed"), stats.Bytes)
	assert.EqualValues(t, 1, stats.Files)
}

func TestRootForRepositoryUsesGitCommonDirectoryFromLinkedWorktree(t *testing.T) {
	repo := initTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "repo-dhc")
	runGit(t, repo, "worktree", "add", "-b", "tester/dhc/root-resolution", worktree, "HEAD")
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(canonicalRepo, ".azedarach", "go"), RootForRepository(context.Background(), worktree))
}

func TestShellProtocolUsesMainAndIssueNamespacesWithSharedGOPATHAndTrimpath(t *testing.T) {
	repo := initTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "repo-dhc")
	runGit(t, repo, "worktree", "add", "-b", "tester/dhc/cache-protocol", worktree, "HEAD")
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "go-cache-env.sh"))
	require.NoError(t, err)
	cacheRoot := RootForRepository(context.Background(), repo)

	mainOutput := runShellProtocol(t, repo, script, cacheRoot)
	issueOutput := runShellProtocol(t, worktree, script, cacheRoot)
	assert.Contains(t, mainOutput, "AZEDARACH_GO_CACHE_NAMESPACE=normal/main")
	assert.Contains(t, issueOutput, "AZEDARACH_GO_CACHE_NAMESPACE=normal/issue-dhc")
	assert.Contains(t, mainOutput, "GOPATH="+filepath.Join(cacheRoot, "path"))
	assert.Contains(t, issueOutput, "GOPATH="+filepath.Join(cacheRoot, "path"))
	assert.Contains(t, mainOutput, "GOFLAGS=-trimpath")
	assert.Contains(t, issueOutput, "GOFLAGS=-trimpath")
}

func TestShellProtocolUsesMainNamespaceForAnonymousDetachedWorktree(t *testing.T) {
	repo := initTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "integration-scratch")
	runGit(t, repo, "worktree", "add", "--detach", worktree, "HEAD")
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "go-cache-env.sh"))
	require.NoError(t, err)
	output := runShellProtocol(t, worktree, script, RootForRepository(context.Background(), repo))
	assert.Contains(t, output, "AZEDARACH_GO_CACHE_NAMESPACE=normal/main")
}

func TestFromEnvironmentRejectsOutOfLayoutOverride(t *testing.T) {
	root := RootForRepository(context.Background(), ".")
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", root)
	t.Setenv("AZEDARACH_GO_CACHE_OWNER", "issue-dhc")
	t.Setenv("AZEDARACH_GOCACHE", filepath.Join(t.TempDir(), "unmanaged"))

	_, err := FromEnvironment(KindNormal)
	require.ErrorContains(t, err, "must equal managed namespace")
}

func TestFromEnvironmentRejectsCacheRootOverride(t *testing.T) {
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", t.TempDir())
	_, err := FromEnvironmentForRepository(context.Background(), KindNormal, ".")
	require.ErrorContains(t, err, "must equal daemon-authoritative project root")
}

func TestShellProtocolRejectsOutOfLayoutOverride(t *testing.T) {
	repo := initTestRepo(t)
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "go-cache-env.sh"))
	require.NoError(t, err)
	cacheRoot := RootForRepository(context.Background(), repo)
	cmd := exec.Command("bash", script, "--print")
	cmd.Dir = repo
	cmd.Env = append(filteredEnvironment("AZEDARACH_GOCACHE"),
		"AZEDARACH_GO_CACHE_ROOT="+cacheRoot,
		"AZEDARACH_GOCACHE="+filepath.Join(t.TempDir(), "unmanaged"),
	)
	output, runErr := cmd.CombinedOutput()
	require.Error(t, runErr)
	assert.Contains(t, string(output), "must equal managed namespace")
}

func TestShellProtocolRejectsCacheRootOverride(t *testing.T) {
	repo := initTestRepo(t)
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "go-cache-env.sh"))
	require.NoError(t, err)
	cmd := exec.Command("bash", script, "--print")
	cmd.Dir = repo
	cmd.Env = append(filteredEnvironment("AZEDARACH_GO_CACHE_ROOT"), "AZEDARACH_GO_CACHE_ROOT="+t.TempDir())
	output, runErr := cmd.CombinedOutput()
	require.Error(t, runErr)
	assert.Contains(t, string(output), "must equal daemon-authoritative project root")
}

func TestTrimpathProducesEquivalentBinariesAcrossWorktreesAtSameCommit(t *testing.T) {
	repo := initTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "repo-dhc")
	runGit(t, repo, "worktree", "add", "-b", "tester/dhc/equivalence", worktree, "HEAD")
	first := buildHash(t, repo, filepath.Join(t.TempDir(), "cache-main"))
	second := buildHash(t, worktree, filepath.Join(t.TempDir(), "cache-issue"))
	assert.Equal(t, first, second)
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.test")
	runGit(t, repo, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test/cache\n\ngo 1.24.2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"stable\") }\n"), 0o644))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "fixture")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func runShellProtocol(t *testing.T, dir, script, cacheRoot string) string {
	t.Helper()
	cmd := exec.Command("bash", script, "--print")
	cmd.Dir = dir
	cmd.Env = append(filteredEnvironment("AZEDARACH_TICKET_ID", "AZEDARACH_ISSUE_ID", "AZEDARACH_GOCACHE", "AZEDARACH_GOPATH", "GOCACHE", "GOFLAGS"), "AZEDARACH_GO_CACHE_ROOT="+cacheRoot)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	return string(output)
}

func buildHash(t *testing.T, dir, cache string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "app")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(filteredEnvironment("GOCACHE", "GOFLAGS"), "GOCACHE="+cache)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func filteredEnvironment(keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
	}
	out := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			out = append(out, item)
		}
	}
	return out
}
