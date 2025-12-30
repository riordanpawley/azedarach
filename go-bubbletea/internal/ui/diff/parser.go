package diff

import (
	"regexp"
	"strconv"
	"strings"
)

// LineType represents the type of a diff line
type LineType int

const (
	// LineContext is unchanged content
	LineContext LineType = iota
	// LineAdd is an added line
	LineAdd
	// LineDelete is a deleted line
	LineDelete
)

// DiffLine represents a single line in a diff hunk
type DiffLine struct {
	Type    LineType
	Content string
	OldLine int
	NewLine int
}

// DiffHunk represents a section of changes in a file
type DiffHunk struct {
	Header  string
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines   []DiffLine
}

// DiffFile represents changes to a single file
type DiffFile struct {
	Path      string
	OldPath   string // For renames
	Status    FileStatus
	Additions int
	Deletions int
	Hunks     []DiffHunk
}

// FileStatus represents the type of change to a file
type FileStatus int

const (
	// FileModified indicates the file was modified
	FileModified FileStatus = iota
	// FileAdded indicates the file was added
	FileAdded
	// FileDeleted indicates the file was deleted
	FileDeleted
	// FileRenamed indicates the file was renamed
	FileRenamed
)

// String returns a string representation of FileStatus
func (s FileStatus) String() string {
	switch s {
	case FileModified:
		return "modified"
	case FileAdded:
		return "added"
	case FileDeleted:
		return "deleted"
	case FileRenamed:
		return "renamed"
	default:
		return "unknown"
	}
}

var (
	// Regex patterns for parsing unified diff format
	fileHeaderRegex = regexp.MustCompile(`^diff --git a/(.*) b/(.*)$`)
	oldFileRegex    = regexp.MustCompile(`^--- (.*)$`)
	newFileRegex    = regexp.MustCompile(`^\+\+\+ (.*)$`)
	hunkHeaderRegex = regexp.MustCompile(`^@@ -(\d+),?(\d*) \+(\d+),?(\d*) @@(.*)$`)
	renameRegex     = regexp.MustCompile(`^rename from (.*)$`)
	renameToRegex   = regexp.MustCompile(`^rename to (.*)$`)
	newFileModeRegex = regexp.MustCompile(`^new file mode`)
	deletedFileRegex = regexp.MustCompile(`^deleted file mode`)
)

// ParseUnifiedDiff parses unified diff output into structured DiffFile objects
func ParseUnifiedDiff(output string) []DiffFile {
	if output == "" {
		return []DiffFile{}
	}

	lines := strings.Split(output, "\n")
	var files []DiffFile
	var currentFile *DiffFile
	var currentHunk *DiffHunk
	var oldLineNum, newLineNum int

	for _, line := range lines {
		// File header: diff --git a/path b/path
		if matches := fileHeaderRegex.FindStringSubmatch(line); matches != nil {
			// Save previous file if exists
			if currentFile != nil {
				if currentHunk != nil {
					currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
					currentHunk = nil
				}
				files = append(files, *currentFile)
			}

			// Start new file
			currentFile = &DiffFile{
				Path:      matches[2],
				OldPath:   matches[1],
				Status:    FileModified,
				Hunks:     []DiffHunk{},
			}
			continue
		}

		if currentFile == nil {
			continue
		}

		// Check for file status markers
		if newFileModeRegex.MatchString(line) {
			currentFile.Status = FileAdded
			continue
		}

		if deletedFileRegex.MatchString(line) {
			currentFile.Status = FileDeleted
			continue
		}

		if matches := renameRegex.FindStringSubmatch(line); matches != nil {
			currentFile.Status = FileRenamed
			currentFile.OldPath = matches[1]
			continue
		}

		if matches := renameToRegex.FindStringSubmatch(line); matches != nil {
			currentFile.Path = matches[1]
			continue
		}

		// Old file marker: --- a/path or --- /dev/null
		if oldFileRegex.MatchString(line) {
			continue
		}

		// New file marker: +++ b/path or +++ /dev/null
		if newFileRegex.MatchString(line) {
			continue
		}

		// Hunk header: @@ -1,3 +1,4 @@
		if matches := hunkHeaderRegex.FindStringSubmatch(line); matches != nil {
			// Save previous hunk if exists
			if currentHunk != nil {
				currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
			}

			oldStart, _ := strconv.Atoi(matches[1])
			oldCount := 1
			if matches[2] != "" {
				oldCount, _ = strconv.Atoi(matches[2])
			}

			newStart, _ := strconv.Atoi(matches[3])
			newCount := 1
			if matches[4] != "" {
				newCount, _ = strconv.Atoi(matches[4])
			}

			currentHunk = &DiffHunk{
				Header:   line,
				OldStart: oldStart,
				OldCount: oldCount,
				NewStart: newStart,
				NewCount: newCount,
				Lines:    []DiffLine{},
			}

			oldLineNum = oldStart
			newLineNum = newStart
			continue
		}

		// Diff content lines
		if currentHunk != nil && len(line) > 0 {
			var diffLine DiffLine

			switch line[0] {
			case '+':
				diffLine = DiffLine{
					Type:    LineAdd,
					Content: line[1:],
					NewLine: newLineNum,
				}
				currentFile.Additions++
				newLineNum++

			case '-':
				diffLine = DiffLine{
					Type:    LineDelete,
					Content: line[1:],
					OldLine: oldLineNum,
				}
				currentFile.Deletions++
				oldLineNum++

			case ' ':
				diffLine = DiffLine{
					Type:    LineContext,
					Content: line[1:],
					OldLine: oldLineNum,
					NewLine: newLineNum,
				}
				oldLineNum++
				newLineNum++

			default:
				// Skip other lines (e.g., "\ No newline at end of file")
				continue
			}

			currentHunk.Lines = append(currentHunk.Lines, diffLine)
		}
	}

	// Save last file and hunk
	if currentFile != nil {
		if currentHunk != nil {
			currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
		}
		files = append(files, *currentFile)
	}

	return files
}
