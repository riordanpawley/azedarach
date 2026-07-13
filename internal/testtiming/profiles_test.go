package testtiming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalProfilesMakeCacheAndScopeExplicit(t *testing.T) {
	tests := []struct {
		name       string
		cleanCache bool
		wantArg    string
		wantPkg    string
	}{
		{name: "cold", cleanCache: true, wantArg: "-count=1", wantPkg: "./..."},
		{name: "cached", wantArg: "-json", wantPkg: "./..."},
		{name: "focused", wantArg: "-count=1", wantPkg: "./internal/testtiming"},
		{name: "race", wantArg: "-race", wantPkg: "./..."},
		{name: "integration", wantArg: "-count=1", wantPkg: "./internal/daemon/testharness"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := ResolveProfile(tt.name, nil, "")
			require.NoError(t, err)
			assert.Equal(t, tt.cleanCache, profile.CleanCache)
			assert.Contains(t, profile.GoTestArgs, tt.wantArg)
			assert.Contains(t, profile.Packages, tt.wantPkg)
			assert.Equal(t, "go", profile.Command()[0])
			assert.Equal(t, "test", profile.Command()[1])
		})
	}
	assert.Equal(t, []string{"cached", "cold", "focused", "integration", "race"}, ProfileNames())
}

func TestFocusedProfileOverridesAreRecordedInExactCommand(t *testing.T) {
	profile, err := ResolveProfile("focused", []string{"./internal/cli", "./internal/tui"}, "TestCommand")
	require.NoError(t, err)
	assert.Equal(t, []string{"./internal/cli", "./internal/tui"}, profile.Packages)
	assert.Equal(t, []string{"go", "test", "-json", "-count=1", "-timeout=5m", "-run", "TestCommand", "./internal/cli", "./internal/tui"}, profile.Command())
}

func TestCompleteProfilesRejectScopeChangingOverrides(t *testing.T) {
	for _, name := range []string{"cold", "cached", "race", "integration"} {
		_, err := ResolveProfile(name, []string{"./internal/testtiming"}, "")
		assert.ErrorContains(t, err, "canonical scope", name)
		_, err = ResolveProfile(name, nil, "TestOnlyOne")
		assert.ErrorContains(t, err, "canonical scope", name)
	}
}
