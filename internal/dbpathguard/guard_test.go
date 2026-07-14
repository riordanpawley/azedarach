package dbpathguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRefusesEveryConfiguredPathBeforeCreation(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "user", "azedarach.db"),
		filepath.Join(root, "project-a", ".azedarach", "azedarach.db"),
		filepath.Join(root, "project-b", ".azedarach", "azedarach.db"),
	}
	encoded, err := Encode(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(RefusePathsEnv, encoded)
	t.Setenv(LegacyRefusePathEnv, "")

	for _, path := range paths {
		if err := Check(path); err == nil {
			t.Fatalf("Check(%q) succeeded, want refusal", path)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatalf("refusal touched parent for %q: %v", path, err)
		}
	}
	if err := Check(filepath.Join(root, "safe", "clone.db")); err != nil {
		t.Fatalf("safe clone path refused: %v", err)
	}
}

func TestCheckCanonicalizesSymlinkedParents(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(realRoot, "nested", "azedarach.db")
	encoded, err := Encode([]string{realPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(RefusePathsEnv, encoded)
	t.Setenv(LegacyRefusePathEnv, "")
	if err := Check(filepath.Join(aliasRoot, "nested", "azedarach.db")); err == nil {
		t.Fatal("symlink alias bypassed refusal")
	}
}

func TestUseProjectOverrideKeepsParallelFixturesDistinct(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "configured", ".azedarach", "azedarach.db")
	isolated := filepath.Join(root, "isolated", ".azedarach", "azedarach.db")
	fixture := filepath.Join(root, "fixture", ".azedarach", "azedarach.db")
	t.Setenv(TestIsolationRootEnv, root)
	t.Setenv(TestCurrentProjectDBEnv, current)
	t.Setenv(TestIsolatedProjectDBEnv, isolated)

	use, err := UseProjectOverride(current, isolated)
	if err != nil || !use {
		t.Fatalf("current project should use isolated override: use=%v err=%v", use, err)
	}
	use, err = UseProjectOverride(fixture, isolated)
	if err != nil || use {
		t.Fatalf("explicit fixture should keep its own DB: use=%v err=%v", use, err)
	}
	explicit := filepath.Join(root, "explicit", "database.db")
	use, err = UseProjectOverride(fixture, explicit)
	if err != nil || !use {
		t.Fatalf("deliberate per-test override should win: use=%v err=%v", use, err)
	}
}
