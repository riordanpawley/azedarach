package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectDefaultsDoNotCreateStaleWorktreeArtifacts(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	data, err := os.ReadFile(filepath.Join(repoRoot, ConfigDirName, ConfigFileName))
	require.NoError(t, err)
	var projectDefaults struct {
		Worktree struct {
			SyncInitCommands  []string `json:"syncInitCommands"`
			AsyncInitCommands []string `json:"asyncInitCommands"`
		} `json:"worktree"`
	}
	require.NoError(t, json.Unmarshal(data, &projectDefaults))
	assert.Equal(t, []string{"direnv allow"}, projectDefaults.Worktree.SyncInitCommands)
	assert.Empty(t, projectDefaults.Worktree.AsyncInitCommands)

	for _, command := range projectDefaults.Worktree.SyncInitCommands {
		assert.NotContains(t, command, ".gocache")
		assert.NotContains(t, command, ".gopath")
	}

	assertFileExcludesText(t, filepath.Join(repoRoot, ".envrc"), "reference-effect")
	assertFileExcludesText(t, filepath.Join(repoRoot, ".codex", "config.toml"), "floop")
	assertFileExcludesText(t, filepath.Join(repoRoot, ".gitignore"), "/reference-effect")
}

func assertFileExcludesText(t *testing.T, path string, forbidden string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Falsef(t, strings.Contains(string(data), forbidden), "%s contains stale project default %q", path, forbidden)
}
