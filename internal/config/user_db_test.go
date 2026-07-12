package config

import (
	"path/filepath"
	"testing"
)

func TestUserDBPathIsIndependentOfRepository(t *testing.T) {
	want := filepath.Join(t.TempDir(), "user.db")
	t.Setenv(userDBPathEnv, want)
	got, err := UserDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("UserDBPath()=%q want %q", got, want)
	}
}
