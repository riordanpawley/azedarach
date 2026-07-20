package issues

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestManagedAgentThreadIdentitySchemaValidatesEveryPinnedColumn(t *testing.T) {
	columns := []struct {
		table, name string
	}{
		{"daemon_managed_agent_incarnations", "agent_thread_id"},
		{"agent_input_delivery_intents", "agent_thread_id"},
		{"daemon_rooted_bootstrap_acknowledgements", "tmux_pane_id"},
		{"daemon_rooted_bootstrap_acknowledgements", "pane_pid"},
		{"daemon_rooted_bootstrap_acknowledgements", "agent_incarnation"},
		{"daemon_rooted_bootstrap_acknowledgements", "agent_thread_id"},
	}
	for _, column := range columns {
		for _, drift := range []string{"missing", "wrong-definition"} {
			t.Run(column.table+"/"+column.name+"/"+drift, func(t *testing.T) {
				db := openManagedThreadSchemaFixture(t)
				defer db.Close()
				if drift == "missing" {
					renameColumnForDrift(t, db, column.table, column.name, column.name+"_missing")
				} else {
					retypeColumnForDrift(t, db, column.table, column.name)
				}
				err := validateManagedAgentThreadIdentitySchema(context.Background(), db)
				wantReason := strings.ReplaceAll(drift, "-", " ")
				if err == nil || !strings.Contains(err.Error(), column.table+"."+column.name) || !strings.Contains(err.Error(), wantReason) {
					t.Fatalf("validation error = %v, want %s for %s.%s", err, drift, column.table, column.name)
				}
			})
		}
	}
}

func openManagedThreadSchemaFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE daemon_managed_agent_incarnations (id TEXT, agent_thread_id TEXT CHECK (agent_thread_id IS NULL OR trim(agent_thread_id) <> ''))`,
		`CREATE TABLE agent_input_delivery_intents (id TEXT, agent_thread_id TEXT CHECK (agent_thread_id IS NULL OR trim(agent_thread_id) <> ''))`,
		`CREATE TABLE daemon_rooted_bootstrap_acknowledgements (id TEXT, tmux_pane_id TEXT, pane_pid INTEGER, agent_incarnation TEXT, agent_thread_id TEXT)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func renameColumnForDrift(t *testing.T, db *sql.DB, table, from, to string) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE ` + table + ` RENAME COLUMN ` + from + ` TO ` + to); err != nil {
		t.Fatal(err)
	}
}

func retypeColumnForDrift(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	needle := column + " TEXT"
	if column == "pane_pid" {
		needle = column + " INTEGER"
	}
	mutated := strings.Replace(tableSQL, needle, column+" BLOB", 1)
	if mutated == tableSQL {
		t.Fatalf("could not mutate %s.%s in %q", table, column, tableSQL)
	}
	if _, err := db.Exec(`PRAGMA writable_schema=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sqlite_master SET sql=? WHERE type='table' AND name=?`, mutated, table); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA writable_schema=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA schema_version=2`); err != nil {
		t.Fatal(err)
	}
}
