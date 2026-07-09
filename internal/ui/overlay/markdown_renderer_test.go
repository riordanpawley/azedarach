package overlay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdownDocumentPrefersLeafInline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script fake leaf")
	}
	binDir := t.TempDir()
	leafPath := filepath.Join(binDir, "leaf")
	script := "#!/bin/sh\nprintf 'leaf-rendered %s\\n' \"$2\"\n"
	if err := os.WriteFile(leafPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake leaf: %v", err)
	}
	t.Setenv("PATH", binDir)

	rendered, err := renderMarkdownDocumentWithFallback(context.Background(), "# Heading", 42)
	if err != nil {
		t.Fatalf("render markdown: %v", err)
	}
	if got, want := strings.TrimSpace(rendered), "leaf-rendered ansi:42"; got != want {
		t.Fatalf("rendered = %q, want %q", got, want)
	}
}

func TestRenderMarkdownDocumentFallsBackWhenLeafMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	rendered, err := renderMarkdownDocumentWithFallback(context.Background(), "# Fallback Heading", 40)
	if err != nil {
		t.Fatalf("render markdown: %v", err)
	}
	if !strings.Contains(ansi.Strip(rendered), "Fallback Heading") {
		t.Fatalf("fallback render missing content: %q", rendered)
	}
}

func TestRenderMarkdownLinesCachedReusesRenderedWidth(t *testing.T) {
	oldRenderer := renderMarkdownDocument
	defer func() { renderMarkdownDocument = oldRenderer }()

	calls := 0
	renderMarkdownDocument = func(_ context.Context, source string, width int) (string, error) {
		calls++
		return fmt.Sprintf("%s\nwidth:%d", source, width), nil
	}

	cache := make(map[string]markdownRenderCacheEntry)
	first := renderMarkdownLinesCached(cache, "Description", "body", 12)
	second := renderMarkdownLinesCached(cache, "Description", "body", 12)
	third := renderMarkdownLinesCached(cache, "Description", "body", 13)

	if calls != 2 {
		t.Fatalf("renderer calls = %d, want 2", calls)
	}
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Fatalf("cached lines changed: first=%q second=%q", first, second)
	}
	if strings.Join(third, "\n") == strings.Join(first, "\n") {
		t.Fatalf("width change should refresh rendered lines")
	}
}
