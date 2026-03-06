package attachment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Service manages image attachments for linear
type Service struct {
	issuesPath string
	logger     *slog.Logger
}

var attachmentIDPattern = regexp.MustCompile(`^[a-f0-9]{8}$`)

// Attachment represents a file attachment
type Attachment struct {
	ID       string    `json:"id"`
	IssueID  string    `json:"issue_id"`
	Filename string    `json:"filename"`
	Path     string    `json:"path"`
	MimeType string    `json:"mime_type"`
	Size     int64     `json:"size"`
	Created  time.Time `json:"created"`
	Corrupt  bool      `json:"corrupt,omitempty"`
}

// NewService creates a new attachment service
func NewService(issuesPath string, logger *slog.Logger) *Service {
	return &Service{
		issuesPath: issuesPath,
		logger:     logger,
	}
}

// Attach copies a file from sourcePath to the linear images directory
func (s *Service) Attach(ctx context.Context, issueID string, sourcePath string) (*Attachment, error) {
	s.logger.Debug("attaching file", "issue_id", issueID, "source", sourcePath)

	// Verify source file exists
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source file not found: %w", err)
	}

	// Open source file
	src, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open source file: %w", err)
	}
	defer src.Close()

	// Read file content
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}

	// Get base filename
	filename := filepath.Base(sourcePath)

	// Create attachment
	return s.createAttachment(ctx, issueID, filename, data, info.Size())
}

// AttachFromClipboard reads an image from the clipboard and attaches it
func (s *Service) AttachFromClipboard(ctx context.Context, issueID string) (*Attachment, error) {
	s.logger.Debug("attaching from clipboard", "issue_id", issueID)

	// Read image from clipboard
	data, err := ReadImageFromClipboard(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read clipboard: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("clipboard is empty or does not contain an image")
	}

	// Determine mime type from data
	mimeType := detectMimeType(data)
	ext := mimeTypeToExt(mimeType)

	// Generate filename with timestamp
	filename := fmt.Sprintf("clipboard-%s%s", time.Now().Format("20060102-150405"), ext)

	return s.createAttachment(ctx, issueID, filename, data, int64(len(data)))
}

// List returns all attachments for a given issue
func (s *Service) List(ctx context.Context, issueID string) ([]Attachment, error) {
	s.logger.Debug("listing attachments", "issue_id", issueID)

	imagesDir := s.getImagesDir(issueID)

	// Check if directory exists
	if _, err := os.Stat(imagesDir); os.IsNotExist(err) {
		return []Attachment{}, nil
	}

	// Read directory entries
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read images directory: %w", err)
	}

	attachments := make([]Attachment, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			s.logger.Warn("failed to get file info", "file", entry.Name(), "error", err)
			continue
		}

		fullPath := filepath.Join(imagesDir, entry.Name())
		mimeType := detectMimeTypeFromFile(fullPath)

		// Parse ID from filename (format: <id>-<original-name>)
		id := ""
		corrupt := false
		parts := strings.SplitN(entry.Name(), "-", 2)
		if len(parts) == 2 && attachmentIDPattern.MatchString(parts[0]) {
			id = parts[0]
		} else {
			corrupt = true
			s.logger.Warn("corrupt attachment metadata: expected <id>-<filename>", "issue_id", issueID, "filename", entry.Name())
		}

		attachments = append(attachments, Attachment{
			ID:       id,
			IssueID:  issueID,
			Filename: entry.Name(),
			Path:     fullPath,
			MimeType: mimeType,
			Size:     info.Size(),
			Created:  info.ModTime(),
			Corrupt:  corrupt,
		})
	}

	s.logger.Debug("found attachments", "count", len(attachments))
	return attachments, nil
}

// Delete removes an attachment by ID
func (s *Service) Delete(ctx context.Context, issueID, attachmentID string) error {
	s.logger.Debug("deleting attachment", "issue_id", issueID, "attachment_id", attachmentID)
	if strings.TrimSpace(attachmentID) == "" {
		return fmt.Errorf("attachment id is required")
	}

	imagesDir := s.getImagesDir(issueID)

	// Find the file with this ID prefix
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return fmt.Errorf("failed to read images directory: %w", err)
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), attachmentID+"-") {
			filePath := filepath.Join(imagesDir, entry.Name())
			if err := os.Remove(filePath); err != nil {
				return fmt.Errorf("failed to delete file: %w", err)
			}
			s.logger.Debug("attachment deleted", "file", entry.Name())
			return nil
		}
	}

	return fmt.Errorf("attachment not found: %s", attachmentID)
}

// DeleteByFilename removes an attachment by exact filename.
func (s *Service) DeleteByFilename(ctx context.Context, issueID, filename string) error {
	s.logger.Debug("deleting attachment by filename", "issue_id", issueID, "filename", filename)
	if strings.TrimSpace(filename) == "" {
		return fmt.Errorf("attachment filename is required")
	}

	imagesDir := s.getImagesDir(issueID)
	filePath := filepath.Join(imagesDir, filename)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("attachment not found: %s", filename)
		}
		return fmt.Errorf("failed to delete file: %w", err)
	}

	return nil
}

// GetPath returns the full path to an attachment
func (s *Service) GetPath(issueID, filename string) string {
	return filepath.Join(s.getImagesDir(issueID), filename)
}

// createAttachment creates a new attachment file
func (s *Service) createAttachment(ctx context.Context, issueID, filename string, data []byte, size int64) (*Attachment, error) {
	// Ensure images directory exists
	imagesDir := s.getImagesDir(issueID)
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create images directory: %w", err)
	}

	// Generate unique ID
	id := generateID()

	// Create filename with ID prefix
	newFilename := fmt.Sprintf("%s-%s", id, filename)
	destPath := filepath.Join(imagesDir, newFilename)

	// Write file
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write attachment: %w", err)
	}

	mimeType := detectMimeType(data)

	attachment := &Attachment{
		ID:       id,
		IssueID:  issueID,
		Filename: newFilename,
		Path:     destPath,
		MimeType: mimeType,
		Size:     size,
		Created:  time.Now(),
	}

	s.logger.Debug("attachment created", "id", id, "path", destPath)
	return attachment, nil
}

// getImagesDir returns the images directory for a issue
func (s *Service) getImagesDir(issueID string) string {
	return filepath.Join(s.issuesPath, "images", issueID)
}

// generateID generates a random ID for an attachment
func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// detectMimeType detects the MIME type from file data
func detectMimeType(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}

	// PNG signature (8 bytes)
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}

	// JPEG signature (3 bytes)
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// GIF signature (6 bytes)
	if len(data) >= 6 && (string(data[0:6]) == "GIF89a" || string(data[0:6]) == "GIF87a") {
		return "image/gif"
	}

	// WebP signature (12 bytes)
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}

	return "application/octet-stream"
}

// detectMimeTypeFromFile detects MIME type from file path
func detectMimeTypeFromFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		// Try to read file and detect
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return "application/octet-stream"
		}
		return detectMimeType(data)
	}
}

// mimeTypeToExt converts MIME type to file extension
func mimeTypeToExt(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}
