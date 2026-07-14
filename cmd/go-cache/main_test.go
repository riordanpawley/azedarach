package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunManagedSelectsInstrumentNamespace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", root)
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
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", t.TempDir())
	assert.ErrorContains(t, runManaged(context.Background(), []string{"--kind", "instrumented", "--", "true"}), "unsupported")
	assert.ErrorContains(t, runManaged(context.Background(), []string{"--kind", "normal"}), "requires")
}
