package testprofile

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalFixtureProfilesAreReusable(t *testing.T) {
	profiles := []Profile{Smoke, Integration, Scale}

	for _, profile := range profiles {
		profile := profile
		t.Run(profile.Name, func(t *testing.T) {
			require.NotEmpty(t, profile.Tasks)
			require.NotEmpty(t, profile.BaseBranch)
			assert.Greater(t, profile.Width, 0)
			assert.Greater(t, profile.Height, 0)

			cloned := append([]domain.Task(nil), profile.Tasks...)
			require.NotEmpty(t, cloned)
			cloned[0].Title = "mutated"
			assert.NotEqual(t, cloned[0].Title, profile.Tasks[0].Title, "fixture tasks must be safe to copy and reuse")
		})
	}
}

func TestCanonicalFixtureProfilesIncludeDependencyVariants(t *testing.T) {
	t.Run("smoke", func(t *testing.T) {
		require.Len(t, Smoke.Tasks, 4)
		assert.Empty(t, Smoke.Tasks[0].Dependencies)
		assert.Len(t, Smoke.Tasks[1].Dependencies, 1)
		assert.Equal(t, domain.DependencyBlocks, Smoke.Tasks[1].Dependencies[0].Type)
		assert.Equal(t, "az-smoke-root", Smoke.Tasks[1].Dependencies[0].ID.String())
		assert.Equal(t, domain.TypeEpic, Smoke.Tasks[2].Type)
		assert.Len(t, Smoke.Tasks[3].Dependencies, 1)
	})

	t.Run("integration", func(t *testing.T) {
		require.Len(t, Integration.Tasks, 4)
		assert.Len(t, Integration.Tasks[2].Dependencies, 2)
		assert.ElementsMatch(t, []string{"az-int-root-a", "az-int-root-b"}, []string{Integration.Tasks[2].Dependencies[0].ID.String(), Integration.Tasks[2].Dependencies[1].ID.String()})
		assert.Equal(t, domain.StatusBlocked, Integration.Tasks[3].Status)
	})

	t.Run("scale", func(t *testing.T) {
		require.Greater(t, len(Scale.Tasks), 4)
		assert.Len(t, Scale.Tasks[2].Dependencies, 1)
		assert.Len(t, Scale.Tasks[3].Dependencies, 1)
		assert.Equal(t, "az-scale-child", Scale.Tasks[3].Dependencies[0].ID.String())
		assert.Equal(t, "az-scale-root", Scale.Tasks[2].Dependencies[0].ID.String())
	})
}

func TestCanonicalFixtureProfilesCoverTerminalAndBranchVariance(t *testing.T) {
	require.Less(t, Smoke.Width, Integration.Width)
	require.Less(t, Integration.Width, Scale.Width)
	require.NotEqual(t, Smoke.BaseBranch, Integration.BaseBranch)
	require.NotEqual(t, Integration.BaseBranch, Scale.BaseBranch)
	require.NotEqual(t, Smoke.BaseBranch, Scale.BaseBranch)

	for _, profile := range []Profile{Smoke, Integration, Scale} {
		profile := profile
		t.Run(profile.Name, func(t *testing.T) {
			require.NotEmpty(t, profile.Tasks)
			assert.Greater(t, profile.Width, 0)
			assert.Greater(t, profile.Height, 0)

			// The fixtures are intended to be copied into models, so ensure the
			// task list can be cloned without aliasing the original data.
			cloned := append([]domain.Task(nil), profile.Tasks...)
			require.NotEmpty(t, cloned)
			cloned[0].Status = domain.StatusDone
			assert.NotEqual(t, cloned[0].Status, profile.Tasks[0].Status)
		})
	}
}
