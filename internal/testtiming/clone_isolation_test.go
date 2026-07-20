package testtiming

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testisolation"
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
	require.Len(t, evidence.Packages, 2)
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
	for index, environment := range environments[:2] {
		assert.Equal(t, evidence.Packages[index].UserDB, environmentValue(environment.Env, "AZEDARACH_USER_DB_CLONE"))
		assert.Equal(t, strings.Join(evidence.Packages[index].ProjectDBs, string(os.PathListSeparator)), environmentValue(environment.Env, "AZEDARACH_PROJECT_DB_CLONES"))
		assert.NotContains(t, environment.Env, "AZEDARACH_USER_DB_CLONE="+userSource)
		assert.NotContains(t, environment.Env, "AZEDARACH_PROJECT_DB_CLONES="+projectSourceA+string(os.PathListSeparator)+projectSourceB)
	}
	assert.NotEqual(t, environmentValue(environments[0].Env, "AZEDARACH_USER_DB_PATH"), environmentValue(environments[1].Env, "AZEDARACH_USER_DB_PATH"))
	assert.NotEqual(t, environmentValue(environments[0].Env, "AZEDARACH_DB_PATH"), environmentValue(environments[1].Env, "AZEDARACH_DB_PATH"))
	assert.NotEqual(t, environmentValue(environments[1].Env, "HOME"), environmentValue(environments[2].Env, "HOME"))
}

func TestPreparePackageCloneEnvironmentsFailsClosedForInvalidAuthorityMapping(t *testing.T) {
	workingDir := t.TempDir()
	userSource := createCloneFixture(t, workingDir, "user-source.db")
	projectSource := createCloneFixture(t, workingDir, "project-source.db")
	configuredBase := withEnv(os.Environ(), "AZEDARACH_USER_DB_CLONE", userSource)
	configuredBase = withEnv(configuredBase, "AZEDARACH_PROJECT_DB_CLONES", projectSource)
	for _, testCase := range []struct {
		name        string
		authorities map[string][]CloneAuthority
		base        []string
		want        string
	}{
		{name: "unknown package", authorities: map[string][]CloneAuthority{"./unknown": {CloneAuthorityUser}}, base: configuredBase, want: "unknown package"},
		{name: "invalid authority", authorities: map[string][]CloneAuthority{"./consumer": {CloneAuthority("invalid")}}, base: configuredBase, want: "invalid authority"},
		{name: "unassigned user authority", authorities: map[string][]CloneAuthority{"./consumer": {CloneAuthorityProject}}, base: configuredBase, want: "user clone authority has no consuming package"},
		{name: "unassigned project authority", authorities: map[string][]CloneAuthority{"./consumer": {CloneAuthorityUser}}, base: configuredBase, want: "project clone authority has no consuming package"},
		{name: "absent capability mapping", authorities: nil, base: configuredBase, want: "user clone authority has no consuming package"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			profile := Profile{Packages: []string{"./consumer"}, PackageIsolatedDBClones: true, PackageCloneAuthorities: testCase.authorities}
			_, _, err := preparePackageCloneEnvironments(context.Background(), profile, t.TempDir(), workingDir, testCase.base)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

func TestPreparePackageCloneEnvironmentsSupportsConfiguredNonAzedarachConsumer(t *testing.T) {
	workingDir := t.TempDir()
	userSource := createCloneFixture(t, workingDir, "user-source.db")
	base := withEnv(os.Environ(), "AZEDARACH_USER_DB_CLONE", userSource)
	profile := Profile{
		Packages:                []string{"example.com/other/tool/storage"},
		PackageIsolatedDBClones: true,
		PackageCloneAuthorities: map[string][]CloneAuthority{"example.com/other/tool/storage": {CloneAuthorityUser}},
	}

	environments, evidence, err := preparePackageCloneEnvironments(context.Background(), profile, t.TempDir(), workingDir, base)
	require.NoError(t, err)
	require.Len(t, environments, 1)
	require.Len(t, evidence.Packages, 1)
	assert.NotEmpty(t, evidence.Packages[0].UserDB)
}

func TestPackageCloneIsolationReproducesSharedSQLiteLockAndProtectsSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_USER_DB_PATH", "")
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATHS", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATH", "")
	workingDir := t.TempDir()
	source := createCloneFixture(t, workingDir, "shared-source.db")
	sourceBefore := fileChecksum(t, source)

	holder := openSingleConnectionSQLite(t, source)
	contender := openSingleConnectionSQLite(t, source)
	lockAcquired := make(chan error, 1)
	releaseLock := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		transaction, err := holder.BeginTx(context.Background(), nil)
		if err == nil {
			_, err = transaction.Exec(`UPDATE sentinel SET value = 'holder'`)
		}
		lockAcquired <- err
		if err != nil {
			return
		}
		<-releaseLock
		holderDone <- transaction.Rollback()
	}()
	require.NoError(t, <-lockAcquired)
	_, sharedErr := contender.Exec(`UPDATE sentinel SET value = 'contender'`)
	require.ErrorContains(t, sharedErr, "database is locked")
	close(releaseLock)
	require.NoError(t, <-holderDone)
	require.NoError(t, holder.Close())
	require.NoError(t, contender.Close())

	privateA := filepath.Join(workingDir, "private-a", "azedarach.db")
	privateB := filepath.Join(workingDir, "private-b", "azedarach.db")
	require.NoError(t, testisolation.SnapshotDatabaseClone(context.Background(), source, privateA, workingDir))
	require.NoError(t, testisolation.SnapshotDatabaseClone(context.Background(), source, privateB, workingDir))
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan error, 2)
	for index, path := range []string{privateA, privateB} {
		go func() {
			database, err := sql.Open("sqlite", path)
			if err != nil {
				results <- err
				return
			}
			defer database.Close()
			database.SetMaxOpenConns(1)
			if _, err = database.Exec(`PRAGMA busy_timeout=0`); err != nil {
				results <- err
				return
			}
			ready <- struct{}{}
			<-start
			_, err = database.Exec(`UPDATE sentinel SET value = ?`, fmt.Sprintf("private-%d", index))
			results <- err
		}()
	}
	<-ready
	<-ready
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	assert.Equal(t, "private-0", cloneFixtureValue(t, privateA))
	assert.Equal(t, "private-1", cloneFixtureValue(t, privateB))
	assert.Equal(t, sourceBefore, fileChecksum(t, source))
	assert.Equal(t, "preserved", cloneFixtureValue(t, source))
}

func openSingleConnectionSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`PRAGMA busy_timeout=0`)
	require.NoError(t, err)
	return database
}

func fileChecksum(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return sha256.Sum256(contents)
}

func cloneFixtureValue(t *testing.T, path string) string {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer database.Close()
	var value string
	require.NoError(t, database.QueryRow(`SELECT value FROM sentinel`).Scan(&value))
	return value
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
