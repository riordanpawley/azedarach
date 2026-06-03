package attachment

import (
	"strings"
	"testing"
)

func TestSummarizeClipboardTextCompactsAndTruncates(t *testing.T) {
	long := strings.Repeat("alpha beta\n", 20)

	got := summarizeClipboardText(long)

	if !strings.Contains(got, "(219 chars, truncated)") {
		t.Fatalf("summary = %q, want character count and truncation marker", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("summary = %q, want compact whitespace", got)
	}
	if len([]rune(got)) > 150 {
		t.Fatalf("summary length = %d runes, want bounded output: %q", len([]rune(got)), got)
	}
}

func TestSummarizeClipboardTextPreservesShortValue(t *testing.T) {
	got := summarizeClipboardText("  /tmp/example.png\n")

	if got != `"/tmp/example.png"` {
		t.Fatalf("summary = %q, want quoted compact value", got)
	}
}

func TestSummarizeClipboardTextHandlesUnicode(t *testing.T) {
	got := summarizeClipboardText(strings.Repeat("界", 130))

	if !strings.Contains(got, "(130 chars, truncated)") {
		t.Fatalf("summary = %q, want rune count and truncation marker", got)
	}
	if strings.Contains(got, "\ufffd") {
		t.Fatalf("summary = %q, should not split unicode runes", got)
	}
}
