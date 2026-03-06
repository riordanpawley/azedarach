package diff

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func TestNewDiffViewer(t *testing.T) {
	viewer := NewDiffViewer("/path/to/worktree", "main")

	if viewer.worktree != "/path/to/worktree" {
		t.Errorf("Expected worktree '/path/to/worktree', got '%s'", viewer.worktree)
	}

	if viewer.baseBranch != "main" {
		t.Errorf("Expected baseBranch 'main', got '%s'", viewer.baseBranch)
	}

	if viewer.cursor != 0 {
		t.Errorf("Expected cursor 0, got %d", viewer.cursor)
	}

	if viewer.scrollY != 0 {
		t.Errorf("Expected scrollY 0, got %d", viewer.scrollY)
	}

	if len(viewer.expanded) != 0 {
		t.Errorf("Expected empty expanded map, got %d entries", len(viewer.expanded))
	}
}

func TestDiffViewer_Init(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	cmd := viewer.Init()

	if cmd != nil {
		t.Error("Expected Init to return nil")
	}

	if !viewer.loading {
		t.Error("Expected loading to be true after Init")
	}
}

func TestDiffViewer_LoadDiffMsg(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.loading = true

	// Simulate successful load
	diffOutput := `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -1,1 +1,2 @@
 line1
+line2
`

	msg := LoadDiffMsg{Output: diffOutput, Err: nil}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	if viewer.loading {
		t.Error("Expected loading to be false after LoadDiffMsg")
	}

	if len(viewer.files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(viewer.files))
	}

	if viewer.diffOutput != diffOutput {
		t.Error("Expected diffOutput to be set")
	}
}

func TestDiffViewer_LoadDiffMsgReducedMode(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.loading = true

	diffOutput := `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -1 +1 @@
-old
+new
`

	msg := LoadDiffMsg{
		Output:            diffOutput,
		Err:               nil,
		ReducedMode:       true,
		ReducedModeNotice: "merge-base diff failed; falling back to plain git diff",
	}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	if !viewer.reducedMode {
		t.Error("Expected reducedMode to be true")
	}

	if !strings.Contains(viewer.reducedModeNotice, "falling back") {
		t.Errorf("Expected reduced mode notice to be set, got %q", viewer.reducedModeNotice)
	}

	view := viewer.View()
	if !strings.Contains(view, "Reduced diff mode") {
		t.Errorf("Expected view to include reduced-mode guidance, got %q", view)
	}
}

func TestDiffViewer_Navigation(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")

	// Populate with test files
	viewer.files = []DiffFile{
		{Path: "file1.go", Status: FileModified, Additions: 1, Deletions: 0, Hunks: []DiffHunk{}},
		{Path: "file2.go", Status: FileModified, Additions: 0, Deletions: 1, Hunks: []DiffHunk{}},
		{Path: "file3.go", Status: FileAdded, Additions: 5, Deletions: 0, Hunks: []DiffHunk{}},
	}

	// Test down navigation
	t.Run("Navigate down", func(t *testing.T) {
		viewer.cursor = 0
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
		updatedModel, _ := viewer.Update(msg)
		viewer = updatedModel.(*DiffViewer)

		if viewer.cursor != 1 {
			t.Errorf("Expected cursor 1, got %d", viewer.cursor)
		}
	})

	// Test up navigation
	t.Run("Navigate up", func(t *testing.T) {
		viewer.cursor = 1
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
		updatedModel, _ := viewer.Update(msg)
		viewer = updatedModel.(*DiffViewer)

		if viewer.cursor != 0 {
			t.Errorf("Expected cursor 0, got %d", viewer.cursor)
		}
	})

	// Test boundary - can't go below 0
	t.Run("Cannot navigate above top", func(t *testing.T) {
		viewer.cursor = 0
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
		updatedModel, _ := viewer.Update(msg)
		viewer = updatedModel.(*DiffViewer)

		if viewer.cursor != 0 {
			t.Errorf("Expected cursor to stay at 0, got %d", viewer.cursor)
		}
	})

	// Test boundary - can't go beyond last file
	t.Run("Cannot navigate below bottom", func(t *testing.T) {
		viewer.cursor = 2
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
		updatedModel, _ := viewer.Update(msg)
		viewer = updatedModel.(*DiffViewer)

		if viewer.cursor != 2 {
			t.Errorf("Expected cursor to stay at 2, got %d", viewer.cursor)
		}
	})
}

func TestDiffViewer_JumpToTop(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.files = []DiffFile{
		{Path: "file1.go"},
		{Path: "file2.go"},
		{Path: "file3.go"},
	}
	viewer.cursor = 2
	viewer.scrollY = 10

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	if viewer.cursor != 0 {
		t.Errorf("Expected cursor 0, got %d", viewer.cursor)
	}

	if viewer.scrollY != 0 {
		t.Errorf("Expected scrollY 0, got %d", viewer.scrollY)
	}
}

func TestDiffViewer_JumpToBottom(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.files = []DiffFile{
		{Path: "file1.go"},
		{Path: "file2.go"},
		{Path: "file3.go"},
	}
	viewer.cursor = 0

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	if viewer.cursor != 2 {
		t.Errorf("Expected cursor 2, got %d", viewer.cursor)
	}
}

func TestDiffViewer_ToggleExpand(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.files = []DiffFile{
		{Path: "file1.go", Hunks: []DiffHunk{{Header: "@@ -1,1 +1,2 @@"}}},
	}
	viewer.cursor = 0

	// Initially not expanded
	if viewer.expanded[0] {
		t.Error("Expected file to not be expanded initially")
	}

	// Press Enter to expand
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	if !viewer.expanded[0] {
		t.Error("Expected file to be expanded after Enter")
	}

	// Press Enter again to collapse
	updatedModel, _ = viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	if viewer.expanded[0] {
		t.Error("Expected file to be collapsed after second Enter")
	}
}

func TestDiffViewer_ExpandAll(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.files = []DiffFile{
		{Path: "file1.go"},
		{Path: "file2.go"},
		{Path: "file3.go"},
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	for i := range viewer.files {
		if !viewer.expanded[i] {
			t.Errorf("Expected file %d to be expanded", i)
		}
	}
}

func TestDiffViewer_CollapseAll(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.files = []DiffFile{
		{Path: "file1.go"},
		{Path: "file2.go"},
		{Path: "file3.go"},
	}

	// First expand all
	for i := range viewer.files {
		viewer.expanded[i] = true
	}

	// Then collapse all
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	for i := range viewer.files {
		if viewer.expanded[i] {
			t.Errorf("Expected file %d to be collapsed", i)
		}
	}
}

func TestDiffViewer_Close(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")

	tests := []struct {
		name string
		key  string
	}{
		{"Escape key", "esc"},
		{"Q key", "q"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg tea.KeyMsg
			if tt.key == "esc" {
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			} else {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(tt.key[0])}}
			}

			_, cmd := viewer.Update(msg)
			if cmd == nil {
				t.Fatal("Expected command to be returned")
			}

			// Execute the command and check it returns CloseOverlayMsg
			result := cmd()
			if _, ok := result.(overlay.CloseOverlayMsg); !ok {
				t.Errorf("Expected CloseOverlayMsg, got %T", result)
			}
		})
	}
}

func TestDiffViewer_Title(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")

	// No files
	title := viewer.Title()
	if title != "Git Diff" {
		t.Errorf("Expected title 'Git Diff', got '%s'", title)
	}

	// One file
	viewer.files = []DiffFile{{Path: "test.go"}}
	title = viewer.Title()
	if title != "Git Diff (1 file)" {
		t.Errorf("Expected title 'Git Diff (1 file)', got '%s'", title)
	}

	// Multiple files
	viewer.files = []DiffFile{{Path: "test1.go"}, {Path: "test2.go"}}
	title = viewer.Title()
	if title != "Git Diff (2 files)" {
		t.Errorf("Expected title 'Git Diff (2 files)', got '%s'", title)
	}
}

func TestDiffViewer_Size(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	width, height := viewer.Size()

	if width != 100 {
		t.Errorf("Expected width 100, got %d", width)
	}

	if height != 30 {
		t.Errorf("Expected height 30, got %d", height)
	}

	if viewer.viewHeight != 20 {
		t.Errorf("Expected viewHeight 20, got %d", viewer.viewHeight)
	}
}

func TestDiffViewer_ViewLoading(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.loading = true

	view := viewer.View()

	if !strings.Contains(view, "Loading") {
		t.Error("Expected view to contain 'Loading'")
	}
}

func TestDiffViewer_ViewError(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.err = &testError{msg: "test error"}

	view := viewer.View()

	if !strings.Contains(view, "Error") {
		t.Error("Expected view to contain 'Error'")
	}
}

func TestDiffViewer_ViewNoChanges(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.files = []DiffFile{}

	view := viewer.View()

	if !strings.Contains(view, "No changes") {
		t.Error("Expected view to contain 'No changes'")
	}
}

func TestDiffViewer_ViewWithFiles(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.files = []DiffFile{
		{
			Path:      "test.go",
			Status:    FileModified,
			Additions: 2,
			Deletions: 1,
			Hunks: []DiffHunk{
				{
					Header:   "@@ -1,3 +1,4 @@",
					OldStart: 1,
					OldCount: 3,
					NewStart: 1,
					NewCount: 4,
					Lines: []DiffLine{
						{Type: LineContext, Content: "line1", OldLine: 1, NewLine: 1},
						{Type: LineAdd, Content: "line2", NewLine: 2},
						{Type: LineContext, Content: "line3", OldLine: 2, NewLine: 3},
					},
				},
			},
		},
	}

	view := viewer.View()

	// Check that file path is present
	if !strings.Contains(view, "test.go") {
		t.Error("Expected view to contain file path 'test.go'")
	}

	// Check that stats are present
	if !strings.Contains(view, "+2") {
		t.Error("Expected view to contain '+2' additions")
	}

	if !strings.Contains(view, "-1") {
		t.Error("Expected view to contain '-1' deletions")
	}
}

func TestDiffViewer_IgnoreKeysWhileLoading(t *testing.T) {
	viewer := NewDiffViewer("/test", "main")
	viewer.loading = true
	viewer.cursor = 0

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	updatedModel, _ := viewer.Update(msg)
	viewer = updatedModel.(*DiffViewer)

	// Cursor should not have changed
	if viewer.cursor != 0 {
		t.Errorf("Expected cursor to stay at 0 while loading, got %d", viewer.cursor)
	}
}

// testError is a simple error type for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
