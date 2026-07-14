package testisolation_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	daemonnotices "github.com/riordanpawley/azedarach/internal/daemon/notices"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/dbpathguard"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/testisolation"
)

func TestConfiguredOriginalDatabasesAreRefusedBeforeSQLiteOpen(t *testing.T) {
	originalHome := t.TempDir()
	currentRoot := t.TempDir()
	type configuredProject struct {
		name string
		root string
	}
	projects := []configuredProject{
		{name: "issues", root: filepath.Join(t.TempDir(), "issues-project")},
		{name: "runtime", root: filepath.Join(t.TempDir(), "runtime-project")},
		{name: "notices", root: filepath.Join(t.TempDir(), "notices-project")},
		{name: "operations", root: filepath.Join(t.TempDir(), "operations-project")},
		{name: "attachments", root: filepath.Join(t.TempDir(), "attachments-project")},
	}
	registryProjects := make([]map[string]string, 0, len(projects))
	for _, project := range projects {
		if err := os.MkdirAll(project.root, 0o755); err != nil {
			t.Fatal(err)
		}
		registryProjects = append(registryProjects, map[string]string{"name": project.name, "path": project.root})
	}
	registryDir := filepath.Join(originalHome, ".config", "azedarach")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, _ := json.Marshal(map[string]any{"projects": registryProjects})
	if err := os.WriteFile(filepath.Join(registryDir, "projects.json"), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	userDB := filepath.Join(originalHome, ".azedarach", "azedarach.db")
	projectDBs := make(map[string]string, len(projects))
	for _, project := range projects {
		projectDBs[project.name] = filepath.Join(project.root, ".azedarach", "azedarach.db")
	}
	t.Setenv("HOME", originalHome)
	t.Setenv("AZEDARACH_USER_DB_PATH", "")
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv(dbpathguard.RefusePathsEnv, "")
	t.Setenv(dbpathguard.LegacyRefusePathEnv, "")

	environment, err := testisolation.New(t.TempDir(), currentRoot)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := environment.Apply()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()

	ctx := context.Background()
	checks := []struct {
		name string
		path string
		open func() error
	}{
		{name: "user store", path: userDB, open: func() error { _, err := userstore.Open(userDB); return err }},
		{name: "issue store", path: projectDBs["issues"], open: func() error {
			_, err := issues.NewClientAtPath(projectDBs["issues"], slog.Default()).DBStats()
			return err
		}},
		{name: "runtime state", path: projectDBs["runtime"], open: func() error {
			_, err := daemonstate.NewRuntimeStateStoreAtPath(projectDBs["runtime"], slog.Default()).ListSessionStates(ctx, "project")
			return err
		}},
		{name: "notices", path: projectDBs["notices"], open: func() error {
			_, err := daemonnotices.NewAtPath(projectDBs["notices"], slog.Default()).List(ctx, daemonnotices.Query{ProjectID: "project"})
			return err
		}},
		{name: "operations", path: projectDBs["operations"], open: func() error {
			_, err := operationstore.NewAtPath(projectDBs["operations"], slog.Default()).List(ctx, operationstore.Query{})
			return err
		}},
		{name: "attachments", path: projectDBs["attachments"], open: func() error {
			_, err := attachment.NewService(filepath.Dir(projectDBs["attachments"]), slog.Default()).List(ctx, "issue")
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.open(); err == nil {
				t.Fatalf("configured original database opened: %s", check.path)
			}
			if _, err := os.Stat(check.path); !os.IsNotExist(err) {
				t.Fatalf("refused path was touched %s: %v", check.path, err)
			}
			if _, err := os.Stat(filepath.Dir(check.path)); !os.IsNotExist(err) {
				t.Fatalf("refused parent was created %s: %v", filepath.Dir(check.path), err)
			}
		})
	}
}
