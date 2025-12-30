package diff

import (
	"testing"
)

func TestParseUnifiedDiff_EmptyInput(t *testing.T) {
	files := ParseUnifiedDiff("")
	if len(files) != 0 {
		t.Errorf("Expected 0 files, got %d", len(files))
	}
}

func TestParseUnifiedDiff_SingleFileModified(t *testing.T) {
	input := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main

+import "fmt"
 func main() {
`
	files := ParseUnifiedDiff(input)

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	file := files[0]
	if file.Path != "main.go" {
		t.Errorf("Expected path 'main.go', got '%s'", file.Path)
	}

	if file.Status != FileModified {
		t.Errorf("Expected status FileModified, got %v", file.Status)
	}

	if file.Additions != 1 {
		t.Errorf("Expected 1 addition, got %d", file.Additions)
	}

	if file.Deletions != 0 {
		t.Errorf("Expected 0 deletions, got %d", file.Deletions)
	}

	if len(file.Hunks) != 1 {
		t.Fatalf("Expected 1 hunk, got %d", len(file.Hunks))
	}

	hunk := file.Hunks[0]
	if hunk.OldStart != 1 || hunk.OldCount != 3 {
		t.Errorf("Expected old range -1,3, got -%d,%d", hunk.OldStart, hunk.OldCount)
	}

	if hunk.NewStart != 1 || hunk.NewCount != 4 {
		t.Errorf("Expected new range +1,4, got +%d,%d", hunk.NewStart, hunk.NewCount)
	}

	expectedLines := 3 // 1 context + 1 add + 1 context (empty lines without prefix are skipped)
	if len(hunk.Lines) != expectedLines {
		t.Errorf("Expected %d lines, got %d", expectedLines, len(hunk.Lines))
	}
}

func TestParseUnifiedDiff_FileAdded(t *testing.T) {
	input := `diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,3 @@
+package new
+
+// New file
`
	files := ParseUnifiedDiff(input)

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	file := files[0]
	if file.Status != FileAdded {
		t.Errorf("Expected status FileAdded, got %v", file.Status)
	}

	if file.Additions != 3 {
		t.Errorf("Expected 3 additions, got %d", file.Additions)
	}
}

func TestParseUnifiedDiff_FileDeleted(t *testing.T) {
	input := `diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package old
-
-// Old file
`
	files := ParseUnifiedDiff(input)

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	file := files[0]
	if file.Status != FileDeleted {
		t.Errorf("Expected status FileDeleted, got %v", file.Status)
	}

	if file.Deletions != 3 {
		t.Errorf("Expected 3 deletions, got %d", file.Deletions)
	}
}

func TestParseUnifiedDiff_FileRenamed(t *testing.T) {
	input := `diff --git a/old.go b/new.go
rename from old.go
rename to new.go
--- a/old.go
+++ b/new.go
`
	files := ParseUnifiedDiff(input)

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	file := files[0]
	if file.Status != FileRenamed {
		t.Errorf("Expected status FileRenamed, got %v", file.Status)
	}

	if file.OldPath != "old.go" {
		t.Errorf("Expected old path 'old.go', got '%s'", file.OldPath)
	}

	if file.Path != "new.go" {
		t.Errorf("Expected new path 'new.go', got '%s'", file.Path)
	}
}

func TestParseUnifiedDiff_MultipleFiles(t *testing.T) {
	input := `diff --git a/file1.go b/file1.go
--- a/file1.go
+++ b/file1.go
@@ -1,1 +1,2 @@
 line1
+line2
diff --git a/file2.go b/file2.go
--- a/file2.go
+++ b/file2.go
@@ -1,2 +1,1 @@
 line1
-line2
`
	files := ParseUnifiedDiff(input)

	if len(files) != 2 {
		t.Fatalf("Expected 2 files, got %d", len(files))
	}

	// Check file1
	if files[0].Path != "file1.go" {
		t.Errorf("Expected first file path 'file1.go', got '%s'", files[0].Path)
	}
	if files[0].Additions != 1 {
		t.Errorf("Expected 1 addition in file1, got %d", files[0].Additions)
	}

	// Check file2
	if files[1].Path != "file2.go" {
		t.Errorf("Expected second file path 'file2.go', got '%s'", files[1].Path)
	}
	if files[1].Deletions != 1 {
		t.Errorf("Expected 1 deletion in file2, got %d", files[1].Deletions)
	}
}

func TestParseUnifiedDiff_MultipleHunks(t *testing.T) {
	input := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main

+import "fmt"
 func main() {
@@ -10,3 +11,4 @@ func helper() {
 	return true
 }

+// New comment
`
	files := ParseUnifiedDiff(input)

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	file := files[0]
	if len(file.Hunks) != 2 {
		t.Fatalf("Expected 2 hunks, got %d", len(file.Hunks))
	}

	if file.Additions != 2 {
		t.Errorf("Expected 2 total additions, got %d", file.Additions)
	}
}

func TestLineType_ParseCorrectly(t *testing.T) {
	input := `diff --git a/test.go b/test.go
--- a/test.go
+++ b/test.go
@@ -1,3 +1,3 @@
 context line
-deleted line
+added line
`
	files := ParseUnifiedDiff(input)

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	hunk := files[0].Hunks[0]
	if len(hunk.Lines) != 3 {
		t.Fatalf("Expected 3 lines, got %d", len(hunk.Lines))
	}

	// Check line types
	if hunk.Lines[0].Type != LineContext {
		t.Errorf("Expected first line to be LineContext, got %v", hunk.Lines[0].Type)
	}

	if hunk.Lines[1].Type != LineDelete {
		t.Errorf("Expected second line to be LineDelete, got %v", hunk.Lines[1].Type)
	}

	if hunk.Lines[2].Type != LineAdd {
		t.Errorf("Expected third line to be LineAdd, got %v", hunk.Lines[2].Type)
	}

	// Check content
	if hunk.Lines[0].Content != "context line" {
		t.Errorf("Expected context content 'context line', got '%s'", hunk.Lines[0].Content)
	}

	if hunk.Lines[1].Content != "deleted line" {
		t.Errorf("Expected delete content 'deleted line', got '%s'", hunk.Lines[1].Content)
	}

	if hunk.Lines[2].Content != "added line" {
		t.Errorf("Expected add content 'added line', got '%s'", hunk.Lines[2].Content)
	}
}

func TestFileStatus_String(t *testing.T) {
	tests := []struct {
		status   FileStatus
		expected string
	}{
		{FileModified, "modified"},
		{FileAdded, "added"},
		{FileDeleted, "deleted"},
		{FileRenamed, "renamed"},
		{FileStatus(999), "unknown"},
	}

	for _, tt := range tests {
		result := tt.status.String()
		if result != tt.expected {
			t.Errorf("FileStatus(%d).String() = %q, expected %q", tt.status, result, tt.expected)
		}
	}
}
