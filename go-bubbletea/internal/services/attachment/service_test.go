package attachment

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestNewService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService("/tmp/beads", logger)

	if service == nil {
		t.Fatal("expected service to be created")
	}

	if service.beadsPath != "/tmp/beads" {
		t.Errorf("expected beadsPath to be /tmp/beads, got %s", service.beadsPath)
	}
}

func TestAttach(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(beadsPath, logger)

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
	if attachment.BeadID != "az-123" {
		t.Errorf("expected bead_id to be az-123, got %s", attachment.BeadID)
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
}

func TestAttachNonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(beadsPath, logger)

	ctx := context.Background()
	_, err := service.Attach(ctx, "az-123", "/nonexistent/file.png")
	if err == nil {
		t.Fatal("expected error when attaching non-existent file")
	}
}

func TestList(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(beadsPath, logger)

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
		if att.BeadID != "az-123" {
			t.Errorf("expected bead_id to be az-123, got %s", att.BeadID)
		}

		if att.ID == "" {
			t.Error("expected attachment to have an ID")
		}

		if att.Size == 0 {
			t.Error("expected attachment to have non-zero size")
		}
	}
}

func TestDelete(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(beadsPath, logger)

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

	// Verify file is deleted
	if _, err := os.Stat(attachment.Path); !os.IsNotExist(err) {
		t.Error("attachment file should be deleted")
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
	beadsPath := filepath.Join(tmpDir, "beads")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(beadsPath, logger)

	ctx := context.Background()

	// Try to delete non-existent attachment
	err := service.Delete(ctx, "az-123", "nonexistent")
	if err == nil {
		t.Fatal("expected error when deleting non-existent attachment")
	}
}

func TestGetPath(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := NewService(beadsPath, logger)

	path := service.GetPath("az-123", "test.png")
	expected := filepath.Join(beadsPath, "images", "az-123", "test.png")

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
			result := detectMimeType(tt.data)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
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

func TestGenerateID(t *testing.T) {
	id1 := generateID()
	id2 := generateID()

	if id1 == "" {
		t.Error("expected non-empty ID")
	}

	if id2 == "" {
		t.Error("expected non-empty ID")
	}

	// IDs should be different (with extremely high probability)
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
}
