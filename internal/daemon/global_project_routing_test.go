package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
)

func TestGlobalDaemonRoutesOnlyRegisteredProjectsWithoutCrossProjectFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_DISABLE_USER_DB", "1")

	consumerA := newRuntimeRouterTestRepo(t, "consumer-a")
	consumerB := newRuntimeRouterTestRepo(t, "consumer-b")
	for _, root := range []string{consumerA, consumerB} {
		storeDir := filepath.Join(root, ".azedarach")
		if err := os.MkdirAll(storeDir, 0o755); err != nil {
			t.Fatalf("create issue store directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(storeDir, "azedarach.db"), nil, 0o600); err != nil {
			t.Fatalf("create issue store: %v", err)
		}
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{
		{ID: "consumer-a", Name: "Consumer A", Path: consumerA},
		{ID: "consumer-b", Name: "Consumer B", Path: consumerB},
	}}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	d := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if got := d.resolveRepoDirForProjectExact("consumer-a"); got != consumerA {
		t.Fatalf("consumer A route = %q, want %q", got, consumerA)
	}
	if got := d.resolveRepoDirForProjectExact("consumer-b"); got != consumerB {
		t.Fatalf("consumer B route = %q, want %q", got, consumerB)
	}
	if got := d.resolveRepoDirForProjectExact("absent-consumer"); got != "" {
		t.Fatalf("absent project route = %q, want fail-closed empty route", got)
	}

	storeA := d.issueClientForProject("consumer-a")
	storeB := d.issueClientForProject("consumer-b")
	if storeA == nil || storeB == nil || storeA == storeB {
		t.Fatalf("registered consumers must receive distinct stores: A=%p B=%p", storeA, storeB)
	}
	if got := d.issueClientForProject("absent-consumer"); got != nil {
		t.Fatalf("absent project store = %p, want fail-closed nil", got)
	}
	if got := d.runtimeConfigForProject("absent-consumer").GateFailureArtifactPaths; len(got) != 0 {
		t.Fatalf("absent project inherited capability paths: %v", got)
	}
	if filepath.Clean(d.cfg.RepoDir) != "." && d.cfg.RepoDir != "" {
		t.Fatalf("global daemon acquired repo authority %q", d.cfg.RepoDir)
	}
}

func TestGlobalDaemonDoesNotReviveStaleRegisteredProjectState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_DISABLE_USER_DB", "1")

	parent := t.TempDir()
	missingRoot := filepath.Join(parent, "missing-root")
	missingStore := filepath.Join(parent, "missing-store")
	if err := os.Mkdir(missingStore, 0o755); err != nil {
		t.Fatalf("create stale root: %v", err)
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{
		{ID: "missing-root", Name: "Missing Root", Path: missingRoot},
		{ID: "missing-store", Name: "Missing Store", Path: missingStore},
	}}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	d := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, testCase := range []struct {
		projectID string
		root      string
	}{
		{projectID: "missing-root", root: missingRoot},
		{projectID: "missing-store", root: missingStore},
	} {
		t.Run(testCase.projectID, func(t *testing.T) {
			if got := d.issueClientForProject(testCase.projectID); got != nil {
				t.Fatalf("stale project client = %p, want unavailable", got)
			}
			if _, err := os.Stat(filepath.Join(testCase.root, ".azedarach")); !os.IsNotExist(err) {
				t.Fatalf("stale project state was created: %v", err)
			}
		})
	}
}
