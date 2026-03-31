package config

import (
	"path/filepath"
	"testing"
)

func TestProjectIDForRootDeterministic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")

	got1, err := ProjectIDForRoot(root)
	if err != nil {
		t.Fatalf("ProjectIDForRoot() error = %v", err)
	}
	got2, err := ProjectIDForRoot(root)
	if err != nil {
		t.Fatalf("ProjectIDForRoot() second error = %v", err)
	}
	if got1 != got2 {
		t.Fatalf("ProjectIDForRoot() not deterministic: %q != %q", got1, got2)
	}
}

func TestProjectIDForRootVariesByPath(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "a", "repo")
	rootB := filepath.Join(base, "b", "repo")

	gotA, err := ProjectIDForRoot(rootA)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(rootA) error = %v", err)
	}
	gotB, err := ProjectIDForRoot(rootB)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(rootB) error = %v", err)
	}
	if gotA == gotB {
		t.Fatalf("ProjectIDForRoot() collision: %q", gotA)
	}
}
