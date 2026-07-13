package testisolation_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/dbpathguard"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/testisolation"
)

func TestConfiguredOriginalDatabasesAreRefusedBeforeSQLiteOpen(t *testing.T) {
	originalHome := t.TempDir()
	projectRoot := filepath.Join(t.TempDir(), "registered-project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	registryDir := filepath.Join(originalHome, ".config", "azedarach")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, _ := json.Marshal(map[string]any{"projects": []map[string]string{{"name": "production", "path": projectRoot}}})
	if err := os.WriteFile(filepath.Join(registryDir, "projects.json"), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	userDB := filepath.Join(originalHome, ".azedarach", "azedarach.db")
	projectDB := filepath.Join(projectRoot, ".azedarach", "azedarach.db")
	t.Setenv("HOME", originalHome)
	t.Setenv("AZEDARACH_USER_DB_PATH", "")
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv(dbpathguard.RefusePathsEnv, "")
	t.Setenv(dbpathguard.LegacyRefusePathEnv, "")

	environment, err := testisolation.New(t.TempDir(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := environment.Apply()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()

	if _, err := userstore.Open(userDB); err == nil {
		t.Fatal("user store opened configured original database")
	}
	client := issues.NewClientAtPath(projectDB, slog.Default())
	if _, err := client.DBStats(); err == nil {
		t.Fatal("issue store opened configured original database")
	}
	for _, path := range []string{userDB, projectDB} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("refused path was touched %s: %v", path, err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatalf("refused parent was created %s: %v", filepath.Dir(path), err)
		}
	}
}
