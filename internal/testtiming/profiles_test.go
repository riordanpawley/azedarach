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
		{name: "ci-timing", cleanCache: true, wantArg: "-count=1", wantPkg: "./..."},
		{name: "cached", wantArg: "-json", wantPkg: "./..."},
		{name: "focused", wantArg: "-count=1", wantPkg: "./internal/testtiming"},
		{name: "race", wantArg: "-race", wantPkg: "./internal/services/issues"},
		{name: "integration", wantArg: "-count=1", wantPkg: "./internal/daemon"},
		{name: "migration-clone", wantArg: "-count=1", wantPkg: "./internal/daemon/userstore"},
		{name: "boundary", wantArg: "-count=1", wantPkg: "./internal/tui"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := ResolveProfile(tt.name, nil, "")
			require.NoError(t, err)
			assert.Equal(t, tt.cleanCache, profile.CleanCache)
			if tt.cleanCache {
				assert.Equal(t, "cleared-and-bypassed", profile.CachePolicy())
			} else if tt.name == "cached" {
				assert.Equal(t, "permitted", profile.CachePolicy())
			} else {
				assert.Equal(t, "bypassed", profile.CachePolicy())
			}
			assert.Contains(t, profile.GoTestArgs, tt.wantArg)
			assert.Contains(t, profile.Packages, tt.wantPkg)
			assert.Equal(t, "go", profile.Command()[0])
			assert.Equal(t, "test", profile.Command()[1])
		})
	}
	assert.Equal(t, []string{"boundary", "cached", "ci-timing", "cold", "focused", "integration", "migration-clone", "race"}, ProfileNames())
	cold, err := ResolveProfile("cold", nil, "")
	require.NoError(t, err)
	assert.Contains(t, cold.GoTestArgs, "-timeout=8m")
	assert.Contains(t, cold.GoTestArgs, "-p=4")
	migrationClone, err := ResolveProfile("migration-clone", nil, "")
	require.NoError(t, err)
	assert.True(t, migrationClone.PackageIsolatedDBClones)
	assert.Equal(t, []CloneAuthority{CloneAuthorityUser}, migrationClone.PackageCloneAuthorities["./internal/daemon/userstore"])
	assert.Equal(t, []CloneAuthority{CloneAuthorityProject}, migrationClone.PackageCloneAuthorities["./internal/services/issues"])
	assert.Equal(t, []CloneAuthority{CloneAuthorityProject}, migrationClone.PackageCloneAuthorities["./internal/daemon/state"])
	assert.Equal(t, []CloneAuthority{CloneAuthorityProject}, migrationClone.PackageCloneAuthorities["./internal/daemon/operations/store"])
	assert.NotContains(t, migrationClone.PackageCloneAuthorities, "./internal/daemon")
}

func TestFocusedProfileOverridesAreRecordedInExactCommand(t *testing.T) {
	profile, err := ResolveProfile("focused", []string{"./internal/cli", "./internal/tui"}, "TestCommand")
	require.NoError(t, err)
	assert.Equal(t, []string{"./internal/cli", "./internal/tui"}, profile.Packages)
	assert.Equal(t, []string{"go", "test", "-json", "-count=1", "-timeout=5m", "-run", "TestCommand", "./internal/cli", "./internal/tui"}, profile.Command())
}

func TestCompleteProfilesRejectScopeChangingOverrides(t *testing.T) {
	for _, name := range []string{"cold", "ci-timing", "cached", "race", "integration", "migration-clone", "boundary"} {
		_, err := ResolveProfile(name, []string{"./internal/testtiming"}, "")
		assert.ErrorContains(t, err, "canonical scope", name)
		_, err = ResolveProfile(name, nil, "TestOnlyOne")
		assert.ErrorContains(t, err, "canonical scope", name)
	}
}
