package testisolation

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotDatabaseClonePreservesPointInTimeAndSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_USER_DB_PATH", "")
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATHS", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATH", "")
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	destination := filepath.Join(root, "snapshots", "destination.db")
	db, err := sql.Open("sqlite", source)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE records (value TEXT NOT NULL); INSERT INTO records(value) VALUES ('before')`)
	require.NoError(t, err)
	require.NoError(t, SnapshotDatabaseClone(context.Background(), source, destination, root))
	_, err = db.Exec(`INSERT INTO records(value) VALUES ('after')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	clone, err := sql.Open("sqlite", "file:"+filepath.ToSlash(destination)+"?mode=ro")
	require.NoError(t, err)
	defer clone.Close()
	var values string
	require.NoError(t, clone.QueryRow(`SELECT group_concat(value, ',') FROM records`).Scan(&values))
	assert.Equal(t, "before", values)
}
