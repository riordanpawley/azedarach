package attachment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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

func TestMacOSClipboardFallbackAttemptsTryNativePasteboardBeforePNGAppleScript(t *testing.T) {
	attempts := macOSClipboardFallbackAttempts()

	pasteboardIndex := -1
	pngScriptIndex := -1
	for idx, attempt := range attempts {
		switch attempt.label {
		case "pasteboard fallback":
			pasteboardIndex = idx
		case "png applescript":
			pngScriptIndex = idx
		}
	}

	if pasteboardIndex < 0 {
		t.Fatalf("missing pasteboard fallback attempt")
	}
	if pngScriptIndex < 0 {
		t.Fatalf("missing png applescript attempt")
	}
	if pasteboardIndex > pngScriptIndex {
		t.Fatalf("pasteboard fallback index = %d, png applescript index = %d; want pasteboard first", pasteboardIndex, pngScriptIndex)
	}
}

func TestRunMacOSClipboardReadAttemptsTimesOutOneAttemptThenUsesNextFallback(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	attempts := []macOSClipboardAttempt{
		{
			label: "slow conversion",
			read: func(ctx context.Context) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		{
			label: "native fallback",
			read: func(context.Context) ([]byte, error) {
				return pngData, nil
			},
		},
	}

	start := time.Now()
	got, errors := runMacOSClipboardReadAttempts(context.Background(), 10*time.Millisecond, attempts)
	elapsed := time.Since(start)

	if string(got) != string(pngData) {
		t.Fatalf("data = %v, want png data %v", got, pngData)
	}
	if len(errors) != 1 || !strings.Contains(errors[0], "slow conversion failed: context deadline exceeded") {
		t.Fatalf("errors = %#v, want timed-out slow conversion only", errors)
	}
	if elapsed > time.Second {
		t.Fatalf("fallback loop took %s, want bounded by per-attempt timeout", elapsed)
	}
}

func TestClipboardReadTimeoutCoversMacOSFallbackChain(t *testing.T) {
	minimum := macOSClipboardAttemptTimeout * time.Duration(len(macOSClipboardFallbackAttempts())+2)
	if clipboardReadTimeout < minimum {
		t.Fatalf("clipboard read timeout = %s, want at least %s to cover bounded macOS attempts", clipboardReadTimeout, minimum)
	}
}

func TestRunMacOSClipboardReadAttemptsRecordsEmptyAttempt(t *testing.T) {
	got, attempts := runMacOSClipboardReadAttempts(context.Background(), time.Second, []macOSClipboardAttempt{
		{
			label: "empty",
			read: func(context.Context) ([]byte, error) {
				return nil, nil
			},
		},
		{
			label: "error",
			read: func(context.Context) ([]byte, error) {
				return nil, errors.New("boom")
			},
		},
	})

	if got != nil {
		t.Fatalf("data = %v, want nil", got)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %#v, want two failure summaries", attempts)
	}
	if attempts[0] != "empty returned empty output" {
		t.Fatalf("attempts[0] = %q", attempts[0])
	}
	if attempts[1] != "error failed: boom" {
		t.Fatalf("attempts[1] = %q", attempts[1])
	}
}
