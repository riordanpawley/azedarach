package overlay

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

func setupTestAttachmentService(t *testing.T) (*attachment.Service, string, func()) {
	// Create temp directory for test beads
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, ".beads")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	service := attachment.NewService(beadsPath, logger)

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return service, beadsPath, cleanup
}

func createTestImage(t *testing.T, service *attachment.Service, beadID string) *attachment.Attachment {
	ctx := context.Background()

	// Create a small test PNG
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}

	// Create temp file
	tmpFile := filepath.Join(t.TempDir(), "test.png")
	err := os.WriteFile(tmpFile, pngData, 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Attach the file
	img, err := service.Attach(ctx, beadID, tmpFile)
	if err != nil {
		t.Fatalf("Failed to attach test image: %v", err)
	}

	return img
}

func TestImagePreviewOverlay_Init(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	overlay := NewImagePreviewOverlay(beadID, service, 0)

	if overlay == nil {
		t.Fatal("NewImagePreviewOverlay returned nil")
	}

	// Test Init command
	cmd := overlay.Init()
	if cmd == nil {
		t.Error("Init should return load command")
	}

	// Execute the command
	msg := cmd()
	if loadedMsg, ok := msg.(imagesLoadedMsg); ok {
		if loadedMsg.images == nil {
			t.Error("Images should be initialized (even if empty)")
		}
	} else {
		t.Errorf("Expected imagesLoadedMsg, got %T", msg)
	}
}

func TestImagePreviewOverlay_Navigation(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"

	// Create multiple test images
	createTestImage(t, service, beadID)
	createTestImage(t, service, beadID)
	createTestImage(t, service, beadID)

	overlay := NewImagePreviewOverlay(beadID, service, 0)

	// Load images first
	cmd := overlay.Init()
	msg := cmd()
	model, _ := overlay.Update(msg)
	overlay = model.(*ImagePreviewOverlay)

	preview := overlay

	// Should start at index 0
	if preview.currentIndex != 0 {
		t.Errorf("Expected initial index 0, got %d", preview.currentIndex)
	}

	// Test move right
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	preview = model.(*ImagePreviewOverlay)

	if preview.currentIndex != 1 {
		t.Errorf("Expected index 1 after 'l', got %d", preview.currentIndex)
	}

	// Test move left
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	preview = model.(*ImagePreviewOverlay)

	if preview.currentIndex != 0 {
		t.Errorf("Expected index 0 after 'h', got %d", preview.currentIndex)
	}

	// Test bounds - can't go below 0
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	preview = model.(*ImagePreviewOverlay)

	if preview.currentIndex < 0 {
		t.Error("Index should not go below 0")
	}

	// Test go to last
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	preview = model.(*ImagePreviewOverlay)

	if preview.currentIndex != len(preview.images)-1 {
		t.Errorf("Expected last index, got %d", preview.currentIndex)
	}

	// Test go to first
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	preview = model.(*ImagePreviewOverlay)

	if preview.currentIndex != 0 {
		t.Errorf("Expected index 0 after 'g', got %d", preview.currentIndex)
	}
}

func TestImagePreviewOverlay_DeleteConfirmation(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	createTestImage(t, service, beadID)

	overlay := NewImagePreviewOverlay(beadID, service, 0)

	// Load images
	cmd := overlay.Init()
	msg := cmd()
	model, _ := overlay.Update(msg)
	overlay = model.(*ImagePreviewOverlay)

	preview := overlay

	// Press 'd' to trigger delete confirmation
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	preview = model.(*ImagePreviewOverlay)

	if !preview.confirmDelete {
		t.Error("Should enter delete confirmation mode")
	}

	// Press 'n' to cancel
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	preview = model.(*ImagePreviewOverlay)

	if preview.confirmDelete {
		t.Error("Should exit delete confirmation mode after 'n'")
	}

	// Try again with 'y' to confirm
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	preview = model.(*ImagePreviewOverlay)

	model, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if cmd == nil {
		t.Error("Should return delete command after 'y'")
	}

	// Execute delete command
	msg = cmd()
	if _, ok := msg.(imageDeletedMsg); !ok {
		if errMsg, ok := msg.(imagePreviewErrorMsg); ok {
			t.Logf("Delete error (expected in test): %v", errMsg.err)
		} else {
			t.Errorf("Expected imageDeletedMsg or error, got %T", msg)
		}
	}
}

func TestImagePreviewOverlay_CloseOnEscape(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	overlay := NewImagePreviewOverlay(beadID, service, 0)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if cmd == nil {
		t.Error("Expected close command on escape")
	}

	// Execute the command and check it returns CloseOverlayMsg
	msg := cmd()
	if _, ok := msg.(CloseOverlayMsg); !ok {
		t.Errorf("Expected CloseOverlayMsg, got %T", msg)
	}

	// Also test 'q' for close
	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if cmd == nil {
		t.Error("Expected close command on 'q'")
	}

	msg = cmd()
	if _, ok := msg.(CloseOverlayMsg); !ok {
		t.Errorf("Expected CloseOverlayMsg, got %T", msg)
	}
}

func TestImagePreviewOverlay_View(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"

	// Test empty view
	overlay := NewImagePreviewOverlay(beadID, service, 0)
	cmd := overlay.Init()
	msg := cmd()
	model, _ := overlay.Update(msg)
	overlay = model.(*ImagePreviewOverlay)

	view := overlay.View()

	if view == "" {
		t.Error("View should not be empty")
	}

	if !strings.Contains(view, "Image Preview") {
		t.Error("View should contain title")
	}

	// Test view with images (create multiple for navigation hints)
	createTestImage(t, service, beadID)
	createTestImage(t, service, beadID)
	overlay = NewImagePreviewOverlay(beadID, service, 0)
	cmd = overlay.Init()
	msg = cmd()
	model, _ = overlay.Update(msg)
	overlay = model.(*ImagePreviewOverlay)

	view = overlay.View()

	if !strings.Contains(view, "Image 1/2") {
		t.Error("View should contain position indicator showing 1/2")
	}

	if !strings.Contains(view, "Filename:") {
		t.Error("View should contain filename label")
	}

	if !strings.Contains(view, "h/l") || !strings.Contains(view, "Navigate") {
		t.Error("View should contain navigation hints")
	}

	// Test delete confirmation view
	preview := overlay
	preview.confirmDelete = true

	confirmView := overlay.View()

	if !strings.Contains(confirmView, "Confirm Delete") {
		t.Error("Confirm view should contain 'Confirm Delete'")
	}

	if !strings.Contains(confirmView, "[Y] Yes") {
		t.Error("Confirm view should contain yes button")
	}

	if !strings.Contains(confirmView, "[N] No") {
		t.Error("Confirm view should contain no button")
	}
}

func TestImagePreviewOverlay_TitleAndSize(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	overlay := NewImagePreviewOverlay(beadID, service, 0)

	title := overlay.Title()
	if title != "Image Preview" {
		t.Errorf("Expected title 'Image Preview', got '%s'", title)
	}

	width, height := overlay.Size()
	if width <= 0 || height <= 0 {
		t.Errorf("Expected positive dimensions, got %dx%d", width, height)
	}

	// Test confirm mode size
	preview := overlay
	preview.confirmDelete = true

	confWidth, confHeight := overlay.Size()
	if confWidth <= 0 || confHeight <= 0 {
		t.Errorf("Expected positive confirm mode dimensions, got %dx%d", confWidth, confHeight)
	}
}

func TestImagePreviewOverlay_Refresh(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	overlay := NewImagePreviewOverlay(beadID, service, 0)

	// Load initial images
	cmd := overlay.Init()
	msg := cmd()
	model, _ := overlay.Update(msg)
	overlay = model.(*ImagePreviewOverlay)

	// Press 'r' to refresh
	model, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if cmd == nil {
		t.Error("Expected refresh command on 'r'")
	}

	// Execute refresh command
	msg = cmd()
	if _, ok := msg.(imagesLoadedMsg); !ok {
		if _, ok := msg.(imagePreviewErrorMsg); !ok {
			t.Errorf("Expected imagesLoadedMsg or error, got %T", msg)
		}
	}
}

func TestImagePreviewOverlay_EmptyImages(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	overlay := NewImagePreviewOverlay(beadID, service, 0)

	// Load images (should be empty)
	cmd := overlay.Init()
	msg := cmd()
	model, _ := overlay.Update(msg)
	overlay = model.(*ImagePreviewOverlay)

	preview := overlay

	// Test that operations don't crash with empty images
	model, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if cmd != nil {
		t.Error("Navigation should not produce commands with no images")
	}

	model, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd != nil {
		t.Error("Delete should not work with no images")
	}

	// Current index should stay at 0 or be clamped
	preview = model.(*ImagePreviewOverlay)
	if preview.currentIndex < 0 {
		t.Error("Index should not go negative with no images")
	}
}

func TestImagePreviewOverlay_LoadedMessage(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	createTestImage(t, service, beadID)

	overlay := NewImagePreviewOverlay(beadID, service, 0)

	// Test that imagesLoadedMsg updates the overlay
	ctx := context.Background()
	images, _ := service.List(ctx, beadID)

	msg := imagesLoadedMsg{images: images}
	model, _ := overlay.Update(msg)

	preview := model.(*ImagePreviewOverlay)

	if len(preview.images) != len(images) {
		t.Errorf("Expected %d images, got %d", len(images), len(preview.images))
	}
}

func TestImagePreviewOverlay_ErrorHandling(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	overlay := NewImagePreviewOverlay(beadID, service, 0)

	// Test error message handling
	errMsg := imagePreviewErrorMsg{err: os.ErrNotExist}
	model, _ := overlay.Update(errMsg)

	preview := model.(*ImagePreviewOverlay)

	if preview.error == "" {
		t.Error("Error should be set after error message")
	}

	if !strings.Contains(preview.error, "does not exist") && !strings.Contains(preview.error, "not exist") {
		t.Errorf("Error message should contain file not found info, got: %s", preview.error)
	}

	// Confirm mode should be reset on error
	preview.confirmDelete = true
	model, _ = overlay.Update(errMsg)
	preview = model.(*ImagePreviewOverlay)

	if preview.confirmDelete {
		t.Error("Confirm mode should be reset on error")
	}
}

func TestImagePreviewOverlay_IndexClamping(t *testing.T) {
	service, _, cleanup := setupTestAttachmentService(t)
	defer cleanup()

	beadID := "test-bead"
	createTestImage(t, service, beadID)

	// Start with an out-of-bounds index
	overlay := NewImagePreviewOverlay(beadID, service, 10)

	// Load images should clamp the index
	cmd := overlay.Init()
	msg := cmd()
	model, _ := overlay.Update(msg)

	preview := model.(*ImagePreviewOverlay)

	if len(preview.images) > 0 && preview.currentIndex >= len(preview.images) {
		t.Errorf("Index should be clamped to valid range, got %d for %d images",
			preview.currentIndex, len(preview.images))
	}

	// Test negative index
	overlay = NewImagePreviewOverlay(beadID, service, -5)
	cmd = overlay.Init()
	msg = cmd()
	model, _ = overlay.Update(msg)

	preview = model.(*ImagePreviewOverlay)

	if len(preview.images) > 0 && preview.currentIndex < 0 {
		t.Errorf("Negative index should be clamped to 0, got %d", preview.currentIndex)
	}
}
