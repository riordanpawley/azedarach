package overlay

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
)

type markdownRenderCacheEntry struct {
	source string
	width  int
	lines  []string
}

var renderMarkdownDocument = renderMarkdownDocumentWithFallback

const markdownInlineRenderTimeout = 200 * time.Millisecond

func renderMarkdownDocumentWithFallback(ctx context.Context, source string, width int) (string, error) {
	width = max(10, width)
	if rendered, err := renderMarkdownWithLeaf(ctx, source, width); err == nil {
		return rendered, nil
	}
	if rendered, err := renderMarkdownWithGlamour(source, width); err == nil {
		return rendered, nil
	}
	return strings.Join(wrapDescriptionLines(source, width), "\n"), nil
}

func renderMarkdownWithLeaf(ctx context.Context, source string, width int) (string, error) {
	path, err := exec.LookPath("leaf")
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	arg := fmt.Sprintf("ansi:%d", max(10, width))
	cmd := exec.CommandContext(runCtx, path, "--inline", arg)
	cmd.Stdin = strings.NewReader(source)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return "", fmt.Errorf("leaf render: %w: %s", err, message)
		}
		return "", fmt.Errorf("leaf render: %w", err)
	}
	return stdout.String(), nil
}

func renderMarkdownWithGlamour(source string, width int) (string, error) {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(max(10, width)),
	)
	if err != nil {
		return "", fmt.Errorf("create markdown renderer: %w", err)
	}
	rendered, err := renderer.Render(source)
	if err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	return rendered, nil
}

func renderMarkdownLinesCached(
	cache map[string]markdownRenderCacheEntry,
	key string,
	source string,
	width int,
) []string {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}
	width = max(10, width)
	if entry, ok := cache[key]; ok && entry.source == source && entry.width == width {
		return append([]string(nil), entry.lines...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), markdownInlineRenderTimeout)
	defer cancel()
	rendered, err := renderMarkdownDocument(ctx, source, width)
	if err != nil {
		rendered = strings.Join(wrapDescriptionLines(source, width), "\n")
	}
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	cache[key] = markdownRenderCacheEntry{
		source: source,
		width:  width,
		lines:  append([]string(nil), lines...),
	}
	return lines
}
