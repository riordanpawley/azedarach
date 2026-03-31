package overlay

import (
	"strings"
	"testing"
)

func TestRenderDialogTwoPane_OptionalRightPane(t *testing.T) {
	view := renderDialogTwoPane(dialogLayoutConfig{
		styles:      New(),
		width:       72,
		height:      16,
		title:       "SINGLE COLUMN",
		breakpoint:  60,
		gap:         2,
		minLeft:     20,
		leftFocused: true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return "main content"
		},
		renderRight: nil,
	})

	if !strings.Contains(view, "SINGLE COLUMN") {
		t.Fatalf("expected title to render, got %q", view)
	}
	if !strings.Contains(view, "main content") {
		t.Fatalf("expected main content to render, got %q", view)
	}
	if strings.Contains(view, "Actions") {
		t.Fatalf("did not expect actions section when right pane is omitted: %q", view)
	}
}

func TestRenderDialogTwoPane_OptionalRightPaneRequiresTitleAndRenderer(t *testing.T) {
	view := renderDialogTwoPane(dialogLayoutConfig{
		styles:            New(),
		width:             72,
		height:            16,
		title:             "NO RIGHT TITLE",
		rightSectionTitle: "",
		breakpoint:        60,
		gap:               2,
		minLeft:           20,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return "left"
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return "right"
		},
	})

	if strings.Contains(view, "right") {
		t.Fatalf("expected right pane to be omitted when right title is empty, got %q", view)
	}
}
