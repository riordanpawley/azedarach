package tmuxselector

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/logging"
)

func TestSelectorLoggerWritesDiagnosticsToLogFileNotTerminal(t *testing.T) {
	logDir := t.TempDir()
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stderrPath := filepath.Join(t.TempDir(), "stderr")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	defer stdoutFile.Close()
	defer stderrFile.Close()

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	oldLogger := slog.Default()
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		slog.SetDefault(oldLogger)
	})

	cfg := config.DefaultConfig()
	cfg.Session.LogDir = logDir
	slog.SetDefault(newSelectorLogger(cfg))
	slog.Default().Info("global selector project snapshot loaded", "project_count", 3)

	if err := stdoutFile.Sync(); err != nil {
		t.Fatalf("sync stdout capture: %v", err)
	}
	if err := stderrFile.Sync(); err != nil {
		t.Fatalf("sync stderr capture: %v", err)
	}
	stdout, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	stderr, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Fatalf("selector diagnostics leaked to terminal stdout=%q stderr=%q", stdout, stderr)
	}

	logPath := filepath.Join(logDir, logging.TmuxSelectorLogFileName)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read selector log: %v", err)
	}
	if !strings.Contains(string(logBytes), "global selector project snapshot loaded") {
		t.Fatalf("selector log missing diagnostic: %q", logBytes)
	}
}

func TestSelectorLogPathUsesDefaultLogDirWhenConfigMissing(t *testing.T) {
	path := selectorLogPath(nil)
	if !strings.HasSuffix(path, filepath.Join(".azedarach", "logs", logging.TmuxSelectorLogFileName)) {
		t.Fatalf("selector log path = %q, want default selector log filename under .azedarach/logs", path)
	}
}
