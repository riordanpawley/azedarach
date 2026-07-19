package testtiming

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreparePackageCloneEnvironmentsNeverSharesMutableCloneIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_USER_DB_PATH", "")
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATHS", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATH", "")
	workingDir := t.TempDir()
	userSource := createCloneFixture(t, workingDir, "user-source.db")
	projectSourceA := createCloneFixture(t, workingDir, "project-a.db")
	projectSourceB := createCloneFixture(t, workingDir, "project-b.db")
	base := os.Environ()
	base = withEnv(base, "AZEDARACH_USER_DB_CLONE", userSource)
	base = withEnv(base, "AZEDARACH_PROJECT_DB_CLONES", projectSourceA+string(os.PathListSeparator)+projectSourceB)
	profile := Profile{
		Packages:                []string{"./consumer/one", "./consumer/two", "./consumer/no-authority"},
		PackageIsolatedDBClones: true,
		PackageCloneAuthorities: map[string][]CloneAuthority{
			"./consumer/one": {CloneAuthorityUser, CloneAuthorityProject},
			"./consumer/two": {CloneAuthorityUser, CloneAuthorityProject},
		},
	}

	environments, evidence, err := preparePackageCloneEnvironments(context.Background(), profile, t.TempDir(), workingDir, base)
	require.NoError(t, err)
	require.Len(t, environments, 3)
	require.Len(t, evidence.Packages, 3)
	assert.Equal(t, cloneIsolationPackageIsolatedParallel, evidence.Mode)
	assert.True(t, evidence.Configured)
	assert.NotEqual(t, evidence.Packages[0].UserDB, evidence.Packages[1].UserDB)
	for projectIndex := range evidence.Packages[0].ProjectDBs {
		assert.NotEqual(t, evidence.Packages[0].ProjectDBs[projectIndex], evidence.Packages[1].ProjectDBs[projectIndex])
	}
	for _, identity := range evidence.Packages {
		if identity.UserDB != "" {
			assert.NotEqual(t, userSource, identity.UserDB)
			assert.FileExists(t, identity.UserDB)
		}
		for _, path := range identity.ProjectDBs {
			assert.NotEqual(t, projectSourceA, path)
			assert.NotEqual(t, projectSourceB, path)
			assert.FileExists(t, path)
		}
	}
	for index, environment := range environments {
		assert.Equal(t, evidence.Packages[index].UserDB, environmentValue(environment.Env, "AZEDARACH_USER_DB_CLONE"))
		assert.Equal(t, strings.Join(evidence.Packages[index].ProjectDBs, string(os.PathListSeparator)), environmentValue(environment.Env, "AZEDARACH_PROJECT_DB_CLONES"))
		assert.NotContains(t, environment.Env, "AZEDARACH_USER_DB_CLONE="+userSource)
		assert.NotContains(t, environment.Env, "AZEDARACH_PROJECT_DB_CLONES="+projectSourceA+string(os.PathListSeparator)+projectSourceB)
	}
	assert.NotEqual(t, environmentValue(environments[0].Env, "AZEDARACH_USER_DB_PATH"), environmentValue(environments[1].Env, "AZEDARACH_USER_DB_PATH"))
	assert.NotEqual(t, environmentValue(environments[0].Env, "AZEDARACH_DB_PATH"), environmentValue(environments[1].Env, "AZEDARACH_DB_PATH"))
	assert.NotEqual(t, environmentValue(environments[1].Env, "HOME"), environmentValue(environments[2].Env, "HOME"))
}

func createCloneFixture(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE sentinel (value TEXT NOT NULL); INSERT INTO sentinel(value) VALUES ('preserved')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}
