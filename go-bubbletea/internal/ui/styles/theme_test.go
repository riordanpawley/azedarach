package styles

import "testing"

func TestStatusBarSegments_NormalizesValues(t *testing.T) {
	mode, separator, hints := StatusBarSegments("  normal  ", "  h/l: columns  ")

	if mode != " NORMAL " {
		t.Fatalf("mode = %q, want %q", mode, " NORMAL ")
	}

	if separator != " | " {
		t.Fatalf("separator = %q, want %q", separator, " | ")
	}

	if hints != "h/l: columns" {
		t.Fatalf("hints = %q, want %q", hints, "h/l: columns")
	}
}

func TestRenderStatusBarContract_DeterministicWidthFill(t *testing.T) {
	result := RenderStatusBarContract(StatusBarContract{
		Mode:  "normal",
		Hints: "h/l: columns",
		Width: 30,
	})

	expected := " NORMAL  | h/l: columns       "
	if result != expected {
		t.Fatalf("render = %q, want %q", result, expected)
	}
}

func TestRenderStatusBarContract_DeterministicTrim(t *testing.T) {
	result := RenderStatusBarContract(StatusBarContract{
		Mode:  "normal",
		Hints: "h/l: columns",
		Width: 12,
	})

	expected := " NORMAL  | h"
	if result != expected {
		t.Fatalf("render = %q, want %q", result, expected)
	}
}
