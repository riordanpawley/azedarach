package overlay

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

type mockClipboardAttachService struct {
	attachments []attachment.Attachment
	attachCalls int
}

func (m *mockClipboardAttachService) List(context.Context, string) ([]attachment.Attachment, error) {
	return m.attachments, nil
}

func (m *mockClipboardAttachService) AttachFromClipboard(context.Context, string) (*attachment.Attachment, error) {
	m.attachCalls++
	attached := &attachment.Attachment{
		ID:       "from-clipboard",
		IssueID:  "az-123",
		Filename: "clipboard.png",
	}
	m.attachments = append(m.attachments, *attached)
	return attached, nil
}

func (m *mockClipboardAttachService) Attach(context.Context, string, string) (*attachment.Attachment, error) {
	return nil, nil
}

func (m *mockClipboardAttachService) Delete(context.Context, string, string) error {
	return nil
}

func TestNewImageAttachOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	if overlay == nil {
		t.Fatal("expected overlay to be created")
	}

	if overlay.issueID != "az-123" {
		t.Errorf("expected issue_id to be az-123, got %s", overlay.issueID)
	}

	if overlay.mode != imageAttachModeList {
		t.Errorf("expected mode to be imageAttachModeList, got %v", overlay.mode)
	}

	if overlay.cursor != 0 {
		t.Errorf("expected cursor to be 0, got %d", overlay.cursor)
	}

	if overlay.inputActive {
		t.Error("expected inputActive to be false")
	}
}

func TestImageAttachOverlay_Title(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	title := overlay.Title()
	if title != "Image Attachments" {
		t.Errorf("expected title to be 'Image Attachments', got %s", title)
	}
}

func TestImageAttachOverlay_Size(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	width, height := overlay.Size()
	if width != 80 {
		t.Errorf("expected width to be 80, got %d", width)
	}

	if height != 30 {
		t.Errorf("expected height to be 30, got %d", height)
	}
}

func TestImageAttachOverlay_NavigationKeys(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Set up some mock files
	overlay.files = []attachment.Attachment{
		{ID: "1", IssueID: "az-123", Filename: "file1.png"},
		{ID: "2", IssueID: "az-123", Filename: "file2.png"},
		{ID: "3", IssueID: "az-123", Filename: "file3.png"},
	}

	// Test down key
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	overlay.Update(msg)
	if overlay.cursor != 1 {
		t.Errorf("expected cursor to be 1 after down, got %d", overlay.cursor)
	}

	// Test down key again
	overlay.Update(msg)
	if overlay.cursor != 2 {
		t.Errorf("expected cursor to be 2 after down, got %d", overlay.cursor)
	}

	// Test down key at end (should stay at end)
	overlay.Update(msg)
	if overlay.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", overlay.cursor)
	}

	// Test up key
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	overlay.Update(msg)
	if overlay.cursor != 1 {
		t.Errorf("expected cursor to be 1 after up, got %d", overlay.cursor)
	}

	// Test up key again
	overlay.Update(msg)
	if overlay.cursor != 0 {
		t.Errorf("expected cursor to be 0 after up, got %d", overlay.cursor)
	}

	// Test up key at start (should stay at start)
	overlay.Update(msg)
	if overlay.cursor != 0 {
		t.Errorf("expected cursor to stay at 0, got %d", overlay.cursor)
	}
}

func TestImageAttachOverlay_EscapeKey(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Test escape in list mode
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	_, cmd := overlay.Update(msg)

	// Should return CloseOverlayMsg
	if cmd == nil {
		t.Fatal("expected command to be returned")
	}

	result := cmd()
	if _, ok := result.(CloseOverlayMsg); !ok {
		t.Errorf("expected CloseOverlayMsg, got %T", result)
	}
}

func TestImageAttachOverlay_DeleteKeyAliasX(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	testFile := filepath.Join(tmpDir, "test.png")
	testData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	ctx := context.Background()
	attached, err := service.Attach(ctx, "az-123", testFile)
	if err != nil {
		t.Fatalf("failed to attach file: %v", err)
	}

	files, err := service.List(ctx, "az-123")
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}
	overlay.files = files

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
	_, cmd := overlay.Update(msg)
	if cmd == nil {
		t.Fatal("expected delete command from x key")
	}

	result := cmd()
	if _, ok := result.(attachmentDeletedMsg); !ok {
		t.Fatalf("expected attachmentDeletedMsg, got %T", result)
	}

	remaining, err := service.List(ctx, "az-123")
	if err != nil {
		t.Fatalf("failed to list attachments after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected attachment to be deleted, still found %d attachments", len(remaining))
	}

	if attached.ID == "" {
		t.Fatal("expected attached image to have an ID")
	}
}

func TestImageAttachOverlay_OpenPreviewOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Set up some mock files
	overlay.files = []attachment.Attachment{
		{ID: "1", IssueID: "az-123", Filename: "file1.png"},
	}

	// Test enter sends OpenImagePreviewMsg
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	_, cmd := overlay.Update(msg)
	if cmd == nil {
		t.Error("expected command to open image preview, got nil")
		return
	}

	// Execute the command and check the message type
	resultMsg := cmd()
	if _, ok := resultMsg.(OpenImagePreviewMsg); !ok {
		t.Errorf("expected OpenImagePreviewMsg, got %T", resultMsg)
	}

	// Test v also sends OpenImagePreviewMsg
	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd == nil {
		t.Error("expected command to open image preview via v, got nil")
		return
	}
	resultMsg = cmd()
	if _, ok := resultMsg.(OpenImagePreviewMsg); !ok {
		t.Errorf("expected OpenImagePreviewMsg via v, got %T", resultMsg)
	}

	overlay.mode = imageAttachModePreview
	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected command to close overlay from preview, got nil")
	}

	closeMsg := cmd()
	if _, ok := closeMsg.(CloseOverlayMsg); !ok {
		t.Errorf("expected CloseOverlayMsg from preview q, got %T", closeMsg)
	}
}

func TestImageAttachOverlay_PasteKeyPTriggersClipboardAttach(t *testing.T) {
	service := &mockClipboardAttachService{}
	overlay := NewImageAttachOverlay("az-123", service)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if cmd == nil {
		t.Fatal("expected paste command for key p")
	}
	msg := cmd()
	if _, ok := msg.(attachmentAddedMsg); !ok {
		t.Fatalf("expected attachmentAddedMsg for key p, got %T", msg)
	}

	if service.attachCalls != 1 {
		t.Fatalf("attach calls = %d, want 1", service.attachCalls)
	}
}

func TestImageAttachOverlay_FileInputMode(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Test 'f' key to activate file input
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}
	overlay.Update(msg)
	if !overlay.inputActive {
		t.Error("expected inputActive to be true")
	}

	// Test escape to deactivate file input
	msg = tea.KeyMsg{Type: tea.KeyEsc}
	overlay.Update(msg)
	if overlay.inputActive {
		t.Error("expected inputActive to be false")
	}
}

func TestImageAttachOverlay_AttachmentsLoadedMsg(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Simulate attachments loaded message
	files := []attachment.Attachment{
		{ID: "1", IssueID: "az-123", Filename: "file1.png"},
		{ID: "2", IssueID: "az-123", Filename: "file2.png"},
	}

	msg := attachmentsLoadedMsg{attachments: files}
	overlay.Update(msg)

	if len(overlay.files) != 2 {
		t.Errorf("expected 2 files, got %d", len(overlay.files))
	}

	if overlay.error != "" {
		t.Errorf("expected no error, got %s", overlay.error)
	}
}

func TestImageAttachOverlay_ErrorMsg(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Simulate error message
	msg := errorMsg{err: os.ErrNotExist}
	overlay.Update(msg)

	if overlay.error == "" {
		t.Error("expected error to be set")
	}
}

func TestImageAttachOverlay_View(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(tmpDir, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Test view in list mode
	view := overlay.View()
	if view == "" {
		t.Error("expected non-empty view")
	}

	// Test view with files
	overlay.files = []attachment.Attachment{
		{ID: "1", IssueID: "az-123", Filename: "file1.png", Size: 1024},
	}
	view = overlay.View()
	if view == "" {
		t.Error("expected non-empty view")
	}

	// Test view in preview mode
	overlay.mode = imageAttachModePreview
	view = overlay.View()
	if view == "" {
		t.Error("expected non-empty view")
	}

	// Test view in input mode
	overlay.mode = imageAttachModeList
	overlay.inputActive = true
	view = overlay.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		name     string
		size     int64
		expected string
	}{
		{"Bytes", 512, "512 B"},
		{"KB", 1024, "1.00 KB"},
		{"KB decimal", 2048, "2.00 KB"},
		{"MB", 1048576, "1.00 MB"},
		{"MB decimal", 2097152, "2.00 MB"},
		{"GB", 1073741824, "1.00 GB"},
		{"GB decimal", 2147483648, "2.00 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatFileSize(tt.size)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"Short string", "hello", 10, "hello"},
		{"Exact length", "hello", 5, "hello"},
		{"Long string", "hello world", 8, "hello..."},
		{"Long string 2", "this is a very long filename.png", 20, "this is a very lo..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestImageAttachOverlay_Integration(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	issuesPath := filepath.Join(tmpDir, "issues")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	service := attachment.NewService(issuesPath, logger)

	overlay := NewImageAttachOverlay("az-123", service)

	// Initialize
	cmd := overlay.Init()
	if cmd == nil {
		t.Fatal("expected Init to return a command")
	}

	// Simulate loading attachments (empty list initially)
	msg := cmd()
	if _, ok := msg.(attachmentsLoadedMsg); !ok {
		// Init might return the load command, execute it
		if cmd != nil {
			msg = cmd()
		}
	}

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.png")
	testData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Verify the overlay can be rendered
	view := overlay.View()
	if view == "" {
		t.Error("expected non-empty view")
	}

	// Test navigation
	overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})

	// Test mode switching
	overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if !overlay.inputActive {
		t.Error("expected input mode to be active")
	}

	overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if overlay.inputActive {
		t.Error("expected input mode to be inactive")
	}
}
