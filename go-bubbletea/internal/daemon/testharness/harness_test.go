package testharness

import (
	"os"
	"strings"
	"testing"
)

func TestHarnessBootShutdownAndStructuredLog(t *testing.T) {
	base := t.TempDir()
	h := New(Config{
		BaseDir:      base,
		ProjectID:    "proj-1",
		OTELExporter: "http://127.0.0.1:4318",
	})

	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := h.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	logFile := h.LogFilePath()
	if logFile == "" {
		t.Fatal("expected log file path")
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "\"event\":\"daemon.harness.boot\"") {
		t.Fatalf("missing boot event record: %s", content)
	}
	if !strings.Contains(content, "\"event\":\"daemon.harness.shutdown\"") {
		t.Fatalf("missing shutdown event record: %s", content)
	}
	if !strings.Contains(content, "\"otel_target\":\"http://127.0.0.1:4318\"") {
		t.Fatalf("missing otel target in event logs: %s", content)
	}
}
