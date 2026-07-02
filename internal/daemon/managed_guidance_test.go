package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedGuidanceBlockCreateUpdateIdempotentAndRetire(t *testing.T) {
	repoDir := t.TempDir()
	learning := protocol.Learning{
		ID:       "learn-1",
		Summary:  "Use daemon-owned guidance blocks",
		Target:   protocol.LearningPromotionTargetAgents,
		TargetID: "AGENTS.md",
	}

	path := filepath.Join(repoDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("# Human guidance\n\nKeep this line.\n"), 0o644))

	created, err := upsertManagedGuidanceBlock(repoDir, learning, "First promotion.", "")
	require.NoError(t, err)
	require.True(t, created.Changed)
	require.NotEmpty(t, created.TargetHash)
	firstBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	first := string(firstBytes)
	assert.Contains(t, first, "# Human guidance")
	assert.Contains(t, first, "Source learning: learn-1")
	assert.Equal(t, 1, strings.Count(first, managedGuidanceStartPrefix("learn-1")))

	duplicate, err := upsertManagedGuidanceBlock(repoDir, learning, "First promotion.", created.TargetHash)
	require.NoError(t, err)
	assert.False(t, duplicate.Changed)
	duplicateBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first, string(duplicateBytes))

	updated, err := upsertManagedGuidanceBlock(repoDir, learning, "Updated promotion.", created.TargetHash)
	require.NoError(t, err)
	assert.True(t, updated.Changed)
	assert.NotEqual(t, created.TargetHash, updated.TargetHash)
	updatedBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	updatedContent := string(updatedBytes)
	assert.Contains(t, updatedContent, "Updated promotion.")
	assert.NotContains(t, updatedContent, "First promotion.")
	assert.Equal(t, 1, strings.Count(updatedContent, managedGuidanceStartPrefix("learn-1")))

	retired, err := removeManagedGuidanceBlock(repoDir, learning, updated.TargetHash)
	require.NoError(t, err)
	assert.True(t, retired.Changed)
	retiredBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	retiredContent := string(retiredBytes)
	assert.Contains(t, retiredContent, "# Human guidance")
	assert.NotContains(t, retiredContent, "Source learning: learn-1")
}

func TestManagedGuidanceBlockRefusesDrift(t *testing.T) {
	repoDir := t.TempDir()
	learning := protocol.Learning{
		ID:       "learn-1",
		Summary:  "Original managed guidance",
		Target:   protocol.LearningPromotionTargetAgents,
		TargetID: "AGENTS.md",
	}

	created, err := upsertManagedGuidanceBlock(repoDir, learning, "", "")
	require.NoError(t, err)
	path := filepath.Join(repoDir, "AGENTS.md")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	drifted := strings.Replace(string(raw), "Original managed guidance", "Human edited managed guidance", 1)
	require.NoError(t, os.WriteFile(path, []byte(drifted), 0o644))

	learning.Summary = "Updated managed guidance"
	_, err = upsertManagedGuidanceBlock(repoDir, learning, "", created.TargetHash)
	require.ErrorIs(t, err, domain.ErrConflict)
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, drifted, string(after))

	_, err = removeManagedGuidanceBlock(repoDir, learning, created.TargetHash)
	require.ErrorIs(t, err, domain.ErrConflict)
	afterRetire, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, drifted, string(afterRetire))
}

func TestManagedGuidanceBlockRejectsUnsafeTargetPaths(t *testing.T) {
	repoDir := t.TempDir()
	learning := protocol.Learning{
		ID:       "learn-1",
		Summary:  "Unsafe path",
		Target:   protocol.LearningPromotionTargetAgents,
		TargetID: "../AGENTS.md",
	}

	_, err := upsertManagedGuidanceBlock(repoDir, learning, "", "")
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestManagedGuidanceBlockRejectsSymlinkTargetPaths(t *testing.T) {
	repoDir := t.TempDir()
	outsideDir := t.TempDir()
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(repoDir, "linked")))

	learning := protocol.Learning{
		ID:       "learn-1",
		Summary:  "Symlink path",
		Target:   protocol.LearningPromotionTargetAgents,
		TargetID: "linked/AGENTS.md",
	}

	_, err := upsertManagedGuidanceBlock(repoDir, learning, "", "")
	require.ErrorIs(t, err, domain.ErrConflict)
	_, statErr := os.Stat(filepath.Join(outsideDir, "AGENTS.md"))
	require.True(t, os.IsNotExist(statErr), "symlink target must not be written")
}
