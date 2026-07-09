package overlay

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	termimg "github.com/blacktop/go-termimg"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

func TestBuildAttachmentPreviewRendersMarkdownSnippet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(path, []byte("# Report\n\n- first finding\n- second finding\n"), 0o644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}

	preview, err := buildAttachmentPreview(attachment.Attachment{
		ID:       "doc-1",
		Filename: "report.md",
		Path:     path,
		MimeType: "text/markdown",
		Size:     39,
		Created:  time.Now(),
	})
	if err != nil {
		t.Fatalf("buildAttachmentPreview markdown: %v", err)
	}

	rendered := strings.Join(preview.lines, "\n")
	if preview.title != "Markdown Preview" {
		t.Fatalf("preview title = %q, want Markdown Preview", preview.title)
	}
	if !strings.Contains(rendered, "Report") || !strings.Contains(rendered, "first finding") {
		t.Fatalf("preview lines = %q, want rendered markdown snippet", rendered)
	}
}

func TestBuildAttachmentPreviewShowsImageFallbackAndDimensions(t *testing.T) {
	restore := stubTerminalImageRenderer(t, "", errTestTerminalImageUnsupported)
	defer restore()

	path := filepath.Join(t.TempDir(), "screen.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatalf("encode png: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close png: %v", err)
	}

	preview, err := buildAttachmentPreview(attachment.Attachment{
		ID:       "img-1",
		Filename: "screen.png",
		Path:     path,
		Size:     80,
		Created:  time.Now(),
	})
	if err != nil {
		t.Fatalf("buildAttachmentPreview image: %v", err)
	}

	rendered := strings.Join(preview.lines, "\n")
	if preview.title != "Image Preview" {
		t.Fatalf("preview title = %q, want Image Preview", preview.title)
	}
	if preview.terminalImage != "" {
		t.Fatalf("terminalImage = %q, want empty fallback", preview.terminalImage)
	}
	if !strings.Contains(rendered, "Terminal image render unavailable") || !strings.Contains(rendered, "3x2 PNG image") {
		t.Fatalf("preview lines = %q, want image fallback with dimensions", rendered)
	}
}

func TestBuildAttachmentPreviewUsesTerminalImageRenderer(t *testing.T) {
	restore := stubTerminalImageRenderer(t, "\x1b_Gfake-image\x1b\\", nil)
	defer restore()

	path := filepath.Join(t.TempDir(), "screen.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatalf("encode png: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close png: %v", err)
	}

	preview, err := buildAttachmentPreview(attachment.Attachment{
		ID:       "img-1",
		Filename: "screen.png",
		Path:     path,
		Size:     80,
		Created:  time.Now(),
	})
	if err != nil {
		t.Fatalf("buildAttachmentPreview image: %v", err)
	}

	if preview.terminalImage != "\x1b_Gfake-image\x1b\\" {
		t.Fatalf("terminalImage = %q, want renderer output", preview.terminalImage)
	}
	rendered := strings.Join(preview.lines, "\n")
	if strings.Contains(rendered, "Terminal image render unavailable") {
		t.Fatalf("preview lines = %q, want no fallback error when renderer succeeds", rendered)
	}
	if !strings.Contains(rendered, "3x2 PNG image") {
		t.Fatalf("preview lines = %q, want dimensions", rendered)
	}
}

func TestRenderAttachmentPreviewBlockWrapsWithinWidth(t *testing.T) {
	block := renderAttachmentPreviewBlock(New(), attachmentPreviewState{
		attachmentID: "doc-1",
		title:        "Document Preview",
		lines:        []string{"This is a long preview line that should wrap within the requested preview width."},
	}, 24)

	for _, line := range strings.Split(block, "\n") {
		if ansi.StringWidth(line) > 30 {
			t.Fatalf("preview line too wide: %q", line)
		}
	}
}

func TestRenderAttachmentPreviewBlockIncludesTerminalImageOutput(t *testing.T) {
	block := renderAttachmentPreviewBlock(New(), attachmentPreviewState{
		attachmentID:  "img-1",
		title:         "Image Preview",
		terminalImage: "\x1b_Gfake-image\x1b\\",
		lines:         []string{"3x2 PNG image"},
	}, 24)

	if !strings.Contains(block, "\x1b_Gfake-image\x1b\\") {
		t.Fatalf("preview block missing terminal image output: %q", block)
	}
	if !strings.Contains(block, "3x2 PNG image") {
		t.Fatalf("preview block missing fallback metadata: %q", block)
	}
}

func TestRenderTerminalImagePreviewSelectsKittyVirtualRenderer(t *testing.T) {
	restore := stubTerminalImageStrategy(t, termimg.Kitty, "kitty-virtual", "halfblock")
	defer restore()

	output, err := renderTerminalImagePreviewWithTermimg("/tmp/screen.png", 40, 8)
	if err != nil {
		t.Fatalf("renderTerminalImagePreviewWithTermimg: %v", err)
	}
	if output != "kitty-virtual" {
		t.Fatalf("output = %q, want kitty virtual renderer output", output)
	}
}

func TestRenderTerminalImagePreviewSelectsHalfblocksForNonKitty(t *testing.T) {
	restore := stubTerminalImageStrategy(t, termimg.Sixel, "kitty-virtual", "halfblock")
	defer restore()

	output, err := renderTerminalImagePreviewWithTermimg("/tmp/screen.png", 40, 8)
	if err != nil {
		t.Fatalf("renderTerminalImagePreviewWithTermimg: %v", err)
	}
	if output != "halfblock" {
		t.Fatalf("output = %q, want halfblock renderer output", output)
	}
}

func TestFitTerminalImageCellsStaysWithinBounds(t *testing.T) {
	tests := []struct {
		name     string
		viewW    int
		viewH    int
		imgW     int
		imgH     int
		wantMaxW int
		wantMaxH int
	}{
		{name: "normal", viewW: 40, viewH: 8, imgW: 800, imgH: 600, wantMaxW: 40, wantMaxH: 8},
		{name: "panorama", viewW: 40, viewH: 8, imgW: 4000, imgH: 100, wantMaxW: 40, wantMaxH: 8},
		{name: "tower", viewW: 40, viewH: 8, imgW: 100, imgH: 4000, wantMaxW: 40, wantMaxH: 8},
		{name: "missing bounds", viewW: 40, viewH: 8, imgW: 0, imgH: 0, wantMaxW: 40, wantMaxH: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotW, gotH := fitTerminalImageCells(tt.viewW, tt.viewH, tt.imgW, tt.imgH)
			if gotW < 1 || gotH < 1 {
				t.Fatalf("fitTerminalImageCells() = %dx%d, want both dimensions >= 1", gotW, gotH)
			}
			if gotW > tt.wantMaxW || gotH > tt.wantMaxH {
				t.Fatalf("fitTerminalImageCells() = %dx%d, want <= %dx%d", gotW, gotH, tt.wantMaxW, tt.wantMaxH)
			}
		})
	}
}

var errTestTerminalImageUnsupported = testTerminalImageUnsupportedError{}

type testTerminalImageUnsupportedError struct{}

func (testTerminalImageUnsupportedError) Error() string {
	return "unsupported terminal"
}

func stubTerminalImageRenderer(t *testing.T, output string, err error) func() {
	t.Helper()
	original := renderTerminalImagePreview
	renderTerminalImagePreview = func(string, int, int) (string, error) {
		return output, err
	}
	return func() {
		renderTerminalImagePreview = original
	}
}

func stubTerminalImageStrategy(t *testing.T, protocol termimg.Protocol, kittyOutput, halfblockOutput string) func() {
	t.Helper()
	originalDetect := detectTerminalImageProtocol
	originalKitty := renderKittyTerminalImage
	originalHalfblock := renderHalfblockTerminalImage
	detectTerminalImageProtocol = func() termimg.Protocol {
		return protocol
	}
	renderKittyTerminalImage = func(string, int, int) (string, error) {
		return kittyOutput, nil
	}
	renderHalfblockTerminalImage = func(string, int, int) (string, error) {
		return halfblockOutput, nil
	}
	return func() {
		detectTerminalImageProtocol = originalDetect
		renderKittyTerminalImage = originalKitty
		renderHalfblockTerminalImage = originalHalfblock
	}
}
