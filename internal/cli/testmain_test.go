package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	tmpRoot, err := os.MkdirTemp("", "azedarach-cli-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmpRoot)

	if err := os.Setenv("HOME", filepath.Join(tmpRoot, "home")); err != nil {
		panic(err)
	}
	dbDir := filepath.Join(tmpRoot, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		panic(err)
	}
	if err := os.Setenv("AZEDARACH_DB_PATH", filepath.Join(dbDir, "azedarach.db")); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}
