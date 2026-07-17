package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/gocache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunManagedSelectsInstrumentNamespace(t *testing.T) {
	repo := initCommandTestRepo(t)
	t.Chdir(repo)
	root := configureCommandTestCacheFamily(t, repo)
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", root)
	t.Setenv("AZEDARACH_GOCACHE", "")
	t.Setenv("AZEDARACH_GO_CACHE_OWNER", "issue-dhc")
	t.Setenv("AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES", "1024")
	t.Setenv("AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES", "2048")
	output := filepath.Join(t.TempDir(), "gocache.txt")

	err := runManaged(context.Background(), []string{"--kind", "coverage", "--", "sh", "-c", `printf '%s' "$GOCACHE" > "$1"`, "sh", output})
	require.NoError(t, err)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "caches", "v1", "coverage", "issue-dhc"), string(data))
}

func TestRunManagedRejectsUnknownKindAndMissingCommand(t *testing.T) {
	repo := initCommandTestRepo(t)
	t.Chdir(repo)
	configureCommandTestCacheFamily(t, repo)
	assert.ErrorContains(t, runManaged(context.Background(), []string{"--kind", "instrumented", "--", "true"}), "unsupported")
	assert.ErrorContains(t, runManaged(context.Background(), []string{"--kind", "normal"}), "requires")
}

func TestCollectInventoryReportsManagedAndLegacyPathsSeparately(t *testing.T) {
	repo := initCommandTestRepo(t)
	root := configureCommandTestCacheFamily(t, repo)
	cfg := gocache.Config{Root: root, Owner: "main", Kind: gocache.KindNormal}
	paths := append([]string{cfg.LayoutRoot()}, gocache.LegacyPaths(root)...)
	for index, path := range paths[:3] {
		require.NoError(t, os.MkdirAll(path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(path, "entry"), make([]byte, index+1), 0o644))
	}

	items, err := collectInventory(cfg)
	require.NoError(t, err)
	require.Len(t, items, 4)
	for index, item := range items {
		assert.Equal(t, paths[index], item.Path)
		if index < 3 {
			assert.True(t, item.Exists)
			assert.EqualValues(t, index+1, item.Stats.Bytes)
		} else {
			assert.False(t, item.Exists)
			assert.Equal(t, gocache.Stats{}, item.Stats)
		}
		if index == 0 {
			assert.Equal(t, "managed", item.Kind)
		} else {
			assert.Equal(t, "legacy", item.Kind)
		}
	}
}

func configureCommandTestCacheFamily(t *testing.T, repo string) string {
	t.Helper()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	root := filepath.Join(canonicalRepo, ".azedarach", "go")
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", root)
	t.Setenv("AZEDARACH_GOCACHE", "")
	return root
}

func initCommandTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	return repo
}
