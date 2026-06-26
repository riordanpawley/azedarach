package attachment

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService("/tmp/issues", logger)

	if service == nil {
		t.Fatal("expected service to be created")
	}

	if service.issuesPath != "/tmp/issues" {
		t.Errorf("expected issuesPath to be /tmp/issues, got %s", service.issuesPath)
	}
}

func TestAttach(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(issuesPath, logger)

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.png")
	testData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG header
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Attach the file
	ctx := context.Background()
	attachment, err := service.Attach(ctx, "az-123", testFile)
	if err != nil {
		t.Fatalf("failed to attach file: %v", err)
	}

	// Verify attachment
	if attachment.IssueID != "az-123" {
		t.Errorf("expected issue_id to be az-123, got %s", attachment.IssueID)
	}

	if attachment.MimeType != "image/png" {
		t.Errorf("expected mime type to be image/png, got %s", attachment.MimeType)
	}

	if attachment.Size != int64(len(testData)) {
		t.Errorf("expected size to be %d, got %d", len(testData), attachment.Size)
	}

	// Verify file was copied
	if _, err := os.Stat(attachment.Path); os.IsNotExist(err) {
		t.Errorf("attachment file does not exist at %s", attachment.Path)
	}
	if !strings.Contains(attachment.Relative, ".azedarach/attachments/") {
		t.Fatalf("attachment relative path = %q, want shared attachments path", attachment.Relative)
	}
	if strings.Contains(attachment.Relative, "/az-123/") {
		t.Fatalf("attachment relative path = %q, should not include issue id", attachment.Relative)
	}
}

func TestAttachNonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(issuesPath, logger)

	ctx := context.Background()
	_, err := service.Attach(ctx, "az-123", "/nonexistent/file.png")
	if err == nil {
		t.Fatal("expected error when attaching non-existent file")
	}
}

func TestList(t *testing.T) {
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(issuesPath, logger)

	ctx := context.Background()

	// List when no attachments exist
	attachments, err := service.List(ctx, "az-123")
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}

	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(attachments))
	}

	// Create test files
	testFile1 := filepath.Join(tmpDir, "test1.png")
	testData1 := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(testFile1, testData1, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	testFile2 := filepath.Join(tmpDir, "test2.jpg")
	testData2 := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	if err := os.WriteFile(testFile2, testData2, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Attach files
	if _, err := service.Attach(ctx, "az-123", testFile1); err != nil {
		t.Fatalf("failed to attach file: %v", err)
	}
	if _, err := service.Attach(ctx, "az-123", testFile2); err != nil {
		t.Fatalf("failed to attach file: %v", err)
	}

	// List attachments
	attachments, err = service.List(ctx, "az-123")
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}

	if len(attachments) != 2 {
		t.Errorf("expected 2 attachments, got %d", len(attachments))
	}

	// Verify attachments have correct data
	for _, att := range attachments {
		if att.IssueID != "az-123" {
			t.Errorf("expected issue_id to be az-123, got %s", att.IssueID)
		}

		if att.ID == "" {
			t.Error("expected attachment to have an ID")
		}

		if att.Size == 0 {
			t.Error("expected attachment to have non-zero size")
		}
	}
}

func TestUnifiedServiceMigratesLegacyImageAttachments(t *testing.T) {
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	documentService := NewDocumentService(issuesPath, logger)
	unifiedService := NewUnifiedService(issuesPath, logger)

	db, err := unifiedService.openDB()
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			notes TEXT
		)
	`); err != nil {
		t.Fatalf("failed to create issues table: %v", err)
	}

	legacyImageDir := filepath.Join(issuesPath, "images", "az-123")
	if err := os.MkdirAll(legacyImageDir, 0755); err != nil {
		t.Fatalf("failed to create legacy image dir: %v", err)
	}
	imageData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	legacyImagePath := filepath.Join(legacyImageDir, "legacyid-screenshot.png")
	if err := os.WriteFile(legacyImagePath, imageData, 0644); err != nil {
		t.Fatalf("failed to create legacy image file: %v", err)
	}
	oldRelative := ".azedarach/images/az-123/legacyid-screenshot.png"
	if _, err := db.Exec(`INSERT INTO issues (id, notes) VALUES (?, ?)`, "az-123", "See ["+oldRelative+"]("+oldRelative+")"); err != nil {
		t.Fatalf("failed to seed issue notes: %v", err)
	}

	reportFile := filepath.Join(tmpDir, "report.md")
	if err := os.WriteFile(reportFile, []byte("# Report\n\nsummary"), 0644); err != nil {
		t.Fatalf("failed to create report file: %v", err)
	}
	report, err := documentService.Attach(ctx, "az-123", reportFile)
	if err != nil {
		t.Fatalf("failed to attach document: %v", err)
	}
	if !strings.Contains(report.Relative, ".azedarach/attachments/") {
		t.Fatalf("document relative path = %q, want attachments path", report.Relative)
	}
	if strings.Contains(report.Relative, "/az-123/") {
		t.Fatalf("document relative path = %q, should not include issue id", report.Relative)
	}

	attachments, err := unifiedService.List(ctx, "az-123")
	if err != nil {
		t.Fatalf("failed to list unified attachments: %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("unified attachments = %d, want 2: %+v", len(attachments), attachments)
	}

	seen := map[string]bool{}
	migratedRelative := ""
	for _, att := range attachments {
		switch {
		case att.ID == "legacyid" && strings.Contains(att.Relative, ".azedarach/attachments/") && IsImage(att):
			seen["image"] = true
			migratedRelative = att.Relative
		case strings.Contains(att.Relative, ".azedarach/attachments/"):
			seen["document"] = true
		}
	}
	if !seen["image"] || !seen["document"] {
		t.Fatalf("unified attachments = %+v, want migrated image and document rows", attachments)
	}
	if _, err := os.Stat(legacyImagePath); !os.IsNotExist(err) {
		t.Fatalf("legacy image file should be removed after migration: %v", err)
	}
	if _, err := os.Stat(legacyImageDir); !os.IsNotExist(err) {
		t.Fatalf("legacy image dir should be removed after migration: %v", err)
	}
	var notes string
	if err := db.QueryRow(`SELECT COALESCE(notes, '') FROM issues WHERE id = ?`, "az-123").Scan(&notes); err != nil {
		t.Fatalf("failed to read migrated issue notes: %v", err)
	}
	if !strings.Contains(notes, migratedRelative) || strings.Contains(notes, oldRelative) {
		t.Fatalf("migrated notes = %q, want new relative %q and no old relative %q", notes, migratedRelative, oldRelative)
	}

	if err := unifiedService.Delete(ctx, "az-123", "legacyid"); err != nil {
		t.Fatalf("failed to delete migrated legacy image reference: %v", err)
	}
	attachments, err = unifiedService.List(ctx, "az-123")
	if err != nil {
		t.Fatalf("failed to list after migrated image delete: %v", err)
	}
	for _, att := range attachments {
		if att.ID == "legacyid" {
			t.Fatalf("migrated image reference should be deleted: %+v", attachments)
		}
	}
}

func TestDocumentServiceStoresSharedFileAndLinksMultipleIssues(t *testing.T) {
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewDocumentService(issuesPath, logger)
	ctx := context.Background()

	reportFile := filepath.Join(tmpDir, "report.md")
	if err := os.WriteFile(reportFile, []byte("# Shared Report\n\nsame contents"), 0644); err != nil {
		t.Fatalf("failed to create report file: %v", err)
	}

	first, err := service.Attach(ctx, "az-1", reportFile)
	if err != nil {
		t.Fatalf("failed to attach first issue: %v", err)
	}
	second, err := service.Attach(ctx, "az-2", reportFile)
	if err != nil {
		t.Fatalf("failed to attach second issue: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("attachment ids differ for same content: %q vs %q", first.ID, second.ID)
	}
	if first.Path != second.Path {
		t.Fatalf("attachment paths differ for same content: %q vs %q", first.Path, second.Path)
	}
	if strings.Contains(first.Relative, "/az-1/") || strings.Contains(second.Relative, "/az-2/") {
		t.Fatalf("shared attachment paths should not include issue ids: %q %q", first.Relative, second.Relative)
	}

	for _, issueID := range []string{"az-1", "az-2"} {
		attachments, err := service.List(ctx, issueID)
		if err != nil {
			t.Fatalf("failed to list attachments for %s: %v", issueID, err)
		}
		if len(attachments) != 1 {
			t.Fatalf("attachments for %s = %d, want 1: %+v", issueID, len(attachments), attachments)
		}
		if attachments[0].Path != first.Path {
			t.Fatalf("attachment path for %s = %q, want %q", issueID, attachments[0].Path, first.Path)
		}
	}

	if err := service.Delete(ctx, "az-1", first.ID); err != nil {
		t.Fatalf("failed to remove first issue reference: %v", err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("shared attachment blob should remain after removing one reference: %v", err)
	}
	firstList, err := service.List(ctx, "az-1")
	if err != nil {
		t.Fatalf("failed to list first issue after delete: %v", err)
	}
	if len(firstList) != 0 {
		t.Fatalf("first issue attachments after delete = %+v, want empty", firstList)
	}
	secondList, err := service.List(ctx, "az-2")
	if err != nil {
		t.Fatalf("failed to list second issue after delete: %v", err)
	}
	if len(secondList) != 1 || secondList[0].ID != first.ID {
		t.Fatalf("second issue attachments after first delete = %+v, want shared attachment", secondList)
	}
}

func TestDelete(t *testing.T) {
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(issuesPath, logger)

	ctx := context.Background()

	// Create and attach a test file
	testFile := filepath.Join(tmpDir, "test.png")
	testData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	attachment, err := service.Attach(ctx, "az-123", testFile)
	if err != nil {
		t.Fatalf("failed to attach file: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(attachment.Path); os.IsNotExist(err) {
		t.Fatal("attachment file should exist")
	}

	// Delete attachment
	if err := service.Delete(ctx, "az-123", attachment.ID); err != nil {
		t.Fatalf("failed to delete attachment: %v", err)
	}

	// Shared attachment blobs remain available for any other references.
	if _, err := os.Stat(attachment.Path); err != nil {
		t.Fatalf("shared attachment file should remain after delete: %v", err)
	}

	// List should be empty
	attachments, err := service.List(ctx, "az-123")
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}

	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments after delete, got %d", len(attachments))
	}
}

func TestDeleteNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(issuesPath, logger)

	ctx := context.Background()

	// Try to delete non-existent attachment
	err := service.Delete(ctx, "az-123", "nonexistent")
	if err == nil {
		t.Fatal("expected error when deleting non-existent attachment")
	}
}

func TestGetPath(t *testing.T) {
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(issuesPath, logger)

	path := service.GetPath("az-123", "test.png")
	expected := filepath.Join(issuesPath, "attachments", "test.png")

	if path != expected {
		t.Errorf("expected path to be %s, got %s", expected, path)
	}
}

func TestDetectMimeType(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected string
	}{
		{
			name:     "PNG",
			data:     []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A},
			expected: "image/png",
		},
		{
			name:     "JPEG",
			data:     []byte{0xFF, 0xD8, 0xFF, 0xE0},
			expected: "image/jpeg",
		},
		{
			name:     "GIF89a",
			data:     []byte("GIF89a"),
			expected: "image/gif",
		},
		{
			name:     "GIF87a",
			data:     []byte("GIF87a"),
			expected: "image/gif",
		},
		{
			name:     "WebP",
			data:     []byte("RIFF\x00\x00\x00\x00WEBP"),
			expected: "image/webp",
		},
		{
			name:     "TIFF little-endian",
			data:     []byte{0x49, 0x49, 0x2A, 0x00},
			expected: "image/tiff",
		},
		{
			name:     "BMP",
			data:     []byte{0x42, 0x4D, 0x36, 0x00},
			expected: "image/bmp",
		},
		{
			name:     "Unknown",
			data:     []byte{0x00, 0x01, 0x02, 0x03},
			expected: "application/octet-stream",
		},
		{
			name:     "Too short",
			data:     []byte{0x00},
			expected: "application/octet-stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectMimeType(tt.data, "")
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestDetectMimeTypeMarkdownByFilename(t *testing.T) {
	if got := detectMimeType([]byte("# Report\n\nbody"), "report.md"); got != "text/markdown" {
		t.Fatalf("detectMimeType markdown = %q, want text/markdown", got)
	}
}

func TestMimeTypeToExt(t *testing.T) {
	tests := []struct {
		mimeType string
		expected string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"image/tiff", ".tiff"},
		{"image/bmp", ".bmp"},
		{"application/octet-stream", ".bin"},
		{"unknown/type", ".bin"},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			result := mimeTypeToExt(tt.mimeType)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}
