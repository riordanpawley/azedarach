package testisolation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/dbpathguard"
)

func TestNewProtectsOriginalUserCurrentAndRegisteredDatabases(t *testing.T) {
	originalHome := t.TempDir()
	currentProject := filepath.Join(t.TempDir(), "current")
	registeredA := filepath.Join(t.TempDir(), "registered-a")
	registeredBBase := filepath.Join(t.TempDir(), "registered-b-base")
	registeredB := filepath.Join(t.TempDir(), "registered-b-worktree")
	for _, dir := range []string{currentProject, registeredA, registeredB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitDir := filepath.Join(registeredBBase, ".git")
	worktreeGitDir := filepath.Join(gitDir, "worktrees", "registered-b")
	if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registeredB, ".git"), []byte("gitdir: "+worktreeGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registryDir := filepath.Join(originalHome, ".config", "azedarach")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, _ := json.Marshal(map[string]any{"projects": []map[string]string{{"name": "a", "path": registeredA}, {"name": "b", "path": registeredB}}})
	if err := os.WriteFile(filepath.Join(registryDir, "projects.json"), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", originalHome)
	t.Setenv("AZEDARACH_USER_DB_PATH", filepath.Join(originalHome, "custom-user.db"))
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv(dbpathguard.RefusePathsEnv, "")
	t.Setenv(dbpathguard.LegacyRefusePathEnv, "")

	environment, err := New(t.TempDir(), currentProject)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := environment.Apply()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()

	if got := os.Getenv("HOME"); got == originalHome {
		t.Fatalf("HOME was not isolated: %s", got)
	}
	if got := os.Getenv("XDG_CONFIG_HOME"); got == "" {
		t.Fatal("XDG_CONFIG_HOME was not isolated")
	}
	for _, path := range []string{
		filepath.Join(originalHome, ".azedarach", "azedarach.db"),
		filepath.Join(originalHome, "custom-user.db"),
		filepath.Join(currentProject, ".azedarach", "azedarach.db"),
		filepath.Join(registeredA, ".azedarach", "azedarach.db"),
		filepath.Join(registeredBBase, ".azedarach", "azedarach.db"),
	} {
		if err := dbpathguard.Check(path); err == nil {
			t.Errorf("original path was not refused: %s", path)
		}
	}
	for _, key := range []string{"AZEDARACH_USER_DB_PATH", "AZEDARACH_DB_PATH"} {
		if err := dbpathguard.Check(os.Getenv(key)); err != nil {
			t.Errorf("isolated %s path refused: %v", key, err)
		}
	}
}

func TestApplyRollsBackPartialEnvironmentOnFailure(t *testing.T) {
	t.Setenv("AZEDARACH_TEST_ROLLBACK", "before")
	environment := &Environment{overrides: map[string]string{
		"AZEDARACH_TEST_ROLLBACK": "after",
		"ZZZ_AZEDARACH_INVALID":   "contains\x00nul",
	}}
	if _, err := environment.Apply(); err == nil {
		t.Fatal("Apply succeeded with invalid environment value")
	}
	if got := os.Getenv("AZEDARACH_TEST_ROLLBACK"); got != "before" {
		t.Fatalf("partial environment was not restored: %q", got)
	}
}

func TestCheckDatabaseCloneRejectsConfiguredOriginalWithoutRunnerEnvironment(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(t.TempDir(), "registered")
	registryDir := filepath.Join(home, ".config", "azedarach")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, _ := json.Marshal(map[string]any{"projects": []map[string]string{{"name": "registered", "path": project}}})
	if err := os.WriteFile(filepath.Join(registryDir, "projects.json"), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AZEDARACH_USER_DB_PATH", "")
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv(dbpathguard.RefusePathsEnv, "")
	t.Setenv(dbpathguard.LegacyRefusePathEnv, "")
	configured := filepath.Join(project, ".azedarach", "azedarach.db")
	if err := CheckDatabaseClone(configured, t.TempDir()); err == nil {
		t.Fatal("configured project database accepted as a clone")
	}
	if err := CheckDatabaseClone(filepath.Join(t.TempDir(), "safe-clone.db"), t.TempDir()); err != nil {
		t.Fatalf("safe clone refused: %v", err)
	}
}
