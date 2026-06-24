package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFile_RotatesAndRetainsBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "az-cli.log")
	logFile, err := OpenRotatingFile(path, 10, 2)
	if err != nil {
		t.Fatalf("OpenRotatingFile() error = %v", err)
	}
	defer logFile.Close()

	for _, line := range []string{"first\n", "second\n", "third\n", "fourth\n"} {
		if _, err := logFile.Write([]byte(line)); err != nil {
			t.Fatalf("Write(%q) error = %v", line, err)
		}
	}

	active := readTestFile(t, path)
	if !strings.Contains(active, "fourth") {
		t.Fatalf("active log = %q, want newest write", active)
	}
	firstBackup := readTestFile(t, path+".1")
	if !strings.Contains(firstBackup, "third") {
		t.Fatalf("first backup = %q, want prior active log", firstBackup)
	}
	secondBackup := readTestFile(t, path+".2")
	if !strings.Contains(secondBackup, "second") {
		t.Fatalf("second backup = %q, want retained older log", secondBackup)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected third backup stat error = %v", err)
	}
}

func TestRotatingFile_RotatesOversizedExistingLogOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azd.log")
	if err := os.WriteFile(path, []byte("already too large\n"), 0o644); err != nil {
		t.Fatalf("write existing log: %v", err)
	}
	logFile, err := OpenRotatingFile(path, 10, 1)
	if err != nil {
		t.Fatalf("OpenRotatingFile() error = %v", err)
	}
	defer logFile.Close()

	if got := readTestFile(t, path+".1"); got != "already too large\n" {
		t.Fatalf("rotated backup = %q, want original content", got)
	}
	if got := readTestFile(t, path); got != "" {
		t.Fatalf("active log = %q, want empty file after startup rotation", got)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return string(b)
}
