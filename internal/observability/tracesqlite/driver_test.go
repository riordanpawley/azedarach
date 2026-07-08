package tracesqlite

import (
	"context"
	"testing"
)

func TestSQLOperation(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"SELECT * FROM issues":                "select",
		"  insert into issues(id) values (?)": "insert",
		"\n\tUPDATE issues SET title = ?":     "update",
		"(`PRAGMA table_info(issues))":        "pragma",
		"":                                    "unknown",
	}
	for query, want := range tests {
		if got := sqlOperation(query); got != want {
			t.Fatalf("sqlOperation(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestOpenSmoke(t *testing.T) {
	t.Parallel()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("PingContext() error = %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE smoke (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("ExecContext() error = %v", err)
	}
}
