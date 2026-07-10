package overlay

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	if !strings.Contains(rendered, "Inline image rendering is not available") || !strings.Contains(rendered, "3x2 PNG image") {
		t.Fatalf("preview lines = %q, want image fallback with dimensions", rendered)
	}
}

func TestBuildAttachmentPreviewUsesSafeTerminalImageRenderer(t *testing.T) {
	safeThumbnail := "\x1b[38;2;1;2;3m▀\x1b[0m"
	restore := stubTerminalImageRenderer(t, safeThumbnail, nil)
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

	if preview.terminalImage != safeThumbnail {
		t.Fatalf("terminalImage = %q, want renderer output", preview.terminalImage)
	}
	rendered := strings.Join(preview.lines, "\n")
	if strings.Contains(rendered, "Inline image rendering is not available") {
		t.Fatalf("preview lines = %q, want no fallback error when renderer succeeds", rendered)
	}
	if !strings.Contains(rendered, "3x2 PNG image") {
		t.Fatalf("preview lines = %q, want dimensions", rendered)
	}
}

func TestBuildAttachmentPreviewRejectsGraphicsProtocolOutput(t *testing.T) {
	restore := stubTerminalImageRenderer(t, "\x1b_Gfake-image\x1b\\", nil)
	defer restore()

	path := filepath.Join(t.TempDir(), "screen.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
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
	if preview.terminalImage != "" {
		t.Fatalf("terminalImage = %q, want rejected graphics protocol output", preview.terminalImage)
	}
	rendered := strings.Join(preview.lines, "\n")
	if !strings.Contains(rendered, "Inline image rendering is not available") {
		t.Fatalf("preview lines = %q, want fallback guidance", rendered)
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

func TestRenderAttachmentPreviewBlockIncludesSafeTerminalImageOutput(t *testing.T) {
	safeThumbnail := "\x1b[38;2;1;2;3m▀\x1b[0m"
	block := renderAttachmentPreviewBlock(New(), attachmentPreviewState{
		attachmentID:  "img-1",
		title:         "Image Preview",
		terminalImage: safeThumbnail,
		lines:         []string{"3x2 PNG image"},
	}, 24)

	if !strings.Contains(block, safeThumbnail) {
		t.Fatalf("preview block missing terminal image output: %q", block)
	}
	if !strings.Contains(block, "3x2 PNG image") {
		t.Fatalf("preview block missing fallback metadata: %q", block)
	}
}

func TestRenderAttachmentPreviewBlockRejectsGraphicsProtocolOutput(t *testing.T) {
	block := renderAttachmentPreviewBlock(New(), attachmentPreviewState{
		attachmentID:  "img-1",
		title:         "Image Preview",
		terminalImage: "\x1b_Gfake-image\x1b\\",
		lines:         []string{"3x2 PNG image"},
	}, 24)

	if strings.Contains(block, "\x1b_Gfake-image\x1b\\") {
		t.Fatalf("preview block included unsupported graphics protocol output: %q", block)
	}
	if !strings.Contains(block, "3x2 PNG image") {
		t.Fatalf("preview block missing fallback metadata: %q", block)
	}
}

func TestRenderTerminalImagePreviewUsesHalfblockRenderer(t *testing.T) {
	original := renderHalfblockTerminalImage
	renderHalfblockTerminalImage = func(string, int, int) (string, error) {
		return "halfblock", nil
	}
	defer func() {
		renderHalfblockTerminalImage = original
	}()

	output, err := renderTerminalImagePreviewWithTermimg("/tmp/screen.png", 40, 8)
	if err != nil {
		t.Fatalf("renderTerminalImagePreviewWithTermimg: %v", err)
	}
	if output != "halfblock" {
		t.Fatalf("output = %q, want halfblock renderer output", output)
	}
}

func TestFitImageCellsAccountsForTerminalCellAspectRatio(t *testing.T) {
	tests := []struct {
		name       string
		viewW      int
		viewH      int
		imageW     int
		imageH     int
		wantWidth  int
		wantHeight int
	}{
		{name: "square image", viewW: 80, viewH: 24, imageW: 1000, imageH: 1000, wantWidth: 48, wantHeight: 24},
		{name: "wide image", viewW: 80, viewH: 24, imageW: 1600, imageH: 900, wantWidth: 80, wantHeight: 22},
		{name: "tall image", viewW: 80, viewH: 24, imageW: 900, imageH: 1600, wantWidth: 27, wantHeight: 24},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			width, height := fitImageCells(tt.viewW, tt.viewH, tt.imageW, tt.imageH)
			if width != tt.wantWidth || height != tt.wantHeight {
				t.Fatalf("fitImageCells() = %dx%d, want %dx%d", width, height, tt.wantWidth, tt.wantHeight)
			}
		})
	}
}

func TestTerminalImageRenderersSerializeCapabilityDetection(t *testing.T) {
	t.Setenv("TERMIMG_BYPASS_DETECTION", "halfblocks")
	path := filepath.Join(t.TempDir(), "screen.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		_ = file.Close()
		t.Fatalf("encode png: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close png: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := renderHalfblockImagePreview(path, 20, 8)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, _, _, _, err := renderFullScreenImagePreviewWithTermimg(path, 80, 24)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("render terminal image: %v", err)
		}
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
